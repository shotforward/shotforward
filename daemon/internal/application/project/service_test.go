package projectapp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestCreateProjectStartsEmptyAndRegistersStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	service, err := NewService(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "雨夜来信", Description: "简介", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	store := factory.createStore.(*fakeStore)
	if workspace.createCalls != 1 || factory.createCalls != 1 || store.initializeCalls != 1 {
		t.Fatalf("creation calls = workspace %d, factory %d, initialize %d", workspace.createCalls, factory.createCalls, store.initializeCalls)
	}
	if result.Project.ID != store.initializedWith.ID || len(result.Episodes) != 0 {
		t.Fatalf("CreateProject() result = %#v, initialized = %#v", result, store.initializedWith)
	}
	if len(library.records) != 1 || library.records[0].EpisodeCount != 0 || library.records[0].RootPath != root {
		t.Fatalf("library records = %#v", library.records)
	}
	if handle, ok := dependencies.Registry.ByProjectID(result.Project.ID); !ok || handle.Store != store {
		t.Fatalf("registered handle = %#v, found %t", handle, ok)
	}
	if workspace.cleanupCalls != 0 || store.closeCalls != 0 {
		t.Fatalf("successful creation cleaned up: cleanup %d, close %d", workspace.cleanupCalls, store.closeCalls)
	}
}

func TestCreateProjectStopsBeforeFilesystemForInvalidInput(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, _ := testDependencies(t, root)
	service, _ := NewService(dependencies)
	_, err := service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "Valid", OutputWidth: 123, OutputHeight: 456,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("CreateProject() error = %v, want ErrInvalidRequest", err)
	}
	if workspace.createCalls != 0 || factory.createCalls != 0 {
		t.Fatalf("invalid request reached filesystem: workspace %d, factory %d", workspace.createCalls, factory.createCalls)
	}
}

func TestCreateProjectCleansUpBeforeInitializationCompletes(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, _ := testDependencies(t, root)
	store := factory.createStore.(*fakeStore)
	store.initializeErr = errInjected
	service, _ := NewService(dependencies)
	_, err := service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "Valid", OutputWidth: 720, OutputHeight: 1280,
	})
	if !errors.Is(err, ErrProjectCreateFailed) || !errors.Is(err, errInjected) {
		t.Fatalf("CreateProject() error = %v, want creation and injected errors", err)
	}
	if store.closeCalls != 1 || workspace.cleanupCalls != 1 {
		t.Fatalf("failure cleanup = close %d, directory %d; want 1 each", store.closeCalls, workspace.cleanupCalls)
	}
}

func TestCreateProjectKeepsValidProjectWhenLibrarySyncFails(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	library.err = errInjected
	service, _ := NewService(dependencies)
	_, err := service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "Valid", OutputWidth: 720, OutputHeight: 1280,
	})
	if !errors.Is(err, ErrProjectCreatedNotRegistered) || !errors.Is(err, errInjected) {
		t.Fatalf("CreateProject() error = %v, want registration and injected errors", err)
	}
	var registration *RegistrationError
	if !errors.As(err, &registration) || registration.RootPath != root {
		t.Fatalf("RegistrationError = %#v, want root %q", registration, root)
	}
	store := factory.createStore.(*fakeStore)
	if workspace.cleanupCalls != 0 || store.closeCalls != 1 {
		t.Fatalf("valid Project handling = cleanup %d, close %d; want 0, 1", workspace.cleanupCalls, store.closeCalls)
	}
}

func TestOpenProjectReadsRegistersAndReusesStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	service, _ := NewService(dependencies)
	first, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if err != nil {
		t.Fatalf("first OpenProject() error = %v", err)
	}
	second, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if err != nil {
		t.Fatalf("second OpenProject() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("OpenProject results differ: %#v / %#v", first, second)
	}
	if workspace.openCalls != 2 || factory.openCalls != 1 {
		t.Fatalf("open calls = workspace %d, Store factory %d; want 2, 1", workspace.openCalls, factory.openCalls)
	}
	if len(library.records) != 2 {
		t.Fatalf("library sync calls = %d, want 2", len(library.records))
	}
}

