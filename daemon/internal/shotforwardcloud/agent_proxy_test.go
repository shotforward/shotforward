package shotforwardcloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentProxyClientPreservesCallerConfiguration(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{ResponseHeaderTimeout: 11 * time.Second, TLSHandshakeTimeout: 7 * time.Second}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &http.Client{Transport: transport, Jar: jar, CheckRedirect: redirect, Timeout: 90 * time.Second}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if manager.http != client || client.Timeout != 90*time.Second || manager.agentHTTP == client || manager.agentHTTP.Timeout != 0 {
		t.Fatal("long request client mutated or replaced the ordinary client")
	}
	if manager.agentHTTP.Transport != transport || manager.agentHTTP.Jar != jar || reflect.ValueOf(manager.agentHTTP.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
		t.Fatal("transport, TLS/proxy, redirect, or cookie policy was lost")
	}
	if manager.proxyTimeouts != (agentProxyTimeouts{header: 5 * time.Minute, readIdle: 5 * time.Minute, writeIdle: 5 * time.Minute}) {
		t.Fatalf("unexpected production timeouts: %#v", manager.proxyTimeouts)
	}
}

func TestAgentProxyContinuousStreamOutlivesOrdinaryTotalTimeout(t *testing.T) {
	const prefix = ": heartbeat\n\n"
	const completed = "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for range 8 {
			_, _ = io.WriteString(w, prefix)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(40 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, completed)
	}))
	defer upstream.Close()
	client := upstream.Client()
	client.Timeout = 30 * time.Millisecond
	manager := agentProxyManager(t, upstream.URL, client)
	manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: 200 * time.Millisecond}
	w := httptest.NewRecorder()
	started := time.Now()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses")
	if w.Code != http.StatusOK || w.Body.String() != strings.Repeat(prefix, 8)+completed || calls.Load() != 1 {
		t.Fatalf("live stream was truncated or replayed: status=%d calls=%d bytes=%d", w.Code, calls.Load(), w.Body.Len())
	}
	if time.Since(started) <= 5*client.Timeout {
		t.Fatal("fixture did not cross the ordinary total timeout")
	}
}

func TestAgentProxyCompactOutlivesOrdinaryTotalTimeout(t *testing.T) {
	const body = `{"object":"response.compaction","output":[]}`
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	client := upstream.Client()
	client.Timeout = 20 * time.Millisecond
	manager := agentProxyManager(t, upstream.URL, client)
	manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: time.Second}
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses/compact"), "/responses/compact")
	if w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("compact was truncated: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAgentProxyOrdinaryModelsStillHaveClientTimeout(t *testing.T) {
	manager := agentProxyManager(t, "https://cloud.example", &http.Client{Timeout: 25 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: time.Second}
	w := httptest.NewRecorder()
	started := time.Now()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/models"), "/models")
	if w.Code != http.StatusGatewayTimeout || time.Since(started) >= time.Second || manager.http.Timeout != 25*time.Millisecond {
		t.Fatalf("ordinary timeout changed: status=%d elapsed=%s", w.Code, time.Since(started))
	}
}

func TestAgentProxyHeaderTimeoutIsBoundedWithoutRetry(t *testing.T) {
	for _, suffix := range []string{"/responses", "/responses/compact"} {
		t.Run(suffix, func(t *testing.T) {
			calls := 0
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				<-r.Context().Done()
				return nil, r.Context().Err()
			})})
			manager.proxyTimeouts = agentProxyTimeouts{header: 25 * time.Millisecond, readIdle: time.Second}
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			w := httptest.NewRecorder()
			manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), suffix), suffix)
			if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), "agent_proxy_header_timeout") || calls != 1 || !strings.Contains(logs.String(), "header_timeout") {
				t.Fatalf("header timeout was hidden or replayed: status=%d calls=%d logs=%q", w.Code, calls, logs.String())
			}
		})
	}
}

