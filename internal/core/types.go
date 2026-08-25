package core

import (
	"strings"
	"time"
)

const (
	ProtocolResponses = "responses"
	ProtocolMessages  = "messages"
	ProtocolChat      = "chat"
)

type Upstream struct {
	ID                 int64             `json:"id"`
	Name               string            `json:"name"`
	Kind               string            `json:"kind"`
	BaseURL            string            `json:"base_url"`
	APIKey             string            `json:"-"`
	AccessToken        string            `json:"-"`
	UserID             string            `json:"-"`
	Enabled            bool              `json:"enabled"`
	Priority           int               `json:"priority"`
	Protocols          []string          `json:"protocols"`
	Models             []string          `json:"models"`
	ModelsLocked       bool              `json:"models_locked"`
	ModelAliases       map[string]string `json:"model_aliases"`
	ConnectTimeout     time.Duration     `json:"-"`
	FirstByteTimeout   time.Duration     `json:"-"`
	IdleTimeout        time.Duration     `json:"-"`
	FailureThreshold   int               `json:"failure_threshold"`
	Cooldown           time.Duration     `json:"-"`
	HealthStatus       string            `json:"health_status"`
	ConsecutiveFailure int               `json:"consecutive_failures"`
	CircuitOpenUntil   *time.Time        `json:"circuit_open_until,omitempty"`
	LastCheckAt        *time.Time        `json:"last_check_at,omitempty"`
	LastError          string            `json:"last_error,omitempty"`
}

func (u Upstream) Supports(protocol, model string, now time.Time) bool {
	if !u.Enabled || (u.CircuitOpenUntil != nil && u.CircuitOpenUntil.After(now)) {
		return false
	}
	if !contains(u.Protocols, protocol) {
		return false
	}
	return len(u.Models) == 0 || contains(u.Models, model) || u.ModelAliases[model] != ""
}

func (u Upstream) UpstreamModel(model string) string {
	if mapped := strings.TrimSpace(u.ModelAliases[model]); mapped != "" {
		return mapped
	}
	return model
}

type APIKey struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Enabled   bool       `json:"enabled"`
	Protocols []string   `json:"protocols"`
	Models    []string   `json:"models"`
	LastUsed  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (k APIKey) Allows(protocol, model string) bool {
	return k.Enabled && (len(k.Protocols) == 0 || contains(k.Protocols, protocol)) && (len(k.Models) == 0 || contains(k.Models, model))
}

type Attempt struct {
	UpstreamID   int64  `json:"upstream_id"`
	UpstreamName string `json:"upstream_name"`
	StatusCode   int    `json:"status_code,omitempty"`
	Error        string `json:"error,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
}

type Usage struct {
	InputTokens       *int64 `json:"input_tokens,omitempty"`
	OutputTokens      *int64 `json:"output_tokens,omitempty"`
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
}

type RequestLog struct {
	RequestID  string    `json:"request_id"`
	APIKeyID   int64     `json:"api_key_id"`
	UpstreamID *int64    `json:"upstream_id,omitempty"`
	Protocol   string    `json:"protocol"`
	Model      string    `json:"model"`
	StatusCode int       `json:"status_code"`
	DurationMS int64     `json:"duration_ms"`
	Attempts   []Attempt `json:"attempts"`
	Usage      Usage     `json:"usage"`
	ErrorCode  string    `json:"error_code,omitempty"`
	ClientIP   string    `json:"client_ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Balance struct {
	Status      string     `json:"status"`
	Available   *float64   `json:"available,omitempty"`
	Used        *float64   `json:"used,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	Unlimited   bool       `json:"unlimited,omitempty"`
	Error       string     `json:"error,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	LastSuccess *time.Time `json:"last_success_at,omitempty"`
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
