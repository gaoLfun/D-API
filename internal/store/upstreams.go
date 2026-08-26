package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

type UpstreamRecord struct {
	core.Upstream
	Balance   core.Balance `json:"balance"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

const upstreamColumns = `
	id, name, kind, base_url, api_key_encrypted, access_token_encrypted, user_id_encrypted,
	enabled, priority, protocols, models, models_locked, model_aliases, connect_timeout_ms,
	first_byte_timeout_ms, idle_timeout_ms, failure_threshold, cooldown_seconds,
	health_status, consecutive_failures, circuit_open_until, last_check_at, last_error,
	balance, created_at, updated_at`

func (s *Store) ListUpstreams(ctx context.Context) ([]core.Upstream, error) {
	records, err := s.ListUpstreamRecords(ctx)
	if err != nil {
		return nil, err
	}
	upstreams := make([]core.Upstream, 0, len(records))
	for _, record := range records {
		upstreams = append(upstreams, record.Upstream)
	}
	return upstreams, nil
}

// ListRouteUpstreams applies cheap routing predicates in SQL before decrypting
// credentials. Circuit state is included so the gateway does not pay the
// decryption cost for entries it will immediately reject.
func (s *Store) ListRouteUpstreams(ctx context.Context, groupID int64, protocol, model string) ([]core.Upstream, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.*
		FROM (SELECT `+upstreamColumns+` FROM upstreams
			WHERE enabled
			  AND ($2='' OR $2 = ANY(protocols))
			  AND ($3='' OR (cardinality(models)=0 OR $3 = ANY(models) OR model_aliases ? $3))
			  AND (circuit_open_until IS NULL OR circuit_open_until <= now())) u
		JOIN group_upstreams gu ON gu.upstream_id=u.id JOIN groups g ON g.id=gu.group_id
		WHERE g.id=$1 AND g.enabled
		ORDER BY u.priority,u.id`, groupID, protocol, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]core.Upstream, 0)
	for rows.Next() {
		record, err := s.scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record.Upstream)
	}
	return result, rows.Err()
}

func (s *Store) ListUpstreamRecords(ctx context.Context) ([]UpstreamRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+upstreamColumns+` FROM upstreams ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]UpstreamRecord, 0)
	for rows.Next() {
		record, err := s.scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) Upstream(ctx context.Context, id int64) (UpstreamRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+upstreamColumns+` FROM upstreams WHERE id=$1`, id)
	record, err := s.scanUpstream(row)
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamRecord{}, ErrNotFound
	}
	return record, err
}

func (s *Store) CreateUpstream(ctx context.Context, upstream core.Upstream) (int64, error) {
	apiKey, accessToken, userID, aliases, err := s.encryptUpstream(upstream)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO upstreams(
			name,kind,base_url,api_key_encrypted,access_token_encrypted,user_id_encrypted,
			enabled,priority,protocols,models,models_locked,model_aliases,connect_timeout_ms,
			first_byte_timeout_ms,idle_timeout_ms,failure_threshold,cooldown_seconds
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id`,
		upstream.Name, upstream.Kind, upstream.BaseURL, apiKey, accessToken, userID,
		upstream.Enabled, upstream.Priority, pq.Array(upstream.Protocols), pq.Array(upstream.Models), upstream.ModelsLocked, aliases,
		upstream.ConnectTimeout.Milliseconds(), upstream.FirstByteTimeout.Milliseconds(),
		upstream.IdleTimeout.Milliseconds(), upstream.FailureThreshold, int(upstream.Cooldown.Seconds()),
	).Scan(&id)
	return id, err
}

func (s *Store) UpdateUpstream(ctx context.Context, upstream core.Upstream) error {
	apiKey, accessToken, userID, aliases, err := s.encryptUpstream(upstream)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE upstreams SET
			name=$1,kind=$2,base_url=$3,api_key_encrypted=$4,access_token_encrypted=$5,
			user_id_encrypted=$6,enabled=$7,priority=$8,protocols=$9,models=$10,models_locked=$11,
			model_aliases=$12,connect_timeout_ms=$13,first_byte_timeout_ms=$14,
			idle_timeout_ms=$15,failure_threshold=$16,cooldown_seconds=$17,updated_at=now()
		WHERE id=$18`,
		upstream.Name, upstream.Kind, upstream.BaseURL, apiKey, accessToken, userID,
		upstream.Enabled, upstream.Priority, pq.Array(upstream.Protocols), pq.Array(upstream.Models), upstream.ModelsLocked, aliases,
		upstream.ConnectTimeout.Milliseconds(), upstream.FirstByteTimeout.Milliseconds(),
		upstream.IdleTimeout.Milliseconds(), upstream.FailureThreshold, int(upstream.Cooldown.Seconds()), upstream.ID,
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

func (s *Store) DeleteUpstream(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM upstreams WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE groups g SET enabled=false,updated_at=now() WHERE g.enabled AND NOT EXISTS (SELECT 1 FROM group_upstreams gu WHERE gu.group_id=g.id)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveModels(ctx context.Context, id int64, models []string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE upstreams SET models=$1, models_locked=true, updated_at=now() WHERE id=$2`, pq.Array(models), id)
	return err
}

