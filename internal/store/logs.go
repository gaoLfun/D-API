package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type LogFilter struct {
	Limit      int
	Offset     int
	StatusMin  int
	StatusMax  int
	UpstreamID int64
	GroupID    int64
}

type RequestLogView struct {
	core.RequestLog
	APIKeyName   string `json:"api_key_name,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	UpstreamName string `json:"upstream_name,omitempty"`
}

func (s *Store) RecordRequest(ctx context.Context, entry core.RequestLog) error {
	prepared, err := s.prepareRequest(ctx, entry)
	if err != nil {
		return err
	}
	return recordPreparedRequest(ctx, s.db, prepared)
}

// RecordRequests commits a batch atomically, amortizing transaction commits
// while preserving the same log and aggregate updates as RecordRequest.
func (s *Store) RecordRequests(ctx context.Context, entries []core.RequestLog) error {
	if len(entries) == 0 {
		return nil
	}
	prepared := make([]preparedRequest, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, duplicate := seen[entry.RequestID]; duplicate {
			continue
		}
		seen[entry.RequestID] = struct{}{}
		item, err := s.prepareRequest(ctx, entry)
		if err != nil {
			return err
		}
		prepared = append(prepared, item)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordPreparedBatch(ctx, tx, prepared); err != nil {
		return err
	}
	return tx.Commit()
}

type preparedRequest struct {
	entry    core.RequestLog
	attempts []byte
}

func (s *Store) prepareRequest(ctx context.Context, entry core.RequestLog) (preparedRequest, error) {
	attempts, err := json.Marshal(entry.Attempts)
	if err != nil {
		return preparedRequest{}, err
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if entry.UpstreamID != nil {
		entry.CostUSD, err = s.RequestCost(ctx, *entry.UpstreamID, entry.Model, entry.Usage, entry.CreatedAt)
		if err != nil {
			return preparedRequest{}, err
		}
	}
	return preparedRequest{entry: entry, attempts: attempts}, nil
}

type requestLogExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func recordPreparedRequest(ctx context.Context, executor requestLogExecer, prepared preparedRequest) error {
	entry := prepared.entry
	_, err := executor.ExecContext(ctx, `
		WITH inserted AS (
			INSERT INTO request_logs(
				request_id,api_key_id,group_id,upstream_id,protocol,model,status_code,duration_ms,ttfb_ms,ttft_ms,
				attempts,input_tokens,output_tokens,cached_input_tokens,cache_creation_input_tokens,uncached_input_tokens,cost_usd,error_code,client_ip,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
				RETURNING api_key_id,group_id,upstream_id,protocol,model,status_code,error_code,created_at,
				input_tokens,output_tokens,cached_input_tokens,cache_creation_input_tokens,uncached_input_tokens,cost_usd
		),
		daily AS (
		INSERT INTO daily_usage(
				day,api_key_id,group_id,upstream_id,protocol,model,requests,successes,input_tokens,output_tokens,cached_input_tokens,
				cache_creation_input_tokens,cache_creation_usage_requests,uncached_input_tokens,usage_requests,cache_hit_requests,cost_usd,cost_known_requests
			)
			SELECT (created_at AT TIME ZONE 'UTC')::date,api_key_id,COALESCE(group_id,0),upstream_id,protocol,model,1,
			CASE WHEN status_code BETWEEN 200 AND 399 AND error_code='' THEN 1 ELSE 0 END,
			COALESCE(input_tokens,0),COALESCE(output_tokens,0),COALESCE(cached_input_tokens,0),
				COALESCE(cache_creation_input_tokens,0),CASE WHEN cache_creation_input_tokens IS NULL THEN 0 ELSE 1 END,
				COALESCE(uncached_input_tokens,0),CASE WHEN input_tokens IS NULL THEN 0 ELSE 1 END,
				CASE WHEN cached_input_tokens > 0 THEN 1 ELSE 0 END,COALESCE(cost_usd,0),CASE WHEN cost_usd IS NULL THEN 0 ELSE 1 END
			FROM inserted
			WHERE api_key_id IS NOT NULL AND upstream_id IS NOT NULL
			ON CONFLICT(day,api_key_id,group_id,upstream_id,protocol,model) DO UPDATE SET
			requests=daily_usage.requests+1,
			successes=daily_usage.successes+EXCLUDED.successes,
			input_tokens=daily_usage.input_tokens+EXCLUDED.input_tokens,
			output_tokens=daily_usage.output_tokens+EXCLUDED.output_tokens,
			cached_input_tokens=daily_usage.cached_input_tokens+EXCLUDED.cached_input_tokens,
			cache_creation_input_tokens=daily_usage.cache_creation_input_tokens+EXCLUDED.cache_creation_input_tokens,
			cache_creation_usage_requests=daily_usage.cache_creation_usage_requests+EXCLUDED.cache_creation_usage_requests,
			uncached_input_tokens=daily_usage.uncached_input_tokens+EXCLUDED.uncached_input_tokens,
			usage_requests=daily_usage.usage_requests+EXCLUDED.usage_requests,
			cache_hit_requests=daily_usage.cache_hit_requests+EXCLUDED.cache_hit_requests,
			cost_usd=daily_usage.cost_usd+EXCLUDED.cost_usd,
			cost_known_requests=daily_usage.cost_known_requests+EXCLUDED.cost_known_requests
		RETURNING 1
		), lifetime AS (
			INSERT INTO upstream_lifetime_usage(upstream_id,requests,cost_known_requests,cost_usd,updated_at)
			SELECT upstream_id,1,CASE WHEN cost_usd IS NULL THEN 0 ELSE 1 END,COALESCE(cost_usd,0),now()
			FROM inserted WHERE api_key_id IS NOT NULL AND upstream_id IS NOT NULL
			ON CONFLICT(upstream_id) DO UPDATE SET
			requests=upstream_lifetime_usage.requests+1,
			cost_known_requests=upstream_lifetime_usage.cost_known_requests+EXCLUDED.cost_known_requests,
			cost_usd=upstream_lifetime_usage.cost_usd+EXCLUDED.cost_usd,
			updated_at=now()
			RETURNING 1
		)
		INSERT INTO hourly_usage(
			hour,api_key_id,group_id,upstream_id,protocol,model,requests,successes,input_tokens,output_tokens,cached_input_tokens,
			cache_creation_input_tokens,cache_creation_usage_requests,uncached_input_tokens,usage_requests,cache_hit_requests,cost_usd,cost_known_requests
		)
		SELECT date_trunc('hour',created_at),api_key_id,COALESCE(group_id,0),upstream_id,protocol,model,1,
			CASE WHEN status_code BETWEEN 200 AND 399 AND error_code='' THEN 1 ELSE 0 END,
			COALESCE(input_tokens,0),COALESCE(output_tokens,0),COALESCE(cached_input_tokens,0),
			COALESCE(cache_creation_input_tokens,0),CASE WHEN cache_creation_input_tokens IS NULL THEN 0 ELSE 1 END,
			COALESCE(uncached_input_tokens,0),CASE WHEN input_tokens IS NULL THEN 0 ELSE 1 END,
			CASE WHEN cached_input_tokens > 0 THEN 1 ELSE 0 END,COALESCE(cost_usd,0),CASE WHEN cost_usd IS NULL THEN 0 ELSE 1 END
		FROM inserted
		WHERE api_key_id IS NOT NULL AND upstream_id IS NOT NULL
		ON CONFLICT(hour,api_key_id,group_id,upstream_id,protocol,model) DO UPDATE SET
			requests=hourly_usage.requests+1,
			successes=hourly_usage.successes+EXCLUDED.successes,
			input_tokens=hourly_usage.input_tokens+EXCLUDED.input_tokens,
			output_tokens=hourly_usage.output_tokens+EXCLUDED.output_tokens,
			cached_input_tokens=hourly_usage.cached_input_tokens+EXCLUDED.cached_input_tokens,
			cache_creation_input_tokens=hourly_usage.cache_creation_input_tokens+EXCLUDED.cache_creation_input_tokens,
			cache_creation_usage_requests=hourly_usage.cache_creation_usage_requests+EXCLUDED.cache_creation_usage_requests,
			uncached_input_tokens=hourly_usage.uncached_input_tokens+EXCLUDED.uncached_input_tokens,
			usage_requests=hourly_usage.usage_requests+EXCLUDED.usage_requests,
			cache_hit_requests=hourly_usage.cache_hit_requests+EXCLUDED.cache_hit_requests,
			cost_usd=hourly_usage.cost_usd+EXCLUDED.cost_usd,
			cost_known_requests=hourly_usage.cost_known_requests+EXCLUDED.cost_known_requests`,
		entry.RequestID, nullableID(entry.APIKeyID), entry.GroupID, entry.UpstreamID, entry.Protocol, entry.Model,
		entry.StatusCode, entry.DurationMS, entry.TTFBMS, entry.TTFTMS, prepared.attempts, entry.Usage.InputTokens, entry.Usage.OutputTokens,
		entry.Usage.CachedInputTokens, entry.Usage.CacheCreationInputTokens, entry.Usage.UncachedInputTokens,
		entry.CostUSD, entry.ErrorCode, entry.ClientIP, entry.CreatedAt,
	)
	return err
}

func (s *Store) ListRequestLogs(ctx context.Context, filter LogFilter) ([]RequestLogView, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	if filter.Offset > 100000 {
		filter.Offset = 100000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.request_id,COALESCE(l.api_key_id,0),l.group_id,l.upstream_id,l.protocol,l.model,l.status_code,l.duration_ms,l.ttfb_ms,l.ttft_ms,
			l.attempts,l.input_tokens,l.output_tokens,l.cached_input_tokens,l.cache_creation_input_tokens,l.uncached_input_tokens,l.cost_usd,l.error_code,l.client_ip,l.created_at,
			COALESCE(k.name,''),COALESCE(g.name,''),COALESCE(u.name,'')
		FROM request_logs l
		LEFT JOIN api_keys k ON k.id=l.api_key_id LEFT JOIN groups g ON g.id=l.group_id LEFT JOIN upstreams u ON u.id=l.upstream_id
		WHERE ($1=0 OR l.status_code >= $1) AND ($2=0 OR l.status_code <= $2) AND ($3=0 OR l.upstream_id=$3) AND ($4=0 OR l.group_id=$4)
		ORDER BY l.created_at DESC LIMIT $5 OFFSET $6`,
		filter.StatusMin, filter.StatusMax, filter.UpstreamID, filter.GroupID, filter.Limit, filter.Offset,
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
			&entry.RequestID, &entry.APIKeyID, &entry.GroupID, &entry.UpstreamID, &entry.Protocol, &entry.Model,
			&entry.StatusCode, &entry.DurationMS, &entry.TTFBMS, &entry.TTFTMS, &attempts, &entry.Usage.InputTokens,
			&entry.Usage.OutputTokens, &entry.Usage.CachedInputTokens, &entry.Usage.CacheCreationInputTokens, &entry.Usage.UncachedInputTokens, &entry.CostUSD, &entry.ErrorCode,
			&entry.ClientIP, &entry.CreatedAt, &entry.APIKeyName, &entry.GroupName, &entry.UpstreamName,
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
	Day                      time.Time `json:"day"`
	APIKeyID                 int64     `json:"api_key_id"`
	APIKeyName               string    `json:"api_key_name"`
	GroupID                  int64     `json:"group_id"`
	GroupName                string    `json:"group_name"`
	UpstreamID               int64     `json:"upstream_id"`
	UpstreamName             string    `json:"upstream_name"`
	UpstreamBaseURL          string    `json:"-"`
	Protocol                 string    `json:"protocol"`
	Model                    string    `json:"model"`
	Requests                 int64     `json:"requests"`
	Successes                int64     `json:"successes"`
	InputTokens              int64     `json:"input_tokens"`
	OutputTokens             int64     `json:"output_tokens"`
	CachedInputTokens        int64     `json:"cached_input_tokens"`
	CacheCreationInputTokens *int64    `json:"cache_creation_input_tokens,omitempty"`
	UncachedInputTokens      int64     `json:"uncached_input_tokens"`
	UsageRequests            int64     `json:"usage_requests"`
	CacheHitRequests         int64     `json:"cache_hit_requests"`
	CostUSD                  float64   `json:"cost_usd"`
	CostKnownRequests        int64     `json:"cost_known_requests"`
	CostCoverage             *float64  `json:"cost_coverage,omitempty"`
	CacheHitRate             *float64  `json:"cache_hit_rate,omitempty"`
	RequestHitRate           *float64  `json:"request_hit_rate,omitempty"`
	AverageDurationMS        *float64  `json:"average_duration_ms,omitempty"`
	P95DurationMS            *float64  `json:"p95_duration_ms,omitempty"`
	cacheCreationKnown       int64
}

type usageGroupKey struct{ bucket, value string }

func (s *Store) Usage(ctx context.Context, days int) ([]UsageRow, error) {
	return s.UsageWithFilter(ctx, UsageFilter{Days: days})
}

// UsageFilter controls aggregation for the admin usage report. Dates are inclusive UTC days.
type UsageFilter struct {
	Days        int
	FromDay     *time.Time
	ToDay       *time.Time
	Dimension   string // upstream, api_key, protocol, model, or empty for all
	Granularity string // day, week, month
	TopN        int
	UpstreamID  int64
	APIKeyID    int64
	GroupID     int64
	Protocol    string
	Model       string
}

type UsageTotals struct {
	Requests                 int64    `json:"requests"`
	Successes                int64    `json:"successes"`
	InputTokens              int64    `json:"input_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	CachedInputTokens        int64    `json:"cached_input_tokens"`
	CacheCreationInputTokens *int64   `json:"cache_creation_input_tokens,omitempty"`
	UncachedInputTokens      int64    `json:"uncached_input_tokens"`
	UsageRequests            int64    `json:"usage_requests"`
	CacheHitRequests         int64    `json:"cache_hit_requests"`
	CacheHitRate             *float64 `json:"cache_hit_rate,omitempty"`
	RequestHitRate           *float64 `json:"request_hit_rate,omitempty"`
	CostUSD                  float64  `json:"cost_usd"`
	CostKnownRequests        int64    `json:"cost_known_requests"`
	CostCoverage             *float64 `json:"cost_coverage,omitempty"`
	AverageDurationMS        *float64 `json:"average_duration_ms,omitempty"`
	P95DurationMS            *float64 `json:"p95_duration_ms,omitempty"`
}

func (s *Store) UsageTotals(ctx context.Context, filter UsageFilter) (UsageTotals, error) {
	from, to := usageDateRange(filter)
	var totals UsageTotals
	var cacheWrite, cacheWriteKnown int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(requests),0)::bigint, COALESCE(sum(successes),0)::bigint,
			COALESCE(sum(input_tokens),0)::bigint, COALESCE(sum(output_tokens),0)::bigint,
			COALESCE(sum(cached_input_tokens),0)::bigint,
			COALESCE(sum(cache_creation_input_tokens),0)::bigint,
			COALESCE(sum(cache_creation_usage_requests),0)::bigint,
			COALESCE(sum(uncached_input_tokens),0)::bigint,
			COALESCE(sum(usage_requests),0)::bigint, COALESCE(sum(cache_hit_requests),0)::bigint,
			COALESCE(sum(cost_usd),0)::double precision, COALESCE(sum(cost_known_requests),0)::bigint
		FROM daily_usage
		WHERE day >= $1::date AND day <= $2::date
		  AND ($3=0 OR upstream_id=$3) AND ($4=0 OR api_key_id=$4) AND ($5=0 OR group_id=$5)
		  AND ($6='' OR protocol=$6) AND ($7='' OR model=$7)`,
		from, to, filter.UpstreamID, filter.APIKeyID, filter.GroupID, filter.Protocol, filter.Model).Scan(
		&totals.Requests, &totals.Successes, &totals.InputTokens, &totals.OutputTokens,
		&totals.CachedInputTokens, &cacheWrite, &cacheWriteKnown, &totals.UncachedInputTokens,
		&totals.UsageRequests, &totals.CacheHitRequests, &totals.CostUSD, &totals.CostKnownRequests)
	if err != nil {
		return UsageTotals{}, err
	}
	if cacheWriteKnown > 0 {
		totals.CacheCreationInputTokens = &cacheWrite
	}
	finalizeUsageTotals(&totals)
	if totals.Requests > 0 {
		coverage := float64(totals.CostKnownRequests) / float64(totals.Requests)
		totals.CostCoverage = &coverage
	}
	var avg, p95 *float64
	err = s.db.QueryRowContext(ctx, `
		SELECT avg(duration_ms)::double precision,
			percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)::double precision
		FROM request_logs
		WHERE created_at >= ($1::date AT TIME ZONE 'UTC') AND created_at < (($2::date + interval '1 day') AT TIME ZONE 'UTC')
		  AND api_key_id IS NOT NULL AND upstream_id IS NOT NULL
		  AND ($3=0 OR upstream_id=$3) AND ($4=0 OR api_key_id=$4) AND ($5=0 OR group_id=$5)
		  AND ($6='' OR protocol=$6) AND ($7='' OR model=$7)`,
		from, to, filter.UpstreamID, filter.APIKeyID, filter.GroupID, filter.Protocol, filter.Model).Scan(&avg, &p95)
	if err != nil {
		return UsageTotals{}, err
	}
	totals.AverageDurationMS, totals.P95DurationMS = avg, p95
	return totals, nil
}

