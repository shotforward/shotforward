package changesetapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var ErrInvalidRequest = errors.New("invalid ChangeSet request")

type Service struct {
	ids    IDGenerator
	clock  Clock
	stores StoreResolver
}

func NewService(ids IDGenerator, clock Clock, stores StoreResolver) (*Service, error) {
	if ids == nil || clock == nil || stores == nil {
		return nil, errors.New("create ChangeSet service: dependencies are required")
	}
	return &Service{ids: ids, clock: clock, stores: stores}, nil
}

func (s *Service) BeginDraft(ctx context.Context, input BeginDraftInput) (CommandResult, error) {
	if ctx == nil || input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil {
		return CommandResult{}, ErrInvalidRequest
	}
	id, err := s.ids.NewChangeSetID()
	if err != nil {
		return CommandResult{}, err
	}
	at, err := operationTime(s.clock.Now())
	if err != nil {
		return CommandResult{}, err
	}
	value := changeset.ChangeSet{ID: id, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Source: changeset.Source{AgentSessionID: input.AgentSessionID, AgentTurnID: input.AgentTurnID, CodexThreadID: input.CodexThreadID, CodexTurnID: input.CodexTurnID}, Title: input.Title, Summary: input.Summary, Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if value.ValidateNew() != nil {
		return CommandResult{}, ErrInvalidRequest
	}
	hash, err := fingerprint(struct{ ProjectID, EpisodeID, AgentSessionID, AgentTurnID, CodexThreadID, CodexTurnID, Title, Summary string }{input.ProjectID.String(), input.EpisodeID.String(), input.AgentSessionID.String(), input.AgentTurnID.String(), input.CodexThreadID, input.CodexTurnID, input.Title, input.Summary})
	if err != nil {
		return CommandResult{}, err
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, input.ProjectID)
	if err != nil {
		return CommandResult{}, err
	}
	created, replayed, err := store.CreateChangeSet(ctx, value, receipt(input.ProjectID, id, input.OperationKey, hash, changeset.CommandBegin, at))
	return CommandResult{ChangeSet: created, Replayed: replayed}, err
}

func (s *Service) AppendOperations(ctx context.Context, input AppendOperationsInput) (CommandResult, error) {
	if ctx == nil || input.ProjectID.Validate() != nil || input.ChangeSetID.Validate() != nil || input.ExpectedRevision < 1 || len(input.Operations) == 0 || len(input.Operations) > changeset.MaxAppendBatch {
		return CommandResult{}, ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, input.ProjectID)
	if err != nil {
		return CommandResult{}, err
	}
	current, err := store.ChangeSet(ctx, input.ChangeSetID)
	if err != nil {
		return CommandResult{}, err
	}
	if current.OperationCount > 0 {
		existing, readErr := store.ChangeSetOperations(ctx, current.ID, 0, 1)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		if len(existing) > 0 && existing[0].Type == changeset.RestoreEpisodeAsset {
			return CommandResult{}, ErrInvalidRequest
		}
	}
	for _, draft := range input.Operations {
		if draft.Type == changeset.RestoreEpisodeAsset && (current.OperationCount != 0 || len(input.Operations) != 1) {
			return CommandResult{}, ErrInvalidRequest
		}
	}
	at, err := operationTime(s.clock.Now())
	if err != nil {
		return CommandResult{}, err
	}
	sceneRefs, clipRefs, err := storyboardReferences(ctx, store, current)
	if err != nil {
		return CommandResult{}, err
	}
	operations := make([]changeset.Operation, 0, len(input.Operations))
	for index, draft := range input.Operations {
		operationID, err := s.ids.NewOperationID()
		if err != nil {
			return CommandResult{}, err
		}
		preconditionID, err := s.ids.NewPreconditionID()
		if err != nil {
			return CommandResult{}, err
		}
		operation := changeset.Operation{ID: operationID, ChangeSetID: input.ChangeSetID, Position: current.OperationCount + index + 1, Type: draft.Type, AssetPrompt: draft.AssetPrompt, ClipPrompts: draft.ClipPrompts, AssetCreation: draft.AssetCreation, AssetRestoration: draft.AssetRestoration}
		switch draft.Type {
		case changeset.UpdateAssetPrompt:
			if draft.AssetPrompt == nil {
				return CommandResult{}, ErrInvalidRequest
			}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetAsset, TargetID: draft.AssetPrompt.AssetID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.UpdateClipPrompts:
			if draft.ClipPrompts == nil {
				return CommandResult{}, ErrInvalidRequest
			}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetClip, TargetID: draft.ClipPrompts.ClipID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.CreateEpisodeAsset:
			if draft.AssetCreation == nil {
				return CommandResult{}, ErrInvalidRequest
			}
			creation := *draft.AssetCreation
			creation.AssetID, err = s.ids.NewAssetID()
			if err != nil {
				return CommandResult{}, err
			}
			if creation.SourceAssetID == "" {
				creation.IdentityID, err = s.ids.NewAssetIdentityID()
				if err != nil {
					return CommandResult{}, err
				}
			} else {
				creation.InheritedMediaVersionID, err = s.ids.NewMediaVersionID()
				if err != nil {
					return CommandResult{}, err
				}
			}
			operation.AssetCreation = &creation
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.RestoreEpisodeAsset:
			if draft.AssetRestoration == nil {
				return CommandResult{}, ErrInvalidRequest
			}
			copy := *draft.AssetRestoration
			operation.AssetRestoration = &copy
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetAsset, TargetID: copy.AssetID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.CreateScene:
			if draft.SceneCreation == nil || draft.ExpectedScriptRevision < 1 {
				return CommandResult{}, ErrInvalidRequest
			}
			generator, ok := s.ids.(StoryboardIDGenerator)
			if !ok {
				return CommandResult{}, errors.New("ChangeSet ID generator does not support Storyboard IDs")
			}
			if _, exists := sceneRefs[draft.SceneCreation.Ref]; exists {
				return CommandResult{}, ErrInvalidRequest
			}
			sceneID, err := generator.NewSceneID()
			if err != nil {
				return CommandResult{}, err
			}
			scene := project.Scene{ID: sceneID, ProjectID: current.ProjectID, EpisodeID: current.EpisodeID, Position: draft.SceneCreation.Position,
				Title: draft.SceneCreation.Title, Description: draft.SceneCreation.Description, Source: draft.SceneCreation.Source,
				Revision: 1, CreatedAt: at, UpdatedAt: at}
			operation.SceneCreation = &changeset.SceneCreation{Ref: draft.SceneCreation.Ref, Scene: scene, ExpectedScriptRevision: draft.ExpectedScriptRevision}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetStoryboard, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
			sceneRefs[draft.SceneCreation.Ref] = sceneID
		case changeset.CreateClip:
			if draft.ClipCreation == nil || draft.ExpectedScriptRevision < 1 {
				return CommandResult{}, ErrInvalidRequest
			}
			generator, ok := s.ids.(StoryboardIDGenerator)
			if !ok {
				return CommandResult{}, errors.New("ChangeSet ID generator does not support Storyboard IDs")
			}
			if _, exists := clipRefs[draft.ClipCreation.Ref]; exists {
				return CommandResult{}, ErrInvalidRequest
			}
			sceneID, exists := sceneRefs[draft.ClipCreation.SceneRef]
			if draft.ClipCreation.ExistingSceneID != "" {
				if draft.ClipCreation.SceneRef != "" || draft.ClipCreation.ExistingSceneID.Validate() != nil {
					return CommandResult{}, ErrInvalidRequest
				}
				sceneID, exists = draft.ClipCreation.ExistingSceneID, true
			}
			if !exists {
				return CommandResult{}, ErrInvalidRequest
			}
			clipID, err := generator.NewClipID()
			if err != nil {
				return CommandResult{}, err
			}
			value := draft.ClipCreation
			clip := project.Clip{ID: clipID, ProjectID: current.ProjectID, EpisodeID: current.EpisodeID, SceneID: sceneID, Position: value.Position,
				Title: value.Title, Description: value.Description, Dialogue: value.Dialogue, CreativeIntent: value.CreativeIntent,
				PlannedDurationSeconds: value.PlannedDurationSeconds, FirstImagePrompt: value.FirstImagePrompt, LastImagePrompt: value.LastImagePrompt,
				GenerationMode: value.GenerationMode, Revision: 1, CreatedAt: at, UpdatedAt: at}
			if !clip.VideoPrompts.SetForMode(value.GenerationMode, value.VideoPrompt) {
				return CommandResult{}, ErrInvalidRequest
			}
			operation.ClipCreation = &changeset.ClipCreation{Ref: value.Ref, SceneRef: value.SceneRef, ExistingSceneID: value.ExistingSceneID, Clip: clip, ExpectedScriptRevision: draft.ExpectedScriptRevision}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetStoryboard, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
			clipRefs[value.Ref] = clipID
		case changeset.AddClipAssetRef:
			if draft.ClipAssetRef == nil || draft.ExpectedScriptRevision < 1 {
				return CommandResult{}, ErrInvalidRequest
			}
			clipID, exists := clipRefs[draft.ClipAssetRef.ClipRef]
			if !exists {
				return CommandResult{}, ErrInvalidRequest
			}
			operation.ClipAssetRef = &changeset.ClipAssetReferenceCreation{ClipRef: draft.ClipAssetRef.ClipRef, ClipID: clipID,
				AssetID: draft.ClipAssetRef.AssetID, Position: draft.ClipAssetRef.Position, ExpectedScriptRevision: draft.ExpectedScriptRevision}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetStoryboard, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.MergeClips:
			if draft.ClipMerge == nil || len(draft.ClipMerge.Sources) < 2 {
				return CommandResult{}, ErrInvalidRequest
			}
			generator, ok := s.ids.(StoryboardIDGenerator)
			if !ok {
				return CommandResult{}, errors.New("ChangeSet ID generator does not support Storyboard IDs")
			}
			merge := draft.ClipMerge
			clipID, err := generator.NewClipID()
			if err != nil {
				return CommandResult{}, err
			}
			var duration int64
			for _, source := range merge.Sources {
				duration += source.PlannedDurationSeconds
			}
			first := merge.Sources[0]
			clip := project.Clip{ID: clipID, ProjectID: current.ProjectID, EpisodeID: current.EpisodeID, SceneID: first.SceneID, Position: first.Position,
				Title: merge.Title, Description: merge.Description, Dialogue: merge.Dialogue, CreativeIntent: merge.CreativeIntent,
				PlannedDurationSeconds: duration, FirstImagePrompt: merge.FirstImagePrompt, LastImagePrompt: merge.LastImagePrompt,
				GenerationMode: merge.GenerationMode, ContinueFromPrevious: first.ContinueFromPrevious, Revision: 1, CreatedAt: at, UpdatedAt: at}
			if !clip.VideoPrompts.SetForMode(merge.GenerationMode, merge.VideoPrompt) {
				return CommandResult{}, ErrInvalidRequest
			}
			firstFrame, err := s.freezeClipMergeFrame(merge.FirstFrame, project.MediaSlotClipFirstImage)
			if err != nil {
				return CommandResult{}, err
			}
			lastFrame, err := s.freezeClipMergeFrame(merge.LastFrame, project.MediaSlotClipLastImage)
			if err != nil {
				return CommandResult{}, err
			}
			operation.ClipMerge = &changeset.ClipMerge{Sources: append([]project.Clip(nil), merge.Sources...), Replacement: clip,
				AssetIDs: append([]project.AssetID(nil), merge.AssetIDs...), FirstFrame: firstFrame, LastFrame: lastFrame}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetStoryboard, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.SplitClip:
			if draft.ClipSplit == nil || len(draft.ClipSplit.Replacements) < 2 || len(draft.ClipSplit.Replacements) > 10 {
				return CommandResult{}, ErrInvalidRequest
			}
			generator, ok := s.ids.(StoryboardIDGenerator)
			if !ok {
				return CommandResult{}, errors.New("ChangeSet ID generator does not support Storyboard IDs")
			}
			split := draft.ClipSplit
			replacements := make([]changeset.ClipSplitReplacement, 0, len(split.Replacements))
			for index, candidate := range split.Replacements {
				clipID, err := generator.NewClipID()
				if err != nil {
					return CommandResult{}, err
				}
				clip := project.Clip{ID: clipID, ProjectID: current.ProjectID, EpisodeID: current.EpisodeID, SceneID: split.Source.SceneID, Position: split.Source.Position + index,
					Title: candidate.Title, Description: candidate.Description, Dialogue: candidate.Dialogue, CreativeIntent: candidate.CreativeIntent,
					PlannedDurationSeconds: candidate.PlannedDurationSeconds, FirstImagePrompt: candidate.FirstImagePrompt, LastImagePrompt: candidate.LastImagePrompt,
					GenerationMode: candidate.GenerationMode, ContinueFromPrevious: index == 0 && split.Source.ContinueFromPrevious, Revision: 1, CreatedAt: at, UpdatedAt: at}
				if !clip.VideoPrompts.SetForMode(candidate.GenerationMode, candidate.VideoPrompt) {
					return CommandResult{}, ErrInvalidRequest
				}
				replacements = append(replacements, changeset.ClipSplitReplacement{Clip: clip, AssetIDs: append([]project.AssetID(nil), candidate.AssetIDs...)})
			}
			firstFrame, err := s.freezeClipMergeFrame(split.FirstFrame, project.MediaSlotClipFirstImage)
			if err != nil {
				return CommandResult{}, err
			}
			lastFrame, err := s.freezeClipMergeFrame(split.LastFrame, project.MediaSlotClipLastImage)
			if err != nil {
				return CommandResult{}, err
			}
			operation.ClipSplit = &changeset.ClipSplit{Source: split.Source, Replacements: replacements, FirstFrame: firstFrame, LastFrame: lastFrame}
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetStoryboard, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.ReplaceEpisodeScript:
			if draft.EpisodeScript == nil || draft.EpisodeScript.EpisodeID != current.EpisodeID {
				return CommandResult{}, ErrInvalidRequest
			}
			copy := *draft.EpisodeScript
			operation.EpisodeScript = &copy
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: current.EpisodeID.String(), ExpectedRevision: draft.ExpectedRevision}
		case changeset.RevertChangeSet:
			if draft.ChangeSetRevert == nil {
				return CommandResult{}, ErrInvalidRequest
			}
			original, readErr := store.ChangeSet(ctx, draft.ChangeSetRevert.OriginalChangeSetID)
			if readErr != nil || original.ProjectID != input.ProjectID || original.Status != changeset.StatusApplied || original.RevertedByChangeSetID != "" {
				return CommandResult{}, ErrChangeSetStateConflict
			}
			copy := *draft.ChangeSetRevert
			operation.ChangeSetRevert = &copy
			operation.Precondition = changeset.Precondition{ID: preconditionID, OperationID: operationID, TargetKind: changeset.TargetChangeSet, TargetID: original.ID.String(), ExpectedRevision: original.Revision}
		default:
			return CommandResult{}, ErrInvalidRequest
		}
		if operation.Validate() != nil {
			return CommandResult{}, ErrInvalidRequest
		}
		operations = append(operations, operation)
	}
	hash, err := fingerprint(struct {
		ProjectID, ChangeSetID string
		ExpectedRevision       int64
		Operations             []OperationDraft
	}{input.ProjectID.String(), input.ChangeSetID.String(), input.ExpectedRevision, input.Operations})
	if err != nil {
		return CommandResult{}, err
	}
	value, replayed, err := store.AppendChangeSetOperations(ctx, input.ChangeSetID, input.ExpectedRevision, operations, at, receipt(input.ProjectID, input.ChangeSetID, input.OperationKey, hash, changeset.CommandAppend, at))
	return CommandResult{ChangeSet: value, Replayed: replayed}, err
}

