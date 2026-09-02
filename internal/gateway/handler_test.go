package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type fakeRepository struct {
	mu             sync.Mutex
	key            core.APIKey
	authErr        error
	candidates     []core.Upstream
	candidateGroup int64
	models         []string
	maxAttempts    int
	logs           []core.RequestLog
	successes      []int64
	failures       []int64
	batches        int
	batchFailures  int
	batchErr       error
}

type blockingHealthRepository struct {
	*fakeRepository
	failureStarted chan struct{}
	releaseFailure chan struct{}
}

func (r *blockingHealthRepository) MarkUpstreamFailure(ctx context.Context, id int64, status int, reason string) error {
	close(r.failureStarted)
	select {
	case <-r.releaseFailure:
		return r.fakeRepository.MarkUpstreamFailure(ctx, id, status, reason)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type blockingBatchRepository struct {
	*fakeRepository
	started chan struct{}
}

func (r *blockingBatchRepository) RecordRequests(ctx context.Context, _ []core.RequestLog) error {
	close(r.started)
	<-ctx.Done()
	return ctx.Err()
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

func (f *fakeRepository) Candidates(_ context.Context, groupID int64, _ string, _ string) ([]core.Upstream, error) {
	f.candidateGroup = groupID
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

func (f *fakeRepository) RecordRequests(_ context.Context, entries []core.RequestLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches++
	if f.batchFailures > 0 {
		f.batchFailures--
		return f.batchErr
	}
	f.logs = append(f.logs, entries...)
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

func TestUpstreamRequestUserAgentPolicy(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "http://dapi.local/v1/responses", strings.NewReader(`{}`))
	in.Header.Set("User-Agent", "caller/1.0")

	out, err := upstreamRequest(in, core.Upstream{BaseURL: "https://upstream.example/v1", APIKey: "secret", UserAgent: "codex_cli_rs/0.101.0"}, []byte(`{}`), core.ProtocolResponses)
	if err != nil || out.Header.Get("User-Agent") != "codex_cli_rs/0.101.0" {
		t.Fatalf("configured User-Agent = %q, %v", out.Header.Get("User-Agent"), err)
	}

	out, err = upstreamRequest(in, core.Upstream{BaseURL: "https://upstream.example/v1", APIKey: "secret"}, []byte(`{}`), core.ProtocolResponses)
	if err != nil || out.Header.Get("User-Agent") != "caller/1.0" {
		t.Fatalf("forwarded User-Agent = %q, %v", out.Header.Get("User-Agent"), err)
	}
}

func TestHealthyUpstreamSuccessSkipsStateWrite(t *testing.T) {
	tests := []struct {
		name     string
		upstream core.Upstream
		want     bool
	}{
		{name: "healthy", upstream: core.Upstream{HealthStatus: "healthy"}},
		{name: "healthy after failure", upstream: core.Upstream{HealthStatus: "healthy", ConsecutiveFailure: 1}, want: true},
		{name: "degraded", upstream: core.Upstream{HealthStatus: "degraded"}, want: true},
		{name: "unknown", upstream: core.Upstream{HealthStatus: "unknown"}, want: true},
		{name: "unhealthy awaits probe", upstream: core.Upstream{HealthStatus: "unhealthy"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := needsSuccessWrite(test.upstream); got != test.want {
				t.Fatalf("needsSuccessWrite() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSuccessWritesAfterConcurrentFailureFromHealthySnapshot(t *testing.T) {
	repo := &blockingHealthRepository{
		fakeRepository: &fakeRepository{}, failureStarted: make(chan struct{}), releaseFailure: make(chan struct{}),
	}
	handler := NewHandler(repo)
	upstream := core.Upstream{ID: 7, HealthStatus: "healthy"}
	failureDone := make(chan struct{})
	go func() {
		handler.markFailure(upstream.ID, http.StatusBadGateway, "failed")
		close(failureDone)
	}()
	<-repo.failureStarted
	successDone := make(chan struct{})
	go func() {
		handler.markSuccess(upstream)
		close(successDone)
	}()
	select {
	case <-successDone:
		t.Fatal("success bypassed an in-flight failure write")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.releaseFailure)
	<-failureDone
	<-successDone
	handler.markSuccess(upstream)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.failures) != 1 || len(repo.successes) != 1 {
		t.Fatalf("failures=%v successes=%v", repo.failures, repo.successes)
	}
	handler.healthMu.Lock()
	defer handler.healthMu.Unlock()
	if len(handler.health) != 0 {
		t.Fatalf("idle health states=%d, want 0", len(handler.health))
	}
}

func TestRequestRecorderFlushesBatchOnClose(t *testing.T) {
	repo := &fakeRepository{}
	recorder := newRequestRecorder(repo)
	for i := range 3 {
		if !recorder.Submit(core.RequestLog{RequestID: strconv.Itoa(i)}) {
			t.Fatal("Submit() rejected an available queue slot")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.logs) != 3 || repo.batches != 1 {
		t.Fatalf("logs = %d, batches = %d", len(repo.logs), repo.batches)
	}
	if recorder.Submit(core.RequestLog{}) {
		t.Fatal("Submit() accepted an entry after Close()")
	}
}

func TestRequestRecorderRetriesBatch(t *testing.T) {
	repo := &fakeRepository{batchFailures: 1, batchErr: errors.New("temporary database error")}
	recorder := newRequestRecorderWithRetryDelays(repo, []time.Duration{0})
	if !recorder.Submit(core.RequestLog{RequestID: "retry"}) {
		t.Fatal("Submit() rejected an available queue slot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if repo.batches != 2 || len(repo.logs) != 1 || recorder.Dropped() != 0 {
		t.Fatalf("batches=%d logs=%d dropped=%d", repo.batches, len(repo.logs), recorder.Dropped())
	}
}

func TestRequestRecorderReportsDroppedBatch(t *testing.T) {
	repo := &fakeRepository{batchFailures: 1, batchErr: errors.New("database unavailable")}
	recorder := newRequestRecorderWithRetryDelays(repo, nil)
	if !recorder.Submit(core.RequestLog{RequestID: "dropped"}) {
		t.Fatal("Submit() rejected an available queue slot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err == nil {
		t.Fatal("Close() did not report a permanently failed flush")
	}
	if recorder.Dropped() != 1 {
		t.Fatalf("dropped=%d", recorder.Dropped())
	}
}

func TestRequestRecorderStopsWorkerWhenCloseDeadlineExpires(t *testing.T) {
	repo := &blockingBatchRepository{fakeRepository: &fakeRepository{}, started: make(chan struct{})}
	recorder := newRequestRecorderWithRetryDelays(repo, defaultRequestLogRetryDelays)
	for i := range requestLogBatchSize + 6 {
		if !recorder.Submit(core.RequestLog{RequestID: strconv.Itoa(i)}) {
			t.Fatal("Submit() rejected an available queue slot")
		}
	}
	<-repo.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recorder.Close(ctx); err == nil {
		t.Fatal("Close() did not report its expired deadline")
	}
	if got := recorder.Dropped(); got != requestLogBatchSize+6 {
		t.Fatalf("dropped=%d, want %d", got, requestLogBatchSize+6)
	}
	select {
	case <-recorder.done:
	default:
		t.Fatal("Close() returned before the recorder worker stopped")
	}
}

type benchmarkRepository struct{ *fakeRepository }

func (*benchmarkRepository) RecordRequest(context.Context, core.RequestLog) error { return nil }

func (*benchmarkRepository) RecordRequests(context.Context, []core.RequestLog) error { return nil }

func BenchmarkHealthyProxyRequest(b *testing.B) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","usage":{"input_tokens":10,"output_tokens":4}}`)
	}))
	b.Cleanup(upstreamServer.Close)
	u := upstream(1, upstreamServer.URL)
	u.HealthStatus = "healthy"
	repo := &benchmarkRepository{fakeRepository: &fakeRepository{
		key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{u},
	}}
	handler := NewHandler(repo, Limits{MaxRequestsPerMinute: 100000})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, requestWithAuth(http.MethodPost, "/v1/responses", `{"model":"m"}`))
		if recorder.Code != http.StatusOK {
			b.Fatalf("status = %d", recorder.Code)
		}
	}
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
		key: core.APIKey{ID: 9, GroupID: 42, Enabled: true, Protocols: []string{core.ProtocolChat}, Models: []string{"public-model"}},
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
	if repo.candidateGroup != 42 || repo.logs[0].GroupID == nil || *repo.logs[0].GroupID != 42 {
		t.Fatalf("group routing metadata = %d, %#v", repo.candidateGroup, repo.logs[0].GroupID)
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

func TestProxyDoesNotForwardClientSessionOrProxyHeaders(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Real-IP"} {
			if value := r.Header.Get(name); value != "" {
				t.Errorf("sensitive header %s was forwarded: %q", name, value)
			}
		}
		w.Header().Set("Set-Cookie", "session=upstream")
		w.Header().Set("Location", "https://upstream.invalid")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstreamServer.Close()

	repo := &fakeRepository{
		key:        core.APIKey{ID: 1, Enabled: true, Protocols: []string{core.ProtocolResponses}},
		candidates: []core.Upstream{upstream(1, upstreamServer.URL)},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m"}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	request.Header.Set("Cookie", "dapi_session=secret")
	request.Header.Set("Forwarded", "for=10.0.0.1")
	request.Header.Set("X-Forwarded-For", "10.0.0.1")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Real-IP", "10.0.0.1")
	recorder := httptest.NewRecorder()
	NewHandler(repo).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Set-Cookie") != "" || recorder.Header().Get("Location") != "" {
		t.Fatalf("unexpected response: status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestGatewayLimitsConcurrentRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstreamServer.Close()
	repo := &fakeRepository{
		key:        core.APIKey{ID: 7, Enabled: true, Protocols: []string{core.ProtocolResponses}},
		candidates: []core.Upstream{upstream(1, upstreamServer.URL)},
	}
	h := NewHandler(repo, Limits{MaxConcurrentRequests: 1, MaxConcurrentPerKey: 1, MaxRequestsPerMinute: 10})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, requestWithAuth(http.MethodPost, "/v1/responses", `{"model":"m"}`))
		firstDone <- recorder
	}()
	<-started
	second := httptest.NewRecorder()
	h.ServeHTTP(second, requestWithAuth(http.MethodPost, "/v1/responses", `{"model":"m"}`))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, body = %s", second.Code, second.Body.String())
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first request status = %d", first.Code)
	}
}

func requestWithAuth(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer client-secret")
	return request
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
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
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
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"response\":{\"usage\":{\"input_tokens\":13,\"output_tokens\":8,\"input_tokens_details\":{\"cached_tokens\":2},\"cache_creation_input_tokens\":4}}}\n\ndata: [DONE]\n\n")
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
		if usage.InputTokens == nil || *usage.InputTokens != 13 || usage.OutputTokens == nil || *usage.OutputTokens != 8 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 2 || usage.CacheCreationInputTokens == nil || *usage.CacheCreationInputTokens != 4 || usage.UncachedInputTokens == nil || *usage.UncachedInputTokens != 11 {
			t.Fatalf("usage = %#v", usage)
		}
		if repo.logs[0].TTFBMS == nil || repo.logs[0].TTFTMS == nil {
			t.Fatalf("timing = ttfb:%v ttft:%v", repo.logs[0].TTFBMS, repo.logs[0].TTFTMS)
		}
	})
}

func TestSSEParserDetectsProtocolTextDeltas(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		event    string
	}{
		{name: "responses", protocol: core.ProtocolResponses, event: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"},
		{name: "chat", protocol: core.ProtocolChat, event: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"},
		{name: "messages", protocol: core.ProtocolMessages, event: "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := sseUsageParser{protocol: test.protocol}
			parser.Feed([]byte(test.event))
			if !parser.HasText() {
				t.Fatalf("parser did not detect %s text delta", test.protocol)
			}
		})
	}
	usage := parseUsageWithProtocol([]byte(`{"usage":{"input_tokens":10,"cache_read_input_tokens":5}}`), core.ProtocolMessages)
	if usage.UncachedInputTokens == nil || *usage.UncachedInputTokens != 10 {
		t.Fatalf("Anthropic uncached input = %#v", usage.UncachedInputTokens)
	}
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
