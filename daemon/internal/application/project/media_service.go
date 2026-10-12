package projectapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Service) GetMediaSlot(ctx context.Context, input GetMediaSlotInput) (domain.MediaSlotDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "get MediaSlot", input.ProjectID)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	return mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, input.IncludeDeleted)
}

func (s *Service) ImportMedia(ctx context.Context, input ImportMediaInput) (domain.MediaSlotDetails, error) {
	if s.mediaFiles == nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", ErrMediaImportUnavailable)
	}
	handle, err := s.assetProjectHandle(ctx, "import media", input.ProjectID)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	details, err := mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
	}
	if err := validateSlotRevision("import media", details.Slot, input.ExpectedSlotRevision); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	if err := ensureMediaOwnerActive(ctx, handle.Store, details.Slot); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
	}
	request := PrepareMediaRequest{ProjectRoot: handle.RootPath, SourcePath: input.SourcePath, Kind: details.Slot.Kind.MediaKind()}
	if details.Slot.Kind == domain.MediaSlotClipFirstImage || details.Slot.Kind == domain.MediaSlotClipLastImage {
		project, err := handle.Store.Project(ctx)
		if err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
		}
		request.RequiredWidth = project.Output.Width()
		request.RequiredHeight = project.Output.Height()
	}
	prepared, err := s.mediaFiles.Prepare(ctx, request)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
	}
	discard := true
	defer func() {
		if discard {
			_ = s.mediaFiles.Discard(handle.RootPath, prepared)
		}
	}()

	artifact, err := handle.Store.ArtifactBySHA256(ctx, input.ProjectID, prepared.SHA256)
	installed := InstalledMedia{}
	if errors.Is(err, ErrArtifactNotFound) {
		artifactID, idErr := s.ids.NewArtifactID()
		if idErr != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w: %w", ErrProjectWriteFailed, idErr)
		}
		installed, err = s.mediaFiles.Install(ctx, handle.RootPath, prepared, artifactID)
		if err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
		}
		discard = false
		now, timeErr := validOperationTime(s.clock.Now())
		if timeErr != nil {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
			return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", timeErr)
		}
		artifact = artifactFromPrepared(artifactID, input.ProjectID, installed.RelativePath, prepared, now)
	} else if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("import media: %w", err)
	}
	if discard {
		if err := s.mediaFiles.Discard(handle.RootPath, prepared); err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("import media: discard duplicate staging file: %w", err)
		}
		discard = false
	}
	result, committed, err := s.addArtifactVersion(ctx, handle, details, artifact, input.ExpectedSlotRevision, input.MakeCurrent)
	if err != nil {
		if installed.RelativePath != "" && !committed {
			if cleanupErr := s.mediaFiles.Remove(handle.RootPath, installed); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove uncommitted media: %w", cleanupErr))
			}
		}
		return domain.MediaSlotDetails{}, err
	}
	return result, nil
}

