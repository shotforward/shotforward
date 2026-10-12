// Package projecthttp exposes Project, Episode, Storyboard, and Generation
// Application use cases. It must be mounted behind localhttp.Gateway.
package projecthttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const MaxJSONRequestBytes = 128 * 1024

var ErrInvalidConfiguration = errors.New("invalid Project HTTP configuration")

type ProjectService interface {
	CreateProject(context.Context, projectapp.CreateProjectInput) (projectapp.ProjectResult, error)
	OpenProject(context.Context, projectapp.OpenProjectInput) (projectapp.ProjectResult, error)
	GetProject(context.Context, project.ProjectID) (projectapp.ProjectResult, error)
	UpdateProject(context.Context, projectapp.UpdateProjectInput) (projectapp.ProjectResult, error)
	CloseProject(context.Context, project.ProjectID) error
	DeleteProject(context.Context, projectapp.DeleteProjectInput) error
	GetEpisode(context.Context, projectapp.GetEpisodeInput) (project.Episode, error)
	CreateEpisode(context.Context, projectapp.CreateEpisodeInput) (projectapp.EpisodeResult, error)
	UpdateEpisode(context.Context, projectapp.UpdateEpisodeInput) (projectapp.EpisodeResult, error)
	DeleteEpisode(context.Context, projectapp.ChangeEpisodeDeletionInput) (projectapp.ProjectResult, error)
	RestoreEpisode(context.Context, projectapp.ChangeEpisodeDeletionInput) (projectapp.EpisodeResult, error)
	SaveScript(context.Context, projectapp.SaveScriptInput) (projectapp.EpisodeResult, error)
	ImportScript(context.Context, projectapp.ImportScriptInput) (projectapp.ImportScriptResult, error)
	GetAsset(context.Context, projectapp.GetAssetInput) (project.EpisodeAsset, error)
	ListEpisodeAssets(context.Context, projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error)
	CreateAsset(context.Context, projectapp.CreateAssetInput) (projectapp.AssetResult, error)
	UpdateAssetPrompt(context.Context, projectapp.UpdateAssetPromptInput) (projectapp.AssetResult, error)
	RenameAssetIdentity(context.Context, projectapp.RenameAssetIdentityInput) (projectapp.AssetIdentityResult, error)
	DeleteAsset(context.Context, projectapp.DeleteAssetInput) (projectapp.AssetResult, error)
	RestoreAsset(context.Context, projectapp.RestoreAssetInput) (projectapp.AssetResult, error)
	GetMediaSlot(context.Context, projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error)
	ImportMedia(context.Context, projectapp.ImportMediaInput) (project.MediaSlotDetails, error)
	AssignArtifact(context.Context, projectapp.AssignArtifactInput) (project.MediaSlotDetails, error)
	SelectMediaVersion(context.Context, projectapp.SelectMediaVersionInput) (project.MediaSlotDetails, error)
	UpdateVideoTrim(context.Context, projectapp.UpdateVideoTrimInput) (project.MediaSlotDetails, error)
	DeleteMediaVersion(context.Context, projectapp.ChangeMediaVersionDeletionInput) (project.MediaSlotDetails, error)
	RestoreMediaVersion(context.Context, projectapp.ChangeMediaVersionDeletionInput) (project.MediaSlotDetails, error)
	ListProjectTrash(context.Context, project.ProjectID) (projectapp.ProjectTrashResult, error)
	GetStoryboard(context.Context, projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error)
	GetStoryboardValidation(context.Context, projectapp.GetStoryboardInput) (projectapp.StoryboardValidationResult, error)
	ResetStoryboard(context.Context, projectapp.ResetStoryboardInput) (projectapp.ResetStoryboardResult, error)
	GetScene(context.Context, projectapp.GetSceneInput) (project.StoryboardScene, error)
	GetClip(context.Context, projectapp.GetClipInput) (project.ClipDetails, error)
	CreateScene(context.Context, projectapp.CreateSceneInput) (project.Scene, error)
	UpdateScene(context.Context, projectapp.UpdateSceneInput) (project.Scene, error)
	ReorderScenes(context.Context, projectapp.ReorderScenesInput) (projectapp.StoryboardResult, error)
	DeleteScene(context.Context, projectapp.DeleteSceneInput) (project.Scene, error)
	RestoreScene(context.Context, projectapp.RestoreSceneInput) (project.Scene, error)
	CreateClip(context.Context, projectapp.CreateClipInput) (project.ClipDetails, error)
	UpdateClip(context.Context, projectapp.UpdateClipInput) (project.ClipDetails, error)
	MoveClip(context.Context, projectapp.MoveClipInput) (project.ClipDetails, error)
	DeleteClip(context.Context, projectapp.DeleteClipInput) (project.ClipDetails, error)
	RestoreClip(context.Context, projectapp.RestoreClipInput) (project.ClipDetails, error)
	BindClipAsset(context.Context, projectapp.BindClipAssetInput) (project.ClipDetails, error)
	UnbindClipAsset(context.Context, projectapp.UnbindClipAssetInput) (project.ClipDetails, error)
	ReplaceClipAsset(context.Context, projectapp.ReplaceClipAssetInput) (project.ClipDetails, error)
	SplitClip(context.Context, projectapp.SplitClipInput) (projectapp.ReplaceClipsResult, error)
	MergeClips(context.Context, projectapp.MergeClipsInput) (projectapp.ReplaceClipsResult, error)
	CheckGenerationReadiness(context.Context, projectapp.CheckGenerationReadinessInput) (projectapp.GenerationReadinessResult, error)
	SubmitGenerationBatch(context.Context, projectapp.SubmitGenerationBatchInput) (project.GenerationBatchDetails, error)
	GetGenerationBatch(context.Context, projectapp.GetGenerationBatchInput) (project.GenerationBatchDetails, error)
	GetGenerationJob(context.Context, projectapp.GetGenerationJobInput) (project.GenerationJob, error)
	CancelGenerationJob(context.Context, projectapp.ChangeGenerationJobInput) (project.GenerationJob, error)
	RequestGenerationStop(context.Context, projectapp.ChangeGenerationJobInput) (project.GenerationJob, error)
	RetryGenerationJob(context.Context, projectapp.RetryGenerationJobInput) (project.GenerationBatchDetails, error)
	CancelGenerationBatch(context.Context, projectapp.CancelGenerationBatchInput) (project.GenerationBatchDetails, error)
	GetExportPreview(context.Context, projectapp.GetExportPreviewInput) (project.ExportSnapshot, error)
	SubmitExportJob(context.Context, projectapp.SubmitExportJobInput) (project.ExportJob, error)
	GetExportJob(context.Context, projectapp.GetExportJobInput) (project.ExportJob, error)
	GetExportContent(context.Context, projectapp.GetExportJobInput) (projectapp.ArtifactContentSource, error)
	CancelExportJob(context.Context, projectapp.CancelExportJobInput) (project.ExportJob, error)
	GetArtifactContent(context.Context, projectapp.GetArtifactContentInput) (projectapp.ArtifactContentSource, error)
	ListClipReferenceMedia(context.Context, projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error)
	UploadClipReferenceMedia(context.Context, projectapp.UploadClipReferenceMediaInput) (project.Artifact, error)
	DeleteClipReferenceMedia(context.Context, projectapp.DeleteClipReferenceMediaInput) error
}

