package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

const (
	maxProbeBody      = 1 << 20
	newAPIQuotaPerUSD = 500000.0
	unlimitedSentinel = 100000000.0
)

type Health struct {
	Status     string        `json:"status"`
	Models     []string      `json:"models,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
	CheckedAt  time.Time     `json:"checked_at"`
}

type Prober struct {
	Client  *http.Client
	Timeout time.Duration
}

func NewProber(client *http.Client, timeout time.Duration) *Prober {
	if client == nil {
		client = http.DefaultClient
	}
	client = withoutRedirects(client)
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Prober{Client: client, Timeout: timeout}
}

func withoutRedirects(client *http.Client) *http.Client {
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cloned
}

func (p *Prober) CheckHealth(ctx context.Context, upstream core.Upstream) Health {
	started := time.Now()
	result := Health{Status: "unhealthy", CheckedAt: started}
	body, status, err := p.get(ctx, upstream, "/v1/models", upstream.APIKey, nil)
	result.Latency = time.Since(started)
	result.StatusCode = status
	if err != nil {
		result.Error = err.Error()
		return result
	}
	models, err := parseModels(body)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = "healthy"
	result.Models = models
	return result
}

func (p *Prober) CheckBalance(ctx context.Context, upstream core.Upstream) core.Balance {
	now := time.Now()
	if strings.TrimSpace(upstream.BaseURL) == "" || strings.TrimSpace(upstream.APIKey) == "" {
		return core.Balance{Status: "unknown", Error: "missing upstream URL or API key", UpdatedAt: &now}
	}

	var failures []string
	var subscription *core.Balance
	try := func(path, credential string, headers map[string]string, parse func([]byte, time.Time) (core.Balance, error)) (core.Balance, bool) {
		body, status, err := p.get(ctx, upstream, path, credential, headers)
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			return core.Balance{}, false
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			return core.Balance{}, false
		}
		balance, err := parse(body, now)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			return core.Balance{}, false
		}
		return balance, true
	}

	if upstream.Kind == "sub2api" {
		if balance, ok := try("/v1/usage", upstream.APIKey, nil, parseSub2APIUsage); ok {
			return balance
		}
	}
	if balance, ok := try("/v1/dashboard/billing/subscription", upstream.APIKey, nil, parseSubscription); ok {
		subscription = &balance
	}
	if balance, ok := try("/api/usage/token/", upstream.APIKey, nil, parseTokenUsage); ok {
		return balance
	}

	credential := upstream.APIKey
	headers := map[string]string(nil)
	if upstream.AccessToken != "" && upstream.UserID != "" {
		credential = upstream.AccessToken
		headers = map[string]string{"New-Api-User": upstream.UserID}
	}
	if balance, ok := try("/api/user/self", credential, headers, parseUserSelf); ok {
		return balance
	}
	if upstream.Kind != "sub2api" {
		if balance, ok := try("/v1/usage", upstream.APIKey, nil, parseSub2APIUsage); ok {
			return balance
		}
	}
	if subscription != nil {
		return *subscription
	}
	if len(failures) == 0 {
		return core.Balance{Status: "unknown", Error: "balance API unsupported", UpdatedAt: &now}
	}
	return core.Balance{Status: "unavailable", Error: strings.Join(failures, "; "), UpdatedAt: &now}
}

func (p *Prober) get(ctx context.Context, upstream core.Upstream, endpoint, credential string, headers map[string]string) ([]byte, int, error) {
	target, err := endpointURL(upstream.BaseURL, endpoint)
	if err != nil {
		return nil, 0, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, errors.New("build request")
	}
	req.Header.Set("Accept", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	if err != nil {
		return nil, resp.StatusCode, errors.New("read response")
	}
	if len(body) > maxProbeBody {
		return nil, resp.StatusCode, errors.New("response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func endpointURL(base, endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("invalid upstream URL")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1") + endpoint
	return u.String(), nil
}

func parseModels(body []byte) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return nil, errors.New("invalid models response")
	}
	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]bool, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	return models, nil
}

func parseSubscription(body []byte, now time.Time) (core.Balance, error) {
	var payload struct {
		Hard *float64 `json:"hard_limit_usd"`
		Soft *float64 `json:"soft_limit_usd"`
		Plan string   `json:"plan"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return core.Balance{}, errors.New("invalid subscription response")
	}
	available := payload.Hard
	if available == nil {
		available = payload.Soft
	}
	if available == nil || *available < 0 {
		return core.Balance{}, errors.New("subscription balance missing")
	}
	balance := successBalance(now)
	balance.Currency, balance.Plan = "USD", payload.Plan
	if *available >= unlimitedSentinel {
		balance.Unlimited = true
	} else {
		balance.Available = available
	}
	return balance, nil
}