func TestAgentProxyLateHeadersCannotTurnTimeoutIntoSuccess(t *testing.T) {
	manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		// A custom Transport can return a response despite cancellation.
		return proxyTestResponse(http.StatusOK, `{"late":true}`), nil
	})})
	manager.proxyTimeouts = agentProxyTimeouts{header: 20 * time.Millisecond, readIdle: time.Second}
	attachProxyTestLogger(t, manager, io.Discard)
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses")
	if w.Code != http.StatusGatewayTimeout || strings.Contains(w.Body.String(), "late") {
		t.Fatalf("late headers hid the timeout: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAgentProxyReadIdleTimeoutClosesBodyAndAbortsStream(t *testing.T) {
	for _, suffix := range []string{"/responses", "/responses/compact"} {
		t.Run(suffix, func(t *testing.T) {
			body := &proxyBlockingBody{prefix: strings.NewReader(": heartbeat\n\n"), closed: make(chan struct{})}
			calls := 0
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})})
			manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: 25 * time.Millisecond}
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			w := httptest.NewRecorder()
			if !proxyCallAborted(func() { manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), suffix), suffix) }) {
				t.Fatal("idle stream returned a clean, apparently successful EOF")
			}
			if calls != 1 || w.Body.String() != ": heartbeat\n\n" || !strings.Contains(logs.String(), "read_idle_timeout") {
				t.Fatalf("idle stream was changed, replayed, or undiagnosed: calls=%d logs=%q", calls, logs.String())
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("idle cancellation did not close the upstream body")
			}
		})
	}
}

func TestAgentProxyCancellationIsNotAnUpstreamFailure(t *testing.T) {
	for _, headersReturned := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[headersReturned], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			body := &proxyBlockingBody{prefix: strings.NewReader(": heartbeat\n\n"), closed: make(chan struct{})}
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				close(entered)
				if headersReturned {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})})
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			done := make(chan struct{})
			w := httptest.NewRecorder()
			go func() {
				defer close(done)
				manager.ProxyOpenAI(w, agentProxyRequest(ctx, "/responses"), "/responses")
			}()
			<-entered
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not release the proxy")
			}
			if w.Code >= 500 || strings.Contains(logs.String(), "WARN") || strings.Contains(w.Body.String(), "cloud_unavailable") {
				t.Fatalf("downstream cancellation was reported as upstream failure: status=%d logs=%q", w.Code, logs.String())
			}
			if headersReturned {
				select {
				case <-body.closed:
				default:
					t.Fatal("downstream cancellation left upstream body open")
				}
			}
		})
	}
}

func TestAgentProxyReadFailureIsNotSilentAndDoesNotLeakSecrets(t *testing.T) {
	const secret = "https://private.example/path?token=secret-token prompt-private"
	manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader("data: partial\n\n"), proxyFailureReader{errors.New(secret)}))}, nil
	})})
	var logs bytes.Buffer
	attachProxyTestLogger(t, manager, &logs)
	w := httptest.NewRecorder()
	if !proxyCallAborted(func() { manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses") }) || !strings.Contains(logs.String(), "upstream_read_failed") {
		t.Fatal("read failure was silently reported as a successful stream")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "secret-token") || strings.Contains(w.Body.String(), "cloud_unavailable") {
		t.Fatal("proxy leaked an upstream error or injected JSON into the SSE bytes")
	}
}

func TestAgentProxyHeaderFailureDoesNotLeakSecrets(t *testing.T) {
	const secret = "https://private.example/path?token=secret-token prompt-private"
	calls := 0
	manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New(secret)
	})})
	var logs bytes.Buffer
	attachProxyTestLogger(t, manager, &logs)
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses")
	if calls != 1 || w.Code != http.StatusBadGateway || !strings.Contains(logs.String(), "upstream_headers_failed") || strings.Contains(logs.String()+w.Body.String(), "secret-token") {
		t.Fatalf("header error leaked, retried, or went undiagnosed: status=%d calls=%d logs=%q", w.Code, calls, logs.String())
	}
}

