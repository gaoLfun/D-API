package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestTotalDeadlineReturns504BeforeResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, server.URL)}}
			response := serve(NewHandler(repo, Limits{MaxRequestDuration: 30 * time.Millisecond}), "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":"m","stream":%t}`, stream), true)
			if response.Code != 504 || !strings.Contains(response.Body.String(), "request_timeout") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(repo.logs) != 1 || repo.logs[0].StatusCode != 504 || repo.logs[0].Attempts[0].FailureClass != "gateway" {
				t.Fatalf("logs=%+v", repo.logs)
			}
		})
	}
}

func TestFailoverDoesNotWaitForHungErrorBody(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer failed.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	defer healthy.Close()
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, failed.URL), reliabilityUpstream(2, healthy.URL)}}
	started := time.Now()
	response := serve(NewHandler(repo), "POST", "/v1/chat/completions", `{"model":"m"}`, true)
	if response.Code != 200 || response.Header().Get("X-DAPI-Attempts") != "2" || time.Since(started) > time.Second {
		t.Fatalf("status=%d elapsed=%s", response.Code, time.Since(started))
	}
}

func TestStreamTerminalEventsAffectRequestAndHealth(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		failed     bool
	}{
		{"error", "event: error\ndata: {\"message\":\"failed\"}\n\n", true},
		{"failed", "data: {\"type\":\"response.failed\"}\n\n", true},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", true},
		{"truncated", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n", true},
		{"complete", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3}}}\n\n", false},
		{"empty", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, server.URL)}}
			serve(NewHandler(repo), "POST", "/v1/responses", `{"model":"m","stream":true}`, true)
			if len(repo.logs) != 1 || (repo.logs[0].ErrorCode != "") != tc.failed || (len(repo.failures) > 0) != tc.failed {
				t.Fatalf("logs=%+v failures=%v", repo.logs, repo.failures)
			}
		})
	}
}

func TestSlowSocketClientReleasesGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := "data: {\"delta\":\"" + strings.Repeat("x", 16000) + "\"}\n\n"
		for {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
			if r.Context().Err() != nil {
				return
			}
		}
	}))
	defer server.Close()
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, server.URL)}}
	handler := NewHandler(repo, Limits{DownstreamWriteTimeout: 40 * time.Millisecond})
	done := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); close(done) }))
	defer gateway.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.SetReadBuffer(1024)
	}
	body := `{"model":"m","stream":true}`
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client-secret\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("slow client blocked gateway")
	}
	if handler.bodyBudget.current() != 0 || len(repo.failures) != 0 || len(repo.logs) != 1 || repo.logs[0].ErrorCode != "client_closed" {
		t.Fatalf("budget=%d logs=%+v failures=%v", handler.bodyBudget.current(), repo.logs, repo.failures)
	}
}

func TestBodyBudgetRejectsConcurrentReservationAndReleases(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	body := `{"model":"m"}`
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, server.URL)}}
	handler := NewHandler(repo, Limits{MaxBufferedRequestBytes: int64(len(body) * 8)})
	done := make(chan struct{})
	go func() { serve(handler, "POST", "/v1/chat/completions", body, true); close(done) }()
	<-entered
	response := serve(handler, "POST", "/v1/chat/completions", body, true)
	close(release)
	<-done
	if response.Code != 429 || !strings.Contains(response.Body.String(), "body_budget_exceeded") || handler.bodyBudget.current() != 0 {
		t.Fatalf("status=%d budget=%d", response.Code, handler.bodyBudget.current())
	}
}

func TestMessagesBillingPreservesCacheHitDenominator(t *testing.T) {
	usage := parseUsageWithProtocol([]byte(`{"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30}}`), core.ProtocolMessages)
	if usage.UncachedInputTokens == nil || *usage.UncachedInputTokens != 40 || usage.BillableInputTokens == nil || *usage.BillableInputTokens != 10 {
		t.Fatalf("usage=%+v", usage)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if failureClass(ctx, ctx.Err()) != "client" {
		t.Fatal("cancellation attributed to upstream")
	}
}

func reliabilityUpstream(id int64, baseURL string) core.Upstream {
	u := upstream(id, baseURL)
	u.Protocols = []string{core.ProtocolChat, core.ProtocolResponses, core.ProtocolMessages}
	return u
}
