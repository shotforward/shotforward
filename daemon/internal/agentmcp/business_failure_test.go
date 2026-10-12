package agentmcp

import (
	"github.com/shotforward/shotforward/daemon/internal/agentimage"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	"testing"
)

type businessFailureEvents struct{ events []agentapp.StudioEvent }

func (e *businessFailureEvents) Publish(event agentapp.StudioEvent) {
	e.events = append(e.events, event)
}

func TestConversationImagePublishesValidBusinessFailure(t *testing.T) {
	server, endpoint, credential, _, _ := newMCPFixture(t)
	publisher := &businessFailureEvents{}
	server.events = publisher
	server.images = &recordingImageCommands{err: &agentimage.Failure{Code: "shotforward_insufficient_points", Message: "cannot accept request"}}
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_image", CodexTurnID: "turn_image", RuntimeKind: agentapp.RuntimeShotForward}); err != nil {
		t.Fatal(err)
	}
	result := callTool(t, server, endpoint, credential.Value, "generate_conversation_image", map[string]any{"prompt": "An image"})
	if !result.IsError || len(publisher.events) != 1 {
		t.Fatalf("result=%+v events=%d", result, len(publisher.events))
	}
	event := publisher.events[0]
	if event.Validate() != nil || event.FailureCode != "shotforward_insufficient_points" || event.SessionID != testSessionID || event.TurnID != testTurnID {
		t.Fatalf("invalid event: %+v", event)
	}
}
