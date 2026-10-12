package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrGenerationBatchNotFound        = errors.New("generation batch not found")
	ErrGenerationJobNotFound          = errors.New("generation job not found")
	ErrGenerationOperationKeyConflict = errors.New("generation operation key conflict")
	ErrGenerationSlotBusy             = errors.New("media slot already has an active generation job")
	ErrGenerationJobStateConflict     = errors.New("generation job state conflict")
	ErrGenerationStopUnsupported      = errors.New("generation Generator does not support stopping")
)

func (s *Store) CreateGenerationBatch(ctx context.Context, batch domain.GenerationBatch, jobs []domain.GenerationJob, updatedAt time.Time) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	if len(jobs) == 0 || updatedAt != batch.CreatedAt || validateMutationTime(updatedAt) != nil {
		return domain.ErrInvalidGenerationBatch
	}
	return s.withTransaction(ctx, "create GenerationBatch", func(tx *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, tx)
		if err != nil {
			return err
		}
		if projectID != batch.ProjectID {
			return errors.New("GenerationBatch belongs to another Project")
		}
		var episodeProject string
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM episodes WHERE episode_id=?", batch.EpisodeID.String()).Scan(&episodeProject); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEpisodeNotFound
			}
			return err
		}
		if episodeProject != batch.ProjectID.String() {
			return errors.New("GenerationBatch Episode belongs to another Project")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_batches (generation_batch_id,project_id,episode_id,operation_key,request_hash,created_at_ms) VALUES (?,?,?,?,?,?)`, batch.ID.String(), batch.ProjectID.String(), batch.EpisodeID.String(), batch.OperationKey, batch.RequestHash, batch.CreatedAt.UnixMilli()); err != nil {
			if isUniqueConstraint(err) {
				return ErrGenerationOperationKeyConflict
			}
			return err
		}
		seenSlots := map[domain.MediaSlotID]struct{}{}
		for _, job := range jobs {
			if err := validateInitialGenerationJob(batch, job); err != nil {
				return err
			}
			if _, exists := seenSlots[job.TargetSlotID]; exists {
				return ErrGenerationSlotBusy
			}
			seenSlots[job.TargetSlotID] = struct{}{}
			if err := validateGenerationJobReferences(ctx, tx, job); err != nil {
				return err
			}
			if err := insertGenerationJob(ctx, tx, job); err != nil {
				return err
			}
		}
		return updateProjectTimestamp(ctx, tx, batch.ProjectID, updatedAt)
	})
}

func (s *Store) ClaimGenerationJob(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "claim GenerationJob", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if job.State != domain.GenerationJobQueued {
			return ErrGenerationJobStateConflict
		}
		job.State = domain.GenerationJobRunning
		job.ProgressStage = "submitting"
		job.ProgressMessage = ""
		job.StartedAt = &updatedAt
		return nil
	})
}

func (s *Store) RecordGenerationOperation(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, operationID, stage, message string, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "record Generation operation", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if (job.State != domain.GenerationJobRunning && job.State != domain.GenerationJobStopRequested) || operationID == "" {
			return ErrGenerationJobStateConflict
		}
		job.GeneratorOperationID = operationID
		job.ProgressStage = stage
		job.ProgressMessage = message
		return nil
	})
}

func (s *Store) UpdateGenerationProgress(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, stage, message string, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "update Generation progress", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if job.State != domain.GenerationJobRunning && job.State != domain.GenerationJobStopRequested {
			return ErrGenerationJobStateConflict
		}
		job.ProgressStage = stage
		job.ProgressMessage = message
		return nil
	})
}

func (s *Store) CancelGenerationJob(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "cancel GenerationJob", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if job.State != domain.GenerationJobQueued {
			return ErrGenerationJobStateConflict
		}
		job.State = domain.GenerationJobCancelled
		job.ProgressStage = "cancelled"
		job.FinishedAt = &updatedAt
		return nil
	})
}

func (s *Store) RequestGenerationStop(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "request Generation stop", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if job.State != domain.GenerationJobRunning {
			return ErrGenerationJobStateConflict
		}
		if !job.StopSupported {
			return ErrGenerationStopUnsupported
		}
		job.State = domain.GenerationJobStopRequested
		job.ProgressStage = "stop_requested"
		job.ProgressMessage = ""
		return nil
	})
}

func (s *Store) FinishGenerationJob(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, targetState domain.GenerationJobState, failureCode, failureMessage string, updatedAt time.Time) (domain.GenerationJob, error) {
	return s.updateGenerationJob(ctx, "finish GenerationJob", id, expectedRevision, updatedAt, func(job *domain.GenerationJob) error {
		if !domain.CanTransitionGenerationJob(job.State, targetState) || (targetState != domain.GenerationJobFailed && targetState != domain.GenerationJobStopped) {
			return ErrGenerationJobStateConflict
		}
		job.State = targetState
		job.FinishedAt = &updatedAt
		if targetState == domain.GenerationJobFailed {
			job.FailureCode = failureCode
			job.FailureMessage = failureMessage
			job.ProgressStage = "failed"
		} else {
			job.FailureCode = ""
			job.FailureMessage = ""
			job.ProgressStage = "stopped"
		}
		job.ProgressMessage = ""
		return nil
	})
}

func (s *Store) CompleteGenerationJob(ctx context.Context, id domain.GenerationJobID, expectedRevision int64, artifact domain.Artifact, versionID domain.MediaVersionID, updatedAt time.Time) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	if err := versionID.Validate(); err != nil {
		return err
	}
	if expectedRevision < 1 || validateMutationTime(updatedAt) != nil || artifact.CreatedAt.After(updatedAt) {
		return domain.ErrInvalidGenerationJob
	}
	return s.withTransaction(ctx, "complete GenerationJob", func(tx *sql.Tx) error {
		job, err := transactionGenerationJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.Revision != expectedRevision || !domain.CanTransitionGenerationJob(job.State, domain.GenerationJobSucceeded) {
			return ErrGenerationJobStateConflict
		}
		slot, err := transactionMediaSlot(ctx, tx, job.TargetSlotID)
		if err != nil {
			return err
		}
		if slot.ProjectID != job.ProjectID || slot.EpisodeID != job.EpisodeID || artifact.ProjectID != job.ProjectID || artifact.Kind != job.Snapshot.TargetMediaKind || slot.Kind.MediaKind() != artifact.Kind {
			return ErrMediaKindMismatch
		}
		if err := validateArtifactOutputForSlot(ctx, tx, slot, artifact); err != nil {
			return err
		}
		artifact, err = ensureGenerationArtifact(ctx, tx, artifact)
		if err != nil {
			return err
		}
		if artifact.ProjectID != job.ProjectID || artifact.Kind != job.Snapshot.TargetMediaKind {
			return ErrMediaKindMismatch
		}
		var duplicate int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM media_versions WHERE media_slot_id=? AND artifact_id=? AND deleted_at_ms IS NULL", slot.ID.String(), artifact.ID.String()).Scan(&duplicate)
		if err == nil {
			return ErrMediaVersionAlreadyExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM media_versions WHERE media_slot_id=?", slot.ID.String()).Scan(&count); err != nil {
			return err
		}
		version := domain.MediaVersion{ID: versionID, SlotID: slot.ID, ArtifactID: artifact.ID, SequenceNumber: count + 1, Revision: 1, CreatedAt: updatedAt, UpdatedAt: updatedAt}
		if slot.Kind == domain.MediaSlotClipVideo {
			version.TrimOutMilliseconds = artifact.DurationMilliseconds
		}
		if err := version.Validate(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,?,?,?,?,NULL)`, version.ID.String(), version.SlotID.String(), version.ArtifactID.String(), version.SequenceNumber, version.TrimInMilliseconds, version.TrimOutMilliseconds, version.Revision, version.CreatedAt.UnixMilli(), version.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		makeCurrent := slot.Revision == job.Snapshot.TargetSlotRevision && mediaOwnerIsActive(ctx, tx, slot)
		var current any
		if makeCurrent {
			current = version.ID.String()
		} else if slot.CurrentVersionID != "" {
			current = slot.CurrentVersionID.String()
		}
		if _, err := tx.ExecContext(ctx, "UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=?", current, updatedAt.UnixMilli(), slot.ID.String()); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET state='succeeded',progress_stage='succeeded',progress_message='',result_artifact_id=?,result_media_version_id=?,failure_code='',failure_message='',revision=revision+1,updated_at_ms=?,finished_at_ms=? WHERE generation_job_id=? AND revision=?`, artifact.ID.String(), version.ID.String(), updatedAt.UnixMilli(), updatedAt.UnixMilli(), job.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrGenerationJobStateConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, job.ProjectID, updatedAt)
	})
}

func (s *Store) updateGenerationJob(ctx context.Context, operation string, id domain.GenerationJobID, expectedRevision int64, updatedAt time.Time, mutate func(*domain.GenerationJob) error) (domain.GenerationJob, error) {
	if err := id.Validate(); err != nil {
		return domain.GenerationJob{}, err
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || validateMutationTime(updatedAt) != nil {
		return domain.GenerationJob{}, domain.ErrInvalidGenerationJob
	}
	var changed domain.GenerationJob
	err := s.withTransaction(ctx, operation, func(tx *sql.Tx) error {
		current, err := transactionGenerationJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrGenerationJobStateConflict
		}
		beforeState := current.State
		if err := mutate(&current); err != nil {
			return err
		}
		if current.State != beforeState && !domain.CanTransitionGenerationJob(beforeState, current.State) {
			return ErrGenerationJobStateConflict
		}
		current.Revision++
		current.UpdatedAt = updatedAt
		if err := current.Validate(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET state=?,generator_operation_id=?,progress_stage=?,progress_message=?,failure_code=?,failure_message=?,revision=?,updated_at_ms=?,started_at_ms=?,finished_at_ms=? WHERE generation_job_id=? AND revision=?`, string(current.State), nullableText(current.GeneratorOperationID), current.ProgressStage, current.ProgressMessage, current.FailureCode, current.FailureMessage, current.Revision, updatedAt.UnixMilli(), nullableTime(current.StartedAt), nullableTime(current.FinishedAt), current.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrGenerationJobStateConflict); err != nil {
			return err
		}
		if err := updateProjectTimestamp(ctx, tx, current.ProjectID, updatedAt); err != nil {
			return err
		}
		changed = current
		return nil
	})
	return changed, err
}

