package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestHealthDiscoversModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "codex_cli_rs/0.101.0" {
			t.Errorf("User-Agent = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5"},{"id":"claude-sonnet"},{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	result := NewProber(server.Client(), time.Second).CheckHealth(context.Background(), core.Upstream{BaseURL: server.URL + "/v1", APIKey: "secret", UserAgent: "codex_cli_rs/0.101.0"})
	if result.Status != "healthy" || len(result.Models) != 2 || result.Models[0] != "gpt-5" || result.Models[1] != "claude-sonnet" {
		t.Fatalf("health = %#v", result)
	}
}

func TestHealthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	result := NewProber(server.Client(), time.Second).CheckHealth(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if result.Status != "unhealthy" || result.StatusCode != http.StatusServiceUnavailable || result.Error == "" {
		t.Fatalf("health = %#v", result)
	}
}

func TestModelProtocols(t *testing.T) {
	paths := make(chan string, 1)
	pinged := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "opencode/1.0.0" {
			t.Errorf("User-Agent = %q", got)
		}
		if r.Method == http.MethodHead {
			pinged <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		paths <- r.URL.Path
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["model"] != "model-a" || payload["stream"] != false {
			t.Errorf("payload = %#v", payload)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			prompt := assertProbeMessage(t, payload, "max_tokens")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + challengeAnswer(prompt) + `"}}]}`))
		case "/v1/responses":
			prompt, ok := payload["input"].(string)
			if !ok || strings.TrimSpace(prompt) == "" || payload["instructions"] != probeInstructions || payload["max_output_tokens"] != float64(probeMaxTokens) {
				t.Errorf("responses payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"` + challengeAnswer(prompt) + `"}]}]}`))
		case "/v1/messages":
			prompt := assertProbeMessage(t, payload, "max_tokens")
			if r.Header.Get("X-Api-Key") != "secret" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
				t.Errorf("messages headers = %#v", r.Header)
			}
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"` + challengeAnswer(prompt) + `"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	upstream := core.Upstream{
		BaseURL: server.URL + "/v1", APIKey: "secret",
		UserAgent:      "opencode/1.0.0",
		Kind:           "sub2api",
		Protocols:      []string{core.ProtocolMessages, core.ProtocolResponses, core.ProtocolChat},
		ConnectTimeout: time.Second, FirstByteTimeout: time.Second,
	}
	result := NewProber(server.Client(), time.Second).TestModel(context.Background(), upstream, "model-a")
	if result.Status != "available" || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
	select {
	case <-pinged:
	default:
		t.Fatal("sub2api origin was not pinged")
	}
	for i, expected := range []string{"/v1/chat/completions"} {
		if path := <-paths; path != expected || result.Results[i].Status != "success" || result.Results[i].StatusCode != http.StatusOK {
			t.Fatalf("request %d path=%q result=%#v", i, path, result.Results[i])
		}
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "15") || strings.Contains(string(encoded), "Calculate") {
		t.Fatalf("generated content leaked: %s", encoded)
	}
}

func TestModelProbeEndpointSelection(t *testing.T) {
	protocols := []string{core.ProtocolResponses, core.ProtocolChat, core.ProtocolMessages}
	cases := []struct {
		name     string
		kind     string
		model    string
		expected string
	}{
		{name: "newapi regular", kind: "newapi", model: "gpt-5.6", expected: core.ProtocolChat},
		{name: "newapi codex", kind: "newapi", model: "gpt-5.6-codex", expected: core.ProtocolResponses},
		{name: "sub2api default", kind: "sub2api", model: "claude-opus-4", expected: core.ProtocolChat},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			selected := modelProbeProtocols(core.Upstream{Kind: test.kind, Protocols: protocols}, test.model)
			if len(selected) != 1 || selected[0] != test.expected {
				t.Fatalf("selected protocols = %#v, want %q", selected, test.expected)
			}
		})
	}
}

func TestResponsesProbeRequestShapes(t *testing.T) {
	_, sub2apiPayload, sub2apiExpected := modelProbeRequest(core.ProtocolResponses, "sub2api", "model-a")
	if sub2apiExpected == "" {
		t.Fatal("sub2api challenge expected answer is empty")
	}
	sub2apiJSON, err := json.Marshal(sub2apiPayload)
	if err != nil {
		t.Fatal(err)
	}
	var sub2api map[string]any
	if err := json.Unmarshal(sub2apiJSON, &sub2api); err != nil {
		t.Fatal(err)
	}
	if input, ok := sub2api["input"].(string); !ok || strings.TrimSpace(input) == "" || sub2api["instructions"] != probeInstructions {
		t.Fatalf("sub2api payload = %#v", sub2api)
	}

	_, newAPIPayload, _ := modelProbeRequest(core.ProtocolResponses, "newapi", "model-a")
	newAPIJSON, err := json.Marshal(newAPIPayload)
	if err != nil {
		t.Fatal(err)
	}
	var newAPI map[string]any
	if err := json.Unmarshal(newAPIJSON, &newAPI); err != nil {
		t.Fatal(err)
	}
	if _, ok := newAPI["instructions"]; ok {
		t.Fatalf("newapi payload unexpectedly has instructions: %#v", newAPI)
	}
	input, ok := newAPI["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("newapi input = %#v", newAPI["input"])
	}
	message, ok := input[0].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != newAPIProbePrompt {
		t.Fatalf("newapi message = %#v", input[0])
	}
}

