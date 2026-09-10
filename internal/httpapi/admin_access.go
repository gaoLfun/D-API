package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/store"
)

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
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	existing, err := s.store.APIKey(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if (enabled || input.GroupID != existing.GroupID) && !s.requireAvailableGroup(w, r, input.GroupID) {
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

func validKeyPayload(input keyPayload) bool {
	name := strings.TrimSpace(input.Name)
	return name != "" && len([]rune(name)) <= 200 && validProtocols(input.Protocols) && validStringList(input.Models, 1000, 200)
}