func (s *Service) freezeClipMergeFrame(draft ClipMergeFrameDraft, kind project.MediaSlotKind) (changeset.ClipMergeFrame, error) {
	frame := changeset.ClipMergeFrame{Kind: kind, SourceSlotID: draft.Slot.ID, ExpectedSourceSlotRevision: draft.Slot.Revision}
	if draft.Current == nil {
		return frame, nil
	}
	version := *draft.Current
	frame.SourceVersion = &version
	id, err := s.ids.NewMediaVersionID()
	if err != nil {
		return changeset.ClipMergeFrame{}, err
	}
	frame.TargetVersionID = id
	return frame, nil
}

func storyboardReferences(ctx context.Context, store Store, owner changeset.ChangeSet) (map[string]project.SceneID, map[string]project.ClipID, error) {
	scenes := make(map[string]project.SceneID)
	clips := make(map[string]project.ClipID)
	for after := 0; ; {
		values, err := store.ChangeSetOperations(ctx, owner.ID, after, 100)
		if err != nil {
			return nil, nil, err
		}
		for _, operation := range values {
			if operation.SceneCreation != nil {
				scenes[operation.SceneCreation.Ref] = operation.SceneCreation.Scene.ID
			}
			if operation.ClipCreation != nil {
				clips[operation.ClipCreation.Ref] = operation.ClipCreation.Clip.ID
			}
			after = operation.Position
		}
		if len(values) < 100 {
			break
		}
	}
	return scenes, clips, nil
}

