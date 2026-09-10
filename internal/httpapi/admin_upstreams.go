package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/netguard"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

type upstreamPayload struct {
	ID                      int64             `json:"id"`
	Name                    string            `json:"name"`
	Kind                    string            `json:"kind"`
	BaseURL                 string            `json:"base_url"`
	UserAgent               string            `json:"user_agent"`
	APIKey                  string            `json:"api_key"`
	AccessToken             string            `json:"access_token"`
	UserID                  string            `json:"user_id"`
	ClearBalanceCredentials bool              `json:"clear_balance_credentials"`
	Enabled                 *bool             `json:"enabled"`
	BalanceProtection       *bool             `json:"balance_protection_enabled"`
	Priority                *int              `json:"priority"`
	Protocols               []string          `json:"protocols"`
	Models                  []string          `json:"models"`
	ModelsLocked            *bool             `json:"models_locked"`
	ModelAliases            map[string]string `json:"model_aliases"`
	PricingProfileID        *int64            `json:"pricing_profile_id"`
	ConnectTimeoutMS        int               `json:"connect_timeout_ms"`
	FirstByteTimeoutMS      int               `json:"first_byte_timeout_ms"`
	IdleTimeoutMS           int               `json:"idle_timeout_ms"`
	FailureThreshold        int               `json:"failure_threshold"`
	CooldownSeconds         int               `json:"cooldown_seconds"`
}

