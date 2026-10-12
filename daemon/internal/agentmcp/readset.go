package agentmcp

import (
	"slices"
	"sync"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type clipPromptField string

const (
	clipFirstPrompt          clipPromptField = "first_image_prompt"
	clipLastPrompt           clipPromptField = "last_image_prompt"
	clipFirstVideoPrompt     clipPromptField = "first_image_video_prompt"
	clipFirstLastVideoPrompt clipPromptField = "first_last_image_video_prompt"
	clipReferenceVideoPrompt clipPromptField = "reference_video_prompt"
)

type assetObservation struct {
	revision int64
	prompt   string
}

type clipObservation struct {
	revision int64
	first    string
	last     string
	videos   project.ClipVideoPrompts
	fields   map[clipPromptField]bool
}

type pagedObservation struct {
	revision int64
	snapshot string
	next     int
	complete bool
}

type priorAssetObservation struct {
	asset           project.EpisodeAsset
	episodeSequence int
}

type deletedAssetObservation struct {
	asset project.EpisodeAsset
}

// TurnReadSet contains only values that were actually returned to the model.
// It is process memory and is destroyed with the active Turn.
type TurnReadSet struct {
	mutex           sync.RWMutex
	assets          map[project.AssetID]assetObservation
	clips           map[project.ClipID]clipObservation
	script          pagedObservation
	assetList       pagedObservation
	storyboard      pagedObservation
	storyboardEmpty bool
	sceneClips      map[project.SceneID]pagedObservation
	prior           map[project.AssetID]priorAssetObservation
	deletedAssets   map[project.AssetID]deletedAssetObservation
}

func (s *TurnReadSet) observeSceneClipsPage(sceneID project.SceneID, revision int64, snapshot string, start, end int, complete bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if start == 0 {
		s.sceneClips[sceneID] = pagedObservation{revision: revision, snapshot: snapshot}
	}
	observed := s.sceneClips[sceneID]
	if observed.revision != revision || observed.snapshot != snapshot || observed.next != start {
		delete(s.sceneClips, sceneID)
		return
	}
	observed.next = end
	observed.complete = complete
	s.sceneClips[sceneID] = observed
}

func (s *TurnReadSet) existingSceneClipsReady(sceneID project.SceneID, revision int64, snapshot string) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	observed, ok := s.sceneClips[sceneID]
	if !ok || !observed.complete {
		return ErrObjectNotObserved
	}
	if observed.revision != revision || observed.snapshot != snapshot {
		return ErrObservationStale
	}
	return nil
}

func (s *TurnReadSet) observeStoryboardPage(revision int64, snapshot string, start, end int, complete, empty bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if start == 0 {
		s.storyboard = pagedObservation{revision: revision, snapshot: snapshot}
		s.storyboardEmpty = empty
	}
	if s.storyboard.revision != revision || s.storyboard.snapshot != snapshot || s.storyboard.next != start || s.storyboardEmpty != empty {
		s.storyboard = pagedObservation{}
		s.storyboardEmpty = false
		return
	}
	s.storyboard.next = end
	s.storyboard.complete = complete
}

func (s *TurnReadSet) storyboardCreationReady(episode project.Episode, scriptState, storyboardState string, storyboard project.Storyboard, assets []project.Asset) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if !s.script.complete || s.script.revision != episode.Script.Revision || s.script.snapshot != scriptState {
		return ErrScriptNotObserved
	}
	if !s.storyboard.complete || s.storyboard.revision != storyboard.Revision || s.storyboard.snapshot != storyboardState {
		return ErrObjectNotObserved
	}
	for _, asset := range assets {
		observed, ok := s.assets[asset.ID]
		if !ok {
			return ErrObjectNotObserved
		}
		if observed.revision != asset.Revision || observed.prompt != asset.Prompt {
			return ErrObservationStale
		}
	}
	return nil
}

func newTurnReadSet() *TurnReadSet {
	return &TurnReadSet{
		assets:        make(map[project.AssetID]assetObservation),
		clips:         make(map[project.ClipID]clipObservation),
		sceneClips:    make(map[project.SceneID]pagedObservation),
		prior:         make(map[project.AssetID]priorAssetObservation),
		deletedAssets: make(map[project.AssetID]deletedAssetObservation),
	}
}

