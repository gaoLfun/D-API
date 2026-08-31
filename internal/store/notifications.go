package store

import (
	"context"
	"database/sql"
	"time"
)

type NotificationJob struct {
	ID        int64
	ChannelID int64
	Payload   []byte
	Attempts  int
}

func (s *Store) EnqueueNotification(ctx context.Context, payload []byte, notBefore time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_outbox(payload,next_attempt_at) VALUES($1::jsonb,$2)`, payload, notBefore)
	return err
}

func (s *Store) EnqueueNotificationForChannel(ctx context.Context, channelID int64, payload []byte, notBefore time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_outbox(channel_id,payload,next_attempt_at) VALUES($1,$2::jsonb,$3)`, channelID, payload, notBefore)
	return err
}

func (s *Store) EnqueueNotificationsForChannels(ctx context.Context, channelIDs []int64, payload []byte, notBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, channelID := range channelIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO notification_outbox(channel_id,payload,next_attempt_at) VALUES($1,$2::jsonb,$3)`, channelID, payload, notBefore); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimNotificationJobs leases due jobs for one worker. The lease grows with
// attempts, making slow deliveries less likely to be claimed twice while
// remaining recoverable after a process crash.
func (s *Store) ClaimNotificationJobs(ctx context.Context, limit int) ([]NotificationJob, error) {
	if limit <= 0 {
		limit = 20
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		WITH picked AS (
			SELECT id FROM notification_outbox
			WHERE dead_at IS NULL AND next_attempt_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE notification_outbox o
		SET attempts=o.attempts+1,
			next_attempt_at=now()+make_interval(secs => LEAST(900, GREATEST(60, (o.attempts+1)*60)))
		FROM picked
		WHERE o.id=picked.id
		RETURNING o.id,o.channel_id,o.payload,o.attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]NotificationJob, 0, limit)
	for rows.Next() {
		var job NotificationJob
		var channelID sql.NullInt64
		if err := rows.Scan(&job.ID, &channelID, &job.Payload, &job.Attempts); err != nil {
			return nil, err
		}
		if channelID.Valid {
			job.ChannelID = channelID.Int64
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) CompleteNotification(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification_outbox WHERE id=$1`, id)
	return err
}

func (s *Store) FailNotification(ctx context.Context, id int64, message string, nextAttempt time.Time) error {
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET last_error=$1,next_attempt_at=$2 WHERE id=$3`, message, nextAttempt, id)
	return err
}

func (s *Store) DeadNotification(ctx context.Context, id int64, message string) error {
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET last_error=$1,dead_at=now() WHERE id=$2`, message, id)
	return err
}
