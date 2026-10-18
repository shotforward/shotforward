package agentscheduler

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

type compactingTestRuntime struct {
	*fakeRuntime
	started chan string
	finish  chan error
}

func (r *compactingTestRuntime) CompactThread(ctx context.Context, threadID string) error {
	select {
	case r.started <- threadID:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.finish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func installCompactingRuntime(fixture *schedulerFixture) *compactingTestRuntime {
	runtime := &compactingTestRuntime{fakeRuntime: fixture.runtime, started: make(chan string, 1), finish: make(chan error, 1)}
	fixture.scheduler.runtime = runtime
	return runtime
}

func awaitCompactionStart(t *testing.T, runtime *compactingTestRuntime) string {
	t.Helper()
	select {
	case threadID := <-runtime.started:
		return threadID
	case <-time.After(schedulerTestTimeout):
		t.Fatal("context compaction did not start")
		return ""
	}
}

func awaitCompactionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(schedulerTestTimeout):
		t.Fatal("context compaction did not finish")
		return nil
	}
}

func TestContextCompactionReservesOnlyItsSessionUntilNativeCompletion(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 2)
	runtime := installCompactingRuntime(fixture)
	sessionID := fixture.sessions[0].ID
	result := make(chan error, 1)
	go func() { result <- fixture.scheduler.CompactSession(context.Background(), sessionID) }()
	if got := awaitCompactionStart(t, runtime); got != fixture.sessions[0].CodexThreadID {
		t.Fatalf("compacted Thread = %s", got)
	}
	if !fixture.scheduler.IsContextCompacting(sessionID) || fixture.scheduler.IsContextCompacting(fixture.sessions[1].ID) {
		t.Fatal("compaction reservation leaked to another Session")
	}
	if err := fixture.scheduler.CompactSession(context.Background(), sessionID); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("duplicate compaction = %v", err)
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "during-compaction", "Hi")); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("input during compaction = %v", err)
	}
	if turns, err := fixture.store.AgentTurns(context.Background(), sessionID); err != nil || len(turns) != 0 {
		t.Fatalf("compaction created fake Turns: %#v, %v", turns, err)
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(1, "other-session", "Hello")); err != nil {
		t.Fatalf("input to another Session = %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("compaction completed before native completion: %v", err)
	default:
	}
	runtime.finish <- nil
	if err := awaitCompactionResult(t, result); err != nil {
		t.Fatal(err)
	}
	if fixture.scheduler.IsContextCompacting(sessionID) {
		t.Fatal("completed compaction retained reservation")
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "after-compaction", "Hi")); err != nil {
		t.Fatalf("input after compaction = %v", err)
	}
}

func TestContextCompactionRejectsQueuedAndActiveTurns(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "queued"
		if active {
			name = "active"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(t, 1)
			runtime := installCompactingRuntime(fixture)
			if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "queued-input", "Hello")); err != nil {
				t.Fatal(err)
			}
			if active {
				done := make(chan error, 1)
				go func() { _, err := fixture.scheduler.ProcessNext(context.Background()); done <- err }()
				started := fixture.runtime.waitStarted(t)
				defer func() {
					fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
					if err := awaitCompactionResult(t, done); err != nil {
						t.Fatal(err)
					}
				}()
			}
			if err := fixture.scheduler.CompactSession(context.Background(), fixture.sessions[0].ID); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
				t.Fatalf("compaction with pending Turn = %v", err)
			}
			if fixture.scheduler.IsContextCompacting(fixture.sessions[0].ID) || len(runtime.started) != 0 {
				t.Fatal("busy Session entered native compaction")
			}
		})
	}
}

func TestContextCompactionRejectsUnsupportedInactiveAndInvalidSessions(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	if err := fixture.scheduler.CompactSession(context.Background(), fixture.sessions[0].ID); !errors.Is(err, agentapp.ErrContextCompactionUnavailable) {
		t.Fatalf("missing capability = %v", err)
	}
	installCompactingRuntime(fixture)
	if err := fixture.scheduler.CompactSession(nil, fixture.sessions[0].ID); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("nil context = %v", err)
	}
	if err := fixture.scheduler.CompactSession(context.Background(), "invalid"); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("invalid Session ID = %v", err)
	}
	archived, err := fixture.sessions[0].Transition(agent.SessionArchived, fixture.clock.Now())
	if err != nil || fixture.store.SaveAgentSession(context.Background(), archived, fixture.sessions[0].Revision) != nil {
		t.Fatalf("archive Session: %v", err)
	}
	if err := fixture.scheduler.CompactSession(context.Background(), archived.ID); !errors.Is(err, ErrSessionNotActive) {
		t.Fatalf("archived Session compaction = %v", err)
	}
}