func (s *TurnReadSet) observeDeletedAsset(value project.EpisodeAsset) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.deletedAssets[value.Asset.ID] = deletedAssetObservation{asset: value}
}

func (s *TurnReadSet) assetRestorationDraft(current project.EpisodeAsset) (changesetapp.OperationDraft, error) {
	s.mutex.RLock()
	observed, ok := s.deletedAssets[current.Asset.ID]
	s.mutex.RUnlock()
	if !ok {
		return changesetapp.OperationDraft{}, ErrObjectNotObserved
	}
	previous := observed.asset
	if !current.Asset.IsDeleted() || !previous.Asset.IsDeleted() || previous.Asset.Revision != current.Asset.Revision ||
		previous.Asset.Prompt != current.Asset.Prompt || previous.Asset.EpisodeID != current.Asset.EpisodeID ||
		previous.Asset.IdentityID != current.Asset.IdentityID || previous.Identity.ID != current.Identity.ID ||
		previous.Identity.Revision != current.Identity.Revision || previous.Identity.Kind != current.Identity.Kind || previous.Identity.Name != current.Identity.Name ||
		!previous.Asset.DeletedAt.Equal(*current.Asset.DeletedAt) {
		return changesetapp.OperationDraft{}, ErrObservationStale
	}
	return changesetapp.OperationDraft{
		Type: changeset.RestoreEpisodeAsset, ExpectedRevision: current.Asset.Revision,
		AssetRestoration: &changeset.AssetRestoration{AssetID: current.Asset.ID},
	}, nil
}

func (s *TurnReadSet) observeScriptPage(revision int64, snapshot string, start, end int, complete bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if start == 0 {
		s.script = pagedObservation{revision: revision, snapshot: snapshot}
	}
	if s.script.revision != revision || s.script.snapshot != snapshot || s.script.next != start {
		s.script = pagedObservation{}
		return
	}
	s.script.next = end
	s.script.complete = complete
}

func (s *TurnReadSet) observeAssetListPage(snapshot string, start, end int, complete bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if start == 0 {
		s.assetList = pagedObservation{snapshot: snapshot}
	}
	if s.assetList.snapshot != snapshot || s.assetList.next != start {
		s.assetList = pagedObservation{}
		return
	}
	s.assetList.next = end
	s.assetList.complete = complete
}

func (s *TurnReadSet) observePriorAsset(value project.EpisodeAsset, episodeSequence int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.prior[value.Asset.ID] = priorAssetObservation{asset: value, episodeSequence: episodeSequence}
}

func (s *TurnReadSet) observeAsset(value project.Asset) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.assets[value.ID] = assetObservation{revision: value.Revision, prompt: value.Prompt}
}

func (s *TurnReadSet) observeClip(value project.Clip, fields map[clipPromptField]bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	previous, merge := s.clips[value.ID]
	if !merge || previous.revision != value.Revision {
		previous = clipObservation{revision: value.Revision, fields: make(map[clipPromptField]bool, len(fields))}
	}
	observation := clipObservation{
		revision: value.Revision,
		first:    previous.first,
		last:     previous.last,
		videos:   previous.videos,
		fields:   previous.fields,
	}
	for field := range fields {
		observation.fields[field] = true
		switch field {
		case clipFirstPrompt:
			observation.first = value.FirstImagePrompt
		case clipLastPrompt:
			observation.last = value.LastImagePrompt
		case clipFirstVideoPrompt:
			observation.videos.FirstImage = value.VideoPrompts.FirstImage
		case clipFirstLastVideoPrompt:
			observation.videos.FirstLastImage = value.VideoPrompts.FirstLastImage
		case clipReferenceVideoPrompt:
			observation.videos.Reference = value.VideoPrompts.Reference
		}
	}
	s.clips[value.ID] = observation
}

func (s *TurnReadSet) clipMergeReady(clips []project.Clip, assets []project.Asset) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	for _, clip := range clips {
		observed, ok := s.clips[clip.ID]
		if !ok {
			return ErrObjectNotObserved
		}
		if observed.revision != clip.Revision || observed.first != clip.FirstImagePrompt || observed.last != clip.LastImagePrompt || observed.videos != clip.VideoPrompts {
			return ErrObservationStale
		}
		for _, field := range []clipPromptField{clipFirstPrompt, clipLastPrompt, clipFirstVideoPrompt, clipFirstLastVideoPrompt, clipReferenceVideoPrompt} {
			if !observed.fields[field] {
				return ErrFieldNotObserved
			}
		}
	}
	for _, asset := range assets {
		observed, ok := s.assets[asset.ID]
		if !ok {
			return ErrObjectNotObserved
		}
		if observed.revision != asset.Revision || observed.prompt != asset.Prompt {
			return ErrObservationStale
		}
	}
	return nil
}

