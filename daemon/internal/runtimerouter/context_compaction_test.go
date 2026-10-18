package runtimerouter

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type compactingRuntime struct {
	fakeRuntime
	thread string
	busy   bool
	err    error
}

func (r *compactingRuntime) CompactThread(_ context.Context, threadID string) error {
	r.thread = threadID
	return r.err
}
func (r *compactingRuntime) ContextCompactionInProgress(threadID string) bool {
	r.thread = threadID
	return r.busy
}

func TestRouterCompactionKeepsRuntimeAndThreadBoundary(t *testing.T) {
	user := &compactingRuntime{}
	cloud := &compactingRuntime{busy: true}
	router, _ := New(user, cloud)
	for _, threadID := range []string{"shotforward|cloud-thread", "user_codex|user-thread", "legacy-thread"} {
		if err := router.CompactThread(context.Background(), threadID); err != nil {
			t.Fatal(err)
		}
	}
	if user.thread != "legacy-thread" || cloud.thread != "cloud-thread" {
		t.Fatalf("compacted wrong Runtime/Thread: user=%q cloud=%q", user.thread, cloud.thread)
	}
	if !router.ContextCompactionInProgress("shotforward|cloud-thread") || router.ContextCompactionInProgress("legacy-thread") {
		t.Fatal("maintenance status crossed Runtime boundary")
	}
	want := errors.New("native compaction failed")
	cloud.err = want
	if err := router.CompactThread(context.Background(), "shotforward|cloud-thread"); !errors.Is(err, want) {
		t.Fatalf("native error was hidden: %v", err)
	}
}

func TestRouterCompactionIsOptionalAndHonorsSelection(t *testing.T) {
	user := &compactingRuntime{}
	router, _ := New(user, &fakeRuntime{})
	if err := router.CompactThread(context.Background(), "shotforward|thread"); !errors.Is(err, agentapp.ErrContextCompactionUnavailable) {
		t.Fatalf("unsupported Runtime: %v", err)
	}
	if router.ContextCompactionInProgress("shotforward|thread") {
		t.Fatal("unsupported Runtime is marked busy")
	}
	selection := &fakeSelectionStore{kind: agentapp.RuntimeShotForward}
	router.selection = selection
	if err := router.CompactThread(context.Background(), "legacy-thread"); !errors.Is(err, agentapp.ErrInvalidRuntimeSelection) {
		t.Fatalf("inactive Runtime accepted compaction: %v", err)
	}
}
