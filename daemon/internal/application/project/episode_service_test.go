package projectapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestGetEpisodeRequiresOpenProjectAndReturnsScript(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	got, err := service.GetEpisode(context.Background(), GetEpisodeInput{
		ProjectID: project.ID,
		EpisodeID: episode.ID,
	})
	if err != nil {
		t.Fatalf("GetEpisode() error = %v", err)
	}
	if got.ID != episode.ID || got.Script != episode.Script || store.episodeCalls != 1 {
		t.Fatalf("GetEpisode() = %#v, calls %d", got, store.episodeCalls)
	}

	closedService, _ := NewService(Dependencies{
		IDs: dependencies.IDs, Clock: dependencies.Clock, Workspace: dependencies.Workspace,
		Stores: dependencies.Stores, Library: dependencies.Library, Importer: dependencies.Importer,
		Registry: NewProjectStoreRegistry(),
	})
	if _, err := closedService.GetEpisode(context.Background(), GetEpisodeInput{
		ProjectID: project.ID,
		EpisodeID: episode.ID,
	}); !errors.Is(err, ErrProjectStoreNotOpen) {
		t.Fatalf("GetEpisode(closed Project) error = %v, want ErrProjectStoreNotOpen", err)
	}
}

func TestCreateEpisodeAppendsWithRequestedMetadata(t *testing.T) {
	t.Parallel()

	service, dependencies, store, library, project, _ := openedEpisodeService(t)
	newID, err := domain.ParseEpisodeID("ep_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	dependencies.IDs = fakeIDs{episodeID: newID}
	service.ids = dependencies.IDs

	result, err := service.CreateEpisode(context.Background(), CreateEpisodeInput{ProjectID: project.ID, Title: "序幕", TargetDuration: 75 * time.Second})
	if err != nil {
		t.Fatalf("CreateEpisode() error = %v", err)
	}
	created := result.Episode
	if created.ID != newID || created.SequenceNumber != 2 || created.Title != "序幕" {
		t.Fatalf("created Episode identity = %#v", created.EpisodeMetadata)
	}
	if created.TargetDuration != 75*time.Second || created.Script.Markdown != "" || created.Script.Revision != 1 {
		t.Fatalf("created Episode defaults = %#v", created)
	}
	if store.createEpisodeCalls != 1 || len(result.Episodes) != 2 || len(library.records) != 1 {
		t.Fatalf("mutation calls/results = store %d, Episodes %d, library %d", store.createEpisodeCalls, len(result.Episodes), len(library.records))
	}
	if result.Project.UpdatedAt != store.projectUpdatedAt {
		t.Fatalf("Project updated at = %v, want %v", result.Project.UpdatedAt, store.projectUpdatedAt)
	}
}

func TestCreateEpisodeRequiresDurationWithinProductRange(t *testing.T) {
	t.Parallel()
	service, _, store, _, project, _ := openedEpisodeService(t)
	for _, duration := range []time.Duration{14 * time.Second, 30*time.Minute + time.Second} {
		if _, err := service.CreateEpisode(context.Background(), CreateEpisodeInput{ProjectID: project.ID, TargetDuration: duration}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("CreateEpisode(%s) error = %v", duration, err)
		}
	}
	if store.createEpisodeCalls != 0 {
		t.Fatalf("invalid durations reached Store %d times", store.createEpisodeCalls)
	}
}

func TestUpdateEpisodeUsesIndependentMetadataRevision(t *testing.T) {
	t.Parallel()

	service, _, store, _, project, episode := openedEpisodeService(t)
	result, err := service.UpdateEpisode(context.Background(), UpdateEpisodeInput{
		ProjectID:        project.ID,
		EpisodeID:        episode.ID,
		Title:            "序章",
		TargetDuration:   90 * time.Second,
		ExpectedRevision: episode.Revision,
	})
	if err != nil {
		t.Fatalf("UpdateEpisode() error = %v", err)
	}
	if result.Episode.Title != "序章" || result.Episode.TargetDuration != 90*time.Second {
		t.Fatalf("UpdateEpisode() = %#v", result.Episode)
	}
	if result.Episode.Revision != episode.Revision+1 || result.Episode.Script.Revision != episode.Script.Revision {
		t.Fatalf("revisions = Episode %d, Script %d", result.Episode.Revision, result.Episode.Script.Revision)
	}
	if store.expectedRevision != episode.Revision || store.updateEpisodeCalls != 1 {
		t.Fatalf("Store update = expected revision %d, calls %d", store.expectedRevision, store.updateEpisodeCalls)
	}
}

func TestUpdateEpisodeRejectsStaleAndInvalidRequestsBeforeWrite(t *testing.T) {
	t.Parallel()

	service, _, store, _, project, episode := openedEpisodeService(t)
	_, err := service.UpdateEpisode(context.Background(), UpdateEpisodeInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Title: "序章",
		TargetDuration: time.Minute, ExpectedRevision: episode.Revision + 1,
	})
	if !errors.Is(err, ErrRevisionConflict) || store.updateEpisodeCalls != 0 {
		t.Fatalf("stale update = %v, Store calls %d", err, store.updateEpisodeCalls)
	}
	_, err = service.UpdateEpisode(context.Background(), UpdateEpisodeInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Title: "",
		TargetDuration: time.Minute, ExpectedRevision: episode.Revision,
	})
	if !errors.Is(err, ErrInvalidRequest) || store.updateEpisodeCalls != 0 {
		t.Fatalf("invalid update = %v, Store calls %d", err, store.updateEpisodeCalls)
	}
}

