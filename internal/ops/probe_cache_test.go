package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestBalancePathDiscoveryReuseAndFallback(t *testing.T) {
	var paths []string
	tokenSupported := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/api/usage/token/" && tokenSupported:
			_, _ = w.Write([]byte(`{"success":true,"data":{"total_available":1500000,"total_used":500000}}`))
		case r.URL.Path == "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000,"used_quota":250000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := NewProber(server.Client(), time.Second)
	u := core.Upstream{BaseURL: server.URL, Kind: "newapi", APIKey: "first"}
	check := func(want []string) {
		t.Helper()
		paths = nil
		if balance := p.CheckBalance(context.Background(), u); balance.Status != "ok" {
			t.Fatalf("balance=%+v", balance)
		}
		if !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths=%v want=%v", paths, want)
		}
	}
	check([]string{"/v1/dashboard/billing/subscription", "/api/usage/token/"})
	check([]string{"/api/usage/token/"})
	tokenSupported = false
	check([]string{"/api/usage/token/", "/v1/dashboard/billing/subscription", "/api/user/self"})
	check([]string{"/api/user/self"})
	u.APIKey = "second"
	check([]string{"/v1/dashboard/billing/subscription", "/api/usage/token/", "/api/user/self"})
	p.balancePaths[balancePathKey(u)] = balancePath{path: "/api/user/self", expires: time.Now().Add(-time.Second)}
	check([]string{"/v1/dashboard/billing/subscription", "/api/usage/token/", "/api/user/self"})
}

func TestProbeAdmissionCancellationAndSeparatePools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Error("cancelled balance probe reached upstream")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-reasoner"}]}`))
	}))
	defer server.Close()
	p := NewProber(server.Client(), time.Second)
	for i := 0; i < cap(p.balanceSlots); i++ {
		p.balanceSlots <- struct{}{}
	}
	u := core.Upstream{BaseURL: server.URL, APIKey: "secret"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if health := p.CheckHealth(ctx, u); health.Status != "healthy" {
		t.Fatalf("health blocked by balance pool: %+v", health)
	}
	if balance := p.CheckBalance(ctx, u); balance.Status != "unavailable" {
		t.Fatalf("balance=%+v", balance)
	}
}
