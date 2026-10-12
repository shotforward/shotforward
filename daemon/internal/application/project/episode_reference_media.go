package projectapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const defaultReferenceMediaNameLength = 20

func (s *Service) ListEpisodeReferenceMedia(ctx context.Context, input ListEpisodeReferenceMediaInput) ([]domain.EpisodeReferenceMedia, error) {
	handle, err := s.referenceMediaEpisode(ctx, "list Episode reference media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return nil, err
	}
	values, err := handle.Store.EpisodeReferenceMediaList(ctx, input.EpisodeID, input.IncludeDeleted)
	if err != nil {
		return nil, fmt.Errorf("list Episode reference media: %w", err)
	}
	return values, nil
}

func (s *Service) UploadEpisodeReferenceMedia(ctx context.Context, input UploadEpisodeReferenceMediaInput) (domain.EpisodeReferenceMedia, error) {
	if s.mediaFiles == nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("upload Episode reference media: %w", ErrMediaImportUnavailable)
	}
	if !input.Kind.IsValid() || input.SourcePath == "" || input.OriginalFilename == "" {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("upload Episode reference media: %w", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "upload Episode reference media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	artifact, installed, err := s.importEpisodeReferenceArtifact(ctx, handle, input.ProjectID, input.SourcePath, input.OriginalFilename, input.Kind)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("upload Episode reference media: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return domain.EpisodeReferenceMedia{}, err
	}
	value, err := s.ensureEpisodeReferenceMedia(ctx, handle, input.EpisodeID, artifact, now)
	if err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("upload Episode reference media: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	return value, nil
}

func (s *Service) UpdateEpisodeReferenceMedia(ctx context.Context, input UpdateEpisodeReferenceMediaInput) (domain.EpisodeReferenceMedia, error) {
	if (input.TrimStartMilliseconds == nil) != (input.TrimEndMilliseconds == nil) {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("update Episode reference media: %w: both trim bounds are required", ErrInvalidRequest)
	}
	handle, err := s.referenceMediaEpisode(ctx, "update Episode reference media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	current, err := handle.Store.EpisodeReferenceMedia(ctx, input.EpisodeID, input.ArtifactID, false)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("update Episode reference media: %w", err)
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = input.Description
	if input.TrimStartMilliseconds != nil {
		current.TrimStartMilliseconds = *input.TrimStartMilliseconds
		current.TrimEndMilliseconds = *input.TrimEndMilliseconds
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	current.UpdatedAt = now
	if err := current.Validate(); err != nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("update Episode reference media: %w: %w", ErrInvalidRequest, err)
	}
	if err := episodeReferenceMediaNameAvailable(ctx, handle.Store, input.EpisodeID, current.Artifact.ID, current.Name); err != nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("update Episode reference media: %w", err)
	}
	if err := handle.Store.UpdateEpisodeReferenceMedia(ctx, current, input.ExpectedRevision, now); err != nil {
		return domain.EpisodeReferenceMedia{}, fmt.Errorf("update Episode reference media: %w", err)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	return handle.Store.EpisodeReferenceMedia(ctx, input.EpisodeID, input.ArtifactID, false)
}

func (s *Service) DeleteEpisodeReferenceMedia(ctx context.Context, input DeleteEpisodeReferenceMediaInput) error {
	handle, err := s.referenceMediaEpisode(ctx, "delete Episode reference media", input.ProjectID, input.EpisodeID)
	if err != nil {
		return err
	}
	current, err := handle.Store.EpisodeReferenceMedia(ctx, input.EpisodeID, input.ArtifactID, false)
	if err != nil {
		return fmt.Errorf("delete Episode reference media: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return err
	}
	if err := handle.Store.DeleteEpisodeReferenceMedia(ctx, current, input.ExpectedRevision, now); err != nil {
		return fmt.Errorf("delete Episode reference media: %w", err)
	}
	return s.syncLibraryAfterMutation(ctx, handle, now)
}

func (s *Service) AttachClipReferenceMedia(ctx context.Context, input AttachClipReferenceMediaInput) error {
	handle, clip, err := s.referenceMediaClip(ctx, "attach Clip reference media", input.ProjectID, input.EpisodeID, input.ClipID)
	if err != nil {
		return err
	}
	value, err := handle.Store.EpisodeReferenceMedia(ctx, input.EpisodeID, input.ArtifactID, false)
	if err != nil {
		return fmt.Errorf("attach Clip reference media: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return err
	}
	if err := handle.Store.AddClipReferenceArtifact(ctx, clip.Clip.ID, value.Artifact, now); err != nil {
		return fmt.Errorf("attach Clip reference media: %w", err)
	}
	return s.syncLibraryAfterMutation(ctx, handle, now)
}

func (s *Service) referenceMediaEpisode(ctx context.Context, operation string, projectID domain.ProjectID, episodeID domain.EpisodeID) (ProjectHandle, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, err
	}
	episode, err := handle.Store.Episode(ctx, episodeID)
	if err != nil || episode.ProjectID != projectID || episode.IsDeleted() {
		return ProjectHandle{}, fmt.Errorf("%s: %w", operation, ErrEpisodeNotFound)
	}
	return handle, nil
}

func (s *Service) importEpisodeReferenceArtifact(ctx context.Context, handle ProjectHandle, projectID domain.ProjectID, sourcePath, originalFilename string, kind domain.MediaKind) (domain.Artifact, InstalledMedia, error) {
	prepared, err := s.mediaFiles.Prepare(ctx, PrepareMediaRequest{ProjectRoot: handle.RootPath, SourcePath: sourcePath, OriginalFilename: originalFilename, Kind: kind})
	if err != nil {
		return domain.Artifact{}, InstalledMedia{}, err
	}
	discard := true
	defer func() {
		if discard {
			_ = s.mediaFiles.Discard(handle.RootPath, prepared)
		}
	}()
	artifact, err := handle.Store.ArtifactBySHA256(ctx, projectID, prepared.SHA256)
	if err == nil {
		if err := s.mediaFiles.Discard(handle.RootPath, prepared); err != nil {
			return domain.Artifact{}, InstalledMedia{}, err
		}
		discard = false
		return artifact, InstalledMedia{}, nil
	}
	if !errors.Is(err, ErrArtifactNotFound) {
		return domain.Artifact{}, InstalledMedia{}, err
	}
	artifactID, err := s.ids.NewArtifactID()
	if err != nil {
		return domain.Artifact{}, InstalledMedia{}, fmt.Errorf("%w: %w", ErrProjectWriteFailed, err)
	}
	installed, err := s.mediaFiles.Install(ctx, handle.RootPath, prepared, artifactID)
	if err != nil {
		return domain.Artifact{}, InstalledMedia{}, err
	}
	discard = false
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		_ = s.mediaFiles.Remove(handle.RootPath, installed)
		return domain.Artifact{}, InstalledMedia{}, err
	}
	return artifactFromPrepared(artifactID, projectID, installed.RelativePath, prepared, now), installed, nil
}

func (s *Service) ensureEpisodeReferenceMedia(ctx context.Context, handle ProjectHandle, episodeID domain.EpisodeID, artifact domain.Artifact, at time.Time) (domain.EpisodeReferenceMedia, error) {
	existing, err := handle.Store.EpisodeReferenceMedia(ctx, episodeID, artifact.ID, true)
	if err == nil && existing.DeletedAt == nil {
		return existing, nil
	}
	if err != nil && !errors.Is(err, ErrArtifactNotFound) {
		return domain.EpisodeReferenceMedia{}, err
	}
	name, err := s.uniqueEpisodeReferenceMediaName(ctx, handle.Store, episodeID, artifact.OriginalFilename, artifact.ID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	value := domain.EpisodeReferenceMedia{EpisodeID: episodeID, Artifact: artifact, Name: name, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := handle.Store.PutEpisodeReferenceMedia(ctx, value, at); err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	return handle.Store.EpisodeReferenceMedia(ctx, episodeID, artifact.ID, false)
}

func (s *Service) uniqueEpisodeReferenceMediaName(ctx context.Context, store ProjectStore, episodeID domain.EpisodeID, filename string, artifactID domain.ArtifactID) (string, error) {
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	base = strings.Map(func(value rune) rune {
		switch value {
		case '@', '\x00':
			return -1
		case '\r', '\n', '\t':
			return ' '
		default:
			return value
		}
	}, base)
	base = strings.Join(strings.Fields(base), " ")
	runes := []rune(base)
	if len(runes) > defaultReferenceMediaNameLength {
		runes = runes[:defaultReferenceMediaNameLength]
	}
	base = strings.TrimSpace(string(runes))
	if base == "" {
		base = "未命名素材"
	}
	used := map[string]bool{}
	assets, err := store.EpisodeAssets(ctx, episodeID, false)
	if err != nil {
		return "", err
	}
	for _, asset := range assets {
		used[strings.ToLower(asset.Identity.Name)] = true
	}
	media, err := store.EpisodeReferenceMediaList(ctx, episodeID, false)
	if err != nil {
		return "", err
	}
	for _, item := range media {
		if item.Artifact.ID != artifactID {
			used[strings.ToLower(item.Name)] = true
		}
	}
	for index := 1; ; index++ {
		candidate := base
		if index > 1 {
			suffix := "-" + strconv.Itoa(index)
			limit := 40 - len([]rune(suffix))
			value := []rune(base)
			if len(value) > limit {
				value = value[:limit]
			}
			candidate = string(value) + suffix
		}
		if !used[strings.ToLower(candidate)] {
			return candidate, nil
		}
	}
}

func episodeReferenceMediaNameAvailable(ctx context.Context, store ProjectStore, episodeID domain.EpisodeID, artifactID domain.ArtifactID, name string) error {
	key := strings.ToLower(strings.TrimSpace(name))
	assets, err := store.EpisodeAssets(ctx, episodeID, false)
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if strings.ToLower(asset.Identity.Name) == key {
			return fmt.Errorf("%w: media name conflicts with an existing generated asset", ErrInvalidRequest)
		}
	}
	return episodeReferenceUploadNameAvailable(ctx, store, episodeID, artifactID, name)
}

func episodeReferenceUploadNameAvailable(ctx context.Context, store ProjectStore, episodeID domain.EpisodeID, artifactID domain.ArtifactID, name string) error {
	key := strings.ToLower(strings.TrimSpace(name))
	media, err := store.EpisodeReferenceMediaList(ctx, episodeID, false)
	if err != nil {
		return err
	}
	for _, item := range media {
		if item.Artifact.ID != artifactID && strings.ToLower(item.Name) == key {
			return fmt.Errorf("%w: media name already exists", ErrInvalidRequest)
		}
	}
	return nil
}

func assetIdentityNameAvailableForUploads(ctx context.Context, store ProjectStore, identityID domain.AssetIdentityID, name string) error {
	episodes, err := store.Episodes(ctx)
	if err != nil {
		return err
	}
	for _, episode := range episodes {
		assets, err := store.EpisodeAssets(ctx, episode.ID, false)
		if err != nil {
			return err
		}
		for _, asset := range assets {
			if asset.Identity.ID != identityID {
				continue
			}
			if err := episodeReferenceUploadNameAvailable(ctx, store, episode.ID, "", name); err != nil {
				return err
			}
			break
		}
	}
	return nil
}
