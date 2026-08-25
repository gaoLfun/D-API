package httpapi

import (
	"testing"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestUpstreamPayloadPreservesExplicitModelMode(t *testing.T) {
	existing := core.Upstream{
		APIKey: "saved-key", AccessToken: "saved-token", UserID: "42", ModelsLocked: false,
	}
	input := upstreamPayload{
		Name: "primary", Kind: "newapi", BaseURL: "https://example.com/v1",
		Protocols: []string{core.ProtocolChat}, Models: []string{"discovered-model"},
	}
	upstream, err := input.upstream(7, existing)
	if err != nil {
		t.Fatal(err)
	}
	if upstream.APIKey != "saved-key" || upstream.ModelsLocked {
		t.Fatalf("inherited upstream = %#v", upstream)
	}

	locked := true
	input.ModelsLocked = &locked
	upstream, err = input.upstream(7, existing)
	if err != nil || !upstream.ModelsLocked {
		t.Fatalf("explicit model mode = %#v, %v", upstream, err)
	}
}

func TestSub2APIPayloadDropsNewAPICredentials(t *testing.T) {
	input := upstreamPayload{
		Name: "secondary", Kind: "sub2api", BaseURL: "https://example.com",
		AccessToken: "new-token", UserID: "99", Protocols: []string{core.ProtocolResponses},
	}
	upstream, err := input.upstream(8, core.Upstream{APIKey: "saved-key", AccessToken: "saved-token", UserID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.APIKey != "saved-key" || upstream.AccessToken != "" || upstream.UserID != "" {
		t.Fatalf("sub2api credentials = %#v", upstream)
	}
}
