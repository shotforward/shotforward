package shotforwardcloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

var (
	errAgentProxyHeaderTimeout    = errors.New("Agent upstream response headers timed out")
	errAgentProxyReadIdleTimeout  = errors.New("Agent upstream response stopped sending data")
	errAgentProxyWriteIdleTimeout = errors.New("Agent downstream stopped accepting data")
)

type agentProxyTimeouts struct {
	header    time.Duration
	readIdle  time.Duration
	writeIdle time.Duration
}

func defaultAgentProxyTimeouts() agentProxyTimeouts {
	// These bound connection/header waits and stalls, not total reasoning time.
	// Any response bytes, including SSE heartbeats, keep a live stream alive.
	return agentProxyTimeouts{header: 5 * time.Minute, readIdle: 5 * time.Minute, writeIdle: 5 * time.Minute}
}

// One timer per request bounds each phase. The lock/deadline check prevents a
// concurrently fired old timer from cancelling a stream just refreshed by data.
type agentProxyWatchdog struct {
	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	cause    error
	cancel   context.CancelCauseFunc
	stopped  bool
}

func newAgentProxyWatchdog(cancel context.CancelCauseFunc, duration time.Duration, cause error) *agentProxyWatchdog {
	w := &agentProxyWatchdog{cancel: cancel, deadline: time.Now().Add(duration), cause: cause}
	w.mu.Lock()
	w.timer = time.AfterFunc(duration, w.expire)
	w.mu.Unlock()
	return w
}

func (w *agentProxyWatchdog) expire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	if remaining := time.Until(w.deadline); remaining > 0 {
		w.timer.Reset(remaining)
		w.mu.Unlock()
		return
	}
	w.stopped = true
	cause := w.cause
	w.mu.Unlock()
	w.cancel(cause)
}

func (w *agentProxyWatchdog) reset(duration time.Duration, cause error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.deadline, w.cause = time.Now().Add(duration), cause
		w.timer.Reset(duration)
	}
}

func (w *agentProxyWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}

type agentProxyActivityBody struct {
	io.ReadCloser
	watchdog *agentProxyWatchdog
	timeout  time.Duration
}

func (b *agentProxyActivityBody) Read(buffer []byte) (int, error) {
	count, err := b.ReadCloser.Read(buffer)
	if count > 0 {
		b.watchdog.reset(b.timeout, errAgentProxyReadIdleTimeout)
	}
	return count, err
}

type agentProxyCloseOnceBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *agentProxyCloseOnceBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

func (m *Manager) forwardAgentProxyBody(w http.ResponseWriter, r *http.Request, suffix string, response *http.Response, ctx context.Context, cancel context.CancelCauseFunc, watchdog *agentProxyWatchdog, started time.Time) {
	w.WriteHeader(response.StatusCode)
	controller := http.NewResponseController(w)
	writeIdle := m.proxyTimeouts.writeIdle
	if writeIdle <= 0 {
		writeIdle = defaultAgentProxyTimeouts().writeIdle
	}
	// The production HTTP server has no global WriteTimeout (SSE can live for
	// hours). Bound each blocked write instead, and wake it promptly on cancel.
	// Serialize callback cleanup so it never uses w after this handler returns.
	var deadlineMu sync.Mutex
	finished := false
	stopWriteCancel := context.AfterFunc(ctx, func() {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		if !finished {
			_ = controller.SetWriteDeadline(time.Now())
		}
	})
	defer func() {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		finished = true
		stopWriteCancel()
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	buffer := make([]byte, 32<<10)
	var forwarded int64
	for {
		if ctx.Err() != nil {
			m.failAgentProxy(w, r, suffix, "read", ctx, ctx.Err(), response.StatusCode, forwarded, started, true)
			return
		}
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if watchdog != nil {
				watchdog.reset(writeIdle, errAgentProxyWriteIdleTimeout)
			}
			deadlineMu.Lock()
			deadline := time.Now().Add(writeIdle)
			if ctx.Err() != nil {
				deadline = time.Now()
			}
			deadlineErr := controller.SetWriteDeadline(deadline)
			deadlineMu.Unlock()
			if deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
				cancel(deadlineErr)
				m.failAgentProxy(w, r, suffix, "write", ctx, deadlineErr, response.StatusCode, forwarded, started, true)
				return
			}
			written, writeErr := w.Write(buffer[:count])
			forwarded += int64(written)
			if writeErr == nil && written != count {
				writeErr = io.ErrShortWrite
			}
			if writeErr == nil {
				if flushErr := controller.Flush(); !errors.Is(flushErr, http.ErrNotSupported) {
					writeErr = flushErr
				}
			}
			if writeErr != nil {
				cancel(writeErr)
				m.failAgentProxy(w, r, suffix, "write", ctx, writeErr, response.StatusCode, forwarded, started, true)
				return
			}
			if watchdog != nil {
				watchdog.reset(m.proxyTimeouts.readIdle, errAgentProxyReadIdleTimeout)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) || ctx.Err() != nil {
				m.failAgentProxy(w, r, suffix, "read", ctx, readErr, response.StatusCode, forwarded, started, true)
			}
			return
		}
	}
}

// Never log raw errors: they may contain credentials, full URLs, or user data.
// Once headers have been sent, abort the HTTP stream instead of turning an
// upstream failure into a clean, apparently successful EOF. The Runtime, not
// this credential proxy, owns all retry decisions.
func (m *Manager) failAgentProxy(w http.ResponseWriter, r *http.Request, suffix, phase string, ctx context.Context, err error, status int, forwarded int64, started time.Time, headersSent bool) {
	reason := "upstream_" + phase + "_failed"
	if phase == "write" {
		reason = "downstream_write_failed"
	}
	code, message, localStatus := "cloud_unavailable", "Agent upstream request failed", http.StatusBadGateway
	cause := context.Cause(ctx)
	switch {
	case r.Context().Err() != nil:
		reason = "downstream_canceled"
	case errors.Is(cause, errAgentProxyHeaderTimeout):
		reason, code, message, localStatus = "header_timeout", "agent_proxy_header_timeout", errAgentProxyHeaderTimeout.Error(), http.StatusGatewayTimeout
	case errors.Is(cause, errAgentProxyReadIdleTimeout):
		reason, code, message, localStatus = "read_idle_timeout", "agent_proxy_idle_timeout", errAgentProxyReadIdleTimeout.Error(), http.StatusGatewayTimeout
	case errors.Is(cause, errAgentProxyWriteIdleTimeout):
		reason, code, message, localStatus = "write_idle_timeout", "agent_proxy_write_timeout", errAgentProxyWriteIdleTimeout.Error(), http.StatusGatewayTimeout
	case errors.Is(err, context.DeadlineExceeded):
		reason, localStatus = "request_timeout", http.StatusGatewayTimeout
	}
	m.mu.Lock()
	logger := m.proxyLogger
	m.mu.Unlock()
	attributes := []any{"operation", suffix, "phase", phase, "reason", reason, "upstream_status", status, "forwarded_bytes", forwarded, "elapsed_ms", time.Since(started).Milliseconds()}
	if reason == "downstream_canceled" {
		logger.Debug("ShotForward Agent proxy canceled", attributes...)
		return // A closed downstream is not an upstream 5xx response.
	}
	logger.Warn("ShotForward Agent proxy interrupted", attributes...)
	if headersSent {
		panic(http.ErrAbortHandler)
	}
	writeJSON(w, localStatus, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
