package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrProjectNotInitialized     = errors.New("project store is not initialized")
	ErrProjectAlreadyInitialized = errors.New("project store is already initialized")
	ErrProjectRevisionConflict   = errors.New("project revision conflict")
	ErrEpisodeNotFound           = errors.New("episode not found")
	ErrInvalidStoredProjectData  = errors.New("invalid stored project data")
)

// UpdateProject persists editable project metadata under optimistic
// concurrency control. Output settings and the project identity are immutable.
func (s *Store) UpdateProject(ctx context.Context, project domain.Project, expectedRevision int64) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("update Project: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("update Project: %w", err)
	}
	if err := project.Validate(); err != nil {
		return fmt.Errorf("update Project: %w", err)
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || project.Revision != expectedRevision+1 {
		return errors.New("update Project: expected and next revisions are invalid")
	}
	if err := validateMutationTime(project.UpdatedAt); err != nil {
		return fmt.Errorf("update Project: %w", err)
	}
	result, err := database.ExecContext(ctx, `
UPDATE projects
SET name = ?, description = ?, revision = ?, updated_at_ms = max(updated_at_ms, ?)
WHERE singleton_key = 1 AND project_id = ? AND revision = ?
`, project.Name, project.Description, project.Revision, project.UpdatedAt.UnixMilli(), project.ID.String(), expectedRevision)
	if err != nil {
		return fmt.Errorf("update Project: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update Project: inspect result: %w", err)
	}
	if changed != 1 {
		return ErrProjectRevisionConflict
	}
	return nil
}

// InitializeProject atomically writes the Project record without implicitly
// creating creative content. A newly created Project may contain zero Episodes.
func (s *Store) InitializeProject(ctx context.Context, project domain.Project) error {
	return s.initialize(ctx, project, nil)
}

// Initialize remains available to persistence fixtures and import paths that
// deliberately create a Project and its first Episode atomically.
func (s *Store) Initialize(ctx context.Context, project domain.Project, defaultEpisode domain.Episode) error {
	return s.initialize(ctx, project, &defaultEpisode)
}

func (s *Store) initialize(ctx context.Context, project domain.Project, defaultEpisode *domain.Episode) (returnErr error) {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("initialize project store: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("initialize project store: %w", err)
	}
	if err := project.Validate(); err != nil {
		return fmt.Errorf("initialize project store: %w", err)
	}
	if defaultEpisode != nil {
		if err := defaultEpisode.Validate(); err != nil {
			return fmt.Errorf("initialize project store: %w", err)
		}
		if defaultEpisode.ProjectID != project.ID {
			return fmt.Errorf("initialize project store: default Episode belongs to a different Project")
		}
		if defaultEpisode.SequenceNumber != 1 {
			return fmt.Errorf("initialize project store: default Episode sequence number must be 1")
		}
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("initialize project store: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, fmt.Errorf("initialize project store: roll back: %w", rollbackErr))
		}
	}()

	var existing int
	if err := transaction.QueryRowContext(ctx, "SELECT count(*) FROM projects").Scan(&existing); err != nil {
		return fmt.Errorf("initialize project store: inspect existing Project: %w", err)
	}
	if existing != 0 {
		return ErrProjectAlreadyInitialized
	}

	if _, err := transaction.ExecContext(ctx, `
INSERT INTO projects (
    project_id, singleton_key, name, description, output_width, output_height,
    revision, created_at_ms, updated_at_ms
) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)
`,
		project.ID.String(),
		project.Name,
		project.Description,
		project.Output.Width(),
		project.Output.Height(),
		project.Revision,
		project.CreatedAt.UnixMilli(),
		project.UpdatedAt.UnixMilli(),
	); err != nil {
		return fmt.Errorf("initialize project store: insert Project: %w", err)
	}

	if defaultEpisode != nil {
		if err := insertEpisode(ctx, transaction, *defaultEpisode); err != nil {
			return fmt.Errorf("initialize project store: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("initialize project store: commit: %w", err)
	}
	committed = true
	return nil
}

// Project returns the store's singleton Project metadata.
func (s *Store) Project(ctx context.Context) (domain.Project, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.Project{}, fmt.Errorf("read Project: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("read Project: %w", err)
	}

	project, err := scanProject(database.QueryRowContext(ctx, `
SELECT project_id, name, description, output_width, output_height,
       revision, created_at_ms, updated_at_ms
FROM projects
WHERE singleton_key = 1
`))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, ErrProjectNotInitialized
	}
	if err != nil {
		return domain.Project{}, fmt.Errorf("read Project: %w", err)
	}
	return project, nil
}

// Episodes returns lightweight Episode summaries in sequence order. It includes
// script freshness but does not load script Markdown.
func (s *Store) Episodes(ctx context.Context) ([]domain.EpisodeSummary, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Episodes: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("list Episodes: %w", err)
	}

	rows, err := database.QueryContext(ctx, `
SELECT episode_id, project_id, sequence_number, title, target_duration_ms,
       revision, created_at_ms, updated_at_ms, deleted_at_ms,
       script_revision, script_updated_at_ms
FROM episodes
WHERE deleted_at_ms IS NULL
ORDER BY sequence_number ASC, episode_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list Episodes: query: %w", err)
	}
	defer rows.Close()

	episodes := make([]domain.EpisodeSummary, 0)
	for rows.Next() {
		episode, err := scanEpisodeSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("list Episodes: %w", err)
		}
		episodes = append(episodes, episode)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Episodes: iterate: %w", err)
	}
	return episodes, nil
}

// DeletedEpisodes returns Episode summaries currently held in the project trash.
func (s *Store) DeletedEpisodes(ctx context.Context) ([]domain.EpisodeSummary, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list deleted Episodes: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("list deleted Episodes: %w", err)
	}
	rows, err := database.QueryContext(ctx, `
SELECT episode_id, project_id, sequence_number, title, target_duration_ms,
       revision, created_at_ms, updated_at_ms, deleted_at_ms,
       script_revision, script_updated_at_ms
FROM episodes
WHERE deleted_at_ms IS NOT NULL
ORDER BY deleted_at_ms DESC, sequence_number ASC, episode_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list deleted Episodes: query: %w", err)
	}
	defer rows.Close()
	values := make([]domain.EpisodeSummary, 0)
	for rows.Next() {
		value, scanErr := scanEpisodeSummary(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list deleted Episodes: %w", scanErr)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deleted Episodes: iterate: %w", err)
	}
	return values, nil
}

