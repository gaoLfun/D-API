package gateway

import (
	"bufio"
	"compress/gzip"
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

func TestCompressedResponsesPreserveUsageAndStreamOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, path, content string
		stream              bool
	}{
		{"chat stream", "/v1/chat/completions", "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", true},
		{"responses stream", "/v1/responses", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n", true},
		{"messages stream", "/v1/messages", "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\nevent: message_stop\ndata: {}\n\n", true},
		{"json", "/v1/chat/completions", `{"usage":{"prompt_tokens":5,"completion_tokens":2}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "gzip" {
					t.Errorf("upstream encoding = %q", r.Header.Get("Accept-Encoding"))
				}
				w.Header().Set("Content-Encoding", "gzip")
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				compressed := gzip.NewWriter(w)
				io.WriteString(compressed, tc.content)
				compressed.Close()
			}))
			defer up.Close()
			for _, encoding := range []string{"gzip", "gzip, br", "identity"} {
				repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, up.URL)}}
				req := httptest.NewRequest("POST", tc.path, strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%t}`, tc.stream)))
				req.Header.Set("Authorization", "Bearer client-secret")
				req.Header.Set("Accept-Encoding", encoding)
				w := httptest.NewRecorder()
				NewHandler(repo).ServeHTTP(w, req)
				if w.Code != 200 || w.Body.String() != tc.content || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Length") != "" {
					t.Fatalf("encoding=%s status=%d headers=%v body=%s", encoding, w.Code, w.Header(), w.Body.String())
				}
				if len(repo.logs) != 1 || repo.logs[0].ErrorCode != "" || len(repo.failures) != 0 {
					t.Fatalf("logs=%+v failures=%v", repo.logs, repo.failures)
				}
				usage := repo.logs[0].Usage
				if usage.InputTokens == nil || *usage.InputTokens != 5 || usage.OutputTokens == nil || *usage.OutputTokens != 2 {
					t.Fatalf("usage lost: %+v", usage)
				}
			}
		})
	}
}

func TestBudgetRejectionDoesNotWaitForUnsentBody(t *testing.T) {
	for _, framing := range []string{"Content-Length: 16", "Transfer-Encoding: chunked", "Content-Length: 16\r\nExpect: 100-continue"} {
		t.Run(framing, func(t *testing.T) {
			repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}}
			handler := NewHandler(repo, Limits{MaxBufferedRequestBytes: 1, MaxRequestDuration: 40 * time.Millisecond})
			closed := make(chan struct{}, 1)
			server := httptest.NewUnstartedServer(handler)
			server.Config.ReadTimeout = 80 * time.Millisecond
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			server.Start()
			defer server.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(time.Second))
			fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client-secret\r\n%s\r\n\r\n", framing)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("rejection waited for unsent body: %v", err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 429 || !response.Close || !strings.Contains(string(body), "body_budget_exceeded") {
				t.Fatalf("status=%d close=%t body=%s err=%v", response.StatusCode, response.Close, body, err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("rejected connection still draining body")
			}
		})
	}
}

type stalledHealthRepository struct{ *fakeRepository }

func (r *stalledHealthRepository) MarkUpstreamSuccess(ctx context.Context, _ core.Upstream) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r *stalledHealthRepository) MarkUpstreamFailure(ctx context.Context, _ core.Upstream, _ int, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestHealthUpdatesRespectTotalDeadline(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, `{}`)
			}))
			defer up.Close()
			repo := &stalledHealthRepository{&fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, up.URL)}}}
			handler := NewHandler(repo, Limits{MaxRequestDuration: 40 * time.Millisecond})
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); close(done) }))
			defer server.Close()
			req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			req.Header.Set("Authorization", "Bearer client-secret")
			client := &http.Client{Timeout: time.Second}
			response, err := client.Do(req)
			if err != nil {
				t.Fatalf("deadline must return 504, not connection EOF/timeout: %v", err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			<-done
			if err != nil || response.StatusCode != 504 || !strings.Contains(string(body), "request_timeout") || len(repo.logs) != 1 || repo.logs[0].StatusCode != 504 || repo.logs[0].ErrorCode != "request_timeout" {
				t.Fatalf("status=%d body=%s logs=%+v err=%v", response.StatusCode, body, repo.logs, err)
			}
		})
	}
}

func TestHealthLockWaitCanBeCanceled(t *testing.T) {
	repo := &fakeRepository{}
	handler := NewHandler(repo)
	state := handler.lockUpstreamHealth(context.Background(), 1)
	defer handler.unlockUpstreamHealth(1, state)
	for _, success := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		done := make(chan struct{})
		go func() {
			if success {
				handler.markSuccess(ctx, reliabilityUpstream(1, "https://example.com"))
			} else {
				handler.markFailure(ctx, 1, 503, "test")
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("health lock ignored request deadline")
		}
		cancel()
	}
	if len(repo.successes) != 0 || len(repo.failures) != 0 || state.references != 1 {
		t.Fatalf("canceled waiter wrote health or leaked reference: refs=%d", state.references)
	}
}
