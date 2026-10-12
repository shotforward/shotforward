package agentsession

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	serviceProject = project.ProjectID("prj_AAAAAAAAAAAAAAAAAAAAAAAAAA")
	serviceEpisode = project.EpisodeID("ep_BBBBBBBBBBBBBBBBBBBBBBBBBB")
	serviceSession = agent.SessionID("ags_CCCCCCCCCCCCCCCCCCCCCCCCCC")
	serviceBinding = agent.MCPBindingID("amb_DDDDDDDDDDDDDDDDDDDDDDDDDD")
)

var serviceTime = time.UnixMilli(1_700_000_000_000).UTC()

type staticIDs struct{}

func (staticIDs) NewSessionID() (agent.SessionID, error)       { return serviceSession, nil }
func (staticIDs) NewTurnID() (agent.TurnID, error)             { return "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", nil }
func (staticIDs) NewMCPBindingID() (agent.MCPBindingID, error) { return serviceBinding, nil }

type staticClock struct{}

func (staticClock) Now() time.Time { return serviceTime }

type fakeStore struct {
	session   agent.AgentSession
	binding   agent.MCPBinding
	turns     []agent.AgentTurn
	inputs    []agent.InputOperation
	createErr error
}

func (f *fakeStore) CreateAgentSessionWithBinding(_ context.Context, session agent.AgentSession, binding agent.MCPBinding) error {
	f.session, f.binding = session, binding
	return f.createErr
}
func (f *fakeStore) AgentSession(context.Context, agent.SessionID) (agent.AgentSession, error) {
	return f.session, nil
}
func (f *fakeStore) AgentSessions(context.Context, project.ProjectID, project.EpisodeID, bool) ([]agent.AgentSession, error) {
	return []agent.AgentSession{f.session}, nil
}
func (f *fakeStore) SaveAgentSession(_ context.Context, session agent.AgentSession, _ int64) error {
	f.session = session
	return nil
}
func (f *fakeStore) AgentTurns(context.Context, agent.SessionID) ([]agent.AgentTurn, error) {
	return f.turns, nil
}
func (f *fakeStore) AgentInputOperations(context.Context, agent.SessionID) ([]agent.InputOperation, error) {
	return f.inputs, nil
}
func (f *fakeStore) AgentMCPBinding(context.Context, agent.SessionID) (agent.MCPBinding, error) {
	return f.binding, nil
}
func (f *fakeStore) SaveAgentMCPBinding(_ context.Context, binding agent.MCPBinding, _ int64) error {
	f.binding = binding
	return nil
}

type fakeChangeSets struct{ values []changeset.ChangeSet }

func (f fakeChangeSets) ChangeSetsForAgentSession(context.Context, project.ProjectID, agent.SessionID) ([]changeset.ChangeSet, error) {
	return f.values, nil
}

type fakeMCP struct {
	unregistered bool
	registered   bool
	scope        agentmcp.BindingScope
}

func (f *fakeMCP) GenerateRouteKey() (string, error) {
	return "route_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", nil
}
func (f *fakeMCP) RegisterBinding(_ context.Context, scope agentmcp.BindingScope) (string, error) {
	f.registered, f.scope = true, scope
	return "http://127.0.0.1:1234/mcp/route", nil
}
func (f *fakeMCP) EnsureBinding(ctx context.Context, scope agentmcp.BindingScope) (string, error) {
	if f.BindingRegistered(scope) {
		return "http://127.0.0.1:1234/mcp/route", nil
	}
	return f.RegisterBinding(ctx, scope)
}
func (f *fakeMCP) UnregisterBinding(context.Context, agent.SessionID) error {
	f.unregistered = true
	return nil
}
func (f *fakeMCP) BindingRegistered(scope agentmcp.BindingScope) bool {
	return f.registered && f.scope == scope
}

type fakeWorkspace struct{ discarded bool }

