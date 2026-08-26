package store

import (
	"testing"
	"time"
)

func TestUsageDateRangeIsBounded(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start, end := usageDateRange(UsageFilter{FromDay: &from, ToDay: &to})
	if !end.Equal(to) {
		t.Fatalf("end = %s, want %s", end, to)
	}
	if end.Sub(start) != 364*24*time.Hour {
		t.Fatalf("range = %s, want 365 days", end.Sub(start))
	}
}

func TestUsageDateRangeNormalizesReversedDates(t *testing.T) {
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start, end := usageDateRange(UsageFilter{FromDay: &from, ToDay: &to})
	if !start.Equal(end) {
		t.Fatalf("range = %s..%s, want equal dates", start, end)
	}
}

func TestUsageDateRangeEndsAtExplicitToDay(t *testing.T) {
	to := time.Date(2020, 1, 30, 0, 0, 0, 0, time.UTC)
	start, end := usageDateRange(UsageFilter{Days: 30, ToDay: &to})
	wantStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(to) {
		t.Fatalf("range = %s..%s, want %s..%s", start, end, wantStart, to)
	}
}
