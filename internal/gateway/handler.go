package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/netguard"
)

const maxBodyBytes = 32 << 20

const (
	defaultMaxConcurrentRequests = 256
	defaultMaxConcurrentPerKey   = 32
	defaultMaxRequestsPerMinute  = 600
	defaultMaxRequestDuration    = 15 * time.Minute
)

var ErrInvalidAPIKey = errors.New("invalid API key")

var errClientClosed = errors.New("client connection closed")

type upstreamTimeout string

func (e upstreamTimeout) Error() string { return string(e) }
func (upstreamTimeout) Timeout() bool   { return true }
func (upstreamTimeout) Temporary() bool { return true }

type Repository interface {
	Authenticate(context.Context, string) (core.APIKey, error)
	Candidates(context.Context, int64, string, string) ([]core.Upstream, error)
	AvailableModels(context.Context, core.APIKey) ([]string, error)
	MaxAttempts(context.Context) (int, error)
	RecordRequest(context.Context, core.RequestLog) error
	MarkUpstreamSuccess(context.Context, int64) error
	MarkUpstreamFailure(context.Context, int64, int, string) error
}

type Handler struct {
	repo       Repository
	mux        *http.ServeMux
	mu         sync.Mutex
	transports map[transportKey]*http.Client
	limits     Limits
	gate       requestGate
	rate       requestRateLimiter
	secure     bool
}

// Limits bounds resource use by authenticated clients. Zero values use safe defaults.
type Limits struct {
	MaxConcurrentRequests int
	MaxConcurrentPerKey   int
	MaxRequestsPerMinute  int
	MaxRequestDuration    time.Duration
}

type requestGate struct {
	mu     sync.Mutex
	active int
	byKey  map[int64]int
}

func (g *requestGate) acquire(key int64, limits Limits) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byKey == nil {
		g.byKey = make(map[int64]int)
	}
	if g.active >= limits.MaxConcurrentRequests || g.byKey[key] >= limits.MaxConcurrentPerKey {
		return false
	}
	g.active++
	g.byKey[key]++
	return true
}

func (g *requestGate) release(key int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		g.active--
	}
	if count := g.byKey[key] - 1; count > 0 {
		g.byKey[key] = count
	} else {
		delete(g.byKey, key)
	}
}

type requestRate struct {
	window time.Time
	count  int
}

type requestRateLimiter struct {
	mu          sync.Mutex
	entries     map[int64]requestRate
	lastCleanup time.Time
}

