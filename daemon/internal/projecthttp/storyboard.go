package projecthttp

import (
	"net/http"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (h *Handler) serveStoryboardRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) < 7 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" || segments[4] != "episodes" {
		return false
	}
	projectID, episodeID, ok := parseStoryboardScope(response, segments[3], segments[5])
	if !ok {
		return true
	}
	switch {
	case len(segments) == 7 && segments[6] == "storyboard":
		h.storyboard(response, request, projectID, episodeID)
	case len(segments) == 7 && segments[6] == "storyboard:reset":
		h.resetStoryboard(response, request, projectID, episodeID)
	case len(segments) == 8 && segments[6] == "storyboard" && segments[7] == "validation":
		h.storyboardValidation(response, request, projectID, episodeID)
	case len(segments) == 7 && segments[6] == "scenes":
		h.createScene(response, request, projectID, episodeID)
	case len(segments) == 7 && segments[6] == "scenes:reorder":
		h.reorderScenes(response, request, projectID, episodeID)
	case len(segments) == 8 && segments[6] == "scenes":
		h.scene(response, request, projectID, episodeID, segments[7])
	case len(segments) == 7 && segments[6] == "clips":
		h.createClip(response, request, projectID, episodeID)
	case len(segments) == 7 && segments[6] == "clips:merge":
		h.mergeClips(response, request, projectID, episodeID)
	case len(segments) == 8 && segments[6] == "clips":
		h.clip(response, request, projectID, episodeID, segments[7])
	case len(segments) == 9 && segments[6] == "clips" && segments[8] == "asset-references":
		h.bindClipAsset(response, request, projectID, episodeID, segments[7])
	case len(segments) == 10 && segments[6] == "clips" && segments[8] == "asset-references":
		h.changeClipAsset(response, request, projectID, episodeID, segments[7], segments[9])
	default:
		return false
	}
	return true
}

func (h *Handler) resetStoryboard(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		ExpectedStoryboardRevision int64 `json:"expected_storyboard_revision"`
		Confirm                    bool  `json:"confirm"`
		RequestStopForRunningJobs  bool  `json:"request_stop_for_running_jobs"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	result, err := h.projects.ResetStoryboard(request.Context(), projectapp.ResetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision, Confirm: body.Confirm, RequestStopForRunningJobs: body.RequestStopForRunningJobs})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	jobs := make([]map[string]any, 0, len(result.ChangedJobs))
	for _, job := range result.ChangedJobs {
		jobs = append(jobs, mapGenerationJob(job))
	}
	status := http.StatusOK
	if result.WaitingForTerminalJobs {
		status = http.StatusAccepted
	}
	writeJSON(response, status, map[string]any{"reset": result.Reset, "waiting_for_terminal_jobs": result.WaitingForTerminalJobs, "changed_jobs": jobs, "storyboard": mapStoryboardResult(projectapp.StoryboardResult{Storyboard: result.Storyboard})["storyboard"]})
}

func parseStoryboardScope(response http.ResponseWriter, rawProjectID, rawEpisodeID string) (project.ProjectID, project.EpisodeID, bool) {
	projectID, projectErr := project.ParseProjectID(rawProjectID)
	episodeID, episodeErr := project.ParseEpisodeID(rawEpisodeID)
	if projectErr != nil || episodeErr != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return "", "", false
	}
	return projectID, episodeID, true
}

func (h *Handler) storyboard(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	result, err := h.projects.GetStoryboard(request.Context(), projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapStoryboardResult(result))
}

func (h *Handler) storyboardValidation(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	result, err := h.projects.GetStoryboardValidation(request.Context(), projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	issues := make([]map[string]any, 0, len(result.Issues))
	for _, issue := range result.Issues {
		item := map[string]any{"severity": issue.Severity, "code": issue.Code, "message": issue.Message, "suggested_action": issue.SuggestedAction}
		if issue.SceneID != "" {
			item["scene_id"] = issue.SceneID.String()
		}
		if issue.ClipID != "" {
			item["clip_id"] = issue.ClipID.String()
		}
		if issue.AssetID != "" {
			item["asset_id"] = issue.AssetID.String()
		}
		issues = append(issues, item)
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"project_id": result.ProjectID.String(), "episode_id": result.EpisodeID.String(), "storyboard_revision": result.StoryboardRevision,
		"planned_duration_ms": result.PlannedDurationMS, "target_duration_ms": result.TargetDurationMS, "valid": result.Valid, "issues": issues,
	})
}

type sceneSourceDTO struct {
	ScriptRevision int64 `json:"script_revision"`
	StartByte      int   `json:"start_byte"`
	EndByte        int   `json:"end_byte"`
}

func (h *Handler) createScene(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		Title                      string          `json:"title"`
		Description                string          `json:"description"`
		Source                     *sceneSourceDTO `json:"source"`
		ExpectedStoryboardRevision int64           `json:"expected_storyboard_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	var source *projectapp.SceneScriptSourceInput
	if body.Source != nil {
		source = &projectapp.SceneScriptSourceInput{ScriptRevision: body.Source.ScriptRevision, StartByte: body.Source.StartByte, EndByte: body.Source.EndByte}
	}
	value, err := h.projects.CreateScene(request.Context(), projectapp.CreateSceneInput{ProjectID: projectID, EpisodeID: episodeID,
		Title: body.Title, Description: body.Description, Source: source, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"scene": mapScene(value)})
}

