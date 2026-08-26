package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

var ErrAPIKeySecretUnavailable = errors.New("api key secret unavailable")

func (s *Store) ListAPIKeys(ctx context.Context) ([]core.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,name,key_prefix,enabled,protocols,models,last_used_at,created_at
		FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]core.APIKey, 0)
	for rows.Next() {
		var key core.APIKey
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &key.Enabled, pq.Array(&key.Protocols), pq.Array(&key.Models), &key.LastUsed, &key.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) InsertAPIKey(ctx context.Context, name, prefix string, hash []byte, protocols, models []string) (int64, error) {
	return s.insertAPIKey(ctx, name, prefix, hash, nil, protocols, models)
}

func (s *Store) InsertAPIKeyWithSecret(ctx context.Context, name, prefix string, hash []byte, secret string, protocols, models []string) (int64, error) {
	if s.box == nil {
		return 0, errors.New("secret encryption unavailable")
	}
	encrypted, err := s.box.Encrypt(secret)
	if err != nil {
		return 0, fmt.Errorf("encrypt api key: %w", err)
	}
	return s.insertAPIKey(ctx, name, prefix, hash, encrypted, protocols, models)
}

func (s *Store) insertAPIKey(ctx context.Context, name, prefix string, hash, encrypted []byte, protocols, models []string) (int64, error) {
	var id int64
	if encrypted == nil {
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO api_keys(name,key_prefix,key_hash,protocols,models)
			VALUES($1,$2,$3,$4,$5) RETURNING id`,
			name, prefix, hash, pq.Array(protocols), pq.Array(models),
		).Scan(&id)
		return id, err
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO api_keys(name,key_prefix,key_hash,key_encrypted,protocols,models)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		name, prefix, hash, encrypted, pq.Array(protocols), pq.Array(models),
	).Scan(&id)
	return id, err
}

func (s *Store) APIKeySecret(ctx context.Context, id int64) (string, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_encrypted FROM api_keys WHERE id=$1`, id).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if len(encrypted) == 0 || s.box == nil {
		return "", ErrAPIKeySecretUnavailable
	}
	secret, err := s.box.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}
	if strings.TrimSpace(secret) == "" {
		return "", ErrAPIKeySecretUnavailable
	}
	return secret, nil
}

func (s *Store) UpdateAPIKey(ctx context.Context, key core.APIKey) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE api_keys SET name=$1,enabled=$2,protocols=$3,models=$4 WHERE id=$5`,
		key.Name, key.Enabled, pq.Array(key.Protocols), pq.Array(key.Models), key.ID,
	)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, raw string) (core.APIKey, error) {
	var key core.APIKey
	err := s.db.QueryRowContext(ctx, `
		SELECT id,name,key_prefix,enabled,protocols,models,last_used_at,created_at
		FROM api_keys WHERE key_hash=$1 AND enabled=true`,
		auth.HashToken(raw),
	).Scan(&key.ID, &key.Name, &key.Prefix, &key.Enabled, pq.Array(&key.Protocols), pq.Array(&key.Models), &key.LastUsed, &key.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.APIKey{}, ErrNotFound
	}
	if err != nil {
		return core.APIKey{}, err
	}
	// Keep the hot authentication path read-only. A minute-level touch is enough
	// for the admin's last-used display and avoids serializing requests on one row.
	if key.LastUsed == nil || time.Since(key.LastUsed.UTC()) >= time.Minute {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE api_keys SET last_used_at=now()
			WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now()-interval '1 minute')`, key.ID)
	}
	return key, nil
}

func (s *Store) AvailableModels(ctx context.Context, key core.APIKey) ([]string, error) {
	upstreams, err := s.ListUpstreams(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, upstream := range upstreams {
		if !upstream.Enabled || (upstream.CircuitOpenUntil != nil && upstream.CircuitOpenUntil.After(time.Now())) {
			continue
		}
		for _, model := range upstream.Models {
			clientModel := model
			for alias, mapped := range upstream.ModelAliases {
				if mapped == model {
					clientModel = alias
					break
				}
			}
			if len(key.Models) == 0 || hasString(key.Models, clientModel) {
				seen[clientModel] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}

func (s *Store) MaxAttempts(ctx context.Context) (int, error) {
	var attempts int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT (value #>> '{}')::int FROM settings WHERE key='max_attempts'), 3)`,
	).Scan(&attempts)
	if err != nil {
		return 0, err
	}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 5 {
		attempts = 5
	}
	return attempts, nil
}

func hasString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