func (s *Service) Finalize(ctx context.Context, input FinalizeInput) (CommandResult, error) {
	return s.simpleCommand(ctx, input.ProjectID, input.ChangeSetID, input.ExpectedRevision, input.OperationKey, changeset.CommandFinalize, "", func(store Store, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
		return store.FinalizeChangeSet(ctx, input.ChangeSetID, input.ExpectedRevision, at, receipt)
	})
}
func (s *Service) Apply(ctx context.Context, input ApplyInput) (CommandResult, error) {
	if ctx == nil || input.ProjectID.Validate() != nil || input.ChangeSetID.Validate() != nil || input.ExpectedRevision < 1 {
		return CommandResult{}, ErrInvalidRequest
	}
	conflictID, err := s.ids.NewConflictID()
	if err != nil {
		return CommandResult{}, err
	}
	return s.simpleCommand(ctx, input.ProjectID, input.ChangeSetID, input.ExpectedRevision, input.OperationKey, changeset.CommandApply, "", func(store Store, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
		return store.ApplyChangeSet(ctx, input.ChangeSetID, input.ExpectedRevision, conflictID, at, receipt)
	})
}
func (s *Service) Reject(ctx context.Context, input RejectInput) (CommandResult, error) {
	return s.simpleCommand(ctx, input.ProjectID, input.ChangeSetID, input.ExpectedRevision, input.OperationKey, changeset.CommandReject, input.Reason, func(store Store, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
		return store.RejectChangeSet(ctx, input.ChangeSetID, input.ExpectedRevision, input.Reason, at, receipt)
	})
}

