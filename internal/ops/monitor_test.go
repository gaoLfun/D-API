package ops

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type monitorRepository struct {
	upstreams         []core.Upstream
	health            []Health
	events            []Event
	status            string
	balanceTransition core.BalanceTransition
	balanceImmediate  bool
}

type boundedMonitorRepository struct{ upstreams []core.Upstream }

func (r boundedMonitorRepository) ListUpstreams(context.Context) ([]core.Upstream, error) {
	return r.upstreams, nil
}
func (boundedMonitorRepository) SaveHealth(context.Context, int64, Health) (string, error) {
	return "healthy", nil
}
func (boundedMonitorRepository) SaveBalance(context.Context, int64, core.Balance, bool) (core.BalanceTransition, error) {
	return core.BalanceUnchanged, nil
}
func (boundedMonitorRepository) SaveEvent(context.Context, Event) error { return nil }

type boundedMonitorProber struct {
	active int32
	max    int32
}

func (p *boundedMonitorProber) CheckHealth(context.Context, core.Upstream) Health {
	active := atomic.AddInt32(&p.active, 1)
	for {
		previous := atomic.LoadInt32(&p.max)
		if active <= previous || atomic.CompareAndSwapInt32(&p.max, previous, active) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	atomic.AddInt32(&p.active, -1)
	return Health{Status: "healthy", CheckedAt: time.Now()}
}
func (*boundedMonitorProber) CheckBalance(context.Context, core.Upstream) core.Balance {
	return core.Balance{Status: "unknown"}
}

func TestMonitorParallelUsesBoundedWorkers(t *testing.T) {
	upstreams := make([]core.Upstream, 20)
	for i := range upstreams {
		upstreams[i] = core.Upstream{ID: int64(i + 1), Enabled: true, HealthStatus: "healthy"}
	}
	prober := &boundedMonitorProber{}
	monitor := NewMonitor(boundedMonitorRepository{upstreams: upstreams}, prober, nil, MonitorConfig{Concurrency: 3})
	if err := monitor.RunHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&prober.max); got > 3 {
		t.Fatalf("max concurrent probes = %d, want <= 3", got)
	}
}

func (r *monitorRepository) ListUpstreams(context.Context) ([]core.Upstream, error) {
	return r.upstreams, nil
}
func (r *monitorRepository) SaveHealth(_ context.Context, id int64, health Health) (string, error) {
	r.health = append(r.health, health)
	status := r.status
	if status == "" {
		status = health.Status
	}
	for index := range r.upstreams {
		if r.upstreams[index].ID == id {
			r.upstreams[index].HealthStatus = status
		}
	}
	return status, nil
}
func (r *monitorRepository) SaveBalance(_ context.Context, _ int64, _ core.Balance, immediate bool) (core.BalanceTransition, error) {
	r.balanceImmediate = immediate
	return r.balanceTransition, nil
}
func (r *monitorRepository) SaveEvent(_ context.Context, event Event) error {
	r.events = append(r.events, event)
	return nil
}

type monitorProber struct{ health Health }

func (p monitorProber) CheckHealth(context.Context, core.Upstream) Health { return p.health }
func (monitorProber) CheckBalance(context.Context, core.Upstream) core.Balance {
	return core.Balance{Status: "unknown"}
}

func TestMonitorEmitsHealthTransition(t *testing.T) {
	repository := &monitorRepository{upstreams: []core.Upstream{{ID: 1, Name: "primary", Enabled: true, HealthStatus: "healthy"}}}
	prober := monitorProber{health: Health{Status: "unhealthy", CheckedAt: time.Now(), Error: "HTTP 503"}}
	notifications := 0
	monitor := NewMonitor(repository, prober, NotifierFunc(func(context.Context, Event) error {
		notifications++
		return nil
	}), MonitorConfig{Concurrency: 1})

	if err := monitor.RunHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.health) != 1 || len(repository.events) != 1 || repository.events[0].State != "unhealthy" || notifications != 1 {
		t.Fatalf("health=%d events=%#v notifications=%d", len(repository.health), repository.events, notifications)
	}
}

func TestMonitorDoesNotNotifyDegradedHealth(t *testing.T) {
	repository := &monitorRepository{
		upstreams: []core.Upstream{{ID: 1, Name: "primary", Enabled: true, HealthStatus: "healthy"}},
		status:    "degraded",
	}
	prober := monitorProber{health: Health{Status: "unhealthy", CheckedAt: time.Now(), Error: "HTTP 503"}}
	notifications := 0
	monitor := NewMonitor(repository, prober, NotifierFunc(func(context.Context, Event) error {
		notifications++
		return errors.New("degraded notification should not be sent")
	}), MonitorConfig{Concurrency: 1})

	if err := monitor.RunHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.events) != 1 || repository.events[0].State != "degraded" {
		t.Fatalf("events = %#v", repository.events)
	}
	if notifications != 0 {
		t.Fatalf("notifications=%d", notifications)
	}
}

