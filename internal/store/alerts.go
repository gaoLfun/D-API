package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

type AlertRule struct {
	ID              int64    `json:"id"`
	Event           string   `json:"event"`
	UpstreamID      *int64   `json:"upstream_id,omitempty"`
	Threshold       *float64 `json:"threshold,omitempty"`
	WindowSeconds   int      `json:"window_seconds"`
	CooldownSeconds int      `json:"cooldown_seconds"`
	Enabled         bool     `json:"enabled"`
}

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,event,upstream_id,threshold,window_seconds,cooldown_seconds,enabled
		FROM alert_rules ORDER BY event,upstream_id NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]AlertRule, 0)
	for rows.Next() {
		var rule AlertRule
		if err := rows.Scan(&rule.ID, &rule.Event, &rule.UpstreamID, &rule.Threshold, &rule.WindowSeconds, &rule.CooldownSeconds, &rule.Enabled); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) UpdateAlertRule(ctx context.Context, rule AlertRule) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE alert_rules SET threshold=$1,window_seconds=$2,cooldown_seconds=$3,enabled=$4 WHERE id=$5`,
		rule.Threshold, rule.WindowSeconds, rule.CooldownSeconds, rule.Enabled, rule.ID,
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

func (s *Store) CreateAlertRule(ctx context.Context, rule AlertRule) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO alert_rules(event,upstream_id,threshold,window_seconds,cooldown_seconds,enabled)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, rule.Event, rule.UpstreamID, rule.Threshold,
		rule.WindowSeconds, rule.CooldownSeconds, rule.Enabled,
	).Scan(&id)
	return id, err
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id=$1 AND upstream_id IS NOT NULL`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

type AlertState struct {
	Active         bool
	Value          float64
	Message        string
	LastObservedAt time.Time
	LastNotifiedAt *time.Time
}

func (s *Store) AlertState(ctx context.Context, ruleID int64, key string) (AlertState, bool, error) {
	var state AlertState
	err := s.db.QueryRowContext(ctx, `
		SELECT active,value,message,last_observed_at,last_notified_at
		FROM alert_states WHERE rule_id=$1 AND observation_key=$2`, ruleID, key,
	).Scan(&state.Active, &state.Value, &state.Message, &state.LastObservedAt, &state.LastNotifiedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AlertState{}, false, nil
		}
		return AlertState{}, false, err
	}
	return state, true, nil
}

func (s *Store) SaveAlertState(ctx context.Context, ruleID int64, key string, state AlertState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_states(rule_id,observation_key,active,value,message,last_observed_at,last_notified_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(rule_id,observation_key) DO UPDATE SET
			active=EXCLUDED.active,value=EXCLUDED.value,message=EXCLUDED.message,
			last_observed_at=EXCLUDED.last_observed_at,last_notified_at=EXCLUDED.last_notified_at`,
		ruleID, key, state.Active, state.Value, state.Message, state.LastObservedAt, state.LastNotifiedAt,
	)
	return err
}

func (s *Store) PruneAlertStates(ctx context.Context, ruleID int64, keys []string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM alert_states WHERE rule_id=$1 AND NOT (observation_key = ANY($2::text[]))`,
		ruleID, pq.Array(keys),
	)
	return err
}
