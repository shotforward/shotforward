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
	ErrEpisodeSequenceConflict    = errors.New("episode sequence conflict")
	ErrEpisodeRevisionConflict    = errors.New("episode revision conflict")
	ErrScriptRevisionConflict     = errors.New("script revision conflict")
	ErrEpisodeNotDeleted          = errors.New("episode is not deleted")
	ErrEpisodeHasActiveGeneration = errors.New("episode has active generation jobs")
	ErrEpisodeHasActiveExport     = errors.New("episode has active export jobs")
)

// CreateEpisode appends one fully validated Episode and updates the Project's
// freshness timestamp in the same transaction.
func (s *Store) CreateEpisode(ctx context.Context, episode domain.Episode, projectUpdatedAt time.Time) error {
	if err := episode.Validate(); err != nil {
		return fmt.Errorf("create Episode: %w", err)
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil {
		return fmt.Errorf("create Episode: %w", err)
	}
	return s.withTransaction(ctx, "create Episode", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		if projectID != episode.ProjectID {
			return errors.New("Episode belongs to another Project")
		}

		var latestSequence int
		if err := transaction.QueryRowContext(ctx, `
SELECT coalesce(max(sequence_number), 0)
FROM episodes
WHERE project_id = ?
`, projectID.String()).Scan(&latestSequence); err != nil {
			return fmt.Errorf("read latest Episode: %w", err)
		}
		if episode.SequenceNumber != latestSequence+1 {
			return fmt.Errorf("%w: expected sequence %d, got %d", ErrEpisodeSequenceConflict, latestSequence+1, episode.SequenceNumber)
		}
		if err := insertEpisode(ctx, transaction, episode); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

func (s *Store) DeleteEpisode(ctx context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, projectUpdatedAt time.Time) error {
	if err := metadata.Validate(); err != nil {
		return fmt.Errorf("delete Episode: %w", err)
	}
	if metadata.DeletedAt == nil || expectedRevision < 1 || metadata.Revision != expectedRevision+1 {
		return errors.New("delete Episode: invalid lifecycle revision")
	}
	if metadata.UpdatedAt != projectUpdatedAt || !metadata.DeletedAt.Equal(projectUpdatedAt) {
		return errors.New("delete Episode: timestamps must match")
	}
	return s.withTransaction(ctx, "delete Episode", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		if projectID != metadata.ProjectID {
			return errors.New("Episode belongs to another Project")
		}
		var activeGeneration int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM generation_jobs WHERE episode_id=? AND state IN ('queued','running','stop_requested')`, metadata.ID.String()).Scan(&activeGeneration); err != nil {
			return fmt.Errorf("inspect active GenerationJobs: %w", err)
		}
		if activeGeneration != 0 {
			return ErrEpisodeHasActiveGeneration
		}
		var activeExport int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM export_jobs WHERE episode_id=? AND state IN ('queued','running','cancel_requested')`, metadata.ID.String()).Scan(&activeExport); err != nil {
			return fmt.Errorf("inspect active ExportJobs: %w", err)
		}
		if activeExport != 0 {
			return ErrEpisodeHasActiveExport
		}
		result, err := transaction.ExecContext(ctx, `UPDATE episodes SET revision=?, updated_at_ms=max(updated_at_ms,?), deleted_at_ms=? WHERE episode_id=? AND project_id=? AND revision=? AND deleted_at_ms IS NULL`, metadata.Revision, metadata.UpdatedAt.UnixMilli(), metadata.DeletedAt.UnixMilli(), metadata.ID.String(), metadata.ProjectID.String(), expectedRevision)
		if err != nil {
			return fmt.Errorf("mark Episode deleted: %w", err)
		}
		if err := requireChangedEpisode(ctx, transaction, result, metadata.ID, ErrEpisodeRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

func (s *Store) RestoreEpisode(ctx context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, projectUpdatedAt time.Time) error {
	if err := metadata.Validate(); err != nil {
		return fmt.Errorf("restore Episode: %w", err)
	}
	if metadata.DeletedAt != nil || expectedRevision < 1 || metadata.Revision != expectedRevision+1 {
		return errors.New("restore Episode: invalid lifecycle revision")
	}
	if metadata.UpdatedAt != projectUpdatedAt {
		return errors.New("restore Episode: timestamps must match")
	}
	return s.withTransaction(ctx, "restore Episode", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `UPDATE episodes SET revision=?, updated_at_ms=max(updated_at_ms,?), deleted_at_ms=NULL WHERE episode_id=? AND project_id=? AND revision=? AND deleted_at_ms IS NOT NULL`, metadata.Revision, metadata.UpdatedAt.UnixMilli(), metadata.ID.String(), metadata.ProjectID.String(), expectedRevision)
		if err != nil {
			return fmt.Errorf("restore Episode: %w", err)
		}
		if err := requireChangedEpisode(ctx, transaction, result, metadata.ID, ErrEpisodeRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

// UpdateEpisode changes only editable Episode metadata. Script fields keep
// their independent revision and contents.
func (s *Store) UpdateEpisode(
	ctx context.Context,
	metadata domain.EpisodeMetadata,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	if err := metadata.Validate(); err != nil {
		return fmt.Errorf("update Episode: %w", err)
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || metadata.Revision != expectedRevision+1 {
		return fmt.Errorf("update Episode: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil {
		return fmt.Errorf("update Episode: %w", err)
	}
	if metadata.UpdatedAt != projectUpdatedAt {
		return errors.New("update Episode: Episode and Project timestamps must match")
	}
	return s.withTransaction(ctx, "update Episode", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		if projectID != metadata.ProjectID {
			return errors.New("Episode belongs to another Project")
		}

		result, err := transaction.ExecContext(ctx, `
UPDATE episodes
SET title = ?, target_duration_ms = ?, revision = ?,
    updated_at_ms = max(updated_at_ms, ?)
WHERE episode_id = ? AND project_id = ? AND revision = ?
  AND deleted_at_ms IS NULL
`,
			metadata.Title,
			metadata.TargetDuration.Milliseconds(),
			metadata.Revision,
			metadata.UpdatedAt.UnixMilli(),
			metadata.ID.String(),
			metadata.ProjectID.String(),
			expectedRevision,
		)
		if err != nil {
			return fmt.Errorf("write Episode metadata: %w", err)
		}
		if err := requireChangedEpisode(ctx, transaction, result, metadata.ID, ErrEpisodeRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

// SaveScript replaces the Episode's current Markdown and advances only the
// independent script revision.
func (s *Store) SaveScript(
	ctx context.Context,
	episodeID domain.EpisodeID,
	script domain.Script,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	if err := episodeID.Validate(); err != nil {
		return fmt.Errorf("save Episode script: %w", err)
	}
	if err := script.Validate(); err != nil {
		return fmt.Errorf("save Episode script: %w", err)
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || script.Revision != expectedRevision+1 {
		return fmt.Errorf("save Episode script: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil {
		return fmt.Errorf("save Episode script: %w", err)
	}
	if script.UpdatedAt != projectUpdatedAt {
		return errors.New("save Episode script: Script and Project timestamps must match")
	}
	return s.withTransaction(ctx, "save Episode script", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE episodes
SET script_markdown = ?, script_revision = ?, script_updated_at_ms = ?,
    updated_at_ms = max(updated_at_ms, ?)
WHERE episode_id = ? AND project_id = ? AND script_revision = ?
  AND deleted_at_ms IS NULL
`,
			script.Markdown,
			script.Revision,
			script.UpdatedAt.UnixMilli(),
			script.UpdatedAt.UnixMilli(),
			episodeID.String(),
			projectID.String(),
			expectedRevision,
		)
		if err != nil {
			return fmt.Errorf("write Episode script: %w", err)
		}
		if err := requireChangedEpisode(ctx, transaction, result, episodeID, ErrScriptRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, transaction, "episode_script", episodeID.String(), script.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

func (s *Store) withTransaction(ctx context.Context, operation string, action func(*sql.Tx) error) (returnErr error) {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin transaction: %w", operation, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, fmt.Errorf("%s: roll back: %w", operation, rollbackErr))
		}
	}()
	if err := action(transaction); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", operation, err)
	}
	committed = true
	return nil
}

func transactionProjectID(ctx context.Context, transaction *sql.Tx) (domain.ProjectID, error) {
	var value string
	if err := transaction.QueryRowContext(ctx, "SELECT project_id FROM projects WHERE singleton_key = 1").Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrProjectNotInitialized
		}
		return "", fmt.Errorf("read Project identity: %w", err)
	}
	projectID, err := domain.ParseProjectID(value)
	if err != nil {
		return "", invalidStoredData("Project ID", err)
	}
	return projectID, nil
}

func requireChangedEpisode(
	ctx context.Context,
	transaction *sql.Tx,
	result sql.Result,
	episodeID domain.EpisodeID,
	conflict error,
) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected Episode count: %w", err)
	}
	if changed == 1 {
		return nil
	}
	if changed != 0 {
		return fmt.Errorf("unexpected affected Episode count %d", changed)
	}
	var exists int
	if err := transaction.QueryRowContext(ctx, "SELECT 1 FROM episodes WHERE episode_id = ?", episodeID.String()).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEpisodeNotFound
		}
		return fmt.Errorf("inspect unchanged Episode: %w", err)
	}
	return conflict
}

func updateProjectTimestamp(ctx context.Context, transaction *sql.Tx, projectID domain.ProjectID, updatedAt time.Time) error {
	result, err := transaction.ExecContext(ctx, `
UPDATE projects
SET updated_at_ms = max(updated_at_ms, ?)
WHERE project_id = ? AND singleton_key = 1
`, updatedAt.UnixMilli(), projectID.String())
	if err != nil {
		return fmt.Errorf("update Project timestamp: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected Project count: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("update Project timestamp: expected one Project, changed %d", changed)
	}
	return nil
}

func validateMutationTime(value time.Time) error {
	if value.IsZero() || value.Location() != time.UTC || value.UnixMilli() < 0 ||
		value.Nanosecond()%int(time.Millisecond) != 0 {
		return errors.New("timestamp must be non-zero UTC with millisecond precision")
	}
	return nil
}
