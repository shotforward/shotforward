package projecthttp

import (
	"encoding/json"
	"net/http"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func (h *Handler) serveGenerationRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 6 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" || segments[4] != "generation" {
		return false
	}
	projectID, err := project.ParseProjectID(segments[3])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	switch {
	case len(segments) == 6 && segments[5] == "readiness":
		h.generationReadiness(response, request, projectID)
	case len(segments) == 6 && segments[5] == "jobs":
		h.submitGenerationBatch(response, request, projectID)
	case len(segments) == 7 && segments[5] == "jobs":
		h.generationJob(response, request, projectID, segments[6])
	case len(segments) == 8 && segments[5] == "jobs" && segments[7] == "cancel":
		h.changeGenerationJob(response, request, projectID, segments[6], "cancel")
	case len(segments) == 8 && segments[5] == "jobs" && segments[7] == "stop":
		h.changeGenerationJob(response, request, projectID, segments[6], "stop")
	case len(segments) == 8 && segments[5] == "jobs" && segments[7] == "retry":
		h.changeGenerationJob(response, request, projectID, segments[6], "retry")
	case len(segments) == 7 && segments[5] == "batches":
		h.generationBatch(response, request, projectID, segments[6])
	case len(segments) == 8 && segments[5] == "batches" && segments[7] == "cancel":
		h.cancelGenerationBatch(response, request, projectID, segments[6])
	default:
		return false
	}
	return true
}

type generationReferenceDTO struct {
	Role        project.GenerationArtifactRole `json:"role"`
	ArtifactID  string                         `json:"artifact_id"`
	TrimStartMS int64                          `json:"trim_start_ms"`
	TrimEndMS   int64                          `json:"trim_end_ms"`
}

type generationTargetDTO struct {
	MediaSlotID             string                     `json:"media_slot_id"`
	ExpectedSlotRevision    int64                      `json:"expected_slot_revision"`
	Prompt                  string                     `json:"prompt"`
	GenerationMode          project.ClipGenerationMode `json:"generation_mode"`
	GeneratorID             project.GeneratorID        `json:"generator_id"`
	ProviderModelID         string                     `json:"provider_model_id"`
	RequestedDurationSecond int64                      `json:"requested_duration_seconds"`
	References              []generationReferenceDTO   `json:"references"`
	Parameters              json.RawMessage            `json:"parameters"`
}

func (d generationTargetDTO) applicationTarget() (projectapp.SubmitGenerationTarget, error) {
	slotID, err := project.ParseMediaSlotID(d.MediaSlotID)
	if err != nil {
		return projectapp.SubmitGenerationTarget{}, err
	}
	references := make([]project.GenerationArtifactReference, 0, len(d.References))
	for _, value := range d.References {
		artifactID, err := project.ParseArtifactID(value.ArtifactID)
		if err != nil {
			return projectapp.SubmitGenerationTarget{}, err
		}
		references = append(references, project.GenerationArtifactReference{Role: value.Role, ArtifactID: artifactID, TrimStartMilliseconds: value.TrimStartMS, TrimEndMilliseconds: value.TrimEndMS})
	}
	parameters := string(d.Parameters)
	if len(d.Parameters) == 0 {
		parameters = "{}"
	}
	return projectapp.SubmitGenerationTarget{MediaSlotID: slotID, ExpectedSlotRevision: d.ExpectedSlotRevision, Prompt: d.Prompt, GenerationMode: d.GenerationMode, GeneratorID: d.GeneratorID, ProviderModelID: d.ProviderModelID, RequestedDurationSeconds: d.RequestedDurationSecond, References: references, ParametersJSON: parameters}, nil
}

func (h *Handler) generationReadiness(response http.ResponseWriter, request *http.Request, projectID project.ProjectID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		EpisodeID string              `json:"episode_id"`
		Target    generationTargetDTO `json:"target"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	episodeID, err := project.ParseEpisodeID(body.EpisodeID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	target, err := body.Target.applicationTarget()
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.CheckGenerationReadiness(request.Context(), projectapp.CheckGenerationReadinessInput{ProjectID: projectID, EpisodeID: episodeID, Target: target})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"ready": true, "project_id": result.ProjectID.String(), "episode_id": result.EpisodeID.String(), "media_slot_id": result.Slot.ID.String(), "slot_revision": result.Slot.Revision, "snapshot": mapGenerationSnapshot(result.Snapshot)})
}

func (h *Handler) submitGenerationBatch(response http.ResponseWriter, request *http.Request, projectID project.ProjectID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		EpisodeID         string                                         `json:"episode_id"`
		OperationKey      string                                         `json:"operation_key"`
		Targets           []generationTargetDTO                          `json:"targets"`
		PromptTranslation *projectapp.GenerationPromptTranslationOptions `json:"prompt_translation"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	episodeID, err := project.ParseEpisodeID(body.EpisodeID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	targets := make([]projectapp.SubmitGenerationTarget, 0, len(body.Targets))
	for _, value := range body.Targets {
		target, err := value.applicationTarget()
		if err != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		targets = append(targets, target)
	}
	result, err := h.projects.SubmitGenerationBatch(request.Context(), projectapp.SubmitGenerationBatchInput{ProjectID: projectID, EpisodeID: episodeID, OperationKey: body.OperationKey, Targets: targets, PromptTranslation: body.PromptTranslation})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, mapGenerationBatch(result))
}

