package store

import (
	"context"
	"encoding/json"
	"time"
)

type Dashboard struct {
	Upstreams       int         `json:"upstreams_total"`
	Healthy         int         `json:"upstreams_healthy"`
	Degraded        int         `json:"degraded"`
	ActiveKeys      int         `json:"active_keys"`
	Requests24H     int64       `json:"requests_24h"`
	SuccessRate24H  float64     `json:"success_rate"`
	AverageLatency  float64     `json:"average_latency_ms"`
	InputTokens24H  int64       `json:"input_tokens_24h"`
	OutputTokens24H int64       `json:"output_tokens_24h"`
	ActiveAlerts    int         `json:"active_alerts"`
	Daily           []DailyStat `json:"daily"`
}

type DailyStat struct {
	Day       time.Time `json:"day"`
	Requests  int64     `json:"requests"`
	Successes int64     `json:"successes"`
	Tokens    int64     `json:"tokens"`
}

type UpstreamUsageToday struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

func (s *Store) TodayUpstreamUsage(ctx context.Context) (map[int64]UpstreamUsageToday, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT upstream_id,sum(requests),sum(input_tokens+output_tokens)
		FROM daily_usage WHERE day=(now() AT TIME ZONE 'UTC')::date GROUP BY upstream_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]UpstreamUsageToday)
	for rows.Next() {
		var upstreamID int64
		var usage UpstreamUsageToday
		if err := rows.Scan(&upstreamID, &usage.Requests, &usage.Tokens); err != nil {
			return nil, err
		}
		result[upstreamID] = usage
	}
	return result, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context) (Dashboard, error) {
	var result Dashboard
	err := s.db.QueryRowContext(ctx, `
		WITH metrics AS (
			SELECT count(*) AS requests,
				COALESCE(100.0*count(*) FILTER (WHERE status_code BETWEEN 200 AND 399)/NULLIF(count(*),0),0) AS success_rate,
				COALESCE(avg(duration_ms),0) AS average_latency,
				COALESCE(sum(input_tokens),0) AS input_tokens,
				COALESCE(sum(output_tokens),0) AS output_tokens
			FROM request_logs WHERE created_at >= now()-interval '24 hours'
		)
		SELECT
			(SELECT count(*) FROM upstreams),
			(SELECT count(*) FROM upstreams WHERE enabled AND health_status='healthy'),
			(SELECT count(*) FROM upstreams WHERE enabled AND health_status IN ('degraded','unhealthy')),
			(SELECT count(*) FROM api_keys WHERE enabled),
			m.requests,m.success_rate,m.average_latency,m.input_tokens,m.output_tokens,
			(SELECT count(*) FROM alert_states WHERE active) +
			(SELECT count(*) FROM upstreams WHERE enabled AND health_status='unhealthy')
		FROM metrics m
	`).Scan(&result.Upstreams, &result.Healthy, &result.Degraded, &result.ActiveKeys,
		&result.Requests24H, &result.SuccessRate24H, &result.AverageLatency,
		&result.InputTokens24H, &result.OutputTokens24H, &result.ActiveAlerts)
	if err != nil {
		return Dashboard{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT day,sum(requests),sum(successes),sum(input_tokens+output_tokens)
		FROM daily_usage WHERE day >= current_date-6 GROUP BY day ORDER BY day`)
	if err != nil {
		return Dashboard{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var stat DailyStat
		if err := rows.Scan(&stat.Day, &stat.Requests, &stat.Successes, &stat.Tokens); err != nil {
			return Dashboard{}, err
		}
		result.Daily = append(result.Daily, stat)
	}
	return result, rows.Err()
}

type NotificationChannel struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (s *Store) ListChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,enabled,config_encrypted,created_at,updated_at FROM notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]NotificationChannel, 0)
	for rows.Next() {
		var channel NotificationChannel
		var encrypted []byte
		if err := rows.Scan(&channel.ID, &channel.Name, &channel.Kind, &channel.Enabled, &encrypted, &channel.CreatedAt, &channel.UpdatedAt); err != nil {
			return nil, err
		}
		plain, err := s.box.Decrypt(encrypted)
		if err != nil {
			return nil, err
		}
		channel.Config = json.RawMessage(plain)
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (s *Store) CreateChannel(ctx context.Context, channel NotificationChannel) (int64, error) {
	encrypted, err := s.box.Encrypt(string(channel.Config))
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO notification_channels(name,kind,enabled,config_encrypted)
		VALUES($1,$2,$3,$4) RETURNING id`, channel.Name, channel.Kind, channel.Enabled, encrypted,
	).Scan(&id)
	return id, err
}

func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SaveAlertEvent(ctx context.Context, upstreamID *int64, event, state, message string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_events(upstream_id,event,state,message) VALUES($1,$2,$3,$4)`,
		upstreamID, event, state, message,
	)
	return err
}

func (s *Store) SetMaxAttempts(ctx context.Context, attempts int) error {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 5 {
		attempts = 5
	}
	value, _ := json.Marshal(attempts)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings(key,value) VALUES('max_attempts',$1)
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, value)
	return err
}
