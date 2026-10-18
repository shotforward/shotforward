package agentsse

import (
	"context"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func TestRetryEventReplaysWithoutEndingTheTurnOrLeakingToOtherSessions(t *testing.T) {
	t.Parallel()
	broker, err := NewBroker(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const session agent.SessionID = "ags_AAAAAAAAAAAAAAAAAAAAAAAAAA"
	const otherSession agent.SessionID = "ags_BBBBBBBBBBBBBBBBBBBBBBBBBB"
	const turn agent.TurnID = "agt_CCCCCCCCCCCCCCCCCCCCCCCCCC"
	cursor, err := broker.Cursor(session)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	other, cancelOther, err := broker.Subscribe(ctx, otherSession, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelOther()
	now := time.Now().UTC().Truncate(time.Millisecond)
	broker.Publish(agentapp.StudioEvent{Type: agentapp.EventTurnRetrying, SessionID: session, TurnID: turn, FailureCode: "runtime_retrying", OccurredAt: now})
	// Malformed retries have no receiving Turn and must not enter replay.
	broker.Publish(agentapp.StudioEvent{Type: agentapp.EventTurnRetrying, SessionID: session, FailureCode: "runtime_retrying", OccurredAt: now})
	events, cancelEvents, err := broker.Subscribe(ctx, session, cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelEvents()
	select {
	case event := <-events:
		if event.Value.Type != agentapp.EventTurnRetrying || event.Value.TurnID != turn || event.ID == cursor {
			t.Fatalf("retry replay = %#v", event)
		}
	default:
		t.Fatal("valid retry was discarded by the event broker")
	}
	broker.Publish(agentapp.StudioEvent{Type: agentapp.EventTurnCompleted, SessionID: session, TurnID: turn, TurnStatus: agent.TurnCompleted, OccurredAt: now})
	select {
	case event, open := <-events:
		if !open || event.Value.Type != agentapp.EventTurnCompleted || event.Value.TurnStatus != agent.TurnCompleted {
			t.Fatalf("retry closed replay or admitted an invalid event: %#v, open=%v", event, open)
		}
	default:
		t.Fatal("retry prevented later completion delivery")
	}
	select {
	case event := <-other:
		t.Fatalf("another session received the retry: %#v", event)
	default:
	}
}
