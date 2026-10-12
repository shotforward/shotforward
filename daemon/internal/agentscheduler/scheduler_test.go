package agentscheduler

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

const schedulerTestTimeout = 10 * time.Second

func TestTurnSchedulerDispatchesIdempotentInput(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	input := fixture.input(0, "operation-1", "Review the current prompts")
	first, err := fixture.scheduler.SubmitInput(context.Background(), input)
	if err != nil || first.Action != agent.InputQueueTurn || first.Replay {
		t.Fatalf("SubmitInput() = %#v, %v", first, err)
	}
	titled, err := fixture.store.AgentSession(context.Background(), fixture.sessions[0].ID)
	if err != nil || titled.CachedTitle != "Review the current …" || titled.Revision != 2 {
		t.Fatalf("first-input Session = %#v, %v", titled, err)
	}
	replay, err := fixture.scheduler.SubmitInput(context.Background(), input)
	if err != nil || replay.Turn.ID != first.Turn.ID || !replay.Replay {
		t.Fatalf("idempotent SubmitInput() = %#v, %v", replay, err)
	}
	conflict := input
	conflict.Text = "Different input"
	if _, err := fixture.scheduler.SubmitInput(context.Background(), conflict); !errors.Is(err, ErrInputIdempotencyConflict) {
		t.Fatalf("conflicting SubmitInput() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, processErr := fixture.scheduler.ProcessNext(context.Background())
		done <- processErr
	}()
	started := fixture.runtime.waitStarted(t)
	fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: started.threadID, CodexTurnID: started.turnID, TextDelta: "hello"})
	fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeToolStarted, CodexThreadID: started.threadID, CodexTurnID: started.turnID, ToolName: "shotforward/get_asset"})
	fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeToolCompleted, CodexThreadID: started.threadID, CodexTurnID: started.turnID, ToolName: "shotforward/get_asset"})
	fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	turn, err := fixture.store.AgentTurn(context.Background(), first.Turn.ID)
	if err != nil || turn.Status != agent.TurnCompleted || turn.Dispatch != agent.DispatchTerminal || turn.CodexTurnID == "" {
		t.Fatalf("completed Turn = %#v, %v", turn, err)
	}
	binding, err := fixture.store.AgentMCPBinding(context.Background(), fixture.sessions[0].ID)
	if err != nil || binding.Status != agent.MCPBindingReady || binding.Revision != 2 {
		t.Fatalf("verified binding = %#v, %v", binding, err)
	}
	if fixture.runtime.startCalls() != 1 || fixture.runtime.resumeCalls() != 0 || fixture.mcp.preparationCount() != 1 || fixture.mcp.activationCount() != 1 || fixture.mcp.endCount() != 1 {
		t.Fatalf("lifecycle counts: start=%d resume=%d prepare=%d activate=%d end=%d", fixture.runtime.startCalls(), fixture.runtime.resumeCalls(), fixture.mcp.preparationCount(), fixture.mcp.activationCount(), fixture.mcp.endCount())
	}
	if got := fixture.events.types(); !containsEventSequence(got, []agentapp.StudioEventType{agentapp.EventTurnQueued, agentapp.EventTurnStarted, agentapp.EventAssistantDelta, agentapp.EventToolStarted, agentapp.EventToolCompleted, agentapp.EventTurnCompleted}) {
		t.Fatalf("Studio events = %v", got)
	}
}

func TestTurnSchedulerImportsGeneratedImagesIntoTheConversation(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-image", "Generate an image"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, processErr := fixture.scheduler.ProcessNext(context.Background())
		done <- processErr
	}()
	started := fixture.runtime.waitStarted(t)
	fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{
		Type: agentapp.RuntimeImageGenerated, CodexThreadID: started.threadID, CodexTurnID: started.turnID,
		GeneratedImagePath: "/runtime/generated/result.png",
	})
	fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	input, calls := fixture.media.snapshot()
	if calls != 1 || input.ProjectID != fixture.sessions[0].ProjectID || input.EpisodeID != fixture.sessions[0].EpisodeID ||
		input.SessionID != fixture.sessions[0].ID.String() || input.TurnID != result.Turn.ID.String() || input.SourcePath != "/runtime/generated/result.png" {
		t.Fatalf("generated media import calls=%d input=%#v", calls, input)
	}
}

