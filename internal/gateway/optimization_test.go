package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestSSEMultilineEventsAcrossEverySplit(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		body := "\xef\xbb\xbf" + strings.Join([]string{": heartbeat", "event: response.completed", `data: {"response":{"usage":{"input_tokens":5,`, `data: "output_tokens":2}}}`, "", ""}, ending)
		for split := 0; split <= len(body); split++ {
			parser := sseUsageParser{protocol: core.ProtocolResponses, requireCompletion: true}
			parser.Feed([]byte(body[:split]))
			parser.Feed([]byte(body[split:]))
			usage := parser.Usage()
			if err := parser.StreamError(); err != nil || usage.InputTokens == nil || *usage.InputTokens != 5 || usage.OutputTokens == nil || *usage.OutputTokens != 2 {
				t.Fatalf("ending=%q split=%d usage=%+v err=%v", ending, split, usage, err)
			}
		}
	}
}

func TestSSEIncompleteAndOversizedEventsCannotComplete(t *testing.T) {
	parser := sseUsageParser{protocol: core.ProtocolResponses, requireCompletion: true}
	parser.Feed([]byte("data: {\"type\":\"response.completed\"}\n"))
	parser.Usage()
	if parser.StreamError() == nil {
		t.Fatal("unterminated event completed")
	}
	parser = sseUsageParser{protocol: core.ProtocolChat, requireCompletion: true}
	parser.Feed([]byte("data: " + strings.Repeat("x", maxSSEEventBytes) + "\ndata: [DONE]\n\n"))
	if parser.StreamError() == nil {
		t.Fatal("oversized event completed")
	}
	parser.Feed([]byte("data: [DONE]\n\n"))
	if err := parser.StreamError(); err != nil {
		t.Fatal(err)
	}
}

type deadlineModelsRepository struct{ *fakeRepository }

func (r *deadlineModelsRepository) AvailableModels(ctx context.Context, _ core.APIKey) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestModelsDeadlineAndCancellation(t *testing.T) {
	repo := &deadlineModelsRepository{&fakeRepository{key: core.APIKey{ID: 1, Enabled: true}}}
	h := NewHandler(repo, Limits{MaxRequestDuration: 20 * time.Millisecond})
	w := serve(h, "GET", "/v1/models", "", true)
	if w.Code != 504 || !strings.Contains(w.Body.String(), "request_timeout") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	if w.Body.Len() != 0 {
		t.Fatalf("canceled request wrote %s", w.Body.String())
	}
}

func TestResponseBudgetIsGlobalAndReleasesAfterWriting(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{}`) }))
	defer up.Close()
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}, candidates: []core.Upstream{reliabilityUpstream(1, up.URL)}}
	h := NewHandler(repo, Limits{MaxBufferedResponseBytes: 32 << 10})
	// Hold another request's buffer while this request tries to allocate.
	other := &responseBuffer{budget: &h.responseBudget, limit: h.limits.MaxBufferedResponseBytes}
	if err := other.append([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	w := serve(h, "POST", "/v1/chat/completions", `{"model":"m"}`, true)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "response_budget_exceeded") || len(repo.failures) != 0 || repo.logs[0].Attempts[0].FailureClass != "gateway" {
		t.Fatalf("status=%d logs=%+v failures=%v", w.Code, repo.logs, repo.failures)
	}
	other.release()
	w = serve(h, "POST", "/v1/chat/completions", `{"model":"m"}`, true)
	if w.Code != 200 || w.Body.String() != `{}` || h.responseBudget.current() != 0 {
		t.Fatalf("status=%d budget=%d", w.Code, h.responseBudget.current())
	}
}

func TestResponseGrowthAccountsForBothAllocations(t *testing.T) {
	budget := &byteBudget{}
	buffer := &responseBuffer{budget: budget, limit: 64 << 10}
	defer buffer.release()
	if err := buffer.append(make([]byte, 32<<10)); err != nil {
		t.Fatal(err)
	}
	if err := buffer.append([]byte{1}); !errors.Is(err, errResponseBudget) {
		t.Fatalf("unaccounted growth: %v", err)
	}
	if budget.current() != 32<<10 || len(buffer.data) != 32<<10 {
		t.Fatal("failed growth changed reservation")
	}
}

func TestLogFallbackBoundsWritersAndCountsLoss(t *testing.T) {
	entered := make(chan struct{}, requestLogFallbackConcurrency)
	release := make(chan struct{})
	var active, peak atomic.Int64
	repo := &recordingRepository{fakeRepository: &fakeRepository{}, write: func(ctx context.Context, _ []core.RequestLog) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := newRequestRecorderWithRetryDelays(repo, nil)
	defer r.Close(context.Background())
	defer close(release)
	var workers sync.WaitGroup
	for range requestLogFallbackConcurrency {
		workers.Go(func() { r.fallback(core.RequestLog{}) })
		<-entered
	}
	if err := r.fallback(core.RequestLog{}); err == nil {
		t.Fatal("expected saturated fallback to time out")
	}
	if peak.Load() != requestLogFallbackConcurrency || r.fallbackRejected.Load() != 1 || r.Dropped() != 1 || r.fallbackWaiting.Load() != 0 || r.fallbackWaitNS.Load() == 0 {
		t.Fatalf("peak=%d rejected=%d dropped=%d", peak.Load(), r.fallbackRejected.Load(), r.Dropped())
	}
	// Release the known writers without waiting for their database timeouts.
	for range requestLogFallbackConcurrency {
		release <- struct{}{}
	}
	workers.Wait()
	if len(r.fallbackSlots) != 0 {
		t.Fatal("fallback slot leaked")
	}
}
