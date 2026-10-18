package agentscheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func TestTurnSchedulerRetryProgressKeepsWorkActiveAndScopedUntilTerminal(t *testing.T) {
	for _, terminal := range []agent.TurnStatus{agent.TurnCompleted, agent.TurnFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(t, 2)
			// Use the real bounded SSE broker: its Validate boundary must accept
			// the new event, not silently discard progress recorded by a fake.
			broker, err := agentsse.NewBroker(fixture.clock, bytes.NewReader(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			fixture.scheduler.events = broker
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cursor, err := broker.Cursor(fixture.sessions[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			events, stop, err := broker.Subscribe(ctx, fixture.sessions[0].ID, cursor)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			otherCursor, err := broker.Cursor(fixture.sessions[1].ID)
			if err != nil {
				t.Fatal(err)
			}
			otherEvents, stopOther, err := broker.Subscribe(ctx, fixture.sessions[1].ID, otherCursor)
			if err != nil {
				t.Fatal(err)
			}
			defer stopOther()
			queued, err := fixture.scheduler.SubmitInput(ctx, fixture.input(0, "retry-operation", "keep the original request"))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := fixture.scheduler.ProcessNext(ctx); done <- err }()
			started := fixture.runtime.waitStarted(t)
			waitForTurnStatus(t, fixture.store, queued.Turn.ID, agent.TurnRunning)
			before, err := fixture.store.AgentTurn(ctx, queued.Turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range []struct{ thread, turn string }{
				{fixture.sessions[1].CodexThreadID, started.turnID},
				{started.threadID, "other-native-turn"},
				{started.threadID, ""},
			} {
				fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeTurnRetrying, CodexThreadID: scope.thread, CodexTurnID: scope.turn})
			}
			fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{
				Type: agentapp.RuntimeTurnRetrying, CodexThreadID: started.threadID, CodexTurnID: started.turnID,
				FailureCode: "usage_limit_exceeded", FailureSummary: "private upstream credentials", TextDelta: "private body", ToolName: "private tool",
			})
			fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: started.threadID, CodexTurnID: started.turnID, TextDelta: "continued after native retry"})
			retries := 0
			for {
				event := awaitRetryStudioEvent(t, events)
				if event.Value.Type == agentapp.EventTurnRetrying {
					retries++
					if event.Value.SessionID != fixture.sessions[0].ID || event.Value.TurnID != queued.Turn.ID || event.Value.FailureCode != agentapp.RuntimeRetryingFailureCode || event.Value.Validate() != nil {
						t.Fatalf("incorrect retry transport event: %+v", event.Value)
					}
					encoded, err := json.Marshal(event.Value)
					if err != nil {
						t.Fatal(err)
					}
					for _, private := range []string{"private", "usage_limit_exceeded", started.threadID, started.turnID, "retry_attempt"} {
						if strings.Contains(string(encoded), private) {
							t.Fatalf("retry leaked/invented %q", private)
						}
					}
				}
				if event.Value.Type == agentapp.EventTurnCompleted || event.Value.Type == agentapp.EventRuntimeError {
					t.Fatalf("retry prematurely finished work: %+v", event.Value)
				}
				if event.Value.Type == agentapp.EventAssistantDelta {
					break
				}
			}
			if retries != 1 {
				t.Fatalf("foreign/missing native Turn scope contaminated progress: %d retries", retries)
			}
			after, err := fixture.store.AgentTurn(ctx, queued.Turn.ID)
			if err != nil || after.Status != agent.TurnRunning || after.Dispatch != agent.DispatchInflight || after.Revision != before.Revision || after.Failure != nil {
				t.Fatalf("retry mutated durable Turn state: %+v, %v", after, err)
			}
			select {
			case err := <-done:
				t.Fatalf("retry ended scheduler execution: %v", err)
			default:
			}
			if fixture.runtime.startCalls() != 1 || fixture.runtime.interruptCalls() != 0 || fixture.runtime.readCalls() != 0 || fixture.mcp.endCount() != 0 {
				t.Fatal("retry resent/interrupted/reconciled work or released the active MCP binding")
			}
			select {
			case event := <-otherEvents:
				t.Fatalf("retry crossed Sessions: %+v", event.Value)
			default:
			}
			fixture.runtime.complete(started.threadID, started.turnID, terminal)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(schedulerTestTimeout):
				t.Fatal("native terminal did not finish scheduler")
			}
			completed := awaitRetryStudioEvent(t, events)
			if completed.Value.Type != agentapp.EventTurnCompleted || completed.Value.TurnStatus != terminal || completed.Value.TurnID != queued.Turn.ID {
				t.Fatalf("native terminal after retry=%+v", completed.Value)
			}
			stored, err := fixture.store.AgentTurn(ctx, queued.Turn.ID)
			if err != nil || stored.Status != terminal || stored.Dispatch != agent.DispatchTerminal || fixture.mcp.endCount() != 1 || fixture.runtime.startCalls() != 1 {
				t.Fatalf("terminal state after retry=%+v, %v", stored, err)
			}
		})
	}
}

func awaitRetryStudioEvent(t *testing.T, events <-chan agentsse.Event) agentsse.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("SSE broker closed before native completion")
		}
		return event
	case <-time.After(schedulerTestTimeout):
		t.Fatal("scheduler retry progress did not reach the SSE broker")
		return agentsse.Event{}
	}
}