func (s *Service) ExtractVideoFrame(ctx context.Context, input ExtractVideoFrameInput) (domain.MediaSlotDetails, error) {
	if s.mediaFiles == nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrMediaImportUnavailable)
	}
	handle, target, err := s.mutableMediaSlot(ctx, "extract video frame", input.ProjectID, input.TargetMediaSlotID, input.ExpectedTargetRevision)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	if target.Slot.Kind != domain.MediaSlotClipFirstImage && target.Slot.Kind != domain.MediaSlotClipLastImage {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrMediaKindMismatch)
	}
	source, err := mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.SourceVideoMediaSlotID, false)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	if source.Slot.Kind != domain.MediaSlotClipVideo || source.Current == nil || source.Slot.EpisodeID != target.Slot.EpisodeID {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrMediaKindMismatch)
	}
	if err := ensureMediaOwnerActive(ctx, handle.Store, source.Slot); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	targetClipID, targetIDErr := domain.ParseClipID(target.Slot.OwnerID)
	sourceClipID, sourceIDErr := domain.ParseClipID(source.Slot.OwnerID)
	if targetIDErr != nil || sourceIDErr != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrProjectDataInvalid)
	}
	targetClip, err := handle.Store.Clip(ctx, targetClipID)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	sourceClip, err := handle.Store.Clip(ctx, sourceClipID)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	if sourceClip.Clip.SceneID != targetClip.Clip.SceneID {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrInvalidRequest)
	}
	scene, err := handle.Store.SceneDetails(ctx, targetClip.Clip.SceneID)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	targetIndex, sourceIndex := -1, -1
	for index, candidate := range scene.Clips {
		switch candidate.Clip.ID {
		case targetClipID:
			targetIndex = index
		case sourceClipID:
			sourceIndex = index
		}
	}
	expectedSourceIndex := targetIndex + 1
	if target.Slot.Kind == domain.MediaSlotClipFirstImage {
		expectedSourceIndex = targetIndex - 1
	}
	if targetIndex < 0 || sourceIndex != expectedSourceIndex {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrInvalidRequest)
	}
	artifact, ok := source.Artifacts[source.Current.ArtifactID]
	if !ok || artifact.Kind != domain.MediaKindVideo {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", ErrProjectDataInvalid)
	}
	position := source.Current.TrimInMilliseconds
	filename := "next-video-first-frame.png"
	if target.Slot.Kind == domain.MediaSlotClipFirstImage {
		trimEnd := source.Current.TrimOutMilliseconds
		if trimEnd == 0 {
			trimEnd = artifact.DurationMilliseconds
		}
		frameDuration := max(int64(1), (1000*artifact.FPSDenominator+artifact.FPSNumerator-1)/artifact.FPSNumerator)
		position = max(source.Current.TrimInMilliseconds, trimEnd-frameDuration)
		filename = "previous-video-last-frame.png"
	}
	project, err := handle.Store.Project(ctx)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	prepared, err := s.mediaFiles.PrepareVideoFrame(ctx, PrepareVideoFrameRequest{
		ProjectRoot:        handle.RootPath,
		SourceRelativePath: artifact.RelativePath,
		AtMilliseconds:     position,
		OriginalFilename:   filename,
		RequiredWidth:      project.Output.Width(),
		RequiredHeight:     project.Output.Height(),
	})
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	discard := true
	defer func() {
		if discard {
			_ = s.mediaFiles.Discard(handle.RootPath, prepared)
		}
	}()

	frameArtifact, err := handle.Store.ArtifactBySHA256(ctx, input.ProjectID, prepared.SHA256)
	installed := InstalledMedia{}
	if errors.Is(err, ErrArtifactNotFound) {
		artifactID, idErr := s.ids.NewArtifactID()
		if idErr != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w: %w", ErrProjectWriteFailed, idErr)
		}
		installed, err = s.mediaFiles.Install(ctx, handle.RootPath, prepared, artifactID)
		if err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
		}
		discard = false
		now, timeErr := validOperationTime(s.clock.Now())
		if timeErr != nil {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", timeErr)
		}
		frameArtifact = artifactFromPrepared(artifactID, input.ProjectID, installed.RelativePath, prepared, now)
	} else if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
	}
	if discard {
		if err := s.mediaFiles.Discard(handle.RootPath, prepared); err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: discard duplicate staging file: %w", err)
		}
		discard = false
	}
	// Continuity frames are refreshed immediately before each video generation.
	// Avoid creating duplicate versions when the adjacent video still resolves to
	// the same frame, and re-select a historical version when the source returns
	// to content that was used before.
	if target.Current != nil && target.Current.ArtifactID == frameArtifact.ID {
		return target, nil
	}
	for _, version := range target.Versions {
		if version.ArtifactID != frameArtifact.ID || version.IsDeleted() {
			continue
		}
		now, err := validOperationTime(s.clock.Now())
		if err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
		}
		if err := handle.Store.SelectMediaVersion(ctx, target.Slot.ID, version.ID, input.ExpectedTargetRevision, now); err != nil {
			return domain.MediaSlotDetails{}, fmt.Errorf("extract video frame: %w", err)
		}
		if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
			return domain.MediaSlotDetails{}, err
		}
		return mediaSlotForProject(ctx, handle.Store, input.ProjectID, target.Slot.ID, true)
	}
	result, committed, err := s.addArtifactVersion(ctx, handle, target, frameArtifact, input.ExpectedTargetRevision, true)
	if err != nil {
		if installed.RelativePath != "" && !committed {
			if cleanupErr := s.mediaFiles.Remove(handle.RootPath, installed); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove uncommitted frame: %w", cleanupErr))
			}
		}
		return domain.MediaSlotDetails{}, err
	}
	return result, nil
}

func (s *Service) AssignArtifact(ctx context.Context, input AssignArtifactInput) (domain.MediaSlotDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "assign Artifact", input.ProjectID)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	details, err := mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("assign Artifact: %w", err)
	}
	if err := validateSlotRevision("assign Artifact", details.Slot, input.ExpectedSlotRevision); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	if err := ensureMediaOwnerActive(ctx, handle.Store, details.Slot); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("assign Artifact: %w", err)
	}
	artifact, err := handle.Store.Artifact(ctx, input.ArtifactID)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("assign Artifact: %w", err)
	}
	if artifact.ProjectID != input.ProjectID || artifact.Kind != details.Slot.Kind.MediaKind() {
		return domain.MediaSlotDetails{}, fmt.Errorf("assign Artifact: %w", ErrMediaKindMismatch)
	}
	result, _, err := s.addArtifactVersion(ctx, handle, details, artifact, input.ExpectedSlotRevision, input.MakeCurrent)
	return result, err
}

