package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const clipReferenceArtifactColumns = `SELECT artifacts.artifact_id,artifacts.project_id,artifacts.media_kind,artifacts.relative_path,artifacts.sha256,artifacts.byte_size,artifacts.mime_type,artifacts.original_filename,artifacts.width,artifacts.height,artifacts.duration_ms,artifacts.fps_numerator,artifacts.fps_denominator,artifacts.audio_sample_rate,artifacts.audio_channels,artifacts.remote_reference_url,artifacts.remote_reference_expires_at_ms,artifacts.created_at_ms FROM artifacts`

func (s *Store) ClipReferenceArtifacts(ctx context.Context, clipID domain.ClipID) ([]domain.Artifact, error) {
	if err := clipID.Validate(); err != nil {
		return nil, err
	}
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, clipReferenceArtifactColumns+`
JOIN clip_reference_media AS reference ON reference.artifact_id=artifacts.artifact_id
WHERE reference.clip_id=? AND reference.deleted_at_ms IS NULL
ORDER BY reference.created_at_ms, artifacts.artifact_id`, clipID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Artifact{}
	for rows.Next() {
		artifact, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (s *Store) AddClipReferenceArtifact(ctx context.Context, clipID domain.ClipID, artifact domain.Artifact, at time.Time) error {
	if err := clipID.Validate(); err != nil {
		return err
	}
	if err := artifact.Validate(); err != nil {
		return err
	}
	return s.withTransaction(ctx, "add Clip reference media", func(tx *sql.Tx) error {
		clip, err := transactionClip(ctx, tx, clipID)
		if err != nil {
			return err
		}
		if clip.IsDeleted() || clip.ProjectID != artifact.ProjectID {
			return ErrClipNotFound
		}
		stored, err := transactionArtifact(ctx, tx, artifact.ID)
		if errors.Is(err, ErrArtifactNotFound) {
			if err := insertArtifact(ctx, tx, artifact); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if stored != artifact {
			return errors.New("Artifact metadata is immutable")
		}
		var deleted sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT deleted_at_ms FROM clip_reference_media WHERE clip_id=? AND artifact_id=?", clipID.String(), artifact.ID.String()).Scan(&deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, "INSERT INTO clip_reference_media (clip_id,artifact_id,created_at_ms,deleted_at_ms) VALUES (?,?,?,NULL)", clipID.String(), artifact.ID.String(), at.UnixMilli())
		case err != nil:
			return err
		case deleted.Valid:
			_, err = tx.ExecContext(ctx, "UPDATE clip_reference_media SET deleted_at_ms=NULL,created_at_ms=? WHERE clip_id=? AND artifact_id=?", at.UnixMilli(), clipID.String(), artifact.ID.String())
		default:
			return nil
		}
		if err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, at)
	})
}

func (s *Store) DeleteClipReferenceArtifact(ctx context.Context, clipID domain.ClipID, artifactID domain.ArtifactID, at time.Time) error {
	if clipID.Validate() != nil || artifactID.Validate() != nil {
		return domain.ErrInvalidArtifact
	}
	return s.withTransaction(ctx, "delete Clip reference media", func(tx *sql.Tx) error {
		clip, err := transactionClip(ctx, tx, clipID)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE clip_reference_media SET deleted_at_ms=? WHERE clip_id=? AND artifact_id=? AND deleted_at_ms IS NULL", at.UnixMilli(), clipID.String(), artifactID.String())
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrArtifactNotFound); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, at)
	})
}
