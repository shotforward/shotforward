package projectapp

import (
	"context"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ResetStoryboard coordinates active Clip generation before atomically moving
// the current Storyboard to the project trash. If running Jobs remain, their
// stop is requested when allowed and the Storyboard is left untouched.
func (s *Service) ResetStoryboard(ctx context.Context, input ResetStoryboardInput) (ResetStoryboardResult, error) {
	handle, err := s.assetProjectHandle(ctx, "reset Storyboard", input.ProjectID)
	if err != nil {
		return ResetStoryboardResult{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return ResetStoryboardResult{}, fmt.Errorf("reset Storyboard: %w", err)
	}
	if !input.Confirm {
		return ResetStoryboardResult{}, fmt.Errorf("reset Storyboard: %w", ErrStoryboardResetConfirmation)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ResetStoryboardResult{}, fmt.Errorf("reset Storyboard: %w", err)
	}
	changed, reset, err := handle.Store.ResetStoryboard(ctx, input.EpisodeID, input.ExpectedStoryboardRevision, input.RequestStopForRunningJobs, now)
	if err != nil {
		return ResetStoryboardResult{}, fmt.Errorf("reset Storyboard: %w", err)
	}
	for _, job := range changed {
		s.publishGenerationJob(job)
	}
	if len(changed) > 0 && s.generationQueue != nil {
		s.generationQueue.Notify(input.ProjectID)
	}
	storyboard, err := handle.Store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return ResetStoryboardResult{}, fmt.Errorf("reset Storyboard: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return ResetStoryboardResult{}, err
	}
	waiting := !reset && len(storyboard.Scenes) > 0
	return ResetStoryboardResult{Storyboard: storyboard, ChangedJobs: changed, Reset: reset, WaitingForTerminalJobs: waiting}, nil
}

const maximumClipReplacements = 100

func (s *Service) GetStoryboard(ctx context.Context, input GetStoryboardInput) (StoryboardResult, error) {
	handle, err := s.assetProjectHandle(ctx, "get Storyboard", input.ProjectID)
	if err != nil {
		return StoryboardResult{}, err
	}
	episode, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return StoryboardResult{}, fmt.Errorf("get Storyboard: %w", err)
	}
	storyboard, err := handle.Store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return StoryboardResult{}, fmt.Errorf("get Storyboard: %w", err)
	}
	if err := storyboard.Validate(); err != nil {
		return StoryboardResult{}, fmt.Errorf("get Storyboard: %w: %w", ErrProjectDataInvalid, err)
	}
	planned := time.Duration(storyboard.PlannedDurationSeconds) * time.Second
	tolerance := max(2*time.Second, episode.TargetDuration/20)
	difference := planned - episode.TargetDuration
	if difference < 0 {
		difference = -difference
	}
	return StoryboardResult{Storyboard: storyboard, TargetDuration: episode.TargetDuration, DurationWarning: difference > tolerance}, nil
}

// GetStoryboardValidation derives structural and mode-input issues from the
// current Storyboard and MediaSlot selections. Generator-specific capability
// checks remain part of generation submission/readiness.
func (s *Service) GetStoryboardValidation(ctx context.Context, input GetStoryboardInput) (StoryboardValidationResult, error) {
	storyboardResult, err := s.GetStoryboard(ctx, input)
	if err != nil {
		return StoryboardValidationResult{}, err
	}
	handle, err := s.assetProjectHandle(ctx, "validate Storyboard", input.ProjectID)
	if err != nil {
		return StoryboardValidationResult{}, err
	}
	result := StoryboardValidationResult{
		ProjectID: input.ProjectID, EpisodeID: input.EpisodeID,
		StoryboardRevision: storyboardResult.Storyboard.Revision,
		PlannedDurationMS:  storyboardResult.Storyboard.PlannedDurationSeconds * 1_000,
		TargetDurationMS:   storyboardResult.TargetDuration.Milliseconds(),
		Valid:              true,
		Issues:             make([]StoryboardValidationIssue, 0),
	}
	appendIssue := func(issue StoryboardValidationIssue) {
		result.Issues = append(result.Issues, issue)
		if issue.Severity == StoryboardValidationError {
			result.Valid = false
		}
	}
	if len(storyboardResult.Storyboard.Scenes) == 0 {
		appendIssue(StoryboardValidationIssue{Severity: StoryboardValidationError, Code: StoryboardIssueEmpty,
			Message: "Storyboard has no Scenes.", SuggestedAction: "Create the first Storyboard from the current Episode script."})
	}
	if storyboardResult.DurationWarning {
		appendIssue(StoryboardValidationIssue{Severity: StoryboardValidationWarning, Code: StoryboardIssueDurationOutsideTolerance,
			Message: "Planned duration is outside the Episode target tolerance.", SuggestedAction: "Adjust Clip durations toward the Episode target."})
	}
	for _, scene := range storyboardResult.Storyboard.Scenes {
		if len(scene.Clips) == 0 {
			appendIssue(StoryboardValidationIssue{Severity: StoryboardValidationWarning, Code: StoryboardIssueEmptyScene, SceneID: scene.Scene.ID,
				Message: "Scene has no Clips.", SuggestedAction: "Add a Clip or remove the empty Scene."})
		}
		for _, clip := range scene.Clips {
			for _, reference := range clip.AssetReferences {
				if reference.AssetDeleted {
					appendIssue(StoryboardValidationIssue{Severity: StoryboardValidationError, Code: StoryboardIssueDeletedAssetReference,
						SceneID: scene.Scene.ID, ClipID: clip.Clip.ID, AssetID: reference.AssetID,
						Message: "Clip references a deleted Asset.", SuggestedAction: "Restore the Asset or replace the Clip binding."})
				}
			}
			if err := s.validateClipModeInputs(ctx, handle.Store, scene.Scene.ID, clip, appendIssue); err != nil {
				return StoryboardValidationResult{}, fmt.Errorf("validate Storyboard: %w", err)
			}
		}
	}
	return result, nil
}

