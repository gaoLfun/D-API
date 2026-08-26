package httpapi

import (
	"testing"
	"time"
)

func TestParseOptionalDate(t *testing.T) {
	if got, err := parseOptionalDate(""); err != nil || !got.IsZero() {
		t.Fatalf("empty date = %v, %v", got, err)
	}
	got, err := parseOptionalDate("2026-08-26")
	if err != nil || !got.Equal(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("parsed date = %v, %v", got, err)
	}
	if _, err := parseOptionalDate("2026-8-26"); err == nil {
		t.Fatal("accepted non-ISO date")
	}
}
