package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/store"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Dashboard(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	filter := store.LogFilter{
		Limit: parseInt(r.URL.Query().Get("limit"), 50), Offset: parseInt(r.URL.Query().Get("offset"), 0),
		UpstreamID: parseInt64(r.URL.Query().Get("upstream_id")),
		GroupID:    parseInt64(r.URL.Query().Get("group_id")),
	}
	filter.APIKeyID = parseInt64(r.URL.Query().Get("api_key_id"))
	filter.UpstreamBaseURL = strings.TrimSpace(r.URL.Query().Get("upstream_base_url"))
	filter.Model = strings.TrimSpace(r.URL.Query().Get("model"))
	filter.Protocol = strings.TrimSpace(r.URL.Query().Get("protocol"))
	if filter.APIKeyID < 0 || (r.URL.Query().Get("api_key_id") != "" && filter.APIKeyID == 0) || len(filter.UpstreamBaseURL) > 2048 || len(filter.Model) > 512 || len(filter.Protocol) > 64 {
		writeError(w, 400, "invalid_filter", "日志筛选条件无效")
		return
	}
	filter.AttemptScope = r.URL.Query().Get("scope") == "attempts"
	filter.AttemptFailure = r.URL.Query().Get("status") == "attempt_error"
	for key, target := range map[string]**time.Time{"since": &filter.Since, "until": &filter.Until} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_time", "时间格式无效")
				return
			}
			*target = &parsed
		}
	}
	if filter.Since != nil && filter.Until != nil && !filter.Since.Before(*filter.Until) {
		writeError(w, http.StatusBadRequest, "invalid_time", "开始时间必须早于结束时间")
		return
	}
	filter.StatusMin, filter.StatusMax = statusRange(r.URL.Query().Get("status"))
	if status := r.URL.Query().Get("status"); status == "success" || status == "error" {
		filter.Outcome = status
		filter.StatusMin, filter.StatusMax = 0, 0
	}
	if r.URL.Query().Get("pagination") == "cursor" || r.URL.Query().Has("cursor") {
		cursor, err := store.ParsePageCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, 400, "invalid_cursor", "分页位置无效，请返回首页刷新")
			return
		}
		filter.Cursor = cursor
		page, err := s.store.RequestLogPage(r.Context(), filter)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
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
	report, err := s.store.UsageReport(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(report)
}

func (s *Server) pricing(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.PricingProfiles(r.Context(), r.URL.Query().Get("summary") == "true")
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

func (s *Server) pricingProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	profile, err := s.store.PricingProfileByID(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}
