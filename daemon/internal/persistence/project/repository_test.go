package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestInitializeAndReadProjectAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	store := newTestStore(t, path)
	project, episode := testModels(t)
	episode.Script.Markdown = "## 场景一\n\n雨夜。"

	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatalf("Open(OpenExisting) error = %v", err)
	}
	defer reopened.Close()

	gotProject, err := reopened.Project(ctx)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !reflect.DeepEqual(gotProject, project) {
		t.Fatalf("Project() = %#v, want %#v", gotProject, project)
	}

	episodes, err := reopened.Episodes(ctx)
	if err != nil {
		t.Fatalf("Episodes() error = %v", err)
	}
	if len(episodes) != 1 || !reflect.DeepEqual(episodes[0], episode.Summary()) {
		t.Fatalf("Episodes() = %#v, want %#v", episodes, []domain.EpisodeSummary{episode.Summary()})
	}

	gotEpisode, err := reopened.Episode(ctx, episode.ID)
	if err != nil {
		t.Fatalf("Episode() error = %v", err)
	}
	if !reflect.DeepEqual(gotEpisode, episode) {
		t.Fatalf("Episode() = %#v, want %#v", gotEpisode, episode)
	}
}

func TestUpdateProjectUsesRevisionAndPreservesImmutableFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	updated := project
	updated.Name = "新的项目名"
	updated.Description = "新的简介"
	updated.Revision++
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Second)
	requestedOutput, err := domain.NewOutputSpec(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	updated.Output = requestedOutput
	if err := store.UpdateProject(ctx, updated, project.Revision); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	got, err := store.Project(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := updated
	want.Output = project.Output
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Project() = %#v, want %#v", got, want)
	}
	stale := want
	stale.Name = "stale"
	stale.UpdatedAt = stale.UpdatedAt.Add(time.Second)
	if err := store.UpdateProject(ctx, stale, project.Revision); !errors.Is(err, ErrProjectRevisionConflict) {
		t.Fatalf("stale UpdateProject() error = %v", err)
	}
}

func TestInitializeIsAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)

	if _, err := store.database.Exec(`
CREATE TRIGGER reject_default_episode
BEFORE INSERT ON episodes
BEGIN
    SELECT RAISE(ABORT, 'injected Episode failure');
END;
`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if err := store.Initialize(ctx, project, episode); err == nil {
		t.Fatal("Initialize() error = nil, want injected failure")
	}

	var projectCount int
	if err := store.database.QueryRow("SELECT count(*) FROM projects").Scan(&projectCount); err != nil {
		t.Fatalf("count Projects: %v", err)
	}
	if projectCount != 0 {
		t.Fatalf("Project count after rollback = %d, want 0", projectCount)
	}
	if _, err := store.Project(ctx); !errors.Is(err, ErrProjectNotInitialized) {
		t.Fatalf("Project() after rollback error = %v, want ErrProjectNotInitialized", err)
	}
}

func TestInitializeOnlyOnce(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatalf("first Initialize() error = %v", err)
	}
	if err := store.Initialize(context.Background(), project, episode); !errors.Is(err, ErrProjectAlreadyInitialized) {
		t.Fatalf("second Initialize() error = %v, want ErrProjectAlreadyInitialized", err)
	}
}

func TestSchemaEnforcesSingletonProjectAndEpisodeOwnership(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if _, err := store.database.Exec(`
INSERT INTO projects (
    project_id, singleton_key, name, description, output_width, output_height,
    revision, created_at_ms, updated_at_ms
) VALUES ('prj_22222222222222222222222222', 1, 'Second', '', 720, 1280, 1, 1, 1)
`); err == nil {
		t.Fatal("second Project insert error = nil, want singleton constraint error")
	}

	if _, err := store.database.Exec(`
INSERT INTO episodes (
    episode_id, project_id, sequence_number, title, target_duration_ms,
    script_markdown, script_revision, script_updated_at_ms,
    revision, created_at_ms, updated_at_ms
) VALUES (
    'ep_77777777777777777777777777',
    'prj_22222222222222222222222222',
    2, 'Orphan', 60000, '', 1, 1, 1, 1, 1
)
`); err == nil {
		t.Fatal("orphan Episode insert error = nil, want foreign-key constraint error")
	}
}

func TestInitializeRejectsMismatchedDefaultEpisode(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	otherProjectID, err := domain.ParseProjectID("prj_22222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	episode.ProjectID = otherProjectID
	if err := store.Initialize(context.Background(), project, episode); err == nil {
		t.Fatal("Initialize(mismatched Project ID) error = nil, want error")
	}

	_, episode = testModels(t)
	episode.SequenceNumber = 2
	if err := store.Initialize(context.Background(), project, episode); err == nil {
		t.Fatal("Initialize(sequence 2) error = nil, want error")
	}
}

func TestReadMissingProjectAndEpisode(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	ctx := context.Background()
	if _, err := store.Project(ctx); !errors.Is(err, ErrProjectNotInitialized) {
		t.Fatalf("Project() error = %v, want ErrProjectNotInitialized", err)
	}
	episodes, err := store.Episodes(ctx)
	if err != nil {
		t.Fatalf("Episodes() error = %v", err)
	}
	if episodes == nil || len(episodes) != 0 {
		t.Fatalf("Episodes() = %#v, want non-nil empty slice", episodes)
	}
	missing, err := domain.ParseEpisodeID("ep_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Episode(ctx, missing); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("Episode() error = %v, want ErrEpisodeNotFound", err)
	}
}

func TestReadsRejectInvalidStoredDomainData(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	_, err := store.database.Exec(`
INSERT INTO projects (
    project_id, singleton_key, name, description, output_width, output_height,
    revision, created_at_ms, updated_at_ms
) VALUES ('not-a-domain-id', 1, 'Invalid', '', 720, 1280, 1, 1, 1)
`)
	if err != nil {
		t.Fatalf("insert invalid stored Project: %v", err)
	}
	if _, err := store.Project(context.Background()); !errors.Is(err, ErrInvalidStoredProjectData) {
		t.Fatalf("Project() error = %v, want ErrInvalidStoredProjectData", err)
	}
}

func TestRepositoryRejectsInvalidAndCancelledInput(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	project.Name = ""
	if err := store.Initialize(context.Background(), project, episode); !errors.Is(err, domain.ErrInvalidProject) {
		t.Fatalf("Initialize(invalid Project) error = %v, want ErrInvalidProject", err)
	}
	if _, err := store.Episode(context.Background(), "invalid"); !errors.Is(err, domain.ErrInvalidEpisodeID) {
		t.Fatalf("Episode(invalid ID) error = %v, want ErrInvalidEpisodeID", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Episodes(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Episodes(cancelled context) error = %v, want context.Canceled", err)
	}
	if _, err := store.Project(nil); err == nil {
		t.Fatal("Project(nil context) error = nil, want error")
	}
}

func newTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path, sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	return store
}

func testModels(t *testing.T) (domain.Project, domain.Episode) {
	t.Helper()
	projectID, err := domain.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	output, err := domain.NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 123_000_000, time.UTC)
	project := domain.Project{
		ID:          projectID,
		Name:        "雨夜来信",
		Description: "一部雨夜短片",
		Output:      output,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	episode := domain.Episode{
		EpisodeMetadata: domain.EpisodeMetadata{
			ID:             episodeID,
			ProjectID:      projectID,
			SequenceNumber: 1,
			Title:          "第 1 集",
			TargetDuration: 60 * time.Second,
			Revision:       1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Script: domain.Script{
			Markdown:  "",
			Revision:  1,
			UpdatedAt: now,
		},
	}
	return project, episode
}
