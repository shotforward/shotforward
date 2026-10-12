package taskhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	"github.com/shotforward/shotforward/daemon/internal/tasksse"
)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

func TestSnapshotReturnsAtomicCursorAndEmptyAuthoritativeTasks(t *testing.T) {
	broker, err := tasksse.NewBroker(fixedClock{at: time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)}, strings.NewReader(strings.Repeat("z", 32)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(projectapp.NewProjectStoreRegistry(), broker)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/snapshot", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tasks":[]`) || !strings.Contains(response.Body.String(), `"cursor":"`) || !strings.Contains(response.Body.String(), `"stream_epoch":"`) {
		t.Fatalf("snapshot = %d %s", response.Code, response.Body.String())
	}
}

func TestExportTaskProjectionOmitsFrozenMediaDetails(t *testing.T) {
	at := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	clipID := project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.FixedMediaSlotID(clipID.String(), project.MediaSlotClipVideo)
	snapshot := project.ExportSnapshot{SchemaVersion: project.ExportSnapshotVersion, StoryboardRevision: 3, OutputWidth: 720, OutputHeight: 1280, Clips: []project.ExportClipSnapshot{{SceneID: project.SceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 2, MediaVersionID: project.MediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), MediaVersionRevision: 1, ArtifactID: project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), TrimOutMilliseconds: 1000}}}
	job := project.ExportJob{ID: project.ExportJobID("exj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ProjectID: project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), EpisodeID: project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"), OperationKey: "export", State: project.ExportJobQueued, Snapshot: snapshot, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	task := mapExportTask(job)
	if task["task_type"] != "export_job" || task["included_clip_count"] != 1 || task["skipped_clip_count"] != 0 {
		t.Fatalf("Export task = %#v", task)
	}
	if _, exists := task["snapshot"]; exists {
		t.Fatalf("Export task leaked frozen media details: %#v", task)
	}
}

func TestGenerationTaskProjectionIncludesProviderModel(t *testing.T) {
	at := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	job := project.GenerationJob{
		ID:           project.GenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		BatchID:      project.GenerationBatchID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		ProjectID:    project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		EpisodeID:    project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		TargetSlotID: project.MediaSlotID("msl_ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		State:        project.GenerationJobQueued,
		Snapshot: project.GenerationInputSnapshot{
			GeneratorID:     project.GeneratorVideoWanOfficial,
			ProviderModelID: "wan3.0-video-prime",
			TargetMediaKind: project.MediaKindVideo,
		},
		ProgressStage: "queued",
		Revision:      1,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	task := mapGenerationTask(job)
	if task["provider_model_id"] != "wan3.0-video-prime" {
		t.Fatalf("Generation task = %#v", task)
	}
}

func TestGenerationTaskProjectionOnlyPublishesSanitizedProviderFailure(t *testing.T) {
	t.Parallel()
	job := project.GenerationJob{FailureCode: generation.ProviderResponseFailureCode, FailureMessage: "Seedance (HTTP 400): SensitiveContent: real person", State: project.GenerationJobFailed}
	task := mapGenerationTask(job)
	if task["failure_message"] != job.FailureMessage {
		t.Fatalf("provider failure = %#v", task["failure_message"])
	}
	job.FailureCode = "generator_start_failed"
	job.FailureMessage = "/private/project/path"
	task = mapGenerationTask(job)
	if task["failure_message"] != "" {
		t.Fatalf("internal failure leaked = %#v", task["failure_message"])
	}
	job.FailureMessage = "start Seedance: prepare reference video delivery: ShotForward cloud login required"
	task = mapGenerationTask(job)
	if task["failure_code"] != generation.ReferenceDeliveryLoginRequiredFailureCode || task["failure_message"] != "" {
		t.Fatalf("historical failure projection = %#v", task)
	}
}

func TestEventsWithPriorProcessCursorReturnResyncAndClose(t *testing.T) {
	broker, err := tasksse.NewBroker(fixedClock{at: time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)}, strings.NewReader(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(projectapp.NewProjectStoreRegistry(), broker)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/events", nil).WithContext(context.Background())
	request.Header.Set("Last-Event-ID", "prior:1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: tasks.resync_required") {
		t.Fatalf("events = %d %s", response.Code, response.Body.String())
	}
}

func TestEventsAcceptBrowserCursorQuery(t *testing.T) {
	broker, err := tasksse.NewBroker(fixedClock{at: time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)}, strings.NewReader(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(projectapp.NewProjectStoreRegistry(), broker)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/events?after=prior%3A1", nil).WithContext(context.Background())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: tasks.resync_required") {
		t.Fatalf("events = %d %s", response.Code, response.Body.String())
	}
}
