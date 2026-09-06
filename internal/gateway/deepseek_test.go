package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestDeepSeekThroughCompatibleUpstream(t *testing.T) {
	for _, kind := range []string{"newapi", "sub2api"} {
		for _, stream := range []bool{false, true} {
			t.Run(kind+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				body := `{"model":"deepseek-reasoner","messages":[{"role":"user","content":"hi"}],"stream":false}`
				response := `{"choices":[{"message":{"content":"hello","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`
				if stream {
					body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
					response = "data: " + `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n\n" +
						"data: " + `{"choices":[{"delta":{"content":"hello"}}]}` + "\n\n" +
						"data: " + `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}` + "\n\ndata: [DONE]\n\n"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got, _ := io.ReadAll(r.Body)
					if string(got) != body || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
						t.Error("request was not forwarded with the expected model, payload, path and authentication")
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					_, _ = io.WriteString(w, response)
				}))
				defer upstream.Close()
				repo := &fakeRepository{
					key:        core.APIKey{ID: 1, GroupID: 1, Enabled: true, Protocols: []string{core.ProtocolChat}},
					candidates: []core.Upstream{{ID: 1, Kind: kind, Name: kind, BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Enabled: true, Protocols: []string{core.ProtocolChat}, Models: []string{"deepseek-reasoner"}}},
				}
				recorder := serve(NewHandler(repo), http.MethodPost, "/v1/chat/completions", body, true)
				if recorder.Code != http.StatusOK || recorder.Body.String() != response {
					t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
				}
				if len(repo.logs) != 1 {
					t.Fatalf("logs=%d", len(repo.logs))
				}
				usage := repo.logs[0].Usage
				if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 80 || usage.UncachedInputTokens == nil || *usage.UncachedInputTokens != 20 {
					t.Fatalf("usage=%+v", usage)
				}
			})
		}
	}
}

func TestDeepSeekCacheDoesNotOverrideNormalizedUsage(t *testing.T) {
	usage := parseUsageWithProtocol([]byte(`{"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":80,"prompt_tokens_details":{"cached_tokens":0}}}`), core.ProtocolChat)
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 0 || *usage.UncachedInputTokens != 100 {
		t.Fatalf("normalized upstream usage must take precedence: %+v", usage)
	}
}
