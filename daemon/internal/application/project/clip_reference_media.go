package projectapp

import (
	"context"
	"fmt"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Service) ListClipReferenceMedia(ctx context.Context, input ListClipReferenceMediaInput) ([]domain.Artifact, error) {
	handle, clip, err := s.referenceMediaClip(ctx, "list Clip reference media", input.ProjectID, input.EpisodeID, input.ClipID)
	if err != nil {
		return nil, err
	}
	values, err := handle.Store.ClipReferenceArtifacts(ctx, clip.Clip.ID)
	if err != nil {
		return nil, fmt.Errorf("list Clip reference media: %w", err)
	}
	return values, nil
}

func (s *Service) UploadClipReferenceMedia(ctx context.Context, input UploadClipReferenceMediaInput) (domain.Artifact, error) {
	if s.mediaFiles == nil {
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", ErrMediaImportUnavailable)
	}
	if !input.Kind.IsValid() || input.SourcePath == "" || input.OriginalFilename == "" {
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", ErrInvalidRequest)
	}
	handle, clip, err := s.referenceMediaClip(ctx, "upload Clip reference media", input.ProjectID, input.EpisodeID, input.ClipID)
	if err != nil {
		return domain.Artifact{}, err
	}
	artifact, installed, err := s.importEpisodeReferenceArtifact(ctx, handle, input.ProjectID, input.SourcePath, input.OriginalFilename, input.Kind)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", err)
	}
	if _, err := s.ensureEpisodeReferenceMedia(ctx, handle, input.EpisodeID, artifact, now); err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", err)
	}
	if err := handle.Store.AddClipReferenceArtifact(ctx, clip.Clip.ID, artifact, now); err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return domain.Artifact{}, fmt.Errorf("upload Clip reference media: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.Artifact{}, err
	}
	return artifact, nil
}

func (s *Service) DeleteClipReferenceMedia(ctx context.Context, input DeleteClipReferenceMediaInput) error {
	handle, clip, err := s.referenceMediaClip(ctx, "delete Clip reference media", input.ProjectID, input.EpisodeID, input.ClipID)
	if err != nil {
		return err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return fmt.Errorf("delete Clip reference media: %w", err)
	}
	if err := handle.Store.DeleteClipReferenceArtifact(ctx, clip.Clip.ID, input.ArtifactID, now); err != nil {
		return fmt.Errorf("delete Clip reference media: %w", err)
	}
	return s.syncLibraryAfterMutation(ctx, handle, now)
}

func (s *Service) referenceMediaClip(ctx context.Context, operation string, projectID domain.ProjectID, episodeID domain.EpisodeID, clipID domain.ClipID) (ProjectHandle, domain.ClipDetails, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.ClipDetails{}, err
	}
	clip, err := handle.Store.Clip(ctx, clipID)
	if err != nil {
		return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, err)
	}
	if clip.Clip.ProjectID != projectID || clip.Clip.EpisodeID != episodeID || clip.Clip.IsDeleted() {
		return ProjectHandle{}, domain.ClipDetails{}, fmt.Errorf("%s: %w", operation, ErrClipNotFound)
	}
	return handle, clip, nil
}
