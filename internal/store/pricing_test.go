package store

import (
	"testing"

	"github.com/gaoLfun/dapi/internal/core"
)

func tokenCount(value int64) *int64 { return &value }

func TestCalculateRequestCost(t *testing.T) {
	price := PricingModelPrice{
		InputUSDPerMillion:      2,
		OutputUSDPerMillion:     8,
		CacheReadUSDPerMillion:  0.5,
		CacheWriteUSDPerMillion: 1,
	}
	usage := core.Usage{
		InputTokens:              tokenCount(100000),
		CachedInputTokens:        tokenCount(25000),
		OutputTokens:             tokenCount(50000),
		CacheCreationInputTokens: tokenCount(10000),
	}
	// Uncached input is inferred as input - cached input: 75k.
	got := calculateRequestCost(price, usage)
	want := (75000*2 + 25000*0.5 + 10000*1 + 50000*8) / 1_000_000.0
	if got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCalculateRequestCostClampsNegativeInferredInput(t *testing.T) {
	price := PricingModelPrice{InputUSDPerMillion: 2, OutputUSDPerMillion: 3}
	usage := core.Usage{InputTokens: tokenCount(10), CachedInputTokens: tokenCount(20), OutputTokens: tokenCount(5)}
	if got, want := calculateRequestCost(price, usage), 15.0/1_000_000; got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCalculateRequestCostUsesExplicitUncachedInput(t *testing.T) {
	price := PricingModelPrice{InputUSDPerMillion: 2, OutputUSDPerMillion: 3}
	usage := core.Usage{InputTokens: tokenCount(100), UncachedInputTokens: tokenCount(40), OutputTokens: tokenCount(10)}
	if got, want := calculateRequestCost(price, usage), 110.0/1_000_000; got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCalculateRequestCostWithoutUsageIsZero(t *testing.T) {
	if got := calculateRequestCost(PricingModelPrice{}, core.Usage{}); got != 0 {
		t.Fatalf("cost = %v, want zero", got)
	}
}

func TestNormalizeUsageBaseURL(t *testing.T) {
	tests := map[string]string{
		"HTTPS://API.Example.com:443/v1/": "https://api.example.com/v1",
		"http://api.example.com:80/":      "http://api.example.com",
		"https://api.example.com/v1":      "https://api.example.com/v1",
	}
	for input, want := range tests {
		if got := normalizeUsageBaseURL(input); got != want {
			t.Errorf("normalizeUsageBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}
