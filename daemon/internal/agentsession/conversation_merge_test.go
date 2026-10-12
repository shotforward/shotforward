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

type pagedConversationRuntime struct {
	fakeRuntime
	page agentapp.RuntimeConversationPage
}

func (r *pagedConversationRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return r.page, nil
}

func newConversationService(t *testing.T, store *fakeStore, runtime agentapp.Runtime) *Service {
	t.Helper()
	binding := validBinding()
	store.session, store.binding = validSession(), binding
	store.session.CachedTitle = "Conversation"
	mcp := &fakeMCP{registered: true, scope: agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode}}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: mcp, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func conversationTestTurn(index int, status agent.TurnStatus) (agent.AgentTurn, agent.InputOperation) {
	id := agent.TurnID(fmt.Sprintf("agt_%026d", index))
	at := serviceTime.Add(time.Duration(index) * time.Second)
	turn := agent.AgentTurn{ID: id, AgentSessionID: serviceSession, ClientOperationID: fmt.Sprintf("input-%d", index),
		ModelID: "gpt-test", CodexTurnID: fmt.Sprintf("runtime-%d", index), Status: status, Revision: 1, CreatedAt: at}
	if status == agent.TurnQueued {
		turn.CodexTurnID, turn.Dispatch = "", agent.DispatchQueued
	} else if status.IsTerminal() {
		turn.Dispatch, turn.StartedAt, turn.CompletedAt = agent.DispatchTerminal, &at, &at
	} else {
		turn.Dispatch, turn.StartedAt = agent.DispatchInflight, &at
	}
	input := agent.InputOperation{AgentSessionID: serviceSession, AgentTurnID: id, ClientOperationID: turn.ClientOperationID,
		Action: agent.InputQueueTurn, InputText: fmt.Sprintf("Question %d", index), CreatedAt: at}
	return turn, input
}

func TestConversationShowsPersistedInputBeforeWholeTurnReachesRuntimeHistory(t *testing.T) {
	for _, status := range []agent.TurnStatus{agent.TurnQueued, agent.TurnRunning, agent.TurnWaitingUser, agent.TurnCompleted, agent.TurnFailed, agent.TurnInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			turn, input := conversationTestTurn(1, status)
			store := &fakeStore{turns: []agent.AgentTurn{turn}, inputs: []agent.InputOperation{input}}
			runtime := &pagedConversationRuntime{}
			service := newConversationService(t, store, runtime)
			page, err := service.Conversation(context.Background(), serviceSession, "", 20)
			if err != nil || len(page.Turns) != 1 || len(page.Turns[0].Messages) != 1 {
				t.Fatalf("missing Runtime Turn fallback = %#v, %v", page, err)
			}
			message := page.Turns[0].Messages[0]
			if message.Text != input.InputText || message.ClientOperationID != input.ClientOperationID || message.ID != publicMessageID(serviceSession, "input:"+input.ClientOperationID) {
				t.Fatalf("persisted user message = %#v", message)
			}
			if turn.CodexTurnID == "" {
				store.turns[0].CodexTurnID = "runtime-1"
			}
			runtime.page.Turns = []agentapp.RuntimeConversationTurn{{CodexTurnID: "runtime-1", Messages: []agentapp.RuntimeConversationMessage{
				{ItemID: "runtime-user", Role: agentapp.ConversationUser, Text: input.InputText},
				{ItemID: "runtime-reply", Role: agentapp.ConversationAssistant, Text: "Answer"},
			}}}
			page, err = service.Conversation(context.Background(), serviceSession, "", 20)
			if err != nil || len(page.Turns) != 1 || len(page.Turns[0].Messages) != 2 || page.Turns[0].Messages[0] != message {
				t.Fatalf("Runtime catch-up changed/duplicated persisted message = %#v, %v", page, err)
			}
		})
	}
}