func TestTurnSchedulerRestoresUnregisteredBindingBeforeDispatch(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixtureForRuntime(t, 1, agentapp.RuntimeShotForward)
	fixture.mcp.setRegistered(false)
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-restore", "Continue after restart")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, processErr := fixture.scheduler.ProcessNext(context.Background())
		done <- processErr
	}()
	started := fixture.runtime.waitStarted(t)
	fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	binding, err := fixture.store.AgentMCPBinding(context.Background(), fixture.sessions[0].ID)
	if err != nil || binding.Status != agent.MCPBindingReady || binding.Revision != 4 || fixture.runtime.resumeCalls() != 1 {
		t.Fatalf("restored binding = %#v, err=%v, resumes=%d", binding, err, fixture.runtime.resumeCalls())
	}
	if request := fixture.runtime.lastResumeRequest(); request.RuntimeKind != agentapp.RuntimeShotForward {
		t.Fatalf("ResumeThread() RuntimeKind = %q, want %q", request.RuntimeKind, agentapp.RuntimeShotForward)
	}
}

func TestTurnSchedulerSteersActiveTurnOnce(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	first, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-1", "Start"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, processErr := fixture.scheduler.ProcessNext(context.Background())
		done <- processErr
	}()
	started := fixture.runtime.waitStarted(t)
	waitForTurnStatus(t, fixture.store, first.Turn.ID, agent.TurnRunning)

	steerInput := fixture.input(0, "operation-2", "Also compare the video prompt")
	steered, err := fixture.scheduler.SubmitInput(context.Background(), steerInput)
	if err != nil || steered.Action != agent.InputSteerTurn || steered.Turn.ID != first.Turn.ID || steered.Replay {
		t.Fatalf("steer SubmitInput() = %#v, %v", steered, err)
	}
	replay, err := fixture.scheduler.SubmitInput(context.Background(), steerInput)
	if err != nil || !replay.Replay {
		t.Fatalf("steer replay = %#v, %v", replay, err)
	}
	if fixture.runtime.steerCalls() != 1 {
		t.Fatalf("Runtime steer calls = %d, want 1", fixture.runtime.steerCalls())
	}
	fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTurnSchedulerRestartInterruptsQueuedInputWithoutReplay(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-restart", "Do not replay me"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := fixture.newScheduler(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	turn, err := fixture.store.AgentTurn(context.Background(), result.Turn.ID)
	if err != nil || turn.Status != agent.TurnInterrupted || fixture.runtime.startCalls() != 0 {
		t.Fatalf("recovered queued Turn = %#v, err=%v, starts=%d", turn, err, fixture.runtime.startCalls())
	}
}

func TestTurnSchedulerReconcilesPersistedRuntimeTurnWithoutResending(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	ctx := context.Background()
	result, err := fixture.scheduler.SubmitInput(ctx, fixture.input(0, "operation-reconcile", "Potentially sent"))
	if err != nil {
		t.Fatal(err)
	}
	turn := result.Turn
	dispatching, _ := turn.Transition(agent.TurnQueued, agent.Dispatching, fixture.clock.Now(), nil)
	if err := fixture.store.SaveAgentTurn(ctx, dispatching, turn.Revision); err != nil {
		t.Fatal(err)
	}
	bound, _ := dispatching.BindRuntimeTurn("codex-turn-persisted")
	if err := fixture.store.SaveAgentTurn(ctx, bound, dispatching.Revision); err != nil {
		t.Fatal(err)
	}
	running, _ := bound.Transition(agent.TurnRunning, agent.DispatchInflight, fixture.clock.Now(), nil)
	if err := fixture.store.SaveAgentTurn(ctx, running, bound.Revision); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.setHistory(fixture.sessions[0].CodexThreadID, []agentapp.RuntimeTurnSnapshot{{CodexTurnID: bound.CodexTurnID, Status: agent.TurnCompleted}})

	if err := fixture.newScheduler(t).Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.store.AgentTurn(ctx, turn.ID)
	if err != nil || got.Status != agent.TurnCompleted || fixture.runtime.startCalls() != 0 || fixture.runtime.readCalls() != 1 {
		t.Fatalf("reconciled Turn = %#v, err=%v, starts=%d reads=%d", got, err, fixture.runtime.startCalls(), fixture.runtime.readCalls())
	}
}