func (s *Service) validateClipModeInputs(ctx context.Context, store ProjectStore, sceneID domain.SceneID, clip domain.ClipDetails, appendIssue func(StoryboardValidationIssue)) error {
	base := StoryboardValidationIssue{Severity: StoryboardValidationError, SceneID: sceneID, ClipID: clip.Clip.ID}
	switch clip.Clip.GenerationMode {
	case domain.ClipGenerationFirstImage:
		available, err := currentMediaAvailable(ctx, store, clip.Clip.ID.String(), domain.MediaSlotClipFirstImage)
		if err != nil {
			return err
		}
		if !available {
			issue := base
			issue.Code, issue.Message, issue.SuggestedAction = StoryboardIssueFirstFrameMissing, "First-frame video mode has no selected first frame.", "Generate, import, or select a first-frame image."
			appendIssue(issue)
		}
	case domain.ClipGenerationFirstLastImage:
		checks := []struct {
			kind    domain.MediaSlotKind
			code    StoryboardValidationCode
			message string
			action  string
		}{
			{domain.MediaSlotClipFirstImage, StoryboardIssueFirstFrameMissing, "First/last-frame video mode has no selected first frame.", "Generate, import, or select a first-frame image."},
			{domain.MediaSlotClipLastImage, StoryboardIssueLastFrameMissing, "First/last-frame video mode has no selected last frame.", "Generate, import, or select a last-frame image."},
		}
		for _, check := range checks {
			available, err := currentMediaAvailable(ctx, store, clip.Clip.ID.String(), check.kind)
			if err != nil {
				return err
			}
			if !available {
				issue := base
				issue.Code, issue.Message, issue.SuggestedAction = check.code, check.message, check.action
				appendIssue(issue)
			}
		}
	case domain.ClipGenerationReference:
		usable := false
		for _, reference := range clip.AssetReferences {
			if reference.AssetDeleted {
				continue
			}
			available, err := currentMediaAvailable(ctx, store, reference.AssetID.String(), domain.MediaSlotAssetPrimaryImage)
			if err != nil {
				return err
			}
			if available {
				usable = true
				continue
			}
			appendIssue(StoryboardValidationIssue{Severity: StoryboardValidationWarning, Code: StoryboardIssueAssetImageMissing,
				SceneID: sceneID, ClipID: clip.Clip.ID, AssetID: reference.AssetID,
				Message: "Bound Asset has no selected primary image.", SuggestedAction: "Generate, import, or select an Asset primary image."})
		}
		if !usable {
			issue := base
			issue.Code, issue.Message, issue.SuggestedAction = StoryboardIssueReferenceInputMissing, "Asset-reference video mode has no usable reference image.", "Bind an active Episode Asset with a selected primary image."
			appendIssue(issue)
		}
	default:
		return ErrProjectDataInvalid
	}
	return nil
}

