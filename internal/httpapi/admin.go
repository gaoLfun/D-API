package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/config"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/netguard"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
	"github.com/lib/pq"
)

const sessionCookie = "dapi_session"

type adminContextKey struct{}

type Server struct {
	store      *store.Store
	cfg        config.Config
	operations Operations
	notifier   ops.Notifier
	logins     loginLimiter
}

type Operations interface {
	Check(context.Context, int64) (ops.Health, error)
	Probe(context.Context, core.Upstream) ops.Health
	TestModel(context.Context, core.Upstream, string) ops.ModelTest
	Balance(context.Context, int64) (core.Upstream, core.Balance, core.BalanceTransition, error)
	Models(context.Context, int64) ([]string, error)
}

type loginLimiter struct {
	mu          sync.Mutex
	attempts    map[string]loginAttempt
	lastCleanup time.Time
}

type loginAttempt struct {
	count       int
	windowStart time.Time
	blockedTill time.Time
}

func New(store *store.Store, cfg config.Config, operations Operations, notifier ops.Notifier) *Server {
	return &Server{store: store, cfg: cfg, operations: operations, notifier: notifier, logins: loginLimiter{attempts: make(map[string]loginAttempt)}}
}

func BootstrapAdmin(ctx context.Context, database *store.Store, username, password string) error {
	count, err := database.AdminCount(ctx)
	if err != nil || count > 0 {
		return err
	}
	if username == "" || password == "" {
		return errors.New("first start requires DAPI_ADMIN_USERNAME and DAPI_ADMIN_PASSWORD")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("initial admin password: %w", err)
	}
	_, err = database.CreateAdmin(ctx, username, hash)
	return err
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/login", s.login)
	mux.Handle("POST /api/admin/logout", s.admin(http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/admin/me", s.admin(http.HandlerFunc(s.me)))
	mux.Handle("PUT /api/admin/password", s.admin(http.HandlerFunc(s.changePassword)))
	mux.Handle("GET /api/admin/dashboard", s.admin(http.HandlerFunc(s.dashboard)))
	mux.Handle("GET /api/admin/upstreams", s.admin(http.HandlerFunc(s.listUpstreams)))
	mux.Handle("POST /api/admin/upstreams", s.admin(http.HandlerFunc(s.createUpstream)))
	mux.Handle("POST /api/admin/upstreams/test", s.admin(http.HandlerFunc(s.testUpstream)))
	mux.Handle("POST /api/admin/upstreams/test-model", s.admin(http.HandlerFunc(s.testModel)))
	mux.Handle("POST /api/admin/upstreams/test-models/audit", s.admin(http.HandlerFunc(s.auditModelTests)))
	mux.Handle("PUT /api/admin/upstreams/{id}", s.admin(http.HandlerFunc(s.updateUpstream)))
	mux.Handle("DELETE /api/admin/upstreams/{id}", s.admin(http.HandlerFunc(s.deleteUpstream)))
	mux.Handle("POST /api/admin/upstreams/{id}/check", s.admin(http.HandlerFunc(s.checkUpstream)))
	mux.Handle("POST /api/admin/upstreams/{id}/balance", s.admin(http.HandlerFunc(s.balanceUpstream)))
	mux.Handle("POST /api/admin/upstreams/{id}/models", s.admin(http.HandlerFunc(s.modelsUpstream)))
	mux.Handle("GET /api/admin/groups", s.admin(http.HandlerFunc(s.listGroups)))
	mux.Handle("POST /api/admin/groups", s.admin(http.HandlerFunc(s.createGroup)))
	mux.Handle("PUT /api/admin/groups/{id}", s.admin(http.HandlerFunc(s.updateGroup)))
	mux.Handle("DELETE /api/admin/groups/{id}", s.admin(http.HandlerFunc(s.deleteGroup)))
	mux.Handle("GET /api/admin/keys", s.admin(http.HandlerFunc(s.listKeys)))
	mux.Handle("POST /api/admin/keys", s.admin(http.HandlerFunc(s.createKey)))
	mux.Handle("GET /api/admin/keys/{id}/secret", s.admin(http.HandlerFunc(s.keySecret)))
	mux.Handle("PUT /api/admin/keys/{id}", s.admin(http.HandlerFunc(s.updateKey)))
	mux.Handle("DELETE /api/admin/keys/{id}", s.admin(http.HandlerFunc(s.deleteKey)))
	mux.Handle("GET /api/admin/logs", s.admin(http.HandlerFunc(s.logs)))
	mux.Handle("GET /api/admin/usage", s.admin(http.HandlerFunc(s.usage)))
	mux.Handle("GET /api/admin/channels", s.admin(http.HandlerFunc(s.listChannels)))
	mux.Handle("POST /api/admin/channels", s.admin(http.HandlerFunc(s.createChannel)))
	mux.Handle("DELETE /api/admin/channels/{id}", s.admin(http.HandlerFunc(s.deleteChannel)))
	mux.Handle("GET /api/admin/alert-rules", s.admin(http.HandlerFunc(s.listAlertRules)))
	mux.Handle("POST /api/admin/alert-rules", s.admin(http.HandlerFunc(s.createAlertRule)))
	mux.Handle("PUT /api/admin/alert-rules/{id}", s.admin(http.HandlerFunc(s.updateAlertRule)))
	mux.Handle("DELETE /api/admin/alert-rules/{id}", s.admin(http.HandlerFunc(s.deleteAlertRule)))
	mux.Handle("GET /api/admin/settings", s.admin(http.HandlerFunc(s.getSettings)))
	mux.Handle("PUT /api/admin/settings", s.admin(http.HandlerFunc(s.updateSettings)))
	mux.Handle("GET /api/admin/pricing", s.admin(http.HandlerFunc(s.pricing)))
	mux.Handle("POST /api/admin/pricing/profiles", s.admin(http.HandlerFunc(s.createPricingProfile)))
	mux.Handle("PUT /api/admin/pricing/profiles/{id}", s.admin(http.HandlerFunc(s.updatePricingProfile)))
	mux.Handle("DELETE /api/admin/pricing/profiles/{id}", s.admin(http.HandlerFunc(s.deletePricingProfile)))
	mux.Handle("POST /api/admin/pricing/refresh", s.admin(http.HandlerFunc(s.refreshPricing)))
	mux.Handle("POST /api/admin/pricing/backfill", s.admin(http.HandlerFunc(s.backfillPricing)))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := s.logins.wait(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "登录尝试过多，请稍后再试")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	username := strings.TrimSpace(input.Username)
	accountKey := "account:" + strings.ToLower(username)
	if wait := s.logins.wait(accountKey); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "登录尝试过多，请稍后再试")
		return
	}
	if len([]rune(username)) > 200 || len(input.Password) > 1024 {
		_ = s.logins.fail(ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	admin, err := s.store.AdminByUsername(r.Context(), username)
	if err != nil || !auth.CheckPassword(admin.PasswordHash, input.Password) {
		blockedIP := s.logins.fail(ip)
		blockedAccount := s.logins.fail(accountKey)
		blocked := blockedIP || blockedAccount
		detail, _ := json.Marshal(map[string]any{"username": username, "blocked": blocked})
		if err := s.store.WriteAudit(r.Context(), nil, "admin.login_failed", "admin", username, detail, ip); err != nil {
			slog.Error("failed login audit write failed", "error", err)
		}
		if blocked {
			s.notifySecurity(ops.Event{Type: "login_failure", State: "firing", Message: "administrator login failed repeatedly from " + ip, At: time.Now()})
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	s.logins.success(ip)
	s.logins.success(accountKey)
	knownIP, knownIPErr := s.store.HasSuccessfulLoginFromIP(r.Context(), admin.ID, ip)
	if knownIPErr != nil {
		slog.Error("login IP lookup failed", "error", knownIPErr)
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "无法创建会话")
		return
	}
	expires := time.Now().Add(s.cfg.SessionTTL)
	if err := s.store.CreateSession(r.Context(), hash, admin.ID, ip, r.UserAgent(), expires); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "无法创建会话")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, Expires: expires,
	})
	detail, _ := json.Marshal(map[string]string{"user_agent": r.UserAgent()})
	if err := s.store.WriteAudit(r.Context(), &admin.ID, "admin.login", "admin", strconv.FormatInt(admin.ID, 10), detail, ip); err != nil {
		slog.Error("login audit write failed", "error", err)
	}
	if knownIPErr == nil && !knownIP {
		s.notifySecurity(ops.Event{Type: "new_login_ip", State: "firing", Message: "administrator logged in from a new IP: " + ip, At: time.Now()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": admin.Username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), auth.HashToken(cookie.Value)); err != nil {
			slog.Error("administrator logout failed", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(adminContextKey{}).(store.Admin)
	writeJSON(w, http.StatusOK, map[string]any{"id": admin.ID, "username": admin.Username})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(adminContextKey{}).(store.Admin)
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &input); err != nil || len(input.CurrentPassword) > 1024 || len(input.NewPassword) > 1024 || !auth.CheckPassword(admin.PasswordHash, input.CurrentPassword) {
		writeError(w, http.StatusBadRequest, "invalid_password", "当前密码错误")
		return
	}
	hash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), admin.ID, hash); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "admin.password_changed", "admin", admin.ID, nil)
	s.notifySecurity(ops.Event{Type: "password_changed", State: "firing", Message: "administrator password was changed", At: time.Now()})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Dashboard(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

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

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

type keyPayload struct {
	Name      string   `json:"name"`
	GroupID   int64    `json:"group_id"`
	Enabled   *bool    `json:"enabled"`
	Protocols []string `json:"protocols"`
	Models    []string `json:"models"`
}

type groupPayload struct {
	Name        string  `json:"name"`
	Enabled     *bool   `json:"enabled"`
	UpstreamIDs []int64 `json:"upstream_ids"`
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func validGroupPayload(input groupPayload, allowEmpty bool) bool {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 200 {
		return false
	}
	if !allowEmpty && len(cleanGroupIDs(input.UpstreamIDs)) == 0 {
		return false
	}
	return true
}

func cleanGroupIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var input groupPayload
	if err := decodeJSON(w, r, &input); err != nil || !validGroupPayload(input, false) {
		writeError(w, http.StatusBadRequest, "invalid_group", "分组名称或上游无效")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	id, err := s.store.CreateGroup(r.Context(), core.Group{Name: strings.TrimSpace(input.Name), Enabled: enabled, UpstreamIDs: cleanGroupIDs(input.UpstreamIDs)})
	if err != nil {
		if errors.Is(err, store.ErrInvalidGroup) {
			writeError(w, http.StatusBadRequest, "invalid_group", "分组包含不存在的上游")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.audit(r, "group.create", "group", id, map[string]any{"name": strings.TrimSpace(input.Name), "upstream_ids": cleanGroupIDs(input.UpstreamIDs)})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input groupPayload
	if err := decodeJSON(w, r, &input); err != nil || !validGroupPayload(input, true) {
		writeError(w, http.StatusBadRequest, "invalid_group", "分组名称或上游无效")
		return
	}
	existing, err := s.store.Group(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	enabled := existing.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	updated := core.Group{ID: id, Name: strings.TrimSpace(input.Name), Enabled: enabled, UpstreamIDs: cleanGroupIDs(input.UpstreamIDs)}
	if err := s.store.UpdateGroup(r.Context(), updated); err != nil {
		if errors.Is(err, store.ErrGroupHasKeys) {
			writeError(w, http.StatusConflict, "group_has_keys", "分组仍有绑定密钥，不能停用")
			return
		}
		if errors.Is(err, store.ErrInvalidGroup) {
			writeError(w, http.StatusBadRequest, "invalid_group", "启用分组必须绑定上游")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.audit(r, "group.update", "group", id, map[string]any{"name": updated.Name, "enabled": updated.Enabled, "before": existing.UpstreamIDs, "after": updated.UpstreamIDs})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrGroupHasKeys) {
			writeError(w, http.StatusConflict, "group_has_keys", "分组仍有绑定密钥，请先迁移密钥")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.audit(r, "group.delete", "group", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var input keyPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !validKeyPayload(input) {
		writeError(w, http.StatusBadRequest, "invalid_key", "名称或协议无效")
		return
	}
	if !s.requireAvailableGroup(w, r, input.GroupID) {
		return
	}
	raw, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "无法创建密钥")
		return
	}
	id, err := s.store.InsertAPIKeyWithSecretInGroup(r.Context(), strings.TrimSpace(input.Name), prefix, hash, raw, input.GroupID, input.Protocols, cleanStrings(input.Models))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "api_key.create", "api_key", id, map[string]any{"name": input.Name, "group_id": input.GroupID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "key": raw, "prefix": prefix})
}

func (s *Server) keySecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret, err := s.store.APIKeySecret(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "记录不存在")
		return
	}
	if errors.Is(err, store.ErrAPIKeySecretUnavailable) {
		writeError(w, http.StatusUnprocessableEntity, "secret_unavailable", "该密钥没有可复制的加密副本，请重新创建密钥")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]string{"key": secret})
}

func (s *Server) updateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input keyPayload
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !validKeyPayload(input) {
		writeError(w, http.StatusBadRequest, "invalid_key", "名称或协议无效")
		return
	}
	if !s.requireAvailableGroup(w, r, input.GroupID) {
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	existing, err := s.store.APIKey(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	err = s.store.UpdateAPIKey(r.Context(), core.APIKey{ID: id, GroupID: input.GroupID, Name: strings.TrimSpace(input.Name), Enabled: enabled, Protocols: input.Protocols, Models: cleanStrings(input.Models)})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "api_key.update", "api_key", id, map[string]any{"name": input.Name, "enabled": enabled, "before_group_id": existing.GroupID, "after_group_id": input.GroupID})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) requireAvailableGroup(w http.ResponseWriter, r *http.Request, id int64) bool {
	if id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_group", "必须选择分组")
		return false
	}
	err := s.store.GroupAvailable(r.Context(), id)
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "分组不存在")
	case errors.Is(err, store.ErrGroupDisabled):
		writeError(w, http.StatusBadRequest, "group_disabled", "分组已停用")
	case errors.Is(err, store.ErrGroupEmpty):
		writeError(w, http.StatusBadRequest, "group_empty", "分组没有上游")
	default:
		writeStoreError(w, err)
	}
	return false
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "api_key.delete", "api_key", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	filter := store.LogFilter{
		Limit: parseInt(r.URL.Query().Get("limit"), 50), Offset: parseInt(r.URL.Query().Get("offset"), 0),
		UpstreamID: parseInt64(r.URL.Query().Get("upstream_id")),
		GroupID:    parseInt64(r.URL.Query().Get("group_id")),
	}
	filter.StatusMin, filter.StatusMax = statusRange(r.URL.Query().Get("status"))
	logs, err := s.store.ListRequestLogs(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	dimension := strings.TrimSpace(query.Get("dimension"))
	if dimension != "" && dimension != "upstream" && dimension != "api_key" && dimension != "group" && dimension != "protocol" && dimension != "model" {
		writeError(w, http.StatusBadRequest, "invalid_dimension", "维度无效")
		return
	}
	granularity := strings.TrimSpace(query.Get("granularity"))
	if granularity == "" {
		granularity = "day"
	}
	if granularity != "day" && granularity != "week" && granularity != "month" {
		writeError(w, http.StatusBadRequest, "invalid_granularity", "粒度无效")
		return
	}
	filter := store.UsageFilter{
		Days: parseInt(query.Get("days"), 30), Dimension: dimension, Granularity: granularity,
		TopN: parseInt(query.Get("top_n"), 5), UpstreamID: parseInt64(query.Get("upstream_id")),
		APIKeyID: parseInt64(query.Get("api_key_id")), Protocol: strings.TrimSpace(query.Get("protocol")), Model: strings.TrimSpace(query.Get("model")),
		GroupID: parseInt64(query.Get("group_id")),
	}
	if rawDays := strings.TrimSpace(query.Get("days")); rawDays != "" {
		days := parseInt(rawDays, 0)
		if days < 1 || days > 365 {
			writeError(w, http.StatusBadRequest, "invalid_days", "时间范围必须为 1 到 365 天")
			return
		}
	}
	if filter.TopN <= 0 {
		filter.TopN = 5
	} else if filter.TopN > 100 {
		filter.TopN = 100
	}
	for _, item := range []struct {
		value  string
		target **time.Time
		name   string
	}{{query.Get("from"), &filter.FromDay, "from"}, {query.Get("to"), &filter.ToDay, "to"}} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", item.value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_date", item.name+" 日期无效")
			return
		}
		*item.target = &parsed
	}
	if filter.FromDay != nil && filter.ToDay != nil && filter.FromDay.After(*filter.ToDay) {
		writeError(w, http.StatusBadRequest, "invalid_date_range", "日期范围无效")
		return
	}
	if filter.FromDay != nil && filter.ToDay != nil && filter.ToDay.Sub(*filter.FromDay) > 364*24*time.Hour {
		writeError(w, http.StatusBadRequest, "invalid_date_range", "日期范围不能超过 365 天")
		return
	}
	if filter.FromDay != nil || filter.ToDay != nil {
		now := time.Now().UTC()
		defaultTo := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		effectiveTo := defaultTo
		if filter.ToDay != nil {
			effectiveTo = filter.ToDay.UTC()
		}
		effectiveFrom := effectiveTo.AddDate(0, 0, -(filter.Days - 1))
		if filter.Days <= 0 {
			effectiveFrom = effectiveTo.AddDate(0, 0, -29)
		}
		if filter.FromDay != nil {
			effectiveFrom = filter.FromDay.UTC()
		}
		if effectiveTo.Sub(effectiveFrom) > 364*24*time.Hour {
			writeError(w, http.StatusBadRequest, "invalid_date_range", "日期范围不能超过 365 天")
			return
		}
	}
	usage, err := s.store.UsageWithFilter(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	totals, err := s.store.UsageTotals(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"daily": usage, "items": usage, "totals": totals, "summary": totals, "dimension": dimension, "granularity": granularity, "top_n": filter.TopN})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		views = append(views, map[string]any{"id": channel.ID, "name": channel.Name, "kind": channel.Kind, "enabled": channel.Enabled, "configured": len(channel.Config) > 0, "created_at": channel.CreatedAt})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var input store.NotificationChannel
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateChannel(input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel", "通知渠道配置无效")
		return
	}
	id, err := s.store.CreateChannel(r.Context(), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "channel.create", "channel", id, map[string]any{"name": input.Name, "kind": input.Kind})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func validateChannel(channel store.NotificationChannel) error {
	if strings.TrimSpace(channel.Name) == "" || len([]rune(strings.TrimSpace(channel.Name))) > 200 {
		return errors.New("invalid channel name")
	}
	if channel.Kind != "email" && channel.Kind != "webhook" {
		return errors.New("invalid channel kind")
	}
	if len(channel.Config) == 0 || len(channel.Config) > 64<<10 || !json.Valid(channel.Config) {
		return errors.New("invalid channel config")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(channel.Config, &values); err != nil || values == nil || len(values) > 16 {
		return errors.New("invalid channel config")
	}
	if channel.Kind == "webhook" {
		var urlValue string
		if err := json.Unmarshal(values["url"], &urlValue); err != nil || len(urlValue) > 2048 {
			return errors.New("invalid webhook URL")
		}
		parsed, err := url.Parse(strings.TrimSpace(urlValue))
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("invalid webhook URL")
		}
		if err := netguard.ValidateURL(urlValue); err != nil {
			return errors.New("invalid webhook URL")
		}
		if raw, ok := values["headers"]; ok {
			var headers map[string]string
			if json.Unmarshal(raw, &headers) != nil || len(headers) > 32 {
				return errors.New("invalid webhook headers")
			}
			for key, value := range headers {
				if strings.TrimSpace(key) == "" || len(key) > 128 || len(value) > 4096 || isHopHeader(key) {
					return errors.New("invalid webhook headers")
				}
			}
		}
		return nil
	}
	var host, address string
	_ = json.Unmarshal(values["smtp_host"], &host)
	_ = json.Unmarshal(values["address"], &address)
	if len(strings.TrimSpace(host)) > 253 || len(strings.TrimSpace(address)) > 512 {
		return errors.New("invalid SMTP host")
	}
	var port int
	if raw, ok := values["smtp_port"]; ok {
		if json.Unmarshal(raw, &port) != nil || port < 1 || port > 65535 {
			return errors.New("invalid SMTP port")
		}
	} else {
		port = 587
	}
	if strings.TrimSpace(address) != "" {
		if err := netguard.ValidateAddress(address); err != nil {
			return errors.New("invalid SMTP address")
		}
	} else if strings.TrimSpace(host) == "" || netguard.ValidateAddress(net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))) != nil {
		return errors.New("invalid SMTP host")
	}
	for _, key := range []string{"username", "password", "from"} {
		if raw, ok := values[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil || len(value) > 1024 {
				return errors.New("invalid SMTP credential")
			}
		}
	}
	rawTo, ok := values["to"]
	if !ok {
		return errors.New("invalid SMTP recipients")
	}
	var recipient string
	if json.Unmarshal(rawTo, &recipient) == nil {
		if strings.TrimSpace(recipient) == "" || len(recipient) > 4096 || strings.ContainsAny(recipient, "\r\n") {
			return errors.New("invalid SMTP recipients")
		}
	} else {
		var recipients []string
		if json.Unmarshal(rawTo, &recipients) != nil || len(recipients) == 0 || len(recipients) > 100 {
			return errors.New("invalid SMTP recipients")
		}
		for _, value := range recipients {
			if strings.TrimSpace(value) == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") {
				return errors.New("invalid SMTP recipients")
			}
		}
	}
	return nil
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "channel.delete", "channel", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var input store.AlertRule
	if err := decodeJSON(w, r, &input); err != nil || !validAlertRule(input, true) {
		writeError(w, http.StatusBadRequest, "invalid_alert_rule", "告警规则无效")
		return
	}
	id, err := s.store.CreateAlertRule(r.Context(), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.create", "alert_rule", id, map[string]any{"event": input.Event, "upstream_id": input.UpstreamID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.AlertRule
	if err := decodeJSON(w, r, &input); err != nil || !validAlertRule(input, false) {
		writeError(w, http.StatusBadRequest, "invalid_alert_rule", "告警规则无效")
		return
	}
	input.ID = id
	if err := s.store.UpdateAlertRule(r.Context(), input); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.update", "alert_rule", id, map[string]any{"threshold": input.Threshold, "enabled": input.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "alert_rule.delete", "alert_rule", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	maxAttempts, err := s.store.MaxAttempts(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"max_attempts": maxAttempts})
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MaxAttempts int `json:"max_attempts"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.MaxAttempts < 1 || input.MaxAttempts > 5 {
		writeError(w, http.StatusBadRequest, "invalid_settings", "最大尝试次数必须为 1 到 5")
		return
	}
	if err := s.store.SetMaxAttempts(r.Context(), input.MaxAttempts); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "settings.update", "settings", 0, map[string]any{"max_attempts": input.MaxAttempts})
	writeJSON(w, http.StatusOK, input)
}

func (s *Server) pricing(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.ListPricingProfiles(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rate, err := s.store.USDCNYRate(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles, "usd_cny_rate": rate})
}

func (s *Server) createPricingProfile(w http.ResponseWriter, r *http.Request) {
	var input store.PricingProfile
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", err.Error())
		return
	}
	id, err := s.store.SavePricingProfile(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrInvalidPricing) {
			writeError(w, http.StatusBadRequest, "invalid_pricing", "价格档案无效")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.audit(r, "pricing_profile.create", "pricing_profile", id, map[string]any{"name": input.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updatePricingProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.PricingProfile
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", err.Error())
		return
	}
	input.ID = id
	if _, err := s.store.SavePricingProfile(r.Context(), input); err != nil {
		if errors.Is(err, store.ErrInvalidPricing) {
			writeError(w, http.StatusBadRequest, "invalid_pricing", "价格档案无效")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.audit(r, "pricing_profile.update", "pricing_profile", id, map[string]any{"name": input.Name})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) refreshPricing(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RefreshPricingProfiles(r.Context()); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "pricing.refresh", "pricing_profile", 0, nil)
	writeJSON(w, http.StatusOK, map[string]any{"checked_at": time.Now().UTC(), "mode": "litellm-sync"})
}

func (s *Server) backfillPricing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", err.Error())
		return
	}
	from, err := parseOptionalDate(input.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", "from 日期无效")
		return
	}
	to, err := parseOptionalDate(input.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", "to 日期无效")
		return
	}
	result, err := s.store.BackfillPricingCosts(r.Context(), from, to)
	if errors.Is(err, store.ErrInvalidPricing) {
		writeError(w, http.StatusBadRequest, "invalid_pricing", "回算范围必须在 365 天以内")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "pricing.backfill", "pricing_profile", 0, result)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) deletePricingProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeletePricingProfile(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r, "pricing_profile.delete", "pricing_profile", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "origin_rejected", "请求来源无效")
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "请先登录")
			return
		}
		admin, err := s.store.AdminBySession(r.Context(), auth.HashToken(cookie.Value))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "会话已失效")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminContextKey{}, admin)))
	})
}

func (s *Server) audit(r *http.Request, action, targetType string, targetID int64, detail any) {
	admin := r.Context().Value(adminContextKey{}).(store.Admin)
	value, _ := json.Marshal(detail)
	if err := s.store.WriteAudit(r.Context(), &admin.ID, action, targetType, strconv.FormatInt(targetID, 10), value, clientIP(r)); err != nil {
		slog.Error("audit log write failed", "action", action, "error", err)
	}
}

func (s *Server) notifySecurity(event ops.Event) {
	s.notifyEvent(event)
}

func (s *Server) notifyEvent(event ops.Event) {
	var upstreamID *int64
	if event.UpstreamID > 0 {
		upstreamID = &event.UpstreamID
	}
	if err := s.store.SaveAlertEvent(context.Background(), upstreamID, event.Type, event.State, event.Message); err != nil {
		slog.Error("event write failed", "event", event.Type, "error", err)
	}
	s.notifyPersistedEvent(event)
}

func (s *Server) notifyPersistedEvent(event ops.Event) {
	if s.notifier == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.notifier.Notify(ctx, event); err != nil {
			slog.Error("event notification failed", "event", event.Type, "error", err)
		}
	}()
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
		FirstByteTimeout: time.Duration(defaultInt(p.FirstByteTimeoutMS, 60000)) * time.Millisecond,
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
		CircuitOpenUntil: record.CircuitOpenUntil, LastCheckAt: record.LastCheckAt, LastError: record.LastError,
		Balance: record.Balance, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func validProtocols(protocols []string) bool {
	if len(protocols) == 0 || len(protocols) > 3 {
		return false
	}
	seen := make(map[string]struct{}, len(protocols))
	for _, protocol := range protocols {
		if protocol != core.ProtocolResponses && protocol != core.ProtocolMessages && protocol != core.ProtocolChat {
			return false
		}
		if _, ok := seen[protocol]; ok {
			return false
		}
		seen[protocol] = struct{}{}
	}
	return true
}

func validKeyPayload(input keyPayload) bool {
	name := strings.TrimSpace(input.Name)
	return name != "" && len([]rune(name)) <= 200 && validProtocols(input.Protocols) && validStringList(input.Models, 1000, 200)
}

func isHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	default:
		return false
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

func validStringList(values []string, maxItems, maxItemRunes int) bool {
	if len(values) > maxItems {
		return false
	}
	for _, value := range values {
		if len([]rune(strings.TrimSpace(value))) > maxItemRunes {
			return false
		}
	}
	return true
}

func validAlertRule(rule store.AlertRule, creating bool) bool {
	if creating {
		validEvent := map[string]bool{
			"low_balance": true, "balance_unavailable": true, "error_rate": true,
			"latency": true,
		}
		if !validEvent[rule.Event] || rule.UpstreamID == nil || *rule.UpstreamID <= 0 {
			return false
		}
	}
	return rule.Threshold != nil && *rule.Threshold >= 0 && rule.WindowSeconds >= 60 && rule.WindowSeconds <= 86400 && rule.CooldownSeconds >= 60 && rule.CooldownSeconds <= 604800
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("JSON 请求无效")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON 请求无效")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "记录不存在")
		return
	}
	var databaseError *pq.Error
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		writeError(w, http.StatusConflict, "already_exists", "名称或记录已存在")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "操作失败")
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID 无效")
		return 0, false
	}
	return id, true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func ProxyHeaders(next http.Handler, trusted bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trusted {
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Proto")
			r.Header.Del("X-Real-IP")
			next.ServeHTTP(w, r)
			return
		}
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			clone := r.Clone(r.Context())
			clone.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			r = clone
		}
		next.ServeHTTP(w, r)
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

func parseOptionalDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", value)
}

func parseInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func statusRange(value string) (int, int) {
	switch value {
	case "success":
		return 200, 399
	case "error":
		return 400, 999
	case "5xx":
		return 500, 599
	case "429":
		return 429, 429
	default:
		return 0, 0
	}
}

func defaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func (l *loginLimiter) wait(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.cleanupLocked(now)
	entry := l.attempts[ip]
	return entry.blockedTill.Sub(now)
}

func (l *loginLimiter) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.cleanupLocked(now)
	entry := l.attempts[ip]
	if now.Sub(entry.windowStart) > 15*time.Minute {
		entry = loginAttempt{windowStart: now}
	}
	entry.count++
	blocked := entry.count >= 5
	if blocked {
		entry.blockedTill = now.Add(15 * time.Minute)
	}
	l.attempts[ip] = entry
	return blocked
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}

func (l *loginLimiter) cleanupLocked(now time.Time) {
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) < time.Minute {
		return
	}
	for ip, entry := range l.attempts {
		if now.Sub(entry.windowStart) > 15*time.Minute && now.After(entry.blockedTill) {
			delete(l.attempts, ip)
		}
	}
	l.lastCleanup = now
}
