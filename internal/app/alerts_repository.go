package app

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/gaoLfun/dapi/internal/alerts"
	"github.com/gaoLfun/dapi/internal/store"
)

type AlertRepository struct{ Store *store.Store }

func (r AlertRepository) ListRules(ctx context.Context) ([]alerts.Rule, error) {
	stored, err := r.Store.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	rules := make([]alerts.Rule, 0, len(stored))
	for _, rule := range stored {
		rules = append(rules, alerts.Rule{
			ID: rule.ID, Event: rule.Event, UpstreamID: rule.UpstreamID, Threshold: rule.Threshold,
			Window:   time.Duration(rule.WindowSeconds) * time.Second,
			Cooldown: time.Duration(rule.CooldownSeconds) * time.Second, Enabled: rule.Enabled,
		})
	}
	return rules, nil
}

func (r AlertRepository) Observe(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	switch rule.Event {
	case alerts.EventLowBalance, alerts.EventBalanceUnavailable:
		return r.observeBalance(ctx, rule)
	case alerts.EventErrorRate, alerts.EventLatency:
		return r.observeUpstreamMetrics(ctx, rule)
	case alerts.EventClientErrorRate:
		return r.observeClientErrors(ctx, rule)
	case alerts.EventLoginFailure:
		return r.observeLoginFailures(ctx, rule)
	case alerts.EventNewLoginIP:
		return r.observeNewLoginIP(ctx, rule)
	default:
		return nil, fmt.Errorf("unsupported alert event %q", rule.Event)
	}
}

func (r AlertRepository) LoadState(ctx context.Context, ruleID int64, key string) (alerts.State, bool, error) {
	state, found, err := r.Store.AlertState(ctx, ruleID, key)
	return alerts.State{
		Active: state.Active, Value: state.Value, Message: state.Message,
		LastObservedAt: state.LastObservedAt, LastNotifiedAt: state.LastNotifiedAt,
	}, found, err
}

func (r AlertRepository) SaveState(ctx context.Context, ruleID int64, key string, state alerts.State) error {
	return r.Store.SaveAlertState(ctx, ruleID, key, store.AlertState{
		Active: state.Active, Value: state.Value, Message: state.Message,
		LastObservedAt: state.LastObservedAt, LastNotifiedAt: state.LastNotifiedAt,
	})
}

func (r AlertRepository) PruneStates(ctx context.Context, ruleID int64, keys []string) error {
	return r.Store.PruneAlertStates(ctx, ruleID, keys)
}

func (r AlertRepository) observeBalance(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	records, err := r.Store.ListUpstreamRecords(ctx)
	if err != nil {
		return nil, err
	}
	threshold := threshold(rule, 5)
	overrides, err := r.overriddenUpstreams(ctx, rule)
	if err != nil {
		return nil, err
	}
	result := make([]alerts.Observation, 0, len(records))
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		if rule.UpstreamID != nil && record.ID != *rule.UpstreamID {
			continue
		}
		if rule.UpstreamID == nil && overrides[record.ID] {
			continue
		}
		observation := alerts.Observation{
			Key: "upstream:" + strconv.FormatInt(record.ID, 10), UpstreamID: record.ID,
			UpstreamName: record.Name,
		}
		if rule.Event == alerts.EventLowBalance {
			if record.Balance.Available != nil {
				observation.Value = *record.Balance.Available
			}
			observation.Active = record.Balance.Status == "ok" && !record.Balance.Unlimited && record.Balance.Available != nil && *record.Balance.Available < threshold
			observation.Message = fmt.Sprintf("upstream %s balance %.2f %s (threshold %.2f)", record.Name, observation.Value, record.Balance.Currency, threshold)
		} else {
			observation.Active = record.Balance.Status == "unavailable"
			if observation.Active {
				observation.Value = 1
			}
			observation.Message = fmt.Sprintf("upstream %s balance query status: %s", record.Name, record.Balance.Status)
		}
		result = append(result, observation)
	}
	return result, nil
}

