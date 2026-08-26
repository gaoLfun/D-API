package netguard

import (
	"context"
	"testing"
)

func TestValidateURLBlocksPrivateDestinations(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "https://169.254.169.254/latest", "http://localhost:80", "http://service:8080"} {
		if err := ValidateURL(raw); err == nil {
			t.Fatalf("ValidateURL(%q) allowed a private destination", raw)
		}
	}
	if err := ValidateURL("https://api.example.com/v1"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}

func TestDialerBlocksLiteralPrivateAddress(t *testing.T) {
	if _, err := (&Dialer{}).DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("dialer allowed loopback")
	}
}
