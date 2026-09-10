package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/netguard"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		views = append(views, map[string]any{"id": channel.ID, "name": channel.Name, "kind": channel.Kind, "enabled": channel.Enabled, "configured": len(channel.Config) > 0, "created_at": channel.CreatedAt})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var input store.NotificationChannel
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateChannel(input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel", "通知渠道配置无效")
		return
	}
	id, err := s.store.CreateChannel(r.Context(), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "channel.create", "channel", id, map[string]any{"name": input.Name, "kind": input.Kind})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	channel, err := s.store.ChannelByID(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if channel.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "unsupported_channel_test", "仅支持测试 Webhook 渠道")
		return
	}
	if err := validateChannel(channel); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel", "通知渠道配置无效")
		return
	}
	var config struct {
		URL      string            `json:"url"`
		Provider string            `json:"provider"`
		Headers  map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(channel.Config, &config); err != nil || strings.TrimSpace(config.URL) == "" {
		writeError(w, http.StatusBadRequest, "invalid_channel", "Webhook 配置无效")
		return
	}
	notifier := ops.NewWebhookNotifier(ops.WebhookConfig{URL: config.URL, Provider: config.Provider, Headers: config.Headers}, nil)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := notifier.Notify(ctx, ops.Event{
		Type:    "notification_test",
		State:   "success",
		Message: "D-API Webhook 连通性测试",
		At:      time.Now().UTC(),
	}); err != nil {
		slog.Warn("webhook test failed", "channel_id", id, "provider", config.Provider, "error", err)
		writeError(w, http.StatusBadGateway, "channel_test_failed", "Webhook 测试失败，请检查地址、响应状态和网络策略")
		return
	}
	s.audit(r, "channel.test", "channel", id, map[string]any{"kind": channel.Kind})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Webhook 测试成功"})
}

func validateChannel(channel store.NotificationChannel) error {
	if strings.TrimSpace(channel.Name) == "" || len([]rune(strings.TrimSpace(channel.Name))) > 200 {
		return errors.New("invalid channel name")
	}
	if channel.Kind != "email" && channel.Kind != "webhook" {
		return errors.New("invalid channel kind")
	}
	if len(channel.Config) == 0 || len(channel.Config) > 64<<10 || !json.Valid(channel.Config) {
		return errors.New("invalid channel config")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(channel.Config, &values); err != nil || values == nil || len(values) > 16 {
		return errors.New("invalid channel config")
	}
	if channel.Kind == "webhook" {
		var urlValue string
		if err := json.Unmarshal(values["url"], &urlValue); err != nil || len(urlValue) > 2048 {
			return errors.New("invalid webhook URL")
		}
		var provider string
		if raw, ok := values["provider"]; ok {
			if err := json.Unmarshal(raw, &provider); err != nil || !ops.IsWebhookProvider(provider) {
				return errors.New("invalid webhook provider")
			}
		}
		parsed, err := url.Parse(strings.TrimSpace(urlValue))
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("invalid webhook URL")
		}
		if err := netguard.ValidateURL(urlValue); err != nil {
			return errors.New("invalid webhook URL")
		}
		if raw, ok := values["headers"]; ok {
			var headers map[string]string
			if json.Unmarshal(raw, &headers) != nil || len(headers) > 32 {
				return errors.New("invalid webhook headers")
			}
			for key, value := range headers {
				if strings.TrimSpace(key) == "" || len(key) > 128 || len(value) > 4096 || isHopHeader(key) {
					return errors.New("invalid webhook headers")
				}
			}
		}
		return nil
	}
	var host, address string
	_ = json.Unmarshal(values["smtp_host"], &host)
	_ = json.Unmarshal(values["address"], &address)
	if len(strings.TrimSpace(host)) > 253 || len(strings.TrimSpace(address)) > 512 {
		return errors.New("invalid SMTP host")
	}
	var port int
	if raw, ok := values["smtp_port"]; ok {
		if json.Unmarshal(raw, &port) != nil || port < 1 || port > 65535 {
			return errors.New("invalid SMTP port")
		}
	} else {
		port = 587
	}
	if strings.TrimSpace(address) != "" {
		if err := netguard.ValidateAddress(address); err != nil {
			return errors.New("invalid SMTP address")
		}
	} else if strings.TrimSpace(host) == "" || netguard.ValidateAddress(net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))) != nil {
		return errors.New("invalid SMTP host")
	}
	for _, key := range []string{"username", "password", "from"} {
		if raw, ok := values[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil || len(value) > 1024 {
				return errors.New("invalid SMTP credential")
			}
		}
	}
	rawTo, ok := values["to"]
	if !ok {
		return errors.New("invalid SMTP recipients")
	}
	var recipient string
	if json.Unmarshal(rawTo, &recipient) == nil {
		if strings.TrimSpace(recipient) == "" || len(recipient) > 4096 || strings.ContainsAny(recipient, "\r\n") {
			return errors.New("invalid SMTP recipients")
		}
	} else {
		var recipients []string
		if json.Unmarshal(rawTo, &recipients) != nil || len(recipients) == 0 || len(recipients) > 100 {
			return errors.New("invalid SMTP recipients")
		}
		for _, value := range recipients {
			if strings.TrimSpace(value) == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") {
				return errors.New("invalid SMTP recipients")
			}
		}
	}
	return nil
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "channel.delete", "channel", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var input store.AlertRule
	if err := decodeJSON(w, r, &input); err != nil || !validAlertRule(input, true) {
		writeError(w, http.StatusBadRequest, "invalid_alert_rule", "告警规则无效")
		return
	}
	if input.MaxNotifications == 0 {
		input.MaxNotifications = 3
	}
	id, err := s.store.CreateAlertRule(r.Context(), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.create", "alert_rule", id, map[string]any{"event": input.Event, "upstream_id": input.UpstreamID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.AlertRule
	if err := decodeJSON(w, r, &input); err != nil || !validAlertRule(input, false) {
		writeError(w, http.StatusBadRequest, "invalid_alert_rule", "告警规则无效")
		return
	}
	if input.MaxNotifications == 0 {
		input.MaxNotifications = 3
	}
	input.ID = id
	if err := s.store.UpdateAlertRule(r.Context(), input); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.update", "alert_rule", id, map[string]any{"threshold": input.Threshold, "enabled": input.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.delete", "alert_rule", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notifySecurity(event ops.Event) {
	s.notifyEvent(event)
}

func (s *Server) notifyEvent(event ops.Event) {
	var upstreamID *int64
	if event.UpstreamID > 0 {
		upstreamID = &event.UpstreamID
	}
	if err := s.store.SaveAlertEvent(context.Background(), upstreamID, event.Type, event.State, event.Message); err != nil {
		slog.Error("event write failed", "event", event.Type, "error", err)
	}
	s.notifyPersistedEvent(event)
}

func (s *Server) notifyPersistedEvent(event ops.Event) {
	if s.notifier == nil {
		return
	}
	go func() {
		var lastErr error
		for attempt, delay := 1, time.Second; attempt <= 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := s.notifier.Notify(ctx, event)
			cancel()
			if err == nil {
				return
			}
			lastErr = err
			if attempt < 3 {
				timer := time.NewTimer(delay)
				<-timer.C
				delay *= 2
			}
		}
		slog.Error("event notification failed", "event", event.Type, "attempts", 3, "error", lastErr)
	}()
}

func validAlertRule(rule store.AlertRule, creating bool) bool {
	if creating {
		validEvent := map[string]bool{
			"low_balance": true, "balance_unavailable": true, "error_rate": true,
			"latency": true,
		}
		if !validEvent[rule.Event] || rule.UpstreamID == nil || *rule.UpstreamID <= 0 {
			return false
		}
	}
	return rule.Threshold != nil && *rule.Threshold >= 0 && rule.WindowSeconds >= 60 && rule.WindowSeconds <= 86400 && rule.CooldownSeconds >= 60 && rule.CooldownSeconds <= 604800 && (rule.MaxNotifications == 0 || (rule.MaxNotifications >= 1 && rule.MaxNotifications <= 100))
}

func (s *Server) alertHistory(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListAlertHistory(r.Context(), parseInt(r.URL.Query().Get("offset"), 0))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}
