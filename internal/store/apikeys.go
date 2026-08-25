package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

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
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO api_keys(name,key_prefix,key_hash,protocols,models)
		VALUES($1,$2,$3,$4,$5) RETURNING id`,
		name, prefix, hash, pq.Array(protocols), pq.Array(models),
	).Scan(&id)
	return id, err
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
		UPDATE api_keys SET last_used_at=now()
		WHERE key_hash=$1 AND enabled=true
		RETURNING id,name,key_prefix,enabled,protocols,models,last_used_at,created_at`,
		auth.HashToken(raw),
	).Scan(&key.ID, &key.Name, &key.Prefix, &key.Enabled, pq.Array(&key.Protocols), pq.Array(&key.Models), &key.LastUsed, &key.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.APIKey{}, ErrNotFound
	}
	return key, err
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