func TestTurnSchedulerFailsTurnThatAttemptsProhibitedCapability(t *testing.T) {
	for _, tool := range []string{"another-server/get_asset", "shotforward/start_generation_job", "tavily/search", "google_search"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(t, 1)
			result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-prohibited", "Analyze only"))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, processErr := fixture.scheduler.ProcessNext(context.Background())
				done <- processErr
			}()
			started := fixture.runtime.waitStarted(t)
			fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeToolStarted, CodexThreadID: started.threadID, CodexTurnID: started.turnID, ToolName: tool})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			turn, err := fixture.store.AgentTurn(context.Background(), result.Turn.ID)
			if err != nil || turn.Status != agent.TurnFailed || turn.Failure == nil || turn.Failure.Code != "runtime_configuration_error" || fixture.runtime.interruptCalls() != 1 {
				t.Fatalf("prohibited Turn = %#v, err=%v, interrupts=%d", turn, err, fixture.runtime.interruptCalls())
			}
		})
	}
}

func TestTurnSchedulerAllowsOnlySandboxedShell(t *testing.T) {
	for _, test := range []struct {
		name      string
		sandboxed bool
		want      agent.TurnStatus
	}{
		{name: "sandboxed", sandboxed: true, want: agent.TurnCompleted},
		{name: "unsandboxed", sandboxed: false, want: agent.TurnFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(t, 1)
			result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-shell-"+test.name, "Inspect media"))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, processErr := fixture.scheduler.ProcessNext(context.Background())
				done <- processErr
			}()
			started := fixture.runtime.waitStarted(t)
			fixture.runtime.emit(started.threadID, agentapp.RuntimeEvent{
				Type: agentapp.RuntimeToolStarted, CodexThreadID: started.threadID, CodexTurnID: started.turnID,
				ToolName: "shell", SandboxedExecution: test.sandboxed,
			})
			if test.sandboxed {
				fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			turn, err := fixture.store.AgentTurn(context.Background(), result.Turn.ID)
			if err != nil || turn.Status != test.want {
				t.Fatalf("shell Turn = %#v, err=%v", turn, err)
			}
		})
	}
}

func TestOnlyCodexWebSearchToolIsAllowed(t *testing.T) {
	t.Parallel()
	if !allowedRuntimeTool("web_search", false) {
		t.Fatal("Codex web_search was rejected")
	}
	for _, tool := range []string{"search", "search/web", "bing/search", "tavily/search", "google_search"} {
		if allowedRuntimeTool(tool, false) {
			t.Fatalf("third-party search tool %q was allowed", tool)
		}
	}
}

func TestTurnSchedulerInterruptsQueuedTurnImmediately(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	result, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "operation-cancel", "Cancel before dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := fixture.scheduler.InterruptTurn(context.Background(), fixture.sessions[0].ID, result.Turn.ID)
	if err != nil || turn.Status != agent.TurnInterrupted || fixture.runtime.startCalls() != 0 {
		t.Fatalf("InterruptTurn() = %#v, err=%v, starts=%d", turn, err, fixture.runtime.startCalls())
	}
}

