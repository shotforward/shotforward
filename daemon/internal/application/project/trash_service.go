package projectapp

import (
	"context"
	"fmt"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type projectTrashReader interface {
	DeletedStoryboardItems(context.Context) ([]domain.StoryboardScene, []domain.ClipDetails, error)
}

func (s *Service) ListProjectTrash(ctx context.Context, projectID domain.ProjectID) (ProjectTrashResult, error) {
	handle, err := s.assetProjectHandle(ctx, "list Project trash", projectID)
	if err != nil {
		return ProjectTrashResult{}, err
	}
	value, err := handle.Store.Project(ctx)
	if err != nil {
		return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", err)
	}
	episodes, err := handle.Store.Episodes(ctx)
	if err != nil {
		return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", err)
	}
	deletedEpisodes, err := handle.Store.DeletedEpisodes(ctx)
	if err != nil {
		return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", err)
	}
	allEpisodes := append(append([]domain.EpisodeSummary{}, episodes...), deletedEpisodes...)
	result := ProjectTrashResult{Project: value, Episodes: allEpisodes, Assets: []domain.EpisodeAsset{}, Scenes: []domain.StoryboardScene{}, Clips: []domain.ClipDetails{}, StoryboardRevisions: make(map[domain.EpisodeID]int64, len(episodes))}
	for _, episode := range episodes {
		assets, readErr := handle.Store.EpisodeAssets(ctx, episode.ID, true)
		if readErr != nil {
			return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", readErr)
		}
		for _, asset := range assets {
			if asset.Asset.IsDeleted() {
				result.Assets = append(result.Assets, asset)
			}
		}
		storyboard, readErr := handle.Store.Storyboard(ctx, episode.ID)
		if readErr != nil {
			return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", readErr)
		}
		result.StoryboardRevisions[episode.ID] = storyboard.Revision
	}
	reader, ok := handle.Store.(projectTrashReader)
	if !ok {
		return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w: trash projection is unavailable", ErrProjectDataInvalid)
	}
	result.Scenes, result.Clips, err = reader.DeletedStoryboardItems(ctx)
	if err != nil {
		return ProjectTrashResult{}, fmt.Errorf("list Project trash: %w", err)
	}
	deletedEpisodeIDs := make(map[domain.EpisodeID]struct{}, len(deletedEpisodes))
	for _, episode := range deletedEpisodes {
		deletedEpisodeIDs[episode.ID] = struct{}{}
	}
	visibleScenes := result.Scenes[:0]
	for _, scene := range result.Scenes {
		if _, hidden := deletedEpisodeIDs[scene.Scene.EpisodeID]; !hidden {
			visibleScenes = append(visibleScenes, scene)
		}
	}
	result.Scenes = visibleScenes
	visibleClips := result.Clips[:0]
	for _, clip := range result.Clips {
		if _, hidden := deletedEpisodeIDs[clip.Clip.EpisodeID]; !hidden {
			visibleClips = append(visibleClips, clip)
		}
	}
	result.Clips = visibleClips
	return result, nil
}
