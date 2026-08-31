package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/gateway"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

type GatewayRepository struct{ Store *store.Store }

func (r GatewayRepository) Authenticate(ctx context.Context, raw string) (core.APIKey, error) {
	key, err := r.Store.AuthenticateAPIKey(ctx, raw)
	if errors.Is(err, store.ErrNotFound) {
		return core.APIKey{}, gateway.ErrInvalidAPIKey
	}
	return key, err
}

func (r GatewayRepository) Candidates(ctx context.Context, groupID int64, protocol, model string) ([]core.Upstream, error) {
	return r.Store.ListRouteUpstreams(ctx, groupID, protocol, model)
}

func (r GatewayRepository) AvailableModels(ctx context.Context, key core.APIKey) ([]string, error) {
	return r.Store.AvailableModels(ctx, key)
}

func (r GatewayRepository) MaxAttempts(ctx context.Context) (int, error) {
	return r.Store.MaxAttempts(ctx)
}

func (r GatewayRepository) RecordRequest(ctx context.Context, entry core.RequestLog) error {
	return r.Store.RecordRequest(ctx, entry)
}

func (r GatewayRepository) MarkUpstreamSuccess(ctx context.Context, id int64) error {
	_, err := r.Store.SaveHealth(ctx, id, true, "", false)
	return err
}

func (r GatewayRepository) MarkUpstreamFailure(ctx context.Context, id int64, status int, reason string) error {
	_, err := r.Store.SaveHealth(ctx, id, false, reason, status == http.StatusUnauthorized || status == http.StatusForbidden)
	return err
}

type OpsRepository struct{ Store *store.Store }

func (r OpsRepository) ListUpstreams(ctx context.Context) ([]core.Upstream, error) {
	return r.Store.ListUpstreams(ctx)
}

func (r OpsRepository) SaveHealth(ctx context.Context, id int64, health ops.Health) (string, error) {
	healthy := health.Status == "healthy"
	status, err := r.Store.SaveHealth(ctx, id, healthy, health.Error, health.StatusCode == http.StatusUnauthorized || health.StatusCode == http.StatusForbidden)
	if err != nil {
		return "", err
	}
	if healthy && health.Models != nil {
		if err := r.Store.SaveDiscoveredModels(ctx, id, health.Models); err != nil {
			return "", err
		}
	}
	return status, nil
}

func (r OpsRepository) SaveBalance(ctx context.Context, id int64, balance core.Balance, immediate bool) (core.BalanceTransition, error) {
	return r.Store.SaveBalance(ctx, id, balance, immediate)
}

func (r OpsRepository) SaveEvent(ctx context.Context, event ops.Event) error {
	var upstreamID *int64
	if event.UpstreamID > 0 {
		upstreamID = &event.UpstreamID
	}
	return r.Store.SaveAlertEvent(ctx, upstreamID, event.Type, event.State, event.Message)
}

type Operations struct {
	Store  *store.Store
	Prober *ops.Prober
}

func (o Operations) Check(ctx context.Context, id int64) (ops.Health, error) {
	upstream, err := o.Store.Upstream(ctx, id)
	if err != nil {
		return ops.Health{}, err
	}
	health := o.Prober.CheckHealth(ctx, upstream.Upstream)
	if _, err := (OpsRepository{Store: o.Store}).SaveHealth(ctx, id, health); err != nil {
		return ops.Health{}, err
	}
	return health, nil
}

func (o Operations) Probe(ctx context.Context, upstream core.Upstream) ops.Health {
	return o.Prober.CheckHealth(ctx, upstream)
}

func (o Operations) TestModel(ctx context.Context, upstream core.Upstream, model string) ops.ModelTest {
	return o.Prober.TestModel(ctx, upstream, model)
}

func (o Operations) Balance(ctx context.Context, id int64) (core.Upstream, core.Balance, core.BalanceTransition, error) {
	upstream, err := o.Store.Upstream(ctx, id)
	if err != nil {
		return core.Upstream{}, core.Balance{}, core.BalanceUnchanged, err
	}
	balance := o.Prober.CheckBalance(ctx, upstream.Upstream)
	transition, err := o.Store.SaveBalance(ctx, id, balance, true)
	if err != nil {
		return core.Upstream{}, core.Balance{}, core.BalanceUnchanged, err
	}
	return upstream.Upstream, balance, transition, nil
}

