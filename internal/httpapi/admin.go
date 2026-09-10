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
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
	"github.com/lib/pq"
)

const sessionCookie = "dapi_session"

type adminContextKey struct{}

type Server struct {
	runtimeMetrics func() any
	store          *store.Store
	cfg            config.Config
	operations     Operations
	notifier       ops.Notifier
	logins         loginLimiter
	usageRates     usageRateLimiter
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
	s.registerReadonly(mux)
	mux.Handle("GET /api/admin/metrics", s.admin(http.HandlerFunc(s.operationMetrics)))
	mux.Handle("GET /api/admin/notifications/dead", s.admin(http.HandlerFunc(s.deadNotifications)))
	mux.Handle("POST /api/admin/notifications/{id}/retry", s.admin(http.HandlerFunc(s.retryNotification)))
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
	mux.Handle("GET /api/admin/alert-history", s.admin(http.HandlerFunc(s.alertHistory)))
	mux.Handle("GET /api/admin/logs", s.admin(http.HandlerFunc(s.logs)))
	mux.Handle("GET /api/admin/usage", s.admin(http.HandlerFunc(s.usage)))
	mux.Handle("GET /api/admin/channels", s.admin(http.HandlerFunc(s.listChannels)))
	mux.Handle("POST /api/admin/channels", s.admin(http.HandlerFunc(s.createChannel)))
	mux.Handle("POST /api/admin/channels/{id}/test", s.admin(http.HandlerFunc(s.testChannel)))
	mux.Handle("DELETE /api/admin/channels/{id}", s.admin(http.HandlerFunc(s.deleteChannel)))
	mux.Handle("GET /api/admin/alert-rules", s.admin(http.HandlerFunc(s.listAlertRules)))
	mux.Handle("POST /api/admin/alert-rules", s.admin(http.HandlerFunc(s.createAlertRule)))
	mux.Handle("PUT /api/admin/alert-rules/{id}", s.admin(http.HandlerFunc(s.updateAlertRule)))
	mux.Handle("DELETE /api/admin/alert-rules/{id}", s.admin(http.HandlerFunc(s.deleteAlertRule)))
	mux.Handle("GET /api/admin/settings", s.admin(http.HandlerFunc(s.getSettings)))
	mux.Handle("PUT /api/admin/settings", s.admin(http.HandlerFunc(s.updateSettings)))
	mux.Handle("GET /api/admin/pricing/profiles/{id}", s.admin(http.HandlerFunc(s.pricingProfile)))
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
			s.notifySecurity(ops.Event{Type: "login_failure", State: "firing", Message: "管理员登录失败次数过多，来源 IP：" + ip, At: time.Now()})
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
	if err := s.store.CreateSessionForPassword(r.Context(), hash, admin, ip, r.UserAgent(), expires); err != nil {
		if errors.Is(err, store.ErrPasswordChanged) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "密码已变更，请重新登录")
			return
		}
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
		s.notifySecurity(ops.Event{Type: "new_login_ip", State: "firing", Message: "管理员从新的 IP 登录：" + ip, At: time.Now()})
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
	s.notifySecurity(ops.Event{Type: "password_changed", State: "firing", Message: "管理员密码已修改", At: time.Now()})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
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

func isHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	default:
		return false
	}
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
	if errors.Is(err, store.ErrUpstreamConfigChanged) {
		writeError(w, http.StatusConflict, "upstream_config_changed", "上游配置已变更，请刷新后重新操作")
		return
	}
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

func ProxyHeaders(next http.Handler, trusted bool, trustedCIDRs ...string) http.Handler {
	var networks []*net.IPNet
	for _, raw := range trustedCIDRs {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(raw)); err == nil {
			networks = append(networks, network)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trusted {
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Proto")
			r.Header.Del("X-Real-IP")
			next.ServeHTTP(w, r)
			return
		}
		proxyIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			proxyIP = r.RemoteAddr
		}
		remoteIP := net.ParseIP(strings.TrimSpace(proxyIP))
		trustedPeer := len(networks) == 0 && isPrivateProxyPeer(remoteIP)
		for _, network := range networks {
			if remoteIP != nil && network.Contains(remoteIP) {
				trustedPeer = true
				break
			}
		}
		if trustedPeer {
			if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
				clone := r.Clone(r.Context())
				clone.RemoteAddr = net.JoinHostPort(ip.String(), "0")
				r = clone
			}
		} else {
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Proto")
			r.Header.Del("X-Real-IP")
		}
		next.ServeHTTP(w, r)
	})
}

func isPrivateProxyPeer(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
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
	if _, exists := l.attempts[ip]; !exists && len(l.attempts) >= 4096 {
		return time.Minute
	}
	entry := l.attempts[ip]
	return entry.blockedTill.Sub(now)
}

func (l *loginLimiter) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.cleanupLocked(now)
	if _, exists := l.attempts[ip]; !exists && len(l.attempts) >= 4096 {
		return true
	}
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
	if !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < time.Minute {
		return
	}
	for ip, entry := range l.attempts {
		if now.Sub(entry.windowStart) > 15*time.Minute && now.After(entry.blockedTill) {
			delete(l.attempts, ip)
		}
	}
	l.lastCleanup = now
}