func (f *fakeWorkspace) Prepare(context.Context, agent.SessionID) (string, error) {
	return serviceWorkspacePath(), nil
}
func (f *fakeWorkspace) Restore(_ context.Context, _ agent.SessionID, path string) error {
	if path != serviceWorkspacePath() {
		return ErrInvalidRequest
	}
	return nil
}
func (f *fakeWorkspace) Discard(string) error { f.discarded = true; return nil }

type fakeRuntime struct{}

func (fakeRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}, nil
}
func (fakeRuntime) Models(context.Context) ([]agentapp.Model, error) {
	return []agentapp.Model{{ID: "gpt-test", DisplayName: "Test"}}, nil
}
func (fakeRuntime) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, nil
}
func (fakeRuntime) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{CodexThreadID: "thread-private", CodexSessionID: "session-private", Title: "New Session", ModelID: "gpt-test"}, nil
}
func (fakeRuntime) ResumeThread(_ context.Context, threadID string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{CodexThreadID: threadID, CodexSessionID: "session-private", ModelID: request.ModelID}, nil
}
func (fakeRuntime) ReadThread(context.Context, string) (agentapp.Thread, error) {
	return agentapp.Thread{}, nil
}
func (fakeRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, nil
}
func (fakeRuntime) StartTurn(context.Context, agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	return agentapp.RuntimeTurn{}, nil
}
func (fakeRuntime) SteerTurn(context.Context, string, string, string) error { return nil }
func (fakeRuntime) InterruptTurn(context.Context, string, string) error     { return nil }
func (fakeRuntime) Subscribe(context.Context, string) (<-chan agentapp.RuntimeEvent, error) {
	return make(chan agentapp.RuntimeEvent), nil
}

type conversationRuntime struct{ fakeRuntime }

func (conversationRuntime) ListConversation(_ context.Context, threadID, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	if threadID != "thread-private" || cursor != "page-2" || limit != 20 {
		return agentapp.RuntimeConversationPage{}, errors.New("unexpected conversation request")
	}
	return agentapp.RuntimeConversationPage{Turns: []agentapp.RuntimeConversationTurn{
		{CodexTurnID: "turn-private", Status: agent.TurnCompleted, Messages: []agentapp.RuntimeConversationMessage{
			{ItemID: "runtime-agent-id", Role: agentapp.ConversationAssistant, Phase: agentapp.ConversationPhaseFinal, Text: "Answer"},
		}},
		{CodexTurnID: "foreign-turn", Status: agent.TurnCompleted, Messages: []agentapp.RuntimeConversationMessage{{ItemID: "foreign", Role: agentapp.ConversationAssistant, Text: "Do not expose"}}},
	}, NextCursor: "page-3"}, nil
}

type titledRuntime struct{ fakeRuntime }

func (titledRuntime) ReadThread(context.Context, string) (agentapp.Thread, error) {
	return agentapp.Thread{Title: "## 修复会话标题"}, nil
}

type unavailableRuntime struct{ fakeRuntime }

func (unavailableRuntime) ResumeThread(context.Context, string, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New("persisted Thread is unavailable")
}

func (unavailableRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, errors.New("conversation history is unavailable")
}

type missingThreadRuntime struct{ fakeRuntime }

func (missingThreadRuntime) ResumeThread(context.Context, string, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.Join(agentapp.ErrThreadUnavailable, errors.New("thread not found"))
}

func TestCreatePersistsSessionAndBindingAsOneApplicationOperation(t *testing.T) {
	store, mcp, workspace := &fakeStore{}, &fakeMCP{}, &fakeWorkspace{}
	service := newTestService(t, store, fakeChangeSets{}, mcp, workspace)
	value, err := service.Create(context.Background(), agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test", WebSearchDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.ID != serviceSession || store.binding.AgentSessionID != value.ID || store.binding.Status != agent.MCPBindingReady || store.binding.LastVerifiedAt == nil || !value.WebSearchDisabled {
		t.Fatalf("session=%#v binding=%#v", value, store.binding)
	}
	if workspace.discarded || mcp.unregistered {
		t.Fatal("successful Session resources were cleaned up")
	}
}

