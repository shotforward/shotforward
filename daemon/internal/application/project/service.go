package projectapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	initialRevision       int64 = 1
	defaultEpisodeTitle         = "视频 1"
	defaultTargetDuration       = 60 * time.Second
)

type Dependencies struct {
	IDs               IDGenerator
	Clock             Clock
	Workspace         ProjectWorkspace
	Stores            ProjectStoreFactory
	Library           ProjectLibrary
	Importer          ScriptImporter
	Registry          *ProjectStoreRegistry
	MediaFiles        MediaFileProcessor
	GenerationCatalog GenerationCapabilityCatalog
	GenerationQueue   GenerationQueueNotifier
	GenerationEvents  GenerationEventPublisher
	PromptTranslator  GenerationPromptTranslator
	ExportQueue       ExportQueueNotifier
	ExportEvents      ExportEventPublisher
	ProjectFiles      ProjectFileReader
}

// Service implements Project creation and opening independently of transport.
type Service struct {
	ids                    IDGenerator
	clock                  Clock
	workspace              ProjectWorkspace
	stores                 ProjectStoreFactory
	library                ProjectLibrary
	importer               ScriptImporter
	registry               *ProjectStoreRegistry
	mediaFiles             MediaFileProcessor
	generationCatalog      GenerationCapabilityCatalog
	generationQueue        GenerationQueueNotifier
	generationEvents       GenerationEventPublisher
	promptTranslator       GenerationPromptTranslator
	generationSubmissionMu sync.Mutex
	generationSubmissions  map[generationSubmissionIdentity]*generationSubmissionGate
	exportQueue            ExportQueueNotifier
	exportEvents           ExportEventPublisher
	projectFiles           ProjectFileReader
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.IDs == nil || dependencies.Clock == nil ||
		dependencies.Workspace == nil || dependencies.Stores == nil ||
		dependencies.Library == nil || dependencies.Importer == nil ||
		dependencies.Registry == nil {
		return nil, errors.New("create Project service: every dependency is required")
	}
	return &Service{
		ids:               dependencies.IDs,
		clock:             dependencies.Clock,
		workspace:         dependencies.Workspace,
		stores:            dependencies.Stores,
		library:           dependencies.Library,
		importer:          dependencies.Importer,
		registry:          dependencies.Registry,
		mediaFiles:        dependencies.MediaFiles,
		generationCatalog: dependencies.GenerationCatalog,
		generationQueue:   dependencies.GenerationQueue,
		generationEvents:  dependencies.GenerationEvents,
		promptTranslator:  dependencies.PromptTranslator,
		exportQueue:       dependencies.ExportQueue,
		exportEvents:      dependencies.ExportEvents,
		projectFiles:      dependencies.ProjectFiles,
	}, nil
}

func (s *Service) CreateProject(ctx context.Context, input CreateProjectInput) (ProjectResult, error) {
	if err := validateContext(ctx); err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w", err)
	}
	if err := validateRootPathInput(input.RootPath); err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w", err)
	}
	output, err := domain.NewOutputSpec(input.OutputWidth, input.OutputHeight)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w: %w", ErrInvalidRequest, err)
	}
	projectID, err := s.ids.NewProjectID()
	if err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w: %w", ErrProjectCreateFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w: %w", ErrProjectCreateFailed, err)
	}

	project := domain.Project{
		ID:          projectID,
		Name:        input.Name,
		Description: input.Description,
		Output:      output,
		Revision:    initialRevision,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := project.Validate(); err != nil {
		if errors.Is(err, domain.ErrInvalidProjectID) {
			return ProjectResult{}, fmt.Errorf("create Project: %w: %w", ErrProjectCreateFailed, err)
		}
		return ProjectResult{}, fmt.Errorf("create Project: %w: %w", ErrInvalidRequest, err)
	}
	paths, err := s.workspace.Create(input.RootPath)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("create Project: %w", err)
	}
	store, err := s.stores.Create(ctx, paths.Database)
	if err != nil {
		return ProjectResult{}, s.cleanupFailedCreation(paths, nil, err)
	}
	if err := store.InitializeProject(ctx, project); err != nil {
		return ProjectResult{}, s.cleanupFailedCreation(paths, store, err)
	}

	result := ProjectResult{
		Project:  project,
		Episodes: []domain.EpisodeSummary{},
	}
	if err := s.library.SyncProject(ctx, libraryRecord(paths.Root, result, now)); err != nil {
		return ProjectResult{}, registrationFailure(ErrProjectCreatedNotRegistered, paths.Root, store, err)
	}

	handle, reused, err := s.registry.Register(ProjectHandle{
		ProjectID: project.ID,
		RootPath:  paths.Root,
		Store:     store,
	})
	if err != nil {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close Project Store: %w", closeErr))
		}
		return ProjectResult{}, fmt.Errorf("create Project: register open Store: %w", err)
	}
	if reused {
		if err := store.Close(); err != nil {
			return ProjectResult{}, fmt.Errorf("create Project: close duplicate Store: %w", err)
		}
		return s.readResult(ctx, handle.Store)
	}
	return result, nil
}

