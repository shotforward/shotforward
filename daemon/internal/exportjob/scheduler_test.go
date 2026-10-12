package exportjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
)

func TestSchedulerCompletesRecoverableExportAndPublishesProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	registry, store, root, job := schedulerFixture(t)
	processor := &fakeProcessor{}
	events := &recordingEvents{}
	scheduler, err := NewScheduler(Options{Registry: registry, Processor: processor, Clock: &incrementingClock{at: job.CreatedAt}, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := scheduler.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	completed, err := store.ExportJob(ctx, job.ID)
	if err != nil || completed.State != domain.ExportJobSucceeded || completed.OutputRelativePath == "" || completed.Revision != 4 || processor.calls != 1 {
		t.Fatalf("completed ExportJob = %#v, err=%v calls=%d", completed, err, processor.calls)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(completed.OutputRelativePath))); err != nil {
		t.Fatal(err)
	}
	if len(events.jobs) != 3 || events.jobs[0].State != domain.ExportJobRunning || events.jobs[2].State != domain.ExportJobSucceeded {
		t.Fatalf("events = %#v", events.jobs)
	}
}

func TestSchedulerFinalizesRecoveredCancelWithoutRunningFFmpeg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	registry, store, _, job := schedulerFixture(t)
	running, err := store.ClaimExportJob(ctx, job.ID, 1, job.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	cancelling, err := store.RequestExportCancel(ctx, job.ID, running.Revision, job.CreatedAt.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	processor := &fakeProcessor{}
	scheduler, _ := NewScheduler(Options{Registry: registry, Processor: processor, Clock: &incrementingClock{at: job.CreatedAt.Add(2 * time.Second)}})
	processed, err := scheduler.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	completed, _ := store.ExportJob(ctx, job.ID)
	if completed.State != domain.ExportJobCancelled || completed.Revision != cancelling.Revision+1 || processor.calls != 0 {
		t.Fatalf("cancelled ExportJob = %#v, processor calls=%d", completed, processor.calls)
	}
}

func schedulerFixture(t *testing.T) (*projectapp.ProjectStoreRegistry, projectapp.ProjectStore, string, domain.ExportJob) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	layout, err := platformpaths.NewProjectLayout(root)
	if err != nil || layout.EnsureDirectories() != nil {
		t.Fatal(err)
	}
	store, err := (projectstore.ApplicationStoreFactory{}).Create(ctx, layout.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Unix(1_800_000_000, 0).UTC()
	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	episodeID := domain.EpisodeID("ep_" + strings.Repeat("D", 26))
	output, _ := domain.NewOutputSpec(720, 1280)
	project := domain.Project{ID: projectID, Name: "Export", Output: output, Revision: 1, CreatedAt: at, UpdatedAt: at}
	episode := domain.Episode{EpisodeMetadata: domain.EpisodeMetadata{ID: episodeID, ProjectID: projectID, SequenceNumber: 1, Title: "Episode", TargetDuration: time.Minute, Revision: 1, CreatedAt: at, UpdatedAt: at}, Script: domain.Script{Revision: 1, UpdatedAt: at}}
	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	sceneID := domain.SceneID("scn_" + strings.Repeat("S", 26))
	scene := domain.Scene{ID: sceneID, ProjectID: projectID, EpisodeID: episodeID, Position: 1, Title: "Scene", Revision: 1, CreatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second)}
	if err := store.CreateScene(ctx, scene, 1, scene.CreatedAt); err != nil {
		t.Fatal(err)
	}
	clipID := domain.ClipID("clp_" + strings.Repeat("C", 26))
	clip := domain.Clip{ID: clipID, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Position: 1, Title: "Clip", PlannedDurationSeconds: 1, GenerationMode: domain.ClipGenerationFirstImage, Revision: 1, CreatedAt: at.Add(2 * time.Second), UpdatedAt: at.Add(2 * time.Second)}
	if err := store.CreateClip(ctx, clip, 2, clip.CreatedAt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(layout.Videos, "source.mp4")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	artifact := domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/source.mp4", SHA256: strings.Repeat("a", 64), ByteSize: 6, MIMEType: "video/mp4", OriginalFilename: "source.mp4", Width: 720, Height: 1280, DurationMilliseconds: 1000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: at.Add(3 * time.Second)}
	slotID, _ := domain.FixedMediaSlotID(clipID.String(), domain.MediaSlotClipVideo)
	versionID := domain.MediaVersionID("mvr_" + strings.Repeat("V", 26))
	version := domain.MediaVersion{ID: versionID, SlotID: slotID, ArtifactID: artifactID, SequenceNumber: 1, TrimOutMilliseconds: 1000, Revision: 1, CreatedAt: artifact.CreatedAt, UpdatedAt: artifact.CreatedAt}
	if err := store.AddMediaVersion(ctx, artifact, version, 1, true, artifact.CreatedAt); err != nil {
		t.Fatal(err)
	}
	created := at.Add(4 * time.Second)
	job := domain.ExportJob{ID: domain.ExportJobID("exj_" + strings.Repeat("E", 26)), ProjectID: projectID, EpisodeID: episodeID, OperationKey: "export", State: domain.ExportJobQueued, ProgressStage: "queued", ProgressMessage: "Waiting", Revision: 1, CreatedAt: created, UpdatedAt: created, Snapshot: domain.ExportSnapshot{SchemaVersion: domain.ExportSnapshotVersion, StoryboardRevision: 3, OutputWidth: 720, OutputHeight: 1280, Clips: []domain.ExportClipSnapshot{{SceneID: sceneID, ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 2, MediaVersionID: versionID, MediaVersionRevision: 1, ArtifactID: artifactID, TrimOutMilliseconds: 1000}}}}
	if err := store.CreateExportJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	if _, _, err := registry.Register(projectapp.ProjectHandle{ProjectID: projectID, RootPath: root, Store: store}); err != nil {
		t.Fatal(err)
	}
	return registry, store, root, job
}

type fakeProcessor struct{ calls int }

func (p *fakeProcessor) Export(_ context.Context, request Request, progress Progress) (string, error) {
	p.calls++
	if err := progress("transcoding", "Preparing clip 1 of 1"); err != nil {
		return "", err
	}
	relative := "exports/" + request.JobID.String() + ".mp4"
	return relative, os.WriteFile(filepath.Join(request.ProjectRoot, filepath.FromSlash(relative)), []byte("video"), 0o600)
}
func (*fakeProcessor) RemoveOutput(root, relative string) error {
	return os.Remove(filepath.Join(root, filepath.FromSlash(relative)))
}

type incrementingClock struct {
	mutex sync.Mutex
	at    time.Time
}

func (c *incrementingClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.at = c.at.Add(time.Second)
	return c.at
}

type recordingEvents struct{ jobs []domain.ExportJob }

func (e *recordingEvents) PublishExportJob(job domain.ExportJob) { e.jobs = append(e.jobs, job) }
