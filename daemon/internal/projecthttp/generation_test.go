package projecthttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type generationProjects struct {
	ProjectService
	submitted   projectapp.SubmitGenerationBatchInput
	result      project.GenerationBatchDetails
	readyErr    error
	submitErr   error
	resetInput  projectapp.ResetStoryboardInput
	resetResult projectapp.ResetStoryboardResult
	resetErr    error
}

func (s *generationProjects) ResetStoryboard(_ context.Context, input projectapp.ResetStoryboardInput) (projectapp.ResetStoryboardResult, error) {
	s.resetInput = input
	return s.resetResult, s.resetErr
}

func (s *generationProjects) SubmitGenerationBatch(_ context.Context, input projectapp.SubmitGenerationBatchInput) (project.GenerationBatchDetails, error) {
	s.submitted = input
	return s.result, s.submitErr
}

func (s *generationProjects) CheckGenerationReadiness(context.Context, projectapp.CheckGenerationReadinessInput) (projectapp.GenerationReadinessResult, error) {
	return projectapp.GenerationReadinessResult{}, s.readyErr
}

type emptyCatalog struct{}

func (emptyCatalog) Projects(context.Context) ([]projectapp.ProjectCatalogEntry, error) {
	return nil, nil
}
func (emptyCatalog) Project(context.Context, project.ProjectID) (projectapp.ProjectCatalogEntry, error) {
	return projectapp.ProjectCatalogEntry{}, nil
}

func TestSubmitGenerationBatchUsesStrictPublicContract(t *testing.T) {
	service, projectID, episodeID, slotID := generationHTTPFixture(t)
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"episode_id":"` + episodeID.String() + `","operation_key":"ui-1","targets":[{"media_slot_id":"` + slotID.String() + `","expected_slot_revision":3,"prompt":"雨夜","generator_id":"image.shotforward","parameters":{}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %s", response.Code, response.Body.String())
	}
	if service.submitted.EpisodeID != episodeID || service.submitted.OperationKey != "ui-1" || len(service.submitted.Targets) != 1 || service.submitted.Targets[0].MediaSlotID != slotID || service.submitted.Targets[0].ParametersJSON != "{}" {
		t.Fatalf("application input = %#v", service.submitted)
	}
	if strings.Contains(response.Body.String(), "generator_operation") {
		t.Fatalf("response leaked opaque Generator operation identity: %s", response.Body.String())
	}

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/jobs", strings.NewReader(strings.TrimSuffix(body, "}")+`,"retry_of_job_id":"gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ"}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}
}

func TestGenerationReadinessMapsStableError(t *testing.T) {
	service, projectID, episodeID, slotID := generationHTTPFixture(t)
	service.readyErr = projectapp.ErrGenerationNotReady
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	body := `{"episode_id":"` + episodeID.String() + `","target":{"media_slot_id":"` + slotID.String() + `","expected_slot_revision":1,"generator_id":"video.wan-3.0.official","generation_mode":"first_image","requested_duration_seconds":5,"parameters":{}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/readiness", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"generation_not_ready"`) {
		t.Fatalf("readiness = %d %s", response.Code, response.Body.String())
	}
}

func TestGenerationReadinessReturnsCapabilityLimitDetails(t *testing.T) {
	service, projectID, episodeID, slotID := generationHTTPFixture(t)
	issue := &projectapp.GenerationCapabilityIssue{
		Constraint: "reference_total_duration", Role: project.GenerationReferenceAudio,
		Actual: 29_832, Maximum: 15_000,
	}
	service.readyErr = fmt.Errorf("check Generation readiness: %w: %w", projectapp.ErrGenerationCapabilityMismatch, issue)
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	body := `{"episode_id":"` + episodeID.String() + `","target":{"media_slot_id":"` + slotID.String() + `","expected_slot_revision":1,"generator_id":"video.h3.shotforward","provider_model_id":"MiniMax-H3-Turbo-Ref2VA","generation_mode":"reference","requested_duration_seconds":10,"parameters":{}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/readiness", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":"capability_unsupported"`) ||
		!strings.Contains(response.Body.String(), `"constraint":"reference_total_duration"`) ||
		!strings.Contains(response.Body.String(), `"role":"audio_reference"`) ||
		!strings.Contains(response.Body.String(), `"actual":29832`) ||
		!strings.Contains(response.Body.String(), `"maximum":15000`) {
		t.Fatalf("readiness capability details = %d %s", response.Code, response.Body.String())
	}
}

func TestStoryboardResetReturnsAcceptedWhileStopsArePending(t *testing.T) {
	service, projectID, episodeID, _ := generationHTTPFixture(t)
	service.resetResult = projectapp.ResetStoryboardResult{Storyboard: project.Storyboard{EpisodeID: episodeID, Revision: 3}, ChangedJobs: service.result.Jobs, WaitingForTerminalJobs: true}
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/episodes/"+episodeID.String()+"/storyboard:reset", strings.NewReader(`{"expected_storyboard_revision":3,"confirm":true,"request_stop_for_running_jobs":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"waiting_for_terminal_jobs":true`) || !service.resetInput.Confirm || !service.resetInput.RequestStopForRunningJobs {
		t.Fatalf("reset = %d %s, input=%#v", response.Code, response.Body.String(), service.resetInput)
	}
}

func generationHTTPFixture(t *testing.T) (*generationProjects, project.ProjectID, project.EpisodeID, project.MediaSlotID) {
	t.Helper()
	projectID, _ := project.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID, _ := project.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.ParseMediaSlotID("msl_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	batchID, _ := project.ParseGenerationBatchID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	jobID, _ := project.ParseGenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	at := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	snapshot := project.GenerationInputSnapshot{SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage, Prompt: "雨夜", GeneratorID: project.GeneratorImageShotForward, CapabilityVersion: "1", OutputWidth: 720, OutputHeight: 1280, ParametersJSON: "{}", TargetSlotRevision: 3}
	batch := project.GenerationBatch{ID: batchID, ProjectID: projectID, EpisodeID: episodeID, OperationKey: "ui-1", RequestHash: strings.Repeat("a", 64), CreatedAt: at}
	job := project.GenerationJob{ID: jobID, BatchID: batchID, ProjectID: projectID, EpisodeID: episodeID, TargetSlotID: slotID, State: project.GenerationJobQueued, Snapshot: snapshot, StopSupported: true, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	return &generationProjects{result: project.GenerationBatchDetails{Batch: batch, Jobs: []project.GenerationJob{job}}}, projectID, episodeID, slotID
}
