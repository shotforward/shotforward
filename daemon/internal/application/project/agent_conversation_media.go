package projectapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const maximumAgentInputImages = 5

func (s *Service) UploadAgentConversationMedia(ctx context.Context, input UploadAgentConversationMediaInput) (domain.AgentConversationMedia, error) {
	if s.mediaFiles == nil || input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil ||
		!validAgentMediaText(input.SessionID, 200) || !validAgentMediaText(input.ClientOperationID, 200) ||
		input.SourcePath == "" || input.OriginalFilename == "" {
		return domain.AgentConversationMedia{}, fmt.Errorf("upload Agent conversation media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "upload Agent conversation media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.AgentConversationMedia{}, err
	}
	artifact, installed, err := s.importEpisodeReferenceArtifact(ctx, handle, input.ProjectID, input.SourcePath, input.OriginalFilename, domain.MediaKindImage)
	if err != nil {
		return domain.AgentConversationMedia{}, fmt.Errorf("upload Agent conversation media: %w", err)
	}
	committed := false
	defer func() {
		if !committed && installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
	}()
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.AgentConversationMedia{}, err
	}
	name := strings.TrimSpace(input.OriginalFilename)
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	value := domain.AgentConversationMedia{EpisodeID: input.EpisodeID, SessionID: input.SessionID,
		ClientOperationID: input.ClientOperationID, Artifact: artifact, Name: name,
		Origin: domain.AgentConversationMediaUploaded, CreatedAt: now}
	if err := handle.Store.PutAgentConversationMedia(ctx, value, now); err != nil {
		return domain.AgentConversationMedia{}, fmt.Errorf("upload Agent conversation media: %w", err)
	}
	committed = true
	return value, nil
}

func (s *Service) PrepareAgentInputMedia(ctx context.Context, input PrepareAgentInputMediaInput) ([]AgentInputMedia, error) {
	if input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil ||
		!validAgentMediaText(input.SessionID, 200) || !validAgentMediaText(input.ClientOperationID, 200) ||
		len(input.References) > maximumAgentInputImages {
		return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "prepare Agent input media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return nil, err
	}
	existing, err := handle.Store.AgentConversationMediaForOperation(ctx, input.EpisodeID, input.SessionID, input.ClientOperationID)
	if err != nil {
		return nil, fmt.Errorf("prepare Agent input media: %w", err)
	}
	byID := make(map[domain.ArtifactID]domain.AgentConversationMedia, len(existing))
	for _, item := range existing {
		byID[item.Artifact.ID] = item
	}
	sessionMedia, err := handle.Store.AgentConversationMediaList(ctx, input.EpisodeID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("prepare Agent input media: %w", err)
	}
	sessionByID := make(map[string]domain.AgentConversationMedia, len(sessionMedia))
	for _, item := range sessionMedia {
		sessionByID[item.Artifact.ID.String()+"\x00"+string(item.Origin)] = item
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return nil, err
	}
	seen := make(map[domain.ArtifactID]struct{}, len(input.References))
	result := make([]AgentInputMedia, 0, len(input.References))
	for _, reference := range input.References {
		if reference.ArtifactID.Validate() != nil || !validAgentMediaText(reference.Name, 200) {
			return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
		}
		if _, duplicate := seen[reference.ArtifactID]; duplicate {
			return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
		}
		seen[reference.ArtifactID] = struct{}{}
		media, exists := byID[reference.ArtifactID]
		if !exists {
			var artifact domain.Artifact
			if reference.Origin == domain.AgentConversationMediaAsset {
				var readErr error
				artifact, readErr = handle.Store.Artifact(ctx, reference.ArtifactID)
				if readErr != nil || artifact.ProjectID != input.ProjectID || artifact.Kind != domain.MediaKindImage {
					return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
				}
				selected, selectedErr := handle.Store.ArtifactSelectedInEpisode(ctx, artifact.ID, input.EpisodeID)
				if selectedErr != nil || !selected {
					return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
				}
			} else {
				prior, found := sessionByID[reference.ArtifactID.String()+"\x00"+string(reference.Origin)]
				if !found || (reference.Origin != domain.AgentConversationMediaUploaded && reference.Origin != domain.AgentConversationMediaGenerated) {
					return nil, fmt.Errorf("prepare Agent input media: %w", ErrInvalidRequest)
				}
				artifact = prior.Artifact
			}
			media = domain.AgentConversationMedia{EpisodeID: input.EpisodeID, SessionID: input.SessionID,
				ClientOperationID: input.ClientOperationID, Artifact: artifact, Name: reference.Name, Origin: reference.Origin, CreatedAt: now}
			if err := handle.Store.PutAgentConversationMedia(ctx, media, now); err != nil {
				return nil, fmt.Errorf("prepare Agent input media: %w", err)
			}
		}
		if media.Artifact.Kind != domain.MediaKindImage {
			return nil, fmt.Errorf("prepare Agent input media: %w", ErrMediaKindMismatch)
		}
		result = append(result, AgentInputMedia{Media: media, AbsolutePath: filepath.Join(handle.RootPath, filepath.FromSlash(media.Artifact.RelativePath))})
	}
	return result, nil
}

