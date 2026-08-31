package app

import (
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
)

func TestAggregateNotificationEvents(t *testing.T) {
	first := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	event := aggregateNotificationEvents([]ops.Event{
		{Type: "latency", State: "firing", Severity: "告警", UpstreamName: "primary", Message: "平均延迟 31s", At: first},
		{Type: "latency", State: "firing", Severity: "告警", UpstreamName: "backup", Message: "平均延迟 32s", At: second},
	})
	if event.Count != 2 || event.UpstreamID != 0 || event.UpstreamName != "" || event.At != second {
		t.Fatalf("aggregated event = %#v", event)
	}
	if event.Message != "- primary：平均延迟 31s\n- backup：平均延迟 32s" {
		t.Fatalf("aggregated message = %q", event.Message)
	}
}

func TestNotificationRetryDelay(t *testing.T) {
	if got := notificationRetryDelay(1); got != 10*time.Second {
		t.Fatalf("first retry delay = %s", got)
	}
	if got := notificationRetryDelay(2); got != time.Minute {
		t.Fatalf("second retry delay = %s", got)
	}
	if got := notificationRetryDelay(3); got != 5*time.Minute {
		t.Fatalf("later retry delay = %s", got)
	}
}