func (h *Handler) reorderScenes(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		OrderedSceneIDs            []string `json:"ordered_scene_ids"`
		ExpectedStoryboardRevision int64    `json:"expected_storyboard_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	ordered := make([]project.SceneID, 0, len(body.OrderedSceneIDs))
	for _, raw := range body.OrderedSceneIDs {
		id, err := project.ParseSceneID(raw)
		if err != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		ordered = append(ordered, id)
	}
	result, err := h.projects.ReorderScenes(request.Context(), projectapp.ReorderScenesInput{ProjectID: projectID, EpisodeID: episodeID, OrderedSceneIDs: ordered, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapStoryboardResult(result))
}

func (h *Handler) scene(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, rawSceneID string) {
	action := ""
	for _, candidate := range []string{":delete", ":restore"} {
		if strings.HasSuffix(rawSceneID, candidate) {
			action, rawSceneID = candidate, strings.TrimSuffix(rawSceneID, candidate)
			break
		}
	}
	sceneID, err := project.ParseSceneID(rawSceneID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	current, err := h.projects.GetScene(request.Context(), projectapp.GetSceneInput{ProjectID: projectID, SceneID: sceneID})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if current.Scene.EpisodeID != episodeID {
		writeError(response, http.StatusNotFound, "not_found", "Scene was not found in this Episode")
		return
	}
	if action != "" {
		h.changeSceneDeletion(response, request, projectID, episodeID, sceneID, action == ":restore")
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, map[string]any{"scene": mapStoryboardScene(current)})
	case http.MethodPatch:
		var body struct {
			Title            string `json:"title"`
			Description      string `json:"description"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &body); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		value, err := h.projects.UpdateScene(request.Context(), projectapp.UpdateSceneInput{ProjectID: projectID, SceneID: sceneID, Title: body.Title, Description: body.Description, ExpectedRevision: body.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		if value.EpisodeID != episodeID {
			writeError(response, http.StatusNotFound, "not_found", "Scene was not found in this Episode")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"scene": mapScene(value)})
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) changeSceneDeletion(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, sceneID project.SceneID, restore bool) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		ExpectedRevision           int64 `json:"expected_revision"`
		ExpectedStoryboardRevision int64 `json:"expected_storyboard_revision"`
		ConfirmNonEmpty            bool  `json:"confirm_non_empty"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	var value project.Scene
	var err error
	if restore {
		value, err = h.projects.RestoreScene(request.Context(), projectapp.RestoreSceneInput{ProjectID: projectID, SceneID: sceneID, ExpectedRevision: body.ExpectedRevision, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	} else {
		value, err = h.projects.DeleteScene(request.Context(), projectapp.DeleteSceneInput{ProjectID: projectID, SceneID: sceneID, ExpectedRevision: body.ExpectedRevision, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision, ConfirmNonEmpty: body.ConfirmNonEmpty})
	}
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if value.EpisodeID != episodeID {
		writeError(response, http.StatusNotFound, "not_found", "Scene was not found in this Episode")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"scene": mapScene(value)})
}

type clipFieldsDTO struct {
	Title                  string                     `json:"title"`
	Description            string                     `json:"description"`
	Dialogue               string                     `json:"dialogue"`
	CreativeIntent         string                     `json:"creative_intent"`
	PlannedDurationSeconds int64                      `json:"planned_duration_seconds"`
	FirstImagePrompt       string                     `json:"first_image_prompt"`
	LastImagePrompt        string                     `json:"last_image_prompt"`
	VideoPrompt            string                     `json:"video_prompt"`
	VideoPrompts           *clipVideoPromptsDTO       `json:"video_prompts"`
	GenerationMode         project.ClipGenerationMode `json:"generation_mode"`
	ContinueFromPrevious   bool                       `json:"continue_from_previous"`
}

type clipVideoPromptsDTO struct {
	FirstImage     string `json:"first_image"`
	FirstLastImage string `json:"first_last_image"`
	Reference      string `json:"reference"`
}

func (f clipFieldsDTO) input() projectapp.ClipFields {
	prompts := project.ClipVideoPrompts{}
	if f.VideoPrompts != nil {
		prompts = project.ClipVideoPrompts{FirstImage: f.VideoPrompts.FirstImage, FirstLastImage: f.VideoPrompts.FirstLastImage, Reference: f.VideoPrompts.Reference}
	} else {
		prompts.SetForMode(f.GenerationMode, f.VideoPrompt)
	}
	return projectapp.ClipFields{Title: f.Title, Description: f.Description, Dialogue: f.Dialogue, CreativeIntent: f.CreativeIntent,
		PlannedDurationSeconds: f.PlannedDurationSeconds, FirstImagePrompt: f.FirstImagePrompt, LastImagePrompt: f.LastImagePrompt,
		VideoPrompts: prompts, GenerationMode: f.GenerationMode, ContinueFromPrevious: f.ContinueFromPrevious}
}

func (h *Handler) createClip(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		SceneID                    string `json:"scene_id"`
		ExpectedStoryboardRevision int64  `json:"expected_storyboard_revision"`
		clipFieldsDTO
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sceneID, err := project.ParseSceneID(body.SceneID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	value, err := h.projects.CreateClip(request.Context(), projectapp.CreateClipInput{ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID,
		ClipFields: body.input(), ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"clip": mapClipDetails(value)})
}

func (h *Handler) clip(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, rawClipID string) {
	action := ""
	for _, candidate := range []string{":move", ":delete", ":restore", ":split"} {
		if strings.HasSuffix(rawClipID, candidate) {
			action, rawClipID = candidate, strings.TrimSuffix(rawClipID, candidate)
			break
		}
	}
	clipID, err := project.ParseClipID(rawClipID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	current, ok := h.clipInEpisode(response, request, projectID, episodeID, clipID)
	if !ok {
		return
	}
	if action != "" {
		if action == ":split" {
			h.splitClip(response, request, projectID, episodeID, clipID)
			return
		}
		h.changeClipStructure(response, request, projectID, episodeID, clipID, action)
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, map[string]any{"clip": mapClipDetails(current)})
	case http.MethodPatch:
		var body struct {
			ExpectedRevision int64 `json:"expected_revision"`
			clipFieldsDTO
		}
		if err := decodeJSON(response, request, &body); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		value, err := h.projects.UpdateClip(request.Context(), projectapp.UpdateClipInput{ProjectID: projectID, ClipID: clipID, ClipFields: body.input(), ExpectedRevision: body.ExpectedRevision})
		if err != nil {
			writeMappedError(response, err)
			return
		}
		if value.Clip.EpisodeID != episodeID {
			writeError(response, http.StatusNotFound, "not_found", "Clip was not found in this Episode")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"clip": mapClipDetails(value)})
	default:
		methodNotAllowed(response)
	}
}

type replacementClipDTO struct {
	clipFieldsDTO
	AssetIDs []string `json:"asset_ids"`
}

func (value replacementClipDTO) input() (projectapp.ReplacementClipInput, error) {
	assetIDs := make([]project.AssetID, 0, len(value.AssetIDs))
	for _, raw := range value.AssetIDs {
		assetID, err := project.ParseAssetID(raw)
		if err != nil {
			return projectapp.ReplacementClipInput{}, err
		}
		assetIDs = append(assetIDs, assetID)
	}
	return projectapp.ReplacementClipInput{ClipFields: value.clipFieldsDTO.input(), AssetIDs: assetIDs}, nil
}

func (h *Handler) splitClip(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, clipID project.ClipID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		ExpectedRevision           int64                `json:"expected_revision"`
		ExpectedStoryboardRevision int64                `json:"expected_storyboard_revision"`
		Replacements               []replacementClipDTO `json:"replacements"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	replacements := make([]projectapp.ReplacementClipInput, 0, len(body.Replacements))
	for _, candidate := range body.Replacements {
		value, err := candidate.input()
		if err != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		replacements = append(replacements, value)
	}
	result, err := h.projects.SplitClip(request.Context(), projectapp.SplitClipInput{
		ProjectID: projectID, ClipID: clipID, ExpectedRevision: body.ExpectedRevision,
		ExpectedStoryboardRevision: body.ExpectedStoryboardRevision, Replacements: replacements,
	})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if !replacementResultInEpisode(result, episodeID) {
		writeError(response, http.StatusNotFound, "not_found", "Clip was not found in this Episode")
		return
	}
	writeJSON(response, http.StatusCreated, mapReplaceClipsResult(result))
}