func TestMonitorPublishesBalanceTransition(t *testing.T) {
	repository := &monitorRepository{
		upstreams:         []core.Upstream{{ID: 1, Name: "primary", Enabled: true}},
		balanceTransition: core.BalanceSuspended,
	}
	notifications := 0
	monitor := NewMonitor(repository, monitorProber{}, NotifierFunc(func(_ context.Context, event Event) error {
		notifications++
		if event.Type != "upstream_balance_protection" || event.State != "suspended" {
			t.Fatalf("event = %#v", event)
		}
		return nil
	}), MonitorConfig{Concurrency: 1})

	if err := monitor.RunBalances(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.balanceImmediate || len(repository.events) != 0 || notifications != 1 {
		t.Fatalf("immediate=%t events=%#v notifications=%d", repository.balanceImmediate, repository.events, notifications)
	}
}

func TestMonitorRetriesBalanceTransitionNotification(t *testing.T) {
	repository := &monitorRepository{
		upstreams:         []core.Upstream{{ID: 1, Name: "primary", Enabled: true, HealthStatus: "healthy"}},
		balanceTransition: core.BalanceSuspended,
	}
	attempts := 0
	monitor := NewMonitor(repository, monitorProber{health: Health{Status: "healthy", CheckedAt: time.Now()}}, NotifierFunc(func(context.Context, Event) error {
		attempts++
		if attempts == 1 {
			return errors.New("notification unavailable")
		}
		return nil
	}), MonitorConfig{Concurrency: 1})

	if err := monitor.RunBalances(context.Background()); err == nil {
		t.Fatal("first balance notification should fail")
	}
	repository.balanceTransition = core.BalanceUnchanged
	if err := monitor.RunHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(repository.events) != 0 {
		t.Fatalf("attempts=%d events=%#v", attempts, repository.events)
	}
}

func TestMonitorQueuesNewTransitionBehindFailedNotification(t *testing.T) {
	repository := &monitorRepository{
		upstreams:         []core.Upstream{{ID: 1, Name: "primary", Enabled: true}},
		balanceTransition: core.BalanceSuspended,
	}
	failing := true
	delivered := make([]string, 0, 2)
	monitor := NewMonitor(repository, monitorProber{}, NotifierFunc(func(_ context.Context, event Event) error {
		if failing {
			return errors.New("notification unavailable")
		}
		delivered = append(delivered, event.Type)
		return nil
	}), MonitorConfig{Concurrency: 1})
	monitor.setPending(Event{Type: "upstream_health", UpstreamID: 1})

	if err := monitor.RunBalances(context.Background()); err == nil {
		t.Fatal("pending notification retry should fail")
	}
	if queued := monitor.pending[1]; len(queued) != 2 || queued[0].Type != "upstream_health" || queued[1].Type != "upstream_balance_protection" {
		t.Fatalf("pending events = %#v", queued)
	}

	failing = false
	if err := monitor.retryPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(monitor.pending[1]) != 0 || len(delivered) != 2 || delivered[0] != "upstream_health" || delivered[1] != "upstream_balance_protection" {
		t.Fatalf("delivered=%#v pending=%#v", delivered, monitor.pending[1])
	}
}

func TestMonitorBoundsAndCoalescesPendingTransitions(t *testing.T) {
	monitor := NewMonitor(nil, nil, nil, MonitorConfig{Concurrency: 1})
	for index := 0; index < maxPendingEventsPerUpstream+4; index++ {
		monitor.setPending(Event{Type: "event-" + strconv.Itoa(index), UpstreamID: 1})
	}
	if got := len(monitor.pending[1]); got != maxPendingEventsPerUpstream {
		t.Fatalf("pending length = %d, want %d", got, maxPendingEventsPerUpstream)
	}
	monitor.setPending(Event{Type: "event-" + strconv.Itoa(maxPendingEventsPerUpstream+2), State: "latest", UpstreamID: 1})
	found := false
	for _, event := range monitor.pending[1] {
		if event.Type == "event-"+strconv.Itoa(maxPendingEventsPerUpstream+2) {
			found = true
			if event.State != "latest" {
				t.Fatalf("coalesced state = %q", event.State)
			}
		}
	}
	if !found {
		t.Fatal("coalesced event was dropped")
	}
}