func currentMediaAvailable(ctx context.Context, store ProjectStore, ownerID string, kind domain.MediaSlotKind) (bool, error) {
	id, err := domain.FixedMediaSlotID(ownerID, kind)
	if err != nil {
		return false, ErrProjectDataInvalid
	}
	details, err := store.MediaSlot(ctx, id, false)
	if err != nil {
		return false, err
	}
	if details.Slot.OwnerID != ownerID || details.Slot.Kind != kind {
		return false, ErrProjectDataInvalid
	}
	return details.Current != nil, nil
}

func (s *Service) GetScene(ctx context.Context, input GetSceneInput) (domain.StoryboardScene, error) {
	handle, err := s.assetProjectHandle(ctx, "get Scene", input.ProjectID)
	if err != nil {
		return domain.StoryboardScene{}, err
	}
	scene, err := handle.Store.SceneDetails(ctx, input.SceneID)
	if err != nil {
		return domain.StoryboardScene{}, fmt.Errorf("get Scene: %w", err)
	}
	if scene.Scene.ProjectID != input.ProjectID {
		return domain.StoryboardScene{}, fmt.Errorf("get Scene: %w", ErrProjectDataInvalid)
	}
	return scene, nil
}
func (s *Service) GetClip(ctx context.Context, input GetClipInput) (domain.ClipDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "get Clip", input.ProjectID)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	return clipForProject(ctx, handle.Store, input.ProjectID, input.ClipID)
}

func (s *Service) CreateScene(ctx context.Context, input CreateSceneInput) (domain.Scene, error) {
	handle, err := s.assetProjectHandle(ctx, "create Scene", input.ProjectID)
	if err != nil {
		return domain.Scene{}, err
	}
	episode, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w", err)
	}
	id, err := s.ids.NewSceneID()
	if err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w: %w", ErrProjectWriteFailed, err)
	}
	var source *domain.SceneScriptSource
	if input.Source != nil {
		if input.Source.ScriptRevision != episode.Script.Revision || input.Source.StartByte < 0 || input.Source.EndByte > len(episode.Script.Markdown) || input.Source.EndByte <= input.Source.StartByte {
			return domain.Scene{}, fmt.Errorf("create Scene: %w: invalid or stale script source", ErrInvalidRequest)
		}
		excerpt := episode.Script.Markdown[input.Source.StartByte:input.Source.EndByte]
		if !utf8.ValidString(excerpt) {
			return domain.Scene{}, fmt.Errorf("create Scene: %w: source range splits UTF-8 text", ErrInvalidRequest)
		}
		source = &domain.SceneScriptSource{ScriptRevision: input.Source.ScriptRevision, StartByte: input.Source.StartByte, EndByte: input.Source.EndByte, Excerpt: excerpt}
	}
	storyboard, err := handle.Store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w", err)
	}
	position, err := nextStoryboardOrderKey(lastSceneOrderKey(storyboard))
	if err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w", err)
	}
	scene := domain.Scene{ID: id, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Position: position, Title: input.Title, Description: domain.NormalizeStoryboardText(input.Description), Source: source, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := scene.Validate(); err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.CreateScene(ctx, scene, input.ExpectedStoryboardRevision, now); err != nil {
		return domain.Scene{}, fmt.Errorf("create Scene: %w", err)
	}
	return s.finishSceneMutation(ctx, handle, scene.ID, now)
}

