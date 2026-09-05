package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestLoadGateCancellationAndCleanup(t *testing.T) {
	var gate loadGate[string]
	release, err := gate.acquire(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := gate.acquire(ctx, "key"); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	var workers sync.WaitGroup
	value := 0
	for range 20 {
		workers.Go(func() {
			release, err := gate.acquire(context.Background(), "key")
			if err != nil {
				return
			}
			defer release()
			value++
		})
	}
	workers.Wait()
	if value != 20 || len(gate.active) != 0 {
		t.Fatalf("value=%d active=%d", value, len(gate.active))
	}
}

func TestPerformanceCachesWithPostgres(t *testing.T) {
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("performance_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
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
	db, err := Open(ctx, u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	up := core.Upstream{Name: "perf", Kind: "newapi", BaseURL: "https://example.com", APIKey: "test", Enabled: true, Protocols: []string{"chat"}, Models: []string{"m"}, FailureThreshold: 2, Cooldown: time.Minute}
	id, err := db.CreateUpstream(ctx, up)
	if err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateGroup(ctx, core.Group{Name: "perf", Enabled: true, UpstreamIDs: []int64{id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveHealth(ctx, id, true, "", false); err != nil {
		t.Fatal(err)
	}
	if routes, err := db.ListRouteUpstreams(ctx, group, "chat", "m"); err != nil || len(routes) != 1 {
		t.Fatalf("routes=%v err=%v", routes, err)
	}
	generation := db.routeGen
	if _, _, err := db.SaveProbeHealth(ctx, id, true, "", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDiscoveredModels(ctx, id, []string{"m"}); err != nil {
		t.Fatal(err)
	}
	if db.routeGen != generation {
		t.Fatal("unchanged probe invalidated routes")
	}
	if _, err := db.SaveHealth(ctx, id, false, "failed", true); err != nil {
		t.Fatal(err)
	}
	if routes, err := db.ListRouteUpstreams(ctx, group, "chat", "m"); err != nil || len(routes) != 0 {
		t.Fatalf("circuit routes=%v err=%v", routes, err)
	}
	for range 3 {
		if _, _, err := db.SaveProbeHealth(ctx, id, true, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if routes, err := db.ListRouteUpstreams(ctx, group, "chat", "m"); err != nil || len(routes) != 1 {
		t.Fatalf("recovered routes=%v err=%v", routes, err)
	}
	start, boundary := time.Now().Add(-time.Hour), time.Now().Add(-30*time.Minute)
	profile, err := db.SavePricingProfile(ctx, PricingProfile{Name: "history", Prices: []PricingModelPrice{{Model: "m", InputUSDPerMillion: 1, ValidFrom: start}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUpstreamPricingProfile(ctx, id, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SavePricingProfile(ctx, PricingProfile{ID: profile, Name: "history", Prices: []PricingModelPrice{{Model: "m", InputUSDPerMillion: 2, ValidFrom: boundary}}}); err != nil {
		t.Fatal(err)
	}
	if rate, err := db.pricingRate(ctx, id, "m", start.Add(time.Minute)); err != nil || rate.input != 1 {
		t.Fatalf("historical rate=%v err=%v", rate, err)
	}
	if _, err := db.Dashboard(ctx); err != nil {
		t.Fatal(err)
	}
	// Occupy the only connection: subsequent cache hits must not need SQL.
	db.db.SetMaxOpenConns(1)
	conn, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, at := range []time.Time{boundary, time.Now()} {
		if rate, err := db.pricingRate(checkCtx, id, "m", at); err != nil || rate.input != 2 {
			t.Fatalf("cached rate=%v err=%v", rate, err)
		}
	}
	if _, err := db.Dashboard(checkCtx); err != nil {
		t.Fatalf("dashboard cache queried SQL: %v", err)
	}
	if routes, err := db.ListRouteUpstreams(checkCtx, group, "chat", "m"); err != nil || len(routes) != 1 {
		t.Fatalf("route cache queried SQL: %v", err)
	}
	conn.Close()
	db.dashboardExpires = time.Time{}
	if err := db.RecordRequests(ctx, []core.RequestLog{{RequestID: "new", Model: "m", StatusCode: 400}}); err != nil {
		t.Fatal(err)
	}
	if value, err := db.Dashboard(ctx); err != nil || value.Requests24H != 1 {
		t.Fatalf("expired dashboard=%v err=%v", value, err)
	}
	t.Run("large price history remains cached", func(t *testing.T) {
		start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		_, err := db.db.ExecContext(ctx, `INSERT INTO pricing_model_prices(profile_id,model,input_usd_per_million,output_usd_per_million,cache_read_usd_per_million,cache_write_usd_per_million,valid_from,valid_to,source)
			SELECT $1,'many',n+1,0,0,0,$2::timestamptz+n*interval '1 hour',$2::timestamptz+(n+1)*interval '1 hour','test' FROM generate_series(0,299) n`, profile, start)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.db.ExecContext(ctx, `UPDATE upstreams SET model_aliases='{"public_many":"many"}' WHERE id=$1`, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.db.ExecContext(ctx, `INSERT INTO pricing_model_prices(profile_id,model,input_usd_per_million,output_usd_per_million,cache_read_usd_per_million,cache_write_usd_per_million,valid_from,valid_to,source)
			VALUES($1,'public_many',999,0,0,0,$2,$3,'test')`, profile, start.Add(151*time.Hour), start.Add(152*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		at := start.Add(150*time.Hour + 30*time.Minute)
		if rate, err := db.pricingRate(ctx, id, "public_many", at); err != nil || rate.input != 151 {
			t.Fatalf("rate=%v err=%v", rate, err)
		}
		entry := db.pricingCache[pricingCacheKey{upstreamID: id, model: "public_many"}]
		if !entry.partial || len(entry.prices) != 1 {
			t.Fatalf("unbounded cache=%+v", entry)
		}
		conn, err := db.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		rate, err := db.pricingRate(checkCtx, id, "public_many", at.Add(time.Second))
		cancel()
		conn.Close()
		if err != nil || rate.input != 151 {
			t.Fatalf("cache queried SQL: %v %v", rate, err)
		}
		for _, check := range []struct {
			at   time.Time
			want float64
		}{
			{start.Add(151 * time.Hour), 999}, {start.Add(152 * time.Hour), 153}, {start.Add(-time.Second), 0},
		} {
			if rate, err := db.pricingRate(ctx, id, "public_many", check.at); err != nil || rate.input != check.want || rate.found != (check.want > 0) {
				t.Fatalf("boundary rate=%v err=%v", rate, err)
			}
		}
	})
	t.Run("stream failure and top N statistics", func(t *testing.T) {
		keyID, err := db.InsertAPIKey(ctx, "stats", "stats", []byte("stats-hash"), []string{"chat"}, []string{})
		if err != nil {
			t.Fatal(err)
		}
		entries := []core.RequestLog{
			{RequestID: "stats-1", APIKeyID: keyID, UpstreamID: &id, Protocol: "chat", Model: "a", StatusCode: 200, DurationMS: 10},
			{RequestID: "stats-2", APIKeyID: keyID, UpstreamID: &id, Protocol: "chat", Model: "a", StatusCode: 200, DurationMS: 20},
			{RequestID: "stats-3", APIKeyID: keyID, UpstreamID: &id, Protocol: "chat", Model: "a", StatusCode: 200, ErrorCode: "stream_interrupted", DurationMS: 100},
			{RequestID: "stats-4", APIKeyID: keyID, UpstreamID: &id, Protocol: "chat", Model: "b", StatusCode: 200, DurationMS: 50},
		}
		if err := db.RecordRequests(ctx, entries); err != nil {
			t.Fatal(err)
		}
		db.dashboardExpires = time.Time{}
		if value, err := db.Dashboard(ctx); err != nil || value.Requests24H != 5 || value.SuccessRate24H != 60 {
			t.Fatalf("dashboard=%v err=%v", value, err)
		}
		rows, err := db.UsageWithFilter(ctx, UsageFilter{Days: 1, Dimension: "model", TopN: 1})
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
		for _, row := range rows {
			if row.Model == "a" {
				if row.P95DurationMS == nil || *row.P95DurationMS != 92 || row.Successes != 2 {
					t.Fatalf("top row=%+v", row)
				}
			} else if row.P95DurationMS != nil {
				t.Fatalf("other P95=%v", row.P95DurationMS)
			}
		}
	})
	if err := db.SetUpstreamPricingProfile(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if rate, err := db.pricingRate(ctx, id, "m", start); err != nil || rate.found {
		t.Fatalf("stale price after unbind: %v %v", rate, err)
	}
}

func TestPricingTimelineBoundariesAndPrecedence(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	boundary := start.Add(30 * time.Minute)
	entry := pricingCacheEntry{prices: []PricingModelPrice{
		{InputUSDPerMillion: 2, ValidFrom: boundary},
		{InputUSDPerMillion: 1, ValidFrom: start, ValidTo: &boundary},
		{InputUSDPerMillion: 9, ValidFrom: start.Add(-time.Hour)},
	}}
	for _, test := range []struct {
		at   time.Time
		want float64
	}{
		{start.Add(-2 * time.Hour), 0}, {start.Add(-time.Minute), 9},
		{start, 1}, {boundary.Add(-time.Nanosecond), 1}, {boundary, 2},
	} {
		got := selectPricingRate(entry, test.at)
		if got.input != test.want || got.found != (test.want > 0) {
			t.Fatalf("at=%v rate=%+v", test.at, got)
		}
	}
}

func TestDashboardCacheCopies(t *testing.T) {
	rate := 0.5
	value := Dashboard{Daily: []DailyStat{{Requests: 7}}, Hourly: []HourlyStat{{Requests: 8}}, CacheHitRate24H: &rate}
	copy := cloneDashboard(value)
	copy.Daily[0].Requests = 0
	copy.Hourly[0].Requests = 0
	*copy.CacheHitRate24H = 0
	if value.Daily[0].Requests != 7 || value.Hourly[0].Requests != 8 || rate != 0.5 {
		t.Fatal("cached dashboard mutated")
	}
}