func TestCreateCleansBindingAndWorkspaceWhenPersistenceFails(t *testing.T) {
	store, mcp, workspace := &fakeStore{createErr: errors.New("write failed")}, &fakeMCP{}, &fakeWorkspace{}
	service := newTestService(t, store, fakeChangeSets{}, mcp, workspace)
	if _, err := service.Create(context.Background(), agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test"}); err == nil {
		t.Fatal("Create() error = nil")
	}
	if !workspace.discarded || !mcp.unregistered {
		t.Fatalf("discarded=%v unregistered=%v", workspace.discarded, mcp.unregistered)
	}
}

func TestSnapshotResumesTerminalSessionFromPriorDaemon(t *testing.T) {
	store := &fakeStore{session: validSession(), binding: validBinding()}
	mcp := &fakeMCP{}
	service := newTestService(t, store, fakeChangeSets{}, mcp, &fakeWorkspace{})

	snapshot, err := service.Snapshot(context.Background(), serviceSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.Status != agent.SessionActive || store.session.Status != agent.SessionActive || !mcp.registered {
		t.Fatalf("snapshot=%#v stored=%#v", snapshot, store.session)
	}
	if store.binding.Status != agent.MCPBindingReady || store.binding.Revision != 3 {
		t.Fatalf("restored binding=%#v", store.binding)
	}
}

func TestSnapshotClassifiesPersistedThreadRestoreFailureAsRuntimeUnavailable(t *testing.T) {
	store := &fakeStore{session: validSession(), binding: validBinding()}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: unavailableRuntime{}, MCP: &fakeMCP{}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Snapshot(context.Background(), serviceSession); !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatalf("Snapshot() error = %v, want ErrRuntimeNotReady", err)
	}
}

func TestSnapshotReturnsPersistedFailureWhenThreadIsUnavailable(t *testing.T) {
	completedAt := serviceTime
	failedTurn := agent.AgentTurn{ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession, ModelID: "gpt-test",
		ClientOperationID: "input", Status: agent.TurnFailed, Dispatch: agent.DispatchTerminal,
		Failure:  &agent.Failure{Code: "runtime_session_unavailable", Summary: "Agent Runtime session is unavailable"},
		Revision: 2, CreatedAt: serviceTime, CompletedAt: &completedAt}
	if err := failedTurn.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{session: validSession(), binding: validBinding(), turns: []agent.AgentTurn{failedTurn}}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: missingThreadRuntime{}, MCP: &fakeMCP{}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.Snapshot(context.Background(), serviceSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].Status != agent.TurnFailed {
		t.Fatalf("Snapshot() turns = %#v", snapshot.Turns)
	}
	if snapshot.Session.Status != agent.SessionHistoryUnavailable || store.session.Status != agent.SessionHistoryUnavailable {
		t.Fatalf("Snapshot() session = %#v, stored = %#v", snapshot.Session, store.session)
	}
	if store.binding.Status != agent.MCPBindingFailed || store.binding.Failure == nil || store.binding.Failure.Code != "thread_unavailable" {
		t.Fatalf("stored binding = %#v", store.binding)
	}
}

