package projecthttp

import (
	"mime"
	"net/http"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (h *Handler) serveExportRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 6 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" {
		return false
	}
	projectID, err := project.ParseProjectID(segments[3])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	switch {
	case len(segments) == 7 && segments[4] == "episodes" && segments[6] == "preview":
		h.exportPreview(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "episodes" && segments[6] == "exports":
		h.submitExport(response, request, projectID, segments[5])
	case len(segments) == 6 && segments[4] == "exports":
		h.exportJob(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "exports" && segments[6] == "cancel":
		h.cancelExport(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "exports" && segments[6] == "content":
		h.exportContent(response, request, projectID, segments[5])
	default:
		return false
	}
	return true
}

func (h *Handler) exportContent(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawJobID string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(response)
		return
	}
	jobID, err := project.ParseExportJobID(rawJobID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	source, err := h.projects.GetExportContent(request.Context(), projectapp.GetExportJobInput{ProjectID: projectID, ExportJobID: jobID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	defer source.Content.Close()
	response.Header().Set("Content-Type", source.MIMEType)
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": source.OriginalFilename}))
	response.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(response, request, source.OriginalFilename, source.ModifiedAt, source.Content)
}

func (h *Handler) exportPreview(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawEpisodeID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	episodeID, err := project.ParseEpisodeID(rawEpisodeID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	snapshot, err := h.projects.GetExportPreview(request.Context(), projectapp.GetExportPreviewInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"preview": mapExportSnapshot(projectID, snapshot)})
}

func (h *Handler) submitExport(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawEpisodeID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		OperationKey               string `json:"operation_key"`
		ExpectedStoryboardRevision int64  `json:"expected_storyboard_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	episodeID, err := project.ParseEpisodeID(rawEpisodeID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	job, err := h.projects.SubmitExportJob(request.Context(), projectapp.SubmitExportJobInput{ProjectID: projectID, EpisodeID: episodeID, OperationKey: body.OperationKey, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": mapExportJob(job)})
}

func (h *Handler) exportJob(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawJobID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	jobID, err := project.ParseExportJobID(rawJobID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	job, err := h.projects.GetExportJob(request.Context(), projectapp.GetExportJobInput{ProjectID: projectID, ExportJobID: jobID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": mapExportJob(job)})
}

func (h *Handler) cancelExport(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawJobID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	jobID, err := project.ParseExportJobID(rawJobID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	job, err := h.projects.CancelExportJob(request.Context(), projectapp.CancelExportJobInput{ProjectID: projectID, ExportJobID: jobID, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": mapExportJob(job)})
}

func mapExportSnapshot(projectID project.ProjectID, value project.ExportSnapshot) map[string]any {
	clips := make([]map[string]any, 0, len(value.Clips))
	var durationMilliseconds int64
	for _, clip := range value.Clips {
		duration := clip.TrimOutMilliseconds - clip.TrimInMilliseconds
		durationMilliseconds += duration
		clips = append(clips, map[string]any{"scene_id": clip.SceneID.String(), "clip_id": clip.ClipID.String(), "position": clip.Position, "media_slot_id": clip.MediaSlotID.String(), "media_slot_revision": clip.MediaSlotRevision, "media_version_id": clip.MediaVersionID.String(), "media_version_revision": clip.MediaVersionRevision, "artifact_id": clip.ArtifactID.String(), "trim_in_ms": clip.TrimInMilliseconds, "trim_out_ms": clip.TrimOutMilliseconds, "duration_ms": duration, "media_url": "/api/v1/projects/" + projectID.String() + "/artifacts/" + clip.ArtifactID.String() + "/content"})
	}
	skipped := make([]map[string]any, 0, len(value.SkippedClips))
	for _, clip := range value.SkippedClips {
		skipped = append(skipped, map[string]any{"scene_id": clip.SceneID.String(), "clip_id": clip.ClipID.String(), "position": clip.Position, "reason": clip.Reason})
	}
	return map[string]any{"schema_version": value.SchemaVersion, "storyboard_revision": value.StoryboardRevision, "output_width": value.OutputWidth, "output_height": value.OutputHeight, "export_duration_ms": durationMilliseconds, "included_clip_count": len(clips), "skipped_clip_count": len(skipped), "clips": clips, "skipped_clips": skipped}
}

func mapExportJob(value project.ExportJob) map[string]any {
	result := map[string]any{"export_job_id": value.ID.String(), "project_id": value.ProjectID.String(), "episode_id": value.EpisodeID.String(), "operation_key": value.OperationKey, "state": value.State, "snapshot": mapExportSnapshot(value.ProjectID, value.Snapshot), "progress_stage": value.ProgressStage, "progress_message": value.ProgressMessage, "failure_code": value.FailureCode, "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
	if value.OutputRelativePath != "" {
		result["output_relative_path"] = value.OutputRelativePath
		result["content_url"] = "/api/v1/projects/" + value.ProjectID.String() + "/exports/" + value.ID.String() + "/content"
	}
	if value.StartedAt != nil {
		result["started_at"] = *value.StartedAt
	}
	if value.FinishedAt != nil {
		result["finished_at"] = *value.FinishedAt
	}
	return result
}