func TestModelContentRejectsWrongChallenge(t *testing.T) {
	if err := parseModelContent(core.ProtocolResponses, []byte(`{"output_text":"14"}`), "15"); err == nil {
		t.Fatal("expected challenge mismatch")
	}
	if err := parseModelContent(core.ProtocolResponses, []byte(`{"output_text":"The answer is 15."}`), "15"); err != nil {
		t.Fatalf("expected challenge success: %v", err)
	}
}

func TestModelRequiresNonEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  "}}]}`))
	}))
	defer server.Close()

	result := NewProber(server.Client(), time.Second).TestModel(context.Background(), core.Upstream{
		BaseURL: server.URL, APIKey: "secret", Protocols: []string{core.ProtocolChat},
	}, "model-a")
	if result.Status != "unavailable" || len(result.Results) != 1 || result.Results[0].Status != "failed" || result.Results[0].StatusCode != http.StatusOK || result.Results[0].Error == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestModelHTTPFailures(t *testing.T) {
	tests := []struct {
		status  int
		body    string
		expects string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, "bad key"},
		{http.StatusTooManyRequests, `{"message":"slow down"}`, "slow down"},
		{http.StatusInternalServerError, `{}`, "HTTP 500"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			result := NewProber(server.Client(), time.Second).TestModel(context.Background(), core.Upstream{
				BaseURL: server.URL, APIKey: "secret", Protocols: []string{core.ProtocolResponses},
			}, "model-a")
			if len(result.Results) != 1 || result.Results[0].StatusCode != test.status || result.Results[0].Error != test.expects {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestModelHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ModelTest, 1)
	go func() {
		done <- NewProber(server.Client(), time.Second).TestModel(ctx, core.Upstream{
			BaseURL: server.URL, APIKey: "secret", Protocols: []string{core.ProtocolChat, core.ProtocolResponses},
			ConnectTimeout: time.Second, FirstByteTimeout: time.Second,
		}, "model-a")
	}()
	<-started
	cancel()
	result := <-done
	close(release)
	if requests.Load() != 1 || len(result.Results) != 1 || result.Results[0].Status != "failed" || !strings.Contains(result.Results[0].Error, "context canceled") {
		t.Fatalf("requests=%d result=%#v", requests.Load(), result)
	}
}

func assertProbeMessage(t *testing.T, payload map[string]any, maxField string) string {
	t.Helper()
	if payload[maxField] != float64(probeMaxTokens) {
		t.Errorf("max output = %#v", payload[maxField])
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Errorf("messages = %#v", payload["messages"])
		return ""
	}
	message, ok := messages[0].(map[string]any)
	content, _ := message["content"].(string)
	if !ok || message["role"] != "user" || strings.TrimSpace(content) == "" {
		t.Errorf("message = %#v", messages[0])
		return ""
	}
	return content
}

var challengePattern = regexp.MustCompile(`Q: (\d+) ([+-]) (\d+) = \?`)

func challengeAnswer(prompt string) string {
	matches := challengePattern.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	left, _ := strconv.Atoi(last[1])
	right, _ := strconv.Atoi(last[3])
	if last[2] == "-" {
		return strconv.Itoa(left - right)
	}
	return strconv.Itoa(left + right)
}

func TestNewAPITokenBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			http.NotFound(w, r)
		case "/api/usage/token/":
			_, _ = w.Write([]byte(`{"success":true,"code":0,"data":{"total_available":1500000,"total_used":500000,"unlimited_quota":false}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL + "/v1", APIKey: "secret"})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 3 || balance.Used == nil || *balance.Used != 1 || balance.Currency != "USD" {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestNewAPIUserBalanceCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/self" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("New-Api-User"); got != "42" {
			t.Errorf("New-Api-User = %q", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000,"used_quota":250000,"group":"default"}}`))
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{
		BaseURL: server.URL, APIKey: "secret", AccessToken: "session", UserID: "42",
	})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 2 || balance.Used == nil || *balance.Used != .5 || balance.Plan != "default" {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestSub2APIBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"quota":{"remaining":80,"used":20,"unit":"USD"},"usage":{"total":{"actual_cost":30}}}`))
	}))
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if balance.Status != "ok" || balance.Available == nil || *balance.Available != 80 || balance.Used == nil || *balance.Used != 20 {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestUnknownBalanceWhenUnsupported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	balance := NewProber(server.Client(), time.Second).CheckBalance(context.Background(), core.Upstream{BaseURL: server.URL, APIKey: "secret"})
	if balance.Status != "unknown" || balance.Available != nil || balance.Used != nil {
		t.Fatalf("balance = %#v", balance)
	}
}
