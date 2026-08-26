package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
)

func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("DAPI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("dapi_test_%x", suffix)
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer adminDB.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	box, err := cryptox.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, parsed.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	adminID, err := database.CreateAdmin(ctx, "admin", []byte("old-hash"))
	if err != nil {
		t.Fatal(err)
	}
	sessionHash := auth.HashToken("session")
	if err := database.CreateSession(ctx, sessionHash, adminID, "127.0.0.1", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAdminPassword(ctx, adminID, []byte("new-hash")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AdminBySession(ctx, sessionHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session was not revoked: %v", err)
	}

	upstreamID, err := database.CreateUpstream(ctx, core.Upstream{
		Name: "primary", Kind: "newapi", BaseURL: "https://upstream.example", APIKey: "upstream-secret",
		Enabled: true, Priority: 10, Protocols: []string{core.ProtocolChat}, Models: []string{"model"},
		ConnectTimeout: time.Second, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		FailureThreshold: 2, Cooldown: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := database.Upstream(ctx, upstreamID)
	if err != nil || record.APIKey != "upstream-secret" || record.ModelsLocked {
		t.Fatalf("upstream secret was not restored: %#v %v", record, err)
	}
	record.ModelsLocked = true
	if err := database.UpdateUpstream(ctx, record.Upstream); err != nil {
		t.Fatal(err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || !record.ModelsLocked {
		t.Fatalf("explicit model lock was not saved: %#v %v", record, err)
	}
	status, err := database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false)
	if err != nil || status != "degraded" {
		t.Fatalf("first failure status=%q err=%v", status, err)
	}
	status, err = database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false)
	if err != nil || status != "unhealthy" {
		t.Fatalf("second failure status=%q err=%v", status, err)
	}

	rawKey := "dapi_test_client_key"
	keyID, err := database.InsertAPIKeyWithSecret(ctx, "client", "dapi_test", auth.HashToken(rawKey), rawKey, []string{core.ProtocolChat}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if secret, err := database.APIKeySecret(ctx, keyID); err != nil || secret != rawKey {
		t.Fatalf("API key secret retrieval failed: %q %v", secret, err)
	}
	if key, err := database.AuthenticateAPIKey(ctx, rawKey); err != nil || key.ID != keyID {
		t.Fatalf("API key authentication failed: %#v %v", key, err)
	}
	input, output := int64(12), int64(3)
	if err := database.RecordRequest(ctx, core.RequestLog{
		RequestID: "request-1", APIKeyID: keyID, UpstreamID: &upstreamID, Protocol: core.ProtocolChat,
		Model: "model", StatusCode: 200, DurationMS: 10, Usage: core.Usage{InputTokens: &input, OutputTokens: &output},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	todayUsage, err := database.TodayUpstreamUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := todayUsage[upstreamID]; got.Requests != 1 || got.Tokens != 15 {
		t.Fatalf("today upstream usage=%#v", got)
	}
	if err := database.DeleteAPIKey(ctx, keyID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteUpstream(ctx, upstreamID); err != nil {
		t.Fatal(err)
	}
	var usageRows int
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM daily_usage`).Scan(&usageRows); err != nil || usageRows != 1 {
		t.Fatalf("historical usage rows=%d err=%v", usageRows, err)
	}

	rules, err := database.ListAlertRules(ctx)
	if err != nil || len(rules) == 0 {
		t.Fatalf("alert rules=%#v err=%v", rules, err)
	}
	now := time.Now()
	if err := database.SaveAlertState(ctx, rules[0].ID, "current", AlertState{Active: true, LastObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveAlertState(ctx, rules[0].ID, "stale", AlertState{Active: true, LastObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.PruneAlertStates(ctx, rules[0].ID, []string{"current"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.AlertState(ctx, rules[0].ID, "stale"); err != nil || found {
		t.Fatalf("stale alert state found=%t err=%v", found, err)
	}

	if err := database.WriteAudit(ctx, &adminID, "test", "test", "1", nil, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveAlertEvent(ctx, nil, "test", "resolved", "test"); err != nil {
		t.Fatal(err)
	}
	before := time.Now().AddDate(0, 0, 1)
	for name, cleanup := range map[string]func(context.Context, time.Time) error{
		"request_logs": database.CleanupLogs, "audit_logs": database.CleanupAuditLogs,
		"alert_events": database.CleanupAlertEvents, "daily_usage": database.CleanupDailyUsage,
	} {
		if err := cleanup(ctx, before); err != nil {
			t.Fatalf("cleanup %s: %v", name, err)
		}
		var count int
		if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cleanup %s left %d rows: %v", name, count, err)
		}
	}
}
