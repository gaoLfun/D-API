package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestHealthDiscoversModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5"},{"id":"claude-sonnet"},{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	result := NewProber(server.Client(), time.Second).CheckHealth(context.Background(), core.Upstream{BaseURL: server.URL + "/v1", APIKey: "secret"})
	if result.Status != "healthy" || len(result.Models) != 2 || result.Models[0] != "gpt-5" || result.Models[1] != "claude-sonnet" {
		t.Fatalf("health = %#v", result)
	}
}

func TestHealthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	result := NewProber(server.Client(), time.Second).CheckHealth(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if result.Status != "unhealthy" || result.StatusCode != http.StatusServiceUnavailable || result.Error == "" {
		t.Fatalf("health = %#v", result)
	}
}

func TestNewAPITokenBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			http.NotFound(w, r)
		case "/api/usage/token/":
			_, _ = w.Write([]byte(`{"success":true,"code":0,"data":{"total_available":1500000,"total_used":500000,"unlimited_quota":false}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL + "/v1", APIKey: "secret"})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 3 || balance.Used == nil || *balance.Used != 1 || balance.Currency != "USD" {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestNewAPIUserBalanceCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/self" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("New-Api-User"); got != "42" {
			t.Errorf("New-Api-User = %q", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000,"used_quota":250000,"group":"default"}}`))
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{
		BaseURL: server.URL, APIKey: "secret", AccessToken: "session", UserID: "42",
	})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 2 || balance.Used == nil || *balance.Used != .5 || balance.Plan != "default" {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestSub2APIBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"quota":{"remaining":80,"used":20,"unit":"USD"},"usage":{"total":{"actual_cost":30}}}`))
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 80 || balance.Used == nil || *balance.Used != 20 {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestUnknownBalanceWhenUnsupported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if balance.Status != "unknown" || balance.Available != nil || balance.Used != nil {
		t.Fatalf("balance = %#v", balance)
	}
}