// Undo creates and immediately applies a daemon-authored inverse ChangeSet.
// The Runtime never supplies inverse operations; the project Store derives
// them deterministically from the immutable original operations.
func (s *Service) Undo(ctx context.Context, input UndoInput) (CommandResult, error) {
	if ctx == nil || input.ProjectID.Validate() != nil || input.ChangeSetID.Validate() != nil || input.OperationKey == "" {
		return CommandResult{}, ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, input.ProjectID)
	if err != nil {
		return CommandResult{}, err
	}
	original, err := store.ChangeSet(ctx, input.ChangeSetID)
	if err != nil {
		return CommandResult{}, err
	}
	if original.ProjectID != input.ProjectID || original.Status != changeset.StatusApplied || original.RevertedByChangeSetID != "" || original.RevertsChangeSetID != "" {
		return CommandResult{}, ErrChangeSetStateConflict
	}
	baseKey := input.OperationKey
	begin, err := s.BeginDraft(ctx, BeginDraftInput{ProjectID: original.ProjectID, EpisodeID: original.EpisodeID,
		AgentSessionID: original.Source.AgentSessionID, AgentTurnID: original.Source.AgentTurnID,
		CodexThreadID: original.Source.CodexThreadID, CodexTurnID: original.Source.CodexTurnID,
		Title: "Undo " + original.ID.String(), Summary: "Reverts " + original.ID.String(), OperationKey: derivedOperationKey(baseKey, "begin")})
	if err != nil {
		return CommandResult{}, err
	}
	inverseID := begin.ChangeSet.ID
	cleanup := func() { _ = s.DiscardDraft(context.WithoutCancel(ctx), original.ProjectID, inverseID) }
	appended, err := s.AppendOperations(ctx, AppendOperationsInput{ProjectID: original.ProjectID, ChangeSetID: inverseID,
		ExpectedRevision: begin.ChangeSet.Revision, OperationKey: derivedOperationKey(baseKey, "append"), Operations: []OperationDraft{{
			Type: changeset.RevertChangeSet, ExpectedRevision: original.Revision,
			ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: original.ID},
		}}})
	if err != nil {
		cleanup()
		return CommandResult{}, err
	}
	finalized, err := s.Finalize(ctx, FinalizeInput{ProjectID: original.ProjectID, ChangeSetID: inverseID,
		ExpectedRevision: appended.ChangeSet.Revision, OperationKey: derivedOperationKey(baseKey, "finalize")})
	if err != nil {
		cleanup()
		return CommandResult{}, err
	}
	return s.Apply(ctx, ApplyInput{ProjectID: original.ProjectID, ChangeSetID: inverseID,
		ExpectedRevision: finalized.ChangeSet.Revision, OperationKey: derivedOperationKey(baseKey, "apply")})
}