func TestUpdateProjectPersistsMetadataAndRefreshesLibrary(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, library := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	dependencies.Clock = fakeClock{value: store.project.UpdatedAt.Add(time.Second)}
	if _, _, err := dependencies.Registry.Register(ProjectHandle{ProjectID: store.project.ID, RootPath: root, Store: store}); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(dependencies)
	result, err := service.UpdateProject(context.Background(), UpdateProjectInput{
		ProjectID: store.project.ID, Name: "雨后新章", Description: "新的简介", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if store.updateProjectCalls != 1 || store.expectedRevision != 1 {
		t.Fatalf("UpdateProject calls = %d, expected revision = %d", store.updateProjectCalls, store.expectedRevision)
	}
	if result.Project.Name != "雨后新章" || result.Project.Description != "新的简介" || result.Project.Revision != 2 {
		t.Fatalf("UpdateProject() = %#v", result.Project)
	}
	if len(library.records) != 1 || library.records[0].DisplayName != "雨后新章" || library.records[0].RootPath != root {
		t.Fatalf("library records = %#v", library.records)
	}
}

func TestUpdateProjectRejectsInvalidAndStaleMetadata(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, _ := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	if _, _, err := dependencies.Registry.Register(ProjectHandle{ProjectID: store.project.ID, RootPath: root, Store: store}); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(dependencies)
	for _, test := range []struct {
		name      string
		input     UpdateProjectInput
		wantError error
	}{
		{name: "blank name", input: UpdateProjectInput{ProjectID: store.project.ID, Name: " ", ExpectedRevision: 1}, wantError: ErrInvalidRequest},
		{name: "stale revision", input: UpdateProjectInput{ProjectID: store.project.ID, Name: "Valid", ExpectedRevision: 2}, wantError: ErrRevisionConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.UpdateProject(context.Background(), test.input); !errors.Is(err, test.wantError) {
				t.Fatalf("UpdateProject() error = %v, want %v", err, test.wantError)
			}
		})
	}
	if store.updateProjectCalls != 0 {
		t.Fatalf("invalid updates reached Store %d times", store.updateProjectCalls)
	}
}

func TestDeleteProjectRequiresExactNameThenPermanentlyRemovesWorkspace(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	if _, _, err := dependencies.Registry.Register(ProjectHandle{ProjectID: store.project.ID, RootPath: root, Store: store}); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(dependencies)
	if err := service.DeleteProject(context.Background(), DeleteProjectInput{ProjectID: store.project.ID, ConfirmationName: "wrong"}); !errors.Is(err, ErrProjectNameConfirmation) {
		t.Fatalf("DeleteProject(wrong name) error = %v", err)
	}
	if workspace.deleteCalls != 0 {
		t.Fatal("wrong confirmation reached filesystem")
	}
	if err := service.DeleteProject(context.Background(), DeleteProjectInput{ProjectID: store.project.ID, ConfirmationName: store.project.Name}); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	if workspace.openCalls != 1 || workspace.deleteCalls != 1 || store.closeCalls != 1 || library.deleteCalls != 1 {
		t.Fatalf("delete calls: open=%d delete=%d close=%d library=%d", workspace.openCalls, workspace.deleteCalls, store.closeCalls, library.deleteCalls)
	}
	if _, ok := dependencies.Registry.ByProjectID(store.project.ID); ok {
		t.Fatal("deleted Project remains registered")
	}
}

func TestDeleteProjectRemovesRegistrationWhenProjectDirectoryIsGone(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "missing-project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	workspace.openErr = ErrProjectPathNotFound
	service, _ := NewService(dependencies)

	if err := service.DeleteProject(context.Background(), DeleteProjectInput{
		ProjectID: factory.openStore.(*fakeStore).project.ID, ConfirmationName: "wrong",
	}); !errors.Is(err, ErrProjectNameConfirmation) {
		t.Fatalf("DeleteProject(wrong name) error = %v, want ErrProjectNameConfirmation", err)
	}
	if library.deleteCalls != 0 || workspace.deleteCalls != 0 {
		t.Fatalf("wrong confirmation deleted data: library=%d workspace=%d", library.deleteCalls, workspace.deleteCalls)
	}

	if err := service.DeleteProject(context.Background(), DeleteProjectInput{
		ProjectID: factory.openStore.(*fakeStore).project.ID, ConfirmationName: library.project.DisplayName,
	}); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	if workspace.openCalls != 1 || workspace.deleteCalls != 0 || library.deleteCalls != 1 {
		t.Fatalf("orphan delete calls: open=%d workspace delete=%d library delete=%d", workspace.openCalls, workspace.deleteCalls, library.deleteCalls)
	}
}

func TestDeleteProjectDoesNotRemoveRegistrationForExistingUnavailableDirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "existing-project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	service, _ := NewService(dependencies)

	err := service.DeleteProject(context.Background(), DeleteProjectInput{
		ProjectID: factory.openStore.(*fakeStore).project.ID, ConfirmationName: library.project.DisplayName,
	})
	if !errors.Is(err, ErrProjectStoreNotOpen) {
		t.Fatalf("DeleteProject() error = %v, want ErrProjectStoreNotOpen", err)
	}
	if library.deleteCalls != 0 || workspace.deleteCalls != 0 {
		t.Fatalf("unavailable existing directory deleted: library=%d workspace=%d", library.deleteCalls, workspace.deleteCalls)
	}
}

func TestRestoreProjectValidatesIdentityWithoutUpdatingLibrary(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, factory, library := testDependencies(t, root)
	expected := factory.openStore.(*fakeStore).project.ID
	service, _ := NewService(dependencies)
	result, err := service.RestoreProject(context.Background(), RestoreProjectInput{ProjectID: expected, RootPath: root})
	if err != nil || result.Project.ID != expected {
		t.Fatalf("RestoreProject() = %#v, %v", result, err)
	}
	if workspace.openCalls != 1 || factory.openCalls != 1 || len(library.records) != 0 {
		t.Fatalf("restore calls: workspace=%d store=%d library=%d", workspace.openCalls, factory.openCalls, len(library.records))
	}
}

