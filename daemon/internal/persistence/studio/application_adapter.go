package studio

import (
	"context"
	"errors"
	"fmt"
	"sync"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ApplicationProjectLibrary adapts the Studio Store to the Application
// ProjectLibrary port. Its mutex keeps lookup/add or refresh/mark-open sequences
// together inside this daemon.
type ApplicationProjectLibrary struct {
	store *Store
	mutex sync.Mutex
}

func NewApplicationProjectLibrary(store *Store) (*ApplicationProjectLibrary, error) {
	if store == nil {
		return nil, errors.New("create Application project library: Studio Store is required")
	}
	return &ApplicationProjectLibrary{store: store}, nil
}

func (l *ApplicationProjectLibrary) SyncProject(ctx context.Context, record projectapp.ProjectLibraryRecord) error {
	if l == nil || l.store == nil {
		return errors.New("sync Project library: adapter is required")
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()

	existing, err := l.store.Project(ctx, record.ProjectID.String())
	if errors.Is(err, ErrProjectLibraryEntryNotFound) {
		openedAt := record.OpenedAt
		if err := l.store.AddProject(ctx, ProjectLibraryEntry{
			ProjectID:    record.ProjectID.String(),
			RootPath:     record.RootPath,
			Snapshot:     toStudioSnapshot(record),
			AddedAt:      record.OpenedAt,
			LastOpenedAt: &openedAt,
			RefreshedAt:  record.OpenedAt,
		}); err != nil {
			if errors.Is(err, ErrProjectLibraryEntryConflict) {
				return projectapp.ErrProjectPathConflict
			}
			return fmt.Errorf("sync Project library: add: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("sync Project library: read existing entry: %w", err)
	}
	if existing.RootPath != record.RootPath {
		return projectapp.ErrProjectPathConflict
	}
	if err := l.store.RefreshProject(ctx, ProjectSnapshotRefresh{
		ProjectID:   record.ProjectID.String(),
		Snapshot:    toStudioSnapshot(record),
		RefreshedAt: record.OpenedAt,
	}); err != nil {
		return fmt.Errorf("sync Project library: refresh: %w", err)
	}
	if err := l.store.MarkProjectOpened(ctx, record.ProjectID.String(), record.OpenedAt); err != nil {
		return fmt.Errorf("sync Project library: mark opened: %w", err)
	}
	return nil
}

func (l *ApplicationProjectLibrary) CanDeleteProject(ctx context.Context, projectID domain.ProjectID) error {
	if l == nil || l.store == nil || ctx == nil || projectID.Validate() != nil {
		return projectapp.ErrInvalidRequest
	}
	active, err := l.store.ProjectHasActiveAgentTurns(ctx, projectID.String())
	if err != nil {
		return fmt.Errorf("inspect Project Agent tasks: %w", err)
	}
	if active {
		return projectapp.ErrProjectHasActiveAgent
	}
	return nil
}

func (l *ApplicationProjectLibrary) CanDeleteEpisode(ctx context.Context, projectID domain.ProjectID, episodeID domain.EpisodeID) error {
	if l == nil || l.store == nil || ctx == nil || projectID.Validate() != nil || episodeID.Validate() != nil {
		return projectapp.ErrInvalidRequest
	}
	active, err := l.store.EpisodeHasActiveAgentTurns(ctx, projectID.String(), episodeID.String())
	if err != nil {
		return fmt.Errorf("inspect Episode Agent tasks: %w", err)
	}
	if active {
		return projectapp.ErrEpisodeHasActiveAgent
	}
	return nil
}

func (l *ApplicationProjectLibrary) DeleteProject(ctx context.Context, projectID domain.ProjectID) error {
	if err := l.CanDeleteProject(ctx, projectID); err != nil {
		return err
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := l.store.DeleteProjectData(ctx, projectID.String()); err != nil {
		if errors.Is(err, ErrProjectHasActiveAgentTurns) {
			return projectapp.ErrProjectHasActiveAgent
		}
		return fmt.Errorf("delete Application project: %w", err)
	}
	return nil
}

func (l *ApplicationProjectLibrary) Projects(ctx context.Context) ([]projectapp.ProjectCatalogEntry, error) {
	if l == nil || l.store == nil || ctx == nil {
		return nil, errors.New("list Application projects: adapter is required")
	}
	values, err := l.store.Projects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Application projects: %w", err)
	}
	result := make([]projectapp.ProjectCatalogEntry, 0, len(values))
	for _, value := range values {
		mapped, err := applicationCatalogEntry(value)
		if err != nil {
			return nil, err
		}
		result = append(result, mapped)
	}
	return result, nil
}

func (l *ApplicationProjectLibrary) Project(ctx context.Context, projectID domain.ProjectID) (projectapp.ProjectCatalogEntry, error) {
	if l == nil || l.store == nil || ctx == nil || projectID.Validate() != nil {
		return projectapp.ProjectCatalogEntry{}, projectapp.ErrInvalidRequest
	}
	value, err := l.store.Project(ctx, projectID.String())
	if errors.Is(err, ErrProjectLibraryEntryNotFound) {
		return projectapp.ProjectCatalogEntry{}, projectapp.ErrProjectPathNotFound
	}
	if err != nil {
		return projectapp.ProjectCatalogEntry{}, fmt.Errorf("get Application project: %w", err)
	}
	return applicationCatalogEntry(value)
}

func applicationCatalogEntry(value ProjectLibraryEntry) (projectapp.ProjectCatalogEntry, error) {
	projectID, err := domain.ParseProjectID(value.ProjectID)
	if err != nil {
		return projectapp.ProjectCatalogEntry{}, fmt.Errorf("map Application project: invalid Project ID: %w", err)
	}
	return projectapp.ProjectCatalogEntry{
		ProjectID: projectID, RootPath: value.RootPath, DisplayName: value.Snapshot.DisplayName,
		CoverCacheKey: value.Snapshot.CoverCacheKey, Width: value.Snapshot.Width, Height: value.Snapshot.Height,
		EpisodeCount: value.Snapshot.EpisodeCount, ProjectUpdatedAt: value.Snapshot.ProjectUpdatedAt,
		LastOpenedAt: value.LastOpenedAt,
	}, nil
}

func toStudioSnapshot(record projectapp.ProjectLibraryRecord) ProjectSnapshot {
	return ProjectSnapshot{
		DisplayName:      record.DisplayName,
		CoverCacheKey:    record.CoverCacheKey,
		Width:            record.Width,
		Height:           record.Height,
		EpisodeCount:     record.EpisodeCount,
		ProjectUpdatedAt: record.ProjectUpdatedAt,
	}
}
