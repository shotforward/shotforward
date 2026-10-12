package agentsse

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

const testSessionID = agent.SessionID("ags_AAAAAAAAAAAAAAAAAAAAAAAAAA")

type fakeClock struct{ value time.Time }

func (c *fakeClock) Now() time.Time { return c.value }

func TestBrokerReplaysAfterCursorAndStreamsFutureEvents(t *testing.T) {
	clock := &fakeClock{value: time.UnixMilli(1_700_000_000_000).UTC()}
	broker, err := NewBroker(clock, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	broker.Publish(turnEvent(agentapp.EventTurnQueued, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value))
	firstCursor, err := broker.Cursor(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	clock.value = clock.value.Add(time.Millisecond)
	broker.Publish(turnEvent(agentapp.EventTurnStarted, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value))

	ctx, cancelContext := context.WithCancel(context.Background())
	events, cancel, err := broker.Subscribe(ctx, testSessionID, firstCursor)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	first := <-events
	if first.Value.Type != agentapp.EventTurnStarted || first.ID == firstCursor {
		t.Fatalf("replay = %#v", first)
	}
	clock.value = clock.value.Add(time.Millisecond)
	broker.Publish(turnEvent(agentapp.EventTurnWaitingUser, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value))
	if next := <-events; next.Value.Type != agentapp.EventTurnWaitingUser {
		t.Fatalf("future event = %#v", next)
	}
	cancelContext()
}

func TestBrokerCaptureKeepsSnapshotAndCursorOnOneBoundary(t *testing.T) {
	clock := &fakeClock{value: time.UnixMilli(1_700_000_000_000).UTC()}
	broker, _ := NewBroker(clock, bytes.NewReader(make([]byte, 16)))
	reading := make(chan struct{})
	release := make(chan struct{})
	captured := make(chan string, 1)
	go func() {
		cursor, _ := broker.Capture(testSessionID, func() error {
			close(reading)
			<-release
			return nil
		})
		captured <- cursor
	}()
	<-reading

	published := make(chan struct{})
	go func() {
		event := turnEvent(agentapp.EventTurnCompleted, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value)
		event.TurnStatus = agent.TurnCompleted
		broker.Publish(event)
		close(published)
	}()
	select {
	case <-published:
		t.Fatal("Publish crossed the snapshot/cursor boundary")
	default:
	}
	close(release)
	cursor := <-captured
	<-published

	events, cancel, err := broker.Subscribe(context.Background(), testSessionID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if value := <-events; value.Value.Type != agentapp.EventTurnCompleted {
		t.Fatalf("replayed event = %#v", value)
	}
}

func TestBrokerRequestsResyncForOtherEpochAndExpiredCursor(t *testing.T) {
	clock := &fakeClock{value: time.UnixMilli(1_700_000_000_000).UTC()}
	broker, _ := NewBroker(clock, bytes.NewReader(make([]byte, 16)))
	broker.Publish(turnEvent(agentapp.EventTurnQueued, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value))

	for _, cursor := range []string{"OTHER:1", broker.epoch + ":0"} {
		if cursor == broker.epoch+":0" {
			clock.value = clock.value.Add(ReplayWindow + time.Millisecond)
			// Trigger pruning without adding a replacement event.
			_, _ = broker.Cursor(testSessionID)
		}
		events, _, err := broker.Subscribe(context.Background(), testSessionID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		value, ok := <-events
		if !ok || value.Value.Type != agentapp.EventSessionResyncRequired {
			t.Fatalf("cursor %q event = %#v, open=%v", cursor, value, ok)
		}
		if _, open := <-events; open {
			t.Fatal("resync stream remained open")
		}
	}
}

func TestBrokerCapsSessionReplayAtTwoThousandEvents(t *testing.T) {
	clock := &fakeClock{value: time.UnixMilli(1_700_000_000_000).UTC()}
	broker, _ := NewBroker(clock, bytes.NewReader(make([]byte, 16)))
	for index := 0; index < MaxEventsPerSession+1; index++ {
		value := turnEvent(agentapp.EventTurnQueued, "agt_AAAAAAAAAAAAAAAAAAAAAAAAAA", clock.value)
		value.Summary = fmt.Sprintf("event %d", index)
		broker.Publish(value)
	}
	events, _, err := broker.Subscribe(context.Background(), testSessionID, broker.epoch+":0")
	if err != nil {
		t.Fatal(err)
	}
	if value := <-events; value.Value.Type != agentapp.EventSessionResyncRequired {
		t.Fatalf("event = %#v", value)
	}
}

func turnEvent(kind agentapp.StudioEventType, turnID agent.TurnID, at time.Time) agentapp.StudioEvent {
	return agentapp.StudioEvent{Type: kind, SessionID: testSessionID, TurnID: turnID, OccurredAt: at}
}
