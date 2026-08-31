package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

const (
	notificationGroupWait   = 10 * time.Second
	notificationBatchSize   = 50
	notificationMaxAttempts = 5
)

// OutboxNotifier makes notification acceptance durable before returning to the
// alert engine or HTTP handler.
type OutboxNotifier struct{ Store *store.Store }

func NewOutboxNotifier(database *store.Store) OutboxNotifier {
	return OutboxNotifier{Store: database}
}

func (n OutboxNotifier) Notify(ctx context.Context, event ops.Event) error {
	if n.Store == nil {
		return fmt.Errorf("notification outbox is not configured")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	channelIDs, err := n.Store.ListEnabledChannelIDs(ctx)
	if err != nil {
		return fmt.Errorf("list notification channels: %w", err)
	}
	notBefore := time.Now().Add(notificationGroupWait)
	if len(channelIDs) == 0 {
		// Keep the event observable when no channel is configured. It is
		// immediately discarded by the worker, but preserves old semantics.
		return n.Store.EnqueueNotification(ctx, payload, notBefore)
	}
	return n.Store.EnqueueNotificationsForChannels(ctx, channelIDs, payload, notBefore)
}

// RunNotificationWorker drains the durable outbox and groups events that are
// ready at the same time by type, state, severity and channel.
func RunNotificationWorker(ctx context.Context, database *store.Store, delivery ops.Notifier) {
	if database == nil || delivery == nil {
		return
	}
	run := func() {
		jobs, err := database.ClaimNotificationJobs(ctx, notificationBatchSize)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("notification outbox claim failed", "error", err)
			}
			return
		}
		groups := make(map[notificationGroup][]store.NotificationJob)
		order := make([]notificationGroup, 0, len(jobs))
		for _, job := range jobs {
			var event ops.Event
			if err := json.Unmarshal(job.Payload, &event); err != nil {
				slog.Error("notification outbox payload invalid", "job_id", job.ID, "error", err)
				if completeErr := database.CompleteNotification(ctx, job.ID); completeErr != nil {
					slog.Error("notification outbox invalid job cleanup failed", "job_id", job.ID, "error", completeErr)
				}
				continue
			}
			key := notificationGroup{Type: event.Type, State: event.State, Severity: event.Severity, ChannelID: job.ChannelID}
			if _, exists := groups[key]; !exists {
				order = append(order, key)
			}
			groups[key] = append(groups[key], job)
		}
		for _, key := range order {
			jobs := groups[key]
			events := make([]ops.Event, 0, len(jobs))
			for _, job := range jobs {
				var event ops.Event
				if err := json.Unmarshal(job.Payload, &event); err == nil {
					events = append(events, event)
				}
			}
			if len(events) == 0 {
				for _, job := range jobs {
					_ = database.CompleteNotification(ctx, job.ID)
				}
				continue
			}
			event := aggregateNotificationEvents(events)
			var deliveryErr error
			if channelDelivery, ok := delivery.(interface {
				NotifyChannel(context.Context, int64, ops.Event) error
			}); ok && key.ChannelID > 0 {
				deliveryErr = channelDelivery.NotifyChannel(ctx, key.ChannelID, event)
			} else {
				deliveryErr = delivery.Notify(ctx, event)
			}
			if deliveryErr != nil {
				for _, job := range jobs {
					var updateErr error
					if job.Attempts >= notificationMaxAttempts {
						updateErr = database.DeadNotification(ctx, job.ID, deliveryErr.Error())
						if updateErr == nil {
							slog.Error("notification moved to dead-letter", "job_id", job.ID, "channel_id", job.ChannelID, "attempts", job.Attempts, "error", deliveryErr)
						}
					} else {
						updateErr = database.FailNotification(ctx, job.ID, deliveryErr.Error(), time.Now().Add(notificationRetryDelay(job.Attempts)))
					}
					if updateErr != nil {
						slog.Error("notification outbox failure update failed", "job_id", job.ID, "error", updateErr)
					}
				}
				continue
			}
			for _, job := range jobs {
				if err := database.CompleteNotification(ctx, job.ID); err != nil {
					slog.Error("notification outbox completion failed", "job_id", job.ID, "error", err)
				}
			}
		}
	}

	run()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

type notificationGroup struct {
	Type      string
	State     string
	Severity  string
	ChannelID int64
}

func aggregateNotificationEvents(events []ops.Event) ops.Event {
	if len(events) == 0 {
		return ops.Event{}
	}
	if len(events) == 1 {
		return events[0]
	}
	result := events[0]
	result.UpstreamID = 0
	result.UpstreamName = ""
	result.Count = len(events)
	lines := make([]string, 0, len(events))
	for _, event := range events {
		name := event.UpstreamName
		if name == "" {
			name = "系统"
		}
		lines = append(lines, fmt.Sprintf("- %s：%s", name, event.Message))
		if event.At.After(result.At) {
			result.At = event.At
		}
	}
	result.Message = strings.Join(lines, "\n")
	return result
}

func notificationRetryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 10 * time.Second
	case attempts == 2:
		return time.Minute
	default:
		return 5 * time.Minute
	}
}
