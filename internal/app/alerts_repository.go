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
			Window: time.Duration(rule.WindowSeconds) * time.Second, Cooldown: time.Duration(rule.CooldownSeconds) * time.Second,
			MaxNotifications: rule.MaxNotifications, Enabled: rule.Enabled,
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
		LastObservedAt: state.LastObservedAt, LastNotifiedAt: state.LastNotifiedAt, NotificationCount: state.NotificationCount,
	}, found, err
}

func (r AlertRepository) SaveState(ctx context.Context, ruleID int64, key string, state alerts.State) error {
	return r.Store.SaveAlertState(ctx, ruleID, key, store.AlertState{
		Active: state.Active, Value: state.Value, Message: state.Message,
		LastObservedAt: state.LastObservedAt, LastNotifiedAt: state.LastNotifiedAt, NotificationCount: state.NotificationCount,
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
			if record.Balance.Status != "ok" || (!record.Balance.Unlimited && record.Balance.Available == nil) {
				observation.Ignore = true
				observation.Message = fmt.Sprintf("上游 %s 当前余额状态：%s", record.Name, record.Balance.Status)
				result = append(result, observation)
				continue
			}
			observation.Active = record.Balance.Status == "ok" && !record.Balance.Unlimited && record.Balance.Available != nil && *record.Balance.Available < threshold
			if observation.Active {
				observation.Message = fmt.Sprintf("上游 %s 当前余额 %.2f %s，低于阈值 %.2f", record.Name, observation.Value, record.Balance.Currency, threshold)
			} else if record.Balance.Status == "ok" && record.Balance.Unlimited {
				observation.Message = fmt.Sprintf("上游 %s 余额已恢复，当前为无限额", record.Name)
				observation.RecoveryMessage = observation.Message
			} else if record.Balance.Status == "ok" && record.Balance.Available != nil {
				if observation.Value > threshold {
					observation.Message = fmt.Sprintf("上游 %s 当前余额 %.2f %s，已高于阈值 %.2f", record.Name, observation.Value, record.Balance.Currency, threshold)
				} else {
					observation.Message = fmt.Sprintf("上游 %s 当前余额 %.2f %s，已达到阈值 %.2f", record.Name, observation.Value, record.Balance.Currency, threshold)
				}
				observation.RecoveryMessage = observation.Message
			} else {
				observation.Message = fmt.Sprintf("上游 %s 当前余额状态：%s", record.Name, record.Balance.Status)
			}
		} else {
			switch record.Balance.Status {
			case "unavailable":
				observation.Active = true
				observation.Value = 1
			case "ok":
				observation.RecoveryMessage = fmt.Sprintf("上游 %s 余额查询已恢复", record.Name)
			default:
				observation.Ignore = true
			}
			observation.Message = fmt.Sprintf("上游 %s 余额查询状态：%s", record.Name, record.Balance.Status)
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
		WITH attempts AS (
			SELECT COALESCE((a->>'upstream_id')::bigint,0) AS upstream_id,
				COALESCE((a->>'status_code')::int,0) AS status_code,
				COALESCE((a->>'duration_ms')::double precision,0) AS duration_ms
			FROM request_logs l
			CROSS JOIN LATERAL jsonb_array_elements(l.attempts) a
			WHERE l.created_at >= now()-make_interval(secs=>$1)
		), metrics AS (
			SELECT upstream_id,COUNT(*) AS attempts,
				100.0*COUNT(*) FILTER (WHERE status_code=0 OR status_code IN (401,403,404,429) OR status_code>=500)
					/NULLIF(COUNT(*),0) AS error_rate,
				AVG(duration_ms) AS latency
			FROM attempts GROUP BY upstream_id
		)
		SELECT u.id,u.name,COALESCE(m.attempts,0),COALESCE(m.error_rate,0),m.latency
		FROM upstreams u LEFT JOIN metrics m ON m.upstream_id=u.id
		WHERE u.enabled AND ($2=0 OR u.id=$2)
			AND ($2<>0 OR NOT EXISTS (SELECT 1 FROM alert_rules ar WHERE ar.event=$3 AND ar.upstream_id=u.id AND ar.enabled))
		ORDER BY u.id`, window, upstreamID, rule.Event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]alerts.Observation, 0)
	for rows.Next() {
		var id, count int64
		var name string
		var errorRate float64
		var latency sql.NullFloat64
		if err := rows.Scan(&id, &name, &count, &errorRate, &latency); err != nil {
			return nil, err
		}
		value := errorRate
		active := count >= 5 && errorRate >= threshold(rule, 20)
		message := fmt.Sprintf("上游 %s 在 %d 秒内错误率为 %.1f%%", name, window, errorRate)
		recoveryMessage := ""
		if rule.Event == alerts.EventLatency {
			if count == 0 || !latency.Valid {
				result = append(result, alerts.Observation{
					Key: "upstream:" + strconv.FormatInt(id, 10), Ignore: true,
					Message:    fmt.Sprintf("上游 %s 最近 %d 秒内没有可用请求样本，暂不判断延迟", name, window),
					UpstreamID: id, UpstreamName: name,
				})
				continue
			}
			value = latency.Float64
			latencyThreshold := threshold(rule, 30000)
			active = latency.Float64 >= latencyThreshold
			latencyText := fmt.Sprintf("%.0fms", latency.Float64)
			if latency.Float64 < 1 {
				latencyText = "<1ms"
			}
			if active {
				message = fmt.Sprintf("上游 %s 最近 %d 秒收到 %d 次请求尝试，平均延迟 %s，达到阈值 %.0fms", name, window, count, latencyText, latencyThreshold)
			} else {
				message = fmt.Sprintf("上游 %s 最近 %d 秒收到 %d 次请求尝试，平均延迟 %s，低于阈值 %.0fms", name, window, count, latencyText, latencyThreshold)
				recoveryMessage = message
			}
		}
		result = append(result, alerts.Observation{
			Key: "upstream:" + strconv.FormatInt(id, 10), Active: active, Value: value,
			Message: message, RecoveryMessage: recoveryMessage, UpstreamID: id, UpstreamName: name,
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
			Value: rate, Message: fmt.Sprintf("客户端密钥 %s 在 %d 秒内错误率为 %.1f%%", name, window, rate),
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
		Message: fmt.Sprintf("管理员登录在 %d 秒内失败 %d 次", window, count),
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
		return []alerts.Observation{{Key: "admin:new_login_ip", Message: "未发现新的管理员登录 IP"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return []alerts.Observation{{
		Key: "admin:new_login_ip", Active: ip != "" && total == 0, Value: float64(total),
		Message: fmt.Sprintf("管理员从新的 IP 登录：%s", ip),
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