func (s *Service) UpdateScene(ctx context.Context, input UpdateSceneInput) (domain.Scene, error) {
	handle, current, err := s.mutableScene(ctx, "update Scene", input.ProjectID, input.SceneID, input.ExpectedRevision, false)
	if err != nil {
		return domain.Scene{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.Scene{}, fmt.Errorf("update Scene: %w", err)
	}
	current.Title = input.Title
	current.Description = domain.NormalizeStoryboardText(input.Description)
	current.Revision++
	current.UpdatedAt = now
	if err := current.Validate(); err != nil {
		return domain.Scene{}, fmt.Errorf("update Scene: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateScene(ctx, current, input.ExpectedRevision, now); err != nil {
		return domain.Scene{}, fmt.Errorf("update Scene: %w", err)
	}
	return s.finishSceneMutation(ctx, handle, current.ID, now)
}

func (s *Service) ReorderScenes(ctx context.Context, input ReorderScenesInput) (StoryboardResult, error) {
	handle, err := s.assetProjectHandle(ctx, "reorder Scenes", input.ProjectID)
	if err != nil {
		return StoryboardResult{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return StoryboardResult{}, fmt.Errorf("reorder Scenes: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return StoryboardResult{}, fmt.Errorf("reorder Scenes: %w", err)
	}
	if err := handle.Store.ReorderScenes(ctx, input.EpisodeID, input.OrderedSceneIDs, input.ExpectedStoryboardRevision, now); err != nil {
		return StoryboardResult{}, fmt.Errorf("reorder Scenes: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return StoryboardResult{}, err
	}
	return s.GetStoryboard(ctx, GetStoryboardInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID})
}

func (s *Service) DeleteScene(ctx context.Context, input DeleteSceneInput) (domain.Scene, error) {
	handle, current, err := s.mutableScene(ctx, "delete Scene", input.ProjectID, input.SceneID, input.ExpectedRevision, false)
	if err != nil {
		return domain.Scene{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.Scene{}, fmt.Errorf("delete Scene: %w", err)
	}
	current.Revision++
	current.UpdatedAt = now
	current.DeletedAt = &now
	if err := handle.Store.DeleteScene(ctx, current, input.ExpectedRevision, input.ExpectedStoryboardRevision, input.ConfirmNonEmpty, now); err != nil {
		return domain.Scene{}, fmt.Errorf("delete Scene: %w", err)
	}
	return s.finishSceneMutation(ctx, handle, current.ID, now)
}

func (s *Service) RestoreScene(ctx context.Context, input RestoreSceneInput) (domain.Scene, error) {
	handle, current, err := s.mutableScene(ctx, "restore Scene", input.ProjectID, input.SceneID, input.ExpectedRevision, true)
	if err != nil {
		return domain.Scene{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.Scene{}, fmt.Errorf("restore Scene: %w", err)
	}
	current.Revision++
	current.UpdatedAt = now
	current.DeletedAt = nil
	if err := handle.Store.RestoreScene(ctx, current, input.ExpectedRevision, input.ExpectedStoryboardRevision, now); err != nil {
		return domain.Scene{}, fmt.Errorf("restore Scene: %w", err)
	}
	return s.finishSceneMutation(ctx, handle, current.ID, now)
}

func (s *Service) CreateClip(ctx context.Context, input CreateClipInput) (domain.ClipDetails, error) {
	if err := validateClipFieldsForWrite(input.ClipFields); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w: %w", ErrInvalidRequest, err)
	}
	handle, err := s.assetProjectHandle(ctx, "create Clip", input.ProjectID)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	scene, err := sceneForProject(ctx, handle.Store, input.ProjectID, input.SceneID)
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	if scene.IsDeleted() {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", ErrSceneDeleted)
	}
	if scene.EpisodeID != input.EpisodeID {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w: Scene belongs to another Episode", ErrInvalidRequest)
	}
	id, err := s.ids.NewClipID()
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	details, err := handle.Store.SceneDetails(ctx, input.SceneID)
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	position, err := nextStoryboardOrderKey(lastClipOrderKey(details.Clips))
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	clip := newClipFromFields(id, input.ProjectID, input.EpisodeID, input.SceneID, position, input.ClipFields, now)
	if err := clip.Validate(); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.CreateClip(ctx, clip, input.ExpectedStoryboardRevision, now); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("create Clip: %w", err)
	}
	return s.finishClipMutation(ctx, handle, clip.ID, now)
}

func lastSceneOrderKey(storyboard domain.Storyboard) int {
	if len(storyboard.Scenes) == 0 {
		return 0
	}
	return storyboard.Scenes[len(storyboard.Scenes)-1].Scene.Position
}

func lastClipOrderKey(clips []domain.ClipDetails) int {
	if len(clips) == 0 {
		return 0
	}
	return clips[len(clips)-1].Clip.Position
}

func nextStoryboardOrderKey(current int) (int, error) {
	if current > math.MaxInt-domain.StoryboardOrderStep {
		return 0, fmt.Errorf("%w: Storyboard order is exhausted", ErrInvalidRequest)
	}
	return current + domain.StoryboardOrderStep, nil
}

func (s *Service) UpdateClip(ctx context.Context, input UpdateClipInput) (domain.ClipDetails, error) {
	if err := validateClipFieldsForWrite(input.ClipFields); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("update Clip: %w: %w", ErrInvalidRequest, err)
	}
	handle, current, err := s.mutableClip(ctx, "update Clip", input.ProjectID, input.ClipID, input.ExpectedRevision, false)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("update Clip: %w", err)
	}
	applyClipFields(&current.Clip, input.ClipFields)
	current.Clip.Revision++
	current.Clip.UpdatedAt = now
	if err := current.Clip.Validate(); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("update Clip: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateClip(ctx, current.Clip, input.ExpectedRevision, now); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("update Clip: %w", err)
	}
	return s.finishClipMutation(ctx, handle, current.Clip.ID, now)
}

func validateClipFieldsForWrite(fields ClipFields) error {
	if err := domain.ValidateClipPlanningDuration(fields.PlannedDurationSeconds, false); err != nil {
		return err
	}
	return nil
}

func (s *Service) MoveClip(ctx context.Context, input MoveClipInput) (domain.ClipDetails, error) {
	if input.BeforeClipID != "" && input.AfterClipID != "" {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w: specify only one placement anchor", ErrInvalidRequest)
	}
	handle, current, err := s.mutableClip(ctx, "move Clip", input.ProjectID, input.ClipID, input.ExpectedRevision, false)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	target, err := sceneForProject(ctx, handle.Store, input.ProjectID, input.TargetSceneID)
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w", err)
	}
	if target.IsDeleted() {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w", ErrSceneDeleted)
	}
	if target.EpisodeID != current.Clip.EpisodeID {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w: target belongs to another Episode", ErrInvalidRequest)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w", err)
	}
	current.Clip.SceneID = input.TargetSceneID
	current.Clip.Revision++
	current.Clip.UpdatedAt = now
	if err := current.Clip.Validate(); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.MoveClip(ctx, current.Clip, input.BeforeClipID, input.AfterClipID, input.ExpectedRevision, input.ExpectedStoryboardRevision, now); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("move Clip: %w", err)
	}
	return s.finishClipMutation(ctx, handle, current.Clip.ID, now)
}