func derivedOperationKey(base, step string) string {
	value := base + ":" + step
	if len(value) <= 200 {
		return value
	}
	sum := sha256.Sum256([]byte(base))
	return "undo:" + hex.EncodeToString(sum[:]) + ":" + step
}

func (s *Service) DiscardDraft(ctx context.Context, projectID project.ProjectID, id changeset.ChangeSetID) error {
	if ctx == nil || projectID.Validate() != nil || id.Validate() != nil {
		return ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return err
	}
	current, err := store.ChangeSet(ctx, id)
	if err != nil {
		return err
	}
	if current.ProjectID != projectID {
		return ErrInvalidRequest
	}
	return store.DiscardChangeSetDraft(ctx, id)
}

func (s *Service) DiscardTurnDrafts(ctx context.Context, projectID project.ProjectID, turnID agent.TurnID) error {
	if ctx == nil || projectID.Validate() != nil || turnID.Validate() != nil {
		return ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return err
	}
	return store.DiscardChangeSetDraftsForTurn(ctx, turnID)
}

func (s *Service) simpleCommand(ctx context.Context, projectID project.ProjectID, id changeset.ChangeSetID, expected int64, operationKey string, command changeset.CommandType, reason string, action func(Store, time.Time, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)) (CommandResult, error) {
	if ctx == nil || projectID.Validate() != nil || id.Validate() != nil || expected < 1 {
		return CommandResult{}, ErrInvalidRequest
	}
	at, err := operationTime(s.clock.Now())
	if err != nil {
		return CommandResult{}, err
	}
	hash, err := fingerprint(struct {
		ProjectID, ChangeSetID string
		ExpectedRevision       int64
		Reason                 string
	}{projectID.String(), id.String(), expected, reason})
	if err != nil {
		return CommandResult{}, err
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return CommandResult{}, err
	}
	value, replayed, err := action(store, at, receipt(projectID, id, operationKey, hash, command, at))
	return CommandResult{ChangeSet: value, Replayed: replayed}, err
}