func (s *Service) OpenProject(ctx context.Context, input OpenProjectInput) (_ ProjectResult, returnErr error) {
	return s.openProject(ctx, input.RootPath, "open Project", "", true)
}

// RestoreProject reopens a Studio-library entry during daemon startup without
// treating process recovery as a user open action.
func (s *Service) RestoreProject(ctx context.Context, input RestoreProjectInput) (_ ProjectResult, returnErr error) {
	if err := input.ProjectID.Validate(); err != nil {
		return ProjectResult{}, fmt.Errorf("restore Project: %w: %w", ErrInvalidRequest, err)
	}
	return s.openProject(ctx, input.RootPath, "restore Project", input.ProjectID, false)
}

func (s *Service) openProject(ctx context.Context, rootPath, operation string, expectedID domain.ProjectID, syncLibrary bool) (_ ProjectResult, returnErr error) {
	if err := validateContext(ctx); err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	if err := validateRootPathInput(rootPath); err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	paths, err := s.workspace.Open(rootPath)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	var now time.Time
	if syncLibrary {
		now, err = validOperationTime(s.clock.Now())
		if err != nil {
			return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
		}
	}

	if handle, ok := s.registry.ByRootPath(paths.Root); ok {
		result, err := s.readResult(ctx, handle.Store)
		if err != nil {
			return ProjectResult{}, err
		}
		if expectedID != "" && result.Project.ID != expectedID {
			return ProjectResult{}, fmt.Errorf("%s: %w", operation, ErrProjectPathConflict)
		}
		if syncLibrary {
			if err := s.library.SyncProject(ctx, libraryRecord(paths.Root, result, now)); err != nil {
				return ProjectResult{}, &RegistrationError{
					Category: ErrProjectOpenedNotRegistered,
					RootPath: paths.Root,
					Cause:    err,
				}
			}
		}
		return result, nil
	}

	store, err := s.stores.Open(ctx, paths.Database)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	keepStore := false
	defer func() {
		if keepStore {
			return
		}
		if closeErr := store.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("%s: close rejected Store: %w", operation, closeErr))
		}
	}()

	result, err := s.readResult(ctx, store)
	if err != nil {
		return ProjectResult{}, err
	}
	if expectedID != "" && result.Project.ID != expectedID {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, ErrProjectPathConflict)
	}
	if existing, ok := s.registry.ByProjectID(result.Project.ID); ok {
		if existing.RootPath != paths.Root {
			return ProjectResult{}, fmt.Errorf("%s: %w", operation, ErrProjectPathConflict)
		}
		return s.readResult(ctx, existing.Store)
	}
	if syncLibrary {
		if err := s.library.SyncProject(ctx, libraryRecord(paths.Root, result, now)); err != nil {
			return ProjectResult{}, &RegistrationError{
				Category: ErrProjectOpenedNotRegistered,
				RootPath: paths.Root,
				Cause:    err,
			}
		}
	}

	handle, reused, err := s.registry.Register(ProjectHandle{
		ProjectID: result.Project.ID,
		RootPath:  paths.Root,
		Store:     store,
	})
	if err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	if reused {
		return s.readResult(ctx, handle.Store)
	}
	keepStore = true
	return result, nil
}

// GetProject returns the authoritative Project and Episode summaries from an
// already-open project Store.
func (s *Service) GetProject(ctx context.Context, projectID domain.ProjectID) (ProjectResult, error) {
	if err := validateContext(ctx); err != nil {
		return ProjectResult{}, fmt.Errorf("get Project: %w", err)
	}
	handle, err := s.openProjectHandle(projectID)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("get Project: %w", err)
	}
	return s.readResult(ctx, handle.Store)
}