func (s *Service) DeleteClip(ctx context.Context, input DeleteClipInput) (domain.ClipDetails, error) {
	return s.changeClipDeletion(ctx, "delete Clip", input.ProjectID, input.ClipID, input.ExpectedRevision, input.ExpectedStoryboardRevision, false)
}
func (s *Service) RestoreClip(ctx context.Context, input RestoreClipInput) (domain.ClipDetails, error) {
	return s.changeClipDeletion(ctx, "restore Clip", input.ProjectID, input.ClipID, input.ExpectedRevision, input.ExpectedStoryboardRevision, true)
}

func (s *Service) changeClipDeletion(ctx context.Context, operation string, projectID domain.ProjectID, clipID domain.ClipID, expected, storyboardRevision int64, restore bool) (domain.ClipDetails, error) {
	handle, current, err := s.mutableClip(ctx, operation, projectID, clipID, expected, restore)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	if restore && current.Clip.DeletedWithSceneID != "" {
		return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, ErrSceneDeleted)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	current.Clip.Revision++
	current.Clip.UpdatedAt = now
	if restore {
		current.Clip.DeletedAt = nil
	} else {
		current.Clip.DeletedAt = &now
	}
	if restore {
		err = handle.Store.RestoreClip(ctx, current.Clip, expected, storyboardRevision, now)
	} else {
		err = handle.Store.DeleteClip(ctx, current.Clip, expected, storyboardRevision, now)
	}
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	return s.finishClipMutation(ctx, handle, current.Clip.ID, now)
}

func (s *Service) BindClipAsset(ctx context.Context, input BindClipAssetInput) (domain.ClipDetails, error) {
	return s.changeClipAsset(ctx, "bind Clip Asset", input.ProjectID, input.ClipID, input.AssetID, "", input.ExpectedClipRevision)
}
func (s *Service) UnbindClipAsset(ctx context.Context, input UnbindClipAssetInput) (domain.ClipDetails, error) {
	return s.changeClipAsset(ctx, "unbind Clip Asset", input.ProjectID, input.ClipID, "", input.AssetID, input.ExpectedClipRevision)
}
func (s *Service) ReplaceClipAsset(ctx context.Context, input ReplaceClipAssetInput) (domain.ClipDetails, error) {
	return s.changeClipAsset(ctx, "replace Clip Asset", input.ProjectID, input.ClipID, input.NewAssetID, input.OldAssetID, input.ExpectedClipRevision)
}

func (s *Service) SplitClip(ctx context.Context, input SplitClipInput) (ReplaceClipsResult, error) {
	if len(input.Replacements) < 2 || len(input.Replacements) > maximumClipReplacements {
		return ReplaceClipsResult{}, fmt.Errorf("split Clip: %w: replacement count must be between 2 and %d", ErrInvalidRequest, maximumClipReplacements)
	}
	handle, source, err := s.mutableClip(ctx, "split Clip", input.ProjectID, input.ClipID, input.ExpectedRevision, false)
	if err != nil {
		return ReplaceClipsResult{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("split Clip: %w", err)
	}
	replacements := make([]domain.ClipReplacement, 0, len(input.Replacements))
	for index, candidate := range input.Replacements {
		replacement, err := s.buildClipReplacement(ctx, handle.Store, source.Clip, source.Clip.Position+index, candidate, []domain.ClipID{source.Clip.ID}, now)
		if err != nil {
			return ReplaceClipsResult{}, fmt.Errorf("split Clip: %w", err)
		}
		replacements = append(replacements, replacement)
	}
	if err := handle.Store.ReplaceClips(ctx, []domain.Clip{source.Clip}, replacements, input.ExpectedStoryboardRevision, now); err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("split Clip: %w", err)
	}
	return s.finishClipReplacement(ctx, handle, []domain.ClipID{source.Clip.ID}, replacements, now)
}

