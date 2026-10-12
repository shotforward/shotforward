package agenthttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

// Simulate Runtime history catching up after the locally persisted Turn. This
// fixture never contacts a model or writes to the user's projects.
type delayedGreetingHistoryRuntime struct {
	*greetingHistoryRuntime
	historyVisible atomic.Bool
}

func (r *delayedGreetingHistoryRuntime) ListConversation(ctx context.Context, threadID, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	if !r.historyVisible.Load() {
		return agentapp.RuntimeConversationPage{}, nil
	}
	return r.greetingHistoryRuntime.ListConversation(ctx, threadID, cursor, limit)
}

func TestConversationRetainsPersistedInputUntilRuntimeHistoryCatchesUpEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	var runtime *delayedGreetingHistoryRuntime
	fixture := newE2EFixture(t, func(credential agentmcp.Credential, _ string) agentapp.Runtime {
		runtime = &delayedGreetingHistoryRuntime{
			greetingHistoryRuntime: &greetingHistoryRuntime{
				scriptedRuntime: newScriptedRuntime(credential.Value), inputs: make(map[string]string),
			},
		}
		return runtime
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := fixture.createSession(t, ctx)
	const operationID = "persisted-before-runtime-history"
	const inputText = "继续提取布布和一二的资产"
	payload, err := json.Marshal(map[string]any{
		"client_operation_id": operationID, "model_id": fixture.modelID, "text": inputText,
		"focus": map[string]any{}, "video_prompt_target": e2eVideoPromptContext(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.request(t, ctx, http.MethodPost, "/api/v1/agent-sessions/"+sessionID.String()+"/inputs", string(payload))
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit = %d, %s", response.Code, response.Body.String())
	}
	type message struct {
		ID                string `json:"id"`
		ClientOperationID string `json:"client_operation_id"`
		Role              string `json:"role"`
		Text              string `json:"text"`
	}
	var stableMessageID string
	readConversation := func(wantStatus string, wantAssistant bool) {
		t.Helper()
		response := fixture.request(t, ctx, http.MethodGet, "/api/v1/agent-sessions/"+sessionID.String()+"/messages", "")
		var page struct {
			Turns []struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
				Messages []message `json:"messages"`
			} `json:"turns"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Turns) != 1 {
			t.Fatalf("history = %d, %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), agentmcp.VideoPromptContextInstructionPreamble) {
			t.Fatal("public conversation exposed private model rules")
		}
		turn := page.Turns[0]
		wantMessages := 1
		if wantAssistant {
			wantMessages = 2
		}
		if turn.Turn.Status != wantStatus || len(turn.Messages) != wantMessages {
			t.Fatalf("turn = %#v, want status %q and %d messages", turn, wantStatus, wantMessages)
		}
		user := turn.Messages[0]
		if user.Role != "user" || user.Text != inputText || user.ClientOperationID != operationID || !strings.HasPrefix(user.ID, "agm_") {
			t.Fatalf("persisted user message = %#v", user)
		}
		if stableMessageID == "" {
			stableMessageID = user.ID
		} else if user.ID != stableMessageID {
			t.Fatalf("message identity changed: %q -> %q", stableMessageID, user.ID)
		}
		if wantAssistant && (turn.Messages[1].Role != "assistant" || turn.Messages[1].Text != "Hello!") {
			t.Fatalf("runtime assistant = %#v", turn.Messages[1])
		}
	}
	// Multiple fresh HTTP reads model reloading the UI while the Turn is queued.
	readConversation("queued", false)
	readConversation("queued", false)
	if processed, err := fixture.scheduler.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process = %v, %v", processed, err)
	}
	// Even a completed Turn may briefly be absent from Runtime history.
	readConversation("completed", false)
	runtime.historyVisible.Store(true)
	readConversation("completed", true)
	readConversation("completed", true)
	if got := fixture.asset(t, ctx, e2eAssetID).Prompt; got != "original prompt" {
		t.Fatalf("conversation read modified project: %q", got)
	}
}
