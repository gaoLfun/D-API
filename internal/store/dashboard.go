package store

import (
	"context"
	"encoding/json"
	"time"
)

type Dashboard struct {
	Upstreams              int          `json:"upstreams_total"`
	Healthy                int          `json:"upstreams_healthy"`
	Degraded               int          `json:"degraded"`
	ActiveKeys             int          `json:"active_keys"`
	Requests24H            int64        `json:"requests_24h"`
	SuccessRate24H         float64      `json:"success_rate"`
	AverageLatency         float64      `json:"average_latency_ms"`
	InputTokens24H         int64        `json:"input_tokens_24h"`
	OutputTokens24H        int64        `json:"output_tokens_24h"`
	CachedInputTokens24H   int64        `json:"cached_input_tokens_24h"`
	CacheWriteTokens24H    int64        `json:"cache_write_tokens_24h"`
	UncachedInputTokens24H int64        `json:"uncached_input_tokens_24h"`
	UsageRequests24H       int64        `json:"usage_requests_24h"`
	CacheHitRequests24H    int64        `json:"cache_hit_requests_24h"`
	CacheHitRate24H        *float64     `json:"cache_hit_rate"`
	RequestHitRate24H      *float64     `json:"request_hit_rate"`
	CostUSD24H             float64      `json:"cost_usd_24h"`
	CostKnownRequests24H   int64        `json:"cost_known_requests_24h"`
	CostCoverage24H        *float64     `json:"cost_coverage"`
	ActiveAlerts           int          `json:"active_alerts"`
	Daily                  []DailyStat  `json:"daily"`
	Hourly                 []HourlyStat `json:"hourly"`
}

type DailyStat struct {
	Day               time.Time `json:"day"`
	Requests          int64     `json:"requests"`
	Successes         int64     `json:"successes"`
	Tokens            int64     `json:"tokens"`
	CachedInputTokens int64     `json:"cached_input_tokens"`
	CacheWriteTokens  int64     `json:"cache_creation_input_tokens"`
	CostUSD           float64   `json:"cost_usd"`
	CostKnownRequests int64     `json:"cost_known_requests"`
}

