package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

func TestInFlightProbeCannotOverwriteEditedUpstream(t *testing.T) {
	for _, kind := range []string{"health", "balance"} {
		t.Run(kind, func(t *testing.T) {
			db := alertTestStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started, release := make(chan struct{}, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "health" {
					w.WriteHeader(401)
					io.WriteString(w, `{}`)
				} else {
					io.WriteString(w, `{"remaining":0,"unit":"USD"}`)
				}
			}))
			defer server.Close()
			id, err := db.CreateUpstream(ctx, core.Upstream{Name: "probe-version", Kind: "sub2api", BaseURL: server.URL, APIKey: "test-only", Enabled: true, BalanceProtection: true, Protocols: []string{"responses"}, Models: []string{"m"}, FailureThreshold: 1, Cooldown: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			operations := Operations{Store: db, Prober: ops.NewProber(server.Client(), time.Second)}
			done := make(chan error, 1)
			go func() {
				if kind == "health" {
					_, err := operations.Check(ctx, id)
					done <- err
				} else {
					_, _, _, err := operations.Balance(ctx, id)
					done <- err
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			current, err := db.Upstream(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			current.APIKey = "replacement-test-only"
			if _, err = db.UpdateUpstream(ctx, current.Upstream); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !errors.Is(err, store.ErrUpstreamConfigChanged) {
				t.Fatalf("old probe was not rejected: %v", err)
			}
			result, err := db.Upstream(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if result.BalanceSuspended || result.ConsecutiveFailure != 0 || result.CircuitOpenUntil != nil {
				t.Fatal("old result changed new routing state")
			}
		})
	}
}
