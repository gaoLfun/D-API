package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestTransportEvictionPreservesActiveResponseAndBoundsCache(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte("last"))
	}))
	defer server.Close()
	defer close(release)
	h := &Handler{transports: make(map[transportKey]*transportEntry)}
	defer h.closeTransports()
	upstream := core.Upstream{ConnectTimeout: time.Second}
	first := h.client(upstream)
	response, err := first.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for i := 2; i < maxCachedTransports+10; i++ {
		h.client(core.Upstream{ConnectTimeout: time.Duration(i) * time.Second})
	}
	if len(h.transports) != maxCachedTransports {
		t.Fatalf("cache size=%d", len(h.transports))
	}
	if h.client(upstream) == first {
		t.Fatal("least recently used client was not evicted")
	}
	// Closing idle pools must not interrupt the response held by the caller.
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.transports) != 0 {
		t.Fatal("shutdown did not clear cache")
	}
	release <- struct{}{}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "firstlast" {
		t.Fatalf("active response interrupted: %q %v", body, err)
	}
}
