package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

const (
	requestLogQueueSize    = 2048
	requestLogBatchSize    = 64
	requestLogFlushEvery   = 100 * time.Millisecond
	requestLogWriteTimeout = 2 * time.Second
)

var defaultRequestLogRetryDelays = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond}

type requestRecorder struct {
	repo        Repository
	queue       chan core.RequestLog
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.RWMutex
	closed      bool
	retryDelays []time.Duration
	errMu       sync.Mutex
	flushErr    error
	dropped     atomic.Uint64
}

func newRequestRecorder(repo Repository) *requestRecorder {
	return newRequestRecorderWithRetryDelays(repo, defaultRequestLogRetryDelays)
}

func newRequestRecorderWithRetryDelays(repo Repository, retryDelays []time.Duration) *requestRecorder {
	ctx, cancel := context.WithCancel(context.Background())
	r := &requestRecorder{
		repo: repo, queue: make(chan core.RequestLog, requestLogQueueSize), done: make(chan struct{}),
		ctx: ctx, cancel: cancel, retryDelays: append([]time.Duration(nil), retryDelays...),
	}
	go r.run()
	return r
}

// Submit returns false when the queue is full or closing. The caller then
// writes synchronously, providing bounded backpressure without losing logs.
func (r *requestRecorder) Submit(entry core.RequestLog) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false
	}
	select {
	case r.queue <- entry:
		return true
	default:
		return false
	}
}

func (r *requestRecorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		r.cancel()
		return r.flushError()
	case <-ctx.Done():
		r.cancel()
		<-r.done
		return errors.Join(ctx.Err(), r.flushError())
	}
}

func (r *requestRecorder) run() {
	defer close(r.done)
	ticker := time.NewTicker(requestLogFlushEvery)
	defer ticker.Stop()
	batch := make([]core.RequestLog, 0, requestLogBatchSize)
	for {
		if r.ctx.Err() != nil {
			r.dropRemaining(batch)
			return
		}
		select {
		case <-r.ctx.Done():
			r.dropRemaining(batch)
			return
		case entry, ok := <-r.queue:
			if !ok {
				r.rememberFlushError(r.flush(batch))
				return
			}
			batch = append(batch, entry)
			if len(batch) >= requestLogBatchSize {
				r.rememberFlushError(r.flush(batch))
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.rememberFlushError(r.flush(batch))
				batch = batch[:0]
			}
		}
	}
}

func (r *requestRecorder) dropRemaining(batch []core.RequestLog) {
	dropped := uint64(len(batch))
	for range r.queue {
		dropped++
	}
	if dropped == 0 {
		return
	}
	total := r.dropped.Add(dropped)
	err := fmt.Errorf("request log shutdown interrupted: %w", r.ctx.Err())
	r.rememberFlushError(err)
	slog.Error("request logs dropped during shutdown", "count", dropped, "dropped_total", total, "error", err)
}

func (r *requestRecorder) rememberFlushError(err error) {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	if err != nil && r.flushErr == nil {
		r.flushErr = err
	}
}

func (r *requestRecorder) flushError() error {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	return r.flushErr
}

// Data and constraint errors will not improve on retry. Split only these
// failures so database outages do not multiply writes for every queued entry.
func isRecordDataError(err error) bool {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return false
	}
	code := state.SQLState()
	return len(code) >= 2 && (code[:2] == "22" || code[:2] == "23")
}

func (r *requestRecorder) flush(entries []core.RequestLog) error {
	if len(entries) == 0 {
		return nil
	}
	if repo, ok := r.repo.(batchRepository); ok {
		var err error
		for attempt := 0; attempt <= len(r.retryDelays); attempt++ {
			ctx, cancel := context.WithTimeout(r.ctx, requestLogWriteTimeout)
			err = repo.RecordRequests(ctx, entries)
			cancel()
			if err == nil {
				return nil
			}
			if r.ctx.Err() == nil && isRecordDataError(err) {
				if len(entries) > 1 {
					middle := len(entries) / 2
					leftErr := r.flush(entries[:middle])
					rightErr := r.flush(entries[middle:])
					return errors.Join(leftErr, rightErr)
				}
				break
			}
			if r.ctx.Err() != nil {
				break
			}
			if attempt < len(r.retryDelays) {
				timer := time.NewTimer(r.retryDelays[attempt])
				select {
				case <-timer.C:
				case <-r.ctx.Done():
					timer.Stop()
				}
			}
		}
		total := r.dropped.Add(uint64(len(entries)))
		slog.Error("request log batch dropped after retries", "count", len(entries), "dropped_total", total, "error", err)
		return fmt.Errorf("write request log batch after retries: %w", err)
	}
	var firstErr error
	var dropped uint64
	for _, entry := range entries {
		ctx, cancel := context.WithTimeout(r.ctx, requestLogWriteTimeout)
		if err := r.repo.RecordRequest(ctx, entry); err != nil {
			slog.Error("request log write failed", "request_id", entry.RequestID, "error", err)
			dropped++
			if firstErr == nil {
				firstErr = err
			}
		}
		cancel()
	}
	if dropped > 0 {
		r.dropped.Add(dropped)
		return fmt.Errorf("write %d request logs: %w", dropped, firstErr)
	}
	return nil
}

func (r *requestRecorder) Dropped() uint64 { return r.dropped.Load() }
