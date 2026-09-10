package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func waitForBlockedQuery(t *testing.T, db *Store, relation, fragment string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := db.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE l.relation=to_regclass($1) AND a.wait_event_type='Lock' AND position($2 in a.query)>0)`, relation, fragment).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("query did not reach expected lock")
}

func TestSessionCreationSerializesWithPasswordChange(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	id, err := db.CreateAdmin(ctx, "session-race", []byte("old-test-hash"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := db.AdminByUsername(ctx, "session-race")
	if err != nil {
		t.Fatal(err)
	}
	// Pause insertion after the login has locked the verified password row.
	lock, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.Exec(`LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	loginDone := make(chan error, 1)
	go func() {
		loginDone <- db.CreateSessionForPassword(ctx, []byte("session-before-change"), old, "", "", time.Now().Add(time.Hour))
	}()
	waitForBlockedQuery(t, db, "sessions", "INSERT INTO sessions")
	changeDone := make(chan error, 1)
	go func() { changeDone <- db.UpdateAdminPassword(ctx, id, []byte("new-test-hash")) }()
	waitForBlockedQuery(t, db, "admins", "UPDATE admins")
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-loginDone; err != nil {
		t.Fatal(err)
	}
	if err = <-changeDone; err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdminBySession(ctx, []byte("session-before-change")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old session survived: %v", err)
	}
	// The opposite order: a login verified before the committed change cannot insert.
	if err = db.CreateSessionForPassword(ctx, []byte("late-session"), old, "", "", time.Now().Add(time.Hour)); !errors.Is(err, ErrPasswordChanged) {
		t.Fatalf("late login accepted: %v", err)
	}
	current, err := db.AdminByUsername(ctx, "session-race")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSessionForPassword(ctx, []byte("current-session"), current, "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdminBySession(ctx, []byte("current-session")); err != nil {
		t.Fatal(err)
	}
}

func TestStaleUpstreamObservationsCannotMutateNewConfiguration(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	id, err := db.CreateUpstream(ctx, core.Upstream{Name: "versioned", Kind: "newapi", BaseURL: "https://old.example.com", APIKey: "test-only", Enabled: true, BalanceProtection: true, Protocols: []string{"responses"}, Models: []string{"old-model"}, FailureThreshold: 1, Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	old, err := db.Upstream(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	updated := old.Upstream
	updated.BaseURL = "https://new.example.com"
	updated.Models = []string{"new-model"}
	if _, err = db.UpdateUpstream(ctx, updated); err != nil {
		t.Fatal(err)
	}
	current, err := db.Upstream(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.ConfigVersion <= old.ConfigVersion {
		t.Fatal("configuration version did not advance")
	}
	zero := 0.0
	if _, err = db.SaveBalance(ctx, id, core.Balance{Status: "ok", Available: &zero}, true, old.ConfigVersion); !errors.Is(err, ErrUpstreamConfigChanged) {
		t.Fatalf("stale balance: %v", err)
	}
	if _, _, err = db.SaveProbeHealth(ctx, id, false, "old failure", true, old.ConfigVersion); !errors.Is(err, ErrUpstreamConfigChanged) {
		t.Fatalf("stale probe: %v", err)
	}
	if _, err = db.SaveHealth(ctx, id, false, "old request", true, old.ConfigVersion); !errors.Is(err, ErrUpstreamConfigChanged) {
		t.Fatalf("stale gateway health: %v", err)
	}
	if err = db.SaveDiscoveredModels(ctx, id, []string{"stale-model"}, old.ConfigVersion); !errors.Is(err, ErrUpstreamConfigChanged) {
		t.Fatalf("stale models: %v", err)
	}
	if _, err = db.ObserveHealthNotification(ctx, id, "unhealthy", time.Second, old.ConfigVersion); !errors.Is(err, ErrUpstreamConfigChanged) {
		t.Fatalf("stale notification: %v", err)
	}
	after, err := db.Upstream(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceSuspended || after.ConsecutiveFailure != current.ConsecutiveFailure || after.HealthStatus != current.HealthStatus || len(after.Models) != 1 || after.Models[0] != "new-model" {
		t.Fatal("stale results modified current configuration")
	}
	if _, err = db.SaveBalance(ctx, id, core.Balance{Status: "ok", Available: &zero}, true, current.ConfigVersion); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.SaveProbeHealth(ctx, id, false, "current failure", true, current.ConfigVersion); err != nil {
		t.Fatal(err)
	}
	after, err = db.Upstream(ctx, id)
	if err != nil || !after.BalanceSuspended || after.HealthStatus != "unhealthy" {
		t.Fatalf("current results not applied: %v", err)
	}
}

func TestUsageURLNormalizationMatchesSQLAndLogFilter(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	for _, raw := range []string{"https://API.example.com:443/v1/?region=test#part", "http://api.example.com:80///", "https://[2606:4700::1111]:443/v1", "http://[2606:4700::1111]:80/v1/?x=y", "https://api.example.com:8443/Prefix/", "https://api.example.com/a%2Fb/"} {
		t.Run(raw, func(t *testing.T) {
			var normalized string
			if err := db.db.QueryRow(`SELECT `+usageBaseURLSQL("$1::text"), raw).Scan(&normalized); err != nil {
				t.Fatal(err)
			}
			if normalized != normalizeUsageBaseURL(raw) {
				t.Fatalf("normalization differs: sql=%q go=%q", normalized, normalizeUsageBaseURL(raw))
			}
			id, err := db.CreateUpstream(ctx, core.Upstream{Name: raw, Kind: "newapi", BaseURL: raw, APIKey: "test-only", Protocols: []string{"responses"}, Models: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.db.Exec(`INSERT INTO request_logs(request_id,upstream_id,protocol,model,status_code,duration_ms) VALUES($1,$2,'responses','m',200,1)`, raw, id); err != nil {
				t.Fatal(err)
			}
			logs, err := db.ListRequestLogs(ctx, LogFilter{UpstreamBaseURL: normalized})
			if err != nil || len(logs) != 1 || logs[0].RequestID != raw {
				t.Fatalf("normalized drilldown failed count=%d err=%v", len(logs), err)
			}
		})
	}
}
