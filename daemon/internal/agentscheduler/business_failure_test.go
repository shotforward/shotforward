package agentscheduler

import (
	"context"
	"errors"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	"testing"
)

func TestSchedulerPersistsStructuredBusinessFailure(t *testing.T) {
	fixture := newSchedulerFixture(t, 1)
	result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "balance-failure", "Hello"))
	if err != nil {
		t.Fatal(err)
	}
	failure := generation.NewGeneratorFailure("shotforward_negative_balance", errors.New("untrusted upstream message"))
	if err := fixture.scheduler.fail(context.Background(), result.Turn, "codex_turn_failed", failure); err != nil {
		t.Fatal(err)
	}
	turn, err := fixture.store.AgentTurn(context.Background(), result.Turn.ID)
	if err != nil || turn.Failure == nil || turn.Failure.Code != "shotforward_negative_balance" || turn.Failure.Summary == "untrusted upstream message" {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
}