type Options struct {
	Projects ProjectService
	Catalog  projectapp.ProjectCatalog
}

type Handler struct {
	projects ProjectService
	catalog  projectapp.ProjectCatalog
}

func NewHandler(options Options) (*Handler, error) {
	if options.Projects == nil || options.Catalog == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Handler{projects: options.Projects, catalog: options.Catalog}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	segments := splitPath(request.URL.Path)
	if h.serveGenerationRoute(response, request, segments) {
		return
	}
	if h.serveExportRoute(response, request, segments) {
		return
	}
	if h.serveArtifactContentRoute(response, request, segments) {
		return
	}
	if h.serveEpisodeReferenceMediaRoute(response, request, segments) {
		return
	}
	if h.serveClipReferenceMediaRoute(response, request, segments) {
		return
	}
	if h.serveStoryboardRoute(response, request, segments) {
		return
	}
	if h.serveAssetRoute(response, request, segments) {
		return
	}
	switch {
	case len(segments) == 3 && equal(segments, "api", "v1", "projects"):
		h.projectsRoot(response, request)
	case len(segments) == 3 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects:open":
		h.openByPath(response, request)
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && strings.HasSuffix(segments[3], ":open"):
		h.openRegistered(response, request, strings.TrimSuffix(segments[3], ":open"))
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && strings.HasSuffix(segments[3], ":close"):
		h.closeProject(response, request, strings.TrimSuffix(segments[3], ":close"))
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects":
		h.getProject(response, request, segments[3])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "trash":
		h.projectTrash(response, request, segments[3])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "episodes":
		h.episodes(response, request, segments[3])
	case len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "episodes":
		if strings.HasSuffix(segments[5], ":restore") {
			h.restoreEpisode(response, request, segments[3], strings.TrimSuffix(segments[5], ":restore"))
		} else {
			h.episode(response, request, segments[3], segments[5])
		}
	case len(segments) == 7 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "episodes" && segments[6] == "script":
		h.script(response, request, segments[3], segments[5])
	case len(segments) == 8 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "episodes" && segments[6] == "script" && segments[7] == "imports":
		h.importScript(response, request, segments[3], segments[5])
	default:
		writeError(response, http.StatusNotFound, "not_found", "route not found")
	}
}

