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
		UserAgent: "codex_cli_rs/0.101.0",
		Enabled:   true, BalanceProtection: true, Priority: 10, Protocols: []string{core.ProtocolChat}, Models: []string{"model"},
		ConnectTimeout: time.Second, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		FailureThreshold: 2, Cooldown: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := database.Upstream(ctx, upstreamID)
	if err != nil || record.APIKey != "upstream-secret" || record.UserAgent != "codex_cli_rs/0.101.0" || record.ModelsLocked {
		t.Fatalf("upstream secret was not restored: %#v %v", record, err)
	}
	record.ModelsLocked = true
	if _, err := database.UpdateUpstream(ctx, record.Upstream); err != nil {
		t.Fatal(err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || !record.ModelsLocked {
		t.Fatalf("explicit model lock was not saved: %#v %v", record, err)
	}
	profileID, err := database.SavePricingProfile(ctx, PricingProfile{
		Name: "test pricing",
		Prices: []PricingModelPrice{
			{Model: "model", InputUSDPerMillion: 1},
			{Model: "removed-model", InputUSDPerMillion: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record.PricingProfileID = &profileID
	if _, err := database.UpdateUpstream(ctx, record.Upstream); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SavePricingProfile(ctx, PricingProfile{
		ID: profileID, Name: "test pricing",
		Prices: []PricingModelPrice{{Model: "model", InputUSDPerMillion: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	profiles, err := database.ListPricingProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundProfile := false
	for _, profile := range profiles {
		if profile.ID == profileID {
			foundProfile = true
			if len(profile.Prices) != 1 || profile.Prices[0].Model != "model" {
				t.Fatalf("updated pricing models = %#v", profile.Prices)
			}
		}
	}
	if !foundProfile {
		t.Fatalf("updated pricing profile %d was not found", profileID)
	}
	oneMillion := int64(1_000_000)
	if cost, err := database.RequestCost(ctx, upstreamID, "removed-model", core.Usage{InputTokens: &oneMillion}, time.Now()); err != nil || cost != nil {
		t.Fatalf("removed model cost = %#v, %v", cost, err)
	}
	status, err := database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false)
	if err != nil || status != "degraded" {
		t.Fatalf("first failure status=%q err=%v", status, err)
	}
	status, err = database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false)
	if err != nil || status != "unhealthy" {
		t.Fatalf("second failure status=%q err=%v", status, err)
	}
	status, err = database.SaveHealth(ctx, upstreamID, true, "", false)
	if err != nil || status != "unhealthy" {
		t.Fatalf("gateway success closed incident status=%q err=%v", status, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || record.ConsecutiveSuccess != 0 || record.LastError != "HTTP 503" {
		t.Fatalf("gateway success changed recovery state: %#v err=%v", record.Upstream, err)
	}
	status, notification, err := database.SaveProbeHealth(ctx, upstreamID, true, "", false)
	if err != nil || status != "unhealthy" || notification != "unhealthy" {
		t.Fatalf("first recovery confirmation status=%q notification=%q err=%v", status, notification, err)
	}
	if err := database.AcknowledgeHealthNotification(ctx, upstreamID, notification); err != nil {
		t.Fatal(err)
	}
	status, notification, err = database.SaveProbeHealth(ctx, upstreamID, true, "", false)
	if err != nil || status != "unhealthy" || notification != "" {
		t.Fatalf("second recovery confirmation status=%q notification=%q err=%v", status, notification, err)
	}
	status, notification, err = database.SaveProbeHealth(ctx, upstreamID, true, "", false)
	if err != nil || status != "healthy" || notification != "healthy" {
		t.Fatalf("confirmed health recovery status=%q notification=%q err=%v", status, notification, err)
	}
	if err := database.AcknowledgeHealthNotification(ctx, upstreamID, notification); err != nil {
		t.Fatal(err)
	}
	if status, err = database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false); err != nil || status != "degraded" {
		t.Fatalf("new incident first failure status=%q err=%v", status, err)
	}
	if status, err = database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false); err != nil || status != "unhealthy" {
		t.Fatalf("new incident status=%q err=%v", status, err)
	}
	if err := database.AcknowledgeHealthNotification(ctx, upstreamID, "unhealthy"); err != nil {
		t.Fatal(err)
	}
	if status, _, err = database.SaveProbeHealth(ctx, upstreamID, true, "", false); err != nil || status != "unhealthy" {
		t.Fatalf("new incident recovery confirmation status=%q err=%v", status, err)
	}
	if status, err = database.SaveHealth(ctx, upstreamID, false, "HTTP 503", false); err != nil || status != "unhealthy" {
		t.Fatalf("recovery interruption status=%q err=%v", status, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || record.ConsecutiveSuccess != 0 || record.RecoveryStartedAt != nil {
		t.Fatalf("recovery progress was not reset: %#v err=%v", record.Upstream, err)
	}
	if status, _, err = database.SaveProbeHealth(ctx, upstreamID, true, "", false); err != nil || status != "unhealthy" {
		t.Fatalf("timed recovery start status=%q err=%v", status, err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE upstreams SET recovery_started_at=now()-interval '3 minutes' WHERE id=$1`, upstreamID); err != nil {
		t.Fatal(err)
	}
	if status, err = database.SaveHealth(ctx, upstreamID, true, "", false); err != nil || status != "unhealthy" {
		t.Fatalf("gateway success bypassed timed recovery status=%q err=%v", status, err)
	}
	if status, notification, err = database.SaveProbeHealth(ctx, upstreamID, true, "", false); err != nil || status != "healthy" || notification != "healthy" {
		t.Fatalf("timed health recovery status=%q err=%v", status, err)
	}

	secondaryID, err := database.CreateUpstream(ctx, core.Upstream{
		Name: "secondary", Kind: "newapi", BaseURL: "https://secondary.example", APIKey: "secondary-secret",
		Enabled: true, Priority: 20, Protocols: []string{core.ProtocolChat}, Models: []string{"model"},
		ConnectTimeout: time.Second, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		FailureThreshold: 2, Cooldown: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	groupID, err := database.CreateGroup(ctx, core.Group{Name: "routing", Enabled: true, UpstreamIDs: []int64{upstreamID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GroupAvailable(ctx, groupID); err != nil {
		t.Fatalf("group should be available: %v", err)
	}
	if err := database.UpdateGroup(ctx, core.Group{ID: groupID, Name: "routing", Enabled: true, UpstreamIDs: []int64{upstreamID, secondaryID}}); err != nil {
		t.Fatal(err)
	}
	if routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "model"); err != nil || len(routed) != 2 {
		t.Fatalf("updated group route candidates=%#v err=%v", routed, err)
	}
	if err := database.UpdateGroup(ctx, core.Group{ID: groupID, Name: "routing", Enabled: true, UpstreamIDs: []int64{upstreamID}}); err != nil {
		t.Fatal(err)
	}
	routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "model")
	if err != nil || len(routed) != 1 || routed[0].ID != upstreamID {
		t.Fatalf("group route candidates=%#v err=%v", routed, err)
	}
	zero, positive := 0.0, 1.0
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "ok", Available: &zero}, false); err != nil || transition != core.BalanceUnchanged {
		t.Fatalf("first zero balance transition=%q err=%v", transition, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || record.ZeroBalanceChecks != 1 {
		t.Fatalf("first zero balance state=%#v err=%v", record, err)
	}
	record.APIKey = "replacement-secret"
	if transition, err := database.UpdateUpstream(ctx, record.Upstream); err != nil || transition != core.BalanceUnchanged {
		t.Fatalf("credential update transition=%q err=%v", transition, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || record.ZeroBalanceChecks != 0 {
		t.Fatalf("credential update did not reset zero checks: %#v err=%v", record, err)
	}
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "ok", Available: &zero}, false); err != nil || transition != core.BalanceUnchanged {
		t.Fatalf("first zero after credential update transition=%q err=%v", transition, err)
	}
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "ok", Available: &zero}, false); err != nil || transition != core.BalanceSuspended {
		t.Fatalf("second zero balance transition=%q err=%v", transition, err)
	}
	if routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "model"); err != nil || len(routed) != 0 {
		t.Fatalf("balance-suspended route candidates=%#v err=%v", routed, err)
	}
	used := 2.5
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "ok", Available: &positive, Used: &used, Currency: "USD"}, false); err != nil || transition != core.BalanceResumed {
		t.Fatalf("balance recovery transition=%q err=%v", transition, err)
	}
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "unknown", Error: "temporary failure"}, false); err != nil || transition != core.BalanceUnchanged {
		t.Fatalf("failed balance refresh transition=%q err=%v", transition, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil || record.Balance.Used == nil || *record.Balance.Used != used || record.Balance.Currency != "USD" || record.Balance.LastSuccess == nil {
		t.Fatalf("last successful balance usage was not retained: %#v err=%v", record.Balance, err)
	}
	if routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "model"); err != nil || len(routed) != 1 {
		t.Fatalf("balance-recovered route candidates=%#v err=%v", routed, err)
	}
	if transition, err := database.SaveBalance(ctx, upstreamID, core.Balance{Status: "ok", Available: &zero}, true); err != nil || transition != core.BalanceSuspended {
		t.Fatalf("manual zero balance transition=%q err=%v", transition, err)
	}
	record, err = database.Upstream(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	record.BalanceProtection = false
	if transition, err := database.UpdateUpstream(ctx, record.Upstream); err != nil || transition != core.BalanceResumed {
		t.Fatalf("disable protection transition=%q err=%v", transition, err)
	}
	if routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "model"); err != nil || len(routed) != 1 {
		t.Fatalf("protection-disabled route candidates=%#v err=%v", routed, err)
	}
	var balanceAlertCount int
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM alert_events WHERE upstream_id=$1 AND event='upstream_balance_protection'`, upstreamID).Scan(&balanceAlertCount); err != nil || balanceAlertCount != 4 {
		t.Fatalf("balance alert count=%d err=%v", balanceAlertCount, err)
	}
	var lastBalanceAlert string
	if err := database.DB().QueryRowContext(ctx, `SELECT message FROM alert_events WHERE upstream_id=$1 AND event='upstream_balance_protection' ORDER BY id DESC LIMIT 1`, upstreamID).Scan(&lastBalanceAlert); err != nil || lastBalanceAlert != "upstream primary resumed because balance protection was disabled" {
		t.Fatalf("last balance alert=%q err=%v", lastBalanceAlert, err)
	}
	if routed, err := database.ListRouteUpstreams(ctx, groupID, core.ProtocolChat, "other-model"); err != nil || len(routed) != 0 {
		t.Fatalf("group model filter candidates=%#v err=%v", routed, err)
	}
	if err := database.GroupAvailable(ctx, 999999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group error=%v", err)
	}
	var emptyGroupID int64
	if err := database.DB().QueryRowContext(ctx, `INSERT INTO groups(name,enabled) VALUES('empty',TRUE) RETURNING id`).Scan(&emptyGroupID); err != nil {
		t.Fatal(err)
	}
	if err := database.GroupAvailable(ctx, emptyGroupID); !errors.Is(err, ErrGroupEmpty) {
		t.Fatalf("empty group error=%v", err)
	}

	rawKey := "dapi_test_client_key"
	keyID, err := database.InsertAPIKeyWithSecret(ctx, "client", "dapi_test", auth.HashToken(rawKey), rawKey, []string{core.ProtocolChat}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAPIKey(ctx, core.APIKey{ID: keyID, Name: "client", GroupID: 0, Enabled: true, Protocols: []string{core.ProtocolChat}, Models: []string{}}); err != nil {
		t.Fatal(err)
	}
	var ungrouped bool
	if err := database.DB().QueryRowContext(ctx, `SELECT group_id IS NULL FROM api_keys WHERE id=$1`, keyID).Scan(&ungrouped); err != nil || !ungrouped {
		t.Fatalf("legacy API key group_id is not NULL: ungrouped=%t err=%v", ungrouped, err)
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
	if got := todayUsage[upstreamID]; got.Requests != 1 || got.Tokens != 15 || got.CostKnownRequests != 1 || got.LifetimeRequests != 1 || got.LifetimeKnownRequests != 1 || got.CostUSD != 0.000012 || got.LifetimeCostUSD != 0.000012 {
		t.Fatalf("today upstream usage=%#v", got)
	}
	backfillAt := time.Now().Add(time.Second)
	if err := database.RecordRequest(ctx, core.RequestLog{
		RequestID: "request-backfill", APIKeyID: keyID, UpstreamID: &secondaryID, Protocol: core.ProtocolChat,
		Model: "model", StatusCode: 200, DurationMS: 10, Usage: core.Usage{InputTokens: &oneMillion}, CreatedAt: backfillAt,
	}); err != nil {
		t.Fatal(err)
	}
	secondary, err := database.Upstream(ctx, secondaryID)
	if err != nil {
		t.Fatal(err)
	}
	secondary.PricingProfileID = &profileID
	if _, err := database.UpdateUpstream(ctx, secondary.Upstream); err != nil {
		t.Fatal(err)
	}
	if result, err := database.BackfillPricingCosts(ctx, backfillAt, time.Now()); err != nil || result.LogsUpdated != 1 {
		t.Fatalf("pricing backfill=%#v err=%v", result, err)
	}
	todayUsage, err = database.TodayUpstreamUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := todayUsage[secondaryID]; got.Requests != 1 || got.CostKnownRequests != 1 || got.LifetimeRequests != 1 || got.LifetimeKnownRequests != 1 || got.CostUSD != 1 || got.LifetimeCostUSD != 1 {
		t.Fatalf("backfilled upstream usage=%#v", got)
	}

	groupedRawKey := "dapi_test_grouped_key"
	groupedKeyID, err := database.InsertAPIKeyWithSecretInGroup(ctx, "grouped-client", "dapi_grouped", auth.HashToken(groupedRawKey), groupedRawKey, groupID, []string{core.ProtocolChat}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if key, err := database.APIKey(ctx, groupedKeyID); err != nil || key.GroupID != groupID {
		t.Fatalf("group binding=%#v err=%v", key, err)
	}
	if err := database.UpdateGroup(ctx, core.Group{ID: groupID, Name: "routing", Enabled: false, UpstreamIDs: []int64{upstreamID}}); !errors.Is(err, ErrGroupHasKeys) {
		t.Fatalf("disable group with key error=%v", err)
	}
	if err := database.DeleteGroup(ctx, groupID); !errors.Is(err, ErrGroupHasKeys) {
		t.Fatalf("delete group with key error=%v", err)
	}
	if err := database.RecordRequest(ctx, core.RequestLog{
		RequestID: "request-grouped", APIKeyID: groupedKeyID, GroupID: &groupID, UpstreamID: &upstreamID,
		Protocol: core.ProtocolChat, Model: "model", StatusCode: 200, DurationMS: 11, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var groupedRows, totalRows int
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM daily_usage WHERE group_id=$1`, groupID).Scan(&groupedRows); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM daily_usage`).Scan(&totalRows); err != nil {
		t.Fatal(err)
	}
	if groupedRows != 1 || totalRows != 3 {
		t.Fatalf("daily usage rows grouped=%d total=%d", groupedRows, totalRows)
	}
	if err := database.DeleteAPIKey(ctx, keyID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteUpstream(ctx, upstreamID); err != nil {
		t.Fatal(err)
	}
	if err := database.GroupAvailable(ctx, groupID); !errors.Is(err, ErrGroupDisabled) {
		t.Fatalf("group after deleting last upstream error=%v", err)
	}
	if err := database.DeleteAPIKey(ctx, groupedKeyID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteGroup(ctx, groupID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteGroup(ctx, emptyGroupID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteUpstream(ctx, secondaryID); err != nil {
		t.Fatal(err)
	}
	var usageRows int
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM daily_usage`).Scan(&usageRows); err != nil || usageRows != 3 {
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
