package httpapi

import (
	"net/http"

	"github.com/gaoLfun/dapi/internal/store"
)

func (s *Server) WithRuntimeMetrics(metrics func() any) *Server { s.runtimeMetrics = metrics; return s }

func (s *Server) operationMetrics(w http.ResponseWriter, r *http.Request) {
	pending, dead, err := s.store.NotificationCounts(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	stats := s.store.DB().Stats()
	var gateway any
	if s.runtimeMetrics != nil {
		gateway = s.runtimeMetrics()
	}
	writeJSON(w, 200, map[string]any{"cleanup": s.store.CleanupStatus(), "gateway": gateway, "database": map[string]any{
		"open_connections": stats.OpenConnections, "in_use": stats.InUse, "idle": stats.Idle,
		"wait_count": stats.WaitCount, "wait_duration_ms": stats.WaitDuration.Milliseconds(),
	}, "notifications": map[string]int64{"pending": pending, "dead": dead}})
}

func (s *Server) deadNotifications(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("pagination") == "cursor" || r.URL.Query().Has("cursor") {
		cursor, err := store.ParsePageCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, 400, "invalid_cursor", "分页位置无效，请返回首页刷新")
			return
		}
		page, err := s.store.DeadNotificationPage(r.Context(), cursor)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	jobs, err := s.store.ListDeadNotifications(r.Context(), parseInt(r.URL.Query().Get("offset"), 0))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, jobs)
}

func (s *Server) retryNotification(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if id <= 0 {
		writeStoreError(w, store.ErrNotFound)
		return
	}
	changed, err := s.store.RetryDeadNotification(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !changed {
		writeError(w, 409, "notification_not_retryable", "任务已重试，或通知渠道已停用/删除")
		return
	}
	s.audit(r, "notification.retry", "notification", id, map[string]any{"queued": true})
	writeJSON(w, 200, map[string]bool{"queued": true})
}
