package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/app"
	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/config"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"github.com/gaoLfun/dapi/internal/gateway"
	"github.com/gaoLfun/dapi/internal/store"
)

func readonlyDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("readonly_test_%d", time.Now().UnixNano())
	if _, err = db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close() })
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
	s, err := store.Open(context.Background(), u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func randomUsageSecret(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "dapi_usage_" + base64.RawURLEncoding.EncodeToString(b[:])
}
func TestReadonlyUsageAccessAndManagement(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	secret := randomUsageSecret(t)
	id1, err := db.CreateUpstream(ctx, core.Upstream{Name: "A", Kind: "newapi", BaseURL: "https://example.com", APIKey: secret, Enabled: true, Protocols: []string{"chat"}, Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.CreateUpstream(ctx, core.Upstream{Name: "B", Enabled: true, Kind: "sub2api", BaseURL: "https://example.com", APIKey: secret, Protocols: []string{}, Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	adminID, err := db.CreateAdmin(ctx, "readonly-test-admin", []byte("unused"))
	if err != nil {
		t.Fatal(err)
	}
	session, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSession(ctx, hash, adminID, "127.0.0.1", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := New(db, config.Config{}, nil, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	g := gateway.NewSecureHandler(app.GatewayRepository{Store: db})
	defer g.Close(ctx)
	mux.Handle("/v1/", g)
	handler := ProxyHeaders(mux, false)
	call := func(method, path, bearer, body string, admin bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if admin {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status=%d want=%d", w.Code, want)
		}
		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), session) {
			t.Fatal("response leaked a secret")
		}
	}
	for _, credential := range []string{"", randomUsageSecret(t), session} {
		check(call("GET", "/api/readonly/usage", credential, "", false), 401)
	}
	model, _, _, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/readonly/usage", model, "", false), 401)
	check(call("GET", "/api/readonly/usage", "", "", true), 401)
	body := fmt.Sprintf(`{"name":"plugin","station_ids":["%d"]}`, id1)
	created := call("POST", "/api/admin/usage-credentials", "", body, true)
	check(created, 201)
	var c struct {
		ID         string `json:"id"`
		Credential string `json:"credential"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Credential == "" {
		t.Fatal("no credential")
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("creation cacheable")
	}
	check(call("GET", "/api/admin/me", c.Credential, "", false), 401)
	check(call("POST", "/v1/chat/completions", c.Credential, `{"model":"x","messages":[]}`, false), 401)
	check(call("POST", "/api/admin/usage-credentials", c.Credential, body, false), 401)
	// Real local HTTP query, with no Operations implementation: refreshes cannot occur.
	server := httptest.NewServer(handler)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/api/readonly/usage", nil)
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var data store.ReadonlyUsage
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(data.Stations) != 1 || data.Stations[0].ID != strconv.FormatInt(id1, 10) || data.Timezone != "UTC" {
		t.Fatal("scope or HTTP query mismatch")
	}
	// A failed balance probe can contain secrets; only the safe projection is exposed.
	snapshot, _ := json.Marshal(core.Balance{Status: "unavailable", Error: secret})
	if _, err = db.DB().Exec(`UPDATE upstreams SET balance=$2::jsonb WHERE id=$1`, id1, snapshot); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/readonly/usage", c.Credential, "", false), 200)
	other := randomUsageSecret(t)
	otherID, err := db.CreateUsageCredential(ctx, "other", auth.HashToken(other), []int64{id2})
	if err != nil {
		t.Fatal(err)
	}
	readIDs := func(credential string, want []int64) {
		t.Helper()
		w := call("GET", "/api/readonly/usage", credential, "", false)
		check(w, 200)
		if strings.Contains(w.Body.String(), credential) {
			t.Fatal("response leaked query credential")
		}
		var d store.ReadonlyUsage
		if err = json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Stations) != len(want) {
			t.Fatal("station count mismatch")
		}
		for i, id := range want {
			if d.Stations[i].ID != strconv.FormatInt(id, 10) {
				t.Fatal("scope crossed")
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("response cacheable")
		}
	}
	readIDs(other, []int64{id2})
	readIDs(c.Credential, []int64{id1})
	check(call("PUT", "/api/admin/usage-credentials/"+c.ID, "", `{"enabled":true,"station_ids":["9223372036854775807"]}`, true), 400)
	readIDs(c.Credential, []int64{id1})

	// Query then shrink and revoke inside the potential 30-second cache window.
	check(call("PUT", "/api/admin/usage-credentials/"+c.ID, "", `{"enabled":true,"station_ids":[]}`, true), 200)
	readIDs(c.Credential, []int64{})
	check(call("PUT", "/api/admin/usage-credentials/"+c.ID, "", `{"enabled":false,"station_ids":[]}`, true), 200)
	check(call("GET", "/api/readonly/usage", c.Credential, "", false), 403)
	check(call("DELETE", "/api/admin/usage-credentials/"+c.ID, "", "", true), 200)
	check(call("GET", "/api/readonly/usage", c.Credential, "", false), 401)
	check(call("PUT", "/api/admin/usage-credentials/"+c.ID, "", `{"enabled":true,"station_ids":[]}`, true), 404)
	listed := call("GET", "/api/admin/usage-credentials", "", "", true)
	check(listed, 200)
	if strings.Contains(listed.Body.String(), c.Credential) {
		t.Fatal("list leaked credential")
	}
	if _, err = db.DB().Exec(`UPDATE upstreams SET balance='[]'::jsonb WHERE id=$1`, id2); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/readonly/usage", other, "", false), 503)
	if _, err = db.DB().Exec(`UPDATE upstreams SET balance='{"status":"unknown"}'::jsonb WHERE id=$1`, id2); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err = db.DB().QueryRow(`SELECT credential_hash FROM usage_credentials WHERE id=$1`, c.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, auth.HashToken(c.Credential)) {
		t.Fatal("not hash-only")
	}
	var audit string
	if err = db.DB().QueryRow(`SELECT COALESCE(string_agg(detail::text,''),'') FROM audit_logs`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, c.Credential) || strings.Contains(logs.String(), c.Credential) || strings.Contains(logs.String(), secret) {
		t.Fatal("logs leaked secret")
	}
	// Exhaust only the valid credential's budget; independent credentials are unaffected.
	for i := 0; i < 10; i++ {
		call("GET", "/api/readonly/usage", other, "", false)
	}
	w := call("GET", "/api/readonly/usage", other, "", false)
	check(w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if err = db.RevokeUsageCredential(ctx, otherID); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/readonly/usage", other, "", false), 401)
	r := httptest.NewRequest("GET", "http://example.com/api/readonly/usage", nil)
	r.RemoteAddr = "203.0.113.8:12"
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	check(w, 403)
	tlsRequest := httptest.NewRequest("GET", "https://example.com/api/readonly/usage", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, tlsRequest)
	check(w, 401)
}

func TestReadonlyUTCStatisticsAndUnknownCoverage(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	raw := randomUsageSecret(t)
	station, err := db.CreateUpstream(ctx, core.Upstream{Name: "stats", Kind: "newapi", BaseURL: "https://example.com", APIKey: raw, Enabled: true, Protocols: []string{"chat"}, Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateGroup(ctx, core.Group{Name: "stats", Enabled: true, UpstreamIDs: []int64{station}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, _, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := db.InsertAPIKeyInGroup(ctx, "stats", "test", auth.HashToken(model), group, []string{"chat"}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateUsageCredential(ctx, "stats", auth.HashToken(raw), []int64{station}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	input, output, cache := int64(100), int64(20), int64(70)
	entry := func(id string, at time.Time) core.RequestLog {
		return core.RequestLog{RequestID: id, APIKeyID: key, GroupID: &group, UpstreamID: &station, Protocol: "chat", Model: "unpriced", StatusCode: 200, CreatedAt: at, Usage: core.Usage{InputTokens: &input, OutputTokens: &output, CachedInputTokens: &cache}}
	}
	// One second before UTC midnight is yesterday, even though local Asia time is today.
	if err = db.RecordRequest(ctx, entry("yesterday", now.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordRequest(ctx, entry("midnight", now)); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordRequests(ctx, []core.RequestLog{entry("after", now.Add(time.Second))}); err != nil {
		t.Fatal(err)
	}
	// Fixture pricing coverage: one of today's two requests is priced.
	if _, err = db.DB().Exec(`UPDATE daily_usage SET cost_usd=2.36,cost_known_requests=1 WHERE day='2026-09-09'`); err != nil {
		t.Fatal(err)
	}
	query := func() store.ReadonlyToday {
		t.Helper()
		result, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), now.Add(time.Minute), 30*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if result.Timezone != "UTC" || len(result.Stations) != 1 {
			t.Fatal("wrong timezone or scope")
		}
		return result.Stations[0].Today
	}
	today := query()
	if today.Requests != 2 || today.Input == nil || *today.Input != 200 || today.Output == nil || *today.Output != 40 || today.Total == nil || *today.Total != 240 {
		t.Fatalf("wrong UTC/token totals: %+v", today)
	}
	if today.Cost == nil || *today.Cost != 2.36 || today.Coverage == nil || *today.Coverage != 0.5 {
		t.Fatal("wrong partial cost coverage")
	}
	missing := entry("missing-output", now.Add(2*time.Second))
	missing.Usage.OutputTokens = nil
	if err = db.RecordRequests(ctx, []core.RequestLog{missing}); err != nil {
		t.Fatal(err)
	}
	today = query()
	if today.Input == nil || *today.Input != 300 || today.Output != nil || today.Total != nil || today.KnownTotal != 340 {
		t.Fatal("unknown output replaced with zero")
	}
	missing = entry("missing-input", now.Add(3*time.Second))
	missing.Usage.InputTokens = nil
	if err = db.RecordRequest(ctx, missing); err != nil {
		t.Fatal(err)
	}
	today = query()
	if today.Input != nil || today.Output != nil || today.Total != nil {
		t.Fatal("partial token totals presented as complete")
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	today = query()
	if today.Requests != 4 {
		t.Fatal("migration changed totals")
	}
	empty, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), now.Add(24*time.Hour), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	zero := empty.Stations[0].Today
	if zero.Requests != 0 || zero.Cost != nil || zero.Coverage != nil || zero.Total == nil || *zero.Total != 0 {
		t.Fatal("empty day semantics wrong")
	}
	// Query must not mutate the station, balance snapshot, or request count.
	var before, after string
	if err = db.DB().QueryRow(`SELECT row_to_json(u)::text FROM upstreams u WHERE id=$1`, station).Scan(&before); err != nil {
		t.Fatal(err)
	}
	query()
	if err = db.DB().QueryRow(`SELECT row_to_json(u)::text FROM upstreams u WHERE id=$1`, station).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("query changed station state")
	}
}

func TestUsageRateWindow(t *testing.T) {
	var l usageRateLimiter
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !l.allow("a", 10, now) {
			t.Fatal("premature limit")
		}
	}
	if l.allow("a", 10, now) {
		t.Fatal("limit ignored")
	}
	if !l.allow("b", 10, now) || !l.allow("a", 10, now.Add(time.Minute)) {
		t.Fatal("scope or window isolation failed")
	}
}

func TestReadonlyLegacyCurrencyAndMissingUnit(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	raw := randomUsageSecret(t)
	id, err := db.CreateUpstream(ctx, core.Upstream{Name: "currency", Enabled: true, Kind: "sub2api", BaseURL: "https://example.com", APIKey: raw, Protocols: []string{}, Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateUsageCredential(ctx, "currency", auth.HashToken(raw), []int64{id}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	zero := 0.0
	for _, reported := range []bool{false, true} {
		snapshot, _ := json.Marshal(core.Balance{Status: "ok", Currency: "USD", CurrencyReported: reported, Available: &zero, UpdatedAt: &now})
		if _, err = db.DB().Exec(`UPDATE upstreams SET balance=$2::jsonb WHERE id=$1`, id, snapshot); err != nil {
			t.Fatal(err)
		}
		result, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), now, 30*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if (result.Stations[0].Balance.Currency != nil) != reported {
			t.Fatal("legacy currency assumed known")
		}
	}
	if _, err = db.SaveBalance(ctx, id, core.Balance{Status: "ok", Available: &zero, UpdatedAt: &now}, false); err != nil {
		t.Fatal(err)
	}
	result, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), now, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stations[0].Balance.Currency != nil {
		t.Fatal("missing unit inherited historical USD")
	}
}

func TestReadonlyEnabledPriorityGroups(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	raw := randomUsageSecret(t)
	makeStation := func(name string, priority int, enabled bool) int64 {
		t.Helper()
		id, err := db.CreateUpstream(ctx, core.Upstream{Name: name, Kind: "newapi", BaseURL: "https://example.com", APIKey: raw, Enabled: enabled, Priority: priority, Protocols: []string{}, Models: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	last := makeStation("last", 90, true)
	first := makeStation("first", 1, true)
	disabled := makeStation("disabled", 0, false)
	hidden := makeStation("outside-scope", 0, true)
	group, err := db.CreateGroup(ctx, core.Group{Name: "codex", Enabled: true, UpstreamIDs: []int64{first, hidden}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateUsageCredential(ctx, "scope", auth.HashToken(raw), []int64{last, first, disabled}); err != nil {
		t.Fatal(err)
	}
	result, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stations) != 2 || result.Stations[0].ID != strconv.FormatInt(first, 10) || result.Stations[1].ID != strconv.FormatInt(last, 10) {
		t.Fatal("disabled/scope/priority mismatch")
	}
	if !result.Stations[0].Enabled || result.Stations[0].Priority != 1 || len(result.Stations[0].Groups) != 1 || result.Stations[0].Groups[0].ID != strconv.FormatInt(group, 10) {
		t.Fatal("group metadata missing")
	}
	if len(result.Stations[1].Groups) != 0 {
		t.Fatal("ungrouped station incorrectly grouped")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "outside-scope") {
		t.Fatal("group leaked unauthorized station")
	}
}

func TestReadonlyUpstreamTodayIsSeparateAndFailureClearsIt(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	raw := randomUsageSecret(t)
	id, err := db.CreateUpstream(ctx, core.Upstream{Name: "direct", Kind: "sub2api", BaseURL: "https://example.com", APIKey: raw, Enabled: true, Protocols: []string{}, Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateUsageCredential(ctx, "scope", auth.HashToken(raw), []int64{id}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	output := int64(21)
	balance := core.Balance{Status: "ok", Source: "sub2api_usage", UpdatedAt: &now, Today: &core.UpstreamToday{Output: &output}}
	write := func() {
		t.Helper()
		body, _ := json.Marshal(balance)
		if _, err = db.DB().Exec(`UPDATE upstreams SET balance=$2::jsonb WHERE id=$1`, id, body); err != nil {
			t.Fatal(err)
		}
	}
	write()
	got, _, err := db.ReadonlyUsage(ctx, auth.HashToken(raw), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stations[0].UpstreamToday == nil || *got.Stations[0].UpstreamToday.Output != 21 || got.Stations[0].Today.Requests != 0 {
		t.Fatal("direct data missing or mixed with gateway totals")
	}
	balance.Status = "unavailable"
	write()
	got, _, err = db.ReadonlyUsage(ctx, auth.HashToken(raw), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stations[0].UpstreamToday != nil {
		t.Fatal("failed query exposed stale direct stats as current")
	}
}
