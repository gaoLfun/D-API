package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestUsageCacheHitDoesNotWaitForLoader(t *testing.T) {
	s := &Store{}
	filter := UsageFilter{Days: 1}
	from, to := usageDateRange(filter)
	filter.FromDay, filter.ToDay = &from, &to
	filter.Days = 0
	filter.Dimension = normalizeDimension(filter.Dimension)
	filter.Granularity = normalizeGranularity(filter.Granularity)
	key, _ := json.Marshal(filter)
	s.usageReports.entries = map[string]usageReportEntry{string(key): {body: []byte(`{"items":[]}`), expires: time.Now().Add(time.Minute)}}
	release, err := s.usageReports.loads.acquire(context.Background(), string(key))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	body, err := s.UsageReport(ctx, filter)
	if err != nil || string(body) != `{"items":[]}` {
		t.Fatalf("cache hit waited for loader: %s %v", body, err)
	}
	body[0] = '!'
	copy, err := s.UsageReport(ctx, filter)
	if err != nil || copy[0] != '{' {
		t.Fatal("cache body was mutated")
	}
}
