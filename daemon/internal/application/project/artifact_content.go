package projectapp

import (
	"context"
	"fmt"
	"strings"
)

func (s *Service) GetArtifactContent(ctx context.Context, input GetArtifactContentInput) (ArtifactContentSource, error) {
	handle, err := s.assetProjectHandle(ctx, "get Artifact content", input.ProjectID)
	if err != nil {
		return ArtifactContentSource{}, err
	}
	if input.ArtifactID.Validate() != nil {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrInvalidRequest)
	}
	artifact, err := handle.Store.Artifact(ctx, input.ArtifactID)
	if err != nil {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", err)
	}
	if artifact.ProjectID != input.ProjectID {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrArtifactNotFound)
	}
	if !strings.HasPrefix(artifact.RelativePath, "media/") {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrProjectDataInvalid)
	}
	if s.projectFiles == nil {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrMediaImportUnavailable)
	}
	file, err := s.projectFiles.OpenProjectFile(handle.RootPath, artifact.RelativePath)
	if err != nil {
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrMediaImportUnreadable)
	}
	if file.Content == nil || file.ByteSize != artifact.ByteSize || file.ByteSize < 1 {
		if file.Content != nil {
			_ = file.Content.Close()
		}
		return ArtifactContentSource{}, fmt.Errorf("get Artifact content: %w", ErrProjectDataInvalid)
	}
	return ArtifactContentSource{Content: file.Content, MIMEType: artifact.MIMEType, OriginalFilename: artifact.OriginalFilename, ByteSize: file.ByteSize, ModifiedAt: file.ModifiedAt}, nil
}
