package projectapp

import (
	"context"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// GetEpisode returns one Episode including its current script Markdown.
func (s *Service) GetEpisode(ctx context.Context, input GetEpisodeInput) (domain.Episode, error) {
	if err := validateContext(ctx); err != nil {
		return domain.Episode{}, fmt.Errorf("get Episode: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return domain.Episode{}, fmt.Errorf("get Episode: %w", err)
	}
	if err := input.EpisodeID.Validate(); err != nil {
		return domain.Episode{}, fmt.Errorf("get Episode: %w: %w", ErrInvalidRequest, err)
	}
	episode, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.Episode{}, fmt.Errorf("get Episode: %w", err)
	}
	return episode, nil
}

// CreateEpisode appends an empty Episode with user-selected metadata. It does
// not copy assets, Storyboard, or Sessions.
func (s *Service) CreateEpisode(ctx context.Context, input CreateEpisodeInput) (EpisodeResult, error) {
	if err := validateContext(ctx); err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w", err)
	}
	if !validEpisodeTargetDuration(input.TargetDuration) {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: target duration must be between %s and %s", ErrInvalidRequest, domain.MinEpisodeTargetDuration, domain.MaxEpisodeTargetDuration)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w", err)
	}
	current, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w", err)
	}
	if current.Project.ID != input.ProjectID {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: open Store belongs to another Project", ErrProjectDataInvalid)
	}
	latestSequence := 0
	for _, episode := range current.Episodes {
		if episode.SequenceNumber > latestSequence {
			latestSequence = episode.SequenceNumber
		}
	}
	deleted, err := handle.Store.DeletedEpisodes(ctx)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w", err)
	}
	for _, episode := range deleted {
		if episode.SequenceNumber > latestSequence {
			latestSequence = episode.SequenceNumber
		}
	}
	if latestSequence == math.MaxInt {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: Episode sequence is exhausted", ErrProjectWriteFailed)
	}
	episodeID, err := s.ids.NewEpisodeID()
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: %w", ErrProjectWriteFailed, err)
	}
	sequence := latestSequence + 1
	title := input.Title
	if title == "" {
		title = fmt.Sprintf("视频 %d", sequence)
	}
	episode := domain.Episode{
		EpisodeMetadata: domain.EpisodeMetadata{
			ID:             episodeID,
			ProjectID:      input.ProjectID,
			SequenceNumber: sequence,
			Title:          title,
			TargetDuration: input.TargetDuration,
			Revision:       initialRevision,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Script: domain.Script{
			Markdown:  "",
			Revision:  initialRevision,
			UpdatedAt: now,
		},
	}
	if err := episode.Validate(); err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.CreateEpisode(ctx, episode, now); err != nil {
		return EpisodeResult{}, fmt.Errorf("create Episode: %w", err)
	}
	return s.finishEpisodeMutation(ctx, handle, episode.ID, now)
}

// UpdateEpisode changes the user-editable Episode metadata under optimistic
// concurrency control. It never modifies the script revision or contents.
func (s *Service) UpdateEpisode(ctx context.Context, input UpdateEpisodeInput) (EpisodeResult, error) {
	if err := validateContext(ctx); err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w", err)
	}
	if err := input.EpisodeID.Validate(); err != nil || input.ExpectedRevision < 1 || !validEpisodeTargetDuration(input.TargetDuration) {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w", ErrInvalidRequest)
	}
	current, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w", err)
	}
	if current.Revision != input.ExpectedRevision {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w: expected %d, current %d", ErrRevisionConflict, input.ExpectedRevision, current.Revision)
	}
	if current.Revision == math.MaxInt64 {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w: revision is exhausted", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w: %w", ErrProjectWriteFailed, err)
	}
	metadata := current.EpisodeMetadata
	metadata.Title = input.Title
	metadata.TargetDuration = input.TargetDuration
	metadata.Revision++
	metadata.UpdatedAt = now
	if err := metadata.Validate(); err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateEpisode(ctx, metadata, input.ExpectedRevision, now); err != nil {
		return EpisodeResult{}, fmt.Errorf("update Episode: %w", err)
	}
	return s.finishEpisodeMutation(ctx, handle, input.EpisodeID, now)
}

func validEpisodeTargetDuration(value time.Duration) bool {
	return value >= domain.MinEpisodeTargetDuration && value <= domain.MaxEpisodeTargetDuration && value%time.Millisecond == 0
}

// DeleteEpisode moves an entire Episode into the project trash without
// modifying any of its owned script, asset, storyboard, media, or Agent data.
func (s *Service) DeleteEpisode(ctx context.Context, input ChangeEpisodeDeletionInput) (ProjectResult, error) {
	if err := validateContext(ctx); err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	if err := input.EpisodeID.Validate(); err != nil || input.ExpectedRevision < 1 {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", ErrInvalidRequest)
	}
	current, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	if current.Revision != input.ExpectedRevision {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", ErrRevisionConflict)
	}
	if current.Revision == math.MaxInt64 {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", ErrProjectWriteFailed)
	}
	if err := s.library.CanDeleteEpisode(ctx, input.ProjectID, input.EpisodeID); err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	metadata := current.EpisodeMetadata
	metadata.Revision++
	metadata.UpdatedAt = now
	metadata.DeletedAt = &now
	if err := handle.Store.DeleteEpisode(ctx, metadata, input.ExpectedRevision, now); err != nil {
		return ProjectResult{}, fmt.Errorf("delete Episode: %w", err)
	}
	return s.finishProjectMutation(ctx, handle, now)
}

