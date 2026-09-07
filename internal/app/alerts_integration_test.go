package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/alerts"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

func alertTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("alerts_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	box, err := cryptox.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func alertTestUpstream(t *testing.T, db *store.Store) int64 {
	t.Helper()
	id, err := db.CreateUpstream(context.Background(), core.Upstream{
		Name: "shared-policy-test", Kind: "newapi", BaseURL: "https://example.com", APIKey: "test",
		Enabled: true, Protocols: []string{"chat"}, Models: []string{}, FailureThreshold: 2, Cooldown: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMetricSampleMinimumAndHysteresis(t *testing.T) {
	db := alertTestStore(t)
	id := alertTestUpstream(t, db)
	repository := AlertRepository{Store: db}
	ctx := context.Background()
	for _, test := range []struct {
		name, event               string
		count, failures, duration int
		ignore, active, hold      bool
	}{
		{"no errors sample", alerts.EventErrorRate, 0, 0, 0, true, false, false},
		{"four failures", alerts.EventErrorRate, 4, 4, 0, true, false, false},
		{"five failures", alerts.EventErrorRate, 5, 5, 0, false, true, false},
		{"error deadband", alerts.EventErrorRate, 20, 3, 0, false, false, true},
		{"error recovered", alerts.EventErrorRate, 20, 2, 0, false, false, false},
		{"one slow request", alerts.EventLatency, 1, 0, 60000, true, false, false},
		{"four slow requests", alerts.EventLatency, 4, 0, 60000, true, false, false},
		{"five slow requests", alerts.EventLatency, 5, 0, 30000, false, true, false},
		{"latency deadband", alerts.EventLatency, 5, 0, 24000, false, false, true},
		{"latency recovered", alerts.EventLatency, 5, 0, 23999, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.DB().Exec(`DELETE FROM request_logs`); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < test.count; i++ {
				status := 200
				if i < test.failures {
					status = 500
				}
				attempts, err := json.Marshal([]map[string]any{{"upstream_id": id, "status_code": status, "duration_ms": test.duration}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB().Exec(`INSERT INTO request_logs(request_id,protocol,status_code,duration_ms,attempts) VALUES($1,'chat',$2,$3,$4::jsonb)`, strconv.Itoa(i), status, test.duration, attempts); err != nil {
					t.Fatal(err)
				}
			}
			observations, err := repository.Observe(ctx, alerts.Rule{Event: test.event, Window: 5 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if len(observations) != 1 {
				t.Fatalf("observations=%+v", observations)
			}
			o := observations[0]
			if o.Ignore != test.ignore || o.Active != test.active || o.Hold != test.hold {
				t.Fatalf("observation=%+v", o)
			}
		})
	}
}

func TestAlertPersistenceAndWebhookDelivery(t *testing.T) {
	db := alertTestStore(t)
	id := alertTestUpstream(t, db)
	ctx := context.Background()
	if _, err := db.DB().Exec(`UPDATE alert_rules SET enabled=(event='error_rate')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		attempts, _ := json.Marshal([]map[string]any{{"upstream_id": id, "status_code": 500, "duration_ms": 10}})
		if _, err := db.DB().Exec(`INSERT INTO request_logs(request_id,protocol,status_code,duration_ms,attempts) VALUES($1,'chat',500,10,$2::jsonb)`, strconv.Itoa(i), attempts); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		// Recreate both adapters on each tick to exercise database restoration.
		engine := alerts.NewEngine(AlertRepository{Store: db}, NewOutboxNotifier(db))
		if err := engine.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	queued := func(want int) {
		t.Helper()
		var count int
		if err := db.DB().QueryRow(`SELECT count(*) FROM notification_outbox`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("queued=%d want=%d", count, want)
		}
	}
	run()
	queued(0)
	// Re-running the idempotent migration must preserve confirmation history.
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	run()
	queued(1)
	run()
	queued(1)
	if _, err := db.DB().Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		run()
	}
	queued(1)
	var active bool
	if err := db.DB().QueryRow(`SELECT active FROM alert_states WHERE observation_key=$1`, "upstream:"+strconv.FormatInt(id, 10)).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("lack of traffic resolved the incident")
	}
	if _, err := db.DB().Exec(`UPDATE notification_outbox SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	received := make(chan ops.Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event ops.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		received <- event
		w.WriteHeader(204)
	}))
	defer server.Close()
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunNotificationWorker(workerCtx, db, ops.NewWebhookNotifier(ops.WebhookConfig{URL: server.URL}, server.Client()))
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case event := <-received:
		if event.Type != "error_rate" || event.State != "firing" || event.UpstreamID != id {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not delivered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		if err := db.DB().QueryRow(`SELECT count(*) FROM notification_outbox`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivered notification remained queued")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthNotificationsPersistCooldownAndLeaveRoutingResponsive(t *testing.T) {
	db := alertTestStore(t)
	id := alertTestUpstream(t, db)
	ctx := context.Background()
	save := func(healthy bool, wantStatus, wantNotification string) {
		t.Helper()
		probe := ops.Health{Status: "unhealthy", StatusCode: 500}
		if healthy {
			probe.Status, probe.StatusCode = "healthy", 200
		}
		repo := OpsRepository{Store: db}
		status, notification, err := repo.SaveHealth(ctx, id, probe)
		if err != nil || status != wantStatus || notification != wantNotification {
			t.Fatalf("status=%s notification=%s err=%v", status, notification, err)
		}
		if notification != "" {
			if err := repo.AcknowledgeHealthNotification(ctx, id, notification); err != nil {
				t.Fatal(err)
			}
		}
	}
	save(false, "degraded", "")
	save(false, "unhealthy", "unhealthy")
	save(true, "unhealthy", "")
	save(true, "unhealthy", "")
	save(true, "healthy", "healthy")
	save(false, "degraded", "")
	save(false, "unhealthy", "")
	if _, err := db.DB().Exec(`UPDATE health_notification_states SET state=jsonb_set(state,'{incident,last_firing_at}',to_jsonb(now()-interval '31 minutes')) WHERE upstream_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	save(false, "unhealthy", "unhealthy")
	save(false, "unhealthy", "")
}

func TestHealthNotificationUpgradeAndEnqueueRetry(t *testing.T) {
	db := alertTestStore(t)
	id := alertTestUpstream(t, db)
	ctx := context.Background()
	if err := db.AcknowledgeHealthNotification(ctx, id, "unhealthy"); err != nil {
		t.Fatal(err)
	}
	observe := func(status, want string) {
		t.Helper()
		got, err := db.ObserveHealthNotification(ctx, id, status, 30*time.Second)
		if err != nil || got != want {
			t.Fatalf("notification=%q want=%q err=%v", got, want, err)
		}
	}
	observe("unhealthy", "")
	observe("healthy", "healthy")
	// Simulate enqueue failure: without acknowledgement the lease prevents flooding.
	observe("healthy", "")
	if _, err := db.DB().Exec(`UPDATE health_notification_states SET state=jsonb_set(state,'{incident,retry_after}',to_jsonb(now()-interval '1 second')) WHERE upstream_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	observe("healthy", "healthy")
	if err := db.AcceptHealthNotification(ctx, id, "healthy"); err != nil {
		t.Fatal(err)
	}
	observe("healthy", "")
	observe("unhealthy", "")
}
