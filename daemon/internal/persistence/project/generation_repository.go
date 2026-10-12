package projectstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const generationBatchColumns = `SELECT generation_batch_id,project_id,episode_id,operation_key,request_hash,created_at_ms FROM generation_batches`
const generationJobColumns = `SELECT generation_job_id,generation_batch_id,project_id,episode_id,target_media_slot_id,retry_of_job_id,state,snapshot_json,stop_supported,generator_operation_id,progress_stage,progress_message,result_artifact_id,result_media_version_id,failure_code,failure_message,revision,created_at_ms,updated_at_ms,started_at_ms,finished_at_ms FROM generation_jobs`

func (s *Store) GenerationBatch(ctx context.Context, id domain.GenerationBatchID) (domain.GenerationBatchDetails, error) {
	if err := id.Validate(); err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	db, err := s.requireDatabase()
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	return readSnapshot(ctx, db, "read GenerationBatch", func(tx *sql.Tx) (domain.GenerationBatchDetails, error) {
		batch, err := transactionGenerationBatch(ctx, tx, id)
		if err != nil {
			return domain.GenerationBatchDetails{}, err
		}
		jobs, err := transactionGenerationJobs(ctx, tx, "generation_batch_id=? ORDER BY created_at_ms,generation_job_id", id.String())
		if err != nil {
			return domain.GenerationBatchDetails{}, err
		}
		result := domain.GenerationBatchDetails{Batch: batch, Jobs: jobs}
		if err := result.Validate(); err != nil {
			return domain.GenerationBatchDetails{}, invalidStoredData("GenerationBatch", err)
		}
		return result, nil
	})
}

func (s *Store) GenerationBatchByOperationKey(ctx context.Context, projectID domain.ProjectID, operationKey string) (domain.GenerationBatchDetails, error) {
	if err := projectID.Validate(); err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	if strings.TrimSpace(operationKey) == "" {
		return domain.GenerationBatchDetails{}, domain.ErrInvalidGenerationBatch
	}
	db, err := s.requireDatabase()
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT generation_batch_id FROM generation_batches WHERE project_id=? AND operation_key=?", projectID.String(), operationKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.GenerationBatchDetails{}, ErrGenerationBatchNotFound
		}
		return domain.GenerationBatchDetails{}, err
	}
	id, err := domain.ParseGenerationBatchID(raw)
	if err != nil {
		return domain.GenerationBatchDetails{}, invalidStoredData("GenerationBatch ID", err)
	}
	return s.GenerationBatch(ctx, id)
}