func TestTurnSchedulerEnforcesFiveGlobalActiveTurns(t *testing.T) {
	fixture := newSchedulerFixture(t, 6)
	for index := range fixture.sessions {
		if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(index, fmt.Sprintf("operation-%d", index), "Run concurrently")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.scheduler.Run(ctx) }()
	started := make([]runtimeStart, 0, 6)
	for len(started) < agent.MaxGlobalActiveTurns {
		started = append(started, fixture.runtime.waitStarted(t))
	}
	select {
	case unexpected := <-fixture.runtime.started:
		t.Fatalf("sixth Turn started before capacity released: %#v", unexpected)
	case <-time.After(100 * time.Millisecond):
	}
	fixture.runtime.complete(started[0].threadID, started[0].turnID, agent.TurnCompleted)
	started = append(started, fixture.runtime.waitStarted(t))
	for _, value := range started[1:] {
		fixture.runtime.complete(value.threadID, value.turnID, agent.TurnCompleted)
	}
	waitForAllTerminal(t, fixture.store, fixture.turnIDs())
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(schedulerTestTimeout):
		t.Fatal("Run() did not stop")
	}
	if fixture.runtime.maximumActive() != agent.MaxGlobalActiveTurns {
		t.Fatalf("maximum Runtime concurrency = %d", fixture.runtime.maximumActive())
	}
}

type schedulerFixture struct {
	store     *studio.Store
	sessions  []agent.AgentSession
	runtime   *fakeRuntime
	mcp       *fakeMCP
	ids       *sequenceIDs
	clock     fixedClock
	scheduler *TurnScheduler
	events    *recordingEvents
	media     *recordingMediaImporter
}

func newSchedulerFixture(t *testing.T, sessionCount int) *schedulerFixture {
	return newSchedulerFixtureForRuntime(t, sessionCount, "")
}

func newSchedulerFixtureForRuntime(t *testing.T, sessionCount int, runtimeKind agentapp.RuntimeKind) *schedulerFixture {
	t.Helper()
	ctx := context.Background()
	store, err := studio.Open(ctx, filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, time.September, 4, 14, 0, 0, 0, time.UTC)
	runtime := newFakeRuntime()
	fixture := &schedulerFixture{store: store, runtime: runtime, mcp: newFakeMCP(), ids: &sequenceIDs{}, clock: fixedClock{at: at}, events: &recordingEvents{}, media: &recordingMediaImporter{}}
	for index := 0; index < sessionCount; index++ {
		suffix := string(rune('A' + index))
		session := agent.AgentSession{
			ID: agent.SessionID("ags_" + strings.Repeat(suffix, 26)), ProjectID: project.ProjectID("prj_" + strings.Repeat(suffix, 26)),
			EpisodeID: project.EpisodeID("ep_" + strings.Repeat(suffix, 26)), CodexThreadID: "thread-" + suffix,
			CodexSessionID: "runtime-session-" + suffix, ModelID: "gpt-test", Status: agent.SessionActive,
			RuntimeKind: string(runtimeKind), Revision: 1, CreatedAt: at, UpdatedAt: at,
		}
		if err := store.CreateAgentSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		verified := at
		binding := agent.MCPBinding{
			ID: agent.MCPBindingID("amb_" + strings.Repeat(suffix, 26)), AgentSessionID: session.ID,
			WorkspacePath: filepath.Join(t.TempDir(), "workspace-"+suffix), RouteKey: "route_" + strings.Repeat(suffix, 32),
			Status: agent.MCPBindingReady, Revision: 1, LastVerifiedAt: &verified, CreatedAt: at, UpdatedAt: at,
		}
		// Persist a previously verified binding. The fake MCP represents the same
		// live process, so dispatch must not try to resume a brand-new Thread.
		preparing := binding
		preparing.Status = agent.MCPBindingPreparing
		preparing.LastVerifiedAt = nil
		if err := store.CreateAgentMCPBinding(ctx, preparing); err != nil {
			t.Fatal(err)
		}
		ready, err := preparing.Transition(agent.MCPBindingReady, at, nil)
		if err != nil || store.SaveAgentMCPBinding(ctx, ready, preparing.Revision) != nil {
			t.Fatalf("prepare binding: %v", err)
		}
		fixture.sessions = append(fixture.sessions, session)
		runtime.addSession(session)
	}
	fixture.scheduler = fixture.newScheduler(t)
	return fixture
}

