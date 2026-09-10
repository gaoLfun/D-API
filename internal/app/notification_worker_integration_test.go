package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
)

type boundedTestNotifier struct {
	calls           atomic.Int64
	invalidDeadline atomic.Bool
}

func (n *boundedTestNotifier) Notify(ctx context.Context, _ ops.Event) error {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 20*time.Second {
		n.invalidDeadline.Store(true)
	}
	n.calls.Add(1)
	return nil
}

func TestNotificationWorkerDrainsSmallBatchesWithoutPollingDelay(t *testing.T) {
	db := alertTestStore(t)
	if _, err := db.DB().Exec(`INSERT INTO notification_outbox(payload) SELECT jsonb_build_object('type','test-'||i,'message','test') FROM generate_series(1,11) i`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	notifier := &boundedTestNotifier{}
	go func() { defer close(done); RunNotificationWorker(ctx, db, notifier) }()
	defer func() { cancel(); <-done }()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeout.C:
			t.Fatal("worker waited for polling tick with due work remaining")
		case <-ticker.C:
			pending, _, err := db.NotificationCounts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if pending == 0 {
				if notifier.calls.Load() != 11 || notifier.invalidDeadline.Load() {
					t.Fatal("delivery count or deadline incorrect")
				}
				return
			}
		}
	}
}
