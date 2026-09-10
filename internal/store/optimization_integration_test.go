package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNotificationLeaseRejectsLateWorkerAndManualRetryABA(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	var channel int64
	if err := db.db.QueryRow(`INSERT INTO notification_channels(name,kind,config_encrypted) VALUES('lease','webhook','') RETURNING id`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := db.EnqueueNotificationForChannel(ctx, channel, []byte(`{"type":"test"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	claim := func() NotificationJob {
		t.Helper()
		jobs, err := db.ClaimNotificationJobs(ctx, 1)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim: %v, jobs=%d", err, len(jobs))
		}
		return jobs[0]
	}
	first := claim()
	if _, err := db.db.Exec(`UPDATE notification_outbox SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RenewNotification(ctx, first); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	second := claim()
	if second.LeaseVersion <= first.LeaseVersion {
		t.Fatal("lease version not advanced")
	}
	assertStale := func(job NotificationJob) {
		t.Helper()
		for _, err := range []error{db.CompleteNotification(ctx, job), db.FailNotification(ctx, job, "late failure", time.Now()), db.DeadNotification(ctx, job, "late dead"), db.RenewNotification(ctx, job)} {
			if !errors.Is(err, ErrNotificationLeaseLost) {
				t.Fatalf("stale lease mutated job: %v", err)
			}
		}
	}
	assertStale(first)
	if err := db.DeadNotification(ctx, second, "failed"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RetryDeadNotification(ctx, second.ID); err != nil || !ok {
		t.Fatalf("retry=%v: %v", ok, err)
	}
	third := claim()
	if third.Attempts != 1 || third.LeaseVersion <= second.LeaseVersion {
		t.Fatalf("retry lease=%+v", third)
	}
	assertStale(first)
	assertStale(second)
	if err := db.RenewNotification(ctx, third); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotification(ctx, third); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPagesHandleTiedTimestampsAndNewRows(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.db.Exec(`INSERT INTO request_logs(request_id,protocol,status_code,duration_ms,created_at) SELECT 'page-'||i,'chat_completions',200,1,$1 FROM generate_series(1,101) i`, at); err != nil {
		t.Fatal(err)
	}
	first, err := db.RequestLogPage(ctx, LogFilter{Limit: 50})
	if err != nil || len(first.Items) != 50 || first.NextCursor == "" {
		t.Fatalf("page1: %v", err)
	}
	if first.Items[0].RequestID != "page-101" {
		t.Fatal("missing stable ID ordering")
	}
	if _, err := db.db.Exec(`INSERT INTO request_logs(request_id,protocol,status_code,duration_ms,created_at) VALUES('new','chat_completions',200,1,$1)`, at); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	page := first
	for {
		for _, row := range page.Items {
			if seen[row.RequestID] || row.RequestID == "new" {
				t.Fatalf("unexpected row %s", row.RequestID)
			}
			seen[row.RequestID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor, err := ParsePageCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		page, err = db.RequestLogPage(ctx, LogFilter{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 101 {
		t.Fatalf("missing rows: %d", len(seen))
	}
	if _, err := db.db.Exec(`INSERT INTO notification_outbox(payload,dead_at) SELECT '{}',$1 FROM generate_series(1,51)`, at); err != nil {
		t.Fatal(err)
	}
	dead, err := db.DeadNotificationPage(ctx, nil)
	if err != nil || len(dead.Items) != 50 || dead.NextCursor == "" {
		t.Fatalf("dead page: %v", err)
	}
	cursor, err := ParsePageCursor(dead.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`DELETE FROM notification_outbox WHERE id=$1`, dead.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	rest, err := db.DeadNotificationPage(ctx, cursor)
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("dead tail: %+v %v", rest, err)
	}
}

func TestCleanupBudgetAndReplicaLock(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	cutoff := time.Now().Add(-24 * time.Hour)
	if _, err := db.db.Exec(`INSERT INTO request_logs(request_id,protocol,status_code,duration_ms,created_at) SELECT 'old-'||i,'chat_completions',200,1,$1 FROM generate_series(1,5000) i`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	lock, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.Exec(`SELECT pg_advisory_xact_lock(hashtext(current_schema()),739842107)`); err != nil {
		t.Fatal(err)
	}
	report, err := db.CleanupRetention(ctx, cutoff, cutoff, cutoff)
	if err != nil || !report.Skipped {
		t.Fatalf("replica lock: %+v %v", report, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	report, err = db.cleanupRetention(ctx, cutoff, cutoff, cutoff, 20*time.Millisecond)
	if err != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("budget: %+v %v", report, err)
	}
	if len(report.Tables) != 5 || !report.Tables[0].BudgetExhausted {
		t.Fatalf("budget not reported: %+v", report)
	}
	var remaining int
	if err := db.db.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&remaining); err != nil || remaining == 0 {
		t.Fatalf("expected remaining work: %d %v", remaining, err)
	}
	report, err = db.CleanupRetention(ctx, cutoff, cutoff, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("resume failed: %d %v", remaining, err)
	}
	snapshot := db.CleanupStatus()
	snapshot.Tables[0].Table = "modified"
	if db.CleanupStatus().Tables[0].Table != "request_logs" {
		t.Fatal("status aliases internal slice")
	}
}

func TestPageCursorRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"!", "e30", encodePageCursor(time.Now(), 0), fmt.Sprintf("%0300d", 1)} {
		if _, err := ParsePageCursor(raw); err == nil {
			t.Fatalf("accepted invalid cursor: %q", raw)
		}
	}
}
