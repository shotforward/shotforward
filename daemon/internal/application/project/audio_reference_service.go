package projectapp

import (
	"context"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Service) GetAudioReferenceAsset(ctx context.Context, input GetAudioReferenceAssetInput) (domain.AudioReferenceAsset, error) {
	handle, err := s.assetProjectHandle(ctx, "get AudioReferenceAsset", input.ProjectID)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	return audioReferenceForProject(ctx, handle.Store, input.ProjectID, input.AudioReferenceAssetID)
}

func (s *Service) ListAudioReferenceAssets(ctx context.Context, input ListAudioReferenceAssetsInput) ([]domain.AudioReferenceAsset, error) {
	handle, err := s.assetProjectHandle(ctx, "list AudioReferenceAssets", input.ProjectID)
	if err != nil {
		return nil, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return nil, fmt.Errorf("list AudioReferenceAssets: %w", err)
	}
	values, err := handle.Store.EpisodeAudioReferenceAssets(ctx, input.EpisodeID, input.IncludeDeleted)
	if err != nil {
		return nil, fmt.Errorf("list AudioReferenceAssets: %w", err)
	}
	for _, value := range values {
		if err := value.Validate(); err != nil || value.ProjectID != input.ProjectID || value.EpisodeID != input.EpisodeID {
			return nil, fmt.Errorf("list AudioReferenceAssets: %w", ErrProjectDataInvalid)
		}
	}
	return values, nil
}

func (s *Service) CreateAudioReferenceAsset(ctx context.Context, input CreateAudioReferenceAssetInput) (domain.AudioReferenceAsset, error) {
	handle, err := s.assetProjectHandle(ctx, "create AudioReferenceAsset", input.ProjectID)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w", err)
	}
	if input.CharacterAssetID != "" {
		asset, err := assetForProject(ctx, handle.Store, input.ProjectID, input.CharacterAssetID)
		if err != nil {
			return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w", err)
		}
		if asset.Asset.IsDeleted() || asset.Asset.EpisodeID != input.EpisodeID || asset.Identity.Kind != domain.AssetKindCharacter {
			return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w", ErrInvalidRequest)
		}
	}
	id, err := s.ids.NewAudioReferenceAssetID()
	if err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w", err)
	}
	value := domain.AudioReferenceAsset{ID: id, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Name: input.Name, CharacterAssetID: input.CharacterAssetID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := value.Validate(); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.CreateAudioReferenceAsset(ctx, value, now); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("create AudioReferenceAsset: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	return audioReferenceForProject(ctx, handle.Store, input.ProjectID, id)
}

func (s *Service) UpdateAudioReferenceAsset(ctx context.Context, input UpdateAudioReferenceAssetInput) (domain.AudioReferenceAsset, error) {
	handle, value, err := s.mutableAudioReference(ctx, "update AudioReferenceAsset", input.ProjectID, input.AudioReferenceAssetID, input.ExpectedRevision, false)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	if input.CharacterAssetID != "" {
		asset, err := assetForProject(ctx, handle.Store, input.ProjectID, input.CharacterAssetID)
		if err != nil || asset.Asset.IsDeleted() || asset.Asset.EpisodeID != value.EpisodeID || asset.Identity.Kind != domain.AssetKindCharacter {
			return domain.AudioReferenceAsset{}, fmt.Errorf("update AudioReferenceAsset: %w", ErrInvalidRequest)
		}
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("update AudioReferenceAsset: %w", err)
	}
	value.Name = input.Name
	value.CharacterAssetID = input.CharacterAssetID
	value.Revision++
	value.UpdatedAt = now
	if err := value.Validate(); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("update AudioReferenceAsset: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateAudioReferenceAsset(ctx, value, input.ExpectedRevision, now); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("update AudioReferenceAsset: %w", err)
	}
	return s.finishAudioReferenceMutation(ctx, handle, value.ID, now)
}

func (s *Service) DeleteAudioReferenceAsset(ctx context.Context, input ChangeAudioReferenceDeletionInput) (domain.AudioReferenceAsset, error) {
	return s.changeAudioReferenceDeletion(ctx, "delete AudioReferenceAsset", input, false)
}

func (s *Service) RestoreAudioReferenceAsset(ctx context.Context, input ChangeAudioReferenceDeletionInput) (domain.AudioReferenceAsset, error) {
	return s.changeAudioReferenceDeletion(ctx, "restore AudioReferenceAsset", input, true)
}

func (s *Service) changeAudioReferenceDeletion(ctx context.Context, operation string, input ChangeAudioReferenceDeletionInput, restore bool) (domain.AudioReferenceAsset, error) {
	handle, value, err := s.mutableAudioReference(ctx, operation, input.ProjectID, input.AudioReferenceAssetID, input.ExpectedRevision, restore)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("%s: %w", operation, err)
	}
	value.Revision++
	value.UpdatedAt = now
	if restore {
		value.DeletedAt = nil
		err = handle.Store.RestoreAudioReferenceAsset(ctx, value, input.ExpectedRevision, now)
	} else {
		value.DeletedAt = &now
		err = handle.Store.DeleteAudioReferenceAsset(ctx, value, input.ExpectedRevision, now)
	}
	if err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("%s: %w", operation, err)
	}
	return s.finishAudioReferenceMutation(ctx, handle, value.ID, now)
}