// RestoreEpisode restores an Episode and all of its still-owned content from
// the project trash.
func (s *Service) RestoreEpisode(ctx context.Context, input ChangeEpisodeDeletionInput) (EpisodeResult, error) {
	if err := validateContext(ctx); err != nil {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", err)
	}
	if err := input.EpisodeID.Validate(); err != nil || input.ExpectedRevision < 1 {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", ErrInvalidRequest)
	}
	current, err := handle.Store.EpisodeIncludingDeleted(ctx, input.EpisodeID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", err)
	}
	if current.ProjectID != input.ProjectID {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", ErrProjectDataInvalid)
	}
	if !current.IsDeleted() {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", ErrEpisodeNotDeleted)
	}
	if current.Revision != input.ExpectedRevision {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", ErrRevisionConflict)
	}
	if current.Revision == math.MaxInt64 {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", err)
	}
	metadata := current.EpisodeMetadata
	metadata.Revision++
	metadata.UpdatedAt = now
	metadata.DeletedAt = nil
	if err := handle.Store.RestoreEpisode(ctx, metadata, input.ExpectedRevision, now); err != nil {
		return EpisodeResult{}, fmt.Errorf("restore Episode: %w", err)
	}
	return s.finishEpisodeMutation(ctx, handle, input.EpisodeID, now)
}

// SaveScript replaces the current script after normalizing platform line
// endings. Script and Episode metadata revisions remain independent.
func (s *Service) SaveScript(ctx context.Context, input SaveScriptInput) (EpisodeResult, error) {
	if err := validateContext(ctx); err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w", err)
	}
	handle, err := s.openProjectHandle(input.ProjectID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w", err)
	}
	if err := input.EpisodeID.Validate(); err != nil || input.ExpectedScriptRevision < 1 {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w", ErrInvalidRequest)
	}
	markdown := domain.NormalizeMarkdown(input.Markdown)
	characterCount := domain.ScriptCharacterCount(markdown)
	if characterCount > domain.MaxScriptCharacters {
		return EpisodeResult{}, fmt.Errorf(
			"save Episode script: %w: got %d characters, maximum is %d",
			ErrScriptTooLong,
			characterCount,
			domain.MaxScriptCharacters,
		)
	}
	if err := domain.ValidateScriptMarkdown(markdown); err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w: %w", ErrInvalidRequest, err)
	}
	current, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w", err)
	}
	if current.Script.Revision != input.ExpectedScriptRevision {
		return EpisodeResult{}, fmt.Errorf(
			"save Episode script: %w: expected %d, current %d",
			ErrRevisionConflict,
			input.ExpectedScriptRevision,
			current.Script.Revision,
		)
	}
	if current.Script.Revision == math.MaxInt64 {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w: revision is exhausted", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w: %w", ErrProjectWriteFailed, err)
	}
	script := domain.Script{
		Markdown:  markdown,
		Revision:  current.Script.Revision + 1,
		UpdatedAt: now,
	}
	if err := script.Validate(); err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.SaveScript(ctx, input.EpisodeID, script, input.ExpectedScriptRevision, now); err != nil {
		return EpisodeResult{}, fmt.Errorf("save Episode script: %w", err)
	}
	return s.finishEpisodeMutation(ctx, handle, input.EpisodeID, now)
}

func (s *Service) openProjectHandle(projectID domain.ProjectID) (ProjectHandle, error) {
	if err := projectID.Validate(); err != nil {
		return ProjectHandle{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	handle, ok := s.registry.ByProjectID(projectID)
	if !ok {
		return ProjectHandle{}, ErrProjectStoreNotOpen
	}
	return handle, nil
}

func episodeForProject(
	ctx context.Context,
	store ProjectStore,
	projectID domain.ProjectID,
	episodeID domain.EpisodeID,
) (domain.Episode, error) {
	episode, err := store.Episode(ctx, episodeID)
	if err != nil {
		return domain.Episode{}, err
	}
	if err := episode.Validate(); err != nil {
		return domain.Episode{}, fmt.Errorf("%w: %w", ErrProjectDataInvalid, err)
	}
	if episode.ProjectID != projectID {
		return domain.Episode{}, fmt.Errorf("%w: Episode %q belongs to another Project", ErrProjectDataInvalid, episode.ID)
	}
	return episode, nil
}

func (s *Service) finishEpisodeMutation(
	ctx context.Context,
	handle ProjectHandle,
	episodeID domain.EpisodeID,
	operationTime time.Time,
) (EpisodeResult, error) {
	projectResult, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return EpisodeResult{}, err
	}
	if projectResult.Project.ID != handle.ProjectID {
		return EpisodeResult{}, fmt.Errorf("%w: open Store belongs to another Project", ErrProjectDataInvalid)
	}
	episode, err := episodeForProject(ctx, handle.Store, projectResult.Project.ID, episodeID)
	if err != nil {
		return EpisodeResult{}, err
	}
	result := EpisodeResult{
		Project:  projectResult.Project,
		Episode:  episode,
		Episodes: projectResult.Episodes,
	}
	if err := s.library.SyncProject(ctx, libraryRecord(handle.RootPath, projectResult, operationTime)); err != nil {
		return result, &RegistrationError{
			Category: ErrProjectUpdatedNotRegistered,
			RootPath: handle.RootPath,
			Cause:    err,
		}
	}
	return result, nil
}

func (s *Service) finishProjectMutation(ctx context.Context, handle ProjectHandle, operationTime time.Time) (ProjectResult, error) {
	result, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return ProjectResult{}, err
	}
	if err := s.library.SyncProject(ctx, libraryRecord(handle.RootPath, result, operationTime)); err != nil {
		return result, &RegistrationError{Category: ErrProjectUpdatedNotRegistered, RootPath: handle.RootPath, Cause: err}
	}
	return result, nil
}
