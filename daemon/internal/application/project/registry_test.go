package projectapp

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestProjectStoreRegistryRegisterReuseAndClose(t *testing.T) {
	t.Parallel()

	project, _ := testProjectResult(t)
	root := filepath.Join(t.TempDir(), "project")
	store := &fakeStore{}
	registry := NewProjectStoreRegistry()
	handle := ProjectHandle{ProjectID: project.ID, RootPath: root, Store: store}
	registered, reused, err := registry.Register(handle)
	if err != nil || reused || registered.Store != store {
		t.Fatalf("Register() = (%#v, %t, %v), want new handle", registered, reused, err)
	}
	duplicate := &fakeStore{}
	registered, reused, err = registry.Register(ProjectHandle{ProjectID: project.ID, RootPath: root, Store: duplicate})
	if err != nil || !reused || registered.Store != store {
		t.Fatalf("Register(duplicate) = (%#v, %t, %v), want existing handle", registered, reused, err)
	}
	if got, ok := registry.ByProjectID(project.ID); !ok || got.Store != store {
		t.Fatalf("ByProjectID() = (%#v, %t)", got, ok)
	}
	if got, ok := registry.ByRootPath(root); !ok || got.ProjectID != project.ID {
		t.Fatalf("ByRootPath() = (%#v, %t)", got, ok)
	}
	if err := registry.CloseProject(project.ID); err != nil {
		t.Fatalf("CloseProject() error = %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("Store.Close() calls = %d, want 1", store.closeCalls)
	}
	if _, ok := registry.ByProjectID(project.ID); ok {
		t.Fatal("closed Project remained registered")
	}
}

func TestProjectStoreRegistryRejectsConflictsAndInvalidHandles(t *testing.T) {
	t.Parallel()

	project, _ := testProjectResult(t)
	root := filepath.Join(t.TempDir(), "one")
	registry := NewProjectStoreRegistry()
	if _, _, err := registry.Register(ProjectHandle{}); err == nil {
		t.Fatal("Register(empty) error = nil, want error")
	}
	if _, _, err := registry.Register(ProjectHandle{ProjectID: project.ID, RootPath: "relative", Store: &fakeStore{}}); err == nil {
		t.Fatal("Register(relative root) error = nil, want error")
	}
	if _, _, err := registry.Register(ProjectHandle{ProjectID: project.ID, RootPath: root, Store: &fakeStore{}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Register(ProjectHandle{
		ProjectID: project.ID,
		RootPath:  filepath.Join(t.TempDir(), "two"),
		Store:     &fakeStore{},
	}); !errors.Is(err, ErrProjectPathConflict) {
		t.Fatalf("Register(conflicting path) error = %v, want ErrProjectPathConflict", err)
	}
}

func TestProjectStoreRegistryRetainsStoreWhenCloseFails(t *testing.T) {
	t.Parallel()

	project, _ := testProjectResult(t)
	store := &fakeStore{closeErr: errInjected}
	registry := NewProjectStoreRegistry()
	if _, _, err := registry.Register(ProjectHandle{
		ProjectID: project.ID,
		RootPath:  filepath.Join(t.TempDir(), "project"),
		Store:     store,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.CloseProject(project.ID); !errors.Is(err, errInjected) {
		t.Fatalf("CloseProject() error = %v, want injected error", err)
	}
	if _, ok := registry.ByProjectID(project.ID); !ok {
		t.Fatal("Store with failed Close was removed")
	}
}

func TestProjectStoreRegistryCloseAllRetainsOnlyFailures(t *testing.T) {
	t.Parallel()

	project, _ := testProjectResult(t)
	otherID, err := domain.ParseProjectID("prj_22222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewProjectStoreRegistry()
	good := &fakeStore{}
	bad := &fakeStore{closeErr: errInjected}
	for _, handle := range []ProjectHandle{
		{ProjectID: project.ID, RootPath: filepath.Join(t.TempDir(), "good"), Store: good},
		{ProjectID: otherID, RootPath: filepath.Join(t.TempDir(), "bad"), Store: bad},
	} {
		if _, _, err := registry.Register(handle); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.CloseAll(); !errors.Is(err, errInjected) {
		t.Fatalf("CloseAll() error = %v, want injected error", err)
	}
	if _, ok := registry.ByProjectID(project.ID); ok {
		t.Fatal("successfully closed Store remained registered")
	}
	if _, ok := registry.ByProjectID(otherID); !ok {
		t.Fatal("Store with failed Close was removed")
	}
}

func TestProjectStoreRegistryConcurrentRegistration(t *testing.T) {
	t.Parallel()

	project, _ := testProjectResult(t)
	root := filepath.Join(t.TempDir(), "project")
	registry := NewProjectStoreRegistry()
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, _ = registry.Register(ProjectHandle{ProjectID: project.ID, RootPath: root, Store: &fakeStore{}})
		}()
	}
	wait.Wait()
	if _, ok := registry.ByProjectID(project.ID); !ok {
		t.Fatal("concurrent registration produced no Store")
	}
}