func finalizeUsageTotals(totals *UsageTotals) {
	if totals.UsageRequests > 0 {
		denominator := totals.CachedInputTokens + totals.UncachedInputTokens
		if denominator > 0 {
			rate := float64(totals.CachedInputTokens) / float64(denominator)
			totals.CacheHitRate = &rate
		}
		rate := float64(totals.CacheHitRequests) / float64(totals.UsageRequests)
		totals.RequestHitRate = &rate
	}
}

func (s *Store) UsageWithFilter(ctx context.Context, filter UsageFilter) ([]UsageRow, error) {
	from, to := usageDateRange(filter)
	dimension := normalizeDimension(filter.Dimension)
	granularity := normalizeGranularity(filter.Granularity)
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.day,d.api_key_id,COALESCE(k.name,''),d.group_id,COALESCE(g.name,''),d.upstream_id,COALESCE(u.name,''),COALESCE(u.base_url,''),d.protocol,d.model,
			d.requests,d.successes,d.input_tokens,d.output_tokens,d.cached_input_tokens,
			 d.cache_creation_input_tokens,d.cache_creation_usage_requests,d.uncached_input_tokens,d.usage_requests,d.cache_hit_requests
			,d.cost_usd,d.cost_known_requests
		FROM daily_usage d
		LEFT JOIN api_keys k ON k.id=d.api_key_id LEFT JOIN groups g ON g.id=d.group_id LEFT JOIN upstreams u ON u.id=d.upstream_id
		WHERE d.day >= $1::date AND d.day <= $2::date
		  AND ($3=0 OR d.upstream_id=$3) AND ($4=0 OR d.api_key_id=$4) AND ($5=0 OR d.group_id=$5)
		  AND ($6='' OR d.protocol=$6) AND ($7='' OR d.model=$7)
		ORDER BY d.day`, from, to, filter.UpstreamID, filter.APIKeyID, filter.GroupID, filter.Protocol, filter.Model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make(map[usageGroupKey]UsageRow)
	for rows.Next() {
		var day time.Time
		var row UsageRow
		var cacheCreation, cacheCreationKnown int64
		if err := rows.Scan(&day, &row.APIKeyID, &row.APIKeyName, &row.GroupID, &row.GroupName, &row.UpstreamID, &row.UpstreamName, &row.UpstreamBaseURL,
			&row.Protocol, &row.Model, &row.Requests, &row.Successes, &row.InputTokens,
			&row.OutputTokens, &row.CachedInputTokens, &cacheCreation, &cacheCreationKnown, &row.UncachedInputTokens,
			&row.UsageRequests, &row.CacheHitRequests, &row.CostUSD, &row.CostKnownRequests); err != nil {
			return nil, err
		}
		if cacheCreationKnown > 0 {
			row.CacheCreationInputTokens = &cacheCreation
		}
		row.cacheCreationKnown = cacheCreationKnown
		row.Day = bucketDate(day, granularity)
		valueKey, label := dimensionValue(row, dimension)
		if dimension == "upstream" {
			row.APIKeyID, row.APIKeyName, row.Protocol, row.Model = 0, "", "", ""
		} else if dimension == "api_key" {
			row.GroupID, row.GroupName, row.UpstreamID, row.UpstreamName, row.Protocol, row.Model = 0, "", 0, "", "", ""
		} else if dimension == "group" {
			row.APIKeyID, row.APIKeyName, row.UpstreamID, row.UpstreamName, row.Protocol, row.Model = 0, "", 0, "", "", ""
		} else if dimension == "protocol" {
			row.APIKeyID, row.APIKeyName, row.UpstreamID, row.UpstreamName, row.Model = 0, "", 0, "", ""
		} else if dimension == "model" {
			row.APIKeyID, row.APIKeyName, row.UpstreamID, row.UpstreamName, row.Protocol = 0, "", 0, "", ""
		} else {
			valueKey, label = "", ""
			row.APIKeyID, row.APIKeyName, row.UpstreamID, row.UpstreamName, row.Protocol, row.Model = 0, "", 0, "", "", ""
		}
		if dimension == "upstream" && label == "" {
			label = "未知"
		}
		if dimension == "api_key" && label == "" {
			label = "未知"
		}
		if dimension == "group" && label == "" {
			label = "历史未分组"
		}
		if dimension == "upstream" {
			row.UpstreamName = label
		}
		if dimension == "api_key" {
			row.APIKeyName = label
		}
		if dimension == "group" {
			row.GroupName = label
		}
		if dimension == "protocol" {
			row.Protocol = label
		}
		if dimension == "model" {
			row.Model = label
		}
		key := usageGroupKey{row.Day.Format("2006-01-02"), valueKey}
		if current, ok := groups[key]; ok {
			current.Requests += row.Requests
			current.Successes += row.Successes
			current.InputTokens += row.InputTokens
			current.OutputTokens += row.OutputTokens
			current.CachedInputTokens += row.CachedInputTokens
			current.CacheCreationInputTokens = addOptional(current.CacheCreationInputTokens, row.CacheCreationInputTokens)
			current.cacheCreationKnown += row.cacheCreationKnown
			current.UncachedInputTokens += row.UncachedInputTokens
			current.UsageRequests += row.UsageRequests
			current.CacheHitRequests += row.CacheHitRequests
			current.CostUSD += row.CostUSD
			current.CostKnownRequests += row.CostKnownRequests
			groups[key] = current
		} else {
			groups[key] = row
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Duration statistics come from raw logs. They naturally remain unknown when old logs were pruned.
	// ponytail: keep values in memory for exact Top-N/other P95; move percentile aggregation into SQL if traffic grows materially.
	durationStats, err := s.usageDurations(ctx, filter, from, to, dimension, granularity)
	if err != nil {
		return nil, err
	}
	if filter.TopN <= 0 {
		filter.TopN = 5
	}
	if filter.TopN > 100 {
		filter.TopN = 100
	}
	if dimension != "" && filter.TopN > 0 {
		totals := make(map[string]int64)
		for key, row := range groups {
			totals[key.value] += row.Requests
		}
		keys := make([]string, 0, len(totals))
		for key := range totals {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if totals[keys[i]] == totals[keys[j]] {
				return keys[i] < keys[j]
			}
			return totals[keys[i]] > totals[keys[j]]
		})
		keep := make(map[string]bool)
		for i := 0; i < len(keys) && i < filter.TopN; i++ {
			keep[keys[i]] = true
		}
		if len(keys) > len(keep) {
			other := make(map[usageGroupKey]UsageRow)
			for key, row := range groups {
				valueKey := key.value
				if !keep[valueKey] {
					key.value = "__other__"
					row.APIKeyID, row.APIKeyName, row.UpstreamID, row.UpstreamName, row.Protocol, row.Model = 0, "", 0, "", "", ""
					if dimension == "upstream" {
						row.UpstreamName = "其他"
					}
					if dimension == "api_key" {
						row.APIKeyName = "其他"
					}
					if dimension == "group" {
						row.GroupName = "其他"
					}
					if dimension == "protocol" {
						row.Protocol = "其他"
					}
					if dimension == "model" {
						row.Model = "其他"
					}
				}
				if prev, ok := other[key]; ok {
					prev.Requests += row.Requests
					prev.Successes += row.Successes
					prev.InputTokens += row.InputTokens
					prev.OutputTokens += row.OutputTokens
					prev.CachedInputTokens += row.CachedInputTokens
					prev.CacheCreationInputTokens = addOptional(prev.CacheCreationInputTokens, row.CacheCreationInputTokens)
					prev.cacheCreationKnown += row.cacheCreationKnown
					prev.UncachedInputTokens += row.UncachedInputTokens
					prev.UsageRequests += row.UsageRequests
					prev.CacheHitRequests += row.CacheHitRequests
					prev.CostUSD += row.CostUSD
					prev.CostKnownRequests += row.CostKnownRequests
					other[key] = prev
				} else {
					other[key] = row
				}
			}
			groups = other
			otherDurations := make(map[usageGroupKey]durationStat)
			for key, stat := range durationStats {
				if !keep[key.value] {
					key.value = "__other__"
				}
				previous := otherDurations[key]
				previous.sum += stat.sum
				previous.count += stat.count
				// P95 cannot be combined from grouped percentiles; leave the
				// aggregate "other" bucket unknown rather than inventing a value.
				previous.p95 = nil
				otherDurations[key] = previous
			}
			durationStats = otherDurations
		}
	}
	for key, stat := range durationStats {
		if row, ok := groups[key]; ok && stat.count > 0 {
			avg := stat.sum / float64(stat.count)
			row.AverageDurationMS = &avg
			row.P95DurationMS = stat.p95
			groups[key] = row
		}
	}
	usage := make([]UsageRow, 0, len(groups))
	for _, row := range groups {
		finalizeUsageRow(&row)
		usage = append(usage, row)
	}
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].Day.Equal(usage[j].Day) {
			return usageLabel(usage[i], dimension) < usageLabel(usage[j], dimension)
		}
		return usage[i].Day.Before(usage[j].Day)
	})
	return usage, nil
}

type durationStat struct {
	sum   float64
	count int64
	p95   *float64
}

func (s *Store) usageDurations(ctx context.Context, filter UsageFilter, from, to time.Time, dimension, granularity string) (map[usageGroupKey]durationStat, error) {
	result := make(map[usageGroupKey]durationStat)
	bucketExpr := usageBucketSQL(granularity)
	dimensionExpr := usageDimensionSQL(dimension)
	query := fmt.Sprintf(`SELECT %s::date, %s, avg(duration_ms)::double precision,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)::double precision, count(*)
		FROM request_logs
		WHERE created_at >= ($1::date AT TIME ZONE 'UTC')
		  AND created_at < (($2::date + interval '1 day') AT TIME ZONE 'UTC')
		  AND api_key_id IS NOT NULL AND upstream_id IS NOT NULL
		  AND ($3=0 OR upstream_id=$3) AND ($4=0 OR api_key_id=$4) AND ($5=0 OR group_id=$5)
		  AND ($6='' OR protocol=$6) AND ($7='' OR model=$7)
		GROUP BY 1,2`, bucketExpr, dimensionExpr)
	rows, err := s.db.QueryContext(ctx, query, from, to, filter.UpstreamID, filter.APIKeyID, filter.GroupID, filter.Protocol, filter.Model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket time.Time
		var value string
		var average, p95 *float64
		var count int64
		if err := rows.Scan(&bucket, &value, &average, &p95, &count); err != nil {
			return nil, err
		}
		key := usageGroupKey{bucket.UTC().Format("2006-01-02"), value}
		result[key] = durationStat{sum: valueFloat(average) * float64(count), count: count, p95: p95}
	}
	return result, rows.Err()
}

func usageBucketSQL(granularity string) string {
	switch granularity {
	case "week":
		return "date_trunc('week', created_at AT TIME ZONE 'UTC')"
	case "month":
		return "date_trunc('month', created_at AT TIME ZONE 'UTC')"
	default:
		return "date_trunc('day', created_at AT TIME ZONE 'UTC')"
	}
}

func usageDimensionSQL(dimension string) string {
	switch dimension {
	case "upstream":
		return fmt.Sprintf("COALESCE((SELECT %s FROM upstreams u WHERE u.id=request_logs.upstream_id),'')", usageBaseURLSQL("base_url"))
	case "api_key":
		return "COALESCE(api_key_id,0)::text"
	case "group":
		return "COALESCE(group_id,0)::text"
	case "protocol":
		return "protocol"
	case "model":
		return "model"
	default:
		return "''"
	}
}

func usageBaseURLSQL(column string) string {
	return fmt.Sprintf("lower(regexp_replace(regexp_replace(regexp_replace(trim(trailing '/' FROM %s), '[?#].*$', ''), '(?i)^https://([^/:]+):443(/|$)', 'https://\\1\\2'), '(?i)^http://([^/:]+):80(/|$)', 'http://\\1\\2'))", column)
}

func normalizeUsageBaseURL(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.ToLower(strings.TrimRight(raw, "/"))
	}
	if (parsed.Scheme == "https" && parsed.Port() == "443") || (parsed.Scheme == "http" && parsed.Port() == "80") {
		parsed.Host = parsed.Hostname()
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery, parsed.Fragment = "", ""
	return strings.ToLower(strings.TrimRight(parsed.String(), "/"))
}

func valueFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func usageDateRange(filter UsageFilter) (time.Time, time.Time) {
	now := time.Now().UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if filter.ToDay != nil {
		to = filter.ToDay.UTC()
	}
	from := to.AddDate(0, 0, -29)
	if filter.Days > 0 && filter.Days <= 365 {
		from = to.AddDate(0, 0, -(filter.Days - 1))
	}
	if filter.FromDay != nil {
		from = filter.FromDay.UTC()
	}
	// Keep analytical queries bounded even when called outside the HTTP API.
	// The daily rollup is intended for a one-year window; older data remains
	// available through export/archival tooling instead of an unbounded scan.
	if from.After(to) {
		from = to
	}
	if to.Sub(from) > 364*24*time.Hour {
		from = to.AddDate(0, 0, -364)
	}
	return from, to
}

func normalizeDimension(value string) string {
	switch value {
	case "upstream", "api_key", "group", "protocol", "model":
		return value
	default:
		return ""
	}
}
func normalizeGranularity(value string) string {
	switch value {
	case "week", "month":
		return value
	default:
		return "day"
	}
}
func bucketDate(day time.Time, granularity string) time.Time {
	day = time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC)
	switch granularity {
	case "week":
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		return day.AddDate(0, 0, -(wd - 1))
	case "month":
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return day
	}
}
func dimensionValue(row UsageRow, dimension string) (string, string) {
	switch dimension {
	case "upstream":
		key := normalizeUsageBaseURL(row.UpstreamBaseURL)
		return key, key
	case "api_key":
		return fmt.Sprintf("%d", row.APIKeyID), row.APIKeyName
	case "group":
		return fmt.Sprintf("%d", row.GroupID), row.GroupName
	case "protocol":
		return row.Protocol, row.Protocol
	case "model":
		return row.Model, row.Model
	default:
		return "", ""
	}
}
func usageLabel(row UsageRow, dimension string) string {
	_, label := dimensionValue(row, dimension)
	return label
}
func finalizeUsageRow(row *UsageRow) {
	if row.UsageRequests > 0 {
		denominator := row.CachedInputTokens + row.UncachedInputTokens
		if denominator > 0 {
			rate := float64(row.CachedInputTokens) / float64(denominator)
			row.CacheHitRate = &rate
		}
		reqRate := float64(row.CacheHitRequests) / float64(row.UsageRequests)
		row.RequestHitRate = &reqRate
	}
	if row.Requests > 0 {
		coverage := float64(row.CostKnownRequests) / float64(row.Requests)
		row.CostCoverage = &coverage
	}
}

func addOptional(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	var total int64
	if a != nil {
		total += *a
	}
	if b != nil {
		total += *b
	}
	return &total
}

func (s *Store) CleanupLogs(ctx context.Context, before time.Time) error {
	return s.cleanupByID(ctx, "request_logs", before)
}

func (s *Store) CleanupAuditLogs(ctx context.Context, before time.Time) error {
	return s.cleanupByID(ctx, "audit_logs", before)
}

func (s *Store) CleanupAlertEvents(ctx context.Context, before time.Time) error {
	return s.cleanupByID(ctx, "alert_events", before)
}

func (s *Store) CleanupDailyUsage(ctx context.Context, before time.Time) error {
	const batchSize = 1000
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM daily_usage
			WHERE ctid IN (
				SELECT ctid FROM daily_usage
				WHERE day < ($1 AT TIME ZONE 'UTC')::date
				ORDER BY day
				LIMIT $2
			)`, before, batchSize)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < batchSize {
			return err
		}
	}
}

func (s *Store) CleanupHourlyUsage(ctx context.Context, before time.Time) error {
	const batchSize = 1000
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM hourly_usage
			WHERE ctid IN (
				SELECT ctid FROM hourly_usage
				WHERE hour < $1
				ORDER BY hour
				LIMIT $2
			)`, before.UTC(), batchSize)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < batchSize {
			return err
		}
	}
}

func (s *Store) cleanupByID(ctx context.Context, table string, before time.Time) error {
	const batchSize = 1000
	// table is selected only by the fixed methods above; it is never caller input.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM `+table+`
			WHERE id IN (
				SELECT id FROM `+table+`
				WHERE created_at < $1
				ORDER BY created_at,id
				LIMIT $2
			)`, before, batchSize)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < batchSize {
			return err
		}
	}
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