func (s *Service) MergeClips(ctx context.Context, input MergeClipsInput) (ReplaceClipsResult, error) {
	if len(input.Sources) < 2 || len(input.Sources) > maximumClipReplacements {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w: source count must be between 2 and %d", ErrInvalidRequest, maximumClipReplacements)
	}
	var handle ProjectHandle
	sources := make([]domain.Clip, 0, len(input.Sources))
	for index, candidate := range input.Sources {
		currentHandle, current, err := s.mutableClip(ctx, "merge Clips", input.ProjectID, candidate.ClipID, candidate.ExpectedRevision, false)
		if err != nil {
			return ReplaceClipsResult{}, err
		}
		if index == 0 {
			handle = currentHandle
		} else if current.Clip.SceneID != sources[0].SceneID {
			return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w: sources must belong to one Scene", ErrInvalidRequest)
		}
		sources = append(sources, current.Clip)
	}
	scene, err := handle.Store.SceneDetails(ctx, sources[0].SceneID)
	if err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w", err)
	}
	sourceStart := -1
	for index := 0; index+len(sources) <= len(scene.Clips); index++ {
		matches := true
		for sourceIndex, source := range sources {
			if scene.Clips[index+sourceIndex].Clip.ID != source.ID {
				matches = false
				break
			}
		}
		if matches {
			sourceStart = index
			break
		}
	}
	if sourceStart < 0 {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w: sources must be adjacent and ordered within one Scene", ErrInvalidRequest)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w", err)
	}
	sourceIDs := make([]domain.ClipID, 0, len(sources))
	for _, source := range sources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	replacement, err := s.buildClipReplacement(ctx, handle.Store, sources[0], sources[0].Position, input.Replacement, sourceIDs, now)
	if err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w", err)
	}
	if err := handle.Store.ReplaceClips(ctx, sources, []domain.ClipReplacement{replacement}, input.ExpectedStoryboardRevision, now); err != nil {
		return ReplaceClipsResult{}, fmt.Errorf("merge Clips: %w", err)
	}
	return s.finishClipReplacement(ctx, handle, sourceIDs, []domain.ClipReplacement{replacement}, now)
}

