package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/store"
	"github.com/lib/pq"
)

type usageRateEntry struct {
	start time.Time
	count int
}
type usageRateLimiter struct {
	mu      sync.Mutex
	entries map[string]usageRateEntry
}

func (l *usageRateLimiter) allow(key string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]usageRateEntry)
	}
	for k, e := range l.entries {
		if now.Sub(e.start) >= time.Minute {
			delete(l.entries, k)
		}
	}
	e, exists := l.entries[key]
	if !exists {
		if len(l.entries) >= 4096 {
			return false
		}
		e.start = now
	}
	if e.count >= limit {
		return false
	}
	e.count++
	l.entries[key] = e
	return true
}
func usageNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
}
func usageTransport(w http.ResponseWriter, r *http.Request) bool {
	if isHTTPS(r) {
		return true
	}
	ip := net.ParseIP(clientIP(r))
	if ip != nil && ip.IsLoopback() {
		return true
	}
	writeError(w, 403, "https_required", "此接口要求 HTTPS")
	return false
}
func (s *Server) registerReadonly(mux *http.ServeMux) {
	// Like the existing admin routes, forwarding headers are normalized by
	// ProxyHeaders at the application entry point.
	mux.Handle("GET /api/readonly/usage", http.HandlerFunc(s.readonlyUsage))
	mux.Handle("GET /api/admin/usage-credentials", s.admin(http.HandlerFunc(s.listUsageCredentials)))
	mux.Handle("POST /api/admin/usage-credentials", s.admin(http.HandlerFunc(s.createUsageCredential)))
	mux.Handle("PUT /api/admin/usage-credentials/{id}", s.admin(http.HandlerFunc(s.updateUsageCredential)))
	mux.Handle("DELETE /api/admin/usage-credentials/{id}", s.admin(http.HandlerFunc(s.revokeUsageCredential)))
}
func (s *Server) readonlyUsage(w http.ResponseWriter, r *http.Request) {
	usageNoStore(w)
	if !usageTransport(w, r) {
		return
	}
	now := time.Now().UTC()
	limited := func() {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "rate_limited", "查询过于频繁，请稍后重试")
	}
	if !s.usageRates.allow("ip:"+clientIP(r), 120, now) {
		limited()
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(r.Header.Values("Authorization")) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "dapi_usage_") || len(parts[1]) != 54 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, 401, "invalid_usage_credential", "用量查询凭据无效")
		return
	}
	staleAfter := 2 * s.cfg.BalanceEvery
	if staleAfter < 30*time.Minute {
		staleAfter = 30 * time.Minute
	}
	result, id, err := s.store.ReadonlyUsage(r.Context(), auth.HashToken(parts[1]), now, staleAfter)
	switch {
	case errors.Is(err, store.ErrNotFound):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, 401, "invalid_usage_credential", "用量查询凭据无效")
	case errors.Is(err, store.ErrReadonlyForbidden):
		writeError(w, 403, "usage_forbidden", "凭据未获准查询用量")
	case err != nil:
		writeError(w, 503, "usage_unavailable", "用量数据暂不可用")
	default:
		if !s.usageRates.allow("credential:"+strconv.FormatInt(id, 10), 10, now) {
			limited()
			return
		}
		writeJSON(w, 200, result)
	}
}

type usageCredentialInput struct {
	Name       string   `json:"name"`
	Enabled    *bool    `json:"enabled"`
	StationIDs []string `json:"station_ids"`
}

func (in usageCredentialInput) scope() ([]int64, error) {
	if in.StationIDs == nil || len(in.StationIDs) > 1000 {
		return nil, errors.New("invalid scope")
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, raw := range in.StationIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid scope")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}
func usageMutationError(w http.ResponseWriter, err error) {
	var pg *pq.Error
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "usage_credential_not_found", "凭据不存在或已撤销")
	case errors.As(err, &pg) && pg.Code == "23503":
		writeError(w, 400, "invalid_station_scope", "中转站范围无效")
	default:
		writeError(w, 503, "usage_unavailable", "凭据操作暂不可用")
	}
}
func (s *Server) createUsageCredential(w http.ResponseWriter, r *http.Request) {
	usageNoStore(w)
	if !usageTransport(w, r) {
		return
	}
	var in usageCredentialInput
	if decodeJSON(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "请求格式无效")
		return
	}
	ids, err := in.scope()
	in.Name = strings.TrimSpace(in.Name)
	if err != nil || in.Name == "" || len([]rune(in.Name)) > 100 || in.Enabled != nil {
		writeError(w, 400, "invalid_request", "需要名称和明确的 station_ids 数组")
		return
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		usageMutationError(w, err)
		return
	}
	raw := "dapi_usage_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	id, err := s.store.CreateUsageCredential(r.Context(), in.Name, auth.HashToken(raw), ids)
	if err != nil {
		usageMutationError(w, err)
		return
	}
	// Audit only scope and identifier, never input names or the generated secret.
	s.audit(r, "usage_credential.create", "usage_credential", id, map[string]any{"station_ids": in.StationIDs})
	writeJSON(w, 201, map[string]any{"id": strconv.FormatInt(id, 10), "credential": raw, "station_ids": in.StationIDs})
}
func (s *Server) updateUsageCredential(w http.ResponseWriter, r *http.Request) {
	usageNoStore(w)
	if !usageTransport(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in usageCredentialInput
	if err != nil || id <= 0 || decodeJSON(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "请求格式无效")
		return
	}
	ids, err := in.scope()
	if err != nil || in.Enabled == nil {
		writeError(w, 400, "invalid_request", "需要 enabled 和明确的 station_ids 数组")
		return
	}
	if err = s.store.UpdateUsageCredential(r.Context(), id, *in.Enabled, ids); err != nil {
		usageMutationError(w, err)
		return
	}
	s.audit(r, "usage_credential.update", "usage_credential", id, map[string]any{"station_ids": in.StationIDs, "enabled": *in.Enabled})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) revokeUsageCredential(w http.ResponseWriter, r *http.Request) {
	usageNoStore(w)
	if !usageTransport(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "invalid_request", "凭据 ID 无效")
		return
	}
	if err = s.store.RevokeUsageCredential(r.Context(), id); err != nil {
		usageMutationError(w, err)
		return
	}
	s.audit(r, "usage_credential.revoke", "usage_credential", id, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) listUsageCredentials(w http.ResponseWriter, r *http.Request) {
	usageNoStore(w)
	if !usageTransport(w, r) {
		return
	}
	credentials, err := s.store.ListUsageCredentials(r.Context())
	if err != nil {
		usageMutationError(w, err)
		return
	}
	result := []map[string]any{}
	for _, c := range credentials {
		ids := []string{}
		for _, id := range c.StationIDs {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		result = append(result, map[string]any{"id": strconv.FormatInt(c.ID, 10), "name": c.Name, "enabled": c.Enabled, "station_ids": ids, "created_at": c.CreatedAt, "revoked_at": c.RevokedAt})
	}
	writeJSON(w, 200, result)
}
