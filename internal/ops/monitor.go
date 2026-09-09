package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type MetricEvidence struct {
	FailedRequestIDs  []string         `json:"failed_request_ids"`
	WindowStart       time.Time        `json:"window_start"`
	WindowEnd         time.Time        `json:"window_end"`
	Threshold         float64          `json:"threshold"`
	Attempts          int64            `json:"attempts"`
	Failures          int64            `json:"failures"`
	RecoveredRequests int64            `json:"recovered_requests"`
	StatusCounts      map[string]int64 `json:"status_counts"`
}

type Event struct {
	ActiveSince        *time.Time      `json:"active_since,omitempty"`
	Evidence           *MetricEvidence `json:"evidence,omitempty"`
	NotificationNumber int             `json:"notification_number,omitempty"`

	Type         string    `json:"type"`
	State        string    `json:"state"`
	Previous     string    `json:"previous,omitempty"`
	Severity     string    `json:"severity,omitempty"`
	Count        int       `json:"count,omitempty"`
	UpstreamID   int64     `json:"upstream_id,omitempty"`
	UpstreamName string    `json:"upstream_name,omitempty"`
	Message      string    `json:"message"`
	At           time.Time `json:"at"`
}

type Repository interface {
	ListUpstreams(context.Context) ([]core.Upstream, error)
	SaveHealth(context.Context, int64, Health) (string, string, error)
	AcknowledgeHealthNotification(context.Context, int64, string) error
	SaveBalance(context.Context, int64, core.Balance, bool) (core.BalanceTransition, error)
	SaveEvent(context.Context, Event) error
}

type ProbeService interface {
	CheckHealth(context.Context, core.Upstream) Health
	CheckBalance(context.Context, core.Upstream) core.Balance
}

type MonitorConfig struct {
	HealthEvery  time.Duration
	BalanceEvery time.Duration
	Concurrency  int
}

type Monitor struct {
	Repository      Repository
	Prober          ProbeService
	Notifier        Notifier
	Config          MonitorConfig
	pendingMu       sync.Mutex
	pending         map[int64][]Event
	pendingInFlight map[int64]bool
}

const maxPendingEventsPerUpstream = 16

func NewMonitor(repository Repository, prober ProbeService, notifier Notifier, config MonitorConfig) *Monitor {
	if config.HealthEvery <= 0 {
		config.HealthEvery = 30 * time.Second
	}
	if config.BalanceEvery <= 0 {
		config.BalanceEvery = 10 * time.Minute
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 8
	}
	return &Monitor{Repository: repository, Prober: prober, Notifier: notifier, Config: config, pending: make(map[int64][]Event)}
}

func (m *Monitor) Run(ctx context.Context) error {
	if m.Repository == nil || m.Prober == nil {
		return errors.New("ops monitor is not configured")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.runLoop(ctx, m.Config.BalanceEvery, m.RunBalances, "balance probe failed")
	}()
	m.runLoop(ctx, m.Config.HealthEvery, m.RunHealth, "health probe failed")
	wg.Wait()
	return nil
}

func (m *Monitor) runLoop(ctx context.Context, every time.Duration, run func(context.Context) error, message string) {
	if every <= 0 {
		every = time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.runAndLog(ctx, run, message)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Monitor) RunHealth(ctx context.Context) error {
	upstreams, err := m.Repository.ListUpstreams(ctx)
	if err != nil {
		return err
	}
	return m.parallel(ctx, upstreams, func(ctx context.Context, upstream core.Upstream) error {
		pendingErr := m.retryPending(ctx, upstream.ID)
		health := m.Prober.CheckHealth(ctx, upstream)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, notification, err := m.Repository.SaveHealth(ctx, upstream.ID, health)
		if err != nil {
			return errors.Join(pendingErr, err)
		}
		transitioned := upstream.HealthStatus != "" && upstream.HealthStatus != "unknown" && upstream.HealthStatus != status
		var transitionEvent *Event
		if transitioned {
			event := healthEvent(upstream, upstream.HealthStatus, status, health.CheckedAt)
			if err := m.Repository.SaveEvent(ctx, event); err != nil {
				return errors.Join(pendingErr, err)
			}
			transitionEvent = &event
		}
		if notification == "" {
			return pendingErr
		}
		previous := "healthy"
		if notification == "healthy" {
			previous = "unhealthy"
		}
		event := healthEvent(upstream, previous, notification, health.CheckedAt)
		if transitionEvent != nil && transitionEvent.State == notification {
			event = *transitionEvent
		} else {
			if err := m.Repository.SaveEvent(ctx, event); err != nil {
				return errors.Join(pendingErr, err)
			}
		}
		if m.Notifier != nil {
			if err := m.Notifier.Notify(ctx, event); err != nil {
				return errors.Join(pendingErr, err)
			}
		}
		if err := m.Repository.AcknowledgeHealthNotification(ctx, upstream.ID, notification); err != nil {
			return errors.Join(pendingErr, err)
		}
		return pendingErr
	})
}

func healthEvent(upstream core.Upstream, previous, status string, at time.Time) Event {
	return Event{
		Type: "upstream_health", State: status, Previous: previous,
		UpstreamID: upstream.ID, UpstreamName: upstream.Name,
		Message: fmt.Sprintf("上游 %s 状态从 %s 变更为 %s", upstream.Name, previous, status), At: at,
	}
}

func (m *Monitor) retryPending(ctx context.Context, upstreamID int64) error {
	if m.Notifier == nil {
		return nil
	}
	m.pendingMu.Lock()
	if m.pendingInFlight[upstreamID] {
		m.pendingMu.Unlock()
		return errors.New("pending notification retry in progress")
	}
	events := append([]Event(nil), m.pending[upstreamID]...)
	if len(events) == 0 {
		m.pendingMu.Unlock()
		return nil
	}
	if m.pendingInFlight == nil {
		m.pendingInFlight = make(map[int64]bool)
	}
	m.pendingInFlight[upstreamID] = true
	m.pendingMu.Unlock()
	defer func() {
		m.pendingMu.Lock()
		delete(m.pendingInFlight, upstreamID)
		m.pendingMu.Unlock()
	}()
	for _, event := range events {
		if err := m.Notifier.Notify(ctx, event); err != nil {
			return fmt.Errorf("retry notification: %w", err)
		}
		m.ackPending(event)
	}
	return nil
}

func (m *Monitor) setPending(event Event) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	events := m.pending[event.UpstreamID]
	// Keep only the latest pending transition for each event type.
	for index := range events {
		if events[index].Type == event.Type {
			events[index] = event
			m.pending[event.UpstreamID] = events
			return
		}
	}
	if len(events) >= maxPendingEventsPerUpstream {
		events = events[len(events)-maxPendingEventsPerUpstream+1:]
	}
	m.pending[event.UpstreamID] = append(events, event)
}

