package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type usageAggregate struct {
	requests, successes                  int64
	input, output, cacheRead, cacheWrite int64
	cacheWriteRequests, uncached         int64
	usageRequests, cacheHits             int64
	cost                                 float64
	costKnown                            int64
}

type usageAggregateKey struct {
	period                        time.Time
	apiKeyID, groupID, upstreamID int64
	protocol, model               string
}

type lifetimeAggregate struct {
	requests, costKnown int64
	cost                float64
}

type insertedRequest struct {
	apiKeyID, groupID, upstreamID sql.NullInt64
}

func recordPreparedBatch(ctx context.Context, tx *sql.Tx, prepared []preparedRequest) error {
	inserted, err := insertRequestLogBatch(ctx, tx, prepared)
	if err != nil {
		return err
	}
	daily := make(map[usageAggregateKey]usageAggregate)
	hourly := make(map[usageAggregateKey]usageAggregate)
	lifetime := make(map[int64]lifetimeAggregate)
	for _, item := range prepared {
		entry := item.entry
		references, ok := inserted[entry.RequestID]
		if !ok || !references.apiKeyID.Valid || !references.upstreamID.Valid {
			continue
		}
		groupID := int64(0)
		if references.groupID.Valid {
			groupID = references.groupID.Int64
		}
		value := aggregateRequest(entry)
		createdAt := entry.CreatedAt.UTC()
		dayKey := usageAggregateKey{
			period:   time.Date(createdAt.Year(), createdAt.Month(), createdAt.Day(), 0, 0, 0, 0, time.UTC),
			apiKeyID: references.apiKeyID.Int64, groupID: groupID, upstreamID: references.upstreamID.Int64,
			protocol: entry.Protocol, model: entry.Model,
		}
		hourKey := dayKey
		hourKey.period = createdAt.Truncate(time.Hour)
		daily[dayKey] = addUsageAggregate(daily[dayKey], value)
		hourly[hourKey] = addUsageAggregate(hourly[hourKey], value)
		current := lifetime[references.upstreamID.Int64]
		current.requests++
		current.cost += value.cost
		current.costKnown += value.costKnown
		lifetime[references.upstreamID.Int64] = current
	}
	if err := upsertUsageAggregates(ctx, tx, "daily_usage", "day", daily); err != nil {
		return err
	}
	if err := upsertUsageAggregates(ctx, tx, "hourly_usage", "hour", hourly); err != nil {
		return err
	}
	return upsertLifetimeAggregates(ctx, tx, lifetime)
}

func upsertLifetimeAggregates(ctx context.Context, tx *sql.Tx, values map[int64]lifetimeAggregate) error {
	if len(values) == 0 {
		return nil
	}
	upstreamIDs := make([]int64, 0, len(values))
	for upstreamID := range values {
		upstreamIDs = append(upstreamIDs, upstreamID)
	}
	sort.Slice(upstreamIDs, func(i, j int) bool { return upstreamIDs[i] < upstreamIDs[j] })
	var query strings.Builder
	query.WriteString(`INSERT INTO upstream_lifetime_usage(upstream_id,requests,cost_known_requests,cost_usd,updated_at) VALUES `)
	args := make([]any, 0, len(upstreamIDs)*4)
	for _, upstreamID := range upstreamIDs {
		if len(args) > 0 {
			query.WriteByte(',')
		}
		query.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,now())", len(args)+1, len(args)+2, len(args)+3, len(args)+4))
		value := values[upstreamID]
		args = append(args, upstreamID, value.requests, value.costKnown, value.cost)
	}
	query.WriteString(` ON CONFLICT(upstream_id) DO UPDATE SET
		requests=upstream_lifetime_usage.requests+EXCLUDED.requests,
		cost_known_requests=upstream_lifetime_usage.cost_known_requests+EXCLUDED.cost_known_requests,
		cost_usd=upstream_lifetime_usage.cost_usd+EXCLUDED.cost_usd,
		updated_at=now()`)
	_, err := tx.ExecContext(ctx, query.String(), args...)
	return err
}

func sortedUsageAggregateKeys(values map[usageAggregateKey]usageAggregate) []usageAggregateKey {
	keys := make([]usageAggregateKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]
		if !left.period.Equal(right.period) {
			return left.period.Before(right.period)
		}
		if left.apiKeyID != right.apiKeyID {
			return left.apiKeyID < right.apiKeyID
		}
		if left.groupID != right.groupID {
			return left.groupID < right.groupID
		}
		if left.upstreamID != right.upstreamID {
			return left.upstreamID < right.upstreamID
		}
		if left.protocol != right.protocol {
			return left.protocol < right.protocol
		}
		return left.model < right.model
	})
	return keys
}