func (o Operations) Models(ctx context.Context, id int64) ([]string, error) {
	health, err := o.Check(ctx, id)
	return health.Models, err
}

type ChannelNotifier struct{ Store *store.Store }

func (n ChannelNotifier) Notify(ctx context.Context, event ops.Event) error {
	channels, err := n.Store.ListChannels(ctx)
	if err != nil {
		return err
	}
	notifiers := make(ops.MultiNotifier, 0, len(channels))
	var configErrors []error
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		notifier, configErr := n.notifierForChannel(channel)
		if configErr != nil {
			configErrors = append(configErrors, fmt.Errorf("channel %d: %w", channel.ID, configErr))
			continue
		}
		if notifier != nil {
			notifiers = append(notifiers, notifier)
		}
	}
	if err := notifiers.Notify(ctx, event); err != nil {
		configErrors = append(configErrors, err)
	}
	return errors.Join(configErrors...)
}

// NotifyChannel is used by the outbox worker so every channel has an
// independent retry lifecycle. Deleted or disabled channels are complete by
// design: they are no longer a valid destination for an old event.
func (n ChannelNotifier) NotifyChannel(ctx context.Context, channelID int64, event ops.Event) error {
	channel, err := n.Store.ChannelByID(ctx, channelID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !channel.Enabled) {
		return nil
	}
	if err != nil {
		return err
	}
	notifier, err := n.notifierForChannel(channel)
	if err != nil {
		return err
	}
	if notifier == nil {
		return errors.New("notification channel is not configured")
	}
	return notifier.Notify(ctx, event)
}

func (n ChannelNotifier) notifierForChannel(channel store.NotificationChannel) (ops.Notifier, error) {
	switch channel.Kind {
	case "webhook":
		var config struct {
			URL      string            `json:"url"`
			Provider string            `json:"provider"`
			Headers  map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(channel.Config, &config); err != nil || strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("invalid webhook configuration")
		}
		return ops.NewWebhookNotifier(ops.WebhookConfig{URL: config.URL, Provider: config.Provider, Headers: config.Headers}, nil), nil
	case "email":
		var config struct {
			Address     string          `json:"address"`
			SMTPHost    string          `json:"smtp_host"`
			SMTPPort    int             `json:"smtp_port"`
			Username    string          `json:"username"`
			Password    string          `json:"password"`
			From        string          `json:"from"`
			To          json.RawMessage `json:"to"`
			ImplicitTLS bool            `json:"implicit_tls"`
		}
		if err := json.Unmarshal(channel.Config, &config); err != nil {
			return nil, errors.New("invalid email configuration")
		}
		if config.Address == "" && config.SMTPHost != "" {
			config.Address = net.JoinHostPort(config.SMTPHost, strconv.Itoa(config.SMTPPort))
		}
		var recipients []string
		if len(config.To) > 0 && config.To[0] == '[' {
			_ = json.Unmarshal(config.To, &recipients)
		} else {
			var recipientList string
			if json.Unmarshal(config.To, &recipientList) == nil {
				for _, recipient := range strings.Split(recipientList, ",") {
					if recipient = strings.TrimSpace(recipient); recipient != "" {
						recipients = append(recipients, recipient)
					}
				}
			}
		}
		if config.From == "" {
			config.From = config.Username
		}
		if config.From == "" && len(recipients) > 0 {
			config.From = recipients[0]
		}
		if config.Address == "" || config.From == "" || len(recipients) == 0 {
			return nil, errors.New("invalid email configuration")
		}
		return ops.NewSMTPNotifier(ops.SMTPConfig{
			Address: config.Address, Username: config.Username, Password: config.Password,
			From: config.From, To: recipients, ImplicitTLS: config.ImplicitTLS,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported notification channel kind %q", channel.Kind)
	}
}

func NewMonitor(database *store.Store, prober *ops.Prober, healthEvery, balanceEvery time.Duration) *ops.Monitor {
	notifier := ops.NewCooldownNotifier(NewOutboxNotifier(database), 30*time.Minute)
	return ops.NewMonitor(OpsRepository{Store: database}, prober, notifier, ops.MonitorConfig{
		HealthEvery: healthEvery, BalanceEvery: balanceEvery, Concurrency: 8,
	})
}
