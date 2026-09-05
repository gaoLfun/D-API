package store

import (
	"context"
	"sync"
)

// A miss waits for an existing load, then rechecks the cache using its own
// context. Invalidation and a canceled loader cannot strand other callers.
type loadGate[K comparable] struct {
	mu     sync.Mutex
	active map[K]chan struct{}
}

func (g *loadGate[K]) acquire(ctx context.Context, key K) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		if g.active == nil {
			g.active = make(map[K]chan struct{})
		}
		done, busy := g.active[key]
		if !busy {
			done = make(chan struct{})
			g.active[key] = done
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				delete(g.active, key)
				close(done)
				g.mu.Unlock()
			}, nil
		}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
	}
}
