package projectstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrExportJobNotFound      = errors.New("export job not found")
	ErrExportJobStateConflict = errors.New("export job state conflict")
	ErrExportJobConflict      = errors.New("export job operation or episode conflict")
)

const exportJobColumns = `SELECT export_job_id,project_id,episode_id,operation_key,state,snapshot_json,storyboard_revision,
progress_stage,progress_message,output_relative_path,failure_code,failure_message,revision,created_at_ms,updated_at_ms,
started_at_ms,finished_at_ms FROM export_jobs`

func (s *Store) CreateExportJob(ctx context.Context, job domain.ExportJob) error {
	if err := validateContext(ctx); err != nil || job.Validate() != nil || job.State != domain.ExportJobQueued || job.Revision != 1 {
		return domain.ErrInvalidExportJob
	}
	snapshot, err := json.Marshal(job.Snapshot)
	if err != nil {
		return domain.ErrInvalidExportJob
	}
	err = s.withTransaction(ctx, "create ExportJob", func(tx *sql.Tx) error {
		if err := validateExportSnapshotReferences(ctx, tx, job); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM export_jobs WHERE project_id=? AND operation_key=?)`, job.ProjectID.String(), job.OperationKey).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrExportJobConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM export_jobs WHERE episode_id=? AND state IN ('queued','running','cancel_requested'))`, job.EpisodeID.String()).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrExportJobConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO export_jobs (
export_job_id,project_id,episode_id,operation_key,state,snapshot_json,storyboard_revision,progress_stage,progress_message,
output_relative_path,failure_code,failure_message,revision,created_at_ms,updated_at_ms,started_at_ms,finished_at_ms)
VALUES (?,?,?,?,?,?,?,?,?,'','','',?,?,?,NULL,NULL)`, job.ID.String(), job.ProjectID.String(), job.EpisodeID.String(), job.OperationKey, string(job.State), string(snapshot), job.Snapshot.StoryboardRevision, job.ProgressStage, job.ProgressMessage, job.Revision, job.CreatedAt.UnixMilli(), job.UpdatedAt.UnixMilli())
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return ErrExportJobConflict
		}
		return err
	})
	return err
}

func validateExportSnapshotReferences(ctx context.Context, tx *sql.Tx, job domain.ExportJob) error {
	var projectID string
	var storyboardRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT project_id,storyboard_revision FROM episodes WHERE episode_id=?`, job.EpisodeID.String()).Scan(&projectID, &storyboardRevision); err != nil {
		return err
	}
	if projectID != job.ProjectID.String() || storyboardRevision != job.Snapshot.StoryboardRevision {
		return domain.ErrInvalidExportSnapshot
	}
	type position struct{ scene, clip string }
	expected := make(map[int]position, len(job.Snapshot.Clips)+len(job.Snapshot.SkippedClips))
	for _, item := range job.Snapshot.Clips {
		expected[item.Position] = position{item.SceneID.String(), item.ClipID.String()}
		slot, err := transactionMediaSlot(ctx, tx, item.MediaSlotID)
		if err != nil || slot.ProjectID != job.ProjectID || slot.EpisodeID != job.EpisodeID || slot.OwnerKind != domain.MediaOwnerClip || slot.OwnerID != item.ClipID.String() || slot.Kind != domain.MediaSlotClipVideo || slot.Revision != item.MediaSlotRevision || slot.CurrentVersionID != item.MediaVersionID {
			return domain.ErrInvalidExportSnapshot
		}
		version, err := transactionMediaVersion(ctx, tx, item.MediaVersionID)
		if err != nil || version.IsDeleted() || version.SlotID != item.MediaSlotID || version.ArtifactID != item.ArtifactID || version.Revision != item.MediaVersionRevision || version.TrimInMilliseconds != item.TrimInMilliseconds || version.TrimOutMilliseconds != item.TrimOutMilliseconds {
			return domain.ErrInvalidExportSnapshot
		}
		artifact, err := transactionArtifact(ctx, tx, item.ArtifactID)
		if err != nil || artifact.ProjectID != job.ProjectID || artifact.Kind != domain.MediaKindVideo || item.TrimOutMilliseconds > artifact.DurationMilliseconds {
			return domain.ErrInvalidExportSnapshot
		}
	}
	for _, item := range job.Snapshot.SkippedClips {
		expected[item.Position] = position{item.SceneID.String(), item.ClipID.String()}
		if item.Reason != "missing_current_video" {
			return domain.ErrInvalidExportSnapshot
		}
		slotID, _ := domain.FixedMediaSlotID(item.ClipID.String(), domain.MediaSlotClipVideo)
		slot, err := transactionMediaSlot(ctx, tx, slotID)
		if err != nil || slot.ProjectID != job.ProjectID || slot.EpisodeID != job.EpisodeID || slot.CurrentVersionID != "" {
			return domain.ErrInvalidExportSnapshot
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT scenes.scene_id,clips.clip_id FROM scenes JOIN clips ON clips.scene_id=scenes.scene_id
WHERE scenes.episode_id=? AND scenes.deleted_at_ms IS NULL AND clips.deleted_at_ms IS NULL
ORDER BY scenes.position,scenes.scene_id,clips.position,clips.clip_id`, job.EpisodeID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	actualPosition := 0
	for rows.Next() {
		actualPosition++
		var sceneID, clipID string
		if err := rows.Scan(&sceneID, &clipID); err != nil {
			return err
		}
		if expected[actualPosition] != (position{sceneID, clipID}) {
			return domain.ErrInvalidExportSnapshot
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if actualPosition != len(expected) {
		return domain.ErrInvalidExportSnapshot
	}
	return nil
}

func (s *Store) ExportJob(ctx context.Context, id domain.ExportJobID) (domain.ExportJob, error) {
	if id.Validate() != nil {
		return domain.ExportJob{}, domain.ErrInvalidExportJob
	}
	db, err := s.requireDatabase()
	if err != nil {
		return domain.ExportJob{}, err
	}
	value, err := scanExportJob(db.QueryRowContext(ctx, exportJobColumns+" WHERE export_job_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExportJob{}, ErrExportJobNotFound
	}
	return value, err
}

func (s *Store) ExportJobByOperationKey(ctx context.Context, projectID domain.ProjectID, operationKey string) (domain.ExportJob, error) {
	if projectID.Validate() != nil || strings.TrimSpace(operationKey) == "" {
		return domain.ExportJob{}, domain.ErrInvalidExportJob
	}
	db, err := s.requireDatabase()
	if err != nil {
		return domain.ExportJob{}, err
	}
	value, err := scanExportJob(db.QueryRowContext(ctx, exportJobColumns+" WHERE project_id=? AND operation_key=?", projectID.String(), operationKey))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExportJob{}, ErrExportJobNotFound
	}
	return value, err
}

func (s *Store) ExportJobsByStates(ctx context.Context, states []domain.ExportJobState, limit int) ([]domain.ExportJob, error) {
	if len(states) == 0 || limit < 1 || limit > 10_000 {
		return nil, domain.ErrInvalidExportJob
	}
	arguments := make([]any, 0, len(states)+1)
	marks := make([]string, len(states))
	for index, state := range states {
		if !state.IsValid() {
			return nil, domain.ErrInvalidExportJob
		}
		marks[index], arguments = "?", append(arguments, string(state))
	}
	arguments = append(arguments, limit)
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	return queryExportJobs(ctx, db, exportJobColumns+" WHERE state IN ("+strings.Join(marks, ",")+") ORDER BY created_at_ms,export_job_id LIMIT ?", arguments...)
}

func (s *Store) RecentExportJobs(ctx context.Context, limit int) ([]domain.ExportJob, error) {
	if limit < 1 || limit > 10_000 {
		return nil, domain.ErrInvalidExportJob
	}
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	return queryExportJobs(ctx, db, exportJobColumns+" ORDER BY updated_at_ms DESC,export_job_id LIMIT ?", limit)
}

func (s *Store) ClaimExportJob(ctx context.Context, id domain.ExportJobID, expectedRevision int64, at time.Time) (domain.ExportJob, error) {
	return s.updateExportJob(ctx, id, expectedRevision, at, func(job *domain.ExportJob) error {
		if job.State != domain.ExportJobQueued {
			return ErrExportJobStateConflict
		}
		job.State, job.ProgressStage, job.ProgressMessage = domain.ExportJobRunning, "preparing", "Preparing episode export"
		job.StartedAt = &at
		return nil
	})
}

func (s *Store) UpdateExportProgress(ctx context.Context, id domain.ExportJobID, expectedRevision int64, stage, message string, at time.Time) (domain.ExportJob, error) {
	return s.updateExportJob(ctx, id, expectedRevision, at, func(job *domain.ExportJob) error {
		if job.State != domain.ExportJobRunning {
			return ErrExportJobStateConflict
		}
		job.ProgressStage, job.ProgressMessage = stage, message
		return nil
	})
}

func (s *Store) RequestExportCancel(ctx context.Context, id domain.ExportJobID, expectedRevision int64, at time.Time) (domain.ExportJob, error) {
	return s.updateExportJob(ctx, id, expectedRevision, at, func(job *domain.ExportJob) error {
		switch job.State {
		case domain.ExportJobQueued:
			job.State, job.ProgressStage, job.ProgressMessage, job.FinishedAt = domain.ExportJobCancelled, "cancelled", "", &at
		case domain.ExportJobRunning:
			job.State, job.ProgressStage, job.ProgressMessage = domain.ExportJobCancelRequested, "cancelling", "Stopping episode export"
		default:
			return ErrExportJobStateConflict
		}
		return nil
	})
}

func (s *Store) FinishExportJob(ctx context.Context, id domain.ExportJobID, expectedRevision int64, state domain.ExportJobState, outputPath, failureCode, failureMessage string, at time.Time) (domain.ExportJob, error) {
	return s.updateExportJob(ctx, id, expectedRevision, at, func(job *domain.ExportJob) error {
		if !domain.CanTransitionExportJob(job.State, state) || state != domain.ExportJobSucceeded && state != domain.ExportJobFailed && state != domain.ExportJobCancelled {
			return ErrExportJobStateConflict
		}
		job.State, job.FinishedAt = state, &at
		job.ProgressStage, job.ProgressMessage = string(state), ""
		job.OutputRelativePath, job.FailureCode, job.FailureMessage = "", "", ""
		switch state {
		case domain.ExportJobSucceeded:
			job.OutputRelativePath = outputPath
		case domain.ExportJobFailed:
			job.FailureCode, job.FailureMessage = failureCode, failureMessage
		}
		return nil
	})
}

func (s *Store) updateExportJob(ctx context.Context, id domain.ExportJobID, expectedRevision int64, at time.Time, mutate func(*domain.ExportJob) error) (domain.ExportJob, error) {
	if id.Validate() != nil || expectedRevision < 1 || at.Location() != time.UTC || mutate == nil {
		return domain.ExportJob{}, domain.ErrInvalidExportJob
	}
	var changed domain.ExportJob
	err := s.withTransaction(ctx, "update ExportJob", func(tx *sql.Tx) error {
		current, err := scanExportJob(tx.QueryRowContext(ctx, exportJobColumns+" WHERE export_job_id=?", id.String()))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrExportJobNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrExportJobStateConflict
		}
		if err := mutate(&current); err != nil {
			return err
		}
		current.Revision++
		current.UpdatedAt = at
		if err := current.Validate(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE export_jobs SET state=?,progress_stage=?,progress_message=?,output_relative_path=?,failure_code=?,failure_message=?,revision=?,updated_at_ms=?,started_at_ms=?,finished_at_ms=? WHERE export_job_id=? AND revision=?`,
			string(current.State), current.ProgressStage, current.ProgressMessage, current.OutputRelativePath, current.FailureCode, current.FailureMessage, current.Revision, at.UnixMilli(), nullableTime(current.StartedAt), nullableTime(current.FinishedAt), id.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrExportJobStateConflict); err != nil {
			return err
		}
		changed = current
		return nil
	})
	return changed, err
}

