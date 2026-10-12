package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ResetStoryboard atomically cancels queued Clip Jobs, optionally requests
// stops for running Clip Jobs, and only soft-deletes the Storyboard when no
// non-terminal Clip Job remains. The boolean reports whether a non-empty
// Storyboard was reset.
func (s *Store) ResetStoryboard(ctx context.Context, episodeID domain.EpisodeID, expectedRevision int64, requestStops bool, updatedAt time.Time) ([]domain.GenerationJob, bool, error) {
	if episodeID.Validate() != nil || expectedRevision < 1 || expectedRevision == math.MaxInt64 || validateMutationTime(updatedAt) != nil {
		return nil, false, domain.ErrInvalidStoryboard
	}
	changed := []domain.GenerationJob{}
	reset := false
	err := s.withTransaction(ctx, "reset Storyboard", func(tx *sql.Tx) error {
		var rawProjectID string
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT project_id,storyboard_revision FROM episodes WHERE episode_id=?", episodeID.String()).Scan(&rawProjectID, &revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEpisodeNotFound
			}
			return err
		}
		if revision != expectedRevision {
			return ErrStoryboardRevisionConflict
		}
		projectID, err := domain.ParseProjectID(rawProjectID)
		if err != nil {
			return invalidStoredData("Episode Project ID", err)
		}
		jobs, err := transactionGenerationJobs(ctx, tx, `state IN ('queued','running','stop_requested') AND target_media_slot_id IN (
			SELECT slot.media_slot_id FROM media_slots AS slot
			JOIN clips AS clip ON slot.owner_kind='clip' AND slot.owner_id=clip.clip_id
			WHERE clip.episode_id=? AND clip.deleted_at_ms IS NULL
		) ORDER BY created_at_ms,generation_job_id`, episodeID.String())
		if err != nil {
			return err
		}
		if requestStops {
			for _, job := range jobs {
				if job.State == domain.GenerationJobRunning && !job.StopSupported {
					return ErrGenerationStopUnsupported
				}
			}
		}
		waiting := false
		for _, job := range jobs {
			switch job.State {
			case domain.GenerationJobQueued:
				result, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET state='cancelled',progress_stage='cancelled',progress_message='',revision=revision+1,updated_at_ms=?,finished_at_ms=? WHERE generation_job_id=? AND revision=? AND state='queued'`, updatedAt.UnixMilli(), updatedAt.UnixMilli(), job.ID.String(), job.Revision)
				if err != nil {
					return err
				}
				if err := requireOne(result, ErrGenerationJobStateConflict); err != nil {
					return err
				}
				updated, err := transactionGenerationJob(ctx, tx, job.ID)
				if err != nil {
					return err
				}
				changed = append(changed, updated)
			case domain.GenerationJobRunning:
				waiting = true
				if requestStops {
					result, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET state='stop_requested',progress_stage='stop_requested',progress_message='',revision=revision+1,updated_at_ms=? WHERE generation_job_id=? AND revision=? AND state='running'`, updatedAt.UnixMilli(), job.ID.String(), job.Revision)
					if err != nil {
						return err
					}
					if err := requireOne(result, ErrGenerationJobStateConflict); err != nil {
						return err
					}
					updated, err := transactionGenerationJob(ctx, tx, job.ID)
					if err != nil {
						return err
					}
					changed = append(changed, updated)
				}
			case domain.GenerationJobStopRequested:
				waiting = true
			}
		}
		if waiting {
			if len(changed) > 0 {
				return updateProjectTimestamp(ctx, tx, projectID, updatedAt)
			}
			return nil
		}
		count, err := activeSceneCount(ctx, tx, episodeID)
		if err != nil {
			return err
		}
		if count == 0 {
			if len(changed) > 0 {
				return updateProjectTimestamp(ctx, tx, projectID, updatedAt)
			}
			return nil
		}
		if err := advanceStoryboard(ctx, tx, episodeID, expectedRevision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1,updated_at_ms=?,deleted_at_ms=?,deleted_with_scene_id=scene_id WHERE episode_id=? AND deleted_at_ms IS NULL`, updatedAt.UnixMilli(), updatedAt.UnixMilli(), episodeID.String()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scenes SET revision=revision+1,updated_at_ms=?,deleted_at_ms=? WHERE episode_id=? AND deleted_at_ms IS NULL`, updatedAt.UnixMilli(), updatedAt.UnixMilli(), episodeID.String()); err != nil {
			return err
		}
		reset = true
		return updateProjectTimestamp(ctx, tx, projectID, updatedAt)
	})
	return changed, reset, err
}