// UpdateProject changes user-editable Project metadata without moving or
// renaming the portable project directory.
func (s *Service) UpdateProject(ctx context.Context, input UpdateProjectInput) (ProjectResult, error) {
	if err := validateContext(ctx); err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w", err)
	}
	if err := input.ProjectID.Validate(); err != nil || input.ExpectedRevision < 1 {
		return ProjectResult{}, fmt.Errorf("update Project: %w", ErrInvalidRequest)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w", err)
	}
	current, err := handle.Store.Project(ctx)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w: %w", ErrProjectDataInvalid, err)
	}
	if current.ID != input.ProjectID {
		return ProjectResult{}, fmt.Errorf("update Project: %w: open Store belongs to another Project", ErrProjectDataInvalid)
	}
	if current.Revision != input.ExpectedRevision {
		return ProjectResult{}, fmt.Errorf("update Project: %w: expected %d, current %d", ErrRevisionConflict, input.ExpectedRevision, current.Revision)
	}
	if current.Revision == math.MaxInt64 {
		return ProjectResult{}, fmt.Errorf("update Project: %w: revision is exhausted", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w: %w", ErrProjectWriteFailed, err)
	}
	updated := current
	updated.Name = input.Name
	updated.Description = input.Description
	updated.Revision++
	updated.UpdatedAt = now
	if err := updated.Validate(); err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateProject(ctx, updated, input.ExpectedRevision); err != nil {
		return ProjectResult{}, fmt.Errorf("update Project: %w", err)
	}
	return s.finishProjectMutation(ctx, handle, now)
}

// CloseProject releases one open project Store without removing its Studio
// library entry or deleting any portable project data.
func (s *Service) CloseProject(ctx context.Context, projectID domain.ProjectID) error {
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("close Project: %w", err)
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("close Project: %w: %w", ErrInvalidRequest, err)
	}
	handle, ok := s.registry.ByProjectID(projectID)
	if !ok {
		return fmt.Errorf("close Project: %w", ErrProjectStoreNotOpen)
	}
	active, err := handle.Store.GenerationJobsByStates(ctx, []domain.GenerationJobState{domain.GenerationJobQueued, domain.GenerationJobRunning, domain.GenerationJobStopRequested}, 1)
	if err != nil {
		return fmt.Errorf("close Project: inspect GenerationJobs: %w", err)
	}
	if len(active) != 0 {
		return fmt.Errorf("close Project: %w", ErrProjectHasActiveGeneration)
	}
	exports, err := handle.Store.ExportJobsByStates(ctx, []domain.ExportJobState{domain.ExportJobQueued, domain.ExportJobRunning, domain.ExportJobCancelRequested}, 1)
	if err != nil {
		return fmt.Errorf("close Project: inspect ExportJobs: %w", err)
	}
	if len(exports) != 0 {
		return fmt.Errorf("close Project: %w", ErrProjectHasActiveExport)
	}
	if err := s.registry.CloseProject(projectID); err != nil {
		return fmt.Errorf("close Project: %w", err)
	}
	return nil
}

// DeleteProject permanently removes the dedicated portable project directory
// and then clears its Studio registration and conversations.
func (s *Service) DeleteProject(ctx context.Context, input DeleteProjectInput) error {
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if errors.Is(err, ErrProjectStoreNotOpen) {
		return s.deleteUnavailableProject(ctx, input)
	}
	if err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	result, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	if input.ConfirmationName != result.Project.Name {
		return fmt.Errorf("delete Project: %w", ErrProjectNameConfirmation)
	}
	if err := s.library.CanDeleteProject(ctx, input.ProjectID); err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	active, err := handle.Store.GenerationJobsByStates(ctx, []domain.GenerationJobState{domain.GenerationJobQueued, domain.GenerationJobRunning, domain.GenerationJobStopRequested}, 1)
	if err != nil {
		return fmt.Errorf("delete Project: inspect GenerationJobs: %w", err)
	}
	if len(active) != 0 {
		return fmt.Errorf("delete Project: %w", ErrProjectHasActiveGeneration)
	}
	exports, err := handle.Store.ExportJobsByStates(ctx, []domain.ExportJobState{domain.ExportJobQueued, domain.ExportJobRunning, domain.ExportJobCancelRequested}, 1)
	if err != nil {
		return fmt.Errorf("delete Project: inspect ExportJobs: %w", err)
	}
	if len(exports) != 0 {
		return fmt.Errorf("delete Project: %w", ErrProjectHasActiveExport)
	}
	paths, err := s.workspace.Open(handle.RootPath)
	if err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	if err := s.registry.CloseProject(input.ProjectID); err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	if err := s.workspace.Delete(paths); err != nil {
		return fmt.Errorf("delete Project: %w", err)
	}
	if err := s.library.DeleteProject(ctx, input.ProjectID); err != nil {
		return fmt.Errorf("delete Project registration: %w", err)
	}
	return nil
}

