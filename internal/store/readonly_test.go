package store

import (
	"encoding/json"
	"github.com/gaoLfun/dapi/internal/core"
	"strings"
	"testing"
	"time"
)

func TestReadonlyBalanceProjection(t *testing.T) {
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	old := now.Add(-31 * time.Minute)
	boundary := now.Add(-30 * time.Minute)
	zero := 0.0
	credits := 42.0
	for _, tt := range []struct {
		name      string
		b         core.Balance
		status    string
		available *float64
		currency  string
	}{
		{"zero", core.Balance{Status: "ok", Available: &zero, Currency: "USD", UpdatedAt: &now}, "ok", &zero, "USD"},
		{"unknown", core.Balance{Status: "unknown"}, "error", nil, ""},
		{"unlimited", core.Balance{Status: "ok", Unlimited: true, Available: &credits, UpdatedAt: &now}, "ok", nil, ""},
		{"CNY", core.Balance{Status: "ok", Available: &credits, Currency: "CNY", UpdatedAt: &now}, "ok", &credits, "CNY"},
		{"credits", core.Balance{Status: "ok", Available: &credits, Currency: "CREDITS", UpdatedAt: &now}, "ok", &credits, "CREDITS"},
		{"stale", core.Balance{Status: "ok", Available: &zero, LastSuccess: &old, UpdatedAt: &now}, "stale", &zero, ""},
		{"boundary", core.Balance{Status: "ok", UpdatedAt: &boundary}, "ok", nil, ""},
		{"no timestamp", core.Balance{Status: "ok"}, "stale", nil, ""},
		{"failure", core.Balance{Status: "unavailable", Available: &credits, Used: &credits, Error: "sensitive-upstream-content", UpdatedAt: &now}, "error", nil, ""},
		{"unsupported", core.Balance{Status: "unknown", Error: "balance API unsupported", UpdatedAt: &now}, "unsupported", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectReadonlyBalance(tt.b, now, 30*time.Minute)
			if got.Status != tt.status {
				t.Fatal("status mismatch")
			}
			if (got.Available == nil) != (tt.available == nil) {
				t.Fatal("unknown balance changed")
			}
			if got.Available != nil && *got.Available != *tt.available {
				t.Fatal("balance mismatch")
			}
			if tt.currency == "" && got.Currency != nil {
				t.Fatal("invented currency")
			}
			if tt.currency != "" && (got.Currency == nil || *got.Currency != tt.currency) {
				t.Fatal("currency mismatch")
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "sensitive-upstream-content") {
				t.Fatal("leaked raw error")
			}
			if got.Available == nil && !strings.Contains(string(raw), `"available":null`) {
				t.Fatal("null field omitted")
			}
			if tt.name == "stale" && !got.UpdatedAt.Equal(old) {
				t.Fatal("response time replaced data time")
			}
		})
	}
}