func (h *Handler) mergeClips(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		Sources []struct {
			ClipID           string `json:"clip_id"`
			ExpectedRevision int64  `json:"expected_revision"`
		} `json:"sources"`
		Replacement                replacementClipDTO `json:"replacement"`
		ExpectedStoryboardRevision int64              `json:"expected_storyboard_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sources := make([]projectapp.MergeClipSource, 0, len(body.Sources))
	for _, candidate := range body.Sources {
		clipID, err := project.ParseClipID(candidate.ClipID)
		if err != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		if _, ok := h.clipInEpisode(response, request, projectID, episodeID, clipID); !ok {
			return
		}
		sources = append(sources, projectapp.MergeClipSource{ClipID: clipID, ExpectedRevision: candidate.ExpectedRevision})
	}
	replacement, err := body.Replacement.input()
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	result, err := h.projects.MergeClips(request.Context(), projectapp.MergeClipsInput{ProjectID: projectID, Sources: sources,
		Replacement: replacement, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if !replacementResultInEpisode(result, episodeID) {
		writeError(response, http.StatusNotFound, "not_found", "Clips were not found in this Episode")
		return
	}
	writeJSON(response, http.StatusCreated, mapReplaceClipsResult(result))
}

func (h *Handler) changeClipStructure(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, clipID project.ClipID, action string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var body struct {
		TargetSceneID              string `json:"target_scene_id"`
		BeforeClipID               string `json:"before_clip_id"`
		AfterClipID                string `json:"after_clip_id"`
		ExpectedRevision           int64  `json:"expected_revision"`
		ExpectedStoryboardRevision int64  `json:"expected_storyboard_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	var value project.ClipDetails
	var err error
	switch action {
	case ":move":
		target, parseErr := project.ParseSceneID(body.TargetSceneID)
		if parseErr != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		var beforeID, afterID project.ClipID
		if body.BeforeClipID != "" {
			beforeID, parseErr = project.ParseClipID(body.BeforeClipID)
		}
		if parseErr == nil && body.AfterClipID != "" {
			afterID, parseErr = project.ParseClipID(body.AfterClipID)
		}
		if parseErr != nil || (beforeID != "" && afterID != "") {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		value, err = h.projects.MoveClip(request.Context(), projectapp.MoveClipInput{ProjectID: projectID, ClipID: clipID, TargetSceneID: target, BeforeClipID: beforeID, AfterClipID: afterID, ExpectedRevision: body.ExpectedRevision, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	case ":delete":
		value, err = h.projects.DeleteClip(request.Context(), projectapp.DeleteClipInput{ProjectID: projectID, ClipID: clipID, ExpectedRevision: body.ExpectedRevision, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	case ":restore":
		value, err = h.projects.RestoreClip(request.Context(), projectapp.RestoreClipInput{ProjectID: projectID, ClipID: clipID, ExpectedRevision: body.ExpectedRevision, ExpectedStoryboardRevision: body.ExpectedStoryboardRevision})
	}
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if value.Clip.EpisodeID != episodeID {
		writeError(response, http.StatusNotFound, "not_found", "Clip was not found in this Episode")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"clip": mapClipDetails(value)})
}

func (h *Handler) bindClipAsset(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, rawClipID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	clipID, err := project.ParseClipID(rawClipID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	if _, ok := h.clipInEpisode(response, request, projectID, episodeID, clipID); !ok {
		return
	}
	var body struct {
		AssetID              string `json:"asset_id"`
		ExpectedClipRevision int64  `json:"expected_clip_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	assetID, err := project.ParseAssetID(body.AssetID)
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	value, err := h.projects.BindClipAsset(request.Context(), projectapp.BindClipAssetInput{ProjectID: projectID, ClipID: clipID, AssetID: assetID, ExpectedClipRevision: body.ExpectedClipRevision})
	h.writeClipAssetResult(response, value, err, episodeID)
}

func (h *Handler) changeClipAsset(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, rawClipID, rawAssetID string) {
	replace := strings.HasSuffix(rawAssetID, ":replace")
	if replace {
		rawAssetID = strings.TrimSuffix(rawAssetID, ":replace")
	}
	wantMethod := http.MethodDelete
	if replace {
		wantMethod = http.MethodPost
	}
	if request.Method != wantMethod {
		methodNotAllowed(response)
		return
	}
	clipID, clipErr := project.ParseClipID(rawClipID)
	assetID, assetErr := project.ParseAssetID(rawAssetID)
	if clipErr != nil || assetErr != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return
	}
	if _, ok := h.clipInEpisode(response, request, projectID, episodeID, clipID); !ok {
		return
	}
	var body struct {
		NewAssetID           string `json:"new_asset_id"`
		ExpectedClipRevision int64  `json:"expected_clip_revision"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	var value project.ClipDetails
	var err error
	if replace {
		newID, parseErr := project.ParseAssetID(body.NewAssetID)
		if parseErr != nil {
			writeMappedError(response, projectapp.ErrInvalidRequest)
			return
		}
		value, err = h.projects.ReplaceClipAsset(request.Context(), projectapp.ReplaceClipAssetInput{ProjectID: projectID, ClipID: clipID, OldAssetID: assetID, NewAssetID: newID, ExpectedClipRevision: body.ExpectedClipRevision})
	} else {
		value, err = h.projects.UnbindClipAsset(request.Context(), projectapp.UnbindClipAssetInput{ProjectID: projectID, ClipID: clipID, AssetID: assetID, ExpectedClipRevision: body.ExpectedClipRevision})
	}
	h.writeClipAssetResult(response, value, err, episodeID)
}

func (h *Handler) writeClipAssetResult(response http.ResponseWriter, value project.ClipDetails, err error, episodeID project.EpisodeID) {
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if value.Clip.EpisodeID != episodeID {
		writeError(response, http.StatusNotFound, "not_found", "Clip was not found in this Episode")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"clip": mapClipDetails(value)})
}

func (h *Handler) clipInEpisode(response http.ResponseWriter, request *http.Request, projectID project.ProjectID, episodeID project.EpisodeID, clipID project.ClipID) (project.ClipDetails, bool) {
	value, err := h.projects.GetClip(request.Context(), projectapp.GetClipInput{ProjectID: projectID, ClipID: clipID})
	if err != nil {
		writeMappedError(response, err)
		return project.ClipDetails{}, false
	}
	if value.Clip.EpisodeID != episodeID {
		writeError(response, http.StatusNotFound, "not_found", "Clip was not found in this Episode")
		return project.ClipDetails{}, false
	}
	return value, true
}

func mapStoryboardResult(value projectapp.StoryboardResult) map[string]any {
	scenes := make([]map[string]any, 0, len(value.Storyboard.Scenes))
	for index, scene := range value.Storyboard.Scenes {
		mapped := mapStoryboardScene(scene)
		mapped["position"] = index + 1
		scenes = append(scenes, mapped)
	}
	return map[string]any{"storyboard": map[string]any{
		"episode_id": value.Storyboard.EpisodeID.String(), "revision": value.Storyboard.Revision,
		"planned_duration_ms": value.Storyboard.PlannedDurationSeconds * 1_000, "scenes": scenes,
	}, "target_duration_ms": value.TargetDuration.Milliseconds(), "duration_warning": value.DurationWarning}
}

func replacementResultInEpisode(value projectapp.ReplaceClipsResult, episodeID project.EpisodeID) bool {
	for _, clip := range value.Created {
		if clip.Clip.EpisodeID != episodeID {
			return false
		}
	}
	for _, clip := range value.Replaced {
		if clip.Clip.EpisodeID != episodeID {
			return false
		}
	}
	return value.Storyboard.Storyboard.EpisodeID == episodeID
}

func mapReplaceClipsResult(value projectapp.ReplaceClipsResult) map[string]any {
	created := make([]map[string]any, 0, len(value.Created))
	for _, clip := range value.Created {
		created = append(created, mapClipDetails(clip))
	}
	replaced := make([]map[string]any, 0, len(value.Replaced))
	for _, clip := range value.Replaced {
		replaced = append(replaced, mapClipDetails(clip))
	}
	result := mapStoryboardResult(value.Storyboard)
	result["created_clips"] = created
	result["replaced_clips"] = replaced
	return result
}

func mapStoryboardScene(value project.StoryboardScene) map[string]any {
	result := mapScene(value.Scene)
	clips := make([]map[string]any, 0, len(value.Clips))
	for index, clip := range value.Clips {
		mapped := mapClipDetails(clip)
		mapped["position"] = index + 1
		clips = append(clips, mapped)
	}
	result["clips"] = clips
	return result
}

func mapScene(value project.Scene) map[string]any {
	result := map[string]any{"scene_id": value.ID.String(), "project_id": value.ProjectID.String(), "episode_id": value.EpisodeID.String(),
		"position": value.Position, "title": value.Title, "description": value.Description, "revision": value.Revision,
		"created_at": value.CreatedAt, "updated_at": value.UpdatedAt}
	if value.Source != nil {
		result["source"] = map[string]any{"script_revision": value.Source.ScriptRevision, "start_byte": value.Source.StartByte, "end_byte": value.Source.EndByte, "excerpt": value.Source.Excerpt}
	}
	if value.DeletedAt != nil {
		result["deleted_at"] = *value.DeletedAt
	}
	return result
}

func mapClipDetails(value project.ClipDetails) map[string]any {
	clip := value.Clip
	result := map[string]any{"clip_id": clip.ID.String(), "project_id": clip.ProjectID.String(), "episode_id": clip.EpisodeID.String(), "scene_id": clip.SceneID.String(),
		"position": clip.Position, "title": clip.Title, "description": clip.Description, "dialogue": clip.Dialogue, "creative_intent": clip.CreativeIntent,
		"planned_duration_ms": clip.PlannedDurationSeconds * 1_000, "first_image_prompt": clip.FirstImagePrompt, "last_image_prompt": clip.LastImagePrompt,
		"video_prompt": clip.VideoPrompts.ForMode(clip.GenerationMode), "video_prompts": map[string]string{"first_image": clip.VideoPrompts.FirstImage, "first_last_image": clip.VideoPrompts.FirstLastImage, "reference": clip.VideoPrompts.Reference},
		"generation_mode": clip.GenerationMode, "continue_from_previous": clip.ContinueFromPrevious, "revision": clip.Revision, "created_at": clip.CreatedAt, "updated_at": clip.UpdatedAt}
	if slotID, err := project.FixedMediaSlotID(clip.ID.String(), project.MediaSlotClipFirstImage); err == nil {
		result["first_image_slot_id"] = slotID.String()
	}
	if slotID, err := project.FixedMediaSlotID(clip.ID.String(), project.MediaSlotClipLastImage); err == nil {
		result["last_image_slot_id"] = slotID.String()
	}
	if slotID, err := project.FixedMediaSlotID(clip.ID.String(), project.MediaSlotClipVideo); err == nil {
		result["video_slot_id"] = slotID.String()
	}
	if slotID, err := project.FixedMediaSlotID(clip.ID.String(), project.MediaSlotAudioPrimary); err == nil {
		result["reference_audio_slot_id"] = slotID.String()
	}
	if clip.DeletedAt != nil {
		result["deleted_at"] = *clip.DeletedAt
	}
	references := make([]map[string]any, 0, len(value.AssetReferences))
	for _, reference := range value.AssetReferences {
		references = append(references, map[string]any{"asset_id": reference.AssetID.String(), "asset_identity_id": reference.AssetIdentityID.String(),
			"kind": reference.Kind, "name": reference.Name, "asset_deleted": reference.AssetDeleted, "position": reference.Position})
	}
	result["asset_references"] = references
	derivedFrom := make([]string, 0, len(value.DerivedFromClipIDs))
	for _, clipID := range value.DerivedFromClipIDs {
		derivedFrom = append(derivedFrom, clipID.String())
	}
	result["derived_from_clip_ids"] = derivedFrom
	return result
}
