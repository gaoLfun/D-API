package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

func (h *Handler) relayStream(ctx context.Context, w http.ResponseWriter, response *http.Response, requestID, upstreamName string, attempts int, firstByteTimeout, idleTimeout time.Duration) (bool, core.Usage, error) {
	committed, usage, _, _, err := h.relayStreamWithMetrics(ctx, w, response, requestID, upstreamName, attempts, firstByteTimeout, idleTimeout, "", time.Now())
	return committed, usage, err
}

func (h *Handler) relayStreamWithMetrics(ctx context.Context, w http.ResponseWriter, response *http.Response, requestID, upstreamName string, attempts int, firstByteTimeout, idleTimeout time.Duration, protocol string, attemptStarted time.Time) (bool, core.Usage, *int64, *int64, error) {
	defer response.Body.Close()
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if firstByteTimeout <= 0 {
		firstByteTimeout = 180 * time.Second
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
	parser := sseUsageParser{protocol: protocol, requireCompletion: protocol != "" && response.StatusCode >= 200 && response.StatusCode < 300}
	var ttfbMS, ttftMS *int64
	timeout := firstByteTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		timer.Reset(timeout)
		select {
		case result := <-results:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if result.n > 0 {
				if err := setDownstreamDeadline(ctx, w, h.limits.DownstreamWriteTimeout); err != nil {
					return committed, parser.Usage(), ttfbMS, ttftMS, fmt.Errorf("%w: write deadline", errClientClosed)
				}
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
						if parser.requireCompletion {
							return false, parser.Usage(), ttfbMS, ttftMS, errors.New("upstream returned an empty stream")
						}
						copyResponseHeaders(w.Header(), response.Header)
						setGatewayHeaders(w.Header(), requestID, upstreamName, attempts)
						w.WriteHeader(response.StatusCode)
						committed = true
					}
					usage := parser.Usage()
					return committed, usage, ttfbMS, ttftMS, parser.StreamError()
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

func readResponseWithMetrics(ctx context.Context, body io.ReadCloser, firstByteTimeout, idleTimeout time.Duration, started time.Time, buffers ...*responseBuffer) ([]byte, *int64, error) {
	if firstByteTimeout <= 0 {
		firstByteTimeout = 180 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	type readResult struct {
		content []byte
		err     error
	}
	results, done, acknowledge := make(chan readResult), make(chan struct{}), make(chan struct{})
	defer close(done)
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := body.Read(buffer)
			content := buffer[:n]
			select {
			case results <- readResult{content: content, err: err}:
			case <-done:
				return
			}
			select {
			case <-acknowledge:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	buffer := &responseBuffer{}
	if len(buffers) > 0 {
		buffer = buffers[0]
	}
	content := buffer.data
	var ttfbMS *int64
	timeout := firstByteTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		timer.Reset(timeout)
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
				if err := buffer.append(result.content); err != nil {
					_ = body.Close()
					return nil, ttfbMS, err
				}
				content = buffer.data
				timeout = idleTimeout
			}
			acknowledge <- struct{}{}
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
