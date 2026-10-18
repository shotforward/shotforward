package codexruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

// These fields come from ErrorNotification in the bundled 0.154.0 app-server's
// generated JSON schema: error, threadId, turnId and willRetry are required.
func TestNormalizeRecoverableNativeErrorIsSafeAndNonTerminal(t *testing.T) {
	t.Parallel()
	for _, info := range []any{nil, "rateLimitExceeded", "usageLimitExceeded", map[string]any{"responseStreamDisconnected": map[string]any{"httpStatusCode": 504}}} {
		params, err := json.Marshal(map[string]any{
			"threadId": "thread-retry", "turnId": "turn-retry", "willRetry": true,
			"error": map[string]any{"message": "private upstream message Bearer private-token", "additionalDetails": "private account details", "codexErrorInfo": info},
		})
		if err != nil {
			t.Fatal(err)
		}
		event, ok := normalizeEvent(rpcNotification{Method: "error", Params: params})
		if !ok || event.Type != agentapp.RuntimeTurnRetrying || event.CodexThreadID != "thread-retry" || event.CodexTurnID != "turn-retry" || event.FailureCode != agentapp.RuntimeRetryingFailureCode {
			t.Fatalf("recoverable native error = %+v, %v", event, ok)
		}
		if event.TurnStatus != "" || event.FailureSummary != "" || event.TextDelta != "" || event.ToolName != "" || event.ItemID != "" || event.GeneratedImagePath != "" || event.OccurredAt.IsZero() {
			t.Fatalf("retry included terminal/private metadata: %+v", event)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private", "usageLimitExceeded", "rateLimitExceeded", "504", "retry_attempt"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("retry leaked or invented metadata %q", private)
			}
		}
	}
}

func TestNormalizeNativeErrorRequiresExplicitRetryAndNativeScope(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"threadId":"thread","turnId":"turn","willRetry":false,"error":{"message":"terminal native error"}}`,
		`{"threadId":"thread","turnId":"turn","error":{"message":"not explicitly retryable"}}`,
		`{"threadId":"thread","turnId":"turn","willRetry":"true","error":{"message":"wrong type"}}`,
		`{"threadId":"thread","willRetry":true,"error":{"message":"missing Turn"}}`,
		`{"turnId":"turn","willRetry":true,"error":{"message":"missing Thread"}}`,
		`{"threadId":"thread","turnId":"turn","willRetry":true}`,
		`{"threadId":"thread","turnId":"turn","willRetry":true,"error":null}`,
		`{"threadId":"thread","turnId":"turn","willRetry":true,"error":{"message":null}}`,
		`{"threadId":"thread","turnId":"turn","willRetry":true,"error":{}}`,
		`{"threadId":`,
	} {
		if event, ok := normalizeEvent(rpcNotification{Method: "error", Params: json.RawMessage(payload)}); ok {
			t.Fatalf("invalid/non-retryable native error normalized: %+v", event)
		}
	}
	terminal, ok := normalizeEvent(rpcNotification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"message":"native retries exhausted"}}}`)})
	if !ok || terminal.Type != agentapp.RuntimeTurnCompleted || terminal.TurnStatus != agent.TurnFailed {
		t.Fatalf("real terminal failure no longer completes its Turn: %+v, %v", terminal, ok)
	}
}

func TestSupervisorRetryNotificationIsThreadScopedAndDoesNotReleaseActiveTurn(t *testing.T) {
	t.Parallel()
	clients := []*fakeProtocolClient{}
	supervisor, err := newSupervisor(Options{WorkingDirectory: filepath.Clean(t.TempDir())}, fakeDependencies(&clients))
	if err != nil {
		t.Fatal(err)
	}
	supervisor.activeTurns["thread-a"] = "turn-a"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eventsA, err := supervisor.Subscribe(ctx, "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	eventsB, err := supervisor.Subscribe(ctx, "thread-b")
	if err != nil {
		t.Fatal(err)
	}
	notify := func(method, params string) {
		supervisor.onNotification(rpcNotification{Method: method, Params: json.RawMessage(params)})
	}
	notify("error", `{"threadId":"thread-a","turnId":"turn-a","willRetry":true,"error":{"message":"private reconnect detail"}}`)
	select {
	case event := <-eventsA:
		if event.Type != agentapp.RuntimeTurnRetrying || event.CodexTurnID != "turn-a" {
			t.Fatalf("retry event=%+v", event)
		}
	default:
		t.Fatal("retry was not routed to its Thread")
	}
	select {
	case event := <-eventsB:
		t.Fatalf("retry crossed Threads: %+v", event)
	default:
	}
	supervisor.mu.Lock()
	active := supervisor.activeTurns["thread-a"]
	supervisor.mu.Unlock()
	if active != "turn-a" {
		t.Fatal("retry released or changed the active Turn")
	}
	notify("error", `{"threadId":"thread-a","turnId":"turn-a","willRetry":false,"error":{"message":"native terminal error before completion"}}`)
	select {
	case event := <-eventsA:
		t.Fatalf("standalone non-retry error terminated the Turn: %+v", event)
	default:
	}
	notify("turn/completed", `{"threadId":"thread-a","turn":{"id":"turn-a","status":"failed"}}`)
	select {
	case event := <-eventsA:
		if event.Type != agentapp.RuntimeTurnCompleted || event.TurnStatus != agent.TurnFailed {
			t.Fatalf("terminal event=%+v", event)
		}
	default:
		t.Fatal("terminal completion was lost after retry")
	}
	supervisor.mu.Lock()
	active = supervisor.activeTurns["thread-a"]
	supervisor.mu.Unlock()
	if active != "" {
		t.Fatal("terminal completion retained the active Turn")
	}
}

func TestSupervisorCompactionRetryDoesNotBecomeConversationProgress(t *testing.T) {
	t.Parallel()
	clients := []*fakeProtocolClient{}
	supervisor, err := newSupervisor(Options{WorkingDirectory: filepath.Clean(t.TempDir())}, fakeDependencies(&clients))
	if err != nil {
		t.Fatal(err)
	}
	supervisor.compactions["thread"] = &contextCompaction{done: make(chan struct{}), turnID: "maintenance"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := supervisor.Subscribe(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	supervisor.onNotification(rpcNotification{Method: "error", Params: json.RawMessage(`{"threadId":"thread","turnId":"maintenance","willRetry":true,"error":{"message":"private retry"}}`)})
	select {
	case event := <-events:
		t.Fatalf("maintenance retry leaked to conversation: %+v", event)
	default:
	}
	if !supervisor.ContextCompactionInProgress("thread") {
		t.Fatal("retry ended native maintenance")
	}
}
