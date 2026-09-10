package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/netguard"
)

const maxCachedTransports = 32

type transportEntry struct {
	client *http.Client
	used   time.Time
}

type transportKey struct {
	connect, firstByte, idle time.Duration
}

func (h *Handler) client(upstream core.Upstream) *http.Client {
	key := transportKey{upstream.ConnectTimeout, upstream.FirstByteTimeout, upstream.IdleTimeout}
	if key.connect <= 0 {
		key.connect = 5 * time.Second
	}
	if key.firstByte <= 0 {
		key.firstByte = 180 * time.Second
	}
	if key.idle <= 0 {
		key.idle = 90 * time.Second
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.transports[key]; entry != nil {
		entry.used = time.Now()
		return entry.client
	}
	if len(h.transports) >= maxCachedTransports {
		var oldest transportKey
		var oldestAt time.Time
		for candidate, entry := range h.transports {
			if oldestAt.IsZero() || entry.used.Before(oldestAt) {
				oldest, oldestAt = candidate, entry.used
			}
		}
		h.transports[oldest].client.CloseIdleConnections()
		delete(h.transports, oldest)
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
	h.transports[key] = &transportEntry{client: client, used: time.Now()}
	return client
}

// Called after handlers drain. Eviction and shutdown only close idle connections.
func (h *Handler) closeTransports() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, entry := range h.transports {
		entry.client.CloseIdleConnections()
		delete(h.transports, key)
	}
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
	// Let Transport negotiate gzip and transparently decode it. Forwarding a
	// caller's Accept-Encoding disables that decoding and breaks usage/SSE parsing.
	out.Header.Del("Accept-Encoding")
	out.Header.Del("X-Api-Key")
	out.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	if upstream.UserAgent != "" {
		out.Header.Set("User-Agent", upstream.UserAgent)
	}
	if protocol == core.ProtocolMessages {
		out.Header.Set("X-Api-Key", upstream.APIKey)
	}
	return out, nil
}

type requestBodies struct {
	body      []byte
	model     string
	rewritten map[string][]byte
}

type requestPayload struct {
	Model  string
	Stream bool
}

// Inspect top-level keys before forwarding the original bytes. Different JSON
// implementations must not choose different model or stream values.
func parseRequestPayload(body []byte) (requestPayload, error) {
	var result requestPayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return result, errors.New("request must be an object")
	}
	seenModel, seenStream := false, false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return result, err
		}
		key, ok := token.(string)
		if !ok {
			return result, errors.New("invalid field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return result, err
		}
		switch {
		case strings.EqualFold(key, "model"):
			if key != "model" || seenModel {
				return result, errors.New("ambiguous model field")
			}
			seenModel = true
			if err := json.Unmarshal(value, &result.Model); err != nil {
				return result, err
			}
		case strings.EqualFold(key, "stream"):
			if key != "stream" || seenStream {
				return result, errors.New("ambiguous stream field")
			}
			seenStream = true
			if err := json.Unmarshal(value, &result.Stream); err != nil {
				return result, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return result, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return result, errors.New("trailing request data")
	}
	return result, nil
}

func (b *requestBodies) forModel(model string) ([]byte, error) {
	if model == b.model {
		return b.body, nil
	}
	if body, ok := b.rewritten[model]; ok {
		return body, nil
	}
	body, err := replaceModel(b.body, model)
	if err != nil {
		return nil, err
	}
	if b.rewritten == nil {
		b.rewritten = make(map[string][]byte)
	}
	b.rewritten[model] = body
	return body, nil
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

func drainAndClose(ctx context.Context, body io.ReadCloser) {
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	timer := time.AfterFunc(100*time.Millisecond, func() { _ = body.Close() })
	defer timer.Stop()
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