// Episode returns one Episode with its script Markdown.
func (s *Store) Episode(ctx context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	return s.episode(ctx, episodeID, false)
}

func (s *Store) EpisodeIncludingDeleted(ctx context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	return s.episode(ctx, episodeID, true)
}

func (s *Store) episode(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) (domain.Episode, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.Episode{}, fmt.Errorf("read Episode: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.Episode{}, fmt.Errorf("read Episode: %w", err)
	}
	if err := episodeID.Validate(); err != nil {
		return domain.Episode{}, fmt.Errorf("read Episode: %w", err)
	}

	query := `
SELECT episode_id, project_id, sequence_number, title, target_duration_ms,
       revision, created_at_ms, updated_at_ms, deleted_at_ms,
       script_markdown, script_revision, script_updated_at_ms
FROM episodes
WHERE episode_id = ?`
	if !includeDeleted {
		query += " AND deleted_at_ms IS NULL"
	}
	episode, err := scanEpisode(database.QueryRowContext(ctx, query, episodeID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Episode{}, fmt.Errorf("read Episode %q: %w", episodeID, ErrEpisodeNotFound)
	}
	if err != nil {
		return domain.Episode{}, fmt.Errorf("read Episode %q: %w", episodeID, err)
	}
	return episode, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func insertEpisode(ctx context.Context, transaction *sql.Tx, episode domain.Episode) error {
	_, err := transaction.ExecContext(ctx, `
INSERT INTO episodes (
    episode_id, project_id, sequence_number, title, target_duration_ms,
    script_markdown, script_revision, script_updated_at_ms,
    revision, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		episode.ID.String(),
		episode.ProjectID.String(),
		episode.SequenceNumber,
		episode.Title,
		episode.TargetDuration.Milliseconds(),
		episode.Script.Markdown,
		episode.Script.Revision,
		episode.Script.UpdatedAt.UnixMilli(),
		episode.Revision,
		episode.CreatedAt.UnixMilli(),
		episode.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert Episode: %w", err)
	}
	return nil
}

func scanProject(row rowScanner) (domain.Project, error) {
	var projectID string
	var name string
	var description string
	var width int
	var height int
	var revision int64
	var createdAtMS int64
	var updatedAtMS int64
	if err := row.Scan(
		&projectID,
		&name,
		&description,
		&width,
		&height,
		&revision,
		&createdAtMS,
		&updatedAtMS,
	); err != nil {
		return domain.Project{}, err
	}

	id, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.Project{}, invalidStoredData("Project ID", err)
	}
	output, err := domain.NewOutputSpec(width, height)
	if err != nil {
		return domain.Project{}, invalidStoredData("Project output", err)
	}
	project := domain.Project{
		ID:          id,
		Name:        name,
		Description: description,
		Output:      output,
		Revision:    revision,
		CreatedAt:   time.UnixMilli(createdAtMS).UTC(),
		UpdatedAt:   time.UnixMilli(updatedAtMS).UTC(),
	}
	if err := project.Validate(); err != nil {
		return domain.Project{}, invalidStoredData("Project", err)
	}
	return project, nil
}

func scanEpisodeSummary(row rowScanner) (domain.EpisodeSummary, error) {
	var episodeID string
	var projectID string
	var sequenceNumber int
	var title string
	var targetDurationMS int64
	var revision int64
	var createdAtMS int64
	var updatedAtMS int64
	var deletedAtMS sql.NullInt64
	var scriptRevision int64
	var scriptUpdatedAtMS int64
	if err := row.Scan(
		&episodeID,
		&projectID,
		&sequenceNumber,
		&title,
		&targetDurationMS,
		&revision,
		&createdAtMS,
		&updatedAtMS,
		&deletedAtMS,
		&scriptRevision,
		&scriptUpdatedAtMS,
	); err != nil {
		return domain.EpisodeSummary{}, err
	}
	metadata, err := buildEpisodeMetadata(
		episodeID,
		projectID,
		sequenceNumber,
		title,
		targetDurationMS,
		revision,
		createdAtMS,
		updatedAtMS,
		deletedAtMS,
	)
	if err != nil {
		return domain.EpisodeSummary{}, err
	}
	summary := domain.EpisodeSummary{
		EpisodeMetadata: metadata,
		ScriptRevision:  scriptRevision,
		ScriptUpdatedAt: time.UnixMilli(scriptUpdatedAtMS).UTC(),
	}
	if err := summary.Validate(); err != nil {
		return domain.EpisodeSummary{}, invalidStoredData("Episode summary", err)
	}
	return summary, nil
}

func buildEpisodeMetadata(
	episodeID string,
	projectID string,
	sequenceNumber int,
	title string,
	targetDurationMS int64,
	revision int64,
	createdAtMS int64,
	updatedAtMS int64,
	deletedAtMS sql.NullInt64,
) (domain.EpisodeMetadata, error) {
	id, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.EpisodeMetadata{}, invalidStoredData("Episode ID", err)
	}
	ownerID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.EpisodeMetadata{}, invalidStoredData("Episode Project ID", err)
	}
	if targetDurationMS > math.MaxInt64/int64(time.Millisecond) {
		return domain.EpisodeMetadata{}, invalidStoredData("Episode target duration", errors.New("milliseconds overflow time.Duration"))
	}
	metadata := domain.EpisodeMetadata{
		ID:             id,
		ProjectID:      ownerID,
		SequenceNumber: sequenceNumber,
		Title:          title,
		TargetDuration: time.Duration(targetDurationMS) * time.Millisecond,
		Revision:       revision,
		CreatedAt:      time.UnixMilli(createdAtMS).UTC(),
		UpdatedAt:      time.UnixMilli(updatedAtMS).UTC(),
	}
	if deletedAtMS.Valid {
		deletedAt := time.UnixMilli(deletedAtMS.Int64).UTC()
		metadata.DeletedAt = &deletedAt
	}
	if err := metadata.Validate(); err != nil {
		return domain.EpisodeMetadata{}, invalidStoredData("Episode metadata", err)
	}
	return metadata, nil
}

func scanEpisode(row rowScanner) (domain.Episode, error) {
	var episodeID string
	var projectID string
	var sequenceNumber int
	var title string
	var targetDurationMS int64
	var revision int64
	var createdAtMS int64
	var updatedAtMS int64
	var deletedAtMS sql.NullInt64
	var scriptMarkdown string
	var scriptRevision int64
	var scriptUpdatedAtMS int64
	if err := row.Scan(
		&episodeID,
		&projectID,
		&sequenceNumber,
		&title,
		&targetDurationMS,
		&revision,
		&createdAtMS,
		&updatedAtMS,
		&deletedAtMS,
		&scriptMarkdown,
		&scriptRevision,
		&scriptUpdatedAtMS,
	); err != nil {
		return domain.Episode{}, err
	}

	metadata, err := buildEpisodeMetadata(
		episodeID,
		projectID,
		sequenceNumber,
		title,
		targetDurationMS,
		revision,
		createdAtMS,
		updatedAtMS,
		deletedAtMS,
	)
	if err != nil {
		return domain.Episode{}, err
	}
	episode := domain.Episode{
		EpisodeMetadata: metadata,
		Script: domain.Script{
			Markdown:  scriptMarkdown,
			Revision:  scriptRevision,
			UpdatedAt: time.UnixMilli(scriptUpdatedAtMS).UTC(),
		},
	}
	if err := episode.Validate(); err != nil {
		return domain.Episode{}, invalidStoredData("Episode", err)
	}
	return episode, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

func invalidStoredData(label string, cause error) error {
	return fmt.Errorf("%w: %s: %w", ErrInvalidStoredProjectData, label, cause)
}