// deleteUnavailableProject removes a stale Studio registration when its
// portable project directory no longer exists. The cached library name is
// used only for the same explicit confirmation required by normal deletion;
// active Agent turns remain protected. Existing but unreadable project roots
// are deliberately left untouched so corruption or permission failures cannot
// turn into accidental data loss.
func (s *Service) deleteUnavailableProject(ctx context.Context, input DeleteProjectInput) error {
	entry, err := s.library.Project(ctx, input.ProjectID)
	if err != nil {
		return fmt.Errorf("delete unavailable Project: %w", err)
	}
	if input.ConfirmationName != entry.DisplayName {
		return fmt.Errorf("delete unavailable Project: %w", ErrProjectNameConfirmation)
	}
	if err := s.library.CanDeleteProject(ctx, input.ProjectID); err != nil {
		return fmt.Errorf("delete unavailable Project: %w", err)
	}
	if _, err := s.workspace.Open(entry.RootPath); !errors.Is(err, ErrProjectPathNotFound) {
		if err == nil {
			err = ErrProjectStoreNotOpen
		}
		return fmt.Errorf("delete unavailable Project: %w", err)
	}
	if err := s.library.DeleteProject(ctx, input.ProjectID); err != nil {
		return fmt.Errorf("delete unavailable Project registration: %w", err)
	}
	return nil
}

func (s *Service) readResult(ctx context.Context, store ProjectStore) (ProjectResult, error) {
	project, err := store.Project(ctx)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("read opened Project: %w: %w", ErrProjectDataInvalid, err)
	}
	if err := project.Validate(); err != nil {
		return ProjectResult{}, fmt.Errorf("read opened Project: %w: %w", ErrProjectDataInvalid, err)
	}
	episodes, err := store.Episodes(ctx)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("read opened Project: %w: %w", ErrProjectDataInvalid, err)
	}
	sequences := make(map[int]struct{}, len(episodes))
	for _, episode := range episodes {
		if err := episode.Validate(); err != nil {
			return ProjectResult{}, fmt.Errorf("read opened Project: %w: %w", ErrProjectDataInvalid, err)
		}
		if episode.ProjectID != project.ID {
			return ProjectResult{}, fmt.Errorf("read opened Project: %w: Episode %q belongs to another Project", ErrProjectDataInvalid, episode.ID)
		}
		if _, duplicate := sequences[episode.SequenceNumber]; duplicate {
			return ProjectResult{}, fmt.Errorf("read opened Project: %w: duplicate Episode sequence %d", ErrProjectDataInvalid, episode.SequenceNumber)
		}
		sequences[episode.SequenceNumber] = struct{}{}
	}
	return ProjectResult{Project: project, Episodes: episodes}, nil
}

func (s *Service) cleanupFailedCreation(paths ProjectPaths, store ProjectStore, cause error) error {
	errorsToJoin := []error{ErrProjectCreateFailed, cause}
	if store != nil {
		if err := store.Close(); err != nil {
			errorsToJoin = append(errorsToJoin, fmt.Errorf("close failed Project Store: %w", err))
		}
	}
	if err := s.workspace.CleanupCreated(paths); err != nil {
		errorsToJoin = append(errorsToJoin, fmt.Errorf("clean up failed Project directory: %w", err))
	}
	return fmt.Errorf("create Project: %w", errors.Join(errorsToJoin...))
}

func registrationFailure(category error, rootPath string, store ProjectStore, cause error) error {
	if closeErr := store.Close(); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close Project Store: %w", closeErr))
	}
	return &RegistrationError{Category: category, RootPath: rootPath, Cause: cause}
}

func libraryRecord(rootPath string, result ProjectResult, openedAt time.Time) ProjectLibraryRecord {
	return ProjectLibraryRecord{
		ProjectID:        result.Project.ID,
		RootPath:         rootPath,
		DisplayName:      result.Project.Name,
		Width:            result.Project.Output.Width(),
		Height:           result.Project.Output.Height(),
		EpisodeCount:     len(result.Episodes),
		ProjectUpdatedAt: result.Project.UpdatedAt,
		OpenedAt:         openedAt,
	}
}

func validOperationTime(value time.Time) (time.Time, error) {
	if value.IsZero() || value.Location() != time.UTC || value.UnixMilli() < 0 ||
		value.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("Clock must return a non-zero UTC timestamp with millisecond precision")
	}
	return value, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

func validateRootPathInput(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: root path is required", ErrInvalidRequest)
	}
	return nil
}
