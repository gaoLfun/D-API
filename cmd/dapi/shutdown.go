package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type requestDrain struct {
	mu      sync.Mutex
	active  int
	closing bool
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

func newRequestDrain() *requestDrain {
	ctx, cancel := context.WithCancel(context.Background())
	return &requestDrain{done: make(chan struct{}), ctx: ctx, cancel: cancel}
}

func (d *requestDrain) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		if d.closing {
			d.mu.Unlock()
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}
		d.active++
		d.mu.Unlock()
		defer func() {
			d.mu.Lock()
			d.active--
			if d.closing && d.active == 0 {
				close(d.done)
			}
			d.mu.Unlock()
		}()
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(d.ctx, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (d *requestDrain) begin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closing {
		d.closing = true
		if d.active == 0 {
			close(d.done)
		}
	}
}

type logCloser interface{ Close(context.Context) error }

func shutdownServer(server *http.Server, drain *requestDrain, logs logCloser, grace, settle, flush time.Duration) error {
	drain.begin()
	defer drain.cancel()
	graceCtx, cancelGrace := context.WithTimeout(context.Background(), grace)
	err := server.Shutdown(graceCtx)
	cancelGrace()
	if err != nil {
		drain.cancel()
		err = errors.Join(err, server.Close())
	}
	settleCtx, cancelSettle := context.WithTimeout(context.Background(), settle)
	defer cancelSettle()
	select {
	case <-drain.done:
	case <-settleCtx.Done():
		err = errors.Join(err, settleCtx.Err())
	}
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), flush)
	defer cancelFlush()
	return errors.Join(err, logs.Close(flushCtx))
}