func (s *Store) GenerationJob(ctx context.Context, id domain.GenerationJobID) (domain.GenerationJob, error) {
	if err := id.Validate(); err != nil {
		return domain.GenerationJob{}, err
	}
	db, err := s.requireDatabase()
	if err != nil {
		return domain.GenerationJob{}, err
	}
	job, err := scanGenerationJob(db.QueryRowContext(ctx, generationJobColumns+" WHERE generation_job_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationJob{}, ErrGenerationJobNotFound
	}
	return job, err
}

func (s *Store) GenerationJobsByStates(ctx context.Context, states []domain.GenerationJobState, limit int) ([]domain.GenerationJob, error) {
	if len(states) == 0 || limit < 1 || limit > 10_000 {
		return nil, domain.ErrInvalidGenerationJob
	}
	placeholders := make([]string, len(states))
	arguments := make([]any, 0, len(states)+1)
	for index, state := range states {
		if !state.IsValid() {
			return nil, domain.ErrInvalidGenerationJob
		}
		placeholders[index] = "?"
		arguments = append(arguments, string(state))
	}
	arguments = append(arguments, limit)
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, generationJobColumns+" WHERE state IN ("+strings.Join(placeholders, ",")+") ORDER BY created_at_ms,generation_job_id LIMIT ?", arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.GenerationJob{}
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) RecentGenerationJobs(ctx context.Context, limit int) ([]domain.GenerationJob, error) {
	if limit < 1 || limit > 10_000 {
		return nil, domain.ErrInvalidGenerationJob
	}
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, generationJobColumns+" ORDER BY updated_at_ms DESC,generation_job_id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.GenerationJob{}
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func transactionGenerationBatch(ctx context.Context, tx *sql.Tx, id domain.GenerationBatchID) (domain.GenerationBatch, error) {
	value, err := scanGenerationBatch(tx.QueryRowContext(ctx, generationBatchColumns+" WHERE generation_batch_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationBatch{}, ErrGenerationBatchNotFound
	}
	return value, err
}

func transactionGenerationJob(ctx context.Context, tx *sql.Tx, id domain.GenerationJobID) (domain.GenerationJob, error) {
	value, err := scanGenerationJob(tx.QueryRowContext(ctx, generationJobColumns+" WHERE generation_job_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationJob{}, ErrGenerationJobNotFound
	}
	return value, err
}

func transactionGenerationJobs(ctx context.Context, tx *sql.Tx, predicate string, arguments ...any) ([]domain.GenerationJob, error) {
	rows, err := tx.QueryContext(ctx, generationJobColumns+" WHERE "+predicate, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.GenerationJob{}
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func scanGenerationBatch(row rowScanner) (domain.GenerationBatch, error) {
	var id, projectID, episodeID, operationKey, requestHash string
	var created int64
	if err := row.Scan(&id, &projectID, &episodeID, &operationKey, &requestHash, &created); err != nil {
		return domain.GenerationBatch{}, err
	}
	parsedID, err := domain.ParseGenerationBatchID(id)
	if err != nil {
		return domain.GenerationBatch{}, invalidStoredData("GenerationBatch ID", err)
	}
	parsedProjectID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.GenerationBatch{}, invalidStoredData("GenerationBatch Project", err)
	}
	parsedEpisodeID, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.GenerationBatch{}, invalidStoredData("GenerationBatch Episode", err)
	}
	value := domain.GenerationBatch{ID: parsedID, ProjectID: parsedProjectID, EpisodeID: parsedEpisodeID, OperationKey: operationKey, RequestHash: requestHash, CreatedAt: time.UnixMilli(created).UTC()}
	if err := value.Validate(); err != nil {
		return domain.GenerationBatch{}, invalidStoredData("GenerationBatch", err)
	}
	return value, nil
}

func scanGenerationJob(row rowScanner) (domain.GenerationJob, error) {
	var id, batchID, projectID, episodeID, slotID, state, snapshotJSON string
	var retryID, operationID, resultArtifactID, resultVersionID sql.NullString
	var progressStage, progressMessage, failureCode, failureMessage string
	var stopSupported bool
	var revision, created, updated int64
	var started, finished sql.NullInt64
	if err := row.Scan(&id, &batchID, &projectID, &episodeID, &slotID, &retryID, &state, &snapshotJSON, &stopSupported, &operationID, &progressStage, &progressMessage, &resultArtifactID, &resultVersionID, &failureCode, &failureMessage, &revision, &created, &updated, &started, &finished); err != nil {
		return domain.GenerationJob{}, err
	}
	parsedID, err := domain.ParseGenerationJobID(id)
	if err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob ID", err)
	}
	parsedBatchID, err := domain.ParseGenerationBatchID(batchID)
	if err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob Batch", err)
	}
	parsedProjectID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob Project", err)
	}
	parsedEpisodeID, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob Episode", err)
	}
	parsedSlotID, err := domain.ParseMediaSlotID(slotID)
	if err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob Slot", err)
	}
	var parsedRetryID domain.GenerationJobID
	if retryID.Valid {
		parsedRetryID, err = domain.ParseGenerationJobID(retryID.String)
		if err != nil {
			return domain.GenerationJob{}, invalidStoredData("GenerationJob retry source", err)
		}
	}
	var snapshot domain.GenerationInputSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob snapshot", err)
	}
	var parsedArtifactID domain.ArtifactID
	if resultArtifactID.Valid {
		parsedArtifactID, err = domain.ParseArtifactID(resultArtifactID.String)
		if err != nil {
			return domain.GenerationJob{}, invalidStoredData("GenerationJob result Artifact", err)
		}
	}
	var parsedVersionID domain.MediaVersionID
	if resultVersionID.Valid {
		parsedVersionID, err = domain.ParseMediaVersionID(resultVersionID.String)
		if err != nil {
			return domain.GenerationJob{}, invalidStoredData("GenerationJob result MediaVersion", err)
		}
	}
	value := domain.GenerationJob{ID: parsedID, BatchID: parsedBatchID, ProjectID: parsedProjectID, EpisodeID: parsedEpisodeID, TargetSlotID: parsedSlotID, RetryOfJobID: parsedRetryID, State: domain.GenerationJobState(state), Snapshot: snapshot, StopSupported: stopSupported, GeneratorOperationID: operationID.String, ProgressStage: progressStage, ProgressMessage: progressMessage, ResultArtifactID: parsedArtifactID, ResultVersionID: parsedVersionID, FailureCode: failureCode, FailureMessage: failureMessage, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC(), StartedAt: nullableMilliseconds(started), FinishedAt: nullableMilliseconds(finished)}
	if err := value.Validate(); err != nil {
		return domain.GenerationJob{}, invalidStoredData("GenerationJob", err)
	}
	return value, nil
}

func nullableMilliseconds(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := time.UnixMilli(value.Int64).UTC()
	return &at
}

func marshalGenerationSnapshot(snapshot domain.GenerationInputSnapshot) (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	value, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode Generation snapshot: %w", err)
	}
	return string(value), nil
}
