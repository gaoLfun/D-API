package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type fakeRepository struct {
	mu          sync.Mutex
	key         core.APIKey
	authErr     error
	candidates  []core.Upstream
	models      []string
	maxAttempts int
	logs        []core.RequestLog
	successes   []int64
	failures    []int64
}

func (f *fakeRepository) Authenticate(_ context.Context, token string) (core.APIKey, error) {
	if f.authErr != nil {
		return core.APIKey{}, f.authErr
	}
	if token != "client-secret" {
		return core.APIKey{}, ErrInvalidAPIKey
	}
	return f.key, nil
}

func (f *fakeRepository) Candidates(context.Context, string, string) ([]core.Upstream, error) {
	return append([]core.Upstream(nil), f.candidates...), nil
}

func (f *fakeRepository) AvailableModels(context.Context, core.APIKey) ([]string, error) {
	return append([]string(nil), f.models...), nil
}

func (f *fakeRepository) MaxAttempts(context.Context) (int, error) {
	if f.maxAttempts == 0 {
		return 3, nil
	}
	return f.maxAttempts, nil
}

func (f *fakeRepository) RecordRequest(_ context.Context, entry core.RequestLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, entry)
	return nil
}

func (f *fakeRepository) MarkUpstreamSuccess(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, id)
	return nil
}

func (f *fakeRepository) MarkUpstreamFailure(_ context.Context, id int64, _ int, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, id)
	return nil
}

func TestProxySwitchesAndRewritesModel(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer failed.Close()

	succeeded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "provider-model" {
			t.Errorf("model = %q", payload.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"ok\",\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":3}}}")
	}))
	defer succeeded.Close()

	repo := &fakeRepository{
		key: core.APIKey{ID: 9, Enabled: true, Protocols: []string{core.ProtocolChat}, Models: []string{"public-model"}},
		candidates: []core.Upstream{
			{ID: 2, Name: "second", BaseURL: succeeded.URL + "/v1", APIKey: "upstream-secret", Enabled: true, Priority: 20, Protocols: []string{core.ProtocolChat}, ModelAliases: map[string]string{"public-model": "provider-model"}},
			{ID: 1, Name: "first", BaseURL: failed.URL, APIKey: "bad", Enabled: true, Priority: 10, Protocols: []string{core.ProtocolChat}, Models: []string{"public-model"}},
		},
	}
	recorder := serve(NewHandler(repo), http.MethodPost, "/v1/chat/completions", "{\"model\":\"public-model\",\"messages\":[]}", true)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-DAPI-Upstream"); got != "second" {
		t.Errorf("X-DAPI-Upstream = %q", got)
	}
	if got := recorder.Header().Get("X-DAPI-Attempts"); got != "2" {
		t.Errorf("X-DAPI-Attempts = %q", got)
	}
	if recorder.Header().Get("X-DAPI-Request-ID") == "" {
		t.Error("missing X-DAPI-Request-ID")
	}
	if len(repo.failures) != 1 || repo.failures[0] != 1 {
		t.Errorf("failures = %v", repo.failures)
	}
	if len(repo.successes) != 1 || repo.successes[0] != 2 {
		t.Errorf("successes = %v", repo.successes)
	}
	if len(repo.logs) != 1 || len(repo.logs[0].Attempts) != 2 {
		t.Fatalf("logs = %#v", repo.logs)
	}
	usage := repo.logs[0].Usage
	if usage.InputTokens == nil || *usage.InputTokens != 11 || usage.OutputTokens == nil || *usage.OutputTokens != 7 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 3 {
		t.Errorf("usage = %#v", usage)
	}
}

func TestAuthenticationAndPermissions(t *testing.T) {
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true, Protocols: []string{core.ProtocolChat}}}
	handler := NewHandler(repo)

	missing := serve(handler, http.MethodPost, "/v1/chat/completions", "{\"model\":\"m\"}", false)
	if missing.Code != http.StatusUnauthorized {
		t.Errorf("missing auth status = %d", missing.Code)
	}

	forbidden := serve(handler, http.MethodPost, "/v1/messages", "{\"model\":\"m\"}", true)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}
	if !strings.Contains(forbidden.Body.String(), "\"type\":\"permission_error\"") {
		t.Errorf("unexpected Anthropic error: %s", forbidden.Body.String())
	}
	if len(repo.logs) != 1 || repo.logs[0].ErrorCode != "permission_denied" {
		t.Errorf("logs = %#v", repo.logs)
	}
}