func TestConversationFirstPageFallbackPreservesRuntimePaginationBoundary(t *testing.T) {
	store := &fakeStore{}
	for index := 1; index <= 8; index++ {
		status := agent.TurnCompleted
		if index == 8 {
			status = agent.TurnQueued
		}
		turn, input := conversationTestTurn(index, status)
		store.turns, store.inputs = append(store.turns, turn), append(store.inputs, input)
	}
	runtime := &pagedConversationRuntime{page: agentapp.RuntimeConversationPage{Turns: []agentapp.RuntimeConversationTurn{
		{CodexTurnID: "runtime-5"}, {CodexTurnID: "runtime-7"},
	}, NextCursor: "older-page"}}
	service := newConversationService(t, store, runtime)
	page, err := service.Conversation(context.Background(), serviceSession, "", 2)
	if err != nil || page.NextCursor != "older-page" {
		t.Fatalf("first page = %#v, %v", page, err)
	}
	var got []agent.TurnID
	for _, turn := range page.Turns {
		got = append(got, turn.Turn.ID)
	}
	want := []agent.TurnID{store.turns[4].ID, store.turns[5].ID, store.turns[6].ID, store.turns[7].ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first page turns = %#v, want %#v", got, want)
	}
	// Do not cap/trim Runtime Turns after their cursor is assigned, and do not
	// append latest local Turns again while an older history page is loaded.
	runtime.page.Turns = []agentapp.RuntimeConversationTurn{{CodexTurnID: "runtime-1"}, {CodexTurnID: "runtime-2"}}
	page, err = service.Conversation(context.Background(), serviceSession, "older-page", 2)
	if err != nil || len(page.Turns) != 2 || page.Turns[0].Turn.ID != store.turns[0].ID || page.Turns[1].Turn.ID != store.turns[1].ID || page.NextCursor != "older-page" {
		t.Fatalf("older page repeated latest Turns = %#v, %v", page, err)
	}
	// An entirely empty Runtime page is not permission to return all local
	// history. Only the latest requested window is a reasonable fallback.
	runtime.page = agentapp.RuntimeConversationPage{NextCursor: "unchanged"}
	page, err = service.Conversation(context.Background(), serviceSession, "", 2)
	if err != nil || len(page.Turns) != 2 || page.Turns[0].Turn.ID != store.turns[6].ID || page.Turns[1].Turn.ID != store.turns[7].ID || page.NextCursor != "unchanged" {
		t.Fatalf("empty Runtime first page = %#v, %v", page, err)
	}
	page, err = service.Conversation(context.Background(), serviceSession, "old-empty", 2)
	if err != nil || len(page.Turns) != 0 {
		t.Fatalf("empty older page = %#v, %v", page, err)
	}
}

func TestMergeConversationMessagesKeepsSteeringAfterEarlierRepliesAndBeforeLaterInput(t *testing.T) {
	input := func(operation, text string, action agent.InputAction) agent.InputOperation {
		return agent.InputOperation{ClientOperationID: operation, InputText: text, Action: action}
	}
	user := func(id, text string) agentapp.RuntimeConversationMessage {
		return agentapp.RuntimeConversationMessage{ItemID: id, Role: agentapp.ConversationUser, Text: text}
	}
	reply := func(id, text string) agentapp.RuntimeConversationMessage {
		return agentapp.RuntimeConversationMessage{ItemID: id, Role: agentapp.ConversationAssistant, Text: text}
	}
	cases := []struct {
		name    string
		inputs  []agent.InputOperation
		runtime []agentapp.RuntimeConversationMessage
		want    []string
	}{
		{"missing queued input", []agent.InputOperation{input("queued", "Hi", agent.InputQueueTurn)}, []agentapp.RuntimeConversationMessage{reply("a", "Reply")}, []string{"Hi", "Reply"}},
		{"missing latest steer", []agent.InputOperation{input("queued", "Hi", agent.InputQueueTurn), input("steer", "Continue", agent.InputSteerTurn)}, []agentapp.RuntimeConversationMessage{user("u", "Hi"), reply("a", "Reply")}, []string{"Hi", "Reply", "Continue"}},
		{"missing preceding steer", []agent.InputOperation{input("queued", "Hi", agent.InputQueueTurn), input("steer-1", "First steer", agent.InputSteerTurn), input("steer-2", "Second steer", agent.InputSteerTurn)}, []agentapp.RuntimeConversationMessage{user("u", "Hi"), reply("a", "Reply"), user("u-2", "Second steer"), reply("b", "Later reply")}, []string{"Hi", "Reply", "First steer", "Second steer", "Later reply"}},
		{"identical words are different operations", []agent.InputOperation{input("queued", "Hi", agent.InputQueueTurn), input("steer", "Hi", agent.InputSteerTurn)}, []agentapp.RuntimeConversationMessage{user("u", "Hi"), reply("a", "Reply")}, []string{"Hi", "Reply", "Hi"}},
		{"both identical runtime items", []agent.InputOperation{input("queued", "Hi", agent.InputQueueTurn), input("steer", "Hi", agent.InputSteerTurn)}, []agentapp.RuntimeConversationMessage{user("u", "Hi"), reply("a", "Reply"), user("u-2", "Hi")}, []string{"Hi", "Reply", "Hi"}},
		{"same millisecond queue precedes steer regardless of operation IDs", []agent.InputOperation{input("a-steer", "Continue", agent.InputSteerTurn), input("z-queued", "Hi", agent.InputQueueTurn)}, []agentapp.RuntimeConversationMessage{user("u", "Hi"), reply("a", "Reply")}, []string{"Hi", "Reply", "Continue"}},
		{"legacy history", nil, []agentapp.RuntimeConversationMessage{user("u", "Historical input"), reply("a", "Reply")}, []string{"Historical input", "Reply"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			messages := mergeConversationMessages(serviceSession, test.runtime, test.inputs)
			var got []string
			operations := make(map[string]bool)
			for _, message := range messages {
				got = append(got, message.Text)
				if message.ClientOperationID != "" {
					if operations[message.ClientOperationID] || message.ID != publicMessageID(serviceSession, "input:"+message.ClientOperationID) {
						t.Fatalf("duplicated/unstable operation: %#v", message)
					}
					operations[message.ClientOperationID] = true
				}
			}
			if !reflect.DeepEqual(got, test.want) || len(operations) != len(test.inputs) {
				t.Fatalf("messages = %#v operations = %#v, want %#v", got, operations, test.want)
			}
		})
	}
}

