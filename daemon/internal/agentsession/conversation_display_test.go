package agentsession

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

type displayConversationRuntime struct {
	fakeRuntime
	messages []agentapp.RuntimeConversationMessage
}

func (r displayConversationRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{Turns: []agentapp.RuntimeConversationTurn{{
		CodexTurnID: "turn-private", Status: agent.TurnCompleted, Messages: r.messages,
	}}}, nil
}

func TestConversationShowsOriginalUserInputWithoutInternalModelContext(t *testing.T) {
	target := &agentmcp.VideoPromptContext{
		GeneratorID: "video.h3.shotforward", ProviderModelID: "MiniMax-H3-Turbo-Ref2VA",
		KeyframeProviderModelID: "MiniMax-H3-Turbo-FL2VA",
	}
	guidance := target.InstructionText()
	enrichedHi := guidance + "\n\nHi"
	typedGuidance := guidance + "\n\nPlease explain these rules."
	cases := []struct {
		name    string
		inputs  []string
		runtime []string
		want    []string
	}{
		{name: "greeting", inputs: []string{"Hi"}, runtime: []string{enrichedHi}, want: []string{"Hi"}},
		{name: "normal text", inputs: []string{"Hi"}, runtime: []string{"Hi"}, want: []string{"Hi"}},
		{name: "multiline unicode input", inputs: []string{"帮我改提示词\n\n保留对白"}, runtime: []string{guidance + "\n\n帮我改提示词\n\n保留对白"}, want: []string{"帮我改提示词\n\n保留对白"}},
		{name: "repeated greeting while steering", inputs: []string{"Hi", "Hi"}, runtime: []string{enrichedHi, "Hi"}, want: []string{"Hi", "Hi"}},
		{name: "missing runtime input fallback", inputs: []string{"Hi"}, want: []string{"Hi"}},
		{name: "missing steering input fallback", inputs: []string{"Hi", "Continue"}, runtime: []string{enrichedHi}, want: []string{"Hi", "Continue"}},
		{name: "user typed rule text is preserved", inputs: []string{typedGuidance}, runtime: []string{typedGuidance}, want: []string{typedGuidance}},
		{name: "nested user typed rule text is preserved", inputs: []string{enrichedHi}, runtime: []string{guidance + "\n\n" + enrichedHi}, want: []string{enrichedHi}},
		{name: "unmatched runtime text is preserved", inputs: []string{"Hi"}, runtime: []string{guidance + "\n\nOther text"}, want: []string{"Hi", guidance + "\n\nOther text"}},
		{name: "suffix alone is not internal context", inputs: []string{"Hi"}, runtime: []string{"A real user message\n\nHi"}, want: []string{"Hi", "A real user message\n\nHi"}},
		{name: "older turns without stored text are preserved", runtime: []string{"A historical message"}, want: []string{"A historical message"}},
	}
	for _, kind := range []agentapp.RuntimeKind{agentapp.RuntimeUserCodex, agentapp.RuntimeShotForward} {
		for _, test := range cases {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				session := validSession()
				session.RuntimeKind, session.CachedTitle = string(kind), "Greeting"
				completed := serviceTime.Add(time.Second)
				turn := agent.AgentTurn{
					ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession,
					ModelID: "gpt-test", CodexTurnID: "turn-private", ClientOperationID: "input-0",
					Status: agent.TurnCompleted, Dispatch: agent.DispatchTerminal, Revision: 2,
					CreatedAt: serviceTime, StartedAt: &serviceTime, CompletedAt: &completed,
				}
				store := &fakeStore{session: session, binding: validBinding(), turns: []agent.AgentTurn{turn}}
				for index, text := range test.inputs {
					action := agent.InputQueueTurn
					if index > 0 {
						action = agent.InputSteerTurn
					}
					store.inputs = append(store.inputs, agent.InputOperation{
						AgentSessionID: serviceSession, AgentTurnID: turn.ID, Action: action,
						ClientOperationID: fmt.Sprintf("input-%d", index), InputText: text, CreatedAt: serviceTime,
					})
				}
				var messages []agentapp.RuntimeConversationMessage
				for index, text := range test.runtime {
					messages = append(messages, agentapp.RuntimeConversationMessage{ItemID: fmt.Sprintf("user-%d", index), Role: agentapp.ConversationUser, Text: text})
				}
				// Assistant text is not user-input context and must never be stripped.
				messages = append(messages, agentapp.RuntimeConversationMessage{ItemID: "assistant", Role: agentapp.ConversationAssistant, Phase: agentapp.ConversationPhaseFinal, Text: enrichedHi})
				binding := validBinding()
				mcp := &fakeMCP{registered: true, scope: agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode}}
				service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: displayConversationRuntime{messages: messages}, MCP: mcp, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
				if err != nil {
					t.Fatal(err)
				}
				// Both the first load and later history reloads use the same safe mapping.
				for _, cursor := range []string{"", "reload"} {
					page, err := service.Conversation(context.Background(), serviceSession, cursor, 20)
					if err != nil || len(page.Turns) != 1 {
						t.Fatalf("Conversation() = %#v, %v", page, err)
					}
					var got []string
					for _, message := range page.Turns[0].Messages {
						if message.Role == agentapp.ConversationUser {
							got = append(got, message.Text)
						} else if message.Text != enrichedHi || message.Phase != agentapp.ConversationPhaseFinal {
							t.Fatalf("assistant content changed: %#v", message)
						}
					}
					if !reflect.DeepEqual(got, test.want) {
						t.Fatalf("visible user messages = %#v, want %#v", got, test.want)
					}
				}
			})
		}
	}
}
