package store

import (
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestRouteCacheReturnsIndependentCopies(t *testing.T) {
	store := &Store{routeCache: make(map[routeCacheKey]routeCacheEntry)}
	key := routeCacheKey{groupID: 1, protocol: "responses", model: "model"}
	upstreams := []core.Upstream{{
		ID: 1, Protocols: []string{"responses"}, Models: []string{"model"},
		ModelAliases: map[string]string{"alias": "model"},
	}}
	now := time.Now()
	_, _, generation := store.cachedRoutes(key, now)
	store.cacheRoutes(key, upstreams, now, generation)

	first, ok, _ := store.cachedRoutes(key, now)
	if !ok {
		t.Fatal("cachedRoutes() missed a fresh entry")
	}
	first[0].Protocols[0] = "changed"
	first[0].Models[0] = "changed"
	first[0].ModelAliases["alias"] = "changed"

	second, ok, _ := store.cachedRoutes(key, now)
	if !ok || second[0].Protocols[0] != "responses" || second[0].Models[0] != "model" || second[0].ModelAliases["alias"] != "model" {
		t.Fatalf("cached value was mutated: %#v", second)
	}
}

func TestRouteCacheIsBounded(t *testing.T) {
	store := &Store{routeCache: make(map[routeCacheKey]routeCacheEntry)}
	now := time.Now()
	for i := range maxRouteCacheEntries + 1 {
		_, _, generation := store.cachedRoutes(routeCacheKey{groupID: int64(i)}, now)
		store.cacheRoutes(routeCacheKey{groupID: int64(i)}, nil, now, generation)
	}
	if got := len(store.routeCache); got > maxRouteCacheEntries {
		t.Fatalf("route cache size = %d", got)
	}
}

func TestInvalidationPreventsStaleCacheRefill(t *testing.T) {
	now := time.Now()
	store := &Store{
		authCache:  make(map[string]authCacheEntry),
		routeCache: make(map[routeCacheKey]routeCacheEntry),
	}

	_, _, authGeneration := store.cachedAPIKey("hash", now)
	store.invalidateAuthCache()
	store.cacheAPIKey("hash", core.APIKey{ID: 1}, now, authGeneration)
	if _, ok, _ := store.cachedAPIKey("hash", now); ok {
		t.Fatal("stale API key query refilled an invalidated cache")
	}

	routeKey := routeCacheKey{groupID: 1, protocol: "responses", model: "model"}
	_, _, routeGeneration := store.cachedRoutes(routeKey, now)
	store.invalidateRouteCache()
	store.cacheRoutes(routeKey, []core.Upstream{{ID: 1}}, now, routeGeneration)
	if _, ok, _ := store.cachedRoutes(routeKey, now); ok {
		t.Fatal("stale route query refilled an invalidated cache")
	}

	_, _, settingsGeneration := store.cachedMaxAttempts(now)
	store.invalidateMaxAttemptsCache()
	store.cacheMaxAttempts(3, now, settingsGeneration)
	if _, ok, _ := store.cachedMaxAttempts(now); ok {
		t.Fatal("stale settings query refilled an invalidated cache")
	}
}

func BenchmarkRouteCacheHit(b *testing.B) {
	store := &Store{routeCache: make(map[routeCacheKey]routeCacheEntry)}
	key := routeCacheKey{groupID: 1, protocol: "responses", model: "model"}
	upstreams := []core.Upstream{{
		ID: 1, Protocols: []string{"responses"}, Models: []string{"model"},
		ModelAliases: map[string]string{"alias": "model"},
	}}
	now := time.Now()
	_, _, generation := store.cachedRoutes(key, now)
	store.cacheRoutes(key, upstreams, now, generation)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, ok, _ := store.cachedRoutes(key, now)
		if !ok || len(result) != 1 {
			b.Fatal("cache miss")
		}
	}
}
