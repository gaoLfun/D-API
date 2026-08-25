package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type LogFilter struct {
	Limit      int
	Offset     int
	StatusMin  int
	StatusMax  int
	UpstreamID int64
}

type RequestLogView struct {
	core.RequestLog
	APIKeyName   string `json:"api_key_name,omitempty"`
	UpstreamName string `json:"upstream_name,omitempty"`
}

func (s *Store) RecordRequest(ctx context.Context, entry core.RequestLog) error {
	attempts, err := json.Marshal(entry.Attempts)
	if err != nil {
		return err
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO request_logs(
			request_id,api_key_id,upstream_id,protocol,model,status_code,duration_ms,
			attempts,input_tokens,output_tokens,cached_input_tokens,error_code,client_ip,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		entry.RequestID, nullableID(entry.APIKeyID), entry.UpstreamID, entry.Protocol, entry.Model,
		entry.StatusCode, entry.DurationMS, attempts, entry.Usage.InputTokens, entry.Usage.OutputTokens,
		entry.Usage.CachedInputTokens, entry.ErrorCode, entry.ClientIP, entry.CreatedAt,
	)
	if err != nil {
		return err
	}
	if entry.APIKeyID > 0 && entry.UpstreamID != nil {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO daily_usage(
				day,api_key_id,upstream_id,protocol,model,requests,successes,input_tokens,output_tokens,cached_input_tokens
			) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9)
			ON CONFLICT(day,api_key_id,upstream_id,protocol,model) DO UPDATE SET
				requests=daily_usage.requests+1,
				successes=daily_usage.successes+EXCLUDED.successes,
				input_tokens=daily_usage.input_tokens+EXCLUDED.input_tokens,
				output_tokens=daily_usage.output_tokens+EXCLUDED.output_tokens,
				cached_input_tokens=daily_usage.cached_input_tokens+EXCLUDED.cached_input_tokens`,
			entry.CreatedAt.UTC().Format("2006-01-02"), entry.APIKeyID, *entry.UpstreamID, entry.Protocol, entry.Model,
			boolInt(entry.StatusCode >= 200 && entry.StatusCode < 400), value(entry.Usage.InputTokens),
			value(entry.Usage.OutputTokens), value(entry.Usage.CachedInputTokens),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListRequestLogs(ctx context.Context, filter LogFilter) ([]RequestLogView, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.request_id,COALESCE(l.api_key_id,0),l.upstream_id,l.protocol,l.model,l.status_code,l.duration_ms,
			l.attempts,l.input_tokens,l.output_tokens,l.cached_input_tokens,l.error_code,l.client_ip,l.created_at,
			COALESCE(k.name,''),COALESCE(u.name,'')
		FROM request_logs l
		LEFT JOIN api_keys k ON k.id=l.api_key_id LEFT JOIN upstreams u ON u.id=l.upstream_id
		WHERE ($1=0 OR l.status_code >= $1) AND ($2=0 OR l.status_code <= $2) AND ($3=0 OR l.upstream_id=$3)
		ORDER BY l.created_at DESC LIMIT $4 OFFSET $5`,
		filter.StatusMin, filter.StatusMax, filter.UpstreamID, filter.Limit, filter.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logs := make([]RequestLogView, 0)
	for rows.Next() {
		var entry RequestLogView
		var attempts []byte
		if err := rows.Scan(
			&entry.RequestID, &entry.APIKeyID, &entry.UpstreamID, &entry.Protocol, &entry.Model,
			&entry.StatusCode, &entry.DurationMS, &attempts, &entry.Usage.InputTokens,
			&entry.Usage.OutputTokens, &entry.Usage.CachedInputTokens, &entry.ErrorCode,
			&entry.ClientIP, &entry.CreatedAt, &entry.APIKeyName, &entry.UpstreamName,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(attempts, &entry.Attempts); err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

type UsageRow struct {
	Day               time.Time `json:"day"`
	APIKeyID          int64     `json:"api_key_id"`
	APIKeyName        string    `json:"api_key_name"`
	UpstreamID        int64     `json:"upstream_id"`
	UpstreamName      string    `json:"upstream_name"`
	Protocol          string    `json:"protocol"`
	Model             string    `json:"model"`
	Requests          int64     `json:"requests"`
	Successes         int64     `json:"successes"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	CachedInputTokens int64     `json:"cached_input_tokens"`
}

func (s *Store) Usage(ctx context.Context, days int) ([]UsageRow, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.day,0,'',0,'','','',sum(d.requests),sum(d.successes),
			sum(d.input_tokens),sum(d.output_tokens),sum(d.cached_input_tokens)
		FROM daily_usage d WHERE d.day >= current_date - $1::int
		GROUP BY d.day ORDER BY d.day`, days-1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := make([]UsageRow, 0)
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(&row.Day, &row.APIKeyID, &row.APIKeyName, &row.UpstreamID, &row.UpstreamName,
			&row.Protocol, &row.Model, &row.Requests, &row.Successes, &row.InputTokens,
			&row.OutputTokens, &row.CachedInputTokens); err != nil {
			return nil, err
		}
		usage = append(usage, row)
	}
	return usage, rows.Err()
}

func (s *Store) CleanupLogs(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE created_at < $1`, before)
	return err
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