type HourlyStat struct {
	Hour              time.Time `json:"hour"`
	Requests          int64     `json:"requests"`
	Successes         int64     `json:"successes"`
	Tokens            int64     `json:"tokens"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	CachedInputTokens int64     `json:"cached_input_tokens"`
	CacheWriteTokens  int64     `json:"cache_creation_input_tokens"`
	CostUSD           float64   `json:"cost_usd"`
	CostKnownRequests int64     `json:"cost_known_requests"`
}

type UpstreamUsageToday struct {
	Requests              int64   `json:"requests"`
	Tokens                int64   `json:"tokens"`
	CostUSD               float64 `json:"cost_usd"`
	CostKnownRequests     int64   `json:"cost_known_requests"`
	LifetimeRequests      int64   `json:"lifetime_requests"`
	LifetimeCostUSD       float64 `json:"lifetime_cost_usd"`
	LifetimeKnownRequests int64   `json:"lifetime_cost_known_requests"`
}

func (s *Store) TodayUpstreamUsage(ctx context.Context) (map[int64]UpstreamUsageToday, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH today AS (
			SELECT upstream_id,sum(requests) AS requests,sum(input_tokens+output_tokens) AS tokens,
				sum(cost_usd) AS cost_usd,sum(cost_known_requests) AS cost_known_requests
			FROM daily_usage WHERE day=(now() AT TIME ZONE 'UTC')::date GROUP BY upstream_id
		)
		SELECT u.id,COALESCE(t.requests,0),COALESCE(t.tokens,0),COALESCE(t.cost_usd,0),COALESCE(t.cost_known_requests,0),
			COALESCE(l.requests,0),COALESCE(l.cost_usd,0),COALESCE(l.cost_known_requests,0)
		FROM upstreams u LEFT JOIN today t ON t.upstream_id=u.id
		LEFT JOIN upstream_lifetime_usage l ON l.upstream_id=u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]UpstreamUsageToday)
	for rows.Next() {
		var upstreamID int64
		var usage UpstreamUsageToday
		if err := rows.Scan(&upstreamID, &usage.Requests, &usage.Tokens, &usage.CostUSD, &usage.CostKnownRequests,
			&usage.LifetimeRequests, &usage.LifetimeCostUSD, &usage.LifetimeKnownRequests); err != nil {
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
				COALESCE(sum(output_tokens),0) AS output_tokens,
				COALESCE(sum(cached_input_tokens),0) AS cached_input_tokens,
				COALESCE(sum(cache_creation_input_tokens),0) AS cache_write_tokens,
				COALESCE(sum(uncached_input_tokens),0) AS uncached_input_tokens,
				count(*) FILTER (WHERE input_tokens IS NOT NULL) AS usage_requests,
				count(*) FILTER (WHERE cached_input_tokens > 0) AS cache_hit_requests,
				COALESCE(sum(cost_usd),0) AS cost_usd,
				count(*) FILTER (WHERE cost_usd IS NOT NULL) AS cost_known_requests
			FROM request_logs WHERE created_at >= now()-interval '24 hours'
		), upstream_health AS (
			SELECT `+usageBaseURLSQL("base_url")+` AS base_url_key,
				bool_or(enabled) AS has_enabled,
				bool_and(NOT enabled OR (NOT balance_suspended AND health_status='healthy')) AS all_healthy,
				bool_or(enabled AND (balance_suspended OR health_status='unhealthy')) AS any_unhealthy
			FROM upstreams GROUP BY 1
		)
		SELECT
			(SELECT count(DISTINCT `+usageBaseURLSQL("base_url")+`) FROM upstreams),
			(SELECT count(*) FROM upstream_health WHERE has_enabled AND all_healthy),
			(SELECT count(*) FROM upstream_health WHERE has_enabled AND NOT all_healthy),
			(SELECT count(*) FROM api_keys WHERE enabled),
			m.requests,m.success_rate,m.average_latency,m.input_tokens,m.output_tokens,m.cached_input_tokens,m.cache_write_tokens,m.uncached_input_tokens,m.usage_requests,m.cache_hit_requests,m.cost_usd,m.cost_known_requests,
			(SELECT count(*) FROM alert_states WHERE active) +
			(SELECT count(*) FROM upstream_health WHERE has_enabled AND any_unhealthy)
		FROM metrics m
	`).Scan(&result.Upstreams, &result.Healthy, &result.Degraded, &result.ActiveKeys,
		&result.Requests24H, &result.SuccessRate24H, &result.AverageLatency,
		&result.InputTokens24H, &result.OutputTokens24H, &result.CachedInputTokens24H, &result.CacheWriteTokens24H, &result.UncachedInputTokens24H,
		&result.UsageRequests24H, &result.CacheHitRequests24H, &result.CostUSD24H, &result.CostKnownRequests24H, &result.ActiveAlerts)
	if err != nil {
		return Dashboard{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT day,sum(requests),sum(successes),sum(input_tokens+output_tokens),sum(cached_input_tokens),sum(cache_creation_input_tokens),sum(cost_usd),sum(cost_known_requests)
		FROM daily_usage WHERE day >= current_date-6 GROUP BY day ORDER BY day`)
	if err != nil {
		return Dashboard{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var stat DailyStat
		if err := rows.Scan(&stat.Day, &stat.Requests, &stat.Successes, &stat.Tokens, &stat.CachedInputTokens, &stat.CacheWriteTokens, &stat.CostUSD, &stat.CostKnownRequests); err != nil {
			return Dashboard{}, err
		}
		result.Daily = append(result.Daily, stat)
	}
	if err := rows.Err(); err != nil {
		return Dashboard{}, err
	}
	if result.Requests24H > 0 {
		coverage := float64(result.CostKnownRequests24H) / float64(result.Requests24H)
		result.CostCoverage24H = &coverage
	}
	if result.UsageRequests24H > 0 {
		requestRate := float64(result.CacheHitRequests24H) / float64(result.UsageRequests24H)
		result.RequestHitRate24H = &requestRate
		denominator := result.CachedInputTokens24H + result.UncachedInputTokens24H
		if denominator > 0 {
			tokenRate := float64(result.CachedInputTokens24H) / float64(denominator)
			result.CacheHitRate24H = &tokenRate
		}
	}
	hourRows, err := s.db.QueryContext(ctx, `
		SELECT hour,sum(requests),sum(successes),sum(input_tokens+output_tokens),sum(input_tokens),sum(output_tokens),sum(cached_input_tokens),sum(cache_creation_input_tokens),sum(cost_usd),sum(cost_known_requests)
		FROM hourly_usage WHERE hour >= date_trunc('hour',now())-interval '23 hours' GROUP BY hour ORDER BY hour`)
	if err != nil {
		return Dashboard{}, err
	}
	defer hourRows.Close()
	for hourRows.Next() {
		var stat HourlyStat
		if err := hourRows.Scan(&stat.Hour, &stat.Requests, &stat.Successes, &stat.Tokens, &stat.InputTokens, &stat.OutputTokens, &stat.CachedInputTokens, &stat.CacheWriteTokens, &stat.CostUSD, &stat.CostKnownRequests); err != nil {
			return Dashboard{}, err
		}
		result.Hourly = append(result.Hourly, stat)
	}
	if err := hourRows.Err(); err != nil {
		return Dashboard{}, err
	}
	return result, nil
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
