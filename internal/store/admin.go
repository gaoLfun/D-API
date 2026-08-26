package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type Admin struct {
	ID              int64
	Username        string
	PasswordHash    []byte
	PasswordChanged time.Time
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM admins`).Scan(&count)
	return count, err
}

func (s *Store) CreateAdmin(ctx context.Context, username string, passwordHash []byte) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO admins(username, password_hash) VALUES($1, $2) RETURNING id`,
		username, passwordHash,
	).Scan(&id)
	return id, err
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	var admin Admin
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, password_changed_at FROM admins WHERE username=$1`, username,
	).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordChanged)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, ErrNotFound
	}
	return admin, err
}

func (s *Store) CreateSession(ctx context.Context, hash []byte, adminID int64, ip, userAgent string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token_hash, admin_id, ip, user_agent, expires_at) VALUES($1,$2,$3,$4,$5)`,
		hash, adminID, ip, userAgent, expiresAt,
	)
	return err
}

func (s *Store) AdminBySession(ctx context.Context, hash []byte) (Admin, error) {
	var admin Admin
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.username, a.password_hash, a.password_changed_at
		FROM sessions s JOIN admins a ON a.id=s.admin_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, hash,
	).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordChanged)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, ErrNotFound
	}
	return admin, err
}

func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	const batchSize = 1000
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM sessions
			WHERE ctid IN (
				SELECT ctid FROM sessions WHERE expires_at <= now()
				ORDER BY expires_at LIMIT $1
			)`, batchSize)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < batchSize {
			return err
		}
	}
}

func (s *Store) UpdateAdminPassword(ctx context.Context, adminID int64, passwordHash []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE admins SET password_hash=$1, password_changed_at=now() WHERE id=$2`, passwordHash, adminID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id=$1`, adminID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HasSuccessfulLoginFromIP(ctx context.Context, adminID int64, ip string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM audit_logs WHERE admin_id=$1 AND action='admin.login' AND ip=$2)`,
		adminID, ip,
	).Scan(&exists)
	return exists, err
}

func (s *Store) WriteAudit(ctx context.Context, adminID *int64, action, targetType, targetID string, detail []byte, ip string) error {
	if len(detail) == 0 {
		detail = []byte(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_logs(admin_id, action, target_type, target_id, detail, ip)
		VALUES($1,$2,$3,$4,$5,$6)`, adminID, action, targetType, targetID, detail, ip,
	)
	return err
}