func TestConversationFirstPageIncludesMissingTurnAtSameMillisecondAsBoundary(t *testing.T) {
	missing, missingInput := conversationTestTurn(1, agent.TurnCompleted)
	known, knownInput := conversationTestTurn(2, agent.TurnCompleted)
	missing.CreatedAt, missingInput.CreatedAt = known.CreatedAt, known.CreatedAt
	store := &fakeStore{turns: []agent.AgentTurn{missing, known}, inputs: []agent.InputOperation{missingInput, knownInput}}
	runtime := &pagedConversationRuntime{page: agentapp.RuntimeConversationPage{Turns: []agentapp.RuntimeConversationTurn{{CodexTurnID: known.CodexTurnID}}, NextCursor: "older"}}
	page, err := newConversationService(t, store, runtime).Conversation(context.Background(), serviceSession, "", 1)
	if err != nil || len(page.Turns) != 2 || page.Turns[0].Turn.ID != missing.ID || page.NextCursor != "older" {
		t.Fatalf("same-millisecond first page lost persisted input: %#v, %v", page, err)
	}
}

func TestConversationFallbackDoesNotExposeOtherSessionInputs(t *testing.T) {
	turn, input := conversationTestTurn(1, agent.TurnRunning)
	foreignTurn, foreignInput := conversationTestTurn(2, agent.TurnRunning)
	foreignTurn.AgentSessionID, foreignInput.AgentSessionID = "ags_ZZZZZZZZZZZZZZZZZZZZZZZZZZ", "ags_ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	foreignInput.InputText = "private foreign input"
	wrongScope := input
	wrongScope.AgentSessionID, wrongScope.InputText = foreignTurn.AgentSessionID, "private mismatched input"
	store := &fakeStore{turns: []agent.AgentTurn{turn, foreignTurn}, inputs: []agent.InputOperation{input, foreignInput, wrongScope}}
	runtime := &pagedConversationRuntime{page: agentapp.RuntimeConversationPage{Turns: []agentapp.RuntimeConversationTurn{{CodexTurnID: foreignTurn.CodexTurnID}}}}
	page, err := newConversationService(t, store, runtime).Conversation(context.Background(), serviceSession, "", 20)
	if err != nil || len(page.Turns) != 1 || page.Turns[0].Turn.ID != turn.ID || len(page.Turns[0].Messages) != 1 || page.Turns[0].Messages[0].Text != input.InputText {
		t.Fatalf("scope-safe fallback = %#v, %v", page, err)
	}
}
