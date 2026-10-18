package agentapp

import (
	"errors"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func TestTurnRetryingEventRequiresScopeAndOnlySafeRetryMetadata(t *testing.T) {
	t.Parallel()
	valid := StudioEvent{
		Type: EventTurnRetrying, SessionID: "ags_AAAAAAAAAAAAAAAAAAAAAAAAAA", TurnID: "agt_BBBBBBBBBBBBBBBBBBBBBBBBBB",
		FailureCode: RuntimeRetryingFailureCode, OccurredAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid retry notification: %v", err)
	}
	for name, change := range map[string]func(*StudioEvent){
		"missing Session":       func(event *StudioEvent) { event.SessionID = "" },
		"missing Turn":          func(event *StudioEvent) { event.TurnID = "" },
		"missing code":          func(event *StudioEvent) { event.FailureCode = "" },
		"provider/account code": func(event *StudioEvent) { event.FailureCode = "usage_limit_exceeded" },
		"raw summary":           func(event *StudioEvent) { event.Summary = "private upstream body" },
		"chat output":           func(event *StudioEvent) { event.TextDelta = "private upstream body" },
		"tool details":          func(event *StudioEvent) { event.ToolName = "private tool" },
		"terminal status":       func(event *StudioEvent) { event.TurnStatus = agent.TurnFailed },
		"ChangeSet":             func(event *StudioEvent) { event.ChangeSetID = "cst_CCCCCCCCCCCCCCCCCCCCCCCCCC" },
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			change(&event)
			if err := event.Validate(); !errors.Is(err, ErrInvalidRuntimeValue) {
				t.Fatalf("invalid retry notification accepted: %+v, %v", event, err)
			}
		})
	}
}