func (s *Service) BindAgentInputMedia(ctx context.Context, input BindAgentInputMediaInput) error {
	if input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil || !validAgentMediaText(input.SessionID, 200) ||
		!validAgentMediaText(input.ClientOperationID, 200) || !validAgentMediaText(input.TurnID, 200) || len(input.ArtifactIDs) == 0 {
		return fmt.Errorf("bind Agent input media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "bind Agent input media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return err
	}
	if err := handle.Store.BindAgentConversationMedia(ctx, input.EpisodeID, input.SessionID, input.ClientOperationID, input.TurnID, input.ArtifactIDs, now); err != nil {
		return fmt.Errorf("bind Agent input media: %w", err)
	}
	return nil
}

func (s *Service) ImportAgentGeneratedMedia(ctx context.Context, input ImportAgentGeneratedMediaInput) (domain.AgentConversationMedia, error) {
	if s.mediaFiles == nil || input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil ||
		!validAgentMediaText(input.SessionID, 200) || !validAgentMediaText(input.TurnID, 200) || input.SourcePath == "" {
		return domain.AgentConversationMedia{}, fmt.Errorf("import Agent generated media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "import Agent generated media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.AgentConversationMedia{}, err
	}
	filename := strings.TrimSpace(input.OriginalFilename)
	if filename == "" {
		filename = filepath.Base(input.SourcePath)
	}
	artifact, installed, err := s.importEpisodeReferenceArtifact(ctx, handle, input.ProjectID, input.SourcePath, filename, domain.MediaKindImage)
	if err != nil {
		return domain.AgentConversationMedia{}, fmt.Errorf("import Agent generated media: %w", err)
	}
	committed := false
	defer func() {
		if !committed && installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
	}()
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.AgentConversationMedia{}, err
	}
	value := domain.AgentConversationMedia{EpisodeID: input.EpisodeID, SessionID: input.SessionID, TurnID: input.TurnID,
		Artifact: artifact, Name: "Generated image", Origin: domain.AgentConversationMediaGenerated, CreatedAt: now}
	if err := handle.Store.PutAgentConversationMedia(ctx, value, now); err != nil {
		return domain.AgentConversationMedia{}, fmt.Errorf("import Agent generated media: %w", err)
	}
	committed = true
	return value, nil
}

func (s *Service) ListAgentConversationMedia(ctx context.Context, input ListAgentConversationMediaInput) ([]domain.AgentConversationMedia, error) {
	if input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil || !validAgentMediaText(input.SessionID, 200) {
		return nil, fmt.Errorf("list Agent conversation media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "list Agent conversation media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return nil, err
	}
	values, err := handle.Store.AgentConversationMediaList(ctx, input.EpisodeID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("list Agent conversation media: %w", err)
	}
	return values, nil
}

// PrepareAgentImageGeneration resolves the images attached to the active Turn
// into daemon-only absolute paths. The scheduler-supplied Artifact IDs are
// authoritative; the Turn lookup remains as a restart-safe fallback.
func (s *Service) PrepareAgentImageGeneration(ctx context.Context, input PrepareAgentImageGenerationInput) (AgentImageGenerationContext, error) {
	if input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil ||
		!validAgentMediaText(input.SessionID, 200) || !validAgentMediaText(input.TurnID, 200) {
		return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "prepare Agent image generation", input.ProjectID, input.EpisodeID)
	if err != nil {
		return AgentImageGenerationContext{}, err
	}
	project, err := handle.Store.Project(ctx)
	if err != nil {
		return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", err)
	}
	values, err := handle.Store.AgentConversationMediaList(ctx, input.EpisodeID, input.SessionID)
	if err != nil {
		return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", err)
	}
	result := AgentImageGenerationContext{ProjectRoot: handle.RootPath, Project: project}
	if len(input.ArtifactIDs) > maximumAgentInputImages {
		return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", ErrInvalidRequest)
	}
	if len(input.ArtifactIDs) > 0 {
		byID := make(map[domain.ArtifactID]domain.AgentConversationMedia, len(values))
		for _, value := range values {
			byID[value.Artifact.ID] = value
		}
		for _, artifactID := range input.ArtifactIDs {
			value, exists := byID[artifactID]
			if !exists || value.Artifact.Kind != domain.MediaKindImage {
				return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", ErrMediaKindMismatch)
			}
			result.References = append(result.References, AgentInputMedia{Media: value, AbsolutePath: filepath.Join(handle.RootPath, filepath.FromSlash(value.Artifact.RelativePath))})
		}
		return result, nil
	}
	for _, value := range values {
		if value.TurnID != input.TurnID || value.Origin == domain.AgentConversationMediaGenerated {
			continue
		}
		if value.Artifact.Kind != domain.MediaKindImage || len(result.References) >= maximumAgentInputImages {
			return AgentImageGenerationContext{}, fmt.Errorf("prepare Agent image generation: %w", ErrMediaKindMismatch)
		}
		result.References = append(result.References, AgentInputMedia{
			Media: value, AbsolutePath: filepath.Join(handle.RootPath, filepath.FromSlash(value.Artifact.RelativePath)),
		})
	}
	return result, nil
}

func validAgentMediaText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]rune(value)) <= maximum && !strings.ContainsAny(value, "\r\n\x00")
}