func (s *Service) Get(ctx context.Context, projectID project.ProjectID, id changeset.ChangeSetID) (changeset.ChangeSet, error) {
	if ctx == nil || projectID.Validate() != nil || id.Validate() != nil {
		return changeset.ChangeSet{}, ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return changeset.ChangeSet{}, err
	}
	value, err := store.ChangeSet(ctx, id)
	if err == nil && value.ProjectID != projectID {
		return changeset.ChangeSet{}, ErrInvalidRequest
	}
	return value, err
}
func (s *Service) Operations(ctx context.Context, projectID project.ProjectID, id changeset.ChangeSetID, afterPosition, limit int) ([]changeset.Operation, error) {
	if ctx == nil || projectID.Validate() != nil || id.Validate() != nil || afterPosition < 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return nil, err
	}
	owner, err := store.ChangeSet(ctx, id)
	if err != nil {
		return nil, err
	}
	if owner.ProjectID != projectID {
		return nil, ErrInvalidRequest
	}
	return store.ChangeSetOperations(ctx, id, afterPosition, limit)
}
func (s *Service) Conflicts(ctx context.Context, projectID project.ProjectID, id changeset.ChangeSetID) ([]changeset.Conflict, error) {
	if ctx == nil || projectID.Validate() != nil || id.Validate() != nil {
		return nil, ErrInvalidRequest
	}
	store, err := s.stores.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return nil, err
	}
	owner, err := store.ChangeSet(ctx, id)
	if err != nil {
		return nil, err
	}
	if owner.ProjectID != projectID {
		return nil, ErrInvalidRequest
	}
	return store.ChangeSetConflicts(ctx, id)
}

func receipt(projectID project.ProjectID, id changeset.ChangeSetID, key, hash string, command changeset.CommandType, at time.Time) changeset.CommandReceipt {
	return changeset.CommandReceipt{ProjectID: projectID, OperationKey: key, RequestHash: hash, Command: command, ChangeSetID: id, CreatedAt: at}
}
func fingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("fingerprint ChangeSet request: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func operationTime(value time.Time) (time.Time, error) {
	value = value.UTC().Truncate(time.Millisecond)
	if value.IsZero() || value.UnixMilli() < 0 {
		return time.Time{}, errors.New("invalid ChangeSet operation time")
	}
	return value, nil
}
