package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestCreateEpisodePersistsAppendAndProjectTimestamp(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, first := testModels(t)
	if err := store.Initialize(context.Background(), project, first); err != nil {
		t.Fatal(err)
	}
	second := testSecondEpisode(t, project, first)
	updatedAt := project.UpdatedAt.Add(time.Second)
	if err := store.CreateEpisode(context.Background(), second, updatedAt); err != nil {
		t.Fatalf("CreateEpisode() error = %v", err)
	}

	got, err := store.Episode(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("Episode() = %#v, want %#v", got, second)
	}
	episodes, err := store.Episodes(context.Background())
	if err != nil || len(episodes) != 2 || episodes[1].ID != second.ID {
		t.Fatalf("Episodes() = %#v, %v", episodes, err)
	}
	gotProject, err := store.Project(context.Background())
	if err != nil || gotProject.UpdatedAt != updatedAt || gotProject.Revision != project.Revision {
		t.Fatalf("Project() = %#v, %v", gotProject, err)
	}
}

func TestCreateEpisodeRejectsNonAppendSequenceAtomically(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, first := testModels(t)
	if err := store.Initialize(context.Background(), project, first); err != nil {
		t.Fatal(err)
	}
	second := testSecondEpisode(t, project, first)
	second.SequenceNumber = 3
	second.Title = "第 3 集"
	if err := store.CreateEpisode(context.Background(), second, project.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrEpisodeSequenceConflict) {
		t.Fatalf("CreateEpisode() error = %v, want ErrEpisodeSequenceConflict", err)
	}
	episodes, err := store.Episodes(context.Background())
	if err != nil || len(episodes) != 1 {
		t.Fatalf("Episodes after rejected append = %#v, %v", episodes, err)
	}
	gotProject, _ := store.Project(context.Background())
	if gotProject.UpdatedAt != project.UpdatedAt {
		t.Fatalf("Project timestamp changed after rollback: %v", gotProject.UpdatedAt)
	}
}

func TestUpdateEpisodePreservesScriptAndGuardsRevision(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	episode.Script.Markdown = "原剧本"
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	updatedAt := project.UpdatedAt.Add(time.Second)
	metadata := episode.EpisodeMetadata
	metadata.Title = "序章"
	metadata.TargetDuration = 90 * time.Second
	metadata.Revision++
	metadata.UpdatedAt = updatedAt
	if err := store.UpdateEpisode(context.Background(), metadata, episode.Revision, updatedAt); err != nil {
		t.Fatalf("UpdateEpisode() error = %v", err)
	}
	got, err := store.Episode(context.Background(), episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EpisodeMetadata != metadata || got.Script != episode.Script {
		t.Fatalf("updated Episode = %#v", got)
	}

	stale := metadata
	stale.Title = "不应写入"
	stale.UpdatedAt = updatedAt.Add(time.Second)
	if err := store.UpdateEpisode(context.Background(), stale, episode.Revision, stale.UpdatedAt); !errors.Is(err, ErrEpisodeRevisionConflict) {
		t.Fatalf("stale UpdateEpisode() error = %v, want ErrEpisodeRevisionConflict", err)
	}
	unchanged, _ := store.Episode(context.Background(), episode.ID)
	if unchanged.Title != metadata.Title || unchanged.Revision != metadata.Revision {
		t.Fatalf("stale update changed Episode = %#v", unchanged)
	}
}

func TestSaveScriptPreservesMetadataRevisionAndGuardsScriptRevision(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	updatedAt := project.UpdatedAt.Add(time.Second)
	script := domain.Script{Markdown: "## 场景一\n\n雨夜。", Revision: episode.Script.Revision + 1, UpdatedAt: updatedAt}
	if err := store.SaveScript(context.Background(), episode.ID, script, episode.Script.Revision, updatedAt); err != nil {
		t.Fatalf("SaveScript() error = %v", err)
	}
	got, err := store.Episode(context.Background(), episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Script != script || got.Revision != episode.Revision || got.UpdatedAt != updatedAt {
		t.Fatalf("saved Episode = %#v", got)
	}

	staleScript := script
	staleScript.Markdown = "不应写入"
	staleScript.UpdatedAt = updatedAt.Add(time.Second)
	if err := store.SaveScript(context.Background(), episode.ID, staleScript, episode.Script.Revision, staleScript.UpdatedAt); !errors.Is(err, ErrScriptRevisionConflict) {
		t.Fatalf("stale SaveScript() error = %v, want ErrScriptRevisionConflict", err)
	}
	unchanged, _ := store.Episode(context.Background(), episode.ID)
	if unchanged.Script != script {
		t.Fatalf("stale save changed Script = %#v", unchanged.Script)
	}
}

func TestEpisodeTrashLifecyclePreservesOwnedContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, first := testModels(t)
	first.Script.Markdown = "原剧本"
	if err := store.Initialize(ctx, project, first); err != nil {
		t.Fatal(err)
	}
	second := testSecondEpisode(t, project, first)
	if err := store.CreateEpisode(ctx, second, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}

	deletedAt := second.UpdatedAt.Add(time.Second)
	metadata := second.EpisodeMetadata
	metadata.Revision++
	metadata.UpdatedAt = deletedAt
	metadata.DeletedAt = &deletedAt
	if err := store.DeleteEpisode(ctx, metadata, second.Revision, deletedAt); err != nil {
		t.Fatalf("DeleteEpisode() error = %v", err)
	}
	if _, err := store.Episode(ctx, second.ID); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("Episode(deleted) error = %v", err)
	}
	active, _ := store.Episodes(ctx)
	deleted, _ := store.DeletedEpisodes(ctx)
	if len(active) != 1 || len(deleted) != 1 || deleted[0].ID != second.ID || deleted[0].DeletedAt == nil {
		t.Fatalf("lifecycle projections = active %#v deleted %#v", active, deleted)
	}
	preserved, err := store.EpisodeIncludingDeleted(ctx, second.ID)
	if err != nil || preserved.Script != second.Script {
		t.Fatalf("EpisodeIncludingDeleted() = %#v, %v", preserved, err)
	}

	restoredAt := deletedAt.Add(time.Second)
	restored := preserved.EpisodeMetadata
	restored.Revision++
	restored.UpdatedAt = restoredAt
	restored.DeletedAt = nil
	if err := store.RestoreEpisode(ctx, restored, metadata.Revision, restoredAt); err != nil {
		t.Fatalf("RestoreEpisode() error = %v", err)
	}
	got, err := store.Episode(ctx, second.ID)
	if err != nil || got.Script != second.Script || got.DeletedAt != nil {
		t.Fatalf("restored Episode = %#v, %v", got, err)
	}
}

