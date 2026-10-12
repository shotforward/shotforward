// Package taskhttp exposes the process-wide Task snapshot and replay stream.
// It must be mounted behind localhttp.Gateway.
package taskhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	"github.com/shotforward/shotforward/daemon/internal/tasksse"
)

var ErrInvalidConfiguration = errors.New("invalid Task HTTP configuration")

type EventStream interface {
	Cursor() (string, error)
	StreamEpoch() (string, error)
	Capture(func() error) (string, error)
	Subscribe(context.Context, string) (<-chan tasksse.Event, func(), error)
}

type Handler struct {
	registry *projectapp.ProjectStoreRegistry
	events   EventStream
}

func NewHandler(registry *projectapp.ProjectStoreRegistry, events EventStream) (*Handler, error) {
	if registry == nil || events == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Handler{registry: registry, events: events}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.URL.Path {
	case "/api/v1/tasks/snapshot":
		if request.Method != http.MethodGet {
			h.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
			return
		}
		h.snapshot(response, request)
	case "/api/v1/tasks/events":
		if request.Method != http.MethodGet {
			h.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
			return
		}
		h.stream(response, request)
	default:
		h.writeError(response, http.StatusNotFound, "not_found", "route not found")
	}
}

func (h *Handler) snapshot(response http.ResponseWriter, request *http.Request) {
	var jobs []project.GenerationJob
	var exports []project.ExportJob
	cursor, err := h.events.Capture(func() error {
		var readErr error
		jobs, readErr = h.generationJobs(request.Context())
		if readErr != nil {
			return readErr
		}
		exports, readErr = h.exportJobs(request.Context())
		return readErr
	})
	if err != nil {
		h.writeError(response, http.StatusInternalServerError, "internal_error", "task snapshot is unavailable")
		return
	}
	epoch, err := h.events.StreamEpoch()
	if err != nil {
		h.writeError(response, http.StatusInternalServerError, "internal_error", "task snapshot is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(jobs)+len(exports))
	for _, job := range jobs {
		items = append(items, h.mapGenerationTaskWithContext(request.Context(), job))
	}
	for _, job := range exports {
		items = append(items, h.mapExportTaskWithContext(request.Context(), job))
	}
	sort.Slice(items, func(i, j int) bool {
		left := items[i]["updated_at"].(time.Time)
		right := items[j]["updated_at"].(time.Time)
		if left.Equal(right) {
			return items[i]["task_id"].(string) < items[j]["task_id"].(string)
		}
		return left.After(right)
	})
	h.writeJSON(response, http.StatusOK, map[string]any{"stream_epoch": epoch, "cursor": cursor, "tasks": items})
}

func (h *Handler) exportJobs(ctx context.Context) ([]project.ExportJob, error) {
	result := []project.ExportJob{}
	for _, handle := range h.registry.Handles() {
		jobs, err := handle.Store.RecentExportJobs(ctx, 10_000)
		if err != nil {
			return nil, err
		}
		result = append(result, jobs...)
	}
	return result, nil
}

func (h *Handler) generationJobs(ctx context.Context) ([]project.GenerationJob, error) {
	result := []project.GenerationJob{}
	for _, handle := range h.registry.Handles() {
		jobs, err := handle.Store.RecentGenerationJobs(ctx, 10_000)
		if err != nil {
			return nil, err
		}
		result = append(result, jobs...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID.String() < result[j].ID.String()
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (h *Handler) stream(response http.ResponseWriter, request *http.Request) {
	after := request.Header.Get("Last-Event-ID")
	if after == "" {
		after = request.URL.Query().Get("after")
	}
	events, cancel, err := h.events.Subscribe(request.Context(), after)
	if err != nil {
		h.writeError(response, http.StatusInternalServerError, "internal_error", "task stream is unavailable")
		return
	}
	defer cancel()
	flusher, ok := response.(http.Flusher)
	if !ok {
		h.writeError(response, http.StatusInternalServerError, "internal_error", "streaming is unavailable")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache, no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			payload := map[string]any{"type": event.Type, "occurred_at": event.OccurredAt}
			if event.Job != nil {
				payload["task"] = h.mapGenerationTaskWithContext(request.Context(), *event.Job)
			}
			if event.ExportJob != nil {
				payload["task"] = h.mapExportTaskWithContext(request.Context(), *event.ExportJob)
			}
			if event.Summary != "" {
				payload["summary"] = event.Summary
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(response, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(response, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func mapExportTask(value project.ExportJob) map[string]any {
	result := map[string]any{"task_type": "export_job", "task_id": value.ID.String(), "project_id": value.ProjectID.String(), "episode_id": value.EpisodeID.String(), "state": value.State, "progress_stage": value.ProgressStage, "progress_message": value.ProgressMessage, "failure_code": value.FailureCode, "included_clip_count": len(value.Snapshot.Clips), "skipped_clip_count": len(value.Snapshot.SkippedClips), "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
	if value.OutputRelativePath != "" {
		result["output_relative_path"] = value.OutputRelativePath
	}
	if value.StartedAt != nil {
		result["started_at"] = *value.StartedAt
	}
	if value.FinishedAt != nil {
		result["finished_at"] = *value.FinishedAt
	}
	return result
}

func (h *Handler) mapExportTaskWithContext(ctx context.Context, value project.ExportJob) map[string]any {
	result := mapExportTask(value)
	h.addEpisodeContext(ctx, result, value.ProjectID, value.EpisodeID)
	result["target_kind"] = "episode"
	result["slot_role"] = "export"
	return result
}

func mapGenerationTask(value project.GenerationJob) map[string]any {
	failureCode, failureMessage := generation.PublicGenerationFailure(value.FailureCode, value.FailureMessage)
	result := map[string]any{"task_type": "generation_job", "task_id": value.ID.String(), "generation_batch_id": value.BatchID.String(), "project_id": value.ProjectID.String(), "episode_id": value.EpisodeID.String(), "target_media_slot_id": value.TargetSlotID.String(), "generator_id": value.Snapshot.GeneratorID, "target_media_kind": value.Snapshot.TargetMediaKind, "state": value.State, "stop_supported": value.StopSupported, "progress_stage": value.ProgressStage, "progress_message": value.ProgressMessage, "failure_code": failureCode, "failure_message": failureMessage, "revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
	if value.Snapshot.ProviderModelID != "" {
		result["provider_model_id"] = value.Snapshot.ProviderModelID
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

func (h *Handler) mapGenerationTaskWithContext(ctx context.Context, value project.GenerationJob) map[string]any {
	result := mapGenerationTask(value)
	h.addEpisodeContext(ctx, result, value.ProjectID, value.EpisodeID)
	h.addGenerationTargetContext(ctx, result, value)
	return result
}

func (h *Handler) addEpisodeContext(ctx context.Context, result map[string]any, projectID project.ProjectID, episodeID project.EpisodeID) {
	handle, ok := h.registry.ByProjectID(projectID)
	if !ok {
		return
	}
	if value, err := handle.Store.Project(ctx); err == nil {
		result["project_name"] = value.Name
	}
	if value, err := handle.Store.EpisodeIncludingDeleted(ctx, episodeID); err == nil {
		result["episode_title"] = value.Title
	}
}

func (h *Handler) addGenerationTargetContext(ctx context.Context, result map[string]any, value project.GenerationJob) {
	handle, ok := h.registry.ByProjectID(value.ProjectID)
	if !ok {
		return
	}
	details, err := handle.Store.MediaSlot(ctx, value.TargetSlotID, true)
	if err != nil {
		return
	}
	result["slot_role"] = string(details.Slot.Kind)
	result["target_kind"] = string(details.Slot.OwnerKind)
	result["target_id"] = details.Slot.OwnerID
	switch details.Slot.OwnerKind {
	case project.MediaOwnerAsset:
		assetID, parseErr := project.ParseAssetID(details.Slot.OwnerID)
		if parseErr != nil {
			return
		}
		if asset, readErr := handle.Store.Asset(ctx, assetID); readErr == nil {
			result["target_name"] = asset.Identity.Name
		}
	case project.MediaOwnerClip:
		clipID, parseErr := project.ParseClipID(details.Slot.OwnerID)
		if parseErr != nil {
			return
		}
		clip, readErr := handle.Store.Clip(ctx, clipID)
		if readErr != nil {
			return
		}
		result["target_name"] = clip.Clip.Title
		result["scene_id"] = clip.Clip.SceneID.String()
		if scene, sceneErr := handle.Store.Scene(ctx, clip.Clip.SceneID); sceneErr == nil {
			result["scene_name"] = scene.Title
		}
		if storyboard, storyboardErr := handle.Store.Storyboard(ctx, value.EpisodeID); storyboardErr == nil {
			for sceneIndex, scene := range storyboard.Scenes {
				if scene.Scene.ID != clip.Clip.SceneID {
					continue
				}
				result["scene_position"] = sceneIndex + 1
				for clipIndex, candidate := range scene.Clips {
					if candidate.Clip.ID == clip.Clip.ID {
						result["clip_position"] = clipIndex + 1
						break
					}
				}
				break
			}
		}
	}
}

func (h *Handler) writeError(response http.ResponseWriter, status int, code, message string) {
	h.writeJSON(response, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}, "retryable": false, "request_id": response.Header().Get("X-Request-ID"), "suggested_action": "Refresh the task snapshot and retry."}})
}

func (h *Handler) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
