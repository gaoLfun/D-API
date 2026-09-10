package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/config"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/store"
)

func TestLogDrilldownFiltersApplyBeforeCursorPaging(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	upstreams := make([]int64, 2)
	for i := range upstreams {
		id, err := db.CreateUpstream(ctx, core.Upstream{Name: fmt.Sprintf("drill-%d", i), Kind: "newapi", BaseURL: fmt.Sprintf("https://cluster-%d.example.com", i), APIKey: "test-only", Enabled: true, Protocols: []string{"responses"}, Models: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		upstreams[i] = id
	}
	group, err := db.CreateGroup(ctx, core.Group{Name: "drill", Enabled: true, UpstreamIDs: upstreams})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]int64, 2)
	for i := range keys {
		id, err := db.InsertAPIKeyInGroup(ctx, fmt.Sprintf("client-%d", i), "test", auth.HashToken(fmt.Sprintf("client-test-%d", i)), group, []string{"responses"}, []string{})
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = id
	}
	for i := range 6 {
		// Four matching rows, one other client, one other model/cluster.
		key, up, model := keys[0], upstreams[0], "model-a"
		if i == 4 {
			key = keys[1]
		}
		if i == 5 {
			model = "model-b"
			up = upstreams[1]
		}
		if _, err := db.DB().Exec(`INSERT INTO request_logs(request_id,api_key_id,upstream_id,protocol,model,status_code,duration_ms,created_at) VALUES($1,$2,$3,'responses',$4,200,1,'2026-09-10T00:00:00Z')`, fmt.Sprintf("drill-%d", i), key, up, model); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := db.CreateAdmin(ctx, "drill-admin", []byte("unused"))
	if err != nil {
		t.Fatal(err)
	}
	session, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSession(ctx, hash, admin, "127.0.0.1", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(db, config.Config{}, nil, nil).Register(mux)
	query := url.Values{"pagination": {"cursor"}, "limit": {"2"}, "api_key_id": {fmt.Sprint(keys[0])}, "model": {"model-a"}, "protocol": {"responses"}, "upstream_base_url": {"https://CLUSTER-0.example.com:443/"}, "since": {"2026-09-10T00:00:00Z"}, "until": {"2026-09-11T00:00:00Z"}}
	seen := map[string]bool{}
	for range 2 {
		request := httptest.NewRequest("GET", "/api/admin/logs?"+query.Encode(), nil)
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var page store.CursorPage[store.RequestLogView]
		if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("wrong filtered count=%d", len(page.Items))
		}
		for _, row := range page.Items {
			if seen[row.RequestID] || row.APIKeyID != keys[0] || row.Model != "model-a" {
				t.Fatalf("unexpected row=%s", row.RequestID)
			}
			seen[row.RequestID] = true
		}
		query.Set("cursor", page.NextCursor)
	}
	if len(seen) != 4 || query.Get("cursor") != "" {
		t.Fatalf("cursor missed rows: %d", len(seen))
	}
	for _, suffix := range []string{"api_key_id=-1", "api_key_id=bad", "model=" + url.QueryEscape(string(make([]byte, 513)))} {
		request := httptest.NewRequest("GET", "/api/admin/logs?"+suffix, nil)
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("invalid filter status=%d", response.Code)
		}
	}
	request := httptest.NewRequest("GET", "/api/admin/logs?"+query.Encode(), nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("unauthenticated drilldown accepted")
	}
}