func (s *Service) GetClipAudioReference(ctx context.Context, input GetClipInput) (domain.AudioReferenceAssetID, error) {
	handle, err := s.assetProjectHandle(ctx, "get Clip audio reference", input.ProjectID)
	if err != nil {
		return "", err
	}
	if _, err := clipForProject(ctx, handle.Store, input.ProjectID, input.ClipID); err != nil {
		return "", fmt.Errorf("get Clip audio reference: %w", err)
	}
	return handle.Store.ClipAudioReference(ctx, input.ClipID)
}

func (s *Service) SetClipAudioReference(ctx context.Context, input SetClipAudioReferenceInput) (domain.ClipDetails, error) {
	handle, clip, err := s.mutableClip(ctx, "set Clip audio reference", input.ProjectID, input.ClipID, input.ExpectedClipRevision, false)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	audio, err := audioReferenceForProject(ctx, handle.Store, input.ProjectID, input.AudioReferenceAssetID)
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("set Clip audio reference: %w", err)
	}
	if audio.IsDeleted() {
		return domain.ClipDetails{}, fmt.Errorf("set Clip audio reference: %w", ErrAudioReferenceDeleted)
	}
	if audio.EpisodeID != clip.Clip.EpisodeID {
		return domain.ClipDetails{}, fmt.Errorf("set Clip audio reference: %w", ErrInvalidRequest)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("set Clip audio reference: %w", err)
	}
	if err := handle.Store.SetClipAudioReference(ctx, input.ClipID, input.AudioReferenceAssetID, input.ExpectedClipRevision, now); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("set Clip audio reference: %w", err)
	}
	return s.finishClipMutation(ctx, handle, input.ClipID, now)
}

func (s *Service) ClearClipAudioReference(ctx context.Context, input ClearClipAudioReferenceInput) (domain.ClipDetails, error) {
	handle, _, err := s.mutableClip(ctx, "clear Clip audio reference", input.ProjectID, input.ClipID, input.ExpectedClipRevision, false)
	if err != nil {
		return domain.ClipDetails{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("clear Clip audio reference: %w", err)
	}
	if err := handle.Store.ClearClipAudioReference(ctx, input.ClipID, input.ExpectedClipRevision, now); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("clear Clip audio reference: %w", err)
	}
	return s.finishClipMutation(ctx, handle, input.ClipID, now)
}

func audioReferenceForProject(ctx context.Context, store ProjectStore, projectID domain.ProjectID, id domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error) {
	if err := id.Validate(); err != nil {
		return domain.AudioReferenceAsset{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	value, err := store.AudioReferenceAsset(ctx, id)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	if err := value.Validate(); err != nil || value.ProjectID != projectID {
		return domain.AudioReferenceAsset{}, ErrProjectDataInvalid
	}
	return value, nil
}

func (s *Service) mutableAudioReference(ctx context.Context, operation string, projectID domain.ProjectID, id domain.AudioReferenceAssetID, expected int64, wantDeleted bool) (ProjectHandle, domain.AudioReferenceAsset, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.AudioReferenceAsset{}, err
	}
	value, err := audioReferenceForProject(ctx, handle.Store, projectID, id)
	if err != nil {
		return ProjectHandle{}, domain.AudioReferenceAsset{}, fmt.Errorf("%s: %w", operation, err)
	}
	if value.IsDeleted() != wantDeleted {
		if wantDeleted {
			return ProjectHandle{}, domain.AudioReferenceAsset{}, fmt.Errorf("%s: %w", operation, ErrAudioReferenceNotDeleted)
		}
		return ProjectHandle{}, domain.AudioReferenceAsset{}, fmt.Errorf("%s: %w", operation, ErrAudioReferenceDeleted)
	}
	if expected < 1 || value.Revision != expected || value.Revision == math.MaxInt64 {
		return ProjectHandle{}, domain.AudioReferenceAsset{}, revisionConflict(operation, expected, value.Revision)
	}
	return handle, value, nil
}

func (s *Service) finishAudioReferenceMutation(ctx context.Context, handle ProjectHandle, id domain.AudioReferenceAssetID, at time.Time) (domain.AudioReferenceAsset, error) {
	value, err := audioReferenceForProject(ctx, handle.Store, handle.ProjectID, id)
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, at); err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	return value, nil
}
