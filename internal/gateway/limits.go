package gateway

import (
	"sync"
	"time"
)

type requestGate struct {
	mu     sync.Mutex
	active int
	byKey  map[int64]int
}

func (g *requestGate) acquire(key int64, limits Limits) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byKey == nil {
		g.byKey = make(map[int64]int)
	}
	if g.active >= limits.MaxConcurrentRequests || g.byKey[key] >= limits.MaxConcurrentPerKey {
		return false
	}
	g.active++
	g.byKey[key]++
	return true
}

func (g *requestGate) release(key int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		g.active--
	}
	if count := g.byKey[key] - 1; count > 0 {
		g.byKey[key] = count
	} else {
		delete(g.byKey, key)
	}
}

type requestRate struct {
	window time.Time
	count  int
}

type requestRateLimiter struct {
	mu          sync.Mutex
	entries     map[int64]requestRate
	lastCleanup time.Time
}

func (l *requestRateLimiter) allow(key int64, max int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[int64]requestRate)
	}
	window := now.UTC().Truncate(time.Minute)

	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for id, candidate := range l.entries {
			if candidate.window.Before(window) {
				delete(l.entries, id)
			}
		}
		l.lastCleanup = now
	}
	entry := l.entries[key]
	if !entry.window.Equal(window) {
		entry = requestRate{window: window}
	}
	if entry.count >= max {
		l.entries[key] = entry
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}