func parseTokenUsage(body []byte, now time.Time) (core.Balance, error) {
	var payload struct {
		Success *bool `json:"success"`
		Code    *int  `json:"code"`
		Data    *struct {
			Available *float64 `json:"total_available"`
			Used      *float64 `json:"total_used"`
			Unlimited bool     `json:"unlimited_quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return core.Balance{}, errors.New("invalid token usage response")
	}
	if (payload.Success != nil && !*payload.Success) || (payload.Code != nil && *payload.Code != 0) {
		return core.Balance{}, errors.New("token usage rejected")
	}
	if payload.Data.Used == nil || *payload.Data.Used < 0 || (!payload.Data.Unlimited && (payload.Data.Available == nil || *payload.Data.Available < 0)) {
		return core.Balance{}, errors.New("token usage balance missing")
	}
	balance := successBalance(now)
	balance.Currency, balance.Unlimited = "USD", payload.Data.Unlimited
	used := *payload.Data.Used / newAPIQuotaPerUSD
	balance.Used = &used
	if payload.Data.Available != nil {
		available := *payload.Data.Available / newAPIQuotaPerUSD
		balance.Available = &available
	}
	return balance, nil
}

func parseUserSelf(body []byte, now time.Time) (core.Balance, error) {
	var payload struct {
		Success *bool `json:"success"`
		Code    *int  `json:"code"`
		Data    *struct {
			Quota *float64 `json:"quota"`
			Used  *float64 `json:"used_quota"`
			Group string   `json:"group"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return core.Balance{}, errors.New("invalid user response")
	}
	if (payload.Success != nil && !*payload.Success) || (payload.Code != nil && *payload.Code != 0) {
		return core.Balance{}, errors.New("user balance rejected")
	}
	if payload.Data.Quota == nil || *payload.Data.Quota < 0 || (payload.Data.Used != nil && *payload.Data.Used < 0) {
		return core.Balance{}, errors.New("user balance missing")
	}
	balance := successBalance(now)
	balance.Currency, balance.Plan = "USD", payload.Data.Group
	available := *payload.Data.Quota / newAPIQuotaPerUSD
	balance.Available = &available
	if payload.Data.Used != nil {
		used := *payload.Data.Used / newAPIQuotaPerUSD
		balance.Used = &used
	}
	return balance, nil
}

func parseSub2APIUsage(body []byte, now time.Time) (core.Balance, error) {
	var payload struct {
		Remaining *float64 `json:"remaining"`
		Unit      string   `json:"unit"`
		Quota     *struct {
			Remaining *float64 `json:"remaining"`
			Used      *float64 `json:"used"`
			Unit      string   `json:"unit"`
		} `json:"quota"`
		Usage *struct {
			Total *struct {
				Cost       *float64 `json:"cost"`
				ActualCost *float64 `json:"actual_cost"`
			} `json:"total"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return core.Balance{}, errors.New("invalid usage response")
	}
	available, used, currency := payload.Remaining, (*float64)(nil), payload.Unit
	if payload.Quota != nil {
		available, used = payload.Quota.Remaining, payload.Quota.Used
		if payload.Quota.Unit != "" {
			currency = payload.Quota.Unit
		}
	} else if payload.Usage != nil && payload.Usage.Total != nil {
		used = payload.Usage.Total.ActualCost
		if used == nil {
			used = payload.Usage.Total.Cost
		}
	}
	if available == nil || *available < 0 || (used != nil && *used < 0) {
		return core.Balance{}, errors.New("usage balance missing")
	}
	if currency == "" {
		currency = "USD"
	}
	balance := successBalance(now)
	balance.Available, balance.Used, balance.Currency = available, used, currency
	return balance, nil
}

func successBalance(now time.Time) core.Balance {
	return core.Balance{Status: "ok", UpdatedAt: &now, LastSuccess: &now}
}