func (l *requestRateLimiter) allow(key int64, max int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[int64]requestRate)
	}
	window := now.UTC().Truncate(time.Minute)
	// Remove stale keys occasionally so an attacker cannot grow this map by
	// creating many API keys. The scan is bounded to once per minute.
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for id, candidate := range l.entries {
			if candidate.window.Before(window) {
				delete(l.entries, id)
			}
		}
		l.lastCleanup = now
	}
	entry := l.entries[key]
	if !entry.window.Equal(window) {
		entry = requestRate{window: window}
	}
	if entry.count >= max {
		l.entries[key] = entry
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

type transportKey struct {
	connect, firstByte, idle time.Duration
}

func NewHandler(repo Repository, configured ...Limits) *Handler {
	return newHandler(repo, false, configured...)
}

// NewSecureHandler enables the outbound address boundary used by the
// production server. NewHandler remains useful for embedders that provide
// their own network boundary (and for in-process test servers).
func NewSecureHandler(repo Repository, configured ...Limits) *Handler {
	return newHandler(repo, true, configured...)
}

func newHandler(repo Repository, secure bool, configured ...Limits) *Handler {
	limits := Limits{MaxConcurrentRequests: defaultMaxConcurrentRequests, MaxConcurrentPerKey: defaultMaxConcurrentPerKey, MaxRequestsPerMinute: defaultMaxRequestsPerMinute, MaxRequestDuration: defaultMaxRequestDuration}
	if len(configured) > 0 {
		if configured[0].MaxConcurrentRequests > 0 {
			limits.MaxConcurrentRequests = configured[0].MaxConcurrentRequests
		}
		if configured[0].MaxConcurrentPerKey > 0 {
			limits.MaxConcurrentPerKey = configured[0].MaxConcurrentPerKey
		}
		if configured[0].MaxRequestsPerMinute > 0 {
			limits.MaxRequestsPerMinute = configured[0].MaxRequestsPerMinute
		}
		if configured[0].MaxRequestDuration > 0 {
			limits.MaxRequestDuration = configured[0].MaxRequestDuration
		}
	}
	if limits.MaxConcurrentRequests > 10000 {
		limits.MaxConcurrentRequests = 10000
	}
	if limits.MaxConcurrentPerKey > 1000 {
		limits.MaxConcurrentPerKey = 1000
	}
	if limits.MaxRequestsPerMinute > 100000 {
		limits.MaxRequestsPerMinute = 100000
	}
	if limits.MaxRequestDuration > 24*time.Hour {
		limits.MaxRequestDuration = 24 * time.Hour
	}
	h := &Handler{repo: repo, mux: http.NewServeMux(), transports: make(map[transportKey]*http.Client), limits: limits, secure: secure}
	h.mux.HandleFunc("POST /v1/responses", h.proxy(core.ProtocolResponses))
	h.mux.HandleFunc("POST /v1/messages", h.proxy(core.ProtocolMessages))
	h.mux.HandleFunc("POST /v1/chat/completions", h.proxy(core.ProtocolChat))
	h.mux.HandleFunc("GET /v1/models", h.models)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) proxy(protocol string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-DAPI-Request-ID", requestID)
		w.Header().Set("X-DAPI-Attempts", "0")

		key, ok := h.authenticate(w, r, protocol)
		if !ok {
			return
		}
		logEntry := core.RequestLog{
			RequestID: requestID, APIKeyID: key.ID, Protocol: protocol,
			ClientIP: clientIP(r), CreatedAt: started, Attempts: []core.Attempt{},
		}
		if key.GroupID > 0 {
			logEntry.GroupID = &key.GroupID
		}
		defer func() {
			logEntry.DurationMS = time.Since(started).Milliseconds()
			h.record(logEntry)
		}()
		if !h.gate.acquire(key.ID, h.limits) {
			logEntry.StatusCode = http.StatusTooManyRequests
			logEntry.ErrorCode = "concurrency_limited"
			w.Header().Set("Retry-After", "1")
			writeError(w, protocol, http.StatusTooManyRequests, "concurrency_limited", "too many concurrent requests")
			return
		}
		defer h.gate.release(key.ID)
		if !h.rate.allow(key.ID, h.limits.MaxRequestsPerMinute, time.Now()) {
			logEntry.StatusCode = http.StatusTooManyRequests
			logEntry.ErrorCode = "rate_limited"
			w.Header().Set("Retry-After", "60")
			writeError(w, protocol, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
			return
		}
		requestCtx, cancel := context.WithTimeout(r.Context(), h.limits.MaxRequestDuration)
		defer cancel()
		r = r.WithContext(requestCtx)

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			logEntry.StatusCode = http.StatusBadRequest
			logEntry.ErrorCode = "invalid_request"
			writeError(w, protocol, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Model) == "" {
			logEntry.StatusCode = http.StatusBadRequest
			logEntry.ErrorCode = "invalid_request"
			writeError(w, protocol, http.StatusBadRequest, "invalid_request", "model is required")
			return
		}
		payload.Model = strings.TrimSpace(payload.Model)
		logEntry.Model = payload.Model
		if !key.Allows(protocol, payload.Model) {
			logEntry.StatusCode = http.StatusForbidden
			logEntry.ErrorCode = "permission_denied"
			writeError(w, protocol, http.StatusForbidden, "permission_denied", "API key cannot use this protocol or model")
			return
		}

		maxAttempts, err := h.repo.MaxAttempts(r.Context())
		if err != nil {
			logEntry.StatusCode = http.StatusInternalServerError
			logEntry.ErrorCode = "internal_error"
			writeError(w, protocol, http.StatusInternalServerError, "internal_error", "gateway configuration unavailable")
			return
		}
		if maxAttempts < 1 {
			maxAttempts = 1
		} else if maxAttempts > 5 {
			maxAttempts = 5
		}
		candidates, err := h.repo.Candidates(r.Context(), key.GroupID, protocol, payload.Model)
		if err != nil {
			logEntry.StatusCode = http.StatusInternalServerError
			logEntry.ErrorCode = "internal_error"
			writeError(w, protocol, http.StatusInternalServerError, "internal_error", "upstream routes unavailable")
			return
		}
		now := time.Now()
		candidates = append([]core.Upstream(nil), candidates...)
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Priority < candidates[j].Priority })
		eligible := candidates[:0]
		for _, upstream := range candidates {
			if upstream.Supports(protocol, payload.Model, now) {
				eligible = append(eligible, upstream)
			}
		}
		if len(eligible) > maxAttempts {
			eligible = eligible[:maxAttempts]
		}

		allRateLimited, allTimedOut := len(eligible) > 0, len(eligible) > 0
		for _, upstream := range eligible {
			attemptStarted := time.Now()
			attempt := core.Attempt{UpstreamID: upstream.ID, UpstreamName: upstream.Name}
			outBody, err := replaceModel(body, upstream.UpstreamModel(payload.Model))
			if err == nil {
				var outReq *http.Request
				outReq, err = upstreamRequest(r, upstream, outBody, protocol)
				if err == nil {
					var response *http.Response
					response, err = h.client(upstream).Do(outReq)
					if err == nil {
						attempt.StatusCode = response.StatusCode
						if retryStatus(response.StatusCode) {
							attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
							attempt.Error = http.StatusText(response.StatusCode)
							logEntry.Attempts = append(logEntry.Attempts, attempt)
							allRateLimited = allRateLimited && response.StatusCode == http.StatusTooManyRequests
							allTimedOut = allTimedOut && response.StatusCode == http.StatusGatewayTimeout
							if countsAsUpstreamFailure(response.StatusCode) {
								h.markFailure(upstream.ID, response.StatusCode, attempt.Error)
							}
							drainAndClose(response.Body)
							continue
						}
						if payload.Stream {
							committed, streamUsage, ttfbMS, ttftMS, streamErr := h.relayStreamWithMetrics(r.Context(), w, response, requestID, upstream.Name, len(logEntry.Attempts)+1, upstream.FirstByteTimeout, upstream.IdleTimeout, protocol, attemptStarted)
							attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
							attempt.TTFBMS, attempt.TTFTMS = ttfbMS, ttftMS
							if streamErr != nil {
								attempt.Error = streamErr.Error()
							}
							logEntry.Attempts = append(logEntry.Attempts, attempt)
							if committed {
								logEntry.UpstreamID = int64ptr(upstream.ID)
								logEntry.StatusCode = response.StatusCode
								logEntry.Usage = streamUsage
								logEntry.TTFBMS, logEntry.TTFTMS = ttfbMS, ttftMS
								if streamErr != nil && r.Context().Err() == nil && !errors.Is(streamErr, errClientClosed) {
									logEntry.ErrorCode = "stream_interrupted"
									h.markFailure(upstream.ID, response.StatusCode, streamErr.Error())
								} else if streamErr != nil {
									logEntry.ErrorCode = "client_closed"
								} else if streamErr == nil {
									h.markSuccess(upstream.ID)
								}
								return
							}
							if r.Context().Err() != nil || errors.Is(streamErr, context.Canceled) {
								if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
									logEntry.StatusCode = http.StatusGatewayTimeout
									logEntry.ErrorCode = "request_timeout"
								} else {
									logEntry.StatusCode = 499
									logEntry.ErrorCode = "client_closed"
								}
								return
							}
							h.markFailure(upstream.ID, 0, streamErr.Error())
							allRateLimited = false
							allTimedOut = allTimedOut && isTimeout(streamErr)
							continue
						}

						responseBody, ttfbMS, readErr := readResponseWithMetrics(r.Context(), response.Body, upstream.FirstByteTimeout, upstream.IdleTimeout, attemptStarted)
						response.Body.Close()
						if readErr == nil {
							attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
							attempt.TTFBMS = ttfbMS
							logEntry.Attempts = append(logEntry.Attempts, attempt)
							logEntry.UpstreamID = int64ptr(upstream.ID)
							logEntry.StatusCode = response.StatusCode
							logEntry.TTFBMS = ttfbMS
							logEntry.Usage = parseUsageWithProtocol(responseBody, protocol)
							h.markSuccess(upstream.ID)
							copyResponseHeaders(w.Header(), response.Header)
							setGatewayHeaders(w.Header(), requestID, upstream.Name, len(logEntry.Attempts))
							w.WriteHeader(response.StatusCode)
							_, _ = w.Write(responseBody)
							return
						}
						attempt.TTFBMS = ttfbMS
						err = readErr
					}
				}
			}

			attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
			attempt.Error = errorText(err)
			logEntry.Attempts = append(logEntry.Attempts, attempt)
			allRateLimited = false
			allTimedOut = allTimedOut && isTimeout(err)
			if r.Context().Err() != nil {
				logEntry.StatusCode = 499
				logEntry.ErrorCode = "client_closed"
				return
			}
			h.markFailure(upstream.ID, 0, attempt.Error)
		}

		status, code, message := http.StatusBadGateway, "bad_gateway", "all upstreams failed"
		if allRateLimited {
			status, code, message = http.StatusTooManyRequests, "rate_limit_exceeded", "all upstreams are rate limited"
		} else if allTimedOut {
			status, code, message = http.StatusGatewayTimeout, "gateway_timeout", "all upstreams timed out"
		}
		if len(logEntry.Attempts) > 0 {
			setGatewayHeaders(w.Header(), requestID, logEntry.Attempts[len(logEntry.Attempts)-1].UpstreamName, len(logEntry.Attempts))
		}
		logEntry.StatusCode = status
		logEntry.ErrorCode = code
		writeError(w, protocol, status, code, message)
	}
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("X-DAPI-Request-ID", requestID)
	w.Header().Set("X-DAPI-Attempts", "0")
	key, ok := h.authenticate(w, r, core.ProtocolChat)
	if !ok {
		return
	}
	if !h.gate.acquire(key.ID, h.limits) {
		w.Header().Set("Retry-After", "1")
		writeError(w, core.ProtocolChat, http.StatusTooManyRequests, "concurrency_limited", "too many concurrent requests")
		return
	}
	defer h.gate.release(key.ID)
	if !h.rate.allow(key.ID, h.limits.MaxRequestsPerMinute, time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, core.ProtocolChat, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
		return
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), h.limits.MaxRequestDuration)
	defer cancel()
	r = r.WithContext(requestCtx)
	models, err := h.repo.AvailableModels(r.Context(), key)
	if err != nil {
		writeError(w, core.ProtocolChat, http.StatusInternalServerError, "internal_error", "model list unavailable")
		return
	}
	allowed := make(map[string]bool, len(key.Models))
	for _, model := range key.Models {
		allowed[model] = true
	}
	seen := make(map[string]bool, len(models))
	filtered := models[:0]
	for _, model := range models {
		if model != "" && !seen[model] && (len(allowed) == 0 || allowed[model]) {
			seen[model] = true
			filtered = append(filtered, model)
		}
	}
	sort.Strings(filtered)
	data := make([]map[string]any, 0, len(filtered))
	for _, model := range filtered {
		data = append(data, map[string]any{"id": model, "object": "model", "created": 0, "owned_by": "dapi"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, protocol string) (core.APIKey, bool) {
	token := ""
	if protocol == core.ProtocolMessages {
		token = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if token == "" {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token = parts[1]
		}
	}
	if token == "" {
		writeError(w, protocol, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
		return core.APIKey{}, false
	}
	key, err := h.repo.Authenticate(r.Context(), token)
	if err != nil {
		if errors.Is(err, ErrInvalidAPIKey) {
			writeError(w, protocol, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		} else {
			writeError(w, protocol, http.StatusInternalServerError, "internal_error", "authentication unavailable")
		}
		return core.APIKey{}, false
	}
	if !key.Enabled {
		writeError(w, protocol, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		return core.APIKey{}, false
	}
	return key, true
}

func (h *Handler) client(upstream core.Upstream) *http.Client {
	key := transportKey{upstream.ConnectTimeout, upstream.FirstByteTimeout, upstream.IdleTimeout}
	if key.connect <= 0 {
		key.connect = 5 * time.Second
	}
	if key.firstByte <= 0 {
		key.firstByte = 60 * time.Second
	}
	if key.idle <= 0 {
		key.idle = 90 * time.Second
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if client := h.transports[key]; client != nil {
		return client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if h.secure {
		transport.Proxy = nil
		transport.DialContext = (&netguard.Dialer{Timeout: key.connect}).DialContext
	} else {
		transport.DialContext = (&net.Dialer{Timeout: key.connect, KeepAlive: 30 * time.Second}).DialContext
	}
	transport.ResponseHeaderTimeout = key.firstByte
	transport.IdleConnTimeout = key.idle
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 100
	transport.MaxConnsPerHost = 128
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	h.transports[key] = client
	return client
}

func upstreamRequest(in *http.Request, upstream core.Upstream, body []byte, protocol string) (*http.Request, error) {
	target, err := url.Parse(strings.TrimSpace(upstream.BaseURL))
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("invalid upstream URL")
	}
	path := in.URL.Path
	if strings.HasSuffix(strings.TrimRight(target.Path, "/"), "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(path, "/")
	target.RawPath = ""
	target.RawQuery = in.URL.RawQuery
	out, err := http.NewRequestWithContext(in.Context(), in.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out.Header = filteredRequestHeaders(in.Header)
	stripHopHeaders(out.Header)
	out.Header.Del("Content-Length")
	out.Header.Del("X-Api-Key")
	out.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	if protocol == core.ProtocolMessages {
		out.Header.Set("X-Api-Key", upstream.APIKey)
	}
	return out, nil
}

func replaceModel(body []byte, model string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	payload["model"] = encoded
	return json.Marshal(payload)
}

func (h *Handler) relayStream(ctx context.Context, w http.ResponseWriter, response *http.Response, requestID, upstreamName string, attempts int, firstByteTimeout, idleTimeout time.Duration) (bool, core.Usage, error) {
	committed, usage, _, _, err := h.relayStreamWithMetrics(ctx, w, response, requestID, upstreamName, attempts, firstByteTimeout, idleTimeout, "", time.Now())
	return committed, usage, err
}

func (h *Handler) relayStreamWithMetrics(ctx context.Context, w http.ResponseWriter, response *http.Response, requestID, upstreamName string, attempts int, firstByteTimeout, idleTimeout time.Duration, protocol string, attemptStarted time.Time) (bool, core.Usage, *int64, *int64, error) {
	defer response.Body.Close()
	if firstByteTimeout <= 0 {
		firstByteTimeout = 60 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	type readResult struct {
		n   int
		err error
	}
	buffer := make([]byte, 32<<10)
	results, acknowledge, done := make(chan readResult), make(chan struct{}), make(chan struct{})
	defer close(done)
	go func() {
		for {
			n, err := response.Body.Read(buffer)
			select {
			case results <- readResult{n: n, err: err}:
			case <-done:
				return
			}
			if n > 0 {
				select {
				case <-acknowledge:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	committed := false
	parser := sseUsageParser{protocol: protocol}
	var ttfbMS, ttftMS *int64
	timeout := firstByteTimeout
	for {
		timer := time.NewTimer(timeout)
		select {
		case result := <-results:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if result.n > 0 {
				if ttfbMS == nil {
					value := time.Since(attemptStarted).Milliseconds()
					ttfbMS = &value
				}
				if !committed {
					copyResponseHeaders(w.Header(), response.Header)
					setGatewayHeaders(w.Header(), requestID, upstreamName, attempts)
					w.Header().Del("Content-Length")
					w.WriteHeader(response.StatusCode)
					committed = true
				}
				parser.Feed(buffer[:result.n])
				if ttftMS == nil && parser.HasText() {
					value := time.Since(attemptStarted).Milliseconds()
					ttftMS = &value
				}
				if _, err := w.Write(buffer[:result.n]); err != nil {
					acknowledge <- struct{}{}
					return true, parser.Usage(), ttfbMS, ttftMS, fmt.Errorf("%w: %v", errClientClosed, err)
				}
				acknowledge <- struct{}{}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return true, parser.Usage(), ttfbMS, ttftMS, fmt.Errorf("%w: %v", errClientClosed, err)
				}
				timeout = idleTimeout
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					if !committed {
						copyResponseHeaders(w.Header(), response.Header)
						setGatewayHeaders(w.Header(), requestID, upstreamName, attempts)
						w.WriteHeader(response.StatusCode)
						committed = true
					}
					return committed, parser.Usage(), ttfbMS, ttftMS, nil
				}
				return committed, parser.Usage(), ttfbMS, ttftMS, result.err
			}
		case <-timer.C:
			_ = response.Body.Close()
			if committed {
				return true, parser.Usage(), ttfbMS, ttftMS, upstreamTimeout("upstream stream idle timeout")
			}
			return false, parser.Usage(), ttfbMS, ttftMS, upstreamTimeout("upstream first byte timeout")
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = response.Body.Close()
			return committed, parser.Usage(), ttfbMS, ttftMS, ctx.Err()
		}
	}
}

func readResponse(ctx context.Context, body io.ReadCloser, firstByteTimeout, idleTimeout time.Duration) ([]byte, error) {
	content, _, err := readResponseWithMetrics(ctx, body, firstByteTimeout, idleTimeout, time.Now())
	return content, err
}

func readResponseWithMetrics(ctx context.Context, body io.ReadCloser, firstByteTimeout, idleTimeout time.Duration, started time.Time) ([]byte, *int64, error) {
	if firstByteTimeout <= 0 {
		firstByteTimeout = 60 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	type readResult struct {
		content []byte
		err     error
	}
	results, done := make(chan readResult), make(chan struct{})
	defer close(done)
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := body.Read(buffer)
			content := append([]byte(nil), buffer[:n]...)
			select {
			case results <- readResult{content: content, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	content := make([]byte, 0)
	var ttfbMS *int64
	timeout := firstByteTimeout
	for {
		timer := time.NewTimer(timeout)
		select {
		case result := <-results:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if len(result.content) > 0 {
				if ttfbMS == nil {
					value := time.Since(started).Milliseconds()
					ttfbMS = &value
				}
				if len(content)+len(result.content) > maxBodyBytes {
					_ = body.Close()
					return nil, ttfbMS, errors.New("upstream response exceeds 32 MiB")
				}
				content = append(content, result.content...)
				timeout = idleTimeout
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return content, ttfbMS, nil
				}
				return nil, ttfbMS, result.err
			}
		case <-timer.C:
			_ = body.Close()
			if len(content) == 0 {
				return nil, ttfbMS, upstreamTimeout("upstream first byte timeout")
			}
			return nil, ttfbMS, upstreamTimeout("upstream response idle timeout")
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = body.Close()
			return nil, ttfbMS, ctx.Err()
		}
	}
}

func parseUsage(body []byte) core.Usage {
	return parseUsageWithProtocol(body, "")
}

func parseUsageWithProtocol(body []byte, protocol string) core.Usage {
	var payload struct {
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return core.Usage{}
	}
	if len(payload.Usage) == 0 {
		payload.Usage = payload.Response.Usage
	}
	if len(payload.Usage) == 0 {
		payload.Usage = payload.Message.Usage
	}
	if len(payload.Usage) == 0 {
		return core.Usage{}
	}
	return parseUsageObjectForProtocol(payload.Usage, protocol)
}

type usageFields struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	Prompt        *int64 `json:"prompt_tokens"`
	Completion    *int64 `json:"completion_tokens"`
	Cached        *int64 `json:"cached_input_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	InputDetails  struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptDetails struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func parseUsageObject(body []byte) core.Usage {
	return parseUsageObjectForProtocol(body, "")
}

func parseUsageObjectForProtocol(body []byte, protocol string) core.Usage {
	var fields usageFields
	if json.Unmarshal(body, &fields) != nil {
		return core.Usage{}
	}
	usage := core.Usage{InputTokens: fields.Input, OutputTokens: fields.Output, CachedInputTokens: fields.Cached, CacheCreationInputTokens: fields.CacheCreation}
	if usage.InputTokens == nil {
		usage.InputTokens = fields.Prompt
	}
	if usage.OutputTokens == nil {
		usage.OutputTokens = fields.Completion
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.CacheRead
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.InputDetails.Cached
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.PromptDetails.Cached
	}
	return normalizeUsageForProtocol(usage, protocol)
}

func normalizeUsage(usage core.Usage) core.Usage {
	return normalizeUsageForProtocol(usage, "")
}

func normalizeUsageForProtocol(usage core.Usage, protocol string) core.Usage {
	if usage.InputTokens != nil {
		uncached := *usage.InputTokens
		if protocol == core.ProtocolMessages {
			if usage.CacheCreationInputTokens != nil {
				uncached += *usage.CacheCreationInputTokens
			}
		} else if usage.CachedInputTokens != nil {
			uncached -= *usage.CachedInputTokens
		}
		if uncached < 0 {
			uncached = 0
		}
		usage.UncachedInputTokens = &uncached
	}
	return usage
}

type sseUsageParser struct {
	pending  []byte
	discard  bool
	usage    core.Usage
	protocol string
	textSeen bool
}

func (p *sseUsageParser) Feed(content []byte) {
	for len(content) > 0 {
		newline := bytes.IndexByte(content, '\n')
		if newline < 0 {
			p.append(content)
			return
		}
		p.append(content[:newline])
		if !p.discard {
			p.parseLine(p.pending)
		}
		p.pending = p.pending[:0]
		p.discard = false
		content = content[newline+1:]
	}
}

func (p *sseUsageParser) append(content []byte) {
	if p.discard {
		return
	}
	if len(p.pending)+len(content) > 1<<20 {
		p.pending = p.pending[:0]
		p.discard = true
		return
	}
	p.pending = append(p.pending, content...)
}

func (p *sseUsageParser) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var event struct {
		Type     string          `json:"type"`
		Delta    json.RawMessage `json:"delta"`
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
		Choices []struct {
			Text  string `json:"text"`
			Delta struct {
				Content json.RawMessage `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	for _, raw := range []json.RawMessage{event.Usage, event.Response.Usage, event.Message.Usage} {
		if len(raw) > 0 {
			p.merge(parseUsageObjectForProtocol(raw, p.protocol))
		}
	}
	if eventHasText(event.Type, event.Delta, event.Choices, p.protocol) {
		p.textSeen = true
	}
}

func eventHasText(eventType string, delta json.RawMessage, choices []struct {
	Text  string `json:"text"`
	Delta struct {
		Content json.RawMessage `json:"content"`
	} `json:"delta"`
}, protocol string) bool {
	switch protocol {
	case core.ProtocolResponses:
		return eventType == "response.output_text.delta" && rawString(delta) != ""
	case core.ProtocolMessages:
		if eventType != "content_block_delta" {
			return false
		}
		var value struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		return json.Unmarshal(delta, &value) == nil && value.Type == "text_delta" && value.Text != ""
	case core.ProtocolChat:
		for _, choice := range choices {
			if rawHasText(choice.Delta.Content) || choice.Text != "" {
				return true
			}
		}
	}
	return false
}

func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rawHasText(raw json.RawMessage) bool {
	if rawString(raw) != "" {
		return true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if (part.Type == "text" || part.Type == "output_text" || part.Type == "") && part.Text != "" {
			return true
		}
	}
	return false
}

func (p *sseUsageParser) merge(usage core.Usage) {
	if usage.InputTokens != nil {
		p.usage.InputTokens = usage.InputTokens
	}
	if usage.OutputTokens != nil {
		p.usage.OutputTokens = usage.OutputTokens
	}
	if usage.CachedInputTokens != nil {
		p.usage.CachedInputTokens = usage.CachedInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		p.usage.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if usage.UncachedInputTokens != nil {
		p.usage.UncachedInputTokens = usage.UncachedInputTokens
	}
}

func (p *sseUsageParser) Usage() core.Usage {
	if !p.discard && len(p.pending) > 0 {
		p.parseLine(p.pending)
	}
	p.pending = nil
	return normalizeUsageForProtocol(p.usage, p.protocol)
}

func (p *sseUsageParser) HasText() bool { return p.textSeen }

func retryStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusTooManyRequests || status >= 500
}

func countsAsUpstreamFailure(status int) bool {
	return status == http.StatusUnauthorized || status >= 500
}

func writeError(w http.ResponseWriter, protocol string, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if protocol == core.ProtocolMessages {
		typeName := "api_error"
		if status == http.StatusTooManyRequests {
			typeName = "rate_limit_error"
		} else if status == http.StatusUnauthorized {
			typeName = "authentication_error"
		} else if status == http.StatusForbidden {
			typeName = "permission_error"
		} else if status == http.StatusBadRequest {
			typeName = "invalid_request_error"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": typeName, "message": message}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": code, "code": code}})
}

func setGatewayHeaders(header http.Header, requestID, upstream string, attempts int) {
	header.Set("X-DAPI-Request-ID", requestID)
	header.Set("X-DAPI-Upstream", upstream)
	header.Set("X-DAPI-Attempts", strconv.Itoa(attempts))
}

func copyResponseHeaders(destination, source http.Header) {
	for key := range destination {
		if preservedGatewayHeader(key) {
			continue
		}
		destination.Del(key)
	}
	for key, values := range source {
		if blockedResponseHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
	stripHopHeaders(destination)
}

func preservedGatewayHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "X-Dapi-Request-Id", "X-Dapi-Upstream", "X-Dapi-Attempts",
		"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options",
		"Referrer-Policy", "Permissions-Policy", "Cross-Origin-Opener-Policy":
		return true
	default:
		return false
	}
}

func blockedResponseHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	if strings.HasPrefix(canonical, "Access-Control-") {
		return true
	}
	switch canonical {
	case "Set-Cookie", "Set-Cookie2", "Location", "Content-Security-Policy",
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy", "Cross-Origin-Opener-Policy", "Strict-Transport-Security":
		return true
	default:
		return false
	}
}

func stripHopHeaders(header http.Header) {
	for _, name := range strings.Split(header.Get("Connection"), ",") {
		header.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}

func filteredRequestHeaders(source http.Header) http.Header {
	destination := make(http.Header)
	for _, name := range []string{
		"Accept", "Accept-Encoding", "Cache-Control", "Content-Type", "Pragma", "User-Agent",
		"X-Request-ID", "OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta",
		"Anthropic-Version", "Anthropic-Beta",
	} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
	return destination
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func errorText(err error) string {
	if err == nil {
		return "upstream request failed"
	}
	return err.Error()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func int64ptr(value int64) *int64 { return &value }

func (h *Handler) record(entry core.RequestLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.repo.RecordRequest(ctx, entry); err != nil {
		slog.Error("request log write failed", "request_id", entry.RequestID, "error", err)
	}
}

func (h *Handler) markSuccess(upstreamID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.repo.MarkUpstreamSuccess(ctx, upstreamID); err != nil {
		slog.Error("upstream success state write failed", "upstream_id", upstreamID, "error", err)
	}
}

func (h *Handler) markFailure(upstreamID int64, status int, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.repo.MarkUpstreamFailure(ctx, upstreamID, status, reason); err != nil {
		slog.Error("upstream failure state write failed", "upstream_id", upstreamID, "status", status, "error", err)
	}
}
