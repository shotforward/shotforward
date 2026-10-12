package projecthttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type episodeReferenceMediaProjects interface {
	ListEpisodeReferenceMedia(context.Context, projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error)
	UploadEpisodeReferenceMedia(context.Context, projectapp.UploadEpisodeReferenceMediaInput) (project.EpisodeReferenceMedia, error)
	UpdateEpisodeReferenceMedia(context.Context, projectapp.UpdateEpisodeReferenceMediaInput) (project.EpisodeReferenceMedia, error)
	DeleteEpisodeReferenceMedia(context.Context, projectapp.DeleteEpisodeReferenceMediaInput) error
	AttachClipReferenceMedia(context.Context, projectapp.AttachClipReferenceMediaInput) error
}

func (h *Handler) episodeReferenceMediaProjects(response http.ResponseWriter) (episodeReferenceMediaProjects, bool) {
	values, ok := h.projects.(episodeReferenceMediaProjects)
	if !ok {
		writeError(response, http.StatusNotImplemented, "not_implemented", "reference media library is unavailable")
	}
	return values, ok
}

func (h *Handler) serveEpisodeReferenceMediaRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 7 || len(segments) > 8 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" || segments[4] != "episodes" || segments[6] != "reference-media" {
		return false
	}
	projectID, episodeID, ok := parseScope(response, segments[3], segments[5])
	if !ok {
		return true
	}
	projects, ok := h.episodeReferenceMediaProjects(response)
	if !ok {
		return true
	}
	if len(segments) == 7 {
		switch request.Method {
		case http.MethodGet:
			values, err := projects.ListEpisodeReferenceMedia(request.Context(), projectapp.ListEpisodeReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID})
			if err != nil {
				writeMappedError(response, err)
				return true
			}
			items := make([]map[string]any, 0, len(values))
			for _, value := range values {
				items = append(items, mapEpisodeReferenceMedia(value))
			}
			writeJSON(response, http.StatusOK, map[string]any{"reference_media": items})
		case http.MethodPost:
			h.uploadEpisodeReferenceMedia(response, request, projects, projectID, episodeID)
		default:
			methodNotAllowed(response)
		}
		return true
	}
	artifactID, err := project.ParseArtifactID(segments[7])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	switch request.Method {
	case http.MethodPatch:
		var input struct {
			Name             string          `json:"name"`
			Description      string          `json:"description"`
			TrimStart        json.RawMessage `json:"trim_start_ms"`
			TrimEnd          json.RawMessage `json:"trim_end_ms"`
			ExpectedRevision int64           `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return true
		}
		trimStart, trimEnd, err := decodeReferenceMediaTrim(input.TrimStart, input.TrimEnd)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return true
		}
		value, err := projects.UpdateEpisodeReferenceMedia(request.Context(), projectapp.UpdateEpisodeReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, ArtifactID: artifactID, Name: input.Name, Description: input.Description, TrimStartMilliseconds: trimStart, TrimEndMilliseconds: trimEnd, ExpectedRevision: input.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return true
		}
		writeJSON(response, http.StatusOK, map[string]any{"reference_media": mapEpisodeReferenceMedia(value)})
	case http.MethodDelete:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return true
		}
		if err := projects.DeleteEpisodeReferenceMedia(request.Context(), projectapp.DeleteEpisodeReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, ArtifactID: artifactID, ExpectedRevision: input.ExpectedRevision}); err != nil {
			writeMappedError(response, err)
			return true
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response)
	}
	return true
}

func (h *Handler) uploadEpisodeReferenceMedia(response http.ResponseWriter, request *http.Request, projects episodeReferenceMediaProjects, projectID project.ProjectID, episodeID project.EpisodeID) {
	request.Body = http.MaxBytesReader(response, request.Body, maxMediaUploadBytes+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, "media_upload_too_large", "uploaded media exceeds 100 MiB")
			return
		}
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "invalid multipart media upload")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	kind := project.MediaKind(request.FormValue("kind"))
	if !kind.IsValid() {
		writeError(response, http.StatusBadRequest, "unsupported_media_type", "reference media kind is not supported")
		return
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "file is required")
		return
	}
	defer file.Close()
	filename := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	extension := filepath.Ext(filename)
	if filename == "." || filename == "" || len(extension) > 12 {
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "uploaded filename is invalid")
		return
	}
	temporary, err := os.CreateTemp("", "shotforward-reference-library-upload-*"+extension)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "media_upload_failed", "could not stage uploaded media")
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = io.Copy(temporary, file); err != nil {
		_ = temporary.Close()
		writeError(response, http.StatusBadRequest, "media_upload_failed", "could not read uploaded media")
		return
	}
	if err = temporary.Close(); err != nil {
		writeError(response, http.StatusInternalServerError, "media_upload_failed", "could not stage uploaded media")
		return
	}
	value, err := projects.UploadEpisodeReferenceMedia(request.Context(), projectapp.UploadEpisodeReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, SourcePath: temporaryPath, OriginalFilename: filename, Kind: kind})
	if err != nil {
		writeMappedError(response, fmt.Errorf("upload Episode reference media: %w", err))
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"reference_media": mapEpisodeReferenceMedia(value)})
}

func mapEpisodeReferenceMedia(value project.EpisodeReferenceMedia) map[string]any {
	return map[string]any{"episode_id": value.EpisodeID.String(), "name": value.Name, "description": value.Description, "trim_start_ms": value.TrimStartMilliseconds, "trim_end_ms": value.TrimEndMilliseconds, "effective_duration_ms": value.EffectiveDurationMilliseconds(), "usage_count": value.UsageCount, "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt, "artifact": mapArtifact(value.Artifact)}
}

// Raw fields distinguish omission (preserve the selection) from JSON null,
// which must not accidentally reset or erase a saved trim.
func decodeReferenceMediaTrim(start, end json.RawMessage) (*int64, *int64, error) {
	if len(start) == 0 && len(end) == 0 {
		return nil, nil, nil
	}
	if len(start) == 0 || len(end) == 0 {
		return nil, nil, errors.New("trim_start_ms and trim_end_ms must be supplied together")
	}
	var startValue, endValue int64
	if strings.TrimSpace(string(start)) == "null" || strings.TrimSpace(string(end)) == "null" ||
		json.Unmarshal(start, &startValue) != nil || json.Unmarshal(end, &endValue) != nil {
		return nil, nil, errors.New("trim bounds must be integer milliseconds, not null")
	}
	return &startValue, &endValue, nil
}

func (h *Handler) serveClipReferenceMediaRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 9 || len(segments) > 10 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" || segments[4] != "episodes" || segments[6] != "clips" || segments[8] != "reference-media" {
		return false
	}
	projectID, episodeID, ok := parseScope(response, segments[3], segments[5])
	if !ok {
		return true
	}
	clipID, err := project.ParseClipID(segments[7])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	if len(segments) == 10 {
		if request.Method == http.MethodPut {
			projects, ok := h.episodeReferenceMediaProjects(response)
			if !ok {
				return true
			}
			artifactID, parseErr := project.ParseArtifactID(segments[9])
			if parseErr != nil {
				writeMappedError(response, projectapp.ErrInvalidRequest)
				return true
			}
			if err := projects.AttachClipReferenceMedia(request.Context(), projectapp.AttachClipReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, ClipID: clipID, ArtifactID: artifactID}); err != nil {
				writeMappedError(response, err)
				return true
			}
			response.WriteHeader(http.StatusNoContent)
		} else {
			h.deleteClipReferenceMedia(response, request, projectID, episodeID, clipID, segments[9])
		}
		return true
	}
	switch request.Method {
	case http.MethodGet:
		values, err := h.projects.ListClipReferenceMedia(request.Context(), projectapp.ListClipReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, ClipID: clipID})
		if err != nil {
			writeMappedError(response, err)
			return true
		}
		items := make([]map[string]any, 0, len(values))
		for _, value := range values {
			items = append(items, mapArtifact(value))
		}
		writeJSON(response, http.StatusOK, map[string]any{"artifacts": items})
	case http.MethodPost:
		h.uploadClipReferenceMedia(response, request, projectID, episodeID, clipID)
	default:
		methodNotAllowed(response)
	}
	return true
}

func (h *Handler) uploadClipReferenceMedia(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, clipID project.ClipID) {
	request.Body = http.MaxBytesReader(response, request.Body, maxMediaUploadBytes+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, "media_upload_too_large", "uploaded media exceeds 100 MiB")
			return
		}
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "invalid multipart media upload")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	kind := project.MediaKind(request.FormValue("kind"))
	if !kind.IsValid() {
		writeError(response, http.StatusBadRequest, "unsupported_media_type", "reference media kind is not supported")
		return
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "file is required")
		return
	}
	defer file.Close()
	filename := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	extension := filepath.Ext(filename)
	if filename == "." || filename == "" || len(extension) > 12 {
		writeError(response, http.StatusBadRequest, "invalid_media_upload", "uploaded filename is invalid")
		return
	}
	temporary, err := os.CreateTemp("", "shotforward-reference-upload-*"+extension)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "media_upload_failed", "could not stage uploaded media")
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = io.Copy(temporary, file); err != nil {
		_ = temporary.Close()
		writeError(response, http.StatusBadRequest, "media_upload_failed", "could not read uploaded media")
		return
	}
	if err = temporary.Close(); err != nil {
		writeError(response, http.StatusInternalServerError, "media_upload_failed", "could not stage uploaded media")
		return
	}
	artifact, err := h.projects.UploadClipReferenceMedia(request.Context(), projectapp.UploadClipReferenceMediaInput{
		ProjectID: projectID, EpisodeID: episodeID, ClipID: clipID, SourcePath: temporaryPath, OriginalFilename: filename, Kind: kind,
	})
	if err != nil {
		writeMappedError(response, fmt.Errorf("upload Clip reference media: %w", err))
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"artifact": mapArtifact(artifact)})
}

func (h *Handler) deleteClipReferenceMedia(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, clipID project.ClipID, rawArtifactID string) {
	if request.Method != http.MethodDelete {
		methodNotAllowed(response)
		return
	}
	artifactID, err := project.ParseArtifactID(rawArtifactID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	if err := h.projects.DeleteClipReferenceMedia(request.Context(), projectapp.DeleteClipReferenceMediaInput{ProjectID: projectID, EpisodeID: episodeID, ClipID: clipID, ArtifactID: artifactID}); err != nil {
		writeMappedError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
