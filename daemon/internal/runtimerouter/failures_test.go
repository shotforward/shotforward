package runtimerouter

import (
	"context"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	"testing"
	"time"
)

type trackedModels struct {
	fakeModelSource
	thread string
}

func (f *trackedModels) BeginAgentTurn(thread string) func() string {
	f.thread = thread
	return func() string { return "shotforward_negative_balance" }
}
func (f *trackedModels) EndAgentTurn(string) string { return "shotforward_negative_balance" }

type eventRuntime struct {
	fakeRuntime
	events chan agentapp.RuntimeEvent
}

func (r *eventRuntime) Subscribe(context.Context, string) (<-chan agentapp.RuntimeEvent, error) {
	return r.events, nil
}

func TestCloudFailureDecoratesOnlyCloudTurn(t *testing.T) {
	for _, kind := range []agentapp.RuntimeKind{agentapp.RuntimeUserCodex, agentapp.RuntimeShotForward} {
		t.Run(string(kind), func(t *testing.T) {
			runtime := &eventRuntime{events: make(chan agentapp.RuntimeEvent, 1)}
			models := &trackedModels{}
			router, err := NewWithShotForwardModels(runtime, runtime, models)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			events, err := router.Subscribe(ctx, prefix(kind, "thread"))
			if err != nil {
				t.Fatal(err)
			}
			runtime.events <- agentapp.RuntimeEvent{Type: agentapp.RuntimeTurnCompleted, CodexThreadID: "thread", TurnStatus: "failed", FailureCode: "codex_turn_failed"}
			select {
			case event := <-events:
				want := "codex_turn_failed"
				if kind == agentapp.RuntimeShotForward {
					want = "shotforward_negative_balance"
					if models.thread != "thread" {
						t.Fatal("correlation used prefixed thread")
					}
				}
				if event.FailureCode != want {
					t.Fatalf("code=%q want=%q", event.FailureCode, want)
				}
			case <-ctx.Done():
				t.Fatal("no event")
			}
		})
	}
}