func TestAgentProxyFlushesThroughResponseWriterWrapper(t *testing.T) {
	manager := proxyTestManager(t, func(*http.Request) (*http.Response, error) {
		return proxyTestResponse(http.StatusOK, "data: unchanged\n\n"), nil
	})
	recorder := httptest.NewRecorder()
	w := proxyResponseWrapper{ResponseWriter: recorder}
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses")
	if !recorder.Flushed || recorder.Body.String() != "data: unchanged\n\n" {
		t.Fatal("stream was buffered or changed through an unwrapping response wrapper")
	}
}

func TestAgentProxyWriteAndFlushFailureStopUpstream(t *testing.T) {
	for _, failFlush := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "flush"}[failFlush], func(t *testing.T) {
			body := &proxyBlockingBody{prefix: strings.NewReader("data: first\n\n"), closed: make(chan struct{})}
			var upstreamContext context.Context
			calls := 0
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				upstreamContext = r.Context()
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})})
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			w := &proxyBrokenWriter{ResponseRecorder: httptest.NewRecorder(), failFlush: failFlush}
			if !proxyCallAborted(func() { manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses") }) {
				t.Fatal("downstream failure did not abort the stream")
			}
			if calls != 1 || upstreamContext.Err() == nil || !strings.Contains(logs.String(), "downstream_write_failed") || strings.Contains(logs.String(), "private-write-error") {
				t.Fatalf("write failure leaked, retried, or left upstream running: calls=%d logs=%q", calls, logs.String())
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("downstream failure left upstream body open")
			}
		})
	}
}

func TestAgentProxyAbortIsObservableByHTTPClient(t *testing.T) {
	manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader("data: partial\n\n"), proxyFailureReader{io.ErrUnexpectedEOF}))}, nil
	})})
	attachProxyTestLogger(t, manager, io.Discard)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { manager.ProxyOpenAI(w, r, "/responses") }))
	defer proxy.Close()
	response, err := proxy.Client().Post(proxy.URL, "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		return // An abort before the HTTP headers arrive is also a transport error.
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("HTTP client received clean successful EOF after upstream read failure")
	}
}

func TestAgentProxyPaymentErrorPrefetchPreservesLongBody(t *testing.T) {
	const chunk = "payment-error-detail-without-private-data\n"
	const count = 300
	// More than 64 KiB ensures both the billing pre-read and forwarding paths
	// are exercised. Each chunk arrives before idle timeout, but the response
	// lasts much longer than the ordinary Client's total timeout.
	want := strings.Repeat(chunk, count*8)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		for range 8 {
			_, _ = io.WriteString(w, strings.Repeat(chunk, count))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}))
	defer upstream.Close()
	client := upstream.Client()
	client.Timeout = 20 * time.Millisecond
	manager := agentProxyManager(t, upstream.URL, client)
	manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: 150 * time.Millisecond}
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, agentProxyRequest(context.Background(), "/responses"), "/responses")
	if len(want) <= 64<<10 || w.Code != http.StatusPaymentRequired || w.Body.String() != want {
		t.Fatalf("billing pre-read truncated or changed the body: status=%d bytes=%d want=%d", w.Code, w.Body.Len(), len(want))
	}
}

func TestAgentProxyPaymentErrorPrefetchStallAndCancellationCloseOriginalBody(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "cancel"}[cancellation], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			body := &proxyBlockingBody{prefix: strings.NewReader(`{"error":`), closed: make(chan struct{})}
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				close(entered)
				return &http.Response{StatusCode: http.StatusPaymentRequired, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})})
			manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: 30 * time.Millisecond}
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				manager.ProxyOpenAI(w, agentProxyRequest(ctx, "/responses"), "/responses")
			}()
			<-entered
			if cancellation {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("billing pre-read stayed blocked after cancellation")
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("original upstream body was not closed")
			}
			if cancellation {
				if w.Code >= 500 || strings.Contains(logs.String(), "WARN") {
					t.Fatalf("cancelled billing pre-read returned 5xx: status=%d logs=%q", w.Code, logs.String())
				}
			} else if w.Code != http.StatusGatewayTimeout || !strings.Contains(logs.String(), "read_idle_timeout") {
				t.Fatalf("stalled billing pre-read was undiagnosed: status=%d logs=%q", w.Code, logs.String())
			}
		})
	}
}

