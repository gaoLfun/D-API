package ops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