func (m *Monitor) ackPending(sent Event) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	events := m.pending[sent.UpstreamID]
	for index, event := range events {
		// Do not acknowledge a newer transition queued during delivery.
		if event == sent {
			events = append(events[:index], events[index+1:]...)
			break
		}
	}
	if len(events) == 0 {
		delete(m.pending, sent.UpstreamID)
	} else {
		m.pending[sent.UpstreamID] = events
	}
}

func (m *Monitor) RunBalances(ctx context.Context) error {
	upstreams, err := m.Repository.ListUpstreams(ctx)
	if err != nil {
		return err
	}
	return m.parallel(ctx, upstreams, func(ctx context.Context, upstream core.Upstream) error {
		pendingErr := m.retryPending(ctx, upstream.ID)
		balance := m.Prober.CheckBalance(ctx, upstream)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		transition, err := m.Repository.SaveBalance(ctx, upstream.ID, balance, false)
		if err != nil {
			return errors.Join(pendingErr, err)
		}
		if transition == core.BalanceUnchanged {
			return pendingErr
		}
		event := BalanceTransitionEvent(upstream, balance, transition)
		if m.Notifier != nil {
			if pendingErr != nil {
				m.setPending(event)
				return pendingErr
			}
			if err := m.Notifier.Notify(ctx, event); err != nil {
				m.setPending(event)
				return err
			}
		}
		return nil
	})
}

func BalanceTransitionEvent(upstream core.Upstream, balance core.Balance, transition core.BalanceTransition) Event {
	at := time.Now()
	if balance.UpdatedAt != nil {
		at = *balance.UpdatedAt
	}
	return Event{
		Type: "upstream_balance_protection", State: string(transition), UpstreamID: upstream.ID,
		UpstreamName: upstream.Name, Message: core.BalanceTransitionMessage(upstream.Name, transition, ""), At: at,
	}
}

func BalanceProtectionDisabledEvent(upstream core.Upstream) Event {
	return Event{
		Type: "upstream_balance_protection", State: string(core.BalanceResumed), UpstreamID: upstream.ID,
		UpstreamName: upstream.Name, Message: core.BalanceTransitionMessage(upstream.Name, core.BalanceResumed, core.BalanceReasonProtectionDisabled), At: time.Now(),
	}
}

func (m *Monitor) parallel(ctx context.Context, upstreams []core.Upstream, work func(context.Context, core.Upstream) error) error {
	limit := m.Config.Concurrency
	if limit <= 0 {
		limit = 8
	}
	errs := make(chan error, len(upstreams))
	jobs := make(chan core.Upstream)
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case upstream, ok := <-jobs:
					if !ok {
						return
					}
					if err := work(ctx, upstream); err != nil {
						errs <- fmt.Errorf("upstream %d: %w", upstream.ID, err)
					}
				}
			}
		}()
	}
dispatch:
	for _, upstream := range upstreams {
		if !upstream.Enabled {
			continue
		}
		select {
		case jobs <- upstream:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	var joined []error
	if ctx.Err() != nil {
		joined = append(joined, ctx.Err())
	}
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (m *Monitor) runAndLog(ctx context.Context, run func(context.Context) error, message string) {
	if err := run(ctx); err != nil && ctx.Err() == nil {
		slog.Error(message, "error", err)
	}
}