func TestMessagesAcceptsAndReplacesAPIKeyHeader(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "upstream-secret" {
			t.Errorf("X-Api-Key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"id":"msg_1","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstreamServer.Close()

	u := upstream(1, upstreamServer.URL)
	u.APIKey = "upstream-secret"
	u.Protocols = []string{core.ProtocolMessages}
	repo := &fakeRepository{
		key:        core.APIKey{ID: 1, Enabled: true, Protocols: []string{core.ProtocolMessages}},
		candidates: []core.Upstream{u},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("X-Api-Key", "client-secret")
	recorder := httptest.NewRecorder()
	NewHandler(repo).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestModelsAreFilteredSortedAndDeduplicated(t *testing.T) {
	repo := &fakeRepository{
		key:    core.APIKey{ID: 1, Enabled: true, Models: []string{"b", "c"}},
		models: []string{"c", "a", "b", "b"},
	}
	recorder := serve(NewHandler(repo), http.MethodGet, "/v1/models", "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 || response.Data[0].ID != "b" || response.Data[1].ID != "c" {
		t.Errorf("models = %#v", response.Data)
	}
}

func TestNormalizedUpstreamErrors(t *testing.T) {
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rateLimited.Close()
	timedOut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "{}")
	}))
	defer timedOut.Close()

	tests := []struct {
		name       string
		upstreams  []core.Upstream
		wantStatus int
		wantCode   string
	}{
		{name: "no upstream", wantStatus: http.StatusBadGateway, wantCode: "bad_gateway"},
		{name: "all rate limited", upstreams: []core.Upstream{upstream(1, rateLimited.URL)}, wantStatus: http.StatusTooManyRequests, wantCode: "rate_limit_exceeded"},
		{name: "all timed out", upstreams: []core.Upstream{func() core.Upstream {
			u := upstream(1, timedOut.URL)
			u.FirstByteTimeout = 5 * time.Millisecond
			return u
		}()}, wantStatus: http.StatusGatewayTimeout, wantCode: "gateway_timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: test.upstreams}
			recorder := serve(NewHandler(repo), http.MethodPost, "/v1/responses", "{\"model\":\"m\"}", true)
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), "\"code\":\""+test.wantCode+"\"") {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestNonStreamingIdleTimeoutSwitchesUpstream(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{")
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
	}))
	defer stalled.Close()
	succeeded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer succeeded.Close()

	first := upstream(1, stalled.URL)
	first.Priority = 10
	first.FirstByteTimeout = 100 * time.Millisecond
	first.IdleTimeout = 5 * time.Millisecond
	second := upstream(2, succeeded.URL)
	second.Priority = 20
	repo := &fakeRepository{
		key:        core.APIKey{ID: 1, Enabled: true},
		candidates: []core.Upstream{first, second},
	}
	recorder := serve(NewHandler(repo), http.MethodPost, "/v1/responses", `{"model":"m"}`, true)

	if recorder.Code != http.StatusOK || recorder.Header().Get("X-DAPI-Attempts") != "2" {
		t.Fatalf("status = %d, attempts = %s, body = %s", recorder.Code, recorder.Header().Get("X-DAPI-Attempts"), recorder.Body.String())
	}
	if len(repo.failures) != 1 || repo.failures[0] != 1 {
		t.Fatalf("failures = %v", repo.failures)
	}
}

func TestStreamIdleTimeoutAndUsage(t *testing.T) {
	t.Run("idle timeout after commit marks upstream failure", func(t *testing.T) {
		upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\"}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstreamServer.Close()

		u := upstream(1, upstreamServer.URL)
		u.FirstByteTimeout = 100 * time.Millisecond
		u.IdleTimeout = 5 * time.Millisecond
		repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{u}}
		recorder := serve(NewHandler(repo), http.MethodPost, "/v1/responses", "{\"model\":\"m\",\"stream\":true}", true)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "response.output_text.delta") {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if len(repo.logs) != 1 || repo.logs[0].ErrorCode != "stream_interrupted" {
			t.Fatalf("logs = %#v", repo.logs)
		}
		if len(repo.failures) != 1 || len(repo.successes) != 0 {
			t.Fatalf("failures = %v, successes = %v", repo.failures, repo.successes)
		}
	})

	t.Run("terminal SSE usage is recorded incrementally", func(t *testing.T) {
		upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"response\":{\"usage\":{\"input_tokens\":13,\"output_tokens\":8,\"input_tokens_details\":{\"cached_tokens\":2}}}}\n\ndata: [DONE]\n\n")
		}))
		defer upstreamServer.Close()

		u := upstream(1, upstreamServer.URL)
		u.IdleTimeout = 100 * time.Millisecond
		repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{u}}
		recorder := serve(NewHandler(repo), http.MethodPost, "/v1/responses", "{\"model\":\"m\",\"stream\":true}", true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if len(repo.logs) != 1 {
			t.Fatalf("logs = %#v", repo.logs)
		}
		usage := repo.logs[0].Usage
		if usage.InputTokens == nil || *usage.InputTokens != 13 || usage.OutputTokens == nil || *usage.OutputTokens != 8 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 2 {
			t.Fatalf("usage = %#v", usage)
		}
	})
}

func TestClientCancellationDoesNotMarkUpstreamFailed(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstreamServer.Close()

	u := upstream(1, upstreamServer.URL)
	u.FirstByteTimeout = time.Second
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{u}}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{\"model\":\"m\",\"stream\":true}"))
	request.Header.Set("Authorization", "Bearer client-secret")
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	time.AfterFunc(20*time.Millisecond, cancel)
	NewHandler(repo).ServeHTTP(httptest.NewRecorder(), request)

	if len(repo.failures) != 0 {
		t.Fatalf("failures = %v", repo.failures)
	}
	if len(repo.logs) != 1 || repo.logs[0].ErrorCode != "client_closed" {
		t.Fatalf("logs = %#v", repo.logs)
	}
}

func TestRepositoryAuthenticationFailureIsNotReportedAsBadKey(t *testing.T) {
	repo := &fakeRepository{authErr: errors.New("database unavailable")}
	recorder := serve(NewHandler(repo), http.MethodPost, "/v1/responses", "{\"model\":\"m\"}", true)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func upstream(id int64, baseURL string) core.Upstream {
	return core.Upstream{ID: id, Name: "upstream", BaseURL: baseURL, APIKey: "secret", Enabled: true, Protocols: []string{core.ProtocolResponses}, Models: []string{"m"}}
}

func serve(handler http.Handler, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		request.Header.Set("Authorization", "Bearer client-secret")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}
