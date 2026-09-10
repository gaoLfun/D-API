package gateway

import (
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/safeerr"
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
	MarkUpstreamSuccess(context.Context, core.Upstream) error
	MarkUpstreamFailure(context.Context, core.Upstream, int, string) error
}

type batchRepository interface {
	RecordRequests(context.Context, []core.RequestLog) error
}

type Handler struct {
	repo           Repository
	mux            *http.ServeMux
	mu             sync.Mutex
	transports     map[transportKey]*transportEntry
	limits         Limits
	gate           requestGate
	authSlots      chan struct{}
	bodyBudget     byteBudget
	responseBudget byteBudget
	rate           requestRateLimiter
	secure         bool
	recorder       *requestRecorder
	healthMu       sync.Mutex
	health         map[int64]*upstreamHealthState
}

type upstreamHealthState struct {
	lock           chan struct{}
	references     int
	pendingFailure bool
}

// Limits bounds resource use by authenticated clients. Zero values use safe defaults.
type Limits struct {
	MaxConcurrentRequests    int
	MaxConcurrentPerKey      int
	MaxRequestsPerMinute     int
	MaxRequestDuration       time.Duration
	DownstreamWriteTimeout   time.Duration
	MaxBufferedRequestBytes  int64
	MaxBufferedResponseBytes int64
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
	limits.DownstreamWriteTimeout = 30 * time.Second
	limits.MaxBufferedRequestBytes = 512 << 20
	limits.MaxBufferedResponseBytes = 512 << 20
	if len(configured) > 0 {
		if configured[0].MaxBufferedResponseBytes > 0 {
			limits.MaxBufferedResponseBytes = configured[0].MaxBufferedResponseBytes
		}
		if configured[0].DownstreamWriteTimeout > 0 {
			limits.DownstreamWriteTimeout = configured[0].DownstreamWriteTimeout
		}
		if configured[0].MaxBufferedRequestBytes > 0 {
			limits.MaxBufferedRequestBytes = configured[0].MaxBufferedRequestBytes
		}
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
	h := &Handler{
		repo: repo, mux: http.NewServeMux(), transports: make(map[transportKey]*transportEntry),
		limits: limits, secure: secure, health: make(map[int64]*upstreamHealthState),
	}
	h.authSlots = make(chan struct{}, limits.MaxConcurrentRequests)
	if secure {
		h.recorder = newRequestRecorder(repo)
	}
	h.mux.HandleFunc("POST /v1/responses", h.proxy(core.ProtocolResponses))
	h.mux.HandleFunc("POST /v1/messages", h.proxy(core.ProtocolMessages))
	h.mux.HandleFunc("POST /v1/chat/completions", h.proxy(core.ProtocolChat))
	h.mux.HandleFunc("GET /v1/models", h.models)
	return h
}

// Close flushes queued request logs. Call it after the HTTP server has stopped
// accepting requests and before closing the repository.
func (h *Handler) Close(ctx context.Context) error {
	h.closeTransports()
	if h.recorder == nil {
		return nil
	}
	return h.recorder.Close(ctx)
}

func (h *Handler) DroppedRequestLogs() uint64 {
	if h.recorder == nil {
		return 0
	}
	return h.recorder.Dropped()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.limits.MaxRequestDuration)
	defer cancel()
	h.mux.ServeHTTP(w, r.WithContext(ctx))
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
		controller := http.NewResponseController(w)
		if deadline, ok := requestCtx.Deadline(); ok {
			// Preserve the server's 30-second body-read limit as well as total duration.
			if readLimit := time.Now().Add(30 * time.Second); readLimit.Before(deadline) {
				deadline = readLimit
			}
			_ = controller.SetReadDeadline(deadline)
		}

		requestEnded := func() bool {
			if requestCtx.Err() == nil {
				return false
			}
			logEntry.StatusCode, logEntry.ErrorCode = 499, "client_closed"
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				logEntry.StatusCode, logEntry.ErrorCode = http.StatusGatewayTimeout, "request_timeout"
				writeError(w, protocol, logEntry.StatusCode, logEntry.ErrorCode, "gateway request timed out")
			}
			return true
		}

		bodySize := r.ContentLength
		if bodySize <= 0 || bodySize > maxBodyBytes {
			bodySize = maxBodyBytes
		}

		// Reserve for the body, decoding and up to five model rewrites.
		reservation := max(bodySize, 1) * 8
		if !h.bodyBudget.acquire(reservation, h.limits.MaxBufferedRequestBytes) {
			// HTTP/1 otherwise drains an unread body before sending the response.
			// Do not wait for a rejected client's body or leave a drain unbounded.
			if r.ProtoMajor == 1 {
				w.Header().Set("Connection", "close")
				_ = controller.SetReadDeadline(time.Now())
			}
			logEntry.StatusCode, logEntry.ErrorCode = 429, "body_budget_exceeded"
			w.Header().Set("Retry-After", "1")
			writeError(w, protocol, 429, logEntry.ErrorCode, "request body memory budget exhausted")
			return
		}
		defer h.bodyBudget.release(reservation)
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			logEntry.StatusCode = http.StatusBadRequest
			logEntry.ErrorCode = "invalid_request"
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || isTimeout(err) {
				logEntry.StatusCode, logEntry.ErrorCode = 504, "request_timeout"
			}
			writeError(w, protocol, logEntry.StatusCode, logEntry.ErrorCode, "invalid or timed out request body")
			return
		}
		_ = controller.SetReadDeadline(time.Time{})
		payload, err := parseRequestPayload(body)
		if err != nil || strings.TrimSpace(payload.Model) == "" || strings.ContainsRune(payload.Model, '\x00') {
			logEntry.StatusCode = http.StatusBadRequest
			logEntry.ErrorCode = "invalid_request"
			writeError(w, protocol, http.StatusBadRequest, "invalid_request", "model is required")
			return
		}
		originalModel := payload.Model
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
			if requestEnded() {
				return
			}
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
			if requestEnded() {
				return
			}
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
		bodies := requestBodies{body: body, model: originalModel}
		for _, upstream := range eligible {
			if requestEnded() {
				return
			}
			attemptStarted := time.Now()
			attempt := core.Attempt{UpstreamID: upstream.ID, UpstreamName: upstream.Name}
			outBody, err := bodies.forModel(upstream.UpstreamModel(payload.Model))
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
								h.markFailure(r.Context(), upstream.ID, response.StatusCode, attempt.Error, upstream.ConfigVersion)
							}
							drainAndClose(r.Context(), response.Body)
							continue
						}
						if payload.Stream {
							committed, streamUsage, ttfbMS, ttftMS, streamErr := h.relayStreamWithMetrics(r.Context(), w, response, requestID, upstream.Name, len(logEntry.Attempts)+1, upstream.FirstByteTimeout, upstream.IdleTimeout, protocol, attemptStarted)
							attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
							attempt.TTFBMS, attempt.TTFTMS = ttfbMS, ttftMS
							if streamErr != nil {
								attempt.Error = safeerr.Text(streamErr, upstream.APIKey)
								attempt.FailureClass = failureClass(r.Context(), streamErr)
							}
							logEntry.Attempts = append(logEntry.Attempts, attempt)
							if committed {
								logEntry.UpstreamID = int64ptr(upstream.ID)
								logEntry.StatusCode = response.StatusCode
								logEntry.Usage = streamUsage
								logEntry.TTFBMS, logEntry.TTFTMS = ttfbMS, ttftMS
								if streamErr != nil && r.Context().Err() == nil && !errors.Is(streamErr, errClientClosed) {
									logEntry.ErrorCode = "stream_interrupted"
									h.markFailure(r.Context(), upstream.ID, response.StatusCode, safeerr.Text(streamErr, upstream.APIKey), upstream.ConfigVersion)
								} else if streamErr != nil {
									logEntry.ErrorCode = "client_closed"
									if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
										logEntry.ErrorCode = "request_timeout"
									}
								} else if streamErr == nil {
									h.markSuccess(r.Context(), upstream)
								}
								return
							}
							if r.Context().Err() != nil || errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, errClientClosed) {
								if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
									logEntry.StatusCode = http.StatusGatewayTimeout
									logEntry.ErrorCode = "request_timeout"
									writeError(w, protocol, http.StatusGatewayTimeout, logEntry.ErrorCode, "gateway request timed out")
								} else {
									logEntry.StatusCode = 499
									logEntry.ErrorCode = "client_closed"
								}
								return
							}
							h.markFailure(r.Context(), upstream.ID, 0, attempt.Error, upstream.ConfigVersion)
							allRateLimited = false
							allTimedOut = allTimedOut && isTimeout(streamErr)
							continue
						}

						buffer := &responseBuffer{budget: &h.responseBudget, limit: h.limits.MaxBufferedResponseBytes}
						responseBody, ttfbMS, readErr := readResponseWithMetrics(r.Context(), response.Body, upstream.FirstByteTimeout, upstream.IdleTimeout, attemptStarted, buffer)
						if readErr == nil {
							defer buffer.release()
						} else {
							buffer.release()
						}
						response.Body.Close()
						if readErr == nil {
							attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
							attempt.TTFBMS = ttfbMS
							logEntry.Attempts = append(logEntry.Attempts, attempt)
							logEntry.UpstreamID = int64ptr(upstream.ID)
							logEntry.StatusCode = response.StatusCode
							logEntry.TTFBMS = ttfbMS
							logEntry.Usage = parseUsageWithProtocol(responseBody, protocol)
							h.markSuccess(r.Context(), upstream)
							if requestEnded() {
								return
							}
							copyResponseHeaders(w.Header(), response.Header)
							setGatewayHeaders(w.Header(), requestID, upstream.Name, len(logEntry.Attempts))
							defer controller.SetWriteDeadline(time.Time{})
							if err := setDownstreamDeadline(r.Context(), w, h.limits.DownstreamWriteTimeout); err == nil {
								w.WriteHeader(response.StatusCode)
								_, err = w.Write(responseBody)
								if err == nil {
									err = controller.Flush()
								}
								if err != nil {
									logEntry.ErrorCode = "client_closed"
								}
							} else {
								logEntry.ErrorCode = "client_closed"
							}
							return
						}
						attempt.TTFBMS = ttfbMS
						err = readErr
					}
				}
			}

			attempt.DurationMS = time.Since(attemptStarted).Milliseconds()
			attempt.Error = safeerr.Text(err, upstream.APIKey)
			attempt.FailureClass = failureClass(r.Context(), err)
			logEntry.Attempts = append(logEntry.Attempts, attempt)
			allRateLimited = false
			allTimedOut = allTimedOut && isTimeout(err)
			if r.Context().Err() != nil {
				logEntry.StatusCode = 499
				logEntry.ErrorCode = "client_closed"
				if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
					logEntry.StatusCode, logEntry.ErrorCode = http.StatusGatewayTimeout, "request_timeout"
					writeError(w, protocol, http.StatusGatewayTimeout, logEntry.ErrorCode, "gateway request timed out")
				}
				return
			}
			if errors.Is(err, errResponseBudget) {
				logEntry.StatusCode, logEntry.ErrorCode = http.StatusServiceUnavailable, "response_budget_exceeded"
				w.Header().Set("Retry-After", "1")
				writeError(w, protocol, logEntry.StatusCode, logEntry.ErrorCode, "response memory budget exhausted")
				return
			}
			h.markFailure(r.Context(), upstream.ID, 0, attempt.Error, upstream.ConfigVersion)
		}

		if requestEnded() {
			return
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
	if r.Context().Err() != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			writeError(w, core.ProtocolChat, http.StatusGatewayTimeout, "request_timeout", "gateway request timed out")
		}
		return
	}
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
	reject := func(status int, code, message string) {
		// Authentication rejects before consuming the body. Do not let HTTP/1
		// drain an unsent body before delivering the rejection or releasing the slot.
		if r.ProtoMajor == 1 && r.Body != nil && r.Body != http.NoBody {
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		}
		writeError(w, protocol, status, code, message)
	}

	select {
	case h.authSlots <- struct{}{}:
		defer func() { <-h.authSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		reject(http.StatusTooManyRequests, "authentication_busy", "too many authentication requests")
		return core.APIKey{}, false
	}
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
		reject(http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
		return core.APIKey{}, false
	}
	key, err := h.repo.Authenticate(r.Context(), token)
	if r.Context().Err() != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			reject(http.StatusGatewayTimeout, "request_timeout", "gateway request timed out")
		}
		return core.APIKey{}, false
	}
	if err != nil {
		if errors.Is(err, ErrInvalidAPIKey) {
			reject(http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		} else {
			reject(http.StatusInternalServerError, "internal_error", "authentication unavailable")
		}
		return core.APIKey{}, false
	}
	if !key.Enabled {
		reject(http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		return core.APIKey{}, false
	}
	return key, true
}

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

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func errorText(err error) string {
	if err == nil {
		return "upstream request failed"
	}
	return safeerr.Text(err)
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
	if h.recorder != nil {
		if !h.recorder.Submit(entry) {
			h.recorder.rememberFlushError(h.recorder.fallback(entry))
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.repo.RecordRequest(ctx, entry); err != nil {
		slog.Error("request log write failed", "request_id", entry.RequestID, "error", err)
	}
}

func (h *Handler) markSuccess(parent context.Context, upstream core.Upstream) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	state := h.lockUpstreamHealth(ctx, upstream.ID)
	if state == nil {
		return
	}
	defer h.unlockUpstreamHealth(upstream.ID, state)
	if !needsSuccessWrite(upstream) && !state.pendingFailure {
		return
	}
	if err := h.repo.MarkUpstreamSuccess(ctx, upstream); err != nil {
		if errors.Is(err, core.ErrUpstreamConfigChanged) {
			return
		}
		slog.Error("upstream success state write failed", "upstream_id", upstream.ID, "error", err)
		return
	}
	state.pendingFailure = false
}

func needsSuccessWrite(upstream core.Upstream) bool {
	return upstream.HealthStatus != "healthy" && upstream.HealthStatus != "unhealthy" ||
		upstream.HealthStatus == "healthy" && upstream.ConsecutiveFailure > 0
}

func (h *Handler) markFailure(parent context.Context, upstreamID int64, status int, reason string, versions ...int64) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	state := h.lockUpstreamHealth(ctx, upstreamID)
	if state == nil {
		return
	}
	defer h.unlockUpstreamHealth(upstreamID, state)
	upstream := core.Upstream{ID: upstreamID}
	if len(versions) > 0 {
		upstream.ConfigVersion = versions[0]
	}
	if err := h.repo.MarkUpstreamFailure(ctx, upstream, status, reason); err != nil {
		if errors.Is(err, core.ErrUpstreamConfigChanged) {
			return
		}
		slog.Error("upstream failure state write failed", "upstream_id", upstreamID, "status", status, "error", err)
		return
	}
	state.pendingFailure = true
}

func (h *Handler) lockUpstreamHealth(ctx context.Context, upstreamID int64) *upstreamHealthState {
	// A per-upstream semaphore preserves failure/success ordering while allowing
	// queued requests to leave when their total or health-write deadline expires.
	h.healthMu.Lock()
	state := h.health[upstreamID]
	if state == nil {
		state = &upstreamHealthState{lock: make(chan struct{}, 1)}
		h.health[upstreamID] = state
	}
	state.references++
	h.healthMu.Unlock()
	select {
	case state.lock <- struct{}{}:
		if ctx.Err() != nil {
			h.unlockUpstreamHealth(upstreamID, state)
			return nil
		}
		return state
	case <-ctx.Done():
		h.releaseUpstreamHealth(upstreamID, state)
		return nil
	}
}

func (h *Handler) unlockUpstreamHealth(upstreamID int64, state *upstreamHealthState) {
	h.releaseUpstreamHealth(upstreamID, state)
	<-state.lock
}

func (h *Handler) releaseUpstreamHealth(upstreamID int64, state *upstreamHealthState) {
	h.healthMu.Lock()
	state.references--
	if state.references == 0 && !state.pendingFailure && h.health[upstreamID] == state {
		delete(h.health, upstreamID)
	}
	h.healthMu.Unlock()
}
