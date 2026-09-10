package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type byteBudget struct {
	mu   sync.Mutex
	used int64
}

func (b *byteBudget) acquire(size, limit int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > limit-b.used {
		return false
	}
	b.used += size
	return true
}
func (b *byteBudget) release(size int64) { b.mu.Lock(); b.used -= size; b.mu.Unlock() }
func (b *byteBudget) current() int64     { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

func setDownstreamDeadline(ctx context.Context, w http.ResponseWriter, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	err := http.NewResponseController(w).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

type RuntimeMetrics struct {
	TransportCacheEntries int    `json:"transport_cache_entries"`
	TransportCacheLimit   int    `json:"transport_cache_limit"`
	BufferedResponseBytes int64  `json:"buffered_response_bytes"`
	BufferedResponseLimit int64  `json:"buffered_response_limit"`
	LogFallbackActive     int    `json:"log_fallback_active"`
	LogFallbackWaiting    int64  `json:"log_fallback_waiting"`
	LogFallbackCount      uint64 `json:"log_fallback_count"`
	LogFallbackRejected   uint64 `json:"log_fallback_rejected"`
	LogFallbackWaitMS     int64  `json:"log_fallback_wait_ms"`
	LogFallbackDurationMS int64  `json:"log_fallback_duration_ms"`
	ActiveRequests        int    `json:"active_requests"`
	BufferedRequestBytes  int64  `json:"buffered_request_bytes"`
	BufferedRequestLimit  int64  `json:"buffered_request_limit"`
	RequestLogQueue       int    `json:"request_log_queue"`
	DroppedRequestLogs    uint64 `json:"dropped_request_logs"`
}

func (h *Handler) Metrics() any {
	h.gate.mu.Lock()
	active := h.gate.active
	h.gate.mu.Unlock()
	m := RuntimeMetrics{BufferedResponseBytes: h.responseBudget.current(), BufferedResponseLimit: h.limits.MaxBufferedResponseBytes, ActiveRequests: active, BufferedRequestBytes: h.bodyBudget.current(), BufferedRequestLimit: h.limits.MaxBufferedRequestBytes, DroppedRequestLogs: h.DroppedRequestLogs()}
	h.mu.Lock()
	m.TransportCacheEntries = len(h.transports)
	m.TransportCacheLimit = maxCachedTransports
	h.mu.Unlock()
	if h.recorder != nil {
		m.RequestLogQueue = len(h.recorder.queue)
		m.LogFallbackActive = len(h.recorder.fallbackSlots)
		m.LogFallbackWaiting = h.recorder.fallbackWaiting.Load()
		m.LogFallbackCount = h.recorder.fallbackCount.Load()
		m.LogFallbackRejected = h.recorder.fallbackRejected.Load()
		m.LogFallbackWaitMS = h.recorder.fallbackWaitNS.Load() / int64(time.Millisecond)
		m.LogFallbackDurationMS = h.recorder.fallbackDurationNS.Load() / int64(time.Millisecond)
	}
	return m
}

// Keep client disconnects and gateway deadlines out of upstream health metrics.
func failureClass(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, errResponseBudget) {
		return "gateway"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, errClientClosed) {
		return "client"
	}
	return "upstream"
}

var errResponseBudget = errors.New("response memory budget exhausted")

// Account for capacity, including both old and new allocations during growth.
// This bounds live response buffers, not the Go runtime's total RSS.
type responseBuffer struct {
	data     []byte
	budget   *byteBudget
	limit    int64
	reserved int64
}

func (b *responseBuffer) append(chunk []byte) error {
	needed := len(b.data) + len(chunk)
	if needed > cap(b.data) {
		next := min(maxBodyBytes, max(needed, max(32<<10, 2*cap(b.data))))
		if b.budget != nil && !b.budget.acquire(int64(next), b.limit) {
			return errResponseBudget
		}
		grown := make([]byte, len(b.data), next)
		copy(grown, b.data)
		if b.budget != nil {
			b.budget.release(b.reserved)
			b.reserved = int64(next)
		}
		b.data = grown
	}
	b.data = append(b.data, chunk...)
	return nil
}
func (b *responseBuffer) release() {
	if b.budget != nil {
		b.budget.release(b.reserved)
	}
	b.reserved = 0
	b.data = nil
}
