package app

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/alerts"
	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/gateway"
	"github.com/gaoLfun/dapi/internal/store"
)

// Exercise real HTTP, database authentication/routing, protocol normalization,
// cost calculation, durable logging and alert evidence together.
func TestGatewayMessagesRequestThroughDatabaseAndHTTP(t *testing.T) {
	db := alertTestStore(t)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "test-upstream" {
			t.Error("upstream protocol or auth mismatch")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"test failure\"}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"test-message","type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":5}}`)
	}))
	defer upstream.Close()
	profile, err := db.SavePricingProfile(ctx, store.PricingProfile{Name: "e2e", Prices: []store.PricingModelPrice{{Model: "m", InputUSDPerMillion: 2, OutputUSDPerMillion: 8, CacheReadUSDPerMillion: 0.5, CacheWriteUSDPerMillion: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	up, err := db.CreateUpstream(ctx, core.Upstream{Name: "e2e", Kind: "newapi", BaseURL: upstream.URL, APIKey: "test-upstream", Enabled: true, Protocols: []string{core.ProtocolMessages}, Models: []string{"m"}, PricingProfileID: &profile, FailureThreshold: 100, Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateGroup(ctx, core.Group{Name: "e2e", Enabled: true, UpstreamIDs: []int64{up}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.InsertAPIKeyInGroup(ctx, "e2e", "test", auth.HashToken("test-client"), group, []string{core.ProtocolMessages}, []string{}); err != nil {
		t.Fatal(err)
	}
	// NewHandler permits the loopback mock; production uses NewSecureHandler.
	handler := gateway.NewHandler(GatewayRepository{Store: db})
	completed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		completed <- struct{}{}
	}))
	defer server.Close()
	call := func(stream bool) {
		t.Helper()
		body := `{"model":"m"}`
		if stream {
			body = `{"model":"m","stream":true}`
		}
		req, _ := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("X-Api-Key", "test-client")
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		content, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Flushing the response can finish the client's read before the handler's
		// deferred log write. Synchronize before querying durable state.
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("gateway handler did not finish")
		}
		if e != nil || resp.StatusCode != 200 {
			t.Fatalf("response status=%d err=%v", resp.StatusCode, e)
		}
		if !stream && !strings.Contains(string(content), "test-message") {
			t.Fatal("upstream body missing")
		}
	}
	call(false)
	logs, err := db.ListRequestLogs(ctx, store.LogFilter{Limit: 10})
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%d err=%v", len(logs), err)
	}
	if logs[0].CostUSD == nil || math.Abs(*logs[0].CostUSD-0.00016) > 1e-9 || logs[0].Usage.UncachedInputTokens == nil || *logs[0].Usage.UncachedInputTokens != 40 {
		t.Fatal("messages billing or statistics mismatch")
	}
	for range 5 {
		call(true)
	}
	failed, err := db.ListRequestLogs(ctx, store.LogFilter{Outcome: "error", Limit: 10})
	if err != nil || len(failed) != 5 {
		t.Fatalf("failed logs=%d err=%v", len(failed), err)
	}
	attempts, err := db.ListRequestLogs(ctx, store.LogFilter{AttemptFailure: true, UpstreamID: up, Limit: 10})
	if err != nil || len(attempts) != 5 {
		t.Fatalf("failed attempts=%d err=%v", len(attempts), err)
	}
	observations, err := (AlertRepository{Store: db}).Observe(ctx, alerts.Rule{Event: alerts.EventErrorRate, UpstreamID: &up, Window: 5 * time.Minute})
	if err != nil || len(observations) != 1 || !observations[0].Active {
		t.Fatalf("alert observations=%+v err=%v", observations, err)
	}
}