func (h *Handler) projectsRoot(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		values, err := h.catalog.Projects(request.Context())
		if err != nil {
			writeMappedError(response, err)
			return
		}
		items := make([]map[string]any, 0, len(values))
		for _, value := range values {
			items = append(items, mapCatalog(value))
		}
		writeJSON(response, http.StatusOK, map[string]any{"projects": items})
	case http.MethodPost:
		var input struct {
			RootPath     string `json:"root_path"`
			Name         string `json:"name"`
			Description  string `json:"description"`
			OutputWidth  int    `json:"output_width"`
			OutputHeight int    `json:"output_height"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.CreateProject(request.Context(), projectapp.CreateProjectInput{RootPath: input.RootPath, Name: input.Name, Description: input.Description, OutputWidth: input.OutputWidth, OutputHeight: input.OutputHeight})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapProjectResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) openByPath(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var input struct {
		RootPath string `json:"root_path"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.OpenProject(request.Context(), projectapp.OpenProjectInput{RootPath: input.RootPath})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapProjectResult(result))
}

func (h *Handler) openRegistered(response http.ResponseWriter, request *http.Request, rawProjectID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	projectID, err := project.ParseProjectID(rawProjectID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	entry, err := h.catalog.Project(request.Context(), projectID)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	result, err := h.projects.OpenProject(request.Context(), projectapp.OpenProjectInput{RootPath: entry.RootPath})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapProjectResult(result))
}

