package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
)

// Serialize the durable incident state independently of routing health updates.
func (s *Store) updateHealthNotification(ctx context.Context, id int64, update func(*ops.IncidentState) string, acknowledged string, versions ...int64) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// All notification writers lock the upstream before the incident row.
	// A common lock order avoids upgrade deadlocks between observation and acknowledgement.
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT config_version FROM upstreams WHERE id=$1 FOR UPDATE`, id).Scan(&current); err != nil {
		return "", err
	}
	if expected := expectedConfigVersion(versions); expected > 0 && current != expected {
		return "", ErrUpstreamConfigChanged
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO health_notification_states(upstream_id,state)
		SELECT id,jsonb_build_object(
			'active',health_notified_status='unhealthy',
			'notification_count',CASE WHEN health_notified_status='unhealthy' THEN 1 ELSE 0 END,
			'last_notified_at',CASE WHEN health_notified_status='unhealthy' THEN now() ELSE NULL END)
		FROM upstreams WHERE id=$1
		ON CONFLICT DO NOTHING`, id); err != nil {
		return "", err
	}
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM health_notification_states WHERE upstream_id=$1 FOR UPDATE`, id).Scan(&payload); err != nil {
		return "", err
	}
	var state ops.IncidentState
	if err := json.Unmarshal(payload, &state); err != nil {
		return "", err
	}
	notification := update(&state)
	payload, err = json.Marshal(state)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE health_notification_states SET state=$2::jsonb WHERE upstream_id=$1`, id, payload); err != nil {
		return "", err
	}
	if acknowledged != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE upstreams SET health_notified_status=$2 WHERE id=$1`, id, acknowledged); err != nil {
			return "", err
		}
	}
	return notification, tx.Commit()
}

func (s *Store) ObserveHealthNotification(ctx context.Context, id int64, status string, interval time.Duration, versions ...int64) (string, error) {
	return s.updateHealthNotification(ctx, id, func(state *ops.IncidentState) string {
		now := time.Now()
		policy := ops.IncidentPolicy{
			FiringConfirmations: 1, RecoveryConfirmations: 1,
			Cooldown: 30 * time.Minute, StableFor: 30 * time.Minute,
			MaxNotifications: 3, MaxGap: 2 * interval,
		}
		// Routing already confirms health recovery with scheduled probes.
		event := state.Observe(now, status == "unhealthy", status != "healthy" && status != "unhealthy", false, policy)
		if event == "" {
			return ""
		}
		// Lease an enqueue attempt; a crash or enqueue failure can retry later.
		state.EnqueueFailed(now, time.Minute)
		if event == "firing" {
			return "unhealthy"
		}
		return "healthy"
	}, "", versions...)
}

func (s *Store) AcceptHealthNotification(ctx context.Context, id int64, status string) error {
	if status != "healthy" && status != "unhealthy" {
		return errors.New("invalid health notification status")
	}
	_, err := s.updateHealthNotification(ctx, id, func(state *ops.IncidentState) string {
		event := "resolved"
		if status == "unhealthy" {
			event = "firing"
		}
		state.Accepted(time.Now(), event)
		return ""
	}, status)
	return err
}
