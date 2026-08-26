package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/ops"
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

func TestUpstreamPayloadPreservesPricingAndAcceptsZeroPriority(t *testing.T) {
	profileID := int64(9)
	existing := core.Upstream{APIKey: "saved-key", Priority: 15, PricingProfileID: &profileID}
	input := upstreamPayload{
		Name: "primary", Kind: "newapi", BaseURL: "https://example.com",
		Protocols: []string{core.ProtocolChat},
	}
	upstream, err := input.upstream(7, existing)
	if err != nil || upstream.Priority != 15 || upstream.PricingProfileID == nil || *upstream.PricingProfileID != profileID {
		t.Fatalf("preserved upstream = %#v, %v", upstream, err)
	}

	zero := 0
	input.Priority = &zero
	upstream, err = input.upstream(7, existing)
	if err != nil || upstream.Priority != 0 {
		t.Fatalf("zero priority upstream = %#v, %v", upstream, err)
	}
}

type modelTestOperations struct {
	upstream core.Upstream
	model    string
}

func (o *modelTestOperations) Check(context.Context, int64) (ops.Health, error) {
	return ops.Health{}, nil
}

func (o *modelTestOperations) Probe(context.Context, core.Upstream) ops.Health {
	return ops.Health{}
}

func (o *modelTestOperations) TestModel(_ context.Context, upstream core.Upstream, model string) ops.ModelTest {
	o.upstream, o.model = upstream, model
	return ops.ModelTest{Model: model, Status: "available", Results: []ops.ModelProbe{{Protocol: core.ProtocolChat, Status: "success", StatusCode: 200}}}
}

func (o *modelTestOperations) Balance(context.Context, int64) (core.Balance, error) {
	return core.Balance{}, nil
}

func (o *modelTestOperations) Models(context.Context, int64) ([]string, error) {
	return nil, nil
}

func TestModelEndpointUsesDraftCredentials(t *testing.T) {
	operations := &modelTestOperations{}
	server := &Server{operations: operations}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/upstreams/test-model", strings.NewReader(`{
		"name":"draft","kind":"newapi","base_url":"https://example.com/v1","api_key":"draft-secret",
		"protocols":["chat"],"model":"model-a","audit":false
	}`))
	recorder := httptest.NewRecorder()
	server.testModel(recorder, request)
	if recorder.Code != http.StatusOK || operations.model != "model-a" || operations.upstream.APIKey != "draft-secret" {
		t.Fatalf("code=%d model=%q upstream=%#v body=%s", recorder.Code, operations.model, operations.upstream, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "draft-secret") || strings.Contains(recorder.Body.String(), "OK") {
		t.Fatalf("secret or generated content leaked: %s", recorder.Body.String())
	}
}

func TestModelAuditRejectsUntrustedDetail(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/upstreams/test-models/audit", strings.NewReader(`{
		"id":1,"name":"primary","models_count":1,"protocol_requests":1,
		"available":1,"partial":0,"unavailable":0,"stopped":false,"api_key":"secret"
	}`))
	recorder := httptest.NewRecorder()
	server.auditModelTests(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestModelAuditValidatesSummary(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/upstreams/test-models/audit", strings.NewReader(`{
		"id":1,"name":"primary","models_count":1,"protocol_requests":2,
		"available":1,"partial":1,"unavailable":0,"stopped":false
	}`))
	recorder := httptest.NewRecorder()
	server.auditModelTests(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestModelAuditResultsExcludeUpstreamError(t *testing.T) {
	results := modelTestAuditResults([]ops.ModelProbe{{
		Protocol: core.ProtocolChat, Status: "failed", StatusCode: http.StatusUnauthorized,
		LatencyMS: 12, Error: "invalid key draft-secret and generated OK",
	}})
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "draft-secret") || strings.Contains(string(encoded), "generated OK") {
		t.Fatalf("sensitive upstream error leaked into audit: %s", encoded)
	}
}

var _ Operations = (*modelTestOperations)(nil)
