package shotforwardcloud

import (
	"context"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailedCloudTaskPreservesBusinessCode(t *testing.T) {
	manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"id": "task-1", "status": "failed", "error": map[string]string{"code": "insufficient_points", "message": "raw upstream"}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken, manager.expiresAt = "test-access", time.Now().Add(time.Hour)
	generator := &Generator{manager: manager}
	status, err := generator.Poll(context.Background(), generation.GeneratorOperation{ID: "task-1"})
	if err != nil || status.FailureCode != "shotforward_insufficient_points" || strings.Contains(status.FailureMessage, "raw upstream") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	code, _ := generation.PublicGenerationFailure(status.FailureCode, status.FailureMessage)
	if code != status.FailureCode {
		t.Fatal("public task response lost code")
	}
}

func TestCloudBusinessFailuresRemainStructured(t *testing.T) {
	for _, code := range []string{"negative_balance", "insufficient_points", "wallet_not_found"} {
		body := `{"error":{"code":"` + code + `","message":"untrusted message"}}`
		err := cloudHTTPStatusError(&http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
		got, message := generation.PublicGeneratorError("generator_start_failed", err)
		if got != "shotforward_"+code || strings.Contains(message, "untrusted") {
			t.Fatalf("failure: %s %s", got, message)
		}
		if cloudErrorCode(500, []byte(body)) != "" {
			t.Fatal("classified unrelated HTTP status")
		}
	}
	if cloudErrorCode(402, []byte(`{"error":{"code":"unknown","message":"insufficient_points"}}`)) != "" {
		t.Fatal("classified message instead of known code")
	}
}

func TestProxyBusinessFailureIsScopedAndPreservesResponse(t *testing.T) {
	for _, header := range []string{"Thread-ID", "Session-ID", ""} {
		t.Run(header, func(t *testing.T) {
			const body = `{"error":{"code":"negative_balance","message":"balance"}}`
			calls := 0
			manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			manager.accessToken, manager.expiresAt = "test-access", time.Now().Add(time.Hour)
			finish := manager.BeginAgentTurn("thread-1")
			other := manager.BeginAgentTurn("thread-2")
			request := httptest.NewRequest("POST", "/responses", strings.NewReader(`{"prompt_cache_key":"thread-1"}`))
			if header != "" {
				request.Header.Set(header, "thread-1")
			}
			response := httptest.NewRecorder()
			manager.ProxyOpenAI(response, request, "/responses")
			if response.Code != 402 || response.Body.String() != body || calls != 1 {
				t.Fatal("proxy changed response or retried")
			}
			if got := finish(); got != "shotforward_negative_balance" {
				t.Fatalf("code=%q", got)
			}
			if other() != "" || manager.BeginAgentTurn("thread-1")() != "" {
				t.Fatal("failure leaked across turns or threads")
			}
			manager.recordAgentFailure("untracked", "shotforward_negative_balance")
			if len(manager.agentFailures) != 0 {
				t.Fatal("retained untracked failure")
			}
		})
	}
}

func TestOldTurnCleanupDoesNotRemoveNewTurn(t *testing.T) {
	manager := &Manager{}
	old := manager.BeginAgentTurn("thread")
	next := manager.BeginAgentTurn("thread")
	old()
	manager.recordAgentFailure("thread", "shotforward_insufficient_points")
	if next() != "shotforward_insufficient_points" {
		t.Fatal("old subscription cleared current turn")
	}
}
