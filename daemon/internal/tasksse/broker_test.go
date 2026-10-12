package tasksse

import (
	"context"
	"strings"
	"testing"
	"time"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type testClock struct{ at time.Time }

func (c testClock) Now() time.Time { return c.at }

func TestBrokerReplaysAfterCapturedSnapshotCursor(t *testing.T) {
	at := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	broker, err := NewBroker(testClock{at: at}, strings.NewReader(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	job := validJob(t, at)
	reading := make(chan struct{})
	release := make(chan struct{})
	captured := make(chan string, 1)
	go func() {
		cursor, _ := broker.Capture(func() error {
			close(reading)
			<-release
			return nil
		})
		captured <- cursor
	}()
	<-reading
	published := make(chan struct{})
	go func() {
		broker.PublishGenerationJob(job)
		close(published)
	}()
	select {
	case <-published:
		t.Fatal("Publish passed the snapshot/cursor boundary")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	cursor := <-captured
	<-published
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, unsubscribe, err := broker.Subscribe(ctx, cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	event := <-events
	if event.Type != EventGenerationJobUpdated || event.Job == nil || event.Job.ID != job.ID {
		t.Fatalf("replayed event = %#v", event)
	}
}

func TestBrokerInvalidCursorRequiresResync(t *testing.T) {
	at := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	broker, err := NewBroker(testClock{at: at}, strings.NewReader(strings.Repeat("y", 32)))
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := broker.Subscribe(context.Background(), "another-process:4")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	event, open := <-events
	if !open || event.Type != EventTasksResyncRequired {
		t.Fatalf("resync event = %#v, open=%v", event, open)
	}
	if _, open := <-events; open {
		t.Fatal("resync stream remained open")
	}
}

func TestBrokerPublishesExportJobOnSharedStream(t *testing.T) {
	at := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	broker, err := NewBroker(testClock{at: at}, strings.NewReader(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := broker.Subscribe(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	job := validExportJob(t, at)
	broker.PublishExportJob(job)
	event := <-events
	if event.Type != EventExportJobUpdated || event.ExportJob == nil || event.ExportJob.ID != job.ID || event.Job != nil {
		t.Fatalf("Export event = %#v", event)
	}
}

func validJob(t *testing.T, at time.Time) project.GenerationJob {
	t.Helper()
	projectID, _ := project.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID, _ := project.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	batchID, _ := project.ParseGenerationBatchID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	jobID, _ := project.ParseGenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.ParseMediaSlotID("msl_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	job := project.GenerationJob{ID: jobID, BatchID: batchID, ProjectID: projectID, EpisodeID: episodeID, TargetSlotID: slotID, State: project.GenerationJobQueued, Snapshot: project.GenerationInputSnapshot{SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage, Prompt: "image", GeneratorID: project.GeneratorImageShotForward, CapabilityVersion: "1", OutputWidth: 720, OutputHeight: 1280, ParametersJSON: "{}", TargetSlotRevision: 1}, StopSupported: true, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	return job
}

func validExportJob(t *testing.T, at time.Time) project.ExportJob {
	t.Helper()
	projectID := project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID := project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	clipID := project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.FixedMediaSlotID(clipID.String(), project.MediaSlotClipVideo)
	snapshot := project.ExportSnapshot{SchemaVersion: project.ExportSnapshotVersion, StoryboardRevision: 2, OutputWidth: 720, OutputHeight: 1280, Clips: []project.ExportClipSnapshot{{SceneID: project.SceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 1, MediaVersionID: project.MediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), MediaVersionRevision: 1, ArtifactID: project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), TrimOutMilliseconds: 1000}}}
	job := project.ExportJob{ID: project.ExportJobID("exj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ProjectID: projectID, EpisodeID: episodeID, OperationKey: "export", State: project.ExportJobQueued, Snapshot: snapshot, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	return job
}
