package gateway

import (
	"bufio"
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

type waitingAuthRepository struct {
	*fakeRepository
	entered chan context.Context
	release chan struct{}
}

func (r *waitingAuthRepository) Authenticate(ctx context.Context, token string) (core.APIKey, error) {
	r.entered <- ctx
	select {
	case <-ctx.Done():
		return core.APIKey{}, ctx.Err()
	case <-r.release:
		return r.fakeRepository.Authenticate(ctx, token)
	}
}
func TestAuthenticationUsesTotalDeadline(t *testing.T) {
	for _, path := range []string{"/v1/models", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			repo := &waitingAuthRepository{fakeRepository: &fakeRepository{}, entered: make(chan context.Context, 1), release: make(chan struct{})}
			h := NewHandler(repo, Limits{MaxRequestDuration: 30 * time.Millisecond})
			server := httptest.NewServer(h)
			defer server.Close()
			method := "POST"
			if path == "/v1/models" {
				method = "GET"
			}
			req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(`{"model":"m"}`))
			req.Header.Set("Authorization", "Bearer client-secret")
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 504 || !strings.Contains(string(body), "request_timeout") {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			if _, ok := (<-repo.entered).Deadline(); !ok {
				t.Fatal("authentication had no deadline")
			}
			if len(h.authSlots) != 0 {
				t.Fatal("authentication permit leaked")
			}
		})
	}
}
func TestAuthenticationAdmissionRejectsOverflowAndReleasesOnCancel(t *testing.T) {
	repo := &waitingAuthRepository{fakeRepository: &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}}, entered: make(chan context.Context, 2), release: make(chan struct{})}
	h := NewHandler(repo, Limits{MaxConcurrentRequests: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer client-secret")
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(first, req) }()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("authentication did not start")
	}
	overflow := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m"}`))
	overflow.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, overflow)
	if response.Code != 429 || !strings.Contains(response.Body.String(), "authentication_busy") {
		t.Fatalf("overflow status=%d", response.Code)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authentication ignored cancellation")
	}
	close(repo.release)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, req.Clone(context.Background()))
	if response.Code != 200 || len(h.authSlots) != 0 {
		t.Fatalf("permit was not reusable: %d", response.Code)
	}
}

func TestAuthenticationRejectionDoesNotWaitForUnsentBody(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		for _, framing := range []string{"Content-Length: 16", "Transfer-Encoding: chunked", "Content-Length: 16\r\nExpect: 100-continue"} {
			t.Run(fmt.Sprintf("overflow=%t/%s", overflow, framing), func(t *testing.T) {
				repo := &waitingAuthRepository{fakeRepository: &fakeRepository{}, entered: make(chan context.Context, 1), release: make(chan struct{})}
				handler := NewHandler(repo, Limits{MaxConcurrentRequests: 1, MaxRequestDuration: 30 * time.Millisecond})
				status, code := 504, "request_timeout"
				if overflow {
					handler.authSlots <- struct{}{}
					status, code = 429, "authentication_busy"
				}
				server := httptest.NewServer(handler)
				defer server.Close()
				conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				fmt.Fprintf(conn, "POST /v1/responses HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client-secret\r\n%s\r\n\r\n", framing)
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatalf("authentication response waited for unsent body: %v", err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != status || !response.Close || !strings.Contains(string(body), code) {
					t.Fatalf("status=%d close=%t body=%s err=%v", response.StatusCode, response.Close, body, err)
				}
			})
		}
	}
}