func TestSaveScriptNormalizesLineEndingsAndUsesScriptRevision(t *testing.T) {
	t.Parallel()

	service, _, store, _, project, episode := openedEpisodeService(t)
	result, err := service.SaveScript(context.Background(), SaveScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID,
		Markdown: "## 场景一\r\n\r\n雨夜。\r旁白。", ExpectedScriptRevision: episode.Script.Revision,
	})
	if err != nil {
		t.Fatalf("SaveScript() error = %v", err)
	}
	if result.Episode.Script.Markdown != "## 场景一\n\n雨夜。\n旁白。" {
		t.Fatalf("normalized Markdown = %q", result.Episode.Script.Markdown)
	}
	if result.Episode.Script.Revision != episode.Script.Revision+1 || result.Episode.Revision != episode.Revision {
		t.Fatalf("revisions = Script %d, Episode %d", result.Episode.Script.Revision, result.Episode.Revision)
	}
	if store.saveScriptCalls != 1 || store.expectedRevision != episode.Script.Revision {
		t.Fatalf("Store save = calls %d, expected revision %d", store.saveScriptCalls, store.expectedRevision)
	}
}

func TestSaveScriptRejectsStaleRevisionAndInvalidContent(t *testing.T) {
	t.Parallel()

	service, _, store, _, project, episode := openedEpisodeService(t)
	_, err := service.SaveScript(context.Background(), SaveScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Markdown: "正文",
		ExpectedScriptRevision: episode.Script.Revision + 1,
	})
	if !errors.Is(err, ErrRevisionConflict) || store.saveScriptCalls != 0 {
		t.Fatalf("stale save = %v, Store calls %d", err, store.saveScriptCalls)
	}
	_, err = service.SaveScript(context.Background(), SaveScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Markdown: "a\x00b",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrInvalidRequest) || store.saveScriptCalls != 0 {
		t.Fatalf("invalid save = %v, Store calls %d", err, store.saveScriptCalls)
	}
}

func TestEpisodeMutationReturnsCommittedResultWhenLibrarySyncFails(t *testing.T) {
	t.Parallel()

	service, _, store, library, project, episode := openedEpisodeService(t)
	library.err = errInjected
	result, err := service.SaveScript(context.Background(), SaveScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Markdown: "正文",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrProjectUpdatedNotRegistered) || !errors.Is(err, errInjected) {
		t.Fatalf("SaveScript() error = %v, want registration and injected errors", err)
	}
	if result.Episode.Script.Markdown != "正文" || store.saveScriptCalls != 1 {
		t.Fatalf("committed result = %#v, Store calls %d", result, store.saveScriptCalls)
	}
}

func openedEpisodeService(t *testing.T) (*Service, Dependencies, *fakeStore, *fakeLibrary, domain.Project, domain.Episode) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	dependencies, _, factory, library := testDependencies(t, root)
	project, episode := testProjectResult(t)
	store := factory.openStore.(*fakeStore)
	dependencies.Clock = fakeClock{value: project.UpdatedAt.Add(time.Second)}
	if _, _, err := dependencies.Registry.Register(ProjectHandle{
		ProjectID: project.ID,
		RootPath:  root,
		Store:     store,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return service, dependencies, store, library, project, episode
}