func TestDeleteEpisodeAllowsProjectToBecomeEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	at := episode.UpdatedAt.Add(time.Second)
	metadata := episode.EpisodeMetadata
	metadata.Revision++
	metadata.UpdatedAt, metadata.DeletedAt = at, &at
	if err := store.DeleteEpisode(ctx, metadata, episode.Revision, at); err != nil {
		t.Fatalf("DeleteEpisode() error = %v", err)
	}
	if episodes, err := store.Episodes(ctx); err != nil || len(episodes) != 0 {
		t.Fatalf("Episodes() = %#v, %v; want empty", episodes, err)
	}
}

func TestEpisodeMutationsDistinguishMissingEpisode(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	missing, err := domain.ParseEpisodeID("ep_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	metadata := episode.EpisodeMetadata
	metadata.ID = missing
	metadata.Revision++
	metadata.UpdatedAt = project.UpdatedAt.Add(time.Second)
	if err := store.UpdateEpisode(context.Background(), metadata, episode.Revision, metadata.UpdatedAt); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("UpdateEpisode(missing) error = %v", err)
	}
	script := domain.Script{Markdown: "正文", Revision: 2, UpdatedAt: metadata.UpdatedAt}
	if err := store.SaveScript(context.Background(), missing, script, 1, metadata.UpdatedAt); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("SaveScript(missing) error = %v", err)
	}
}

func TestEpisodeMutationRollsBackWhenProjectTimestampFails(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`
CREATE TRIGGER reject_project_timestamp
BEFORE UPDATE OF updated_at_ms ON projects
BEGIN
    SELECT RAISE(ABORT, 'injected Project timestamp failure');
END;
`); err != nil {
		t.Fatal(err)
	}
	updatedAt := project.UpdatedAt.Add(time.Second)
	script := domain.Script{Markdown: "不应提交", Revision: 2, UpdatedAt: updatedAt}
	if err := store.SaveScript(context.Background(), episode.ID, script, 1, updatedAt); err == nil {
		t.Fatal("SaveScript() error = nil, want injected failure")
	}
	got, err := store.Episode(context.Background(), episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != episode {
		t.Fatalf("Episode changed despite rollback = %#v", got)
	}
}

func testSecondEpisode(t *testing.T, project domain.Project, first domain.Episode) domain.Episode {
	t.Helper()
	id, err := domain.ParseEpisodeID("ep_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	now := first.UpdatedAt.Add(time.Second)
	return domain.Episode{
		EpisodeMetadata: domain.EpisodeMetadata{
			ID: id, ProjectID: project.ID, SequenceNumber: 2, Title: "第 2 集",
			TargetDuration: first.TargetDuration, Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
		Script: domain.Script{Markdown: "", Revision: 1, UpdatedAt: now},
	}
}
