package projecthttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const maxMediaUploadBytes = 100 << 20

func (h *Handler) serveAssetRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 6 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" {
		return false
	}
	projectID, err := project.ParseProjectID(segments[3])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	switch {
	case len(segments) == 7 && segments[4] == "episodes" && segments[6] == "assets":
		episodeID, parseErr := project.ParseEpisodeID(segments[5])
		if parseErr != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return true
		}
		h.episodeAssets(response, request, projectID, episodeID)
	case len(segments) == 6 && segments[4] == "assets" && strings.HasSuffix(segments[5], ":restore"):
		h.restoreAsset(response, request, projectID, strings.TrimSuffix(segments[5], ":restore"))
	case len(segments) == 6 && segments[4] == "assets":
		h.asset(response, request, projectID, segments[5])
	case len(segments) == 6 && segments[4] == "asset-identities" && strings.HasSuffix(segments[5], ":rename"):
		h.renameAssetIdentity(response, request, projectID, strings.TrimSuffix(segments[5], ":rename"))
	case len(segments) == 6 && segments[4] == "media-slots":
		h.mediaSlot(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "media-slots" && segments[6] == "imports":
		h.importMedia(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "media-slots" && segments[6] == "uploads":
		h.uploadMedia(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "media-slots" && segments[6] == "artifacts":
		h.assignArtifact(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "media-slots" && segments[6] == "frame-extractions":
		h.extractVideoFrame(response, request, projectID, segments[5])
	case len(segments) == 7 && segments[4] == "media-slots" && segments[6] == "selection":
		h.selectMediaVersion(response, request, projectID, segments[5])
	case len(segments) == 8 && segments[4] == "media-slots" && segments[6] == "versions" && strings.HasSuffix(segments[7], ":restore"):
		h.restoreMediaVersion(response, request, projectID, segments[5], strings.TrimSuffix(segments[7], ":restore"))
	case len(segments) == 8 && segments[4] == "media-slots" && segments[6] == "versions":
		h.mediaVersion(response, request, projectID, segments[5], segments[7])
	default:
		return false
	}
	return true
}

func (h *Handler) assignArtifact(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	slotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		ArtifactID           string `json:"artifact_id"`
		ExpectedSlotRevision int64  `json:"expected_slot_revision"`
		MakeCurrent          bool   `json:"make_current"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	artifactID, err := project.ParseArtifactID(body.ArtifactID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.AssignArtifact(request.Context(), projectapp.AssignArtifactInput{
		ProjectID: projectID, MediaSlotID: slotID, ArtifactID: artifactID, ExpectedSlotRevision: body.ExpectedSlotRevision, MakeCurrent: body.MakeCurrent,
	})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, mapMediaSlotDetails(result))
}

type videoFrameExtractor interface {
	ExtractVideoFrame(context.Context, projectapp.ExtractVideoFrameInput) (project.MediaSlotDetails, error)
}

func (h *Handler) extractVideoFrame(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	targetSlotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		SourceVideoMediaSlotID string `json:"source_video_media_slot_id"`
		ExpectedTargetRevision int64  `json:"expected_target_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sourceSlotID, err := project.ParseMediaSlotID(body.SourceVideoMediaSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	extractor, ok := h.projects.(videoFrameExtractor)
	if !ok {
		writeMappedError(response, projectapp.ErrMediaImportUnavailable)
		return
	}
	result, err := extractor.ExtractVideoFrame(request.Context(), projectapp.ExtractVideoFrameInput{
		ProjectID: projectID, TargetMediaSlotID: targetSlotID, SourceVideoMediaSlotID: sourceSlotID,
		ExpectedTargetRevision: body.ExpectedTargetRevision,
	})
	if err != nil {
		writeMappedError(response, fmt.Errorf("extract video frame: %w", err))
		return
	}
	writeJSON(response, http.StatusCreated, mapMediaSlotDetails(result))
}

func (h *Handler) episodeAssets(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	switch request.Method {
	case http.MethodGet:
		values, err := h.projects.ListEpisodeAssets(request.Context(), projectapp.ListEpisodeAssetsInput{ProjectID: projectID, EpisodeID: episodeID, IncludeDeleted: request.URL.Query().Get("include_deleted") == "true"})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		items := make([]map[string]any, 0, len(values))
		for _, value := range values {
			items = append(items, mapEpisodeAsset(value))
		}
		writeJSON(response, http.StatusOK, map[string]any{"assets": items})
	case http.MethodPost:
		var body struct {
			Kind   project.AssetKind `json:"kind"`
			Name   string            `json:"name"`
			Prompt string            `json:"prompt"`
		}
		if err := decodeJSON(response, request, &body); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		result, err := h.projects.CreateAsset(request.Context(), projectapp.CreateAssetInput{ProjectID: projectID, EpisodeID: episodeID, Kind: body.Kind, Name: body.Name, Prompt: body.Prompt})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapAssetResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) asset(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawAssetID string) {
	assetID, err := project.ParseAssetID(rawAssetID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	switch request.Method {
	case http.MethodGet:
		value, readErr := h.projects.GetAsset(request.Context(), projectapp.GetAssetInput{ProjectID: projectID, AssetID: assetID})
		if readErr != nil {
			writeMappedError(response, readErr)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"asset": mapEpisodeAsset(value)})
	case http.MethodPatch:
		var body struct {
			Prompt           string `json:"prompt"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if decodeErr := decodeJSON(response, request, &body); decodeErr != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", decodeErr.Error())
			return
		}
		result, updateErr := h.projects.UpdateAssetPrompt(request.Context(), projectapp.UpdateAssetPromptInput{ProjectID: projectID, AssetID: assetID, Prompt: body.Prompt, ExpectedRevision: body.ExpectedRevision})
		if updateErr != nil {
			writeMappedError(response, updateErr)
			return
		}
		writeJSON(response, http.StatusOK, mapAssetResult(result))
	case http.MethodDelete:
		var body struct {
			ExpectedRevision  int64 `json:"expected_revision"`
			ConfirmReferenced bool  `json:"confirm_referenced"`
		}
		if decodeErr := decodeJSON(response, request, &body); decodeErr != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", decodeErr.Error())
			return
		}
		result, deleteErr := h.projects.DeleteAsset(request.Context(), projectapp.DeleteAssetInput{ProjectID: projectID, AssetID: assetID, ExpectedRevision: body.ExpectedRevision, ConfirmReferenced: body.ConfirmReferenced})
		if deleteErr != nil {
			writeMappedError(response, deleteErr)
			return
		}
		writeJSON(response, http.StatusOK, mapAssetResult(result))
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) restoreAsset(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawAssetID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	assetID, err := project.ParseAssetID(rawAssetID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.RestoreAsset(request.Context(), projectapp.RestoreAssetInput{ProjectID: projectID, AssetID: assetID, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapAssetResult(result))
}

func (h *Handler) renameAssetIdentity(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawIdentityID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	identityID, err := project.ParseAssetIdentityID(rawIdentityID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		Name             string `json:"name"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.RenameAssetIdentity(request.Context(), projectapp.RenameAssetIdentityInput{ProjectID: projectID, AssetIdentityID: identityID, Name: body.Name, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"project": mapProject(result.Project), "identity": mapAssetIdentity(result.Identity)})
}

func (h *Handler) mediaSlot(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	slotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.GetMediaSlot(request.Context(), projectapp.GetMediaSlotInput{ProjectID: projectID, MediaSlotID: slotID, IncludeDeleted: request.URL.Query().Get("include_deleted") == "true"})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapMediaSlotDetails(result))
}

func (h *Handler) importMedia(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	slotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		SourcePath           string `json:"source_path"`
		ExpectedSlotRevision int64  `json:"expected_slot_revision"`
		MakeCurrent          bool   `json:"make_current"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.ImportMedia(request.Context(), projectapp.ImportMediaInput{ProjectID: projectID, MediaSlotID: slotID, SourcePath: body.SourcePath, ExpectedSlotRevision: body.ExpectedSlotRevision, MakeCurrent: body.MakeCurrent})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, mapMediaSlotDetails(result))
}

func (h *Handler) uploadMedia(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	slotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxMediaUploadBytes+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, "media_upload_too_large", "uploaded media exceeds 100 MiB")
			return
		}
		writeError(response, http.StatusBadRequest, "invalid_argument", "invalid media upload")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	revision, err := strconv.ParseInt(request.FormValue("expected_slot_revision"), 10, 64)
	if err != nil || revision < 1 {
		writeError(response, http.StatusBadRequest, "invalid_argument", "expected_slot_revision must be a positive integer")
		return
	}
	makeCurrent, err := strconv.ParseBool(request.FormValue("make_current"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", "make_current must be true or false")
		return
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", "file is required")
		return
	}
	defer file.Close()
	extension := filepath.Ext(filepath.Base(header.Filename))
	if len(extension) > 12 {
		writeError(response, http.StatusBadRequest, "invalid_argument", "uploaded filename extension is invalid")
		return
	}
	temporary, err := os.CreateTemp("", "shotforward-media-upload-*"+extension)
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
	result, err := h.projects.ImportMedia(request.Context(), projectapp.ImportMediaInput{
		ProjectID: projectID, MediaSlotID: slotID, SourcePath: temporaryPath,
		ExpectedSlotRevision: revision, MakeCurrent: makeCurrent,
	})
	if err != nil {
		writeMappedError(response, fmt.Errorf("upload media: %w", err))
		return
	}
	writeJSON(response, http.StatusCreated, mapMediaSlotDetails(result))
}

func (h *Handler) selectMediaVersion(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	slotID, err := project.ParseMediaSlotID(rawSlotID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		MediaVersionID       string `json:"media_version_id"`
		ExpectedSlotRevision int64  `json:"expected_slot_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	versionID, err := project.ParseMediaVersionID(body.MediaVersionID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.SelectMediaVersion(request.Context(), projectapp.SelectMediaVersionInput{ProjectID: projectID, MediaSlotID: slotID, MediaVersionID: versionID, ExpectedSlotRevision: body.ExpectedSlotRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapMediaSlotDetails(result))
}

func (h *Handler) mediaVersion(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID, rawVersionID string) {
	switch request.Method {
	case http.MethodPatch:
		h.updateVideoTrim(response, request, projectID, rawSlotID, rawVersionID)
	case http.MethodDelete:
		h.changeMediaVersionDeletion(response, request, projectID, rawSlotID, rawVersionID, false)
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) updateVideoTrim(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID, rawVersionID string) {
	slotID, slotErr := project.ParseMediaSlotID(rawSlotID)
	versionID, versionErr := project.ParseMediaVersionID(rawVersionID)
	if slotErr != nil || versionErr != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		TrimInMilliseconds      int64 `json:"trim_in_ms"`
		TrimOutMilliseconds     int64 `json:"trim_out_ms"`
		ExpectedVersionRevision int64 `json:"expected_version_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.UpdateVideoTrim(request.Context(), projectapp.UpdateVideoTrimInput{
		ProjectID: projectID, MediaSlotID: slotID, MediaVersionID: versionID,
		TrimInMilliseconds: body.TrimInMilliseconds, TrimOutMilliseconds: body.TrimOutMilliseconds,
		ExpectedVersionRevision: body.ExpectedVersionRevision,
	})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapMediaSlotDetails(result))
}

func (h *Handler) restoreMediaVersion(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID, rawVersionID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	h.changeMediaVersionDeletion(response, request, projectID, rawSlotID, rawVersionID, true)
}

func (h *Handler) changeMediaVersionDeletion(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, rawSlotID, rawVersionID string, restore bool) {
	slotID, slotErr := project.ParseMediaSlotID(rawSlotID)
	versionID, versionErr := project.ParseMediaVersionID(rawVersionID)
	if slotErr != nil || versionErr != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	var body struct {
		ExpectedMediaVersionRevision int64 `json:"expected_media_version_revision"`
		ExpectedMediaSlotRevision    int64 `json:"expected_media_slot_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	input := projectapp.ChangeMediaVersionDeletionInput{ProjectID: projectID, MediaSlotID: slotID, MediaVersionID: versionID, ExpectedMediaVersionRevision: body.ExpectedMediaVersionRevision, ExpectedMediaSlotRevision: body.ExpectedMediaSlotRevision}
	var result project.MediaSlotDetails
	var err error
	if restore {
		result, err = h.projects.RestoreMediaVersion(request.Context(), input)
	} else {
		result, err = h.projects.DeleteMediaVersion(request.Context(), input)
	}
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapMediaSlotDetails(result))
}

func mapAssetResult(value projectapp.AssetResult) map[string]any {
	return map[string]any{"project": mapProject(value.Project), "asset": mapEpisodeAsset(value.Asset)}
}

func mapEpisodeAsset(value project.EpisodeAsset) map[string]any {
	result := map[string]any{
		"asset_id": value.Asset.ID.String(), "project_id": value.Asset.ProjectID.String(), "episode_id": value.Asset.EpisodeID.String(),
		"identity": mapAssetIdentity(value.Identity), "prompt": value.Asset.Prompt, "revision": value.Asset.Revision,
		"created_at": value.Asset.CreatedAt, "updated_at": value.Asset.UpdatedAt,
	}
	if value.Asset.DerivedFromAssetID != "" {
		result["derived_from_asset_id"] = value.Asset.DerivedFromAssetID.String()
	}
	if value.Asset.DeletedAt != nil {
		result["deleted_at"] = *value.Asset.DeletedAt
	}
	if slotID, err := project.FixedMediaSlotID(value.Asset.ID.String(), project.MediaSlotAssetPrimaryImage); err == nil {
		result["primary_image_slot_id"] = slotID.String()
	}
	return result
}

func mapAssetIdentity(value project.AssetIdentity) map[string]any {
	return map[string]any{"asset_identity_id": value.ID.String(), "project_id": value.ProjectID.String(), "kind": value.Kind, "name": value.Name, "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
}

func mapMediaSlotDetails(value project.MediaSlotDetails) map[string]any {
	versions := make([]map[string]any, 0, len(value.Versions))
	for _, version := range value.Versions {
		item := map[string]any{"media_version_id": version.ID.String(), "artifact_id": version.ArtifactID.String(), "sequence_number": version.SequenceNumber, "trim_in_ms": version.TrimInMilliseconds, "trim_out_ms": version.TrimOutMilliseconds, "revision": version.Revision, "created_at": version.CreatedAt, "updated_at": version.UpdatedAt}
		if version.DeletedAt != nil {
			item["deleted_at"] = *version.DeletedAt
		}
		versions = append(versions, item)
	}
	artifacts := make([]map[string]any, 0, len(value.Artifacts))
	for _, artifact := range value.Artifacts {
		artifacts = append(artifacts, mapArtifact(artifact))
	}
	return map[string]any{"slot": map[string]any{"media_slot_id": value.Slot.ID.String(), "episode_id": value.Slot.EpisodeID.String(), "owner_kind": value.Slot.OwnerKind, "owner_id": value.Slot.OwnerID, "kind": value.Slot.Kind, "current_media_version_id": value.Slot.CurrentVersionID.String(), "revision": value.Slot.Revision, "updated_at": value.Slot.UpdatedAt}, "versions": versions, "artifacts": artifacts}
}

func mapArtifact(artifact project.Artifact) map[string]any {
	result := map[string]any{"artifact_id": artifact.ID.String(), "kind": artifact.Kind, "mime_type": artifact.MIMEType, "original_filename": artifact.OriginalFilename, "byte_size": artifact.ByteSize, "width": artifact.Width, "height": artifact.Height, "duration_ms": artifact.DurationMilliseconds, "created_at": artifact.CreatedAt}
	if artifact.RemoteReferenceExpiresAt != nil {
		result["remote_reference_expires_at"] = *artifact.RemoteReferenceExpiresAt
	}
	return result
}

func (h *Handler) projectTrash(response http.ResponseWriter, request *http.Request, rawProjectID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	projectID, err := project.ParseProjectID(rawProjectID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	value, err := h.projects.ListProjectTrash(request.Context(), projectID)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	assets := make([]map[string]any, 0, len(value.Assets))
	for _, asset := range value.Assets {
		assets = append(assets, mapEpisodeAsset(asset))
	}
	scenes := make([]map[string]any, 0, len(value.Scenes))
	for _, scene := range value.Scenes {
		scenes = append(scenes, mapStoryboardScene(scene))
	}
	clips := make([]map[string]any, 0, len(value.Clips))
	for _, clip := range value.Clips {
		clips = append(clips, mapClipDetails(clip))
	}
	revisions := make(map[string]int64, len(value.StoryboardRevisions))
	for episodeID, revision := range value.StoryboardRevisions {
		revisions[episodeID.String()] = revision
	}
	writeJSON(response, http.StatusOK, map[string]any{"project": mapProject(value.Project), "episodes": mapEpisodeSummaries(value.Episodes), "assets": assets, "scenes": scenes, "clips": clips, "storyboard_revisions": revisions})
}