type upstreamView struct {
	ID                   int64             `json:"id"`
	Name                 string            `json:"name"`
	Kind                 string            `json:"kind"`
	BaseURL              string            `json:"base_url"`
	UserAgent            string            `json:"user_agent"`
	HasAPIKey            bool              `json:"has_api_key"`
	HasAccessToken       bool              `json:"has_access_token"`
	HasUserID            bool              `json:"has_user_id"`
	Enabled              bool              `json:"enabled"`
	BalanceProtection    bool              `json:"balance_protection_enabled"`
	BalanceSuspended     bool              `json:"balance_suspended"`
	ZeroBalanceChecks    int               `json:"zero_balance_checks"`
	Priority             int               `json:"priority"`
	Protocols            []string          `json:"protocols"`
	Models               []string          `json:"models"`
	ModelsLocked         bool              `json:"models_locked"`
	ModelAliases         map[string]string `json:"model_aliases"`
	PricingProfileID     *int64            `json:"pricing_profile_id,omitempty"`
	ConnectTimeoutMS     int64             `json:"connect_timeout_ms"`
	FirstByteTimeoutMS   int64             `json:"first_byte_timeout_ms"`
	IdleTimeoutMS        int64             `json:"idle_timeout_ms"`
	FailureThreshold     int               `json:"failure_threshold"`
	CooldownSeconds      int64             `json:"cooldown_seconds"`
	HealthStatus         string            `json:"health_status"`
	ConsecutiveFailures  int               `json:"consecutive_failures"`
	ConsecutiveSuccesses int               `json:"consecutive_successes"`
	RecoveryStartedAt    *time.Time        `json:"recovery_started_at,omitempty"`
	CircuitOpenUntil     *time.Time        `json:"circuit_open_until,omitempty"`
	LastCheckAt          *time.Time        `json:"last_check_at,omitempty"`
	LastError            string            `json:"last_error,omitempty"`
	TodayRequests        int64             `json:"today_requests"`
	TodayTokens          int64             `json:"today_tokens"`
	TodayCostUSD         float64           `json:"today_cost_usd"`
	TodayCostCoverage    *float64          `json:"today_cost_coverage"`
	LifetimeRequests     int64             `json:"lifetime_requests"`
	LifetimeCostUSD      float64           `json:"lifetime_cost_usd"`
	LifetimeCostCoverage *float64          `json:"lifetime_cost_coverage"`
	Balance              core.Balance      `json:"balance"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

func (s *Server) listUpstreams(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListUpstreamRecords(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	todayUsage, err := s.store.TodayUpstreamUsage(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]upstreamView, 0, len(records))
	for _, record := range records {
		view := viewUpstream(record)
		view.TodayRequests = todayUsage[record.ID].Requests
		view.TodayTokens = todayUsage[record.ID].Tokens
		view.TodayCostUSD = todayUsage[record.ID].CostUSD
		view.LifetimeRequests = todayUsage[record.ID].LifetimeRequests
		view.LifetimeCostUSD = todayUsage[record.ID].LifetimeCostUSD
		if view.TodayRequests > 0 {
			coverage := float64(todayUsage[record.ID].CostKnownRequests) / float64(view.TodayRequests)
			view.TodayCostCoverage = &coverage
		}
		if view.LifetimeRequests > 0 {
			coverage := float64(todayUsage[record.ID].LifetimeKnownRequests) / float64(view.LifetimeRequests)
			view.LifetimeCostCoverage = &coverage
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createUpstream(w http.ResponseWriter, r *http.Request) {
	var input upstreamPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	upstream, err := input.upstream(0, core.Upstream{})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", err.Error())
		return
	}
	if err := netguard.ValidateURL(upstream.BaseURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "上游地址不允许访问内网或无效地址")
		return
	}
	if upstream.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "API Key 不能为空")
		return
	}
	id, err := s.store.CreateUpstream(r.Context(), upstream)
	if err != nil {
		slog.Error("create upstream failed", "name", upstream.Name, "error", err)
		writeStoreError(w, err)
		return
	}
	s.audit(r, "upstream.create", "upstream", id, map[string]any{"name": upstream.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updateUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.store.Upstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input upstreamPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	upstream, err := input.upstream(id, existing.Upstream)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", err.Error())
		return
	}
	if err := netguard.ValidateURL(upstream.BaseURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "上游地址不允许访问内网或无效地址")
		return
	}
	transition, err := s.store.UpdateUpstream(r.Context(), upstream)
	if err != nil {
		slog.Error("update upstream failed", "upstream_id", id, "name", upstream.Name, "error", err)
		writeStoreError(w, err)
		return
	}
	s.audit(r, "upstream.update", "upstream", id, map[string]any{"name": upstream.Name})
	if transition != core.BalanceUnchanged {
		s.notifyPersistedEvent(ops.BalanceProtectionDisabledEvent(upstream))
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) testUpstream(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "probe_unavailable", "探测服务不可用")
		return
	}
	var input upstreamPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var existing core.Upstream
	if input.ID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID 无效")
		return
	}
	if input.ID > 0 {
		record, err := s.store.Upstream(r.Context(), input.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		existing = record.Upstream
	}
	upstream, err := input.upstream(input.ID, existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", err.Error())
		return
	}
	if err := netguard.ValidateURL(upstream.BaseURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "上游地址不允许访问内网或无效地址")
		return
	}
	if upstream.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "API Key 不能为空")
		return
	}
	health := s.operations.Probe(r.Context(), upstream)
	s.audit(r, "upstream.test", "upstream", input.ID, map[string]any{"name": upstream.Name, "status": health.Status})
	writeJSON(w, http.StatusOK, health)
}

type modelTestPayload struct {
	upstreamPayload
	Model string `json:"model"`
	Audit bool   `json:"audit"`
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "probe_unavailable", "探测服务不可用")
		return
	}
	var input modelTestPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.ID < 0 || input.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "模型或上游 ID 无效")
		return
	}
	var existing core.Upstream
	if input.ID > 0 {
		record, err := s.store.Upstream(r.Context(), input.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		existing = record.Upstream
	}
	upstream, err := input.upstream(input.ID, existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", err.Error())
		return
	}
	if err := netguard.ValidateURL(upstream.BaseURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "上游地址不允许访问内网或无效地址")
		return
	}
	if upstream.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "API Key 不能为空")
		return
	}
	result := s.operations.TestModel(r.Context(), upstream, input.Model)
	if input.Audit {
		s.audit(r, "upstream.model_test", "upstream", input.ID, map[string]any{
			"name": upstream.Name, "model": input.Model, "status": result.Status, "results": modelTestAuditResults(result.Results),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func modelTestAuditResults(results []ops.ModelProbe) []map[string]any {
	sanitized := make([]map[string]any, 0, len(results))
	for _, result := range results {
		sanitized = append(sanitized, map[string]any{
			"protocol": result.Protocol, "status": result.Status,
			"status_code": result.StatusCode, "latency_ms": result.LatencyMS,
		})
	}
	return sanitized
}

type modelTestsAuditPayload struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	ModelsCount      int    `json:"models_count"`
	ProtocolRequests int    `json:"protocol_requests"`
	Available        int    `json:"available"`
	Partial          int    `json:"partial"`
	Unavailable      int    `json:"unavailable"`
	Stopped          bool   `json:"stopped"`
}

func (s *Server) auditModelTests(w http.ResponseWriter, r *http.Request) {
	var input modelTestsAuditPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	completed := input.Available + input.Partial + input.Unavailable
	if input.ID < 0 || len([]rune(input.Name)) > 200 || input.ModelsCount < 0 || input.ProtocolRequests < 0 || input.Available < 0 || input.Partial < 0 || input.Unavailable < 0 || completed > input.ModelsCount {
		writeError(w, http.StatusBadRequest, "invalid_request", "测试汇总无效")
		return
	}
	s.audit(r, "upstream.models_test", "upstream", input.ID, input)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUpstream(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "upstream.delete", "upstream", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "probe_unavailable", "探测服务不可用")
		return
	}
	health, err := s.operations.Check(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "upstream.check", "upstream", id, map[string]any{"status": health.Status})
	writeJSON(w, http.StatusOK, health)
}

func (s *Server) balanceUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "probe_unavailable", "探测服务不可用")
		return
	}
	upstream, balance, transition, err := s.operations.Balance(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if transition != core.BalanceUnchanged {
		s.notifyPersistedEvent(ops.BalanceTransitionEvent(upstream, balance, transition))
	}
	writeJSON(w, http.StatusOK, balance)
}

func (s *Server) modelsUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "probe_unavailable", "探测服务不可用")
		return
	}
	models, err := s.operations.Models(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (p upstreamPayload) upstream(id int64, existing core.Upstream) (core.Upstream, error) {
	name := strings.TrimSpace(p.Name)
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	userAgent := strings.TrimSpace(p.UserAgent)
	if err := validateUpstreamPayload(p, name, base); err != nil {
		return core.Upstream{}, err
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return core.Upstream{}, errors.New("Base URL 必须是有效的 HTTP(S) 地址")
	}
	if name == "" || (p.Kind != "newapi" && p.Kind != "sub2api") || !validProtocols(p.Protocols) {
		return core.Upstream{}, errors.New("名称、类型或协议无效")
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	apiKey, accessToken, userID := p.APIKey, p.AccessToken, p.UserID
	priority := 100
	balanceProtection := true
	pricingProfileID := p.PricingProfileID
	modelsLocked := false
	if id != 0 {
		priority = existing.Priority
		balanceProtection = existing.BalanceProtection
		modelsLocked = existing.ModelsLocked
		if pricingProfileID == nil {
			pricingProfileID = existing.PricingProfileID
		}
		if apiKey == "" {
			apiKey = existing.APIKey
		}
		if accessToken == "" {
			accessToken = existing.AccessToken
		}
		if userID == "" {
			userID = existing.UserID
		}
		if p.ClearBalanceCredentials {
			accessToken, userID = "", ""
		}
	}
	if p.ModelsLocked != nil {
		modelsLocked = *p.ModelsLocked
	}
	if p.Priority != nil {
		priority = *p.Priority
	}
	if p.BalanceProtection != nil {
		balanceProtection = *p.BalanceProtection
	}
	if p.Kind == "sub2api" {
		accessToken, userID = "", ""
	}
	return core.Upstream{
		ID: id, Name: name, Kind: p.Kind, BaseURL: base, UserAgent: userAgent, APIKey: apiKey, AccessToken: accessToken, UserID: userID,
		Enabled: enabled, BalanceProtection: balanceProtection, BalanceSuspended: existing.BalanceSuspended, ZeroBalanceChecks: existing.ZeroBalanceChecks,
		Priority: priority, Protocols: p.Protocols, Models: cleanStrings(p.Models),
		ModelsLocked: modelsLocked, ModelAliases: p.ModelAliases, ConnectTimeout: time.Duration(defaultInt(p.ConnectTimeoutMS, 5000)) * time.Millisecond,
		PricingProfileID: pricingProfileID,
		FirstByteTimeout: time.Duration(defaultInt(p.FirstByteTimeoutMS, 180000)) * time.Millisecond,
		IdleTimeout:      time.Duration(defaultInt(p.IdleTimeoutMS, 300000)) * time.Millisecond,
		FailureThreshold: defaultInt(p.FailureThreshold, 3), Cooldown: time.Duration(defaultInt(p.CooldownSeconds, 60)) * time.Second,
	}, nil
}

func viewUpstream(record store.UpstreamRecord) upstreamView {
	return upstreamView{
		ID: record.ID, Name: record.Name, Kind: record.Kind, BaseURL: record.BaseURL, UserAgent: record.UserAgent,
		HasAPIKey: record.APIKey != "", HasAccessToken: record.AccessToken != "", HasUserID: record.UserID != "",
		Enabled: record.Enabled, BalanceProtection: record.BalanceProtection, BalanceSuspended: record.BalanceSuspended, ZeroBalanceChecks: record.ZeroBalanceChecks,
		Priority: record.Priority, Protocols: record.Protocols, Models: record.Models, ModelsLocked: record.ModelsLocked,
		ModelAliases: record.ModelAliases, PricingProfileID: record.PricingProfileID, ConnectTimeoutMS: record.ConnectTimeout.Milliseconds(),
		FirstByteTimeoutMS: record.FirstByteTimeout.Milliseconds(), IdleTimeoutMS: record.IdleTimeout.Milliseconds(),
		FailureThreshold: record.FailureThreshold, CooldownSeconds: int64(record.Cooldown.Seconds()),
		HealthStatus: record.HealthStatus, ConsecutiveFailures: record.ConsecutiveFailure,
		ConsecutiveSuccesses: record.ConsecutiveSuccess, RecoveryStartedAt: record.RecoveryStartedAt,
		CircuitOpenUntil: record.CircuitOpenUntil, LastCheckAt: record.LastCheckAt, LastError: record.LastError,
		Balance: record.Balance, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func validateUpstreamPayload(p upstreamPayload, name, base string) error {
	if name == "" || len([]rune(name)) > 200 || len(base) > 2048 {
		return errors.New("名称或 Base URL 过长")
	}
	for _, value := range []string{p.APIKey, p.AccessToken, p.UserID} {
		if len(value) > 4096 {
			return errors.New("上游凭据过长")
		}
	}
	if len([]rune(strings.TrimSpace(p.UserAgent))) > 256 || strings.ContainsAny(p.UserAgent, "\r\n") {
		return errors.New("User-Agent 不能包含换行且不能超过 256 个字符")
	}
	if !validProtocols(p.Protocols) {
		return errors.New("协议无效")
	}
	if !validStringList(p.Models, 1000, 200) {
		return errors.New("模型列表过大或包含过长项目")
	}
	if len(p.ModelAliases) > 1000 {
		return errors.New("模型别名过多")
	}
	for alias, mapped := range p.ModelAliases {
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(mapped) == "" || len([]rune(alias)) > 200 || len([]rune(mapped)) > 200 {
			return errors.New("模型别名无效")
		}
	}
	if p.Priority != nil && (*p.Priority < 0 || *p.Priority > 1000000) {
		return errors.New("优先级超出范围")
	}
	for _, item := range []struct {
		value    int
		name     string
		min, max int
	}{{p.ConnectTimeoutMS, "连接超时", 0, 120000}, {p.FirstByteTimeoutMS, "首包超时", 0, 600000}, {p.IdleTimeoutMS, "空闲超时", 0, 1800000}, {p.FailureThreshold, "失败阈值", 0, 20}, {p.CooldownSeconds, "冷却时间", 0, 86400}} {
		if item.value < item.min || item.value > item.max {
			return fmt.Errorf("%s超出范围", item.name)
		}
	}
	return nil
}