func (h *Handler) generationBatch(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawBatchID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	batchID, err := project.ParseGenerationBatchID(rawBatchID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.GetGenerationBatch(request.Context(), projectapp.GetGenerationBatchInput{ProjectID: projectID, GenerationBatchID: batchID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapGenerationBatch(result))
}

func (h *Handler) generationJob(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawJobID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	jobID, err := project.ParseGenerationJobID(rawJobID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	job, err := h.projects.GetGenerationJob(request.Context(), projectapp.GetGenerationJobInput{ProjectID: projectID, GenerationJobID: jobID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": mapGenerationJob(job)})
}

func (h *Handler) changeGenerationJob(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawJobID, action string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	jobID, err := project.ParseGenerationJobID(rawJobID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	if action == "retry" {
		var body struct {
			ExpectedRevision int64  `json:"expected_revision"`
			OperationKey     string `json:"operation_key"`
		}
		if err := decodeJSON(response, request, &body); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.RetryGenerationJob(request.Context(), projectapp.RetryGenerationJobInput{ProjectID: projectID, GenerationJobID: jobID, ExpectedRevision: body.ExpectedRevision, OperationKey: body.OperationKey})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted, mapGenerationBatch(result))
		return
	}
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	input := projectapp.ChangeGenerationJobInput{ProjectID: projectID, GenerationJobID: jobID, ExpectedRevision: body.ExpectedRevision}
	var job project.GenerationJob
	if action == "cancel" {
		job, err = h.projects.CancelGenerationJob(request.Context(), input)
	} else {
		job, err = h.projects.RequestGenerationStop(request.Context(), input)
	}
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": mapGenerationJob(job)})
}

func (h *Handler) cancelGenerationBatch(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawBatchID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	batchID, err := project.ParseGenerationBatchID(rawBatchID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		RequestStopForRunningJobs bool `json:"request_stop_for_running_jobs"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.CancelGenerationBatch(request.Context(), projectapp.CancelGenerationBatchInput{ProjectID: projectID, GenerationBatchID: batchID, RequestStopForRunningJobs: body.RequestStopForRunningJobs})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapGenerationBatch(result))
}

func mapGenerationBatch(value project.GenerationBatchDetails) map[string]any {
	jobs := make([]map[string]any, 0, len(value.Jobs))
	for _, job := range value.Jobs {
		jobs = append(jobs, mapGenerationJob(job))
	}
	return map[string]any{"batch": map[string]any{"generation_batch_id": value.Batch.ID.String(), "project_id": value.Batch.ProjectID.String(), "episode_id": value.Batch.EpisodeID.String(), "operation_key": value.Batch.OperationKey, "created_at": value.Batch.CreatedAt}, "jobs": jobs}
}

func mapGenerationJob(value project.GenerationJob) map[string]any {
	failureCode, failureMessage := publicGenerationFailure(value.FailureCode, value.FailureMessage)
	result := map[string]any{"generation_job_id": value.ID.String(), "generation_batch_id": value.BatchID.String(), "project_id": value.ProjectID.String(), "episode_id": value.EpisodeID.String(), "target_media_slot_id": value.TargetSlotID.String(), "state": value.State, "snapshot": mapGenerationSnapshot(value.Snapshot), "stop_supported": value.StopSupported, "progress_stage": value.ProgressStage, "progress_message": value.ProgressMessage, "failure_code": failureCode, "failure_message": failureMessage, "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
	if value.RetryOfJobID != "" {
		result["retry_of_job_id"] = value.RetryOfJobID.String()
	}
	if value.ResultArtifactID != "" {
		result["result_artifact_id"] = value.ResultArtifactID.String()
		result["result_media_version_id"] = value.ResultVersionID.String()
	}
	if value.StartedAt != nil {
		result["started_at"] = *value.StartedAt
	}
	if value.FinishedAt != nil {
		result["finished_at"] = *value.FinishedAt
	}
	return result
}

func publicGenerationFailure(code, storedMessage string) (string, string) {
	publicCode, message := generation.PublicGenerationFailure(code, storedMessage)
	if publicCode == "" || message != "" {
		return publicCode, message
	}
	switch publicCode {
	case "generator_unavailable":
		message = "The selected generator is unavailable."
	case "execution_interrupted":
		message = "Generation was interrupted and could not be recovered."
	case "reference_unavailable":
		message = "A required reference is unavailable."
	case "invalid_generator_output", "generator_protocol_error":
		message = "The generator returned an invalid result."
	case "generator_request_too_large":
		message = "The generation request is too large."
	case "provider_unreachable":
		message = "The generation service could not be reached."
	case "reference_delivery_login_required":
		message = "ShotForward sign-in is required to deliver the reference video."
	case "reference_delivery_failed":
		message = "The reference video could not be prepared for the generation service."
	case "target_slot_unavailable", "result_commit_failed":
		message = "The generated result could not be attached to its target."
	default:
		message = "Generation failed."
	}
	return publicCode, message
}

func mapGenerationSnapshot(value project.GenerationInputSnapshot) map[string]any {
	references := make([]map[string]any, 0, len(value.References))
	for _, reference := range value.References {
		references = append(references, map[string]any{"role": reference.Role, "artifact_id": reference.ArtifactID.String(), "trim_start_ms": reference.TrimStartMilliseconds, "trim_end_ms": reference.TrimEndMilliseconds, "preparation_required": reference.PreparationRequired})
	}
	return map[string]any{"schema_version": value.SchemaVersion, "target_media_kind": value.TargetMediaKind, "prompt": value.Prompt, "generation_mode": value.GenerationMode, "generator_id": value.GeneratorID, "provider_model_id": value.ProviderModelID, "provider_connection_revision": value.ProviderConnectionRevision, "capability_version": value.CapabilityVersion, "output_width": value.OutputWidth, "output_height": value.OutputHeight, "requested_duration_seconds": value.RequestedDurationSeconds, "references": references, "parameters": json.RawMessage(value.ParametersJSON), "target_slot_revision": value.TargetSlotRevision}
}
