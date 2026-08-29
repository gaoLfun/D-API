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

type Event struct {
	Type         string    `json:"type"`
	State        string    `json:"state"`
	Previous     string    `json:"previous,omitempty"`
	UpstreamID   int64     `json:"upstream_id,omitempty"`
	UpstreamName string    `json:"upstream_name,omitempty"`
	Message      string    `json:"message"`
	At           time.Time `json:"at"`
}

type Repository interface {
	ListUpstreams(context.Context) ([]core.Upstream, error)
	SaveHealth(context.Context, int64, Health) (string, error)
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
	Repository Repository
	Prober     ProbeService
	Notifier   Notifier
	Config     MonitorConfig
	pendingMu  sync.Mutex
	pending    map[int64][]Event
}

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
	m.runAndLog(ctx, m.RunHealth, "health probe failed")
	m.runAndLog(ctx, m.RunBalances, "balance probe failed")
	healthTicker := time.NewTicker(m.Config.HealthEvery)
	balanceTicker := time.NewTicker(m.Config.BalanceEvery)
	defer healthTicker.Stop()
	defer balanceTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-healthTicker.C:
			m.runAndLog(ctx, m.RunHealth, "health probe failed")
		case <-balanceTicker.C:
			m.runAndLog(ctx, m.RunBalances, "balance probe failed")
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
		status, err := m.Repository.SaveHealth(ctx, upstream.ID, health)
		if err != nil {
			return errors.Join(pendingErr, err)
		}
		if upstream.HealthStatus == "" || upstream.HealthStatus == "unknown" || upstream.HealthStatus == status {
			return pendingErr
		}
		event := Event{
			Type:         "upstream_health",
			State:        status,
			Previous:     upstream.HealthStatus,
			UpstreamID:   upstream.ID,
			UpstreamName: upstream.Name,
			Message:      fmt.Sprintf("上游 %s 状态从 %s 变更为 %s", upstream.Name, upstream.HealthStatus, status),
			At:           health.CheckedAt,
		}
		if err := m.Repository.SaveEvent(ctx, event); err != nil {
			return errors.Join(pendingErr, err)
		}
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
		return pendingErr
	})
}

func (m *Monitor) retryPending(ctx context.Context, upstreamID int64) error {
	if m.Notifier == nil {
		return nil
	}
	m.pendingMu.Lock()
	events := append([]Event(nil), m.pending[upstreamID]...)
	m.pendingMu.Unlock()
	if len(events) == 0 {
		return nil
	}
	for index, event := range events {
		if err := m.Notifier.Notify(ctx, event); err != nil {
			m.ackPending(upstreamID, index)
			return fmt.Errorf("retry notification: %w", err)
		}
	}
	m.ackPending(upstreamID, len(events))
	return nil
}

func (m *Monitor) setPending(event Event) {
	m.pendingMu.Lock()
	m.pending[event.UpstreamID] = append(m.pending[event.UpstreamID], event)
	m.pendingMu.Unlock()
}

func (m *Monitor) ackPending(upstreamID int64, count int) {
	if count == 0 {
		return
	}
	m.pendingMu.Lock()
	events := m.pending[upstreamID]
	if count >= len(events) {
		delete(m.pending, upstreamID)
	} else {
		m.pending[upstreamID] = append([]Event(nil), events[count:]...)
	}
	m.pendingMu.Unlock()
}

func (m *Monitor) RunBalances(ctx context.Context) error {
	upstreams, err := m.Repository.ListUpstreams(ctx)
	if err != nil {
		return err
	}
	return m.parallel(ctx, upstreams, func(ctx context.Context, upstream core.Upstream) error {
		pendingErr := m.retryPending(ctx, upstream.ID)
		balance := m.Prober.CheckBalance(ctx, upstream)
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
	for _, upstream := range upstreams {
		if !upstream.Enabled {
			continue
		}
		select {
		case jobs <- upstream:
		case <-ctx.Done():
			break
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	var joined []error
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
