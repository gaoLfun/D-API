package ops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookNotifier(t *testing.T) {
	var received Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Hook-Secret") != "secret" {
			t.Errorf("unexpected request: %s, header %q", r.Method, r.Header.Get("X-Hook-Secret"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode event: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	event := Event{Type: "upstream_health", State: "unhealthy", UpstreamID: 7, UpstreamName: "primary", At: time.Now()}
	notifier := NewWebhookNotifier(WebhookConfig{URL: server.URL, Headers: map[string]string{"X-Hook-Secret": "secret"}}, server.Client())
	if err := notifier.Notify(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if received.UpstreamID != event.UpstreamID || received.State != event.State {
		t.Fatalf("event = %#v", received)
	}
}

func TestWebhookNotifierRejectsApplicationError(t *testing.T) {
	for _, body := range []string{
		`{"errcode":310000,"errmsg":"sign not match"}`,
		`{"code":999,"msg":"rejected"}`,
		`{"success":false}`,
		`{"ok":false}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			notifier := NewWebhookNotifier(WebhookConfig{URL: server.URL}, server.Client())
			if err := notifier.Notify(context.Background(), Event{Type: "test"}); err == nil {
				t.Fatal("application-level rejection should return an error")
			}
		})
	}
}

func TestWebhookPayloadAdapters(t *testing.T) {
	event := Event{Type: "upstream_health", State: "unhealthy", Previous: "healthy", UpstreamName: "primary", Message: "连接失败"}
	tests := []struct {
		provider string
		want     string
	}{
		{"dingtalk", `{"markdown":{"text":"【D-API】通知\n\n**事件：**上游健康状态变更\n\n**上游：**primary\n\n**状态：**异常（unhealthy）\n\n**之前：**正常（healthy）\n\n**详情：**上游状态已从正常（healthy）变更为异常（unhealthy）","title":"D-API 通知"},"msgtype":"markdown"}`},
		{"feishu", `{"content":{"text":"【D-API】通知\n事件：上游健康状态变更\n上游：primary\n状态：异常（unhealthy）\n之前：正常（healthy）\n详情：上游状态已从正常（healthy）变更为异常（unhealthy）"},"msg_type":"text"}`},
		{"wecom", `{"msgtype":"text","text":{"content":"【D-API】通知\n事件：上游健康状态变更\n上游：primary\n状态：异常（unhealthy）\n之前：正常（healthy）\n详情：上游状态已从正常（healthy）变更为异常（unhealthy）"}}`},
		{"slack", `{"text":"【D-API】通知\n事件：上游健康状态变更\n上游：primary\n状态：异常（unhealthy）\n之前：正常（healthy）\n详情：上游状态已从正常（healthy）变更为异常（unhealthy）"}`},
		{"discord", `{"content":"【D-API】通知\n事件：上游健康状态变更\n上游：primary\n状态：异常（unhealthy）\n之前：正常（healthy）\n详情：上游状态已从正常（healthy）变更为异常（unhealthy）"}`},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			body, err := webhookPayload(test.provider, "", event)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != test.want {
				t.Fatalf("payload = %s, want %s", body, test.want)
			}
		})
	}
}

func TestWebhookProviderDetectionUsesHostname(t *testing.T) {
	event := Event{Type: "notification_test"}
	dingtalk, err := webhookPayload("", "https://oapi.dingtalk.com/robot/send?next=dingtalk.com", event)
	if err != nil {
		t.Fatal(err)
	}
	if string(dingtalk) == string(mustJSON(event)) {
		t.Fatal("dingtalk hostname was not detected")
	}
	generic, err := webhookPayload("", "https://example.com/callback?next=dingtalk.com", event)
	if err != nil {
		t.Fatal(err)
	}
	if string(generic) != string(mustJSON(event)) {
		t.Fatalf("query string incorrectly selected provider: %s", generic)
	}
}

func TestWebhookEventTextUsesUTC8(t *testing.T) {
	text := webhookEventText(Event{Type: "notification_test", At: time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)})
	if !strings.Contains(text, "时间：2026-08-29 20:00:00 (UTC+8)") {
		t.Fatalf("text = %q", text)
	}
}

func mustJSON(event Event) []byte {
	body, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return body
}

func TestMultiNotifierAcceptsPartialDelivery(t *testing.T) {
	delivered := 0
	notifier := MultiNotifier{
		NotifierFunc(func(context.Context, Event) error { return errors.New("smtp unavailable") }),
		NotifierFunc(func(context.Context, Event) error { delivered++; return nil }),
	}
	if err := notifier.Notify(context.Background(), Event{Type: "test"}); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d", delivered)
	}
	if err := (MultiNotifier{NotifierFunc(func(context.Context, Event) error {
		return errors.New("unavailable")
	})}).Notify(context.Background(), Event{Type: "test"}); err == nil {
		t.Fatal("all failed notifications should return an error")
	}
}

func TestCooldownNotifierAllowsRecovery(t *testing.T) {
	count := 0
	notifier := NewCooldownNotifier(NotifierFunc(func(context.Context, Event) error {
		count++
		return nil
	}), time.Hour)
	failure := Event{Type: "upstream_health", State: "unhealthy", UpstreamID: 1}
	if err := notifier.Notify(context.Background(), failure); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), failure); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), Event{Type: "upstream_health", State: "healthy", UpstreamID: 1}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("notifications = %d, want 2", count)
	}
}
