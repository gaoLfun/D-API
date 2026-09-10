package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/gaoLfun/dapi/internal/safeerr"
)

type NotificationJob struct {
	LeaseVersion int64
	ID           int64
	ChannelID    int64
	Payload      []byte
	Attempts     int
}

func (s *Store) EnqueueNotification(ctx context.Context, payload []byte, notBefore time.Time) error {
	return s.EnqueueNotificationsForChannels(ctx, nil, payload, notBefore)
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
	// Archive the original event and enqueue deliveries in the same transaction.
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_events(upstream_id,event,state,message,payload)
		SELECT u.id,p->>'type',p->>'state',p->>'message',p
		FROM (SELECT $1::jsonb AS p) e LEFT JOIN upstreams u ON u.id=NULLIF((p->>'upstream_id')::bigint,0)
		WHERE p->>'type' IN ('error_rate','latency','low_balance','balance_unavailable','client_error_rate','login_failure','new_login_ip')`, payload); err != nil {
		return err
	}
	if len(channelIDs) == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_outbox(payload,next_attempt_at) VALUES($1::jsonb,$2)`, payload, notBefore); err != nil {
			return err
		}
	}
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
			ORDER BY next_attempt_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE notification_outbox o
		SET attempts=o.attempts+1,lease_version=o.lease_version+1,
			next_attempt_at=now()+make_interval(secs => LEAST(900, GREATEST(60, (o.attempts+1)*60)))
		FROM picked
		WHERE o.id=picked.id
		RETURNING o.id,o.channel_id,o.payload,o.attempts,o.lease_version`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]NotificationJob, 0, limit)
	for rows.Next() {
		var job NotificationJob
		var channelID sql.NullInt64
		if err := rows.Scan(&job.ID, &channelID, &job.Payload, &job.Attempts, &job.LeaseVersion); err != nil {
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

var ErrNotificationLeaseLost = errors.New("notification lease lost")

func notificationLeaseResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotificationLeaseLost
	}
	return nil
}

// Renew only a still-owned, unexpired lease before starting delivery.
func (s *Store) RenewNotification(ctx context.Context, job NotificationJob) error {
	result, err := s.db.ExecContext(ctx, `UPDATE notification_outbox SET next_attempt_at=now()+interval '60 seconds'
 WHERE id=$1 AND lease_version=$2 AND dead_at IS NULL AND next_attempt_at>now()`, job.ID, job.LeaseVersion)
	return notificationLeaseResult(result, err)
}

func (s *Store) CompleteNotification(ctx context.Context, job NotificationJob) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM notification_outbox WHERE id=$1 AND lease_version=$2 AND dead_at IS NULL AND next_attempt_at>now()`, job.ID, job.LeaseVersion)
	return notificationLeaseResult(result, err)
}

func (s *Store) FailNotification(ctx context.Context, job NotificationJob, message string, nextAttempt time.Time) error {
	message = safeerr.Text(errors.New(message))
	if len(message) > 2000 {
		message = message[:2000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET last_error=$1,next_attempt_at=$2,lease_version=lease_version+1 WHERE id=$3 AND lease_version=$4 AND dead_at IS NULL AND next_attempt_at>now()`, message, nextAttempt, job.ID, job.LeaseVersion)
	return notificationLeaseResult(result, err)
}

func (s *Store) DeadNotification(ctx context.Context, job NotificationJob, message string) error {
	message = safeerr.Text(errors.New(message))
	if len(message) > 2000 {
		message = message[:2000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET last_error=$1,dead_at=now(),lease_version=lease_version+1 WHERE id=$2 AND lease_version=$3 AND dead_at IS NULL AND next_attempt_at>now()`, message, job.ID, job.LeaseVersion)
	return notificationLeaseResult(result, err)
}

type DeadNotification struct {
	ID          int64     `json:"id"`
	ChannelID   int64     `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error"`
	DeadAt      time.Time `json:"dead_at"`
	Retryable   bool      `json:"retryable"`
}

func (s *Store) NotificationCounts(ctx context.Context) (pending, dead int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE dead_at IS NULL),COUNT(*) FILTER(WHERE dead_at IS NOT NULL) FROM notification_outbox`).Scan(&pending, &dead)
	return
}

func (s *Store) ListDeadNotifications(ctx context.Context, offset int) ([]DeadNotification, error) {
	return s.listDeadNotifications(ctx, offset, 50, nil)
}

func (s *Store) listDeadNotifications(ctx context.Context, offset, limit int, cursor *PageCursor) ([]DeadNotification, error) {
	if offset < 0 {
		offset = 0
	}
	if offset > 100000 {
		offset = 100000
	}
	args := []any{offset, limit}
	cursorClause := ""
	if cursor != nil {
		cursorClause = " AND (o.dead_at,o.id) < ($3::timestamptz,$4::bigint) "
		args = append(args, cursor.At, cursor.ID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,COALESCE(o.channel_id,0),COALESCE(c.name,''),o.attempts,o.last_error,o.dead_at,COALESCE(c.enabled,false)
 FROM notification_outbox o LEFT JOIN notification_channels c ON c.id=o.channel_id WHERE o.dead_at IS NOT NULL `+cursorClause+` ORDER BY o.dead_at DESC,o.id DESC LIMIT $2 OFFSET $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DeadNotification, 0)
	for rows.Next() {
		var job DeadNotification
		if err := rows.Scan(&job.ID, &job.ChannelID, &job.ChannelName, &job.Attempts, &job.LastError, &job.DeadAt, &job.Retryable); err != nil {
			return nil, err
		}
		job.LastError = safeerr.Text(errors.New(job.LastError))
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) RetryDeadNotification(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE notification_outbox o SET attempts=0,last_error='',dead_at=NULL,next_attempt_at=now(),lease_version=o.lease_version+1
 FROM notification_channels c WHERE o.id=$1 AND o.dead_at IS NOT NULL AND c.id=o.channel_id AND c.enabled`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