func (s *Service) buildClipReplacement(ctx context.Context, store ProjectStore, source domain.Clip, position int, input ReplacementClipInput, sourceIDs []domain.ClipID, at time.Time) (domain.ClipReplacement, error) {
	if err := validateClipFieldsForWrite(input.ClipFields); err != nil {
		return domain.ClipReplacement{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	id, err := s.ids.NewClipID()
	if err != nil {
		return domain.ClipReplacement{}, fmt.Errorf("%w: generate replacement Clip ID: %v", ErrProjectWriteFailed, err)
	}
	assetIDs := make([]domain.AssetID, 0, len(input.AssetIDs))
	seen := make(map[domain.AssetID]struct{}, len(input.AssetIDs))
	for _, assetID := range input.AssetIDs {
		if _, duplicate := seen[assetID]; duplicate {
			return domain.ClipReplacement{}, fmt.Errorf("%w: duplicate replacement Asset", ErrInvalidRequest)
		}
		asset, err := assetForProject(ctx, store, source.ProjectID, assetID)
		if err != nil {
			return domain.ClipReplacement{}, err
		}
		if asset.Asset.IsDeleted() || asset.Asset.EpisodeID != source.EpisodeID {
			return domain.ClipReplacement{}, fmt.Errorf("%w: replacement Asset is deleted or belongs to another Episode", ErrInvalidRequest)
		}
		seen[assetID] = struct{}{}
		assetIDs = append(assetIDs, assetID)
	}
	value := domain.ClipReplacement{
		Clip:     newClipFromFields(id, source.ProjectID, source.EpisodeID, source.SceneID, position, input.ClipFields, at),
		AssetIDs: assetIDs, SourceClipIDs: append([]domain.ClipID(nil), sourceIDs...),
	}
	if err := value.Validate(); err != nil {
		return domain.ClipReplacement{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return value, nil
}

func (s *Service) finishClipReplacement(ctx context.Context, handle ProjectHandle, sourceIDs []domain.ClipID, replacements []domain.ClipReplacement, at time.Time) (ReplaceClipsResult, error) {
	created := make([]domain.ClipDetails, 0, len(replacements))
	for _, replacement := range replacements {
		value, err := clipForProject(ctx, handle.Store, handle.ProjectID, replacement.Clip.ID)
		if err != nil {
			return ReplaceClipsResult{}, err
		}
		created = append(created, value)
	}
	replaced := make([]domain.ClipDetails, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		value, err := clipForProject(ctx, handle.Store, handle.ProjectID, id)
		if err != nil {
			return ReplaceClipsResult{}, err
		}
		replaced = append(replaced, value)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, at); err != nil {
		return ReplaceClipsResult{}, err
	}
	storyboard, err := s.GetStoryboard(ctx, GetStoryboardInput{ProjectID: handle.ProjectID, EpisodeID: replacements[0].Clip.EpisodeID})
	if err != nil {
		return ReplaceClipsResult{}, err
	}
	return ReplaceClipsResult{Storyboard: storyboard, Created: created, Replaced: replaced}, nil
}

func (s *Service) changeClipAsset(ctx context.Context, operation string, projectID domain.ProjectID, clipID domain.ClipID, newAssetID, oldAssetID domain.AssetID, expected int64) (domain.ClipDetails, error) {
	handle, current, err := s.mutableClip(ctx, operation, projectID, clipID, expected, false)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	if newAssetID != "" {
		asset, err := assetForProject(ctx, handle.Store, projectID, newAssetID)
		if err != nil {
			return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
		}
		if asset.Asset.IsDeleted() {
			return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, ErrAssetDeleted)
		}
		if asset.Asset.EpisodeID != current.Clip.EpisodeID {
			return domain.ClipDetails{}, fmt.Errorf("%s: %w: Asset belongs to another Episode", operation, ErrInvalidRequest)
		}
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	switch {
	case oldAssetID == "":
		err = handle.Store.BindClipAsset(ctx, clipID, newAssetID, expected, now)
	case newAssetID == "":
		err = handle.Store.UnbindClipAsset(ctx, clipID, oldAssetID, expected, now)
	default:
		err = handle.Store.ReplaceClipAsset(ctx, clipID, oldAssetID, newAssetID, expected, now)
	}
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	return s.finishClipMutation(ctx, handle, clipID, now)
}

func (s *Service) GetAssetUsage(ctx context.Context, input GetAssetInput) (AssetUsage, error) {
	handle, err := s.assetProjectHandle(ctx, "get Asset usage", input.ProjectID)
	if err != nil {
		return AssetUsage{}, err
	}
	if _, err := assetForProject(ctx, handle.Store, input.ProjectID, input.AssetID); err != nil {
		return AssetUsage{}, fmt.Errorf("get Asset usage: %w", err)
	}
	clips, err := handle.Store.AssetUsage(ctx, input.AssetID)
	if err != nil {
		return AssetUsage{}, fmt.Errorf("get Asset usage: %w", err)
	}
	for _, clip := range clips {
		if clip.ProjectID != input.ProjectID {
			return AssetUsage{}, fmt.Errorf("get Asset usage: %w", ErrProjectDataInvalid)
		}
	}
	return AssetUsage{AssetID: input.AssetID, Clips: clips}, nil
}

func newClipFromFields(id domain.ClipID, projectID domain.ProjectID, episodeID domain.EpisodeID, sceneID domain.SceneID, position int, fields ClipFields, now time.Time) domain.Clip {
	clip := domain.Clip{ID: id, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Position: position, Revision: 1, CreatedAt: now, UpdatedAt: now}
	applyClipFields(&clip, fields)
	return clip
}
func applyClipFields(clip *domain.Clip, fields ClipFields) {
	clip.Title = fields.Title
	clip.Description = domain.NormalizeStoryboardText(fields.Description)
	clip.Dialogue = domain.NormalizeStoryboardText(fields.Dialogue)
	clip.CreativeIntent = domain.NormalizeStoryboardText(fields.CreativeIntent)
	clip.PlannedDurationSeconds = fields.PlannedDurationSeconds
	clip.FirstImagePrompt = domain.NormalizeStoryboardText(fields.FirstImagePrompt)
	clip.LastImagePrompt = domain.NormalizeStoryboardText(fields.LastImagePrompt)
	clip.VideoPrompts = domain.ClipVideoPrompts{
		FirstImage:     domain.NormalizeStoryboardText(fields.VideoPrompts.FirstImage),
		FirstLastImage: domain.NormalizeStoryboardText(fields.VideoPrompts.FirstLastImage),
		Reference:      domain.NormalizeStoryboardText(fields.VideoPrompts.Reference),
	}
	clip.GenerationMode = fields.GenerationMode
	clip.ContinueFromPrevious = fields.ContinueFromPrevious
}

func sceneForProject(ctx context.Context, store ProjectStore, projectID domain.ProjectID, sceneID domain.SceneID) (domain.Scene, error) {
	if err := sceneID.Validate(); err != nil {
		return domain.Scene{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	scene, err := store.Scene(ctx, sceneID)
	if err != nil {
		return domain.Scene{}, err
	}
	if err := scene.Validate(); err != nil || scene.ProjectID != projectID {
		return domain.Scene{}, ErrProjectDataInvalid
	}
	return scene, nil
}
func clipForProject(ctx context.Context, store ProjectStore, projectID domain.ProjectID, clipID domain.ClipID) (domain.ClipDetails, error) {
	if err := clipID.Validate(); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	clip, err := store.Clip(ctx, clipID)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	if err := clip.Validate(); err != nil || clip.Clip.ProjectID != projectID {
		return domain.ClipDetails{}, ErrProjectDataInvalid
	}
	return clip, nil
}
func (s *Service) mutableScene(ctx context.Context, operation string, projectID domain.ProjectID, id domain.SceneID, expected int64, wantDeleted bool) (ProjectHandle, domain.Scene, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.Scene{}, err
	}
	scene, err := sceneForProject(ctx, handle.Store, projectID, id)
	if err != nil {
		return ProjectHandle{}, domain.Scene{}, fmt.Errorf("%s: %w", operation, err)
	}
	if scene.IsDeleted() != wantDeleted {
		if wantDeleted {
			return ProjectHandle{}, domain.Scene{}, fmt.Errorf("%s: %w", operation, ErrSceneNotDeleted)
		}
		return ProjectHandle{}, domain.Scene{}, fmt.Errorf("%s: %w", operation, ErrSceneDeleted)
	}
	if expected < 1 || scene.Revision != expected {
		return ProjectHandle{}, domain.Scene{}, revisionConflict(operation, expected, scene.Revision)
	}
	if scene.Revision == math.MaxInt64 {
		return ProjectHandle{}, domain.Scene{}, fmt.Errorf("%s: revision exhausted", operation)
	}
	return handle, scene, nil
}
func (s *Service) mutableClip(ctx context.Context, operation string, projectID domain.ProjectID, id domain.ClipID, expected int64, wantDeleted bool) (ProjectHandle, domain.ClipDetails, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.ClipDetails{}, err
	}
	clip, err := clipForProject(ctx, handle.Store, projectID, id)
	if err != nil {
		return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	if clip.Clip.IsDeleted() != wantDeleted {
		if wantDeleted {
			return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, ErrClipNotDeleted)
		}
		return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, ErrClipDeleted)
	}
	if expected < 1 || clip.Clip.Revision != expected {
		return ProjectHandle{}, domain.ClipDetails{}, revisionConflict(operation, expected, clip.Clip.Revision)
	}
	if clip.Clip.Revision == math.MaxInt64 {
		return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: revision exhausted", operation)
	}
	return handle, clip, nil
}
func (s *Service) finishSceneMutation(ctx context.Context, handle ProjectHandle, id domain.SceneID, at time.Time) (domain.Scene, error) {
	scene, err := sceneForProject(ctx, handle.Store, handle.ProjectID, id)
	if err != nil {
		return domain.Scene{}, err
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, at); err != nil {
		return domain.Scene{}, err
	}
	return scene, nil
}
func (s *Service) finishClipMutation(ctx context.Context, handle ProjectHandle, id domain.ClipID, at time.Time) (domain.ClipDetails, error) {
	clip, err := clipForProject(ctx, handle.Store, handle.ProjectID, id)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, at); err != nil {
		return domain.ClipDetails{}, err
	}
	return clip, nil
}

func (s *Service) syncLibraryAfterMutation(ctx context.Context, handle ProjectHandle, at time.Time) error {
	result, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return err
	}
	if err := s.library.SyncProject(ctx, libraryRecord(handle.RootPath, result, at)); err != nil {
		return &RegistrationError{Category: ErrProjectUpdatedNotRegistered, RootPath: handle.RootPath, Cause: err}
	}
	return nil
}