func TestListLeavesHistoricalSessionsLazy(t *testing.T) {
	session, err := validSession().Transition(agent.SessionHistoryUnavailable, serviceTime)
	if err != nil {
		t.Fatal(err)
	}
	mcp := &fakeMCP{}
	service := newTestService(t, &fakeStore{session: session, binding: validBinding()}, fakeChangeSets{}, mcp, &fakeWorkspace{})

	sessions, err := service.List(context.Background(), serviceProject, serviceEpisode, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != agent.SessionHistoryUnavailable || mcp.registered {
		t.Fatalf("sessions=%#v MCP registered=%v", sessions, mcp.registered)
	}
}

func TestSnapshotBackfillsDefaultTitleFromPersistedThread(t *testing.T) {
	completed := serviceTime.Add(time.Second)
	session := validSession()
	session.CachedTitle = "New Session"
	binding := validBinding()
	scope := agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode}
	store := &fakeStore{session: session, binding: binding, turns: []agent.AgentTurn{{
		ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession, ModelID: "gpt-test", ClientOperationID: "input",
		Status: agent.TurnCompleted, Dispatch: agent.DispatchTerminal, Revision: 2, CreatedAt: serviceTime,
		StartedAt: &serviceTime, CompletedAt: &completed,
	}}}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: titledRuntime{}, MCP: &fakeMCP{registered: true, scope: scope}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.Snapshot(context.Background(), serviceSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.CachedTitle != "修复会话标题" || store.session.CachedTitle != "修复会话标题" || store.session.Revision != 2 {
		t.Fatalf("snapshot=%#v stored=%#v", snapshot.Session, store.session)
	}
}

func TestSnapshotReactivatesSessionDowngradedByPriorBehavior(t *testing.T) {
	session, err := validSession().Transition(agent.SessionHistoryUnavailable, serviceTime)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{session: session, binding: validBinding()}
	service := newTestService(t, store, fakeChangeSets{}, &fakeMCP{}, &fakeWorkspace{})

	snapshot, err := service.Snapshot(context.Background(), serviceSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.Status != agent.SessionActive || store.session.Status != agent.SessionActive || store.session.Revision != 3 {
		t.Fatalf("snapshot=%#v stored=%#v", snapshot, store.session)
	}
}

func TestSnapshotKeepsSessionActiveWhenBindingBelongsToCurrentDaemon(t *testing.T) {
	binding := validBinding()
	scope := agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode}
	store := &fakeStore{session: validSession(), binding: binding}
	service := newTestService(t, store, fakeChangeSets{}, &fakeMCP{registered: true, scope: scope}, &fakeWorkspace{})

	snapshot, err := service.Snapshot(context.Background(), serviceSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.Status != agent.SessionActive || store.session.Status != agent.SessionActive {
		t.Fatalf("snapshot=%#v stored=%#v", snapshot, store.session)
	}
}

func TestSnapshotRejectsChangeSetOutsideSessionScope(t *testing.T) {
	store := &fakeStore{session: validSession()}
	foreign := validPendingChangeSet()
	foreign.EpisodeID = "ep_ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	service := newTestService(t, store, fakeChangeSets{values: []changeset.ChangeSet{foreign}}, &fakeMCP{}, &fakeWorkspace{})
	if _, err := service.Snapshot(context.Background(), serviceSession); err == nil {
		t.Fatal("Snapshot() error = nil")
	}
}

func TestConversationMapsOnlySessionTurnsAndHidesRuntimeItemIDs(t *testing.T) {
	completed := serviceTime.Add(time.Second)
	store := &fakeStore{session: validSession(), binding: validBinding(), turns: []agent.AgentTurn{{
		ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession, ModelID: "gpt-test", CodexTurnID: "turn-private", ClientOperationID: "input",
		Status: agent.TurnCompleted, Dispatch: agent.DispatchTerminal, Revision: 2, CreatedAt: serviceTime, StartedAt: &serviceTime, CompletedAt: &completed,
	}}, inputs: []agent.InputOperation{{
		AgentSessionID: serviceSession, ClientOperationID: "input", RequestFingerprint: strings.Repeat("a", 64), InputText: "Question",
		Action: agent.InputQueueTurn, AgentTurnID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", CreatedAt: serviceTime,
	}}}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: conversationRuntime{}, MCP: &fakeMCP{}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Conversation(context.Background(), serviceSession, "page-2", 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "page-3" || len(page.Turns) != 1 || len(page.Turns[0].Messages) != 2 {
		t.Fatalf("Conversation() = %#v", page)
	}
	if page.Turns[0].Messages[0].Role != agentapp.ConversationUser || page.Turns[0].Messages[0].Text != "Question" {
		t.Fatalf("fallback user message = %#v", page.Turns[0].Messages[0])
	}
	for _, message := range page.Turns[0].Messages {
		if message.ID == "runtime-user-id" || message.ID == "runtime-agent-id" || len(message.ID) != len("agm_")+32 {
			t.Fatalf("public message ID = %q", message.ID)
		}
	}
}

func TestConversationClassifiesHistoryFailureAsRuntimeUnavailable(t *testing.T) {
	binding := validBinding()
	scope := agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode}
	completedAt := serviceTime.Add(time.Second)
	store := &fakeStore{session: validSession(), binding: binding, turns: []agent.AgentTurn{{
		ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession, ModelID: "gpt-test", CodexTurnID: "turn-private", ClientOperationID: "input",
		Status: agent.TurnCompleted, Dispatch: agent.DispatchTerminal, Revision: 2, CreatedAt: serviceTime, StartedAt: &serviceTime, CompletedAt: &completedAt,
	}}}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: unavailableRuntime{}, MCP: &fakeMCP{registered: true, scope: scope}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Conversation(context.Background(), serviceSession, "", 20); !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatalf("Conversation() error = %v, want ErrRuntimeNotReady", err)
	}
}

func TestArchiveRejectsNonTerminalTurn(t *testing.T) {
	store := &fakeStore{session: validSession(), turns: []agent.AgentTurn{{ID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", AgentSessionID: serviceSession, ModelID: "gpt-test",
		ClientOperationID: "input", Status: agent.TurnQueued, Dispatch: agent.DispatchQueued, Revision: 1, CreatedAt: serviceTime}}}
	service := newTestService(t, store, fakeChangeSets{}, &fakeMCP{}, &fakeWorkspace{})
	if _, err := service.Archive(context.Background(), serviceSession, 1); !errors.Is(err, ErrSessionHasActiveTurn) {
		t.Fatalf("Archive() error = %v", err)
	}
}

func newTestService(t *testing.T, store *fakeStore, changes fakeChangeSets, mcp *fakeMCP, workspace *fakeWorkspace) *Service {
	t.Helper()
	service, err := NewService(Options{Store: store, Changes: changes, Runtime: fakeRuntime{}, MCP: mcp, Workspace: workspace, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validSession() agent.AgentSession {
	return agent.AgentSession{ID: serviceSession, ProjectID: serviceProject, EpisodeID: serviceEpisode, CodexThreadID: "thread-private", CodexSessionID: "session-private",
		ModelID: "gpt-test", Status: agent.SessionActive, Revision: 1, CreatedAt: serviceTime, UpdatedAt: serviceTime}
}

func validBinding() agent.MCPBinding {
	at := serviceTime
	return agent.MCPBinding{ID: serviceBinding, AgentSessionID: serviceSession, WorkspacePath: serviceWorkspacePath(),
		RouteKey: "route_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", Status: agent.MCPBindingReady, Revision: 1, LastVerifiedAt: &at, CreatedAt: at, UpdatedAt: at}
}

func serviceWorkspacePath() string {
	return filepath.Join(os.TempDir(), serviceSession.String())
}

func validPendingChangeSet() changeset.ChangeSet {
	at := serviceTime
	return changeset.ChangeSet{ID: "cst_FFFFFFFFFFFFFFFFFFFFFFFFFF", ProjectID: serviceProject, EpisodeID: serviceEpisode,
		Source: changeset.Source{AgentSessionID: serviceSession, AgentTurnID: "agt_EEEEEEEEEEEEEEEEEEEEEEEEEE", CodexThreadID: "thread-private", CodexTurnID: "turn-private"},
		Title:  "Proposal", Status: changeset.StatusPending, OperationCount: 1, Revision: 2, CreatedAt: at, UpdatedAt: at, FinalizedAt: &at}
}