func validateInitialGenerationJob(batch domain.GenerationBatch, job domain.GenerationJob) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if job.BatchID != batch.ID || job.ProjectID != batch.ProjectID || job.EpisodeID != batch.EpisodeID || job.State != domain.GenerationJobQueued || job.Revision != 1 || job.CreatedAt != batch.CreatedAt || job.UpdatedAt != batch.CreatedAt {
		return domain.ErrInvalidGenerationJob
	}
	return nil
}

func validateGenerationJobReferences(ctx context.Context, tx *sql.Tx, job domain.GenerationJob) error {
	slot, err := transactionMediaSlot(ctx, tx, job.TargetSlotID)
	if err != nil {
		return err
	}
	if err := validateMediaOwnerActive(ctx, tx, slot); err != nil {
		return err
	}
	if slot.ProjectID != job.ProjectID || slot.EpisodeID != job.EpisodeID || slot.Revision != job.Snapshot.TargetSlotRevision || slot.Kind.MediaKind() != job.Snapshot.TargetMediaKind || slot.Kind == domain.MediaSlotAudioPrimary {
		return domain.ErrInvalidGenerationJob
	}
	var width, height int
	if err := tx.QueryRowContext(ctx, "SELECT output_width,output_height FROM projects WHERE project_id=?", job.ProjectID.String()).Scan(&width, &height); err != nil {
		return err
	}
	projectOutput, err := domain.NewOutputSpec(width, height)
	if err != nil {
		return err
	}
	expectedWidth, expectedHeight := domain.GenerationOutputSize(slot.Kind, projectOutput)
	if expectedWidth != job.Snapshot.OutputWidth || expectedHeight != job.Snapshot.OutputHeight {
		return domain.ErrInvalidGenerationSnapshot
	}
	if job.RetryOfJobID != "" {
		prior, err := transactionGenerationJob(ctx, tx, job.RetryOfJobID)
		if err != nil {
			return err
		}
		if !prior.State.IsTerminal() || prior.ProjectID != job.ProjectID || prior.EpisodeID != job.EpisodeID || prior.TargetSlotID != job.TargetSlotID {
			return domain.ErrInvalidGenerationJob
		}
	}
	for _, reference := range job.Snapshot.References {
		artifact, err := transactionArtifact(ctx, tx, reference.ArtifactID)
		if err != nil {
			return err
		}
		if artifact.ProjectID != job.ProjectID {
			return domain.ErrInvalidGenerationSnapshot
		}
		// General image references come from the project-wide asset library and
		// do not need to be selected in the target Episode. Timeline-bound input
		// roles still must resolve to current media in that Episode.
		if reference.Role != domain.GenerationReferenceImage {
			selected, err := artifactSelectedInEpisode(ctx, tx, artifact.ID, job.EpisodeID)
			if err != nil {
				return err
			}
			if !selected {
				return domain.ErrInvalidGenerationSnapshot
			}
		}
		expectedKind := reference.Role.MediaKind()
		if artifact.Kind != expectedKind {
			return domain.ErrInvalidGenerationSnapshot
		}
		if reference.Role == domain.GenerationReferenceFirstImage || reference.Role == domain.GenerationReferenceLastImage {
			if int64(artifact.Width)*int64(height) != int64(artifact.Height)*int64(width) {
				return ErrMediaAspectRatioMismatch
			}
			if (artifact.Width != width || artifact.Height != height) && !reference.PreparationRequired {
				return domain.ErrInvalidGenerationSnapshot
			}
		}
		if artifact.Kind != domain.MediaKindImage && (reference.TrimStartMilliseconds < 0 || reference.TrimEndMilliseconds <= reference.TrimStartMilliseconds || reference.TrimEndMilliseconds > artifact.DurationMilliseconds) {
			return domain.ErrInvalidGenerationSnapshot
		}
	}
	return nil
}