func (f *schedulerFixture) newScheduler(t *testing.T) *TurnScheduler {
	t.Helper()
	scheduler, err := NewTurnScheduler(SchedulerOptions{Store: f.store, Runtime: f.runtime, MCP: f.mcp, IDs: f.ids, Clock: f.clock, Events: f.events, Media: f.media, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func (f *schedulerFixture) input(index int, operation, text string) SubmitInput {
	return SubmitInput{AgentSessionID: f.sessions[index].ID, ClientOperationID: operation, ModelID: f.sessions[index].ModelID, Text: text}
}

func (f *schedulerFixture) turnIDs() []agent.TurnID {
	result := make([]agent.TurnID, 0, len(f.sessions))
	for _, session := range f.sessions {
		turns, _ := f.store.AgentTurns(context.Background(), session.ID)
		for _, turn := range turns {
			result = append(result, turn.ID)
		}
	}
	return result
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type recordingEvents struct {
	mutex  sync.Mutex
	values []agentapp.StudioEvent
}

type recordingMediaImporter struct {
	mutex sync.Mutex
	input projectapp.ImportAgentGeneratedMediaInput
	calls int
}

func (r *recordingMediaImporter) ImportAgentGeneratedMedia(_ context.Context, input projectapp.ImportAgentGeneratedMediaInput) (project.AgentConversationMedia, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.input = input
	r.calls++
	return project.AgentConversationMedia{}, nil
}

func (r *recordingMediaImporter) snapshot() (projectapp.ImportAgentGeneratedMediaInput, int) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.input, r.calls
}

func (r *recordingEvents) Publish(value agentapp.StudioEvent) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.values = append(r.values, value)
}

func (r *recordingEvents) types() []agentapp.StudioEventType {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	result := make([]agentapp.StudioEventType, 0, len(r.values))
	for _, value := range r.values {
		result = append(result, value.Type)
	}
	return result
}

func containsEventSequence(values, expected []agentapp.StudioEventType) bool {
	position := 0
	for _, value := range values {
		if position < len(expected) && value == expected[position] {
			position++
		}
	}
	return position == len(expected)
}

type sequenceIDs struct {
	mutex sync.Mutex
	next  int
}

func (g *sequenceIDs) value(prefix string) string {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.next++
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	return prefix + strings.Repeat("A", 25) + string(alphabet[(g.next-1)%len(alphabet)])
}
func (g *sequenceIDs) NewSessionID() (agent.SessionID, error) {
	return agent.SessionID(g.value("ags_")), nil
}
func (g *sequenceIDs) NewTurnID() (agent.TurnID, error) { return agent.TurnID(g.value("agt_")), nil }
func (g *sequenceIDs) NewMCPBindingID() (agent.MCPBindingID, error) {
	return agent.MCPBindingID(g.value("amb_")), nil
}

type runtimeStart struct {
	threadID, turnID string
	request          agentapp.TurnRequest
}

type fakeRuntime struct {
	mutex      sync.Mutex
	sessions   map[string]agent.AgentSession
	events     map[string]chan agentapp.RuntimeEvent
	history    map[string][]agentapp.RuntimeTurnSnapshot
	started    chan runtimeStart
	nextTurn   int
	starts     int
	steers     int
	reads      int
	resumes    int
	resumeReqs []agentapp.ThreadRequest
	interrupts int
	active     int
	maxActive  int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{sessions: map[string]agent.AgentSession{}, events: map[string]chan agentapp.RuntimeEvent{}, history: map[string][]agentapp.RuntimeTurnSnapshot{}, started: make(chan runtimeStart, 20)}
}
func (r *fakeRuntime) addSession(session agent.AgentSession) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.sessions[session.CodexThreadID] = session
	r.events[session.CodexThreadID] = make(chan agentapp.RuntimeEvent, 20)
}
func (r *fakeRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}, nil
}
func (r *fakeRuntime) Models(context.Context) ([]agentapp.Model, error) { return nil, nil }
func (r *fakeRuntime) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, nil
}
func (r *fakeRuntime) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New("unexpected StartThread")
}
func (r *fakeRuntime) ResumeThread(_ context.Context, id string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.resumes++
	r.resumeReqs = append(r.resumeReqs, request)
	session, ok := r.sessions[id]
	if !ok {
		return agentapp.Thread{}, errors.New("unknown Thread")
	}
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: session.CodexSessionID, ModelID: session.ModelID}, nil
}

