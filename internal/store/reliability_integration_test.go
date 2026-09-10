package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
)

func reliabilityStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("reliability_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	box, err := cryptox.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrationRetainsPrimaryIndexAndUpgradesLegacySchema(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	var before, after int64
	index := `SELECT oid::bigint FROM pg_class WHERE oid='daily_usage_pkey'::regclass`
	if err := db.db.QueryRow(index).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.QueryRow(index).Scan(&after); err != nil || before != after {
		t.Fatalf("index changed: %d -> %d err=%v", before, after, err)
	}
	if _, err := db.db.Exec(`DROP TABLE schema_migrations;ALTER TABLE daily_usage DROP CONSTRAINT daily_usage_pkey;ALTER TABLE daily_usage ADD PRIMARY KEY(day,api_key_id,upstream_id,protocol,model)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := db.db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='daily_usage'::regclass AND contype='p'`).Scan(&definition); err != nil || !strings.Contains(definition, "group_id") {
		t.Fatalf("legacy migration=%s err=%v", definition, err)
	}
}

func TestMessagesHistoricalCorrectionIsDeltaBasedAndIdempotent(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	profile, err := db.SavePricingProfile(ctx, PricingProfile{Name: "regression", Prices: []PricingModelPrice{{Model: "m", InputUSDPerMillion: 2, OutputUSDPerMillion: 8, CacheReadUSDPerMillion: 0.5, CacheWriteUSDPerMillion: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	up, err := db.CreateUpstream(ctx, core.Upstream{Name: "regression", Kind: "newapi", BaseURL: "https://example.com", APIKey: "test-only", Enabled: true, Protocols: []string{core.ProtocolMessages}, Models: []string{"m"}, PricingProfileID: &profile})
	if err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateGroup(ctx, core.Group{Name: "regression", Enabled: true, UpstreamIDs: []int64{up}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := db.InsertAPIKeyInGroup(ctx, "regression", "test-only", auth.HashToken("test-only"), group, []string{core.ProtocolMessages}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	entry := core.RequestLog{RequestID: "messages-regression", APIKeyID: key, GroupID: &group, UpstreamID: &up, Protocol: core.ProtocolMessages, Model: "m", StatusCode: 200, Usage: core.Usage{InputTokens: tokenCount(10), UncachedInputTokens: tokenCount(40), CachedInputTokens: tokenCount(20), CacheCreationInputTokens: tokenCount(30), OutputTokens: tokenCount(5)}}
	if err = db.RecordRequests(ctx, []core.RequestLog{entry}); err != nil {
		t.Fatal(err)
	}
	assertCosts := func(want float64) {
		t.Helper()
		for _, table := range []string{"request_logs", "daily_usage", "hourly_usage", "upstream_lifetime_usage"} {
			var cost float64
			if err := db.db.QueryRow("SELECT cost_usd FROM " + table + " LIMIT 1").Scan(&cost); err != nil || math.Abs(cost-want) > 1e-9 {
				t.Fatalf("%s cost=%f want=%f err=%v", table, cost, want, err)
			}
		}
	}
	assertCosts(0.00016)
	for _, table := range []string{"request_logs", "daily_usage", "hourly_usage", "upstream_lifetime_usage"} {
		if _, err = db.db.Exec("UPDATE " + table + " SET cost_usd=cost_usd+0.00006"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.db.Exec(`UPDATE request_logs SET pricing_version=1`); err != nil {
		t.Fatal(err)
	}
	result, err := db.BackfillPricingCosts(ctx, time.Now(), time.Now())
	if err != nil || result.LogsUpdated != 1 {
		t.Fatalf("backfill=%+v err=%v", result, err)
	}
	assertCosts(0.00016)
	result, err = db.BackfillPricingCosts(ctx, time.Now(), time.Now())
	if err != nil || result.LogsUpdated != 0 {
		t.Fatalf("repeat=%+v err=%v", result, err)
	}
	totals, err := db.UsageTotals(ctx, UsageFilter{Days: 1})
	if err != nil || totals.CostKnownRequests != 1 || totals.UncachedInputTokens != 40 {
		t.Fatalf("totals=%+v err=%v", totals, err)
	}

	// A profile change or manual historical amount must not be silently repriced.
	if _, err = db.db.Exec(`UPDATE request_logs SET pricing_version=1,cost_usd=1.00016`); err != nil {
		t.Fatal(err)
	}
	result, err = db.BackfillPricingCosts(ctx, time.Now(), time.Now())
	if err != nil || result.LogsUpdated != 0 {
		t.Fatalf("unverifiable amount changed: %+v err=%v", result, err)
	}
	var preserved float64
	if err = db.db.QueryRow(`SELECT cost_usd FROM request_logs LIMIT 1`).Scan(&preserved); err != nil || preserved != 1.00016 {
		t.Fatalf("unverifiable cost=%f err=%v", preserved, err)
	}
	// Encoded cache results must not be mutable by callers.
	first, err := db.UsageReport(ctx, UsageFilter{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	first[0] = '!'
	second, err := db.UsageReport(ctx, UsageFilter{Days: 1})
	if err != nil || len(second) == 0 || second[0] != '{' {
		t.Fatalf("mutable cache err=%v", err)
	}
}

func TestNotificationDeadLetterRetryIsAtomicAndSanitized(t *testing.T) {
	db := reliabilityStore(t)
	ctx := context.Background()
	var channel int64
	if err := db.db.QueryRow(`INSERT INTO notification_channels(name,kind,config_encrypted) VALUES('test','webhook','') RETURNING id`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := db.EnqueueNotificationForChannel(ctx, channel, []byte(`{"type":"test"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ClaimNotificationJobs(ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim err=%v", err)
	}
	if err = db.DeadNotification(ctx, jobs[0], "Post https://example.com/path-secret?key=query-secret failed"); err != nil {
		t.Fatal(err)
	}
	dead, err := db.ListDeadNotifications(ctx, 0)
	if err != nil || len(dead) != 1 || strings.Contains(dead[0].LastError, "secret") {
		t.Fatalf("dead letter sanitization failed: %v", err)
	}
	pending, count, err := db.NotificationCounts(ctx)
	if err != nil || pending != 0 || count != 1 {
		t.Fatalf("pending=%d dead=%d err=%v", pending, count, err)
	}
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			ok, e := db.RetryDeadNotification(ctx, jobs[0].ID)
			if e != nil {
				t.Error(e)
			}
			results <- ok
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("retries=%d", successes)
	}
	jobs, err = db.ClaimNotificationJobs(ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("reclaim: %v", err)
	}
	if err = db.DeadNotification(ctx, jobs[0], "failed again"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE notification_channels SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RetryDeadNotification(ctx, jobs[0].ID); err != nil || ok {
		t.Fatalf("disabled retry=%v err=%v", ok, err)
	}
}

func TestAttemptFailurePredicateIncludesInterrupted200(t *testing.T) {
	db := reliabilityStore(t)
	for _, tc := range []struct {
		attempt, final string
		want           bool
	}{
		{`{"status_code":200,"error":"truncated","failure_class":"upstream"}`, "stream_interrupted", true},
		{`{"status_code":200,"error":"write failed","failure_class":"client"}`, "client_closed", false},
		{`{"status_code":0,"error":"deadline","failure_class":"gateway"}`, "request_timeout", false},
		{`{"status_code":503}`, "", true}, {`{"status_code":200}`, "", false},
	} {
		var failed bool
		err := db.db.QueryRow(`SELECT `+AttemptFailureSQL("a", "final_error")+` FROM (SELECT $1::jsonb a,$2::text final_error) v`, tc.attempt, tc.final).Scan(&failed)
		if err != nil || failed != tc.want {
			t.Fatalf("failed=%v want=%v err=%v", failed, tc.want, err)
		}
	}
}
