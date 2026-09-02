package store

import (
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

const (
	authCacheTTL         = 5 * time.Second
	routeCacheTTL        = 2 * time.Second
	maxAttemptsCacheTTL  = 30 * time.Second
	maxAuthCacheEntries  = 4096
	maxRouteCacheEntries = 1024
)

type authCacheEntry struct {
	key     core.APIKey
	expires time.Time
}

type routeCacheKey struct {
	groupID  int64
	protocol string
	model    string
}

type routeCacheEntry struct {
	upstreams []core.Upstream
	expires   time.Time
}

type maxAttemptsCacheEntry struct {
	value   int
	expires time.Time
}

func (s *Store) cachedAPIKey(hash string, now time.Time) (core.APIKey, bool, uint64) {
	s.cacheMu.RLock()
	entry, ok := s.authCache[hash]
	generation := s.authGen
	s.cacheMu.RUnlock()
	if !ok || !entry.expires.After(now) {
		return core.APIKey{}, false, generation
	}
	return cloneAPIKey(entry.key), true, generation
}

func (s *Store) cacheAPIKey(hash string, key core.APIKey, now time.Time, generation uint64) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.authGen != generation {
		return
	}
	if len(s.authCache) >= maxAuthCacheEntries {
		clear(s.authCache)
	}
	s.authCache[hash] = authCacheEntry{key: cloneAPIKey(key), expires: now.Add(authCacheTTL)}
}

func (s *Store) cachedRoutes(key routeCacheKey, now time.Time) ([]core.Upstream, bool, uint64) {
	s.cacheMu.RLock()
	entry, ok := s.routeCache[key]
	generation := s.routeGen
	s.cacheMu.RUnlock()
	if !ok || !entry.expires.After(now) {
		return nil, false, generation
	}
	return cloneUpstreams(entry.upstreams), true, generation
}

func (s *Store) cacheRoutes(key routeCacheKey, upstreams []core.Upstream, now time.Time, generation uint64) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.routeGen != generation {
		return
	}
	if len(s.routeCache) >= maxRouteCacheEntries {
		clear(s.routeCache)
	}
	s.routeCache[key] = routeCacheEntry{upstreams: cloneUpstreams(upstreams), expires: now.Add(routeCacheTTL)}
}

func (s *Store) cachedMaxAttempts(now time.Time) (int, bool, uint64) {
	s.cacheMu.RLock()
	entry := s.maxAttempts
	generation := s.maxAttemptsGen
	s.cacheMu.RUnlock()
	return entry.value, entry.value > 0 && entry.expires.After(now), generation
}

func (s *Store) cacheMaxAttempts(value int, now time.Time, generation uint64) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.maxAttemptsGen != generation {
		return
	}
	s.maxAttempts = maxAttemptsCacheEntry{value: value, expires: now.Add(maxAttemptsCacheTTL)}
}

func (s *Store) invalidateAuthCache() {
	s.cacheMu.Lock()
	clear(s.authCache)
	s.authGen++
	s.cacheMu.Unlock()
}

func (s *Store) invalidateRouteCache() {
	s.cacheMu.Lock()
	clear(s.routeCache)
	s.routeGen++
	s.cacheMu.Unlock()
}

func (s *Store) invalidateMaxAttemptsCache() {
	s.cacheMu.Lock()
	s.maxAttempts = maxAttemptsCacheEntry{}
	s.maxAttemptsGen++
	s.cacheMu.Unlock()
}

func cloneAPIKey(key core.APIKey) core.APIKey {
	key.Protocols = append([]string(nil), key.Protocols...)
	key.Models = append([]string(nil), key.Models...)
	return key
}

func cloneUpstreams(upstreams []core.Upstream) []core.Upstream {
	result := make([]core.Upstream, len(upstreams))
	for i, upstream := range upstreams {
		upstream.Protocols = append([]string(nil), upstream.Protocols...)
		upstream.Models = append([]string(nil), upstream.Models...)
		if upstream.ModelAliases != nil {
			upstream.ModelAliases = make(map[string]string, len(upstream.ModelAliases))
			for alias, model := range upstreams[i].ModelAliases {
				upstream.ModelAliases[alias] = model
			}
		}
		result[i] = upstream
	}
	return result
}
