package store

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
)

type usageReportEntry struct {
	body    []byte
	expires time.Time
}
type usageReportCache struct {
	mu      sync.Mutex
	entries map[string]usageReportEntry
	loads   loadGate[string]
}

// UsageReport caches immutable encoded results for five seconds. It uses the
// routing rollups; Dashboard deliberately continues to count every raw request.
func (s *Store) UsageReport(ctx context.Context, filter UsageFilter) ([]byte, error) {
	from, to := usageDateRange(filter)
	filter.FromDay, filter.ToDay = &from, &to
	filter.Days = 0
	filter.Dimension = normalizeDimension(filter.Dimension)
	filter.Granularity = normalizeGranularity(filter.Granularity)
	encoded, _ := json.Marshal(filter)
	key := string(encoded)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if body, ok := s.usageReports.get(key); ok {
		return body, nil
	}
	release, err := s.usageReports.loads.acquire(ctx, key)
	if err != nil {
		return nil, err
	}
	defer release()
	if body, ok := s.usageReports.get(key); ok {
		return body, nil
	}
	rows, err := s.UsageWithFilter(ctx, filter)
	if err != nil {
		return nil, err
	}
	totals, err := s.UsageTotals(ctx, filter)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"daily": rows, "items": rows, "totals": totals, "summary": totals, "dimension": filter.Dimension, "granularity": filter.Granularity, "top_n": filter.TopN})
	if err != nil {
		return nil, err
	}
	s.usageReports.mu.Lock()
	defer s.usageReports.mu.Unlock()
	if s.usageReports.entries == nil {
		s.usageReports.entries = make(map[string]usageReportEntry)
	}
	for k, v := range s.usageReports.entries {
		if time.Now().After(v.expires) {
			delete(s.usageReports.entries, k)
		}
	}
	if len(body) <= 1<<20 && len(s.usageReports.entries) < 32 {
		s.usageReports.entries[key] = usageReportEntry{bytes.Clone(body), time.Now().Add(5 * time.Second)}
	}
	return body, nil
}

func (c *usageReportCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	cached, ok := c.entries[key]
	c.mu.Unlock()
	if !ok || !time.Now().Before(cached.expires) {
		return nil, false
	}
	return bytes.Clone(cached.body), true
}