func (s *Store) SaveDiscoveredModels(ctx context.Context, id int64, models []string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE upstreams SET models=$1, updated_at=now() WHERE id=$2 AND models_locked=false`, pq.Array(models), id)
	return err
}

func (s *Store) SaveBalance(ctx context.Context, id int64, balance core.Balance) error {
	value, err := json.Marshal(balance)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE upstreams SET balance=$1, balance_updated_at=now(), updated_at=now() WHERE id=$2`, value, id)
	return err
}

func (s *Store) SaveHealth(ctx context.Context, id int64, healthy bool, message string, authFailure bool) (string, error) {
	var status string
	if healthy {
		err := s.db.QueryRowContext(ctx, `
			UPDATE upstreams SET health_status='healthy', consecutive_failures=0,
				circuit_open_until=NULL, last_check_at=now(), last_error='', updated_at=now()
			WHERE id=$1 RETURNING health_status`, id).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return status, err
	}
	err := s.db.QueryRowContext(ctx, `
		UPDATE upstreams SET
			health_status=CASE WHEN $3 OR consecutive_failures + 1 >= failure_threshold THEN 'unhealthy' ELSE 'degraded' END,
			consecutive_failures=consecutive_failures + 1,
			circuit_open_until=CASE WHEN $3 OR consecutive_failures + 1 >= failure_threshold
				THEN now() + make_interval(secs => cooldown_seconds) ELSE circuit_open_until END,
			last_check_at=now(), last_error=$2, updated_at=now()
		WHERE id=$1 RETURNING health_status`, id, message, authFailure).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

func (s *Store) encryptUpstream(upstream core.Upstream) ([]byte, []byte, []byte, []byte, error) {
	apiKey, err := s.box.Encrypt(upstream.APIKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	accessToken, err := s.box.Encrypt(upstream.AccessToken)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	userID, err := s.box.Encrypt(upstream.UserID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	aliases, err := json.Marshal(upstream.ModelAliases)
	return apiKey, accessToken, userID, aliases, err
}

type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUpstream(row scanner) (UpstreamRecord, error) {
	var record UpstreamRecord
	var apiKey, accessToken, userID, aliases, balance []byte
	var connectMS, firstByteMS, idleMS, cooldownSeconds int64
	err := row.Scan(
		&record.ID, &record.Name, &record.Kind, &record.BaseURL, &apiKey, &accessToken, &userID,
		&record.Enabled, &record.Priority, pq.Array(&record.Protocols), pq.Array(&record.Models), &record.ModelsLocked, &aliases,
		&connectMS, &firstByteMS, &idleMS, &record.FailureThreshold, &cooldownSeconds,
		&record.HealthStatus, &record.ConsecutiveFailure, &record.CircuitOpenUntil, &record.LastCheckAt,
		&record.LastError, &balance, &record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		return UpstreamRecord{}, err
	}
	record.APIKey, err = s.box.Decrypt(apiKey)
	if err != nil {
		return UpstreamRecord{}, fmt.Errorf("decrypt upstream %d api key: %w", record.ID, err)
	}
	record.AccessToken, err = s.box.Decrypt(accessToken)
	if err != nil {
		return UpstreamRecord{}, fmt.Errorf("decrypt upstream %d access token: %w", record.ID, err)
	}
	record.UserID, err = s.box.Decrypt(userID)
	if err != nil {
		return UpstreamRecord{}, fmt.Errorf("decrypt upstream %d user id: %w", record.ID, err)
	}
	if err := json.Unmarshal(aliases, &record.ModelAliases); err != nil {
		return UpstreamRecord{}, fmt.Errorf("decode upstream aliases: %w", err)
	}
	if err := json.Unmarshal(balance, &record.Balance); err != nil {
		return UpstreamRecord{}, fmt.Errorf("decode upstream balance: %w", err)
	}
	record.ConnectTimeout = time.Duration(connectMS) * time.Millisecond
	record.FirstByteTimeout = time.Duration(firstByteMS) * time.Millisecond
	record.IdleTimeout = time.Duration(idleMS) * time.Millisecond
	record.Cooldown = time.Duration(cooldownSeconds) * time.Second
	return record, nil
}