func (s *Service) addArtifactVersion(ctx context.Context, handle ProjectHandle, details domain.MediaSlotDetails, artifact domain.Artifact, expectedRevision int64, makeCurrent bool) (domain.MediaSlotDetails, bool, error) {
	id, err := s.ids.NewMediaVersionID()
	if err != nil {
		return domain.MediaSlotDetails{}, false, fmt.Errorf("add MediaVersion: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.MediaSlotDetails{}, false, fmt.Errorf("add MediaVersion: %w", err)
	}
	version := domain.MediaVersion{ID: id, SlotID: details.Slot.ID, ArtifactID: artifact.ID, SequenceNumber: len(details.Versions) + 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if details.Slot.Kind == domain.MediaSlotClipVideo {
		version.TrimOutMilliseconds = artifact.DurationMilliseconds
	}
	if err := version.Validate(); err != nil {
		return domain.MediaSlotDetails{}, false, fmt.Errorf("add MediaVersion: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.AddMediaVersion(ctx, artifact, version, expectedRevision, makeCurrent, now); err != nil {
		return domain.MediaSlotDetails{}, false, fmt.Errorf("add MediaVersion: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.MediaSlotDetails{}, true, err
	}
	result, err := mediaSlotForProject(ctx, handle.Store, handle.ProjectID, details.Slot.ID, true)
	return result, true, err
}

func (s *Service) SelectMediaVersion(ctx context.Context, input SelectMediaVersionInput) (domain.MediaSlotDetails, error) {
	handle, details, err := s.mutableMediaSlot(ctx, "select MediaVersion", input.ProjectID, input.MediaSlotID, input.ExpectedSlotRevision)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	version, ok := mediaVersionIn(details, input.MediaVersionID)
	if !ok || version.IsDeleted() {
		return domain.MediaSlotDetails{}, fmt.Errorf("select MediaVersion: %w", ErrMediaVersionNotFound)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("select MediaVersion: %w", err)
	}
	if err := handle.Store.SelectMediaVersion(ctx, input.MediaSlotID, input.MediaVersionID, input.ExpectedSlotRevision, now); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("select MediaVersion: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	return mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
}

func (s *Service) UpdateVideoTrim(ctx context.Context, input UpdateVideoTrimInput) (domain.MediaSlotDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "update video trim", input.ProjectID)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	details, err := mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", err)
	}
	if details.Slot.Kind != domain.MediaSlotClipVideo {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", ErrMediaKindMismatch)
	}
	if err := ensureMediaOwnerActive(ctx, handle.Store, details.Slot); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", err)
	}
	version, ok := mediaVersionIn(details, input.MediaVersionID)
	if !ok || version.IsDeleted() {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", ErrMediaVersionNotFound)
	}
	artifact, ok := details.Artifacts[version.ArtifactID]
	if !ok {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", ErrProjectDataInvalid)
	}
	if input.TrimInMilliseconds < 0 || input.TrimOutMilliseconds <= input.TrimInMilliseconds || input.TrimOutMilliseconds > artifact.DurationMilliseconds {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", ErrInvalidRequest)
	}
	if input.ExpectedVersionRevision < 1 || version.Revision != input.ExpectedVersionRevision || version.Revision == math.MaxInt64 {
		return domain.MediaSlotDetails{}, revisionConflict("update video trim", input.ExpectedVersionRevision, version.Revision)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", err)
	}
	version.TrimInMilliseconds = input.TrimInMilliseconds
	version.TrimOutMilliseconds = input.TrimOutMilliseconds
	version.Revision++
	version.UpdatedAt = now
	if err := handle.Store.UpdateVideoTrim(ctx, version, input.ExpectedVersionRevision, now); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("update video trim: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	return mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
}

func (s *Service) DeleteMediaVersion(ctx context.Context, input ChangeMediaVersionDeletionInput) (domain.MediaSlotDetails, error) {
	return s.changeMediaVersionDeletion(ctx, "delete MediaVersion", input, false)
}

func (s *Service) RestoreMediaVersion(ctx context.Context, input ChangeMediaVersionDeletionInput) (domain.MediaSlotDetails, error) {
	return s.changeMediaVersionDeletion(ctx, "restore MediaVersion", input, true)
}

func (s *Service) changeMediaVersionDeletion(ctx context.Context, operation string, input ChangeMediaVersionDeletionInput, restore bool) (domain.MediaSlotDetails, error) {
	handle, details, err := s.mutableMediaSlot(ctx, operation, input.ProjectID, input.MediaSlotID, input.ExpectedMediaSlotRevision)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	version, ok := mediaVersionIn(details, input.MediaVersionID)
	if !ok {
		return domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, ErrMediaVersionNotFound)
	}
	if version.IsDeleted() != restore || input.ExpectedMediaVersionRevision < 1 || version.Revision != input.ExpectedMediaVersionRevision || version.Revision == math.MaxInt64 {
		return domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, ErrRevisionConflict)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	version.Revision++
	version.UpdatedAt = now
	if restore {
		version.DeletedAt = nil
		err = handle.Store.RestoreMediaVersion(ctx, version, input.ExpectedMediaVersionRevision, input.ExpectedMediaSlotRevision, now)
	} else {
		version.DeletedAt = &now
		err = handle.Store.DeleteMediaVersion(ctx, version, input.ExpectedMediaVersionRevision, input.ExpectedMediaSlotRevision, now)
	}
	if err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	return mediaSlotForProject(ctx, handle.Store, input.ProjectID, input.MediaSlotID, true)
}

func artifactFromPrepared(id domain.ArtifactID, projectID domain.ProjectID, relativePath string, p PreparedMedia, at time.Time) domain.Artifact {
	return domain.Artifact{ID: id, ProjectID: projectID, Kind: p.Kind, RelativePath: relativePath, SHA256: p.SHA256, ByteSize: p.ByteSize, MIMEType: p.MIMEType, OriginalFilename: p.OriginalFilename, Width: p.Width, Height: p.Height, DurationMilliseconds: p.DurationMilliseconds, FPSNumerator: p.FPSNumerator, FPSDenominator: p.FPSDenominator, AudioSampleRate: p.AudioSampleRate, AudioChannels: p.AudioChannels, CreatedAt: at}
}

func mediaSlotForProject(ctx context.Context, store ProjectStore, projectID domain.ProjectID, id domain.MediaSlotID, includeDeleted bool) (domain.MediaSlotDetails, error) {
	if err := id.Validate(); err != nil {
		return domain.MediaSlotDetails{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	details, err := store.MediaSlot(ctx, id, includeDeleted)
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	if err := details.Validate(); err != nil || details.Slot.ProjectID != projectID {
		return domain.MediaSlotDetails{}, ErrProjectDataInvalid
	}
	return details, nil
}

func (s *Service) mutableMediaSlot(ctx context.Context, operation string, projectID domain.ProjectID, id domain.MediaSlotID, expected int64) (ProjectHandle, domain.MediaSlotDetails, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.MediaSlotDetails{}, err
	}
	details, err := mediaSlotForProject(ctx, handle.Store, projectID, id, true)
	if err != nil {
		return ProjectHandle{}, domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	if err := validateSlotRevision(operation, details.Slot, expected); err != nil {
		return ProjectHandle{}, domain.MediaSlotDetails{}, err
	}
	if err := ensureMediaOwnerActive(ctx, handle.Store, details.Slot); err != nil {
		return ProjectHandle{}, domain.MediaSlotDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	return handle, details, nil
}

func validateSlotRevision(operation string, slot domain.MediaSlot, expected int64) error {
	if expected < 1 || slot.Revision != expected || slot.Revision == math.MaxInt64 {
		return revisionConflict(operation, expected, slot.Revision)
	}
	return nil
}

func mediaVersionIn(details domain.MediaSlotDetails, id domain.MediaVersionID) (domain.MediaVersion, bool) {
	for _, version := range details.Versions {
		if version.ID == id {
			return version, true
		}
	}
	return domain.MediaVersion{}, false
}

func ensureMediaOwnerActive(ctx context.Context, store ProjectStore, slot domain.MediaSlot) error {
	switch slot.OwnerKind {
	case domain.MediaOwnerAsset:
		id, err := domain.ParseAssetID(slot.OwnerID)
		if err != nil {
			return ErrProjectDataInvalid
		}
		value, err := store.Asset(ctx, id)
		if err != nil {
			return err
		}
		if value.Asset.IsDeleted() {
			return ErrAssetDeleted
		}
	case domain.MediaOwnerClip:
		id, err := domain.ParseClipID(slot.OwnerID)
		if err != nil {
			return ErrProjectDataInvalid
		}
		value, err := store.Clip(ctx, id)
		if err != nil {
			return err
		}
		if value.Clip.IsDeleted() {
			return ErrClipDeleted
		}
	case domain.MediaOwnerAudioReference:
		id, err := domain.ParseAudioReferenceAssetID(slot.OwnerID)
		if err != nil {
			return ErrProjectDataInvalid
		}
		value, err := store.AudioReferenceAsset(ctx, id)
		if err != nil {
			return err
		}
		if value.IsDeleted() {
			return ErrAudioReferenceDeleted
		}
	default:
		return ErrProjectDataInvalid
	}
	return nil
}
