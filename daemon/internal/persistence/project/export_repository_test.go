package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestExportJobFreezesCurrentVideoAndTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, _ := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	at := episode.UpdatedAt.Add(4 * time.Second)
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	artifact := testVideoArtifact(t, project.ID, "V", strings.Repeat("b", 64), at)
	version := testMediaVersion(t, slotID, artifact.ID, "V", 1, 1000, at)
	if err := store.AddMediaVersion(ctx, artifact, version, 1, true, at); err != nil {
		t.Fatal(err)
	}
	job := exportJobFixture(t, project, episode, clip, artifact, version, 2, at.Add(time.Second))
	if err := store.CreateExportJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	stored, err := store.ExportJobByOperationKey(ctx, project.ID, job.OperationKey)
	if err != nil || stored.ID != job.ID || stored.Snapshot.Clips[0].ArtifactID != artifact.ID {
		t.Fatalf("ExportJobByOperationKey() = %#v, %v", stored, err)
	}
	running, err := store.ClaimExportJob(ctx, job.ID, 1, at.Add(2*time.Second))
	if err != nil || running.State != domain.ExportJobRunning || running.StartedAt == nil {
		t.Fatalf("ClaimExportJob() = %#v, %v", running, err)
	}
	cancelling, err := store.RequestExportCancel(ctx, job.ID, running.Revision, at.Add(3*time.Second))
	if err != nil || cancelling.State != domain.ExportJobCancelRequested {
		t.Fatalf("RequestExportCancel() = %#v, %v", cancelling, err)
	}
	cancelled, err := store.FinishExportJob(ctx, job.ID, cancelling.Revision, domain.ExportJobCancelled, "", "", "", at.Add(4*time.Second))
	if err != nil || cancelled.State != domain.ExportJobCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("FinishExportJob() = %#v, %v", cancelled, err)
	}
}

func TestCreateExportJobRejectsChangedTrimAndActiveEpisode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, _ := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	at := episode.UpdatedAt.Add(4 * time.Second)
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	artifact := testVideoArtifact(t, project.ID, "W", strings.Repeat("c", 64), at)
	version := testMediaVersion(t, slotID, artifact.ID, "W", 1, 1000, at)
	if err := store.AddMediaVersion(ctx, artifact, version, 1, true, at); err != nil {
		t.Fatal(err)
	}
	stale := exportJobFixture(t, project, episode, clip, artifact, version, 2, at.Add(time.Second))
	version.TrimInMilliseconds, version.TrimOutMilliseconds, version.Revision = 100, 900, 2
	version.UpdatedAt = at.Add(time.Second)
	if err := store.UpdateVideoTrim(ctx, version, 1, version.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExportJob(ctx, stale); !errors.Is(err, domain.ErrInvalidExportSnapshot) {
		t.Fatalf("stale CreateExportJob() error = %v", err)
	}
	current := exportJobFixture(t, project, episode, clip, artifact, version, 2, at.Add(2*time.Second))
	if err := store.CreateExportJob(ctx, current); err != nil {
		t.Fatal(err)
	}
	second := current
	second.ID = domain.ExportJobID("exj_" + strings.Repeat("X", 26))
	second.OperationKey = "second-export"
	if err := store.CreateExportJob(ctx, second); !errors.Is(err, ErrExportJobConflict) {
		t.Fatalf("active CreateExportJob() error = %v", err)
	}
}

func exportJobFixture(t *testing.T, project domain.Project, episode domain.Episode, clip domain.Clip, artifact domain.Artifact, version domain.MediaVersion, slotRevision int64, at time.Time) domain.ExportJob {
	t.Helper()
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	return domain.ExportJob{
		ID: domain.ExportJobID("exj_" + strings.Repeat("E", 26)), ProjectID: project.ID, EpisodeID: episode.ID, OperationKey: "export-operation", State: domain.ExportJobQueued, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at,
		Snapshot: domain.ExportSnapshot{SchemaVersion: domain.ExportSnapshotVersion, StoryboardRevision: 3, OutputWidth: project.Output.Width(), OutputHeight: project.Output.Height(), Clips: []domain.ExportClipSnapshot{{
			SceneID: clip.SceneID, ClipID: clip.ID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: slotRevision, MediaVersionID: version.ID, MediaVersionRevision: version.Revision, ArtifactID: artifact.ID, TrimInMilliseconds: version.TrimInMilliseconds, TrimOutMilliseconds: version.TrimOutMilliseconds,
		}}},
	}
}
