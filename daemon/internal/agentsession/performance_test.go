package agentsession

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

type historyCountingRuntime struct {
	fakeRuntime
	historyCalls int
}

func (r *historyCountingRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	r.historyCalls++
	return agentapp.RuntimeConversationPage{}, errors.New("unexpected empty history RPC")
}

func TestEmptyConversationSkipsRuntimeHistoryButKeepsBindingVerification(t *testing.T) {
	store, runtime, mcp := &fakeStore{session: validSession(), binding: validBinding()}, &historyCountingRuntime{}, &fakeMCP{}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: mcp, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Conversation(context.Background(), serviceSession, "", 20)
	if err != nil || page.Turns == nil || len(page.Turns) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page = %#v, %v", page, err)
	}
	if runtime.historyCalls != 0 || !mcp.registered || store.binding.Status != agent.MCPBindingReady ||
		mcp.scope.SessionID != serviceSession || mcp.scope.ProjectID != serviceProject || mcp.scope.EpisodeID != serviceEpisode {
		t.Fatalf("history calls=%d binding verified=%v", runtime.historyCalls, mcp.registered)
	}
}

type selectedRuntime struct {
	fakeRuntime
	selected      agentapp.RuntimeKind
	inspectedKind agentapp.RuntimeKind
	modelKind     agentapp.RuntimeKind
	threadCalls   int
	threadError   error
}

func (r *selectedRuntime) ActiveRuntime(context.Context) (agentapp.RuntimeKind, error) {
	return r.selected, nil
}
func (r *selectedRuntime) SelectRuntime(context.Context, agentapp.RuntimeKind) error { return nil }
func (r *selectedRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return agentapp.RuntimeSnapshot{}, errors.New("must inspect the requested Runtime")
}
func (r *selectedRuntime) Models(context.Context) ([]agentapp.Model, error) {
	return nil, errors.New("must inspect the requested Runtime models")
}
func (r *selectedRuntime) SnapshotFor(_ context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
	r.inspectedKind = kind
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}, nil
}
func (r *selectedRuntime) ModelsFor(_ context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	r.modelKind = kind
	return []agentapp.Model{{ID: "gpt-test", DisplayName: "Test", RuntimeKind: kind}}, nil
}
func (r *selectedRuntime) StartThread(ctx context.Context, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	r.threadCalls++
	if r.threadError != nil {
		return agentapp.Thread{}, r.threadError
	}
	return r.fakeRuntime.StartThread(ctx, request)
}

func TestCreateUsesSelectedRuntimeAndLogsSafePreparationStages(t *testing.T) {
	runtime := &selectedRuntime{selected: agentapp.RuntimeShotForward}
	var output bytes.Buffer
	store := &fakeStore{}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: &fakeMCP{}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}, Logger: slog.New(slog.NewJSONHandler(&output, nil))})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Create(context.Background(), agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test", RuntimeKind: agentapp.RuntimeShotForward})
	if err != nil || session.RuntimeKind != string(agentapp.RuntimeShotForward) || runtime.inspectedKind != agentapp.RuntimeShotForward || runtime.modelKind != agentapp.RuntimeShotForward {
		t.Fatalf("session=%#v runtime=%#v err=%v", session, runtime, err)
	}
	for _, stage := range []string{"runtime_selection", "runtime_snapshot", "models", "workspace", "binding_register", "thread_start_and_scope_verify", "persist"} {
		if !strings.Contains(output.String(), `"stage":"`+stage+`"`) {
			t.Errorf("missing timing stage %q", stage)
		}
	}
	for _, private := range []string{"http://", "route_", "thread-private", "session-private"} {
		if strings.Contains(output.String(), private) {
			t.Errorf("timing log contains private value %q", private)
		}
	}
}

func TestCreateRejectsSwitchedRuntimeBeforePreparingResources(t *testing.T) {
	runtime, store, mcp, workspace := &selectedRuntime{selected: agentapp.RuntimeShotForward}, &fakeStore{}, &fakeMCP{}, &fakeWorkspace{}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: mcp, Workspace: workspace, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test", RuntimeKind: agentapp.RuntimeUserCodex})
	if !errors.Is(err, agentapp.ErrInvalidRuntimeSelection) || runtime.threadCalls != 0 || mcp.registered || workspace.discarded || store.session.ID != "" {
		t.Fatalf("switched Runtime was used: runtime=%#v store=%#v err=%v", runtime, store, err)
	}
}

func TestCreateDoesNotPersistFailedThreadScopeVerification(t *testing.T) {
	runtime, store, mcp, workspace := &selectedRuntime{selected: agentapp.RuntimeUserCodex, threadError: errors.New("get_session_context scope mismatch")}, &fakeStore{}, &fakeMCP{}, &fakeWorkspace{}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: mcp, Workspace: workspace, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test"})
	if err == nil || store.session.ID != "" || !mcp.unregistered || !workspace.discarded || runtime.threadCalls != 1 {
		t.Fatalf("failed Thread was persisted: store=%#v mcp=%#v err=%v", store, mcp, err)
	}
}

type verificationGateRuntime struct {
	fakeRuntime
	entered  chan struct{}
	verified chan struct{}
}

func (r *verificationGateRuntime) StartThread(ctx context.Context, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	close(r.entered)
	select {
	case <-ctx.Done():
		return agentapp.Thread{}, ctx.Err()
	case <-r.verified:
		return r.fakeRuntime.StartThread(ctx, request)
	}
}

type countingCreateStore struct {
	fakeStore
	writes atomic.Int32
}

func (s *countingCreateStore) CreateAgentSessionWithBinding(ctx context.Context, session agent.AgentSession, binding agent.MCPBinding) error {
	s.writes.Add(1)
	return s.fakeStore.CreateAgentSessionWithBinding(ctx, session, binding)
}

func TestCreateCannotPublishSessionBeforeThreadScopeIsVerified(t *testing.T) {
	runtime := &verificationGateRuntime{entered: make(chan struct{}), verified: make(chan struct{})}
	store := &countingCreateStore{}
	service, err := NewService(Options{Store: store, Changes: fakeChangeSets{}, Runtime: runtime, MCP: &fakeMCP{}, Workspace: &fakeWorkspace{}, IDs: staticIDs{}, Clock: staticClock{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, agentapp.CreateSessionInput{ProjectID: serviceProject, EpisodeID: serviceEpisode, ModelID: "gpt-test"})
		done <- err
	}()
	select {
	case <-runtime.entered:
	case <-ctx.Done():
		t.Fatal("thread verification was not reached")
	}
	if store.writes.Load() != 0 {
		t.Fatal("Session persisted before verification completed")
	}
	select {
	case err := <-done:
		t.Fatalf("unverified Session returned: %v", err)
	default:
	}
	close(runtime.verified)
	select {
	case err := <-done:
		if err != nil || store.writes.Load() != 1 {
			t.Fatalf("verified Session writes=%d err=%v", store.writes.Load(), err)
		}
	case <-ctx.Done():
		t.Fatal("verified Session creation did not complete")
	}
}