func insertRequestLogBatch(ctx context.Context, tx *sql.Tx, prepared []preparedRequest) (map[string]insertedRequest, error) {
	var query strings.Builder
	query.WriteString(`INSERT INTO request_logs(
		request_id,api_key_id,group_id,upstream_id,protocol,model,status_code,duration_ms,ttfb_ms,ttft_ms,
		attempts,input_tokens,output_tokens,cached_input_tokens,cache_creation_input_tokens,uncached_input_tokens,cost_usd,error_code,client_ip,created_at
	) VALUES `)
	args := make([]any, 0, len(prepared)*20)
	for i, item := range prepared {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for column := range 20 {
			if column > 0 {
				query.WriteByte(',')
			}
			placeholder := fmt.Sprintf("$%d", len(args)+column+1)
			switch column {
			case 1:
				query.WriteString("CASE WHEN EXISTS(SELECT 1 FROM api_keys WHERE id=" + placeholder + ") THEN " + placeholder + " ELSE NULL END")
			case 3:
				query.WriteString("CASE WHEN EXISTS(SELECT 1 FROM upstreams WHERE id=" + placeholder + ") THEN " + placeholder + " ELSE NULL END")
			default:
				query.WriteString(placeholder)
			}
		}
		query.WriteByte(')')
		entry := item.entry
		args = append(args,
			entry.RequestID, nullableID(entry.APIKeyID), entry.GroupID, entry.UpstreamID, entry.Protocol, entry.Model,
			entry.StatusCode, entry.DurationMS, entry.TTFBMS, entry.TTFTMS, item.attempts,
			entry.Usage.InputTokens, entry.Usage.OutputTokens, entry.Usage.CachedInputTokens,
			entry.Usage.CacheCreationInputTokens, entry.Usage.UncachedInputTokens,
			entry.CostUSD, entry.ErrorCode, entry.ClientIP, entry.CreatedAt,
		)
	}
	query.WriteString(` ON CONFLICT(request_id) DO NOTHING RETURNING request_id,api_key_id,group_id,upstream_id`)
	rows, err := tx.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inserted := make(map[string]insertedRequest, len(prepared))
	for rows.Next() {
		var requestID string
		var references insertedRequest
		if err := rows.Scan(&requestID, &references.apiKeyID, &references.groupID, &references.upstreamID); err != nil {
			return nil, err
		}
		inserted[requestID] = references
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func aggregateRequest(entry core.RequestLog) usageAggregate {
	value := usageAggregate{requests: 1}
	if entry.StatusCode >= 200 && entry.StatusCode <= 399 && entry.ErrorCode == "" {
		value.successes = 1
	}
	value.input = pointerValue(entry.Usage.InputTokens)
	value.output = pointerValue(entry.Usage.OutputTokens)
	value.cacheRead = pointerValue(entry.Usage.CachedInputTokens)
	value.cacheWrite = pointerValue(entry.Usage.CacheCreationInputTokens)
	value.uncached = pointerValue(entry.Usage.UncachedInputTokens)
	if entry.Usage.CacheCreationInputTokens != nil {
		value.cacheWriteRequests = 1
	}
	if entry.Usage.InputTokens != nil {
		value.usageRequests = 1
	}
	if value.cacheRead > 0 {
		value.cacheHits = 1
	}
	if entry.CostUSD != nil {
		value.cost = math.Round(*entry.CostUSD*1e8) / 1e8
		value.costKnown = 1
	}
	return value
}

func pointerValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func addUsageAggregate(left, right usageAggregate) usageAggregate {
	left.requests += right.requests
	left.successes += right.successes
	left.input += right.input
	left.output += right.output
	left.cacheRead += right.cacheRead
	left.cacheWrite += right.cacheWrite
	left.cacheWriteRequests += right.cacheWriteRequests
	left.uncached += right.uncached
	left.usageRequests += right.usageRequests
	left.cacheHits += right.cacheHits
	left.cost += right.cost
	left.costKnown += right.costKnown
	return left
}

func upsertUsageAggregates(ctx context.Context, tx *sql.Tx, table, periodColumn string, values map[usageAggregateKey]usageAggregate) error {
	if len(values) == 0 {
		return nil
	}
	keys := sortedUsageAggregateKeys(values)
	var query strings.Builder
	query.WriteString(fmt.Sprintf(`
		INSERT INTO %s(
			%s,api_key_id,group_id,upstream_id,protocol,model,requests,successes,input_tokens,output_tokens,cached_input_tokens,
			cache_creation_input_tokens,cache_creation_usage_requests,uncached_input_tokens,usage_requests,cache_hit_requests,cost_usd,cost_known_requests
		) VALUES `, table, periodColumn))
	args := make([]any, 0, len(keys)*18)
	for _, key := range keys {
		if len(args) > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for column := range 18 {
			if column > 0 {
				query.WriteByte(',')
			}
			query.WriteString(fmt.Sprintf("$%d", len(args)+column+1))
		}
		query.WriteByte(')')
		value := values[key]
		args = append(args,
			key.period, key.apiKeyID, key.groupID, key.upstreamID, key.protocol, key.model,
			value.requests, value.successes, value.input, value.output, value.cacheRead,
			value.cacheWrite, value.cacheWriteRequests, value.uncached, value.usageRequests,
			value.cacheHits, value.cost, value.costKnown,
		)
	}
	query.WriteString(fmt.Sprintf(` ON CONFLICT(%s,api_key_id,group_id,upstream_id,protocol,model) DO UPDATE SET
			requests=%s.requests+EXCLUDED.requests,
			successes=%s.successes+EXCLUDED.successes,
			input_tokens=%s.input_tokens+EXCLUDED.input_tokens,
			output_tokens=%s.output_tokens+EXCLUDED.output_tokens,
			cached_input_tokens=%s.cached_input_tokens+EXCLUDED.cached_input_tokens,
			cache_creation_input_tokens=%s.cache_creation_input_tokens+EXCLUDED.cache_creation_input_tokens,
			cache_creation_usage_requests=%s.cache_creation_usage_requests+EXCLUDED.cache_creation_usage_requests,
			uncached_input_tokens=%s.uncached_input_tokens+EXCLUDED.uncached_input_tokens,
			usage_requests=%s.usage_requests+EXCLUDED.usage_requests,
			cache_hit_requests=%s.cache_hit_requests+EXCLUDED.cache_hit_requests,
			cost_usd=%s.cost_usd+EXCLUDED.cost_usd,
			cost_known_requests=%s.cost_known_requests+EXCLUDED.cost_known_requests`, periodColumn,
		table, table, table, table, table, table, table, table, table, table, table, table))
	_, err := tx.ExecContext(ctx, query.String(), args...)
	return err
}