func (r *fakeRuntime) lastResumeRequest() agentapp.ThreadRequest {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if len(r.resumeReqs) == 0 {
		return agentapp.ThreadRequest{}
	}
	return r.resumeReqs[len(r.resumeReqs)-1]
}
func (r *fakeRuntime) ReadThread(_ context.Context, id string) (agentapp.Thread, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.reads++
	session, ok := r.sessions[id]
	if !ok {
		return agentapp.Thread{}, errors.New("unknown Thread")
	}
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: session.CodexSessionID, Turns: append([]agentapp.RuntimeTurnSnapshot(nil), r.history[id]...)}, nil
}
func (r *fakeRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, nil
}
func (r *fakeRuntime) StartTurn(_ context.Context, request agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	r.mutex.Lock()
	r.nextTurn++
	r.starts++
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	id := fmt.Sprintf("codex-turn-%d", r.nextTurn)
	r.mutex.Unlock()
	r.started <- runtimeStart{threadID: request.CodexThreadID, turnID: id, request: request}
	return agentapp.RuntimeTurn{CodexTurnID: id}, nil
}
func (r *fakeRuntime) SteerTurn(context.Context, string, string, string) error {
	r.mutex.Lock()
	r.steers++
	r.mutex.Unlock()
	return nil
}
func (r *fakeRuntime) InterruptTurn(context.Context, string, string) error {
	r.mutex.Lock()
	r.interrupts++
	r.mutex.Unlock()
	return nil
}
func (r *fakeRuntime) Subscribe(_ context.Context, threadID string) (<-chan agentapp.RuntimeEvent, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.events[threadID], nil
}
func (r *fakeRuntime) complete(threadID, turnID string, status agent.TurnStatus) {
	r.mutex.Lock()
	r.active--
	channel := r.events[threadID]
	r.mutex.Unlock()
	channel <- agentapp.RuntimeEvent{Type: agentapp.RuntimeTurnCompleted, CodexThreadID: threadID, CodexTurnID: turnID, TurnStatus: status}
}
func (r *fakeRuntime) emit(threadID string, event agentapp.RuntimeEvent) {
	r.mutex.Lock()
	channel := r.events[threadID]
	r.mutex.Unlock()
	channel <- event
}
func (r *fakeRuntime) setHistory(threadID string, turns []agentapp.RuntimeTurnSnapshot) {
	r.mutex.Lock()
	r.history[threadID] = turns
	r.mutex.Unlock()
}
func (r *fakeRuntime) waitStarted(t *testing.T) runtimeStart {
	t.Helper()
	select {
	case value := <-r.started:
		return value
	case <-time.After(schedulerTestTimeout):
		t.Fatal("Runtime Turn did not start")
		return runtimeStart{}
	}
}
func (r *fakeRuntime) startCalls() int { r.mutex.Lock(); defer r.mutex.Unlock(); return r.starts }
func (r *fakeRuntime) steerCalls() int { r.mutex.Lock(); defer r.mutex.Unlock(); return r.steers }
func (r *fakeRuntime) readCalls() int  { r.mutex.Lock(); defer r.mutex.Unlock(); return r.reads }
func (r *fakeRuntime) resumeCalls() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.resumes
}
func (r *fakeRuntime) interruptCalls() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.interrupts
}
func (r *fakeRuntime) maximumActive() int { r.mutex.Lock(); defer r.mutex.Unlock(); return r.maxActive }