func (s *TurnReadSet) assetDraft(current project.Asset, after string) (changesetapp.OperationDraft, error) {
	s.mutex.RLock()
	observed, ok := s.assets[current.ID]
	s.mutex.RUnlock()
	if !ok {
		return changesetapp.OperationDraft{}, ErrObjectNotObserved
	}
	if observed.revision != current.Revision || observed.prompt != current.Prompt {
		return changesetapp.OperationDraft{}, ErrObservationStale
	}
	after = project.NormalizeAssetPrompt(after)
	return changesetapp.OperationDraft{
		Type:             changeset.UpdateAssetPrompt,
		ExpectedRevision: observed.revision,
		AssetPrompt: &changeset.AssetPromptChange{
			AssetID: current.ID,
			Before:  observed.prompt,
			After:   after,
		},
	}, nil
}

func (s *TurnReadSet) clipDraft(current project.Clip, assetIDsBefore, assetIDsAfter []project.AssetID, referenceIDsBefore, referenceIDsAfter []project.ArtifactID, first, last *string, videoMode project.ClipGenerationMode, video *string) (changesetapp.OperationDraft, error) {
	s.mutex.RLock()
	observed, ok := s.clips[current.ID]
	s.mutex.RUnlock()
	if !ok {
		return changesetapp.OperationDraft{}, ErrObjectNotObserved
	}
	if observed.revision != current.Revision {
		return changesetapp.OperationDraft{}, ErrObservationStale
	}
	videoField, validMode := clipVideoPromptField(videoMode)
	if !validMode {
		return changesetapp.OperationDraft{}, ErrInvalidArguments
	}
	for field, requested := range map[clipPromptField]*string{
		clipFirstPrompt: first,
		clipLastPrompt:  last,
		videoField:      video,
	} {
		if requested != nil && !observed.fields[field] {
			return changesetapp.OperationDraft{}, ErrFieldNotObserved
		}
	}
	if (observed.fields[clipFirstPrompt] && observed.first != current.FirstImagePrompt) ||
		(observed.fields[clipLastPrompt] && observed.last != current.LastImagePrompt) ||
		(observed.fields[videoField] && observed.videos.ForMode(videoMode) != current.VideoPrompts.ForMode(videoMode)) {
		return changesetapp.OperationDraft{}, ErrObservationStale
	}
	firstAfter, lastAfter, videoAfter := current.FirstImagePrompt, current.LastImagePrompt, current.VideoPrompts.ForMode(videoMode)
	if first != nil {
		firstAfter = *first
	}
	if last != nil {
		lastAfter = *last
	}
	if video != nil {
		videoAfter = *video
	}
	if firstAfter == current.FirstImagePrompt && lastAfter == current.LastImagePrompt && videoAfter == current.VideoPrompts.ForMode(videoMode) && slices.Equal(assetIDsBefore, assetIDsAfter) && slices.Equal(referenceIDsBefore, referenceIDsAfter) {
		return changesetapp.OperationDraft{}, ErrInvalidArguments
	}
	return changesetapp.OperationDraft{
		Type:             changeset.UpdateClipPrompts,
		ExpectedRevision: observed.revision,
		ClipPrompts: &changeset.ClipPromptsChange{
			ClipID:                     current.ID,
			FirstBefore:                current.FirstImagePrompt,
			FirstAfter:                 firstAfter,
			LastBefore:                 current.LastImagePrompt,
			LastAfter:                  lastAfter,
			VideoMode:                  videoMode,
			VideoBefore:                current.VideoPrompts.ForMode(videoMode),
			VideoAfter:                 videoAfter,
			AssetIDsBefore:             append([]project.AssetID(nil), assetIDsBefore...),
			AssetIDsAfter:              append([]project.AssetID(nil), assetIDsAfter...),
			AssetBindingsRecorded:      true,
			ReferenceArtifactIDsBefore: append([]project.ArtifactID(nil), referenceIDsBefore...),
			ReferenceArtifactIDsAfter:  append([]project.ArtifactID(nil), referenceIDsAfter...),
			ReferenceBindingsRecorded:  true,
		},
	}, nil
}

