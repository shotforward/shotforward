package studio

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

const studioAgentSuffix = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestStudioV1MigratesToLatestSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "studio.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Studio, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:1]); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer store.Close()
	version, err := sharedsqlite.SchemaVersion(ctx, store.database)
	if err != nil || version != len(migrations) {
		t.Fatalf("SchemaVersion() = %d, %v, want %d", version, err, len(migrations))
	}
	for _, table := range []string{"agent_runtime_settings", "agent_sessions", "agent_turns", "agent_input_operations", "agent_mcp_bindings", "generation_provider_profiles"} {
		var count int
		if err := store.database.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count = %d, %v", table, count, err)
		}
	}
}

func TestAgentRuntimeSettingsUseOptimisticRevision(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	ctx := context.Background()
	settings, err := store.RuntimeSettings(ctx)
	if err != nil || settings.Revision != 1 || settings.CodexExecutablePath != "" || settings.ActiveRuntimeKind != "" {
		t.Fatalf("RuntimeSettings() = %#v, %v", settings, err)
	}
	path := filepath.Join(t.TempDir(), "codex")
	updated, err := store.UpdateRuntimeExecutable(ctx, path, settings.Revision, studioAgentTime(1))
	if err != nil || updated.Revision != 2 || updated.CodexExecutablePath != path {
		t.Fatalf("UpdateRuntimeExecutable() = %#v, %v", updated, err)
	}
	if _, err := store.UpdateRuntimeExecutable(ctx, path, 1, studioAgentTime(2)); !errors.Is(err, ErrAgentRevisionConflict) {
		t.Fatalf("stale UpdateRuntimeExecutable() error = %v", err)
	}
	selected, err := store.UpdateActiveRuntime(ctx, "shotforward", updated.Revision, studioAgentTime(2))
	if err != nil || selected.ActiveRuntimeKind != "shotforward" || selected.Revision != updated.Revision+1 {
		t.Fatalf("UpdateActiveRuntime() = %#v, %v", selected, err)
	}
	if _, err := store.UpdateActiveRuntime(ctx, "invalid", selected.Revision, studioAgentTime(3)); err == nil {
		t.Fatal("UpdateActiveRuntime(invalid) succeeded")
	}
}

func TestCreateAgentSessionWithBindingRollsBackBothRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	first := studioAgentSession()
	firstBinding := agent.MCPBinding{ID: agent.MCPBindingID("amb_" + studioAgentSuffix), AgentSessionID: first.ID,
		WorkspacePath: filepath.Join(t.TempDir(), "first"), RouteKey: "binding-route", Status: agent.MCPBindingPreparing,
		Revision: 1, CreatedAt: studioAgentTime(0), UpdatedAt: studioAgentTime(0)}
	if err := store.CreateAgentSessionWithBinding(ctx, first, firstBinding); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = agent.SessionID("ags_ZYXWVUTSRQPONMLKJIHGFEDCBA")
	second.CodexThreadID = "codex-thread-2"
	second.CodexSessionID = "codex-session-2"
	secondBinding := firstBinding
	secondBinding.ID = agent.MCPBindingID("amb_ZYXWVUTSRQPONMLKJIHGFEDCBA")
	secondBinding.AgentSessionID = second.ID
	secondBinding.WorkspacePath = filepath.Join(t.TempDir(), "second")
	if err := store.CreateAgentSessionWithBinding(ctx, second, secondBinding); !errors.Is(err, ErrAgentRecordConflict) {
		t.Fatalf("duplicate binding route error = %v", err)
	}
	if _, err := store.AgentSession(ctx, second.ID); !errors.Is(err, ErrAgentSessionNotFound) {
		t.Fatalf("rolled-back Session lookup error = %v", err)
	}
}

func TestAgentRecordsPersistWithoutMessageContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "studio.db")
	store := newTestStore(t, path)

	session := studioAgentSession()
	if err := store.CreateAgentSession(ctx, session); err != nil {
		t.Fatalf("CreateAgentSession() error = %v", err)
	}
	turn := studioAgentTurn(session.ID)
	if err := store.CreateAgentTurn(ctx, turn); err != nil {
		t.Fatalf("CreateAgentTurn() error = %v", err)
	}
	operation := agent.InputOperation{
		AgentSessionID:     session.ID,
		ClientOperationID:  turn.ClientOperationID,
		RequestFingerprint: strings.Repeat("a", 64),
		Action:             agent.InputQueueTurn,
		AgentTurnID:        turn.ID,
		CreatedAt:          turn.CreatedAt,
	}
	if got, created, err := store.RecordAgentInputOperation(ctx, operation); err != nil || !created || got != operation {
		t.Fatalf("RecordAgentInputOperation() = %#v, %v, %v", got, created, err)
	}
	if _, created, err := store.RecordAgentInputOperation(ctx, operation); err != nil || created {
		t.Fatalf("idempotent RecordAgentInputOperation() = %v, %v", created, err)
	}
	conflict := operation
	conflict.RequestFingerprint = strings.Repeat("b", 64)
	if _, _, err := store.RecordAgentInputOperation(ctx, conflict); !errors.Is(err, ErrAgentInputIdempotencyConflict) {
		t.Fatalf("conflicting RecordAgentInputOperation() error = %v", err)
	}

	binding := agent.MCPBinding{
		ID:             agent.MCPBindingID("amb_" + studioAgentSuffix),
		AgentSessionID: session.ID,
		WorkspacePath:  filepath.Join(t.TempDir(), "workspace"),
		RouteKey:       "binding-route-1",
		Status:         agent.MCPBindingPreparing,
		Revision:       1,
		CreatedAt:      studioAgentTime(0),
		UpdatedAt:      studioAgentTime(0),
	}
	if err := store.CreateAgentMCPBinding(ctx, binding); err != nil {
		t.Fatalf("CreateAgentMCPBinding() error = %v", err)
	}
	ready, err := binding.Transition(agent.MCPBindingReady, studioAgentTime(1), nil)
	if err != nil || store.SaveAgentMCPBinding(ctx, ready, binding.Revision) != nil {
		t.Fatalf("save ready MCP Binding = %v", err)
	}

	dispatching, err := turn.Transition(agent.TurnQueued, agent.Dispatching, studioAgentTime(1), nil)
	if err != nil || store.SaveAgentTurn(ctx, dispatching, turn.Revision) != nil {
		t.Fatalf("save dispatching Turn = %v", err)
	}
	bound, err := dispatching.BindRuntimeTurn("codex-turn-1")
	if err != nil || store.SaveAgentTurn(ctx, bound, dispatching.Revision) != nil {
		t.Fatalf("save bound Turn = %v", err)
	}
	running, err := bound.Transition(agent.TurnRunning, agent.DispatchInflight, studioAgentTime(2), nil)
	if err != nil || store.SaveAgentTurn(ctx, running, bound.Revision) != nil {
		t.Fatalf("save running Turn = %v", err)
	}
	interrupted, err := running.Transition(agent.TurnInterrupted, agent.DispatchTerminal, studioAgentTime(3), nil)
	if err != nil || store.SaveAgentTurn(ctx, interrupted, running.Revision) != nil {
		t.Fatalf("save interrupted Turn = %v", err)
	}
	archived, err := session.Transition(agent.SessionArchived, studioAgentTime(4))
	if err != nil || store.SaveAgentSession(ctx, archived, session.Revision) != nil {
		t.Fatalf("save archived Session = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gotSession, err := store.AgentSession(ctx, session.ID)
	if err != nil || gotSession.Status != agent.SessionArchived {
		t.Fatalf("AgentSession() = %#v, %v", gotSession, err)
	}
	gotTurns, err := store.AgentTurns(ctx, session.ID)
	if err != nil || len(gotTurns) != 1 || gotTurns[0].Status != agent.TurnInterrupted {
		t.Fatalf("AgentTurns() = %#v, %v", gotTurns, err)
	}
	gotBinding, err := store.AgentMCPBinding(ctx, session.ID)
	if err != nil || gotBinding.Status != agent.MCPBindingReady {
		t.Fatalf("AgentMCPBinding() = %#v, %v", gotBinding, err)
	}
	notArchived, err := store.AgentSessions(ctx, session.ProjectID, session.EpisodeID, false)
	if err != nil || len(notArchived) != 0 {
		t.Fatalf("AgentSessions(exclude archived) = %#v, %v", notArchived, err)
	}

	for _, prohibited := range []string{"message", "prompt", "tool_arguments", "tool_result", "raw_json"} {
		var count int
		if err := store.database.QueryRowContext(ctx, `
SELECT count(*) FROM pragma_table_info('agent_turns') WHERE lower(name) = ?`, prohibited).Scan(&count); err != nil || count != 0 {
			t.Fatalf("prohibited column %q count = %d, %v", prohibited, count, err)
		}
	}
}

func TestAgentRepositoryRejectsStaleRevisionAndInvalidStoredData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	session := studioAgentSession()
	if err := store.CreateAgentSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	archived, err := session.Transition(agent.SessionArchived, studioAgentTime(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentSession(ctx, archived, session.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentSession(ctx, archived, session.Revision); !errors.Is(err, ErrAgentRevisionConflict) {
		t.Fatalf("SaveAgentSession(stale) error = %v", err)
	}
	if _, err := store.database.ExecContext(ctx, `
UPDATE agent_sessions SET model_id = ' model ' WHERE id = ?`, session.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentSession(ctx, agent.SessionID("ags_"+studioAgentSuffix)); err == nil {
		t.Fatal("AgentSession(invalid stored data) error = nil")
	}
}

func TestAgentTurnPartialIndexesEnforceSessionCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	session := studioAgentSession()
	if err := store.CreateAgentSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	first := studioAgentTurn(session.ID)
	if err := store.CreateAgentTurn(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = agent.TurnID("agt_BCDEFGHIJKLMNOPQRSTUVWXYZA")
	second.ClientOperationID = "operation-2"
	if err := store.CreateAgentTurn(ctx, second); !errors.Is(err, ErrAgentRecordConflict) {
		t.Fatalf("second queued CreateAgentTurn() error = %v", err)
	}

	firstDispatching, _ := first.Transition(agent.TurnQueued, agent.Dispatching, studioAgentTime(1), nil)
	if err := store.SaveAgentTurn(ctx, firstDispatching, first.Revision); err != nil {
		t.Fatal(err)
	}
	firstBound, _ := firstDispatching.BindRuntimeTurn("codex-turn-1")
	if err := store.SaveAgentTurn(ctx, firstBound, firstDispatching.Revision); err != nil {
		t.Fatal(err)
	}
	firstRunning, _ := firstBound.Transition(agent.TurnRunning, agent.DispatchInflight, studioAgentTime(2), nil)
	if err := store.SaveAgentTurn(ctx, firstRunning, firstBound.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgentTurn(ctx, second); err != nil {
		t.Fatalf("queued beside active CreateAgentTurn() error = %v", err)
	}
	secondDispatching, _ := second.Transition(agent.TurnQueued, agent.Dispatching, studioAgentTime(3), nil)
	if err := store.SaveAgentTurn(ctx, secondDispatching, second.Revision); err != nil {
		t.Fatal(err)
	}
	secondBound, _ := secondDispatching.BindRuntimeTurn("codex-turn-2")
	if err := store.SaveAgentTurn(ctx, secondBound, secondDispatching.Revision); err != nil {
		t.Fatal(err)
	}
	secondRunning, _ := secondBound.Transition(agent.TurnRunning, agent.DispatchInflight, studioAgentTime(4), nil)
	if err := store.SaveAgentTurn(ctx, secondRunning, secondBound.Revision); !errors.Is(err, ErrAgentRecordConflict) {
		t.Fatalf("second active SaveAgentTurn() error = %v", err)
	}
}

func TestAdmitAgentInputAtomicallyQueuesSteersAndDeduplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	session := studioAgentSession()
	if err := store.CreateAgentSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	first := studioAgentTurn(session.ID)
	fingerprint := strings.Repeat("a", 64)
	operation, created, err := store.AdmitAgentInput(ctx, first, fingerprint, "Review current prompts", "Review current prompts")
	if err != nil || !created || operation.Action != agent.InputQueueTurn || operation.AgentTurnID != first.ID {
		t.Fatalf("AdmitAgentInput(queue) = %#v, %v, %v", operation, created, err)
	}
	titled, err := store.AgentSession(ctx, session.ID)
	if err != nil || titled.CachedTitle != "Review current prompts" || titled.Revision != 2 {
		t.Fatalf("first-input Session = %#v, %v", titled, err)
	}
	replayed, created, err := store.AdmitAgentInput(ctx, first, fingerprint, "Review current prompts", "Review current prompts")
	if err != nil || created || replayed != operation {
		t.Fatalf("AdmitAgentInput(replay) = %#v, %v, %v", replayed, created, err)
	}
	if _, _, err := store.AdmitAgentInput(ctx, first, strings.Repeat("b", 64), "Different input", "Different title"); !errors.Is(err, ErrAgentInputIdempotencyConflict) {
		t.Fatalf("AdmitAgentInput(conflict) error = %v", err)
	}
	dispatching, _ := first.Transition(agent.TurnQueued, agent.Dispatching, studioAgentTime(1), nil)
	if err := store.SaveAgentTurn(ctx, dispatching, first.Revision); err != nil {
		t.Fatal(err)
	}
	bound, _ := dispatching.BindRuntimeTurn("codex-turn-admission")
	if err := store.SaveAgentTurn(ctx, bound, dispatching.Revision); err != nil {
		t.Fatal(err)
	}
	running, _ := bound.Transition(agent.TurnRunning, agent.DispatchInflight, studioAgentTime(2), nil)
	if err := store.SaveAgentTurn(ctx, running, bound.Revision); err != nil {
		t.Fatal(err)
	}
	second := studioAgentTurn(session.ID)
	second.ID = agent.TurnID("agt_BCDEFGHIJKLMNOPQRSTUVWXYZA")
	second.ClientOperationID = "operation-2"
	second.CreatedAt = studioAgentTime(3)
	steer, created, err := store.AdmitAgentInput(ctx, second, strings.Repeat("c", 64), "Steer the active turn", "Must not replace title")
	if err != nil || !created || steer.Action != agent.InputSteerTurn || steer.AgentTurnID != first.ID {
		t.Fatalf("AdmitAgentInput(steer) = %#v, %v, %v", steer, created, err)
	}
	inputs, err := store.AgentInputOperations(ctx, session.ID)
	if err != nil || len(inputs) != 2 || inputs[0].InputText != "Review current prompts" || inputs[1].InputText != "Steer the active turn" {
		t.Fatalf("AgentInputOperations() = %#v, %v", inputs, err)
	}
	titled, err = store.AgentSession(ctx, session.ID)
	if err != nil || titled.CachedTitle != "Review current prompts" || titled.Revision != 2 {
		t.Fatalf("steered Session title = %#v, %v", titled, err)
	}
	turns, err := store.AgentTurns(ctx, session.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("AgentTurns() = %#v, %v", turns, err)
	}
}

func TestAdmitAgentInputEnforcesGlobalQueueLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	for index := 0; index <= agent.MaxGlobalQueuedTurns; index++ {
		suffix := strings.Repeat("A", 24) + string(alphabet[index/len(alphabet)]) + string(alphabet[index%len(alphabet)])
		session := studioAgentSession()
		session.ID = agent.SessionID("ags_" + suffix)
		session.ProjectID = project.ProjectID("prj_" + suffix)
		session.EpisodeID = project.EpisodeID("ep_" + suffix)
		session.CodexThreadID = "thread-" + suffix
		session.CodexSessionID = "runtime-session-" + suffix
		if err := store.CreateAgentSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		turn := studioAgentTurn(session.ID)
		turn.ID = agent.TurnID("agt_" + suffix)
		turn.ClientOperationID = "operation-" + suffix
		_, _, err := store.AdmitAgentInput(ctx, turn, strings.Repeat("a", 64), "Queued conversation", "Queued conversation")
		if index < agent.MaxGlobalQueuedTurns && err != nil {
			t.Fatalf("queue %d error = %v", index+1, err)
		}
		if index == agent.MaxGlobalQueuedTurns && !errors.Is(err, agent.ErrGlobalQueueFull) {
			t.Fatalf("queue beyond global limit error = %v", err)
		}
	}
}

func studioAgentSession() agent.AgentSession {
	return agent.AgentSession{
		ID:                agent.SessionID("ags_" + studioAgentSuffix),
		ProjectID:         project.ProjectID("prj_" + studioAgentSuffix),
		EpisodeID:         project.EpisodeID("ep_" + studioAgentSuffix),
		CodexThreadID:     "codex-thread-1",
		CodexSessionID:    "codex-session-1",
		ModelID:           "gpt-test",
		WebSearchDisabled: false,
		Status:            agent.SessionActive,
		Revision:          1,
		CreatedAt:         studioAgentTime(0),
		UpdatedAt:         studioAgentTime(0),
	}
}

func studioAgentTurn(sessionID agent.SessionID) agent.AgentTurn {
	return agent.AgentTurn{
		ID:                agent.TurnID("agt_" + studioAgentSuffix),
		AgentSessionID:    sessionID,
		ModelID:           "gpt-test",
		ClientOperationID: "operation-1",
		Focus: agent.FocusSnapshot{
			SceneID: project.SceneID("scn_" + studioAgentSuffix),
			ClipID:  project.ClipID("clp_" + studioAgentSuffix),
			AssetID: project.AssetID("ast_" + studioAgentSuffix),
		},
		Status:    agent.TurnQueued,
		Dispatch:  agent.DispatchQueued,
		Revision:  1,
		CreatedAt: studioAgentTime(0),
	}
}

func studioAgentTime(seconds int) time.Time {
	return time.Date(2026, time.September, 4, 13, 0, seconds, 0, time.UTC)
}