func queryExportJobs(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, arguments ...any) ([]domain.ExportJob, error) {
	rows, err := queryer.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.ExportJob{}
	for rows.Next() {
		job, err := scanExportJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func scanExportJob(row rowScanner) (domain.ExportJob, error) {
	var id, projectID, episodeID, operationKey, state, snapshotJSON, stage, message, output, failureCode, failureMessage string
	var storyboardRevision, revision, created, updated int64
	var started, finished sql.NullInt64
	if err := row.Scan(&id, &projectID, &episodeID, &operationKey, &state, &snapshotJSON, &storyboardRevision, &stage, &message, &output, &failureCode, &failureMessage, &revision, &created, &updated, &started, &finished); err != nil {
		return domain.ExportJob{}, err
	}
	parsedID, err := domain.ParseExportJobID(id)
	if err != nil {
		return domain.ExportJob{}, invalidStoredData("ExportJob ID", err)
	}
	pid, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.ExportJob{}, invalidStoredData("ExportJob Project", err)
	}
	eid, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.ExportJob{}, invalidStoredData("ExportJob Episode", err)
	}
	var snapshot domain.ExportSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil || snapshot.StoryboardRevision != storyboardRevision {
		return domain.ExportJob{}, invalidStoredData("ExportJob snapshot", domain.ErrInvalidExportSnapshot)
	}
	job := domain.ExportJob{ID: parsedID, ProjectID: pid, EpisodeID: eid, OperationKey: operationKey, State: domain.ExportJobState(state), Snapshot: snapshot, ProgressStage: stage, ProgressMessage: message, OutputRelativePath: output, FailureCode: failureCode, FailureMessage: failureMessage, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC()}
	if started.Valid {
		value := time.UnixMilli(started.Int64).UTC()
		job.StartedAt = &value
	}
	if finished.Valid {
		value := time.UnixMilli(finished.Int64).UTC()
		job.FinishedAt = &value
	}
	if err := job.Validate(); err != nil {
		return domain.ExportJob{}, invalidStoredData("ExportJob", err)
	}
	return job, nil
}