func (h *Handler) closeProject(response http.ResponseWriter, request *http.Request, rawProjectID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	projectID, err := project.ParseProjectID(rawProjectID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	if err := h.projects.CloseProject(request.Context(), projectID); err != nil {
		writeMappedError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getProject(response http.ResponseWriter, request *http.Request, rawProjectID string) {
	projectID, err := project.ParseProjectID(rawProjectID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	switch request.Method {
	case http.MethodGet:
		result, err := h.projects.GetProject(request.Context(), projectID)
		if err != nil {
			writeMappedError(response, err)
			return
		}
		payload := mapProjectResult(result)
		if entry, catalogErr := h.catalog.Project(request.Context(), projectID); catalogErr == nil {
			payload["root_path"] = entry.RootPath
		}
		writeJSON(response, http.StatusOK, payload)
	case http.MethodPatch:
		var input struct {
			Name             string `json:"name"`
			Description      string `json:"description"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.UpdateProject(request.Context(), projectapp.UpdateProjectInput{
			ProjectID: projectID, Name: input.Name, Description: input.Description, ExpectedRevision: input.ExpectedRevision,
		})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		payload := mapProjectResult(result)
		if entry, catalogErr := h.catalog.Project(request.Context(), projectID); catalogErr == nil {
			payload["root_path"] = entry.RootPath
		}
		writeJSON(response, http.StatusOK, payload)
	case http.MethodDelete:
		var input struct {
			ConfirmationName string `json:"confirmation_name"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if err := h.projects.DeleteProject(request.Context(), projectapp.DeleteProjectInput{ProjectID: projectID, ConfirmationName: input.ConfirmationName}); err != nil {
			writeMappedError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) episodes(response http.ResponseWriter, request *http.Request, rawProjectID string) {
	projectID, err := project.ParseProjectID(rawProjectID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	switch request.Method {
	case http.MethodGet:
		result, err := h.projects.GetProject(request.Context(), projectID)
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"episodes": mapEpisodeSummaries(result.Episodes)})
	case http.MethodPost:
		var input struct {
			Title            string `json:"title"`
			TargetDurationMS int64  `json:"target_duration_ms"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if input.TargetDurationMS > math.MaxInt64/int64(time.Millisecond) {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		result, err := h.projects.CreateEpisode(request.Context(), projectapp.CreateEpisodeInput{ProjectID: projectID, Title: input.Title, TargetDuration: time.Duration(input.TargetDurationMS) * time.Millisecond})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapEpisodeResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) episode(response http.ResponseWriter, request *http.Request, rawProjectID, rawEpisodeID string) {
	projectID, episodeID, ok := parseScope(response, rawProjectID, rawEpisodeID)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet:
		value, err := h.projects.GetEpisode(request.Context(), projectapp.GetEpisodeInput{ProjectID: projectID, EpisodeID: episodeID})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		scope, err := h.projects.GetProject(request.Context(), projectID)
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, mapEpisodeResult(projectapp.EpisodeResult{
			Project: scope.Project, Episode: value, Episodes: scope.Episodes,
		}))
	case http.MethodPatch:
		var input struct {
			Title            string `json:"title"`
			TargetDurationMS int64  `json:"target_duration_ms"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.UpdateEpisode(request.Context(), projectapp.UpdateEpisodeInput{ProjectID: projectID, EpisodeID: episodeID, Title: input.Title, TargetDuration: time.Duration(input.TargetDurationMS) * time.Millisecond, ExpectedRevision: input.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, mapEpisodeResult(result))
	case http.MethodDelete:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.DeleteEpisode(request.Context(), projectapp.ChangeEpisodeDeletionInput{ProjectID: projectID, EpisodeID: episodeID, ExpectedRevision: input.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, mapProjectResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) restoreEpisode(response http.ResponseWriter, request *http.Request, rawProjectID, rawEpisodeID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	projectID, episodeID, ok := parseScope(response, rawProjectID, rawEpisodeID)
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.RestoreEpisode(request.Context(), projectapp.ChangeEpisodeDeletionInput{ProjectID: projectID, EpisodeID: episodeID, ExpectedRevision: input.ExpectedRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapEpisodeResult(result))
}

func (h *Handler) script(response http.ResponseWriter, request *http.Request, rawProjectID, rawEpisodeID string) {
	projectID, episodeID, ok := parseScope(response, rawProjectID, rawEpisodeID)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet:
		value, err := h.projects.GetEpisode(request.Context(), projectapp.GetEpisodeInput{ProjectID: projectID, EpisodeID: episodeID})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"script": mapScript(value.Script)})
	case http.MethodPut:
		var input struct {
			Markdown         string `json:"markdown"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.SaveScript(request.Context(), projectapp.SaveScriptInput{ProjectID: projectID, EpisodeID: episodeID, Markdown: input.Markdown, ExpectedScriptRevision: input.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, mapEpisodeResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) importScript(response http.ResponseWriter, request *http.Request, rawProjectID, rawEpisodeID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	projectID, episodeID, ok := parseScope(response, rawProjectID, rawEpisodeID)
	if !ok {
		return
	}
	var input struct {
		SourcePath       string `json:"source_path"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.ImportScript(request.Context(), projectapp.ImportScriptInput{ProjectID: projectID, EpisodeID: episodeID, SourcePath: input.SourcePath, ExpectedScriptRevision: input.ExpectedRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"project": mapProject(result.Project), "episode": mapEpisode(result.Episode), "episodes": mapEpisodeSummaries(result.Episodes), "format": result.Format, "character_count": result.CharacterCount, "diagnostics": result.Diagnostics})
}

func parseScope(response http.ResponseWriter, rawProjectID, rawEpisodeID string) (project.ProjectID, project.EpisodeID, bool) {
	projectID, projectErr := project.ParseProjectID(rawProjectID)
	episodeID, episodeErr := project.ParseEpisodeID(rawEpisodeID)
	if projectErr != nil || episodeErr != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return "", "", false
	}
	return projectID, episodeID, true
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		return errors.New("application/json is required")
	}
	request.Body = http.MaxBytesReader(response, request.Body, MaxJSONRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func requireEmptyJSON(response http.ResponseWriter, request *http.Request) error {
	var input struct{}
	return decodeJSON(response, request, &input)
}

func splitPath(value string) []string {
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func equal(values []string, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	for index := range values {
		if values[index] != expected[index] {
			return false
		}
	}
	return true
}

func methodNotAllowed(response http.ResponseWriter) {
	writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
}

func writeMappedError(response http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "project operation failed"
	details := map[string]any{}
	switch {
	case errors.Is(err, projectapp.ErrInvalidRequest), errors.Is(err, projectapp.ErrProjectPathInvalid), errors.Is(err, projectapp.ErrProjectNameConfirmation), errors.Is(err, projectapp.ErrScriptTooLong), errors.Is(err, projectapp.ErrScriptImportUnsupported), errors.Is(err, projectapp.ErrScriptImportEncoding), errors.Is(err, projectapp.ErrScriptImportInvalid):
		status, code, message = http.StatusBadRequest, "invalid_argument", "project request is invalid"
	case errors.Is(err, projectapp.ErrProjectPathNotFound), errors.Is(err, projectapp.ErrProjectDatabaseNotFound), errors.Is(err, projectapp.ErrEpisodeNotFound),
		errors.Is(err, projectapp.ErrSceneNotFound), errors.Is(err, projectapp.ErrClipNotFound), errors.Is(err, projectapp.ErrAssetNotFound),
		errors.Is(err, projectapp.ErrMediaSlotNotFound), errors.Is(err, projectapp.ErrArtifactNotFound), errors.Is(err, projectapp.ErrGenerationBatchNotFound), errors.Is(err, projectapp.ErrGenerationJobNotFound), errors.Is(err, projectapp.ErrExportJobNotFound):
		status, code, message = http.StatusNotFound, "not_found", "project object was not found"
	case errors.Is(err, projectapp.ErrRevisionConflict), errors.Is(err, projectapp.ErrStoryboardRevisionConflict):
		status, code, message = http.StatusConflict, "revision_conflict", "project state changed since it was read"
	case errors.Is(err, projectapp.ErrSceneNotEmpty), errors.Is(err, projectapp.ErrStoryboardResetConfirmation):
		status, code, message = http.StatusConflict, "confirmation_required", "Scene contains active Clips and requires explicit confirmation"
	case errors.Is(err, projectapp.ErrProjectPathExists), errors.Is(err, projectapp.ErrProjectPathConflict), errors.Is(err, projectapp.ErrProjectStoreNotOpen), errors.Is(err, projectapp.ErrProjectSchemaTooNew), errors.Is(err, projectapp.ErrProjectHasActiveGeneration), errors.Is(err, projectapp.ErrProjectHasActiveExport), errors.Is(err, projectapp.ErrProjectHasActiveAgent), errors.Is(err, projectapp.ErrEpisodeNotDeleted), errors.Is(err, projectapp.ErrEpisodeHasActiveGeneration), errors.Is(err, projectapp.ErrEpisodeHasActiveExport), errors.Is(err, projectapp.ErrEpisodeHasActiveAgent),
		errors.Is(err, projectapp.ErrSceneDeleted), errors.Is(err, projectapp.ErrSceneNotDeleted), errors.Is(err, projectapp.ErrClipDeleted), errors.Is(err, projectapp.ErrClipNotDeleted),
		errors.Is(err, projectapp.ErrSceneOrderMismatch), errors.Is(err, projectapp.ErrClipAssetAlreadyBound), errors.Is(err, projectapp.ErrClipAssetNotBound),
		errors.Is(err, projectapp.ErrClipHasActiveGeneration), errors.Is(err, projectapp.ErrGenerationJobStateConflict), errors.Is(err, projectapp.ErrGenerationStopUnsupported),
		errors.Is(err, projectapp.ErrExportEpisodeBusy), errors.Is(err, projectapp.ErrExportJobStateConflict):
		status, code, message = http.StatusConflict, "invalid_state", "project state does not allow this operation"
	case errors.Is(err, projectapp.ErrGenerationOperationConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", "operation key was already used with different input"
	case errors.Is(err, projectapp.ErrExportOperationConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", "operation key was already used with different input"
	case errors.Is(err, projectapp.ErrGenerationSlotBusy):
		status, code, message = http.StatusConflict, "active_slot_job_exists", "media slot already has an active generation job"
	case errors.Is(err, projectapp.ErrGenerationNotReady):
		status, code, message = http.StatusUnprocessableEntity, "generation_not_ready", "generation target is missing required input"
	case errors.Is(err, projectapp.ErrPromptTranslationUnavailable):
		status, code, message = http.StatusServiceUnavailable, "prompt_translation_unavailable", "selected Agent is unavailable for prompt translation"
	case errors.Is(err, projectapp.ErrPromptTranslationSelectionChanged):
		status, code, message = http.StatusConflict, "prompt_translation_selection_changed", "selected Agent service or model changed; refresh and retry"
	case errors.Is(err, projectapp.ErrPromptTranslationTimeout):
		status, code, message = http.StatusGatewayTimeout, "prompt_translation_timeout", "English prompt preparation timed out; no generation task was submitted"
	case errors.Is(err, projectapp.ErrPromptTranslationInvalid):
		status, code, message = http.StatusUnprocessableEntity, "prompt_translation_invalid", "translated prompt failed preservation or length checks; no generation task was submitted"
	case errors.Is(err, projectapp.ErrPromptTranslationFailed):
		status, code, message = http.StatusBadGateway, "prompt_translation_failed", "selected Agent could not prepare the English prompt; check login and available quota"
		var business *projectapp.GenerationPromptTranslationFailure
		if errors.As(err, &business) {
			switch business.Code {
			case "shotforward_negative_balance", "shotforward_insufficient_points":
				status, code, message = http.StatusPaymentRequired, business.Code, "ShotForward balance is insufficient for prompt translation"
			case "shotforward_wallet_not_found":
				status, code, message = http.StatusPaymentRequired, business.Code, "ShotForward wallet is unavailable for prompt translation"
			}
		}
	case errors.Is(err, projectapp.ErrExportNotReady):
		status, code, message = http.StatusUnprocessableEntity, "export_not_ready", "episode has no completed export or current video"
	case errors.Is(err, projectapp.ErrGenerationCapabilityMismatch):
		status, code, message = http.StatusUnprocessableEntity, "capability_unsupported", "generator does not support this request"
		var issue *projectapp.GenerationCapabilityIssue
		if errors.As(err, &issue) {
			details = map[string]any{
				"constraint": issue.Constraint,
				"role":       string(issue.Role),
				"actual":     issue.Actual,
				"minimum":    issue.Minimum,
				"maximum":    issue.Maximum,
			}
		}
	case errors.Is(err, projectapp.ErrGenerationGeneratorNotFound):
		status, code, message = http.StatusServiceUnavailable, "provider_unavailable", "generator is unavailable"
	case errors.Is(err, projectapp.ErrMediaImportUnsupported):
		status, code, message = http.StatusUnsupportedMediaType, "unsupported_media_type", "media format is not supported"
	case errors.Is(err, projectapp.ErrMediaAspectRatioMismatch):
		status, code, message = http.StatusUnprocessableEntity, "aspect_ratio_mismatch", "image aspect ratio does not match project output"
	case errors.Is(err, projectapp.ErrMediaOutputSpecMismatch):
		status, code, message = http.StatusUnprocessableEntity, "output_spec_mismatch", "image dimensions do not match project output"
	case errors.Is(err, projectapp.ErrMediaKindMismatch):
		status, code, message = http.StatusUnprocessableEntity, "media_kind_mismatch", "media kind does not match the target slot"
	case errors.Is(err, projectapp.ErrMediaImportInvalid):
		status, code, message = http.StatusUnprocessableEntity, "invalid_media", "media file is invalid"
	case errors.Is(err, projectapp.ErrMediaImportUnreadable):
		status, code, message = http.StatusUnprocessableEntity, "unreadable_media", "media file could not be read"
	case errors.Is(err, projectapp.ErrMediaImportUnavailable):
		status, code, message = http.StatusServiceUnavailable, "media_import_unavailable", "media import is unavailable"
	case errors.Is(err, projectapp.ErrProjectPathNotWritable), errors.Is(err, projectapp.ErrScriptImportUnreadable):
		status, code, message = http.StatusUnprocessableEntity, "resource_unavailable", "local file or directory is unavailable"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusServiceUnavailable, "request_interrupted", "project operation was interrupted"
	}
	writeErrorDetails(response, status, code, message, details)
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeErrorDetails(response, status, code, message, map[string]any{})
}

func writeErrorDetails(response http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(response, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": details, "retryable": false,
		"request_id": response.Header().Get("X-Request-ID"), "suggested_action": "Refresh local state and retry the operation.",
	}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func mapCatalog(value projectapp.ProjectCatalogEntry) map[string]any {
	result := map[string]any{"project_id": value.ProjectID.String(), "root_path": value.RootPath, "display_name": value.DisplayName, "cover_cache_key": value.CoverCacheKey, "output_width": value.Width, "output_height": value.Height, "episode_count": value.EpisodeCount, "project_updated_at": value.ProjectUpdatedAt}
	if value.LastOpenedAt != nil {
		result["last_opened_at"] = *value.LastOpenedAt
	}
	return result
}

func mapProjectResult(value projectapp.ProjectResult) map[string]any {
	return map[string]any{"project": mapProject(value.Project), "episodes": mapEpisodeSummaries(value.Episodes)}
}

func mapEpisodeResult(value projectapp.EpisodeResult) map[string]any {
	return map[string]any{"project": mapProject(value.Project), "episode": mapEpisode(value.Episode), "episodes": mapEpisodeSummaries(value.Episodes)}
}

func mapProject(value project.Project) map[string]any {
	return map[string]any{"project_id": value.ID.String(), "name": value.Name, "description": value.Description, "output_width": value.Output.Width(), "output_height": value.Output.Height(), "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
}

func mapEpisode(value project.Episode) map[string]any {
	result := mapEpisodeSummary(value.Summary())
	result["script"] = mapScript(value.Script)
	return result
}

func mapEpisodeSummaries(values []project.EpisodeSummary) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, mapEpisodeSummary(value))
	}
	return result
}

func mapEpisodeSummary(value project.EpisodeSummary) map[string]any {
	return map[string]any{"episode_id": value.ID.String(), "project_id": value.ProjectID.String(), "sequence_number": value.SequenceNumber, "title": value.Title, "target_duration_ms": value.TargetDuration.Milliseconds(), "revision": value.Revision, "script_revision": value.ScriptRevision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt, "deleted_at": value.DeletedAt, "script_updated_at": value.ScriptUpdatedAt}
}

func mapScript(value project.Script) map[string]any {
	return map[string]any{"markdown": value.Markdown, "revision": value.Revision, "updated_at": value.UpdatedAt, "character_count": project.ScriptCharacterCount(value.Markdown)}
}
