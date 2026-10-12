package agent

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const testSuffix = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestSessionStateMachine(t *testing.T) {
	t.Parallel()
	session := validSession()
	if err := session.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	historyUnavailable, err := session.Transition(SessionHistoryUnavailable, testAgentTime(1))
	if err != nil {
		t.Fatalf("Transition(history unavailable) error = %v", err)
	}
	restored, err := historyUnavailable.Transition(SessionActive, testAgentTime(2))
	if err != nil || restored.Status != SessionActive || restored.Revision != 3 {
		t.Fatalf("restored Session = %#v, error = %v", restored, err)
	}
	historyUnavailable, err = restored.Transition(SessionHistoryUnavailable, testAgentTime(3))
	if err != nil {
		t.Fatalf("second Transition(history unavailable) error = %v", err)
	}
	archived, err := historyUnavailable.Transition(SessionArchived, testAgentTime(4))
	if err != nil {
		t.Fatalf("Transition(archived) error = %v", err)
	}
	if archived.ArchivedAt == nil || archived.Revision != 5 {
		t.Fatalf("archived Session = %#v", archived)
	}
	if _, err := archived.Transition(SessionActive, testAgentTime(5)); !errors.Is(err, ErrInvalidSessionState) {
		t.Fatalf("Transition(active) error = %v, want ErrInvalidSessionState", err)
	}
}

func TestNewAggregateValidationRejectsPreAdvancedState(t *testing.T) {
	t.Parallel()
	session := validSession()
	if err := session.ValidateNew(); err != nil {
		t.Fatalf("Session ValidateNew() error = %v", err)
	}
	history := session
	history.Status = SessionHistoryUnavailable
	if err := history.ValidateNew(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("history Session ValidateNew() error = %v", err)
	}

	turn := validQueuedTurn()
	if err := turn.ValidateNew(); err != nil {
		t.Fatalf("Turn ValidateNew() error = %v", err)
	}
	turn.CodexTurnID = "pre-bound"
	if err := turn.ValidateNew(); !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("bound Turn ValidateNew() error = %v", err)
	}
}

func TestTurnStateMachineAndRuntimeBinding(t *testing.T) {
	t.Parallel()
	turn := validQueuedTurn()
	dispatching, err := turn.Transition(TurnQueued, Dispatching, testAgentTime(1), nil)
	if err != nil {
		t.Fatalf("Transition(dispatching) error = %v", err)
	}
	bound, err := dispatching.BindRuntimeTurn("codex-turn-1")
	if err != nil {
		t.Fatalf("BindRuntimeTurn() error = %v", err)
	}
	if _, err := bound.BindRuntimeTurn("codex-turn-2"); !errors.Is(err, ErrRuntimeTurnAlreadyBound) {
		t.Fatalf("second BindRuntimeTurn() error = %v", err)
	}
	running, err := bound.Transition(TurnRunning, DispatchInflight, testAgentTime(2), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	waiting, err := running.Transition(TurnWaitingUser, DispatchInflight, testAgentTime(3), nil)
	if err != nil {
		t.Fatalf("Transition(waiting) error = %v", err)
	}
	running, err = waiting.Transition(TurnRunning, DispatchInflight, testAgentTime(4), nil)
	if err != nil {
		t.Fatalf("Transition(resumed running) error = %v", err)
	}
	completed, err := running.Transition(TurnCompleted, DispatchTerminal, testAgentTime(5), nil)
	if err != nil {
		t.Fatalf("Transition(completed) error = %v", err)
	}
	if completed.StartedAt == nil || completed.CompletedAt == nil || completed.Revision != 7 {
		t.Fatalf("completed Turn = %#v", completed)
	}
	if _, err := completed.Transition(TurnFailed, DispatchTerminal, testAgentTime(6), &Failure{Code: "late", Summary: "late"}); !errors.Is(err, ErrInvalidTurnState) {
		t.Fatalf("terminal Transition() error = %v", err)
	}
}

func TestFailedTurnRequiresSafeFailure(t *testing.T) {
	t.Parallel()
	turn := validQueuedTurn()
	failed := turn
	failed.Status = TurnFailed
	failed.Dispatch = DispatchTerminal
	completedAt := testAgentTime(1)
	failed.CompletedAt = &completedAt
	if err := failed.Validate(); !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTurn", err)
	}
	failed.Failure = &Failure{Code: "runtime_disconnected", Summary: "Runtime disconnected"}
	if err := failed.Validate(); err != nil {
		t.Fatalf("valid failed Turn error = %v", err)
	}
}