func TestAgentProxyBlockedDownstreamWriteIsBoundedAndCancelable(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		t.Run(map[bool]string{false: "write idle", true: "cancel"}[cancellation], func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close() // Deliberately never read from the downstream peer.
			body := &proxyBlockingBody{prefix: strings.NewReader("data: first\n\n"), closed: make(chan struct{})}
			manager := agentProxyManager(t, "https://cloud.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})})
			manager.proxyTimeouts = agentProxyTimeouts{header: time.Second, readIdle: time.Second, writeIdle: 30 * time.Millisecond}
			if cancellation {
				manager.proxyTimeouts.writeIdle = time.Hour
			}
			var logs bytes.Buffer
			attachProxyTestLogger(t, manager, &logs)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &proxyConnectionWriter{Conn: left, header: make(http.Header), entered: make(chan struct{})}
			done := make(chan bool, 1)
			go func() {
				done <- proxyCallAborted(func() { manager.ProxyOpenAI(w, agentProxyRequest(ctx, "/responses"), "/responses") })
			}()
			<-w.entered
			if cancellation {
				cancel()
			}
			select {
			case aborted := <-done:
				if aborted == cancellation {
					t.Fatalf("unexpected abort after cancellation=%v: aborted=%v", cancellation, aborted)
				}
			case <-time.After(time.Second):
				t.Fatal("downstream Write stayed blocked beyond its deadline or cancellation")
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("blocked write left upstream body open")
			}
			if cancellation && strings.Contains(logs.String(), "WARN") {
				t.Fatalf("downstream cancellation was misclassified: %s", logs.String())
			}
		})
	}
}

func agentProxyManager(t *testing.T, base string, client *http.Client) *Manager {
	t.Helper()
	manager, err := NewManager(base, &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken, manager.expiresAt = "cloud-test-token", time.Now().Add(time.Hour)
	return manager
}

func agentProxyRequest(ctx context.Context, suffix string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai"+suffix, strings.NewReader(`{"stream":true}`)).WithContext(ctx)
}

func attachProxyTestLogger(t *testing.T, manager *Manager, output io.Writer) {
	t.Helper()
	if _, err := NewHandler(manager, slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug}))); err != nil {
		t.Fatal(err)
	}
}

func proxyCallAborted(call func()) (aborted bool) {
	defer func() {
		if caught := recover(); caught != nil {
			if caught != http.ErrAbortHandler {
				panic(caught)
			}
			aborted = true
		}
	}()
	call()
	return false
}

type proxyBlockingBody struct {
	prefix *strings.Reader
	closed chan struct{}
	once   sync.Once
}

func (b *proxyBlockingBody) Read(buffer []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(buffer)
	}
	<-b.closed
	return 0, errors.New("closed")
}

func (b *proxyBlockingBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type proxyFailureReader struct{ err error }

func (r proxyFailureReader) Read([]byte) (int, error) { return 0, r.err }

type proxyBrokenWriter struct {
	*httptest.ResponseRecorder
	failFlush bool
}

func (w *proxyBrokenWriter) Write(buffer []byte) (int, error) {
	if !w.failFlush {
		return 0, errors.New("private-write-error")
	}
	return w.ResponseRecorder.Write(buffer)
}

func (w *proxyBrokenWriter) FlushError() error { return errors.New("private-write-error") }

type proxyConnectionWriter struct {
	net.Conn
	header  http.Header
	entered chan struct{}
	once    sync.Once
}

func (w *proxyConnectionWriter) Header() http.Header { return w.header }
func (w *proxyConnectionWriter) WriteHeader(int)     {}
func (w *proxyConnectionWriter) Write(buffer []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.Conn.Write(buffer)
}

type proxyResponseWrapper struct{ http.ResponseWriter }

func (w proxyResponseWrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }
