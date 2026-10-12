package agenthttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

// The Runtime stores the full dispatched input, just as Codex does, while the
// public conversation must expose only the persisted original user input.
type greetingHistoryRuntime struct {
	*scriptedRuntime
	inputs map[string]string
}

func (r *greetingHistoryRuntime) StartTurn(_ context.Context, request agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	r.mutex.Lock()
	r.nextTurn++
	id := fmt.Sprintf("greeting-turn-%d", r.nextTurn)
	r.inputs[id] = request.Text
	r.mutex.Unlock()
	r.emit(request.CodexThreadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: request.CodexThreadID, CodexTurnID: id, TextDelta: "Hello!"})
	r.complete(request.CodexThreadID, id, agent.TurnCompleted, "")
	return agentapp.RuntimeTurn{CodexTurnID: id}, nil
}

func (r *greetingHistoryRuntime) ListConversation(_ context.Context, threadID, _ string, _ int) (agentapp.RuntimeConversationPage, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	page := agentapp.RuntimeConversationPage{}
	for _, turn := range r.history[threadID] {
		page.Turns = append(page.Turns, agentapp.RuntimeConversationTurn{
			CodexTurnID: turn.CodexTurnID, Status: turn.Status,
			Messages: []agentapp.RuntimeConversationMessage{
				{ItemID: turn.CodexTurnID + "-user", Role: agentapp.ConversationUser, Text: r.inputs[turn.CodexTurnID]},
				{ItemID: turn.CodexTurnID + "-assistant", Role: agentapp.ConversationAssistant, Phase: agentapp.ConversationPhaseFinal, Text: "Hello!"},
			},
		})
	}
	return page, nil
}

func TestGreetingConversationHidesModelContextEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	var runtime *greetingHistoryRuntime
	fixture := newE2EFixture(t, func(credential agentmcp.Credential, _ string) agentapp.Runtime {
		runtime = &greetingHistoryRuntime{scriptedRuntime: newScriptedRuntime(credential.Value), inputs: make(map[string]string)}
		return runtime
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := fixture.createSession(t, ctx)
	texts := []string{"Hi", "Other"}
	for index, text := range texts {
		payload, err := json.Marshal(map[string]any{
			"client_operation_id": fmt.Sprintf("greeting-input-%d", index), "model_id": fixture.modelID,
			"text": text, "focus": map[string]any{}, "video_prompt_target": e2eVideoPromptContext(),
		})
		if err != nil {
			t.Fatal(err)
		}
		response := fixture.request(t, ctx, http.MethodPost, "/api/v1/agent-sessions/"+sessionID.String()+"/inputs", string(payload))
		if response.Code != http.StatusAccepted {
			t.Fatalf("submit status = %d, %s", response.Code, response.Body.String())
		}
		if processed, err := fixture.scheduler.ProcessNext(ctx); err != nil || !processed {
			t.Fatalf("process = %v, %v", processed, err)
		}
	}
	operations, err := fixture.studioStore.AgentInputOperations(ctx, sessionID)
	if err != nil || len(operations) != len(texts) {
		t.Fatalf("persisted inputs = %#v, %v", operations, err)
	}
	for index, input := range operations {
		if input.InputText != texts[index] {
			t.Fatalf("stored user text = %q, want %q", input.InputText, texts[index])
		}
	}
	for _, text := range texts {
		found := false
		for _, dispatched := range runtime.inputs {
			if dispatched == e2eVideoPromptContext().InstructionText()+"\n\n"+text {
				found = true
			}
		}
		if !found {
			t.Fatalf("Runtime lost model rules for %q", text)
		}
	}
	for reload := 0; reload < 2; reload++ {
		response := fixture.request(t, ctx, http.MethodGet, "/api/v1/agent-sessions/"+sessionID.String()+"/messages", "")
		var page struct {
			Turns []struct {
				Messages []struct {
					Role string `json:"role"`
					Text string `json:"text"`
				} `json:"messages"`
			} `json:"turns"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Turns) != len(texts) {
			t.Fatalf("history = %d, %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), agentmcp.VideoPromptContextInstructionPreamble) {
			t.Fatal("public conversation exposed private model context")
		}
		for index, turn := range page.Turns {
			if len(turn.Messages) != 2 || turn.Messages[0].Role != "user" || turn.Messages[0].Text != texts[index] || turn.Messages[1].Role != "assistant" || turn.Messages[1].Text != "Hello!" {
				t.Fatalf("visible Turn %d = %#v", index, turn)
			}
		}
	}
	if got := fixture.asset(t, ctx, e2eAssetID).Prompt; got != "original prompt" {
		t.Fatalf("greeting modified project: %q", got)
	}
}
