package runtimerouter

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type fakeSelectionStore struct {
	kind   agentapp.RuntimeKind
	active bool
}

func (s *fakeSelectionStore) ActiveRuntime(context.Context) (agentapp.RuntimeKind, error) {
	return s.kind, nil
}
func (s *fakeSelectionStore) SetActiveRuntime(_ context.Context, kind agentapp.RuntimeKind) error {
	s.kind = kind
	return nil
}
func (s *fakeSelectionStore) HasActiveTurns(context.Context) (bool, error) { return s.active, nil }
func (s *fakeSelectionStore) RuntimeWithActiveTurns(context.Context) (agentapp.RuntimeKind, bool, error) {
	return "", false, nil
}

type fakeRuntime struct {
	name          string
	starts, reads int
}

type fakeModelSource struct {
	models []agentapp.Model
	err    error
}

func (f fakeModelSource) Models(context.Context) ([]agentapp.Model, error) {
	return append([]agentapp.Model(nil), f.models...), f.err
}

type fakeRefreshingModelSource struct {
	fakeModelSource
	reads, refreshes int
}

func (f *fakeRefreshingModelSource) Models(context.Context) ([]agentapp.Model, error) {
	f.reads++
	return append([]agentapp.Model(nil), f.models...), f.err
}

func (f *fakeRefreshingModelSource) RefreshModels(context.Context) ([]agentapp.Model, error) {
	f.refreshes++
	return append([]agentapp.Model(nil), f.models...), f.err
}

func (f *fakeRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady, Version: f.name}, nil
}
func (f *fakeRuntime) Models(context.Context) ([]agentapp.Model, error) {
	return []agentapp.Model{{ID: f.name, DisplayName: f.name}}, nil
}
func (f *fakeRuntime) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, nil
}
func (f *fakeRuntime) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	f.starts++
	return agentapp.Thread{CodexThreadID: f.name + "-thread", CodexSessionID: f.name + "-session"}, nil
}
func (f *fakeRuntime) ResumeThread(context.Context, string, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, nil
}
func (f *fakeRuntime) ReadThread(_ context.Context, id string) (agentapp.Thread, error) {
	f.reads++
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: f.name + "-session"}, nil
}
func (f *fakeRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, nil
}
func (f *fakeRuntime) StartTurn(context.Context, agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	return agentapp.RuntimeTurn{}, nil
}
func (f *fakeRuntime) SteerTurn(context.Context, string, string, string) error { return nil }
func (f *fakeRuntime) InterruptTurn(context.Context, string, string) error     { return nil }
func (f *fakeRuntime) Subscribe(context.Context, string) (<-chan agentapp.RuntimeEvent, error) {
	ch := make(chan agentapp.RuntimeEvent)
	close(ch)
	return ch, nil
}

func TestRouterPinsNewAndLegacyThreads(t *testing.T) {
	user, cloud := &fakeRuntime{name: "user"}, &fakeRuntime{name: "cloud"}
	router, _ := New(user, cloud)
	thread, err := router.StartThread(context.Background(), agentapp.ThreadRequest{RuntimeKind: agentapp.RuntimeShotForward})
	if err != nil {
		t.Fatal(err)
	}
	if thread.CodexThreadID != "shotforward|cloud-thread" || cloud.starts != 1 || user.starts != 0 {
		t.Fatalf("thread=%#v user=%d cloud=%d", thread, user.starts, cloud.starts)
	}
	if _, err = router.ReadThread(context.Background(), "legacy-thread"); err != nil {
		t.Fatal(err)
	}
	if user.reads != 1 || cloud.reads != 0 {
		t.Fatalf("legacy route user=%d cloud=%d", user.reads, cloud.reads)
	}
	models, _ := router.Models(context.Background())
	if len(models) != 2 || models[0].RuntimeKind != agentapp.RuntimeUserCodex || models[1].RuntimeKind != agentapp.RuntimeShotForward {
		t.Fatalf("models=%#v", models)
	}
	userSnapshot, _ := router.SnapshotFor(context.Background(), agentapp.RuntimeUserCodex)
	cloudSnapshot, _ := router.SnapshotFor(context.Background(), agentapp.RuntimeShotForward)
	if userSnapshot.Version != "user" || cloudSnapshot.Version != "cloud" {
		t.Fatalf("routed snapshots user=%#v cloud=%#v", userSnapshot, cloudSnapshot)
	}
}

