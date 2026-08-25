package core

import (
	"testing"
	"time"
)

func TestRouteEligibilityAndAlias(t *testing.T) {
	now := time.Now()
	upstream := Upstream{
		Enabled: true, Protocols: []string{ProtocolResponses}, Models: []string{"gpt-upstream"},
		ModelAliases: map[string]string{"gpt-client": "gpt-upstream"},
	}
	if !upstream.Supports(ProtocolResponses, "gpt-client", now) || upstream.UpstreamModel("gpt-client") != "gpt-upstream" {
		t.Fatal("model alias route rejected")
	}
	if upstream.Supports(ProtocolMessages, "gpt-client", now) {
		t.Fatal("unsupported protocol accepted")
	}
	openUntil := now.Add(time.Minute)
	upstream.CircuitOpenUntil = &openUntil
	if upstream.Supports(ProtocolResponses, "gpt-client", now) {
		t.Fatal("open circuit accepted")
	}
	key := APIKey{Enabled: true, Protocols: []string{ProtocolResponses}, Models: []string{"gpt-client"}}
	if !key.Allows(ProtocolResponses, "gpt-client") || key.Allows(ProtocolChat, "gpt-client") {
		t.Fatal("API key scope failed")
	}
}