func insertGenerationJob(ctx context.Context, tx *sql.Tx, job domain.GenerationJob) error {
	snapshot, err := marshalGenerationSnapshot(job.Snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO generation_jobs (generation_job_id,generation_batch_id,project_id,episode_id,target_media_slot_id,retry_of_job_id,state,snapshot_json,generator_id,target_slot_revision,stop_supported,generator_operation_id,progress_stage,progress_message,result_artifact_id,result_media_version_id,failure_code,failure_message,revision,created_at_ms,updated_at_ms,started_at_ms,finished_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL,?,?,NULL,NULL,'','',?,?,?,NULL,NULL)`, job.ID.String(), job.BatchID.String(), job.ProjectID.String(), job.EpisodeID.String(), job.TargetSlotID.String(), nullableGenerationJobID(job.RetryOfJobID), string(job.State), snapshot, string(job.Snapshot.GeneratorID), job.Snapshot.TargetSlotRevision, job.StopSupported, job.ProgressStage, job.ProgressMessage, job.Revision, job.CreatedAt.UnixMilli(), job.UpdatedAt.UnixMilli())
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrGenerationSlotBusy
		}
		return err
	}
	positions := map[domain.GenerationArtifactRole]int{}
	for _, reference := range job.Snapshot.References {
		positions[reference.Role]++
		if _, err := tx.ExecContext(ctx, "INSERT INTO generation_job_artifact_references (generation_job_id,role,position,artifact_id,trim_start_ms,trim_end_ms,preparation_required) VALUES (?,?,?,?,?,?,?)", job.ID.String(), string(reference.Role), positions[reference.Role], reference.ArtifactID.String(), reference.TrimStartMilliseconds, reference.TrimEndMilliseconds, reference.PreparationRequired); err != nil {
			return err
		}
	}
	return nil
}

func ensureGenerationArtifact(ctx context.Context, tx *sql.Tx, artifact domain.Artifact) (domain.Artifact, error) {
	var existingID string
	err := tx.QueryRowContext(ctx, "SELECT artifact_id FROM artifacts WHERE sha256=?", artifact.SHA256).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact, insertArtifact(ctx, tx, artifact)
	}
	if err != nil {
		return domain.Artifact{}, err
	}
	id, err := domain.ParseArtifactID(existingID)
	if err != nil {
		return domain.Artifact{}, invalidStoredData("Artifact ID", err)
	}
	if artifact.RemoteReferenceURL != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET remote_reference_url=?,remote_reference_expires_at_ms=? WHERE artifact_id=?`, artifact.RemoteReferenceURL, artifact.RemoteReferenceExpiresAt.UnixMilli(), id.String()); err != nil {
			return domain.Artifact{}, err
		}
	}
	return transactionArtifact(ctx, tx, id)
}

func validateArtifactOutputForSlot(ctx context.Context, tx *sql.Tx, slot domain.MediaSlot, artifact domain.Artifact) error {
	if slot.Kind != domain.MediaSlotClipFirstImage && slot.Kind != domain.MediaSlotClipLastImage {
		return nil
	}
	var width, height int
	if err := tx.QueryRowContext(ctx, "SELECT output_width,output_height FROM projects WHERE project_id=?", slot.ProjectID.String()).Scan(&width, &height); err != nil {
		return err
	}
	if int64(artifact.Width)*int64(height) != int64(artifact.Height)*int64(width) {
		return ErrMediaAspectRatioMismatch
	}
	if artifact.Width != width || artifact.Height != height {
		return ErrMediaOutputSpecMismatch
	}
	return nil
}

func mediaOwnerIsActive(ctx context.Context, tx *sql.Tx, slot domain.MediaSlot) bool {
	return validateMediaOwnerActive(ctx, tx, slot) == nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableGenerationJobID(value domain.GenerationJobID) any {
	if value == "" {
		return nil
	}
	return value.String()
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