func TestRouterSelectsOneRuntimeAndDefaultsToShotForward(t *testing.T) {
	user, cloud := &fakeRuntime{name: "user"}, &fakeRuntime{name: "cloud"}
	selection := &fakeSelectionStore{}
	router, err := New(user, cloud, selection)
	if err != nil {
		t.Fatal(err)
	}

	kind, err := router.ActiveRuntime(context.Background())
	if err != nil || kind != agentapp.RuntimeShotForward || selection.kind != agentapp.RuntimeShotForward {
		t.Fatalf("ActiveRuntime() = %q, %v; persisted %q", kind, err, selection.kind)
	}
	models, err := router.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "cloud" || models[0].RuntimeKind != agentapp.RuntimeShotForward {
		t.Fatalf("cloud Models() = %#v, %v", models, err)
	}
	if _, err := router.StartThread(context.Background(), agentapp.ThreadRequest{RuntimeKind: agentapp.RuntimeUserCodex}); !errors.Is(err, agentapp.ErrInvalidRuntimeSelection) {
		t.Fatalf("mismatched StartThread() error = %v", err)
	}

	if err := router.SelectRuntime(context.Background(), agentapp.RuntimeUserCodex); err != nil {
		t.Fatalf("SelectRuntime(user) = %v", err)
	}
	models, err = router.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "user" || models[0].RuntimeKind != agentapp.RuntimeUserCodex {
		t.Fatalf("user Models() = %#v, %v", models, err)
	}

	selection.active = true
	if err := router.SelectRuntime(context.Background(), agentapp.RuntimeShotForward); !errors.Is(err, agentapp.ErrRuntimeSwitchBlocked) {
		t.Fatalf("SelectRuntime(active Turn) error = %v", err)
	}
	if selection.kind != agentapp.RuntimeUserCodex {
		t.Fatalf("blocked selection changed to %q", selection.kind)
	}
}

func TestRouterIntersectsShotForwardRuntimeWithCloudCatalog(t *testing.T) {
	user, cloud := &fakeRuntime{name: "user"}, &fakeRuntime{name: "cloud"}
	cloudModels := fakeModelSource{models: []agentapp.Model{
		{ID: "cloud", DisplayName: "Cloud display name"},
		{ID: "new-cloud-only", DisplayName: "Not understood locally"},
		{ID: "cloud", DisplayName: "Duplicate"},
	}}
	router, err := NewWithShotForwardModels(user, cloud, cloudModels)
	if err != nil {
		t.Fatal(err)
	}

	models, err := router.ModelsFor(context.Background(), agentapp.RuntimeShotForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "cloud" || models[0].DisplayName != "Cloud display name" || models[0].RuntimeKind != agentapp.RuntimeShotForward {
		t.Fatalf("filtered models=%#v", models)
	}
}

func TestRouterDoesNotMaskCloudCatalogFailure(t *testing.T) {
	want := errors.New("cloud unavailable")
	router, err := NewWithShotForwardModels(&fakeRuntime{name: "user"}, &fakeRuntime{name: "cloud"}, fakeModelSource{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = router.ModelsFor(context.Background(), agentapp.RuntimeShotForward); !errors.Is(err, want) {
		t.Fatalf("ModelsFor() error=%v", err)
	}
}

func TestRouterOnlyBypassesCloudCatalogCacheForExplicitRefresh(t *testing.T) {
	source := &fakeRefreshingModelSource{fakeModelSource: fakeModelSource{models: []agentapp.Model{{ID: "cloud"}}}}
	router, err := NewWithShotForwardModels(&fakeRuntime{name: "user"}, &fakeRuntime{name: "cloud"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = router.ModelsFor(context.Background(), agentapp.RuntimeShotForward); err != nil {
		t.Fatal(err)
	}
	if _, err = router.RefreshModelsFor(context.Background(), agentapp.RuntimeShotForward); err != nil {
		t.Fatal(err)
	}
	if source.reads != 1 || source.refreshes != 1 {
		t.Fatalf("catalog reads=%d refreshes=%d", source.reads, source.refreshes)
	}
}
