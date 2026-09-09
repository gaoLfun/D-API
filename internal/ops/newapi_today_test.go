package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestNewAPIAccountTodayPagesAndUnknowns(t *testing.T) {
	now := time.Date(2026, 9, 9, 1, 30, 0, 0, time.UTC)
	start := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC).Unix()
	for _, mode := range []string{"pages", "zero", "missing-output", "duplicate", "wrong-window", "forbidden", "changed-total", "slash-only"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if q.Get("start_timestamp") != strconv.FormatInt(start, 10) || q.Get("end_timestamp") != strconv.FormatInt(now.Unix(), 10) || q.Get("type") != "2" {
					t.Error("wrong day range")
				}
				if r.Header.Get("Authorization") != "Bearer session" || r.Header.Get("New-Api-User") != "42" {
					t.Error("wrong credentials")
				}
				if r.URL.Path == "/prefix/api/log/self/stat" {
					w.Write([]byte(`{"success":true,"data":{"quota":1000000}}`))
					return
				}
				if mode == "slash-only" && r.URL.Path == "/prefix/api/log/self" {
					http.Redirect(w, r, "/prefix/api/log/self/", http.StatusTemporaryRedirect)
					return
				}
				if mode != "slash-only" && r.URL.Path == "/prefix/api/log/self/" {
					http.Redirect(w, r, "/prefix/api/log/self", http.StatusMovedPermanently)
					return
				}
				if r.URL.Path != "/prefix/api/log/self" && r.URL.Path != "/prefix/api/log/self/" {
					t.Error("query encoded into path")
					http.NotFound(w, r)
					return
				}
				if mode == "forbidden" {
					w.WriteHeader(403)
					return
				}
				total := 2
				items := []map[string]any{}
				if mode == "zero" {
					total = 0
				} else {
					p, _ := strconv.Atoi(q.Get("p"))
					id := p
					if mode == "duplicate" {
						id = 1
					}
					row := map[string]any{"id": id, "type": 2, "created_at": start + 1, "prompt_tokens": 100, "completion_tokens": 20}
					if mode == "missing-output" && p == 2 {
						delete(row, "completion_tokens")
					}
					if mode == "wrong-window" {
						row["created_at"] = start - 1
					}
					if mode == "changed-total" && p == 2 {
						total = 3
					}
					items = append(items, row)
				}
				json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"total": total, "items": items}})
			}))
			defer server.Close()
			got := NewProber(server.Client(), time.Second).newAPIAccountToday(context.Background(), core.Upstream{BaseURL: server.URL + "/prefix/v1"}, "session", map[string]string{"New-Api-User": "42"}, now)
			if got == nil || got.Cost == nil || *got.Cost != 2 || got.Timezone == nil || *got.Timezone != "UTC+08:00" {
				t.Fatal("cost/timezone missing")
			}
			switch mode {
			case "pages", "slash-only":
				wantCalls := 3
				if mode == "slash-only" {
					wantCalls = 5
				}
				if got.Input == nil || *got.Input != 200 || got.Output == nil || *got.Output != 40 || got.Total == nil || *got.Total != 240 || calls != wantCalls {
					t.Fatal("pagination totals incorrect")
				}
			case "zero":
				if got.Requests == nil || *got.Requests != 0 || got.Output == nil || *got.Output != 0 || got.Total == nil || *got.Total != 0 {
					t.Fatal("zero confused with missing")
				}
			case "missing-output":
				if got.Input == nil || *got.Input != 200 || got.Output != nil || got.Total != nil {
					t.Fatal("unknown output fabricated")
				}
			default:
				if got.Input != nil || got.Output != nil || got.Total != nil {
					t.Fatal("incomplete/invalid data reported complete")
				}
			}
		})
	}
}