func clipVideoPromptField(mode project.ClipGenerationMode) (clipPromptField, bool) {
	switch mode {
	case project.ClipGenerationFirstImage:
		return clipFirstVideoPrompt, true
	case project.ClipGenerationFirstLastImage:
		return clipFirstLastVideoPrompt, true
	case project.ClipGenerationReference:
		return clipReferenceVideoPrompt, true
	default:
		return "", false
	}
}

func (s *TurnReadSet) assetIsCurrent(current project.Asset) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	observed, ok := s.assets[current.ID]
	return ok && observed.revision == current.Revision && observed.prompt == current.Prompt
}

func (s *TurnReadSet) scriptDraft(current project.Episode, markdown string) (changesetapp.OperationDraft, error) {
	markdown = project.NormalizeMarkdown(markdown)
	if project.ValidateScriptMarkdown(markdown) != nil || markdown == current.Script.Markdown {
		return changesetapp.OperationDraft{}, ErrInvalidArguments
	}
	s.mutex.RLock()
	observed := s.script
	s.mutex.RUnlock()
	if !observed.complete {
		return changesetapp.OperationDraft{}, ErrScriptNotObserved
	}
	if observed.revision != current.Script.Revision || observed.snapshot != scriptSnapshot(current.Script) {
		return changesetapp.OperationDraft{}, ErrObservationStale
	}
	return changesetapp.OperationDraft{
		Type:             changeset.ReplaceEpisodeScript,
		ExpectedRevision: current.Script.Revision,
		EpisodeScript: &changeset.EpisodeScriptChange{
			EpisodeID: current.ID,
			Before:    current.Script.Markdown,
			After:     markdown,
		},
	}, nil
}

func (s *TurnReadSet) assetCreationDraft(currentEpisode project.Episode, scriptSnapshot, listSnapshot string, currentAssets []project.EpisodeAsset, kind project.AssetKind, name, prompt string, source *project.EpisodeAsset, sourceSequence int) (changesetapp.OperationDraft, error) {
	s.mutex.RLock()
	script := s.script
	list := s.assetList
	var prior priorAssetObservation
	var observedSource bool
	if source != nil {
		prior, observedSource = s.prior[source.Asset.ID]
	}
	s.mutex.RUnlock()
	if !script.complete || script.revision != currentEpisode.Script.Revision || script.snapshot != scriptSnapshot {
		return changesetapp.OperationDraft{}, ErrScriptNotObserved
	}
	if !list.complete || list.snapshot != listSnapshot {
		return changesetapp.OperationDraft{}, ErrAssetListNotObserved
	}
	for _, existing := range currentAssets {
		if existing.Identity.Kind == kind && existing.Identity.Name == name {
			return changesetapp.OperationDraft{}, ErrAssetAlreadyPresent
		}
	}
	creation := &changeset.AssetCreation{Kind: kind, Name: name, Prompt: project.NormalizeAssetPrompt(prompt)}
	if source != nil {
		if !observedSource {
			return changesetapp.OperationDraft{}, ErrSourceAssetNotObserved
		}
		if prior.episodeSequence != sourceSequence || prior.asset.Asset.ID != source.Asset.ID ||
			prior.asset.Asset.Revision != source.Asset.Revision || prior.asset.Asset.Prompt != source.Asset.Prompt ||
			prior.asset.Identity.ID != source.Identity.ID || prior.asset.Identity.Revision != source.Identity.Revision ||
			prior.asset.Identity.Kind != source.Identity.Kind || prior.asset.Identity.Name != source.Identity.Name {
			return changesetapp.OperationDraft{}, ErrObservationStale
		}
		for _, existing := range currentAssets {
			if existing.Identity.ID == source.Identity.ID {
				return changesetapp.OperationDraft{}, ErrAssetAlreadyPresent
			}
		}
		creation.IdentityID = source.Identity.ID
		creation.SourceAssetID = source.Asset.ID
		creation.SourceAssetRevision = source.Asset.Revision
	}
	return changesetapp.OperationDraft{Type: changeset.CreateEpisodeAsset, ExpectedRevision: currentEpisode.Script.Revision, AssetCreation: creation}, nil
}
