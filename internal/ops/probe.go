package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/netguard"
)

const (
	maxProbeBody             = 1 << 20
	newAPIQuotaPerUSD        = 500000.0
	unlimitedSentinel        = 100000000.0
	newAPIProbePrompt        = "hi"
	probeInstructions        = "You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly."
	probeMaxTokens           = 16
	sub2APIPingTimeout       = 8 * time.Second
	sub2APIDegradedThreshold = 6 * time.Second
)

var probeNumberPattern = regexp.MustCompile(`-?\d+`)

type Health struct {
	Status     string        `json:"status"`
	Models     []string      `json:"models,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
	CheckedAt  time.Time     `json:"checked_at"`
}

type ModelProbe struct {
	Protocol      string `json:"protocol"`
	Status        string `json:"status"`
	StatusCode    int    `json:"status_code"`
	LatencyMS     int64  `json:"latency_ms"`
	PingLatencyMS int64  `json:"ping_latency_ms,omitempty"`
	Error         string `json:"error"`
}

type ModelTest struct {
	Model   string       `json:"model"`
	Status  string       `json:"status"`
	Results []ModelProbe `json:"results"`
}

type Prober struct {
	Client  *http.Client
	Timeout time.Duration
	secure  bool
}

func NewProber(client *http.Client, timeout time.Duration) *Prober {
	secure := client == nil
	if client == nil {
		client = netguard.NewHTTPClient(timeout)
	}
	client = withoutRedirects(client)
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Prober{Client: client, Timeout: timeout, secure: secure}
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

func (p *Prober) TestModel(ctx context.Context, upstream core.Upstream, model string) ModelTest {
	result := ModelTest{Model: strings.TrimSpace(model), Status: "unavailable", Results: []ModelProbe{}}
	challenge := modelProbeChallenge(upstream.Kind)
	var pingLatencyMS int64
	if upstream.Kind == "sub2api" {
		pingLatencyMS, _ = p.pingOrigin(ctx, upstream)
	}
	protocols := modelProbeProtocols(upstream, result.Model)
	succeeded := 0
	for _, protocol := range protocols {
		probe := p.testModelProtocol(ctx, upstream, result.Model, protocol, challenge)
		probe.PingLatencyMS = pingLatencyMS
		result.Results = append(result.Results, probe)
		if probe.Status == "success" || probe.Status == "degraded" {
			succeeded++
		}
		if ctx.Err() != nil {
			break
		}
	}
	if succeeded > 0 {
		result.Status = "partial"
		if succeeded == len(protocols) {
			result.Status = "available"
		}
	}
	return result
}

// modelProbeProtocols mirrors the channel testers' endpoint selection. The
// configured protocol list still limits what can be used, but one model test
// exercises the selected native endpoint rather than multiplying provider calls.
func modelProbeProtocols(upstream core.Upstream, model string) []string {
	preferred := core.ProtocolChat
	if upstream.Kind == "newapi" && strings.Contains(strings.ToLower(model), "codex") {
		preferred = core.ProtocolResponses
	}
	if hasProtocol(upstream.Protocols, preferred) {
		return []string{preferred}
	}
	for _, protocol := range []string{core.ProtocolResponses, core.ProtocolChat, core.ProtocolMessages} {
		if hasProtocol(upstream.Protocols, protocol) {
			return []string{protocol}
		}
	}
	return nil
}

func (p *Prober) testModelProtocol(ctx context.Context, upstream core.Upstream, model, protocol string, challenge probeChallenge) ModelProbe {
	started := time.Now()
	result := ModelProbe{Protocol: protocol, Status: "failed"}
	endpoint, payload, expected := modelProbeRequestWithChallenge(protocol, upstream.Kind, model, challenge)
	body, err := json.Marshal(payload)
	if err == nil {
		body, result.StatusCode, err = p.post(ctx, upstream, endpoint, body, protocol)
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	if err == nil {
		err = parseModelContent(protocol, body, expected)
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = "success"
	if upstream.Kind == "sub2api" && time.Since(started) >= sub2APIDegradedThreshold {
		result.Status = "degraded"
	}
	return result
}

func modelProbeRequest(protocol, kind, model string) (string, any, string) {
	return modelProbeRequestWithChallenge(protocol, kind, model, modelProbeChallenge(kind))
}

type probeChallenge struct {
	Prompt   string
	Expected string
}

func modelProbeChallenge(kind string) probeChallenge {
	if kind != "sub2api" {
		return probeChallenge{Prompt: newAPIProbePrompt}
	}
	left := rand.IntN(50) + 1
	right := rand.IntN(50) + 1
	operator := "+"
	answer := left + right
	if rand.IntN(2) == 1 {
		if right > left {
			left, right = right, left
		}
		operator = "-"
		answer = left - right
	}
	prompt := fmt.Sprintf("Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: %d %s %d = ?\nA:", left, operator, right)
	return probeChallenge{Prompt: prompt, Expected: strconv.Itoa(answer)}
}

func modelProbeRequestWithChallenge(protocol, kind, model string, challenge probeChallenge) (string, any, string) {
	message := []map[string]string{{"role": "user", "content": challenge.Prompt}}
	if protocol == core.ProtocolResponses {
		payload := map[string]any{
			"model": model, "max_output_tokens": probeMaxTokens, "stream": false,
		}
		if kind == "newapi" {
			// NewAPI's channel tester uses the Responses message-array shape.
			payload["input"] = message
		} else {
			// Sub2API's monitor accepts a string input and explicit instructions.
			payload["instructions"] = probeInstructions
			payload["input"] = challenge.Prompt
		}
		return "/v1/responses", payload, challenge.Expected
	}
	return "/v1/" + map[string]string{core.ProtocolChat: "chat/completions", core.ProtocolMessages: "messages"}[protocol], map[string]any{
		"model": model, "messages": message, "max_tokens": probeMaxTokens, "stream": false,
	}, challenge.Expected
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
	applyUserAgent(req, upstream)
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

func (p *Prober) post(ctx context.Context, upstream core.Upstream, endpoint string, body []byte, protocol string) ([]byte, int, error) {
	target, err := endpointURL(upstream.BaseURL, endpoint)
	if err != nil {
		return nil, 0, err
	}
	connectTimeout, firstByteTimeout, totalTimeout := p.modelTimeouts(upstream)
	requestCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("build request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	applyUserAgent(req, upstream)
	req.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	if protocol == core.ProtocolMessages {
		req.Header.Set("X-Api-Key", upstream.APIKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	client := *p.Client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		cloned := base.Clone()
		if p.secure {
			cloned.Proxy = nil
		}
		if p.secure {
			cloned.DialContext = (&netguard.Dialer{Timeout: connectTimeout}).DialContext
		} else {
			cloned.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
		}
		cloned.ResponseHeaderTimeout = firstByteTimeout
		client.Transport = cloned
		defer cloned.CloseIdleConnections()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	if err != nil {
		return nil, resp.StatusCode, errors.New("read response")
	}
	if len(responseBody) > maxProbeBody {
		return nil, resp.StatusCode, errors.New("response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, probeHTTPError(resp.StatusCode, responseBody)
	}
	return responseBody, resp.StatusCode, nil
}

// pingOrigin mirrors Sub2API's monitor preflight. A failed HEAD is informational
// only; the model request remains the source of truth for availability.
func (p *Prober) pingOrigin(ctx context.Context, upstream core.Upstream) (int64, bool) {
	u, err := url.Parse(strings.TrimSpace(upstream.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, false
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	requestCtx, cancel := context.WithTimeout(ctx, sub2APIPingTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodHead, u.String(), nil)
	if err != nil {
		return 0, false
	}
	applyUserAgent(request, upstream)
	started := time.Now()
	response, err := p.Client.Do(request)
	if err != nil {
		return 0, false
	}
	_ = response.Body.Close()
	return time.Since(started).Milliseconds(), true
}

func applyUserAgent(request *http.Request, upstream core.Upstream) {
	if upstream.UserAgent != "" {
		request.Header.Set("User-Agent", upstream.UserAgent)
	}
}

func (p *Prober) modelTimeouts(upstream core.Upstream) (time.Duration, time.Duration, time.Duration) {
	fallback := p.Timeout
	if fallback <= 0 {
		fallback = 10 * time.Second
	}
	connect, firstByte := upstream.ConnectTimeout, upstream.FirstByteTimeout
	if connect <= 0 {
		connect = fallback
	}
	if firstByte <= 0 {
		firstByte = fallback
	}
	total := connect + firstByte
	if total < fallback {
		total = fallback
	}
	return connect, firstByte, total
}

func probeHTTPError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if message := strings.TrimSpace(payload.Error.Message); message != "" {
			return errors.New(message)
		}
		if message := strings.TrimSpace(payload.Message); message != "" {
			return errors.New(message)
		}
	}
	return fmt.Errorf("HTTP %d", status)
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

func parseModelContent(protocol string, body []byte, expected string) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return probeHTTPError(http.StatusBadGateway, body)
	}
	texts := make([]string, 0, 2)
	switch protocol {
	case core.ProtocolChat:
		var payload struct {
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return errors.New("invalid response")
		}
		for _, choice := range payload.Choices {
			texts = append(texts, rawContentText(choice.Message.Content))
		}
	case core.ProtocolResponses:
		var payload struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return errors.New("invalid response")
		}
		texts = append(texts, payload.OutputText)
		for _, output := range payload.Output {
			for _, content := range output.Content {
				texts = append(texts, content.Text)
			}
		}
	case core.ProtocolMessages:
		var payload struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return errors.New("invalid response")
		}
		for _, content := range payload.Content {
			texts = append(texts, content.Text)
		}
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		if expected == "" || probeAnswerMatches(text, expected) {
			return nil
		}
	}
	if expected != "" {
		return errors.New("response content mismatch")
	}
	return errors.New("response content missing")
}

func rawContentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "")
}

func probeAnswerMatches(text, expected string) bool {
	if strings.TrimSpace(text) == "" || expected == "" {
		return false
	}
	for _, number := range probeNumberPattern.FindAllString(text, -1) {
		if number == expected {
			return true
		}
	}
	return false
}

func hasProtocol(protocols []string, target string) bool {
	for _, protocol := range protocols {
		if protocol == target {
			return true
		}
	}
	return false
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