func TestRestoreProjectRejectsStudioIdentityMismatch(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, _ := testDependencies(t, root)
	other, _ := domain.ParseProjectID("prj_22222222222222222222222222")
	service, _ := NewService(dependencies)
	if _, err := service.RestoreProject(context.Background(), RestoreProjectInput{ProjectID: other, RootPath: root}); !errors.Is(err, ErrProjectPathConflict) {
		t.Fatalf("RestoreProject() error = %v", err)
	}
	if factory.openStore.(*fakeStore).closeCalls != 1 {
		t.Fatalf("mismatched Store close calls = %d", factory.openStore.(*fakeStore).closeCalls)
	}
}

func TestOpenProjectAcceptsProjectWithoutEpisodes(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, library := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	store.episodes = nil
	service, _ := NewService(dependencies)
	result, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if err != nil || len(result.Episodes) != 0 {
		t.Fatalf("OpenProject() = %#v, %v; want empty Episodes", result, err)
	}
	if store.closeCalls != 0 || len(library.records) != 1 {
		t.Fatalf("empty Project handling = close %d, library %d", store.closeCalls, len(library.records))
	}
}

func TestOpenProjectRejectsInvalidProjectAndClosesStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, library := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	store.project.Name = ""
	service, _ := NewService(dependencies)
	_, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if !errors.Is(err, ErrProjectDataInvalid) {
		t.Fatalf("OpenProject() error = %v, want ErrProjectDataInvalid", err)
	}
	if store.closeCalls != 1 || len(library.records) != 0 {
		t.Fatalf("invalid data handling = close %d, library %d", store.closeCalls, len(library.records))
	}
}

func TestOpenProjectClosesNewStoreWhenLibrarySyncFails(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, library := testDependencies(t, root)
	library.err = errInjected
	service, _ := NewService(dependencies)
	_, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if !errors.Is(err, ErrProjectOpenedNotRegistered) || !errors.Is(err, errInjected) {
		t.Fatalf("OpenProject() error = %v, want registration and injected errors", err)
	}
	if factory.openStore.(*fakeStore).closeCalls != 1 {
		t.Fatalf("Store.Close() calls = %d, want 1", factory.openStore.(*fakeStore).closeCalls)
	}
}

func TestOpenProjectRejectsIdentityAtAnotherOpenPath(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "selected")
	dependencies, _, factory, _ := testDependencies(t, root)
	project, _ := testProjectResult(t)
	if _, _, err := dependencies.Registry.Register(ProjectHandle{
		ProjectID: project.ID,
		RootPath:  filepath.Join(t.TempDir(), "other"),
		Store:     &fakeStore{},
	}); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(dependencies)
	_, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root})
	if !errors.Is(err, ErrProjectPathConflict) {
		t.Fatalf("OpenProject() error = %v, want ErrProjectPathConflict", err)
	}
	if factory.openStore.(*fakeStore).closeCalls != 1 {
		t.Fatalf("candidate Store.Close() calls = %d, want 1", factory.openStore.(*fakeStore).closeCalls)
	}
}

func TestNewServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewService(Dependencies{}); err == nil {
		t.Fatal("NewService() error = nil, want error")
	}
}

func TestCreateProjectReportsIDAndClockFailures(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, workspace, _, _ := testDependencies(t, root)
	dependencies.IDs = fakeIDs{projectErr: errInjected}
	service, _ := NewService(dependencies)
	_, err := service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "Valid", OutputWidth: 720, OutputHeight: 1280,
	})
	if !errors.Is(err, ErrProjectCreateFailed) || workspace.createCalls != 0 {
		t.Fatalf("ID failure = %v, workspace calls %d", err, workspace.createCalls)
	}

	dependencies, workspace, _, _ = testDependencies(t, root)
	dependencies.Clock = fakeClock{value: time.Time{}}
	service, _ = NewService(dependencies)
	_, err = service.CreateProject(context.Background(), CreateProjectInput{
		RootPath: root, Name: "Valid", OutputWidth: 720, OutputHeight: 1280,
	})
	if !errors.Is(err, ErrProjectCreateFailed) || workspace.createCalls != 0 {
		t.Fatalf("Clock failure = %v, workspace calls %d", err, workspace.createCalls)
	}
}

func TestReadResultRejectsWrongEpisodeOwner(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, _ := testDependencies(t, root)
	store := factory.openStore.(*fakeStore)
	other, err := domain.ParseProjectID("prj_22222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	store.episodes[0].ProjectID = other
	service, _ := NewService(dependencies)
	if _, err := service.OpenProject(context.Background(), OpenProjectInput{RootPath: root}); !errors.Is(err, ErrProjectDataInvalid) {
		t.Fatalf("OpenProject() error = %v, want ErrProjectDataInvalid", err)
	}
}
