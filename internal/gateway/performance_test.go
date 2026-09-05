package gateway

import (
	"context"
	"encoding/json"
	"github.com/gaoLfun/dapi/internal/core"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestBodiesReuseAndNormalize(t *testing.T) {
	body := []byte(`{"model":" m ","messages":[{"content":"keep  spacing"}]}`)
	bodies := requestBodies{body: body, model: " m "}
	original, err := bodies.forModel(" m ")
	if err != nil || &original[0] != &body[0] {
		t.Fatal("unchanged body was not reused")
	}
	for _, model := range []string{"m", "alias", "m"} {
		got, err := bodies.forModel(model)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Model    string
			Messages []struct{ Content string }
		}
		if err := json.Unmarshal(got, &payload); err != nil || payload.Model != model || payload.Messages[0].Content != "keep  spacing" {
			t.Fatalf("body=%s err=%v", got, err)
		}
		again, _ := bodies.forModel(model)
		if &again[0] != &got[0] {
			t.Fatal("rewritten body was not reused")
		}
	}
}

func TestProxyRejectsAmbiguousModelBeforeRouting(t *testing.T) {
	for _, body := range []string{
		`{"model":"restricted","MODEL":"allowed"}`,
		`{"MODEL":"allowed","model":"restricted"}`,
		`{"model":"restricted","model":"allowed"}`,
		`{"model":"restricted","\u006dodel":"allowed"}`,
		`{"Model":"allowed"}`,
		`{"model":"allowed","stream":true,"Stream":false}`,
		`{"model":"allowed"} {"model":"restricted"}`,
	} {
		t.Run(body, func(t *testing.T) {
			repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true, Models: []string{"allowed"}}}
			w := httptest.NewRecorder()
			NewHandler(repo).ServeHTTP(w, requestWithAuth(http.MethodPost, "/v1/responses", body))
			if w.Code != 400 || len(repo.logs) != 1 || repo.logs[0].ErrorCode != "invalid_request" || len(repo.logs[0].Attempts) != 0 {
				t.Fatalf("status=%d logs=%+v", w.Code, repo.logs)
			}
		})
	}
	payload, err := parseRequestPayload([]byte(`{"model":"allowed","messages":[{"model":"nested","content":"ok"}],"stream":true}`))
	if err != nil || payload.Model != "allowed" || !payload.Stream {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
}

func BenchmarkRequestBodyRetries(b *testing.B) {
	body := []byte(`{"model":"m","messages":[{"content":"` + strings.Repeat("x", 1<<20) + `"}]}`)
	for _, mode := range []string{"original", "optimized"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				bodies := requestBodies{body: body, model: "m"}
				for range 3 {
					var err error
					if mode == "original" {
						_, err = replaceModel(body, "m")
					} else {
						_, err = bodies.forModel("m")
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

type chunkReader struct{ io.Reader }

func (r chunkReader) Read(p []byte) (int, error) {
	if len(p) > 128 {
		p = p[:128]
	}
	return r.Reader.Read(p)
}

func BenchmarkReadLargeResponse(b *testing.B) {
	data := strings.Repeat("x", 1<<20)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		body := io.NopCloser(strings.NewReader(data))
		got, _, err := readResponseWithMetrics(context.Background(), body, time.Second, time.Second, time.Now())
		if err != nil || len(got) != len(data) {
			b.Fatal(err)
		}
	}
}

func BenchmarkRelayManyChunks(b *testing.B) {
	data := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", 1000)
	h := NewHandler(&fakeRepository{})
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(chunkReader{strings.NewReader(data)})}
		_, _, _, _, err := h.relayStreamWithMetrics(context.Background(), httptest.NewRecorder(), response, "bench", "upstream", 1, time.Second, time.Second, "chat", time.Now())
		if err != nil {
			b.Fatal(err)
		}
	}
}
