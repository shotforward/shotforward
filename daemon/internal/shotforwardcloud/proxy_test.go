package shotforwardcloud

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProxyOpenAITurnStateRoundTrip(t *testing.T) {
	const body = `{"model":"test-model","input":"hello","prompt_cache_key":"session-1","stream":true}`
	const stream = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":64}}}}\n\n"
	var wantRequestState, responseState string
	status, calls := http.StatusOK, 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-codex-turn-state") != wantRequestState {
			t.Error("upstream turn state was not preserved")
		}
		if r.Header.Get("Authorization") != "Bearer cloud-test-token" || r.Header.Get("X-Private-Local") != "" {
			t.Error("credential boundary was not preserved")
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Error("request body was not preserved")
		}
		header := http.Header{"Content-Type": {"text/event-stream"}}
		if responseState != "" {
			header.Set("x-codex-turn-state", responseState)
		}
		header.Set("Set-Cookie", "private=test")
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken = "cloud-test-token"
	manager.expiresAt = time.Now().Add(time.Hour)
	var receivedState string
	for _, step := range []struct {
		name, expectedRequest, upstreamState string
		continueTurn                         bool
		status                               int
	}{
		{"first", "", "opaque-state-1; route=A", false, http.StatusOK},
		{"continuation", "opaque-state-1; route=A", "opaque-state-2; route=B", true, http.StatusOK},
		{"fresh turn", "", "", false, http.StatusOK},
		{"upstream error", "", "opaque-error-state", false, http.StatusServiceUnavailable},
	} {
		t.Run(step.name, func(t *testing.T) {
			wantRequestState, responseState, status = step.expectedRequest, step.upstreamState, step.status
			r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer local-test-capability")
			r.Header.Set("X-Private-Local", "private")
			if step.continueTurn {
				r.Header.Set("x-codex-turn-state", receivedState)
			}
			w := httptest.NewRecorder()
			before := calls
			manager.ProxyOpenAI(w, r, "/responses")
			if calls != before+1 {
				t.Error("Responses request was retried")
			}
			receivedState = w.Header().Get("x-codex-turn-state")
			if receivedState != step.upstreamState {
				t.Error("response turn state was not preserved")
			}
			if w.Code != status || w.Body.String() != stream || w.Header().Get("Content-Type") != "text/event-stream" {
				t.Error("upstream status or SSE payload was not preserved")
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Error("unapproved response header was forwarded")
			}
		})
	}
}

func TestProxyOpenAIPreservesSessionMetadata(t *testing.T) {
	metadata := http.Header{}
	metadata.Set("Session-ID", "synthetic-session")
	metadata.Set("Thread-ID", "synthetic-thread")
	metadata.Set("X-Codex-Turn-Metadata", `{"turn_id":"synthetic-turn"}`)
	metadata.Set("Traceparent", "00-00000000000000000000000000000001-0000000000000001-01")
	metadata.Add("Tracestate", "vendor1=value1")
	metadata.Add("Tracestate", "vendor2=value2")
	var expected http.Header
	calls := 0
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		for name := range metadata {
			if !reflect.DeepEqual(r.Header.Values(name), expected.Values(name)) {
				t.Errorf("%s values were not preserved", name)
			}
		}
		if r.Header.Get("Authorization") != "Bearer cloud-test-token" || r.Header.Get("Cookie") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Error("local credentials or account override crossed the boundary")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken = "cloud-test-token"
	manager.expiresAt = time.Now().Add(time.Hour)
	for _, withMetadata := range []bool{true, false} {
		expected = make(http.Header)
		if withMetadata {
			expected = metadata.Clone()
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", strings.NewReader(`{"model":"test","input":[{"role":"user","content":"hello"}],"store":false,"stream":true}`))
		r.Header = expected.Clone()
		r.Header.Set("Authorization", "Bearer local-test-capability")
		r.Header.Set("Cookie", "local=test")
		r.Header.Set("ChatGPT-Account-ID", "untrusted-local-account")
		w := httptest.NewRecorder()
		manager.ProxyOpenAI(w, r, "/responses")
		if w.Code != http.StatusOK {
			t.Fatalf("proxy status=%d", w.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("upstream requests=%d, want 2", calls)
	}
}

func TestProxyOpenAIPreservesLargeImageHistory(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "test-model",
		"input": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "Inspect this image"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("a", 5<<20)},
			}},
			map[string]any{"role": "assistant", "content": "The first image is visible."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "Compare this image"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("b", 4<<20)},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 8<<20 {
		t.Fatal("fixture does not cross the former proxy limit")
	}
	calls := 0
	manager := proxyTestManager(t, func(r *http.Request) (*http.Response, error) {
		calls++
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) || !json.Valid(got) {
			t.Error("large multi-image history was changed or truncated")
		}
		if r.ContentLength != int64(len(body)) {
			t.Errorf("upstream Content-Length=%d, want %d", r.ContentLength, len(body))
		}
		return proxyTestResponse(http.StatusOK, `{}`), nil
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", bytes.NewReader(body))
	// Chunked uploads must receive the same protection as known-length uploads.
	r.ContentLength = -1
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, r, "/responses")
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d upstream calls=%d", w.Code, calls)
	}
}

