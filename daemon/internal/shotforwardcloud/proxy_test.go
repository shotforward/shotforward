package shotforwardcloud

import (
	"io"
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
