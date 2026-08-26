package ops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type monitorRepository struct {
	upstreams []core.Upstream
	health    []Health
	events    []Event
	status    string
}

type boundedMonitorRepository struct{ upstreams []core.Upstream }

func (r boundedMonitorRepository) ListUpstreams(context.Context) ([]core.Upstream, error) {
	return r.upstreams, nil
}
func (boundedMonitorRepository) SaveHealth(context.Context, int64, Health) (string, error) {
	return "healthy", nil
}
func (boundedMonitorRepository) SaveBalance(context.Context, int64, core.Balance) error { return nil }
func (boundedMonitorRepository) SaveEvent(context.Context, Event) error                 { return nil }

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
func (r *monitorRepository) SaveBalance(context.Context, int64, core.Balance) error { return nil }
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

func TestMonitorUsesPersistedHealthStateAndRetriesNotification(t *testing.T) {
	repository := &monitorRepository{
		upstreams: []core.Upstream{{ID: 1, Name: "primary", Enabled: true, HealthStatus: "healthy"}},
		status:    "degraded",
	}
	prober := monitorProber{health: Health{Status: "unhealthy", CheckedAt: time.Now(), Error: "HTTP 503"}}
	attempts := 0
	monitor := NewMonitor(repository, prober, NotifierFunc(func(context.Context, Event) error {
		attempts++
		if attempts == 1 {
			return errors.New("webhook unavailable")
		}
		return nil
	}), MonitorConfig{Concurrency: 1})

	if err := monitor.RunHealth(context.Background()); err == nil {
		t.Fatal("first notification should fail")
	}
	if len(repository.events) != 1 || repository.events[0].State != "degraded" {
		t.Fatalf("events = %#v", repository.events)
	}
	if err := monitor.RunHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(repository.events) != 1 {
		t.Fatalf("attempts=%d events=%#v", attempts, repository.events)
	}
}
