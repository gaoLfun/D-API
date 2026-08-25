package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/ops"
)

func TestOperationsProbeDraft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer draft-key" {
			t.Fatalf("probe request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()

	operations := Operations{Prober: ops.NewProber(server.Client(), time.Second)}
	health := operations.Probe(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "draft-key"})
	if health.Status != "healthy" || len(health.Models) != 1 || health.Models[0] != "model-a" {
		t.Fatalf("health = %#v", health)
	}
}