func TestContextCompactionReleasesReservationAfterFailureAndCancellation(t *testing.T) {
	for _, failure := range []error{errors.New("native compact failed"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(t, 1)
			runtime := installCompactingRuntime(fixture)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- fixture.scheduler.CompactSession(ctx, fixture.sessions[0].ID) }()
			awaitCompactionStart(t, runtime)
			if errors.Is(failure, context.Canceled) {
				cancel()
			} else {
				runtime.finish <- failure
			}
			if err := awaitCompactionResult(t, result); !errors.Is(err, failure) {
				t.Fatalf("compaction failure = %v, want %v", err, failure)
			}
			if fixture.scheduler.IsContextCompacting(fixture.sessions[0].ID) {
				t.Fatal("failed compaction retained reservation")
			}
			if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "after-failure", "Hi")); err != nil {
				t.Fatalf("input after failed compaction = %v", err)
			}
		})
	}
}

type blockedCompactionAdmissionStore struct {
	SchedulerStore
	entered chan struct{}
	release chan struct{}
}

func (s blockedCompactionAdmissionStore) AdmitAgentInput(ctx context.Context, turn agent.AgentTurn, fingerprint, text, title string) (agent.InputOperation, bool, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return agent.InputOperation{}, false, ctx.Err()
	}
	return s.SchedulerStore.AdmitAgentInput(ctx, turn, fingerprint, text, title)
}

func TestContextCompactionAndInputAdmissionAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	runtime := installCompactingRuntime(fixture)
	store := blockedCompactionAdmissionStore{SchedulerStore: fixture.store, entered: make(chan struct{}), release: make(chan struct{})}
	fixture.scheduler.store = store
	inputDone := make(chan error, 1)
	go func() {
		_, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "race", "Hi"))
		inputDone <- err
	}()
	<-store.entered
	compactDone := make(chan error, 1)
	go func() { compactDone <- fixture.scheduler.CompactSession(context.Background(), fixture.sessions[0].ID) }()
	close(store.release)
	if err := awaitCompactionResult(t, inputDone); err != nil {
		t.Fatal(err)
	}
	if err := awaitCompactionResult(t, compactDone); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("compaction raced admitted input: %v", err)
	}
	if len(runtime.started) != 0 {
		t.Fatal("native compaction ran concurrently with admitted input")
	}
}

type lingeringNativeCompactionRuntime struct {
	*compactingTestRuntime
	nativeBusy atomic.Bool
}

func (r *lingeringNativeCompactionRuntime) CompactThread(ctx context.Context, threadID string) error {
	r.nativeBusy.Store(true)
	return r.compactingTestRuntime.CompactThread(ctx, threadID)
}

func (r *lingeringNativeCompactionRuntime) ContextCompactionInProgress(string) bool {
	return r.nativeBusy.Load()
}

func TestContextCompactionCancelledWaitKeepsNativeThreadIsolated(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	runtime := &lingeringNativeCompactionRuntime{compactingTestRuntime: installCompactingRuntime(fixture)}
	fixture.scheduler.runtime = runtime
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	sessionID := fixture.sessions[0].ID
	go func() { result <- fixture.scheduler.CompactSession(ctx, sessionID) }()
	awaitCompactionStart(t, runtime.compactingTestRuntime)
	cancel()
	if err := awaitCompactionResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fixture.scheduler.mutex.Lock()
	_, retained := fixture.scheduler.compacting[sessionID]
	fixture.scheduler.mutex.Unlock()
	if retained || !fixture.scheduler.IsContextCompacting(sessionID) {
		t.Fatal("cancelled request retained its reservation or hid unfinished native compaction")
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "native-still-busy", "Hi")); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("input admitted while native compaction remains active: %v", err)
	}
	if err := fixture.scheduler.CompactSession(context.Background(), sessionID); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("second compaction overlapped native operation: %v", err)
	}
	runtime.nativeBusy.Store(false)
	if fixture.scheduler.IsContextCompacting(sessionID) {
		t.Fatal("finished native compaction remains visible as active")
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "native-finished", "Hi")); err != nil {
		t.Fatalf("input after native completion = %v", err)
	}
}

func TestContextCompactionLeavesPersistedConversationAndSessionUntouched(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	runtime := installCompactingRuntime(fixture)
	sessionID := fixture.sessions[0].ID
	input, err := fixture.scheduler.SubmitInput(context.Background(), fixture.input(0, "historical-input", "Keep this message"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.scheduler.InterruptTurn(context.Background(), sessionID, input.Turn.ID); err != nil {
		t.Fatal(err)
	}
	turnsBefore, err := fixture.store.AgentTurns(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sessionBefore, err := fixture.store.AgentSession(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := fixture.events.types()
	runtime.finish <- nil
	if err := fixture.scheduler.CompactSession(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	turnsAfter, err := fixture.store.AgentTurns(context.Background(), sessionID)
	if err != nil || !reflect.DeepEqual(turnsBefore, turnsAfter) {
		t.Fatalf("compaction changed persisted Turns: before=%#v after=%#v error=%v", turnsBefore, turnsAfter, err)
	}
	sessionAfter, err := fixture.store.AgentSession(context.Background(), sessionID)
	if err != nil || !reflect.DeepEqual(sessionBefore, sessionAfter) || !reflect.DeepEqual(eventsBefore, fixture.events.types()) {
		t.Fatalf("compaction changed Session metadata or invented Turn events: %#v, %v", sessionAfter, err)
	}
}
