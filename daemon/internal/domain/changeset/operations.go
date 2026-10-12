package changeset

import (
	"strings"
	"unicode"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type OperationType string

const (
	UpdateAssetPrompt    OperationType = "update_asset_prompt"
	UpdateClipPrompts    OperationType = "update_clip_prompts"
	CreateEpisodeAsset   OperationType = "create_episode_asset"
	RestoreEpisodeAsset  OperationType = "restore_episode_asset"
	CreateScene          OperationType = "create_scene"
	CreateClip           OperationType = "create_clip"
	AddClipAssetRef      OperationType = "add_clip_asset_reference"
	MergeClips           OperationType = "merge_clips"
	SplitClip            OperationType = "split_clip"
	ReplaceEpisodeScript OperationType = "replace_episode_script"
	RevertChangeSet      OperationType = "revert_change_set"
)

type TargetKind string

const (
	TargetAsset         TargetKind = "asset"
	TargetClip          TargetKind = "clip"
	TargetEpisodeScript TargetKind = "episode_script"
	TargetStoryboard    TargetKind = "storyboard"
	TargetChangeSet     TargetKind = "change_set"
)

type Precondition struct {
	ID               PreconditionID
	OperationID      OperationID
	TargetKind       TargetKind
	TargetID         string
	ExpectedRevision int64
}

func (p Precondition) Validate() error {
	if p.ID.Validate() != nil || p.OperationID.Validate() != nil || p.ExpectedRevision < 1 {
		return ErrInvalidPrecondition
	}
	switch p.TargetKind {
	case TargetAsset:
		if _, err := project.ParseAssetID(p.TargetID); err != nil {
			return ErrInvalidPrecondition
		}
	case TargetClip:
		if _, err := project.ParseClipID(p.TargetID); err != nil {
			return ErrInvalidPrecondition
		}
	case TargetEpisodeScript, TargetStoryboard:
		if _, err := project.ParseEpisodeID(p.TargetID); err != nil {
			return ErrInvalidPrecondition
		}
	case TargetChangeSet:
		if _, err := ParseChangeSetID(p.TargetID); err != nil {
			return ErrInvalidPrecondition
		}
	default:
		return ErrInvalidPrecondition
	}
	return nil
}

// Storyboard creation operations use Agent-local references to connect newly
// proposed objects. A Clip added to an existing Scene instead freezes that
// already-durable Scene ID and its resolved sparse order key. New object IDs
// are always allocated by the daemon.
type SceneCreation struct {
	Ref                    string
	Scene                  project.Scene
	ExpectedScriptRevision int64
}

func (c SceneCreation) Validate() error {
	return validateStoryboardCreation(c.Ref, c.ExpectedScriptRevision, c.Scene.Validate(), c.Scene.Revision, c.Scene.IsDeleted())
}

type ClipCreation struct {
	Ref                    string
	SceneRef               string
	ExistingSceneID        project.SceneID
	Clip                   project.Clip
	ExpectedScriptRevision int64
}

func (c ClipCreation) Validate() error {
	if c.ExistingSceneID != "" {
		if c.SceneRef != "" || c.ExistingSceneID.Validate() != nil || c.Clip.SceneID != c.ExistingSceneID {
			return ErrInvalidOperation
		}
	} else if !validTemporaryReference(c.SceneRef) {
		return ErrInvalidOperation
	}
	return validateStoryboardCreation(c.Ref, c.ExpectedScriptRevision, c.Clip.Validate(), c.Clip.Revision, c.Clip.IsDeleted())
}

type ClipAssetReferenceCreation struct {
	ClipRef                string
	ClipID                 project.ClipID
	AssetID                project.AssetID
	Position               int
	ExpectedScriptRevision int64
}

// ClipMergeFrame freezes the current boundary-frame selection observed when
// the merge is drafted. A nil SourceVersion deliberately means that the
// corresponding boundary was observed empty and must still be empty at apply.
type ClipMergeFrame struct {
	Kind                       project.MediaSlotKind
	SourceSlotID               project.MediaSlotID
	ExpectedSourceSlotRevision int64
	SourceVersion              *project.MediaVersion
	TargetVersionID            project.MediaVersionID
}

func (f ClipMergeFrame) Validate(sourceClipID, targetClipID project.ClipID) error {
	if f.Kind != project.MediaSlotClipFirstImage && f.Kind != project.MediaSlotClipLastImage || f.ExpectedSourceSlotRevision < 1 {
		return ErrInvalidOperation
	}
	sourceSlotID, err := project.FixedMediaSlotID(sourceClipID.String(), f.Kind)
	if err != nil || sourceSlotID != f.SourceSlotID {
		return ErrInvalidOperation
	}
	if f.SourceVersion == nil {
		if f.TargetVersionID != "" {
			return ErrInvalidOperation
		}
		return nil
	}
	if f.SourceVersion.Validate() != nil || f.SourceVersion.IsDeleted() || f.SourceVersion.SlotID != f.SourceSlotID || f.TargetVersionID.Validate() != nil || f.SourceVersion.ID == f.TargetVersionID {
		return ErrInvalidOperation
	}
	targetSlotID, err := project.FixedMediaSlotID(targetClipID.String(), f.Kind)
	if err != nil || targetSlotID == f.SourceSlotID {
		return ErrInvalidOperation
	}
	return nil
}

// ClipMerge is a lossless Storyboard structure replacement. Source Clips are
// soft-deleted, their media remains attached, and only the first source's
// current first frame plus the last source's current last frame are inherited.
type ClipMerge struct {
	Sources     []project.Clip
	Replacement project.Clip
	AssetIDs    []project.AssetID
	FirstFrame  ClipMergeFrame
	LastFrame   ClipMergeFrame
}

// ClipSplit atomically replaces one source Clip with two or more independently
// editable Clips. The source and all of its media remain available for Undo;
// only its outer boundary frames may be inherited by the first and last result.
// Each result has its own planned duration; splitting may revise the total.
type ClipSplitReplacement struct {
	Clip     project.Clip
	AssetIDs []project.AssetID
}

type ClipSplit struct {
	Source       project.Clip
	Replacements []ClipSplitReplacement
	FirstFrame   ClipMergeFrame
	LastFrame    ClipMergeFrame
}

func (s ClipSplit) Validate() error {
	if s.Source.Validate() != nil || s.Source.IsDeleted() || len(s.Replacements) < 2 || len(s.Replacements) > 10 {
		return ErrInvalidOperation
	}
	seenClips := make(map[project.ClipID]struct{}, len(s.Replacements))
	for index, replacement := range s.Replacements {
		clip := replacement.Clip
		if clip.Validate() != nil || project.ValidateClipPlanningDuration(clip.PlannedDurationSeconds, true) != nil || clip.IsDeleted() || clip.Revision != 1 || clip.ID == s.Source.ID ||
			clip.ProjectID != s.Source.ProjectID || clip.EpisodeID != s.Source.EpisodeID || clip.SceneID != s.Source.SceneID ||
			clip.Position != s.Source.Position+index || index == 0 && clip.ContinueFromPrevious != s.Source.ContinueFromPrevious ||
			index > 0 && clip.ContinueFromPrevious {
			return ErrInvalidOperation
		}
		if _, duplicate := seenClips[clip.ID]; duplicate {
			return ErrInvalidOperation
		}
		seenClips[clip.ID] = struct{}{}
		seenAssets := make(map[project.AssetID]struct{}, len(replacement.AssetIDs))
		for _, assetID := range replacement.AssetIDs {
			if assetID.Validate() != nil {
				return ErrInvalidOperation
			}
			if _, duplicate := seenAssets[assetID]; duplicate {
				return ErrInvalidOperation
			}
			seenAssets[assetID] = struct{}{}
		}
	}
	if s.FirstFrame.Kind != project.MediaSlotClipFirstImage || s.FirstFrame.Validate(s.Source.ID, s.Replacements[0].Clip.ID) != nil ||
		s.LastFrame.Kind != project.MediaSlotClipLastImage || s.LastFrame.Validate(s.Source.ID, s.Replacements[len(s.Replacements)-1].Clip.ID) != nil {
		return ErrInvalidOperation
	}
	return nil
}

func (m ClipMerge) Validate() error {
	if len(m.Sources) < 2 || m.Replacement.Validate() != nil || m.Replacement.IsDeleted() || m.Replacement.Revision != 1 {
		return ErrInvalidOperation
	}
	first := m.Sources[0]
	seenSources := make(map[project.ClipID]struct{}, len(m.Sources))
	var duration int64
	for _, source := range m.Sources {
		if source.Validate() != nil || source.IsDeleted() || source.ProjectID != first.ProjectID || source.EpisodeID != first.EpisodeID || source.SceneID != first.SceneID {
			return ErrInvalidOperation
		}
		if _, exists := seenSources[source.ID]; exists {
			return ErrInvalidOperation
		}
		seenSources[source.ID] = struct{}{}
		duration += source.PlannedDurationSeconds
	}
	if _, exists := seenSources[m.Replacement.ID]; exists || m.Replacement.ProjectID != first.ProjectID || m.Replacement.EpisodeID != first.EpisodeID || m.Replacement.SceneID != first.SceneID || m.Replacement.Position != first.Position || m.Replacement.PlannedDurationSeconds != duration || m.Replacement.ContinueFromPrevious != first.ContinueFromPrevious {
		return ErrInvalidOperation
	}
	seenAssets := make(map[project.AssetID]struct{}, len(m.AssetIDs))
	for _, assetID := range m.AssetIDs {
		if assetID.Validate() != nil {
			return ErrInvalidOperation
		}
		if _, exists := seenAssets[assetID]; exists {
			return ErrInvalidOperation
		}
		seenAssets[assetID] = struct{}{}
	}
	if m.FirstFrame.Kind != project.MediaSlotClipFirstImage || m.FirstFrame.Validate(first.ID, m.Replacement.ID) != nil || m.LastFrame.Kind != project.MediaSlotClipLastImage || m.LastFrame.Validate(m.Sources[len(m.Sources)-1].ID, m.Replacement.ID) != nil {
		return ErrInvalidOperation
	}
	return nil
}

func (c ClipAssetReferenceCreation) Validate() error {
	if !validTemporaryReference(c.ClipRef) || c.ClipID.Validate() != nil || c.AssetID.Validate() != nil || c.Position < 1 || c.ExpectedScriptRevision < 1 {
		return ErrInvalidOperation
	}
	return nil
}

func validateStoryboardCreation(ref string, scriptRevision int64, valueErr error, revision int64, deleted bool) error {
	if !validTemporaryReference(ref) || scriptRevision < 1 || valueErr != nil || revision != 1 || deleted {
		return ErrInvalidOperation
	}
	return nil
}

func validTemporaryReference(value string) bool {
	if value == "" || len(value) > 100 || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

type AssetPromptChange struct {
	AssetID project.AssetID
	Before  string
	After   string
}

type ClipPromptsChange struct {
	ClipID      project.ClipID
	FirstBefore string
	FirstAfter  string
	LastBefore  string
	LastAfter   string
	VideoMode   project.ClipGenerationMode
	VideoBefore string
	VideoAfter  string
	// Asset dependencies are part of the Clip generation definition. Keeping
	// them in this same immutable operation makes Prompt text and bindings
	// apply and undo atomically.
	AssetIDsBefore        []project.AssetID
	AssetIDsAfter         []project.AssetID
	AssetBindingsRecorded bool
	// Uploaded image, video, and audio references are also part of the Clip
	// generation definition. Store their immutable Artifact IDs so prompt text
	// and media bindings apply and undo together.
	ReferenceArtifactIDsBefore []project.ArtifactID
	ReferenceArtifactIDsAfter  []project.ArtifactID
	ReferenceBindingsRecorded  bool
}

type EpisodeScriptChange struct {
	EpisodeID project.EpisodeID
	Before    string
	After     string
}

type ChangeSetRevert struct {
	OriginalChangeSetID ChangeSetID
}

// AssetCreation freezes every identity and provenance decision before review.
// IDs are generated by the daemon, never supplied by the Agent.
type AssetCreation struct {
	AssetID                 project.AssetID
	IdentityID              project.AssetIdentityID
	SourceAssetID           project.AssetID
	SourceAssetRevision     int64
	InheritedMediaVersionID project.MediaVersionID
	Kind                    project.AssetKind
	Name                    string
	Prompt                  string
}

// AssetRestoration restores the exact soft-deleted Episode Asset. The Asset
// ID and revision precondition preserve its identity, Prompt, and media.
type AssetRestoration struct {
	AssetID project.AssetID
}

func (r AssetRestoration) Validate() error {
	if r.AssetID.Validate() != nil {
		return ErrInvalidOperation
	}
	return nil
}

func (c AssetCreation) Validate() error {
	if c.AssetID.Validate() != nil || c.IdentityID.Validate() != nil || !c.Kind.IsValid() ||
		project.ValidateAssetIdentityName(c.Name) != nil || project.ValidateAssetPrompt(c.Prompt) != nil {
		return ErrInvalidOperation
	}
	if c.SourceAssetID == "" {
		if c.SourceAssetRevision != 0 || c.InheritedMediaVersionID != "" {
			return ErrInvalidOperation
		}
		return nil
	}
	if c.SourceAssetID.Validate() != nil || c.SourceAssetRevision < 1 || c.SourceAssetID == c.AssetID || c.InheritedMediaVersionID.Validate() != nil {
		return ErrInvalidOperation
	}
	return nil
}

type Operation struct {
	ID               OperationID
	ChangeSetID      ChangeSetID
	Position         int
	Type             OperationType
	Precondition     Precondition
	AssetPrompt      *AssetPromptChange
	ClipPrompts      *ClipPromptsChange
	AssetCreation    *AssetCreation
	AssetRestoration *AssetRestoration
	SceneCreation    *SceneCreation
	ClipCreation     *ClipCreation
	ClipAssetRef     *ClipAssetReferenceCreation
	ClipMerge        *ClipMerge
	ClipSplit        *ClipSplit
	EpisodeScript    *EpisodeScriptChange
	ChangeSetRevert  *ChangeSetRevert
}

func (o Operation) Validate() error {
	if o.ID.Validate() != nil || o.ChangeSetID.Validate() != nil || o.Position < 1 || o.Precondition.Validate() != nil || o.Precondition.OperationID != o.ID {
		return ErrInvalidOperation
	}
	payloads := 0
	for _, present := range []bool{o.AssetPrompt != nil, o.ClipPrompts != nil, o.AssetCreation != nil, o.AssetRestoration != nil, o.SceneCreation != nil, o.ClipCreation != nil, o.ClipAssetRef != nil, o.ClipMerge != nil, o.ClipSplit != nil, o.EpisodeScript != nil, o.ChangeSetRevert != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return ErrInvalidOperation
	}
	switch o.Type {
	case UpdateAssetPrompt:
		if o.AssetPrompt == nil || o.hasCreation() || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.AssetPrompt.AssetID.Validate() != nil || o.Precondition.TargetKind != TargetAsset || o.Precondition.TargetID != o.AssetPrompt.AssetID.String() || project.ValidateAssetPrompt(o.AssetPrompt.Before) != nil || project.ValidateAssetPrompt(o.AssetPrompt.After) != nil || o.AssetPrompt.Before == o.AssetPrompt.After {
			return ErrInvalidOperation
		}
	case UpdateClipPrompts:
		if o.ClipPrompts == nil || o.hasCreation() || o.AssetPrompt != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.ClipPrompts.ClipID.Validate() != nil || o.Precondition.TargetKind != TargetClip || o.Precondition.TargetID != o.ClipPrompts.ClipID.String() || !validClipPrompts(*o.ClipPrompts) {
			return ErrInvalidOperation
		}
	case CreateEpisodeAsset:
		if o.AssetCreation == nil || o.AssetRestoration != nil || o.hasCreation() || o.AssetPrompt != nil || o.ClipPrompts != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.AssetCreation.Validate() != nil ||
			o.Precondition.TargetKind != TargetEpisodeScript {
			return ErrInvalidOperation
		}
	case RestoreEpisodeAsset:
		if o.AssetRestoration == nil || o.AssetRestoration.Validate() != nil || o.AssetCreation != nil || o.hasCreation() || o.AssetPrompt != nil || o.ClipPrompts != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil ||
			o.Precondition.TargetKind != TargetAsset || o.Precondition.TargetID != o.AssetRestoration.AssetID.String() {
			return ErrInvalidOperation
		}
	case CreateScene:
		if o.SceneCreation == nil || o.ClipCreation != nil || o.ClipAssetRef != nil || o.ClipMerge != nil || o.ClipSplit != nil || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.SceneCreation.Validate() != nil || !o.validStoryboardPrecondition(o.SceneCreation.Scene.EpisodeID) {
			return ErrInvalidOperation
		}
	case CreateClip:
		if o.ClipCreation == nil || o.SceneCreation != nil || o.ClipAssetRef != nil || o.ClipMerge != nil || o.ClipSplit != nil || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.ClipCreation.Validate() != nil || !o.validStoryboardPrecondition(o.ClipCreation.Clip.EpisodeID) {
			return ErrInvalidOperation
		}
	case AddClipAssetRef:
		if o.ClipAssetRef == nil || o.SceneCreation != nil || o.ClipCreation != nil || o.ClipMerge != nil || o.ClipSplit != nil || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.ClipAssetRef.Validate() != nil || o.Precondition.TargetKind != TargetStoryboard {
			return ErrInvalidOperation
		}
	case MergeClips:
		if o.ClipMerge == nil || o.ClipSplit != nil || o.SceneCreation != nil || o.ClipCreation != nil || o.ClipAssetRef != nil || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.ClipMerge.Validate() != nil || !o.validStoryboardPrecondition(o.ClipMerge.Replacement.EpisodeID) {
			return ErrInvalidOperation
		}
	case SplitClip:
		if o.ClipSplit == nil || o.ClipMerge != nil || o.SceneCreation != nil || o.ClipCreation != nil || o.ClipAssetRef != nil || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil || o.EpisodeScript != nil || o.ChangeSetRevert != nil || o.ClipSplit.Validate() != nil || !o.validStoryboardPrecondition(o.ClipSplit.Source.EpisodeID) {
			return ErrInvalidOperation
		}
	case ReplaceEpisodeScript:
		if o.EpisodeScript == nil || o.ChangeSetRevert != nil || o.hasCreation() || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil ||
			o.EpisodeScript.EpisodeID.Validate() != nil || o.Precondition.TargetKind != TargetEpisodeScript ||
			o.Precondition.TargetID != o.EpisodeScript.EpisodeID.String() || project.ValidateScriptMarkdown(o.EpisodeScript.Before) != nil ||
			project.ValidateScriptMarkdown(o.EpisodeScript.After) != nil || o.EpisodeScript.Before == o.EpisodeScript.After {
			return ErrInvalidOperation
		}
	case RevertChangeSet:
		if o.ChangeSetRevert == nil || o.EpisodeScript != nil || o.hasCreation() || o.AssetPrompt != nil || o.ClipPrompts != nil || o.AssetCreation != nil ||
			o.ChangeSetRevert.OriginalChangeSetID.Validate() != nil || o.ChangeSetRevert.OriginalChangeSetID == o.ChangeSetID ||
			o.Precondition.TargetKind != TargetChangeSet || o.Precondition.TargetID != o.ChangeSetRevert.OriginalChangeSetID.String() {
			return ErrInvalidOperation
		}
	default:
		return ErrInvalidOperation
	}
	return nil
}

func (o Operation) hasCreation() bool {
	return o.SceneCreation != nil || o.ClipCreation != nil || o.ClipAssetRef != nil || o.ClipMerge != nil || o.ClipSplit != nil
}

func (o Operation) validStoryboardPrecondition(episodeID project.EpisodeID) bool {
	return o.Precondition.TargetKind == TargetStoryboard && o.Precondition.TargetID == episodeID.String()
}

func validClipPrompts(value ClipPromptsChange) bool {
	if !value.VideoMode.IsValid() {
		return false
	}
	for _, prompt := range []string{value.FirstBefore, value.FirstAfter, value.LastBefore, value.LastAfter, value.VideoBefore, value.VideoAfter} {
		if project.ValidateClipPrompt(prompt) != nil {
			return false
		}
	}
	for _, ids := range [][]project.AssetID{value.AssetIDsBefore, value.AssetIDsAfter} {
		seen := make(map[project.AssetID]struct{}, len(ids))
		for _, id := range ids {
			if id.Validate() != nil {
				return false
			}
			if _, duplicate := seen[id]; duplicate {
				return false
			}
			seen[id] = struct{}{}
		}
	}
	if !value.AssetBindingsRecorded && (len(value.AssetIDsBefore) != 0 || len(value.AssetIDsAfter) != 0) {
		return false
	}
	for _, ids := range [][]project.ArtifactID{value.ReferenceArtifactIDsBefore, value.ReferenceArtifactIDsAfter} {
		seen := make(map[project.ArtifactID]struct{}, len(ids))
		for _, id := range ids {
			if id.Validate() != nil {
				return false
			}
			if _, duplicate := seen[id]; duplicate {
				return false
			}
			seen[id] = struct{}{}
		}
	}
	if !value.ReferenceBindingsRecorded && (len(value.ReferenceArtifactIDsBefore) != 0 || len(value.ReferenceArtifactIDsAfter) != 0) {
		return false
	}
	// This operation edits one Clip Prompt definition. Asset dependencies may
	// change atomically with it, but are not a second, binding-only mutation
	// hidden under the Prompt operation type.
	return value.FirstBefore != value.FirstAfter || value.LastBefore != value.LastAfter || value.VideoBefore != value.VideoAfter
}

func (o Operation) TargetKey() string {
	if o.AssetCreation != nil {
		return string(o.Type) + ":" + string(o.AssetCreation.Kind) + ":" + o.AssetCreation.Name
	}
	if o.AssetRestoration != nil {
		return string(o.Type) + ":" + o.AssetRestoration.AssetID.String()
	}
	if o.SceneCreation != nil {
		return string(o.Type) + ":" + o.SceneCreation.Ref
	}
	if o.ClipCreation != nil {
		return string(o.Type) + ":" + o.ClipCreation.Ref
	}
	if o.ClipAssetRef != nil {
		return string(o.Type) + ":" + o.ClipAssetRef.ClipRef + ":" + o.ClipAssetRef.AssetID.String()
	}
	if o.EpisodeScript != nil {
		return string(o.Type) + ":" + o.EpisodeScript.EpisodeID.String()
	}
	if o.ChangeSetRevert != nil {
		return string(o.Type) + ":" + o.ChangeSetRevert.OriginalChangeSetID.String()
	}
	return string(o.Precondition.TargetKind) + ":" + o.Precondition.TargetID
}