func TestProxyOpenAIRequestBodyBoundary(t *testing.T) {
	const limit = 64 << 20
	for _, step := range []struct {
		name          string
		size          int64
		unknownLength bool
		wantStatus    int
	}{
		{"exact limit", limit, false, http.StatusOK},
		{"limit plus one", limit + 1, false, http.StatusRequestEntityTooLarge},
		{"unknown length limit plus one", limit + 1, true, http.StatusRequestEntityTooLarge},
	} {
		t.Run(step.name, func(t *testing.T) {
			calls := 0
			wantDigest := sha256.New()
			_, _ = io.Copy(wantDigest, proxyJSONBody(step.size))
			manager := proxyTestManager(t, func(r *http.Request) (*http.Response, error) {
				calls++
				gotDigest := sha256.New()
				size, err := io.Copy(gotDigest, r.Body)
				if err != nil || size != step.size || !bytes.Equal(gotDigest.Sum(nil), wantDigest.Sum(nil)) {
					t.Error("boundary-sized body was changed or truncated")
				}
				if r.ContentLength != step.size {
					t.Errorf("Content-Length=%d, want %d", r.ContentLength, step.size)
				}
				return proxyTestResponse(http.StatusOK, `{}`), nil
			})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", proxyJSONBody(step.size))
			r.ContentLength = step.size
			if step.unknownLength {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			manager.ProxyOpenAI(w, r, "/responses")
			if w.Code != step.wantStatus {
				t.Fatalf("status=%d, want %d", w.Code, step.wantStatus)
			}
			if step.wantStatus == http.StatusOK {
				if calls != 1 {
					t.Fatalf("upstream calls=%d, want 1", calls)
				}
				return
			}
			if calls != 0 {
				t.Fatal("oversized request reached upstream")
			}
			var envelope struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Param   any    `json:"param"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error.Code != "request_too_large" || envelope.Error.Type != "invalid_request_error" || envelope.Error.Param != nil || !strings.Contains(envelope.Error.Message, "64 MiB") {
				t.Fatal("oversized request did not return an actionable OpenAI error")
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatal("oversized request did not return JSON content type")
			}
		})
	}
}

func TestProxyOpenAIRejectsRequestReadErrors(t *testing.T) {
	calls := 0
	manager := proxyTestManager(t, func(*http.Request) (*http.Response, error) {
		calls++
		return proxyTestResponse(http.StatusOK, `{}`), nil
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", io.MultiReader(strings.NewReader(`{"input":"partial`), proxyErrorReader{}))
	w := httptest.NewRecorder()
	manager.ProxyOpenAI(w, r, "/responses")
	if w.Code != http.StatusBadGateway || calls != 0 || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("read failure status=%d upstream calls=%d", w.Code, calls)
	}
}

func TestProxyOpenAIPreservesEncodedRequestBodies(t *testing.T) {
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err := zipper.Write([]byte(`{"input":"compressed image history"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		encoding string
		body     []byte
	}{
		{"gzip", compressed.Bytes()},
		// The proxy must also preserve encodings it cannot decode itself.
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x00, 0x01, 0x00, 0x00}},
	} {
		t.Run(step.encoding, func(t *testing.T) {
			calls := 0
			manager := proxyTestManager(t, func(r *http.Request) (*http.Response, error) {
				calls++
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, step.body) || r.Header.Get("Content-Encoding") != step.encoding {
					t.Error("encoded request bytes or encoding header were changed")
				}
				if r.Header.Get("Content-Type") != "application/json" || r.ContentLength != int64(len(step.body)) {
					t.Error("encoded request metadata was changed")
				}
				return proxyTestResponse(http.StatusOK, `{}`), nil
			})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai/responses", bytes.NewReader(step.body))
			r.Header.Set("Content-Encoding", step.encoding)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			manager.ProxyOpenAI(w, r, "/responses")
			if w.Code != http.StatusOK || calls != 1 {
				t.Fatalf("status=%d upstream calls=%d", w.Code, calls)
			}
		})
	}
}

func TestProxyOpenAICompactRouteIsExplicit(t *testing.T) {
	calls := 0
	const body = `{"model":"test-model","input":[{"role":"user","content":"summarize history"}]}`
	manager := proxyTestManager(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses/compact" {
			t.Error("unexpected compact upstream method or path")
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Error("compact body was changed")
		}
		if r.Header.Get("Authorization") != "Bearer cloud-test-token" || r.Header.Get("Cookie") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Error("compact request crossed the credential boundary")
		}
		return proxyTestResponse(http.StatusOK, `{"object":"response.compaction","output":[]}`), nil
	})
	handler, err := NewHandler(manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		method, suffix string
		wantStatus     int
	}{
		{http.MethodPost, "/responses/compact", http.StatusOK},
		{http.MethodGet, "/responses/compact", http.StatusMethodNotAllowed},
		{http.MethodPut, "/responses/compact", http.StatusMethodNotAllowed},
		{http.MethodPost, "/responses/compact/", http.StatusNotFound},
		{http.MethodPost, "/responses/compact/other", http.StatusNotFound},
		{http.MethodPost, "/responses/compact-other", http.StatusNotFound},
		{http.MethodPost, "/other", http.StatusNotFound},
	} {
		t.Run(step.method+" "+step.suffix, func(t *testing.T) {
			before := calls
			r := httptest.NewRequest(step.method, "/api/v1/cloud/openai"+step.suffix, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer local-test-capability")
			r.Header.Set("Cookie", "local=test")
			r.Header.Set("ChatGPT-Account-ID", "untrusted-local-account")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != step.wantStatus {
				t.Fatalf("status=%d, want %d", w.Code, step.wantStatus)
			}
			wantCalls := before
			if step.wantStatus == http.StatusOK {
				wantCalls++
			}
			if calls != wantCalls {
				t.Fatal("unapproved route was forwarded or request was retried")
			}
			if step.wantStatus == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodPost {
				t.Fatal("compact method rejection did not advertise POST")
			}
		})
	}
}

func TestProxyOpenAIPreservesUpstreamErrorsWithoutRetry(t *testing.T) {
	for _, suffix := range []string{"/responses", "/responses/compact"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusInternalServerError} {
			t.Run(suffix+" "+http.StatusText(status), func(t *testing.T) {
				calls := 0
				const body = `{"error":{"message":"upstream rejected request","type":"invalid_request_error","param":null,"code":"upstream_error"}}`
				manager := proxyTestManager(t, func(*http.Request) (*http.Response, error) {
					calls++
					response := proxyTestResponse(status, body)
					response.Header.Set("X-Request-ID", "req_synthetic")
					return response, nil
				})
				r := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/openai"+suffix, strings.NewReader(`{"input":"hello"}`))
				w := httptest.NewRecorder()
				manager.ProxyOpenAI(w, r, suffix)
				if w.Code != status || w.Body.String() != body || w.Header().Get("X-Request-ID") != "req_synthetic" || calls != 1 {
					t.Fatalf("upstream error changed: status=%d calls=%d", w.Code, calls)
				}
			})
		}
	}
}

func proxyTestManager(t *testing.T, transport roundTripFunc) *Manager {
	t.Helper()
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken = "cloud-test-token"
	manager.expiresAt = time.Now().Add(time.Hour)
	return manager
}

func proxyTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// Generate valid large JSON without holding another entire boundary-sized body
// in memory alongside the proxy's bounded request buffer.
func proxyJSONBody(size int64) io.Reader {
	const prefix, suffix = `{"input":"`, `"}`
	return io.MultiReader(strings.NewReader(prefix), io.LimitReader(proxyPaddingReader{}, size-int64(len(prefix)+len(suffix))), strings.NewReader(suffix))
}

type proxyPaddingReader struct{}

func (proxyPaddingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'a'
	}
	return len(buffer), nil
}

type proxyErrorReader struct{}

func (proxyErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic request read failure")
}
