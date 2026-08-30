package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
)

const (
	EventLowBalance         = "low_balance"
	EventBalanceUnavailable = "balance_unavailable"
	EventErrorRate          = "error_rate"
	EventLatency            = "latency"
	EventClientErrorRate    = "client_error_rate"
	EventLoginFailure       = "login_failure"
	EventNewLoginIP         = "new_login_ip"
)

type Rule struct {
	ID               int64         `json:"id"`
	Event            string        `json:"event"`
	UpstreamID       *int64        `json:"upstream_id,omitempty"`
	Threshold        *float64      `json:"threshold,omitempty"`
	Window           time.Duration `json:"window"`
	Cooldown         time.Duration `json:"cooldown"`
	MaxNotifications int           `json:"max_notifications"`
	Enabled          bool          `json:"enabled"`
}

type Observation struct {
	Key             string  `json:"key"`
	Active          bool    `json:"active"`
	Ignore          bool    `json:"ignore,omitempty"`
	Value           float64 `json:"value"`
	Message         string  `json:"message"`
	RecoveryMessage string  `json:"recovery_message,omitempty"`
	UpstreamID      int64   `json:"upstream_id,omitempty"`
	UpstreamName    string  `json:"upstream_name,omitempty"`
}

type State struct {
	Active            bool       `json:"active"`
	Value             float64    `json:"value"`
	Message           string     `json:"message"`
	LastObservedAt    time.Time  `json:"last_observed_at"`
	LastNotifiedAt    *time.Time `json:"last_notified_at,omitempty"`
	NotificationCount int        `json:"notification_count"`
}

type Repository interface {
	ListRules(context.Context) ([]Rule, error)
	Observe(context.Context, Rule) ([]Observation, error)
	LoadState(context.Context, int64, string) (State, bool, error)
	SaveState(context.Context, int64, string, State) error
	PruneStates(context.Context, int64, []string) error
}

type Engine struct {
	Repository Repository
	Notifier   ops.Notifier
	now        func() time.Time
}

func NewEngine(repository Repository, notifier ops.Notifier) *Engine {
	return &Engine{Repository: repository, Notifier: notifier, now: time.Now}
}

func (e *Engine) Run(ctx context.Context, interval time.Duration) error {
	if e.Repository == nil {
		return errors.New("alert repository is not configured")
	}
	if interval <= 0 {
		return errors.New("alert interval must be positive")
	}
	e.runAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.runAndLog(ctx)
		}
	}
}

func (e *Engine) RunOnce(ctx context.Context) error {
	if e.Repository == nil {
		return errors.New("alert repository is not configured")
	}
	rules, err := e.Repository.ListRules(ctx)
	if err != nil {
		return fmt.Errorf("list alert rules: %w", err)
	}
	var errs []error
	for _, rule := range rules {
		if !rule.Enabled {
			if err := e.Repository.PruneStates(ctx, rule.ID, nil); err != nil {
				errs = append(errs, fmt.Errorf("prune disabled rule %d: %w", rule.ID, err))
			}
			continue
		}
		observations, err := e.Repository.Observe(ctx, rule)
		if err != nil {
			errs = append(errs, fmt.Errorf("observe rule %d: %w", rule.ID, err))
			continue
		}
		keys := make([]string, 0, len(observations))
		for _, observation := range observations {
			keys = append(keys, observation.Key)
			if observation.Ignore {
				continue
			}
			if err := e.handle(ctx, rule, observation); err != nil {
				errs = append(errs, fmt.Errorf("rule %d observation %q: %w", rule.ID, observation.Key, err))
			}
		}
		if err := e.Repository.PruneStates(ctx, rule.ID, keys); err != nil {
			errs = append(errs, fmt.Errorf("prune rule %d: %w", rule.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) handle(ctx context.Context, rule Rule, observation Observation) error {
	if observation.Key == "" {
		return errors.New("observation key is empty")
	}
	state, exists, err := e.Repository.LoadState(ctx, rule.ID, observation.Key)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	now := e.now()
	notify := observation.Active && (!exists || !state.Active)
	if observation.Active && exists && state.Active && rule.Cooldown > 0 && (rule.MaxNotifications <= 0 || state.NotificationCount < rule.MaxNotifications) {
		notify = state.LastNotifiedAt == nil || now.Sub(*state.LastNotifiedAt) >= rule.Cooldown
	}
	resolved := !observation.Active && exists && state.Active
	if observation.Active && (!exists || !state.Active) {
		state.NotificationCount = 0
	}
	if notify || resolved {
		event := ops.Event{
			Type:         rule.Event,
			State:        "firing",
			Previous:     "inactive",
			UpstreamID:   observation.UpstreamID,
			UpstreamName: observation.UpstreamName,
			Message:      observation.Message,
			At:           now,
		}
		if resolved {
			event.State, event.Previous = "resolved", "active"
			if observation.RecoveryMessage != "" {
				event.Message = observation.RecoveryMessage
			}
		}
		var notifyErr error
		if e.Notifier != nil {
			notifyErr = e.Notifier.Notify(ctx, event)
		}
		state.LastNotifiedAt = &now
		if resolved && notifyErr == nil {
			state.NotificationCount = 0
		} else {
			state.NotificationCount++
		}
		if notifyErr != nil {
			// Keep an active state for failed recoveries so the recovery is retried
			// after the cooldown instead of being lost.
			state.Active = true
		} else {
			state.Active = observation.Active
		}
		state.Value = observation.Value
		state.Message = observation.Message
		state.LastObservedAt = now
		if err := e.Repository.SaveState(ctx, rule.ID, observation.Key, state); err != nil {
			if notifyErr != nil {
				return errors.Join(fmt.Errorf("notify: %w", notifyErr), fmt.Errorf("save state: %w", err))
			}
			return fmt.Errorf("save state: %w", err)
		}
		if notifyErr != nil {
			return fmt.Errorf("notify: %w", notifyErr)
		}
		return nil
	}
	state.Active = observation.Active
	state.Value = observation.Value
	state.Message = observation.Message
	state.LastObservedAt = now
	if err := e.Repository.SaveState(ctx, rule.ID, observation.Key, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func (e *Engine) runAndLog(ctx context.Context) {
	if err := e.RunOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Error("alert evaluation failed", "error", err)
	}
}