func TestReplacementValidationRejectsConstructedStateMachineBypass(t *testing.T) {
	t.Parallel()
	turn := validQueuedTurn()
	forged := turn
	forged.Status = TurnCompleted
	forged.Dispatch = DispatchTerminal
	completedAt := testAgentTime(1)
	forged.CompletedAt = &completedAt
	forged.Revision++
	if err := forged.Validate(); err != nil {
		t.Fatalf("forged snapshot should be internally valid: %v", err)
	}
	if err := turn.ValidateReplacement(forged); !errors.Is(err, ErrInvalidTurnState) {
		t.Fatalf("ValidateReplacement(forged) error = %v", err)
	}

	session := validSession()
	forgedSession := session
	forgedSession.Status = SessionArchived
	forgedSession.Revision++
	forgedSession.UpdatedAt = testAgentTime(1)
	forgedSession.ArchivedAt = &forgedSession.UpdatedAt
	if err := session.ValidateReplacement(forgedSession); err != nil {
		t.Fatalf("active to archived replacement error = %v", err)
	}
	forgedSession.Status = SessionActive
	forgedSession.ArchivedAt = nil
	forgedSession.Revision++
	forgedSession.UpdatedAt = testAgentTime(2)
	if err := session.ValidateReplacement(forgedSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("skipped revision replacement error = %v", err)
	}
}

func TestMCPBindingStateMachine(t *testing.T) {
	t.Parallel()
	binding := MCPBinding{
		ID:             MCPBindingID("amb_" + testSuffix),
		AgentSessionID: SessionID("ags_" + testSuffix),
		WorkspacePath:  filepath.Join(t.TempDir(), "workspace"),
		RouteKey:       "route-1",
		Status:         MCPBindingPreparing,
		Revision:       1,
		CreatedAt:      testAgentTime(0),
		UpdatedAt:      testAgentTime(0),
	}
	ready, err := binding.Transition(MCPBindingReady, testAgentTime(1), nil)
	if err != nil {
		t.Fatalf("Transition(ready) error = %v", err)
	}
	if ready.LastVerifiedAt == nil {
		t.Fatal("ready binding has no verification time")
	}
	preparing, err := ready.Transition(MCPBindingPreparing, testAgentTime(2), nil)
	if err != nil {
		t.Fatalf("Transition(preparing) error = %v", err)
	}
	failed, err := preparing.Transition(MCPBindingFailed, testAgentTime(3), &Failure{Code: "mcp_unavailable", Summary: "MCP unavailable"})
	if err != nil || failed.Failure == nil {
		t.Fatalf("Transition(failed) = %#v, %v", failed, err)
	}
	initialReady := binding
	initialReady.Status = MCPBindingReady
	verifiedAt := initialReady.CreatedAt
	initialReady.LastVerifiedAt = &verifiedAt
	if err := initialReady.ValidateNew(); err != nil {
		t.Fatalf("initial ready ValidateNew() error = %v", err)
	}
	initialReady.LastVerifiedAt = nil
	if err := initialReady.ValidateNew(); !errors.Is(err, ErrInvalidMCPBinding) {
		t.Fatalf("unverified initial ready error = %v", err)
	}
}

func validSession() AgentSession {
	return AgentSession{
		ID:             SessionID("ags_" + testSuffix),
		ProjectID:      project.ProjectID("prj_" + testSuffix),
		EpisodeID:      project.EpisodeID("ep_" + testSuffix),
		CodexThreadID:  "codex-thread-1",
		CodexSessionID: "codex-session-1",
		ModelID:        "gpt-test",
		Status:         SessionActive,
		Revision:       1,
		CreatedAt:      testAgentTime(0),
		UpdatedAt:      testAgentTime(0),
	}
}

func validQueuedTurn() AgentTurn {
	return AgentTurn{
		ID:                TurnID("agt_" + testSuffix),
		AgentSessionID:    SessionID("ags_" + testSuffix),
		ModelID:           "gpt-test",
		ClientOperationID: "operation-1",
		Status:            TurnQueued,
		Dispatch:          DispatchQueued,
		Revision:          1,
		CreatedAt:         testAgentTime(0),
	}
}

func testAgentTime(seconds int) time.Time {
	return time.Date(2026, time.September, 4, 12, 0, seconds, 0, time.UTC)
}