type fakeMCP struct {
	mutex         sync.Mutex
	active        map[agent.SessionID]agent.TurnID
	preparing     map[agent.SessionID]agent.TurnID
	registered    bool
	preparations  int
	activations   int
	ends          int
	lastActivated agentmcp.ActiveTurn
}

func newFakeMCP() *fakeMCP {
	return &fakeMCP{active: map[agent.SessionID]agent.TurnID{}, preparing: map[agent.SessionID]agent.TurnID{}, registered: true}
}
func (m *fakeMCP) EnsureBinding(_ context.Context, scope agentmcp.BindingScope) (string, error) {
	m.mutex.Lock()
	m.registered = true
	m.mutex.Unlock()
	return "http://127.0.0.1:12345/mcp/" + scope.RouteKey, nil
}
func (m *fakeMCP) BindingRegistered(scope agentmcp.BindingScope) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.registered && scope.SessionID.Validate() == nil && scope.ProjectID.Validate() == nil && scope.EpisodeID.Validate() == nil
}
func (m *fakeMCP) setRegistered(value bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.registered = value
}
func (m *fakeMCP) PrepareTurn(sessionID agent.SessionID, turnID agent.TurnID) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if _, exists := m.active[sessionID]; exists || m.preparing[sessionID] != "" {
		return agentmcp.ErrTurnConflict
	}
	m.preparing[sessionID] = turnID
	m.preparations++
	return nil
}
func (m *fakeMCP) ActivateTurn(sessionID agent.SessionID, turn agentmcp.ActiveTurn) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if _, exists := m.active[sessionID]; exists {
		return agentmcp.ErrTurnConflict
	}
	if prepared := m.preparing[sessionID]; prepared != "" && prepared != turn.AgentTurnID {
		return agentmcp.ErrTurnConflict
	}
	delete(m.preparing, sessionID)
	m.active[sessionID] = turn.AgentTurnID
	m.lastActivated = turn
	m.activations++
	return nil
}
func (m *fakeMCP) EndTurn(_ context.Context, sessionID agent.SessionID, turnID agent.TurnID) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.preparing[sessionID] == turnID {
		delete(m.preparing, sessionID)
		return nil
	}
	if m.active[sessionID] != turnID {
		return agentmcp.ErrTurnNotActive
	}
	delete(m.active, sessionID)
	m.ends++
	return nil
}
func (m *fakeMCP) CleanupTurn(context.Context, project.ProjectID, agent.TurnID) error { return nil }
func (m *fakeMCP) preparationCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.preparations
}
func (m *fakeMCP) activationCount() int { m.mutex.Lock(); defer m.mutex.Unlock(); return m.activations }
func (m *fakeMCP) endCount() int        { m.mutex.Lock(); defer m.mutex.Unlock(); return m.ends }

func (m *fakeMCP) lastActivatedTurn() agentmcp.ActiveTurn {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.lastActivated
}

func waitForTurnStatus(t *testing.T, store *studio.Store, id agent.TurnID, status agent.TurnStatus) {
	t.Helper()
	deadline := time.Now().Add(schedulerTestTimeout)
	for time.Now().Before(deadline) {
		turn, err := store.AgentTurn(context.Background(), id)
		if err == nil && turn.Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Turn %s did not reach %s", id, status)
}

func waitForAllTerminal(t *testing.T, store *studio.Store, ids []agent.TurnID) {
	t.Helper()
	deadline := time.Now().Add(schedulerTestTimeout)
	for time.Now().Before(deadline) {
		all := true
		for _, id := range ids {
			turn, err := store.AgentTurn(context.Background(), id)
			all = all && err == nil && turn.Status.IsTerminal()
		}
		if all {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Turns did not become terminal")
}
