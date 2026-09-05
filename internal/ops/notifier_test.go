package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type brokenWebhookBody struct{}

func (brokenWebhookBody) Read(p []byte) (int, error) {
	return copy(p, `{"errcode":`), io.ErrUnexpectedEOF
}
func (brokenWebhookBody) Close() error { return nil }

type webhookTransport func(*http.Request) (*http.Response, error)

func (f webhookTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebhookRejectsUnreadableBody(t *testing.T) {
	for _, body := range []io.ReadCloser{brokenWebhookBody{}, io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1)))} {
		client := &http.Client{Transport: webhookTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
		})}
		notifier := NewWebhookNotifier(WebhookConfig{URL: "https://example.com"}, client)
		if err := notifier.Notify(context.Background(), Event{}); err == nil {
			t.Fatal("unreadable response marked successful")
		}
	}
}

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
		{"dingtalk", `{"markdown":{"text":"【D-API】通知\n\n**事件：**上游健康状态变更\n\n**级别：**严重\n\n**上游：**primary\n\n**状态：**异常\n\n**之前：**正常\n\n**详情：**上游状态已从正常变更为异常","title":"D-API 通知"},"msgtype":"markdown"}`},
		{"feishu", `{"content":{"text":"【D-API】通知\n事件：上游健康状态变更\n级别：严重\n上游：primary\n状态：异常\n之前：正常\n详情：上游状态已从正常变更为异常"},"msg_type":"text"}`},
		{"wecom", `{"msgtype":"text","text":{"content":"【D-API】通知\n事件：上游健康状态变更\n级别：严重\n上游：primary\n状态：异常\n之前：正常\n详情：上游状态已从正常变更为异常"}}`},
		{"slack", `{"text":"【D-API】通知\n事件：上游健康状态变更\n级别：严重\n上游：primary\n状态：异常\n之前：正常\n详情：上游状态已从正常变更为异常"}`},
		{"discord", `{"content":"【D-API】通知\n事件：上游健康状态变更\n级别：严重\n上游：primary\n状态：异常\n之前：正常\n详情：上游状态已从正常变更为异常"}`},
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

func TestGenericWebhookPayloadUsesUTC8(t *testing.T) {
	body, err := webhookPayload("generic", "", Event{
		Type: "notification_test",
		At:   time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload Event
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.At.Format("2006-01-02 15:04:05 -0700"); got != "2026-08-29 20:00:00 +0800" {
		t.Fatalf("generic webhook time = %q", got)
	}
}

func TestWebhookEventTextUsesRecoveryLabel(t *testing.T) {
	text := webhookEventText(Event{
		Type: "low_balance", State: "resolved", Previous: "firing",
		UpstreamName: "primary", Message: "当前余额 10.00 USD，已高于阈值 5.00",
	})
	if !strings.Contains(text, "事件：上游余额恢复") || strings.Contains(text, "事件：上游余额不足") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "详情：当前余额 10.00 USD，已高于阈值 5.00") {
		t.Fatalf("recovery detail missing: %q", text)
	}
	if got := webhookStateLabel("resumed"); got != "已恢复路由" {
		t.Fatalf("resumed label = %q", got)
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