func (r AlertRepository) observeUpstreamMetrics(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	window := windowSeconds(rule)
	upstreamID := int64(0)
	if rule.UpstreamID != nil {
		upstreamID = *rule.UpstreamID
	}
	rows, err := r.Store.DB().QueryContext(ctx, `
		SELECT u.id,u.name,COUNT(a),
			COALESCE(100.0*COUNT(a) FILTER (WHERE COALESCE((a->>'status_code')::int,0)=0 OR
				COALESCE((a->>'status_code')::int,0) IN (401,403,404,429) OR COALESCE((a->>'status_code')::int,0)>=500)
				/NULLIF(COUNT(a),0),0),
			COALESCE(AVG(COALESCE((a->>'duration_ms')::double precision,0)),0)
		FROM upstreams u
		LEFT JOIN request_logs l ON l.created_at >= now()-make_interval(secs=>$1)
		LEFT JOIN LATERAL jsonb_array_elements(l.attempts) a ON COALESCE((a->>'upstream_id')::bigint,0)=u.id
		WHERE u.enabled AND ($2=0 OR u.id=$2)
			AND ($2<>0 OR NOT EXISTS (SELECT 1 FROM alert_rules ar WHERE ar.event=$3 AND ar.upstream_id=u.id AND ar.enabled))
		GROUP BY u.id,u.name ORDER BY u.id`, window, upstreamID, rule.Event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]alerts.Observation, 0)
	for rows.Next() {
		var id, count int64
		var name string
		var errorRate, latency float64
		if err := rows.Scan(&id, &name, &count, &errorRate, &latency); err != nil {
			return nil, err
		}
		value := errorRate
		active := count >= 5 && errorRate >= threshold(rule, 20)
		message := fmt.Sprintf("upstream %s error rate %.1f%% in %ds", name, errorRate, window)
		if rule.Event == alerts.EventLatency {
			value = latency
			active = count > 0 && latency >= threshold(rule, 30000)
			message = fmt.Sprintf("upstream %s average latency %.0fms in %ds", name, latency, window)
		}
		result = append(result, alerts.Observation{
			Key: "upstream:" + strconv.FormatInt(id, 10), Active: active, Value: value,
			Message: message, UpstreamID: id, UpstreamName: name,
		})
	}
	return result, rows.Err()
}

func (r AlertRepository) observeClientErrors(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	window := windowSeconds(rule)
	rows, err := r.Store.DB().QueryContext(ctx, `
		SELECT k.id,k.name,COUNT(l.id),COALESCE(100.0*COUNT(l.id) FILTER (WHERE l.status_code>=400)/NULLIF(COUNT(l.id),0),0)
		FROM api_keys k LEFT JOIN request_logs l ON l.api_key_id=k.id AND l.created_at>=now()-make_interval(secs=>$1)
		WHERE k.enabled GROUP BY k.id,k.name ORDER BY k.id`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]alerts.Observation, 0)
	for rows.Next() {
		var id, count int64
		var name string
		var rate float64
		if err := rows.Scan(&id, &name, &count, &rate); err != nil {
			return nil, err
		}
		result = append(result, alerts.Observation{
			Key: "api_key:" + strconv.FormatInt(id, 10), Active: count >= 10 && rate >= threshold(rule, 50),
			Value: rate, Message: fmt.Sprintf("client key %s error rate %.1f%% in %ds", name, rate, window),
		})
	}
	return result, rows.Err()
}

func (r AlertRepository) observeLoginFailures(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	window := windowSeconds(rule)
	var count int64
	err := r.Store.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action='admin.login_failed' AND created_at>=now()-make_interval(secs=>$1)`, window,
	).Scan(&count)
	if err != nil {
		return nil, err
	}
	return []alerts.Observation{{
		Key: "admin:login_failures", Active: float64(count) >= threshold(rule, 5), Value: float64(count),
		Message: fmt.Sprintf("administrator login failed %d times in %ds", count, window),
	}}, nil
}

func (r AlertRepository) observeNewLoginIP(ctx context.Context, rule alerts.Rule) ([]alerts.Observation, error) {
	window := windowSeconds(rule)
	var ip string
	var total int64
	err := r.Store.DB().QueryRowContext(ctx, `
		WITH latest AS (
			SELECT ip,created_at FROM audit_logs WHERE action='admin.login' ORDER BY created_at DESC LIMIT 1
		)
		SELECT COALESCE(latest.ip,''),COUNT(previous.id)
		FROM latest LEFT JOIN audit_logs previous ON previous.action='admin.login' AND previous.ip=latest.ip
			AND previous.created_at<latest.created_at
		WHERE latest.created_at>=now()-make_interval(secs=>$1)
		GROUP BY latest.ip`, window,
	).Scan(&ip, &total)
	if err == sql.ErrNoRows {
		return []alerts.Observation{{Key: "admin:new_login_ip", Message: "no new administrator login IP"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return []alerts.Observation{{
		Key: "admin:new_login_ip", Active: ip != "" && total == 0, Value: float64(total),
		Message: fmt.Sprintf("administrator logged in from a new IP: %s", ip),
	}}, nil
}

func threshold(rule alerts.Rule, fallback float64) float64 {
	if rule.Threshold != nil {
		return *rule.Threshold
	}
	return fallback
}

func windowSeconds(rule alerts.Rule) int {
	seconds := int(rule.Window.Seconds())
	if seconds <= 0 {
		return 300
	}
	return seconds
}

func (r AlertRepository) overriddenUpstreams(ctx context.Context, rule alerts.Rule) (map[int64]bool, error) {
	result := make(map[int64]bool)
	if rule.UpstreamID != nil {
		return result, nil
	}
	rows, err := r.Store.DB().QueryContext(ctx, `SELECT upstream_id FROM alert_rules WHERE event=$1 AND upstream_id IS NOT NULL AND enabled`, rule.Event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}
