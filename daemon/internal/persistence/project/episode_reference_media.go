package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const episodeReferenceMediaColumns = `SELECT reference.episode_id,reference.name,reference.description,reference.trim_start_ms,reference.trim_end_ms,reference.revision,reference.created_at_ms,reference.updated_at_ms,reference.deleted_at_ms,
artifacts.artifact_id,artifacts.project_id,artifacts.media_kind,artifacts.relative_path,artifacts.sha256,artifacts.byte_size,artifacts.mime_type,artifacts.original_filename,artifacts.width,artifacts.height,artifacts.duration_ms,artifacts.fps_numerator,artifacts.fps_denominator,artifacts.audio_sample_rate,artifacts.audio_channels,artifacts.created_at_ms,
(SELECT count(*) FROM clip_reference_media AS clip_reference JOIN clips AS clip ON clip.clip_id=clip_reference.clip_id WHERE clip_reference.artifact_id=reference.artifact_id AND clip.episode_id=reference.episode_id AND clip_reference.deleted_at_ms IS NULL AND clip.deleted_at_ms IS NULL) AS usage_count
FROM episode_reference_media AS reference JOIN artifacts ON artifacts.artifact_id=reference.artifact_id`

func (s *Store) EpisodeReferenceMedia(ctx context.Context, episodeID domain.EpisodeID, artifactID domain.ArtifactID, includeDeleted bool) (domain.EpisodeReferenceMedia, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	predicate := " AND reference.deleted_at_ms IS NULL"
	if includeDeleted {
		predicate = ""
	}
	value, err := scanEpisodeReferenceMedia(db.QueryRowContext(ctx, episodeReferenceMediaColumns+" WHERE reference.episode_id=? AND reference.artifact_id=?"+predicate, episodeID.String(), artifactID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EpisodeReferenceMedia{}, ErrArtifactNotFound
	}
	return value, err
}

func (s *Store) EpisodeReferenceMediaList(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.EpisodeReferenceMedia, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	predicate := " WHERE reference.episode_id=? AND reference.deleted_at_ms IS NULL"
	if includeDeleted {
		predicate = " WHERE reference.episode_id=?"
	}
	rows, err := db.QueryContext(ctx, episodeReferenceMediaColumns+predicate+" ORDER BY reference.updated_at_ms DESC, reference.artifact_id", episodeID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.EpisodeReferenceMedia{}
	for rows.Next() {
		value, scanErr := scanEpisodeReferenceMedia(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) PutEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, at time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return s.withTransaction(ctx, "put Episode reference media", func(tx *sql.Tx) error {
		artifact, err := transactionArtifact(ctx, tx, value.Artifact.ID)
		if errors.Is(err, ErrArtifactNotFound) {
			if err := insertArtifact(ctx, tx, value.Artifact); err != nil {
				return err
			}
			artifact = value.Artifact
		} else if err != nil {
			return err
		} else if artifact != value.Artifact {
			return errors.New("Artifact metadata is immutable")
		}
		var projectID string
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM episodes WHERE episode_id=? AND deleted_at_ms IS NULL", value.EpisodeID.String()).Scan(&projectID); err != nil {
			return err
		}
		if projectID != artifact.ProjectID.String() {
			return ErrArtifactNotFound
		}
		var deleted sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT deleted_at_ms FROM episode_reference_media WHERE episode_id=? AND artifact_id=?", value.EpisodeID.String(), artifact.ID.String()).Scan(&deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO episode_reference_media (episode_id,artifact_id,project_id,name,description,trim_start_ms,trim_end_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,?,?,1,?,?,NULL)`, value.EpisodeID.String(), artifact.ID.String(), artifact.ProjectID.String(), value.Name, value.Description, value.TrimStartMilliseconds, value.TrimEndMilliseconds, at.UnixMilli(), at.UnixMilli())
		case err != nil:
			return err
		case deleted.Valid:
			_, err = tx.ExecContext(ctx, `UPDATE episode_reference_media SET name=?,description=?,trim_start_ms=?,trim_end_ms=?,revision=revision+1,updated_at_ms=?,deleted_at_ms=NULL WHERE episode_id=? AND artifact_id=?`, value.Name, value.Description, value.TrimStartMilliseconds, value.TrimEndMilliseconds, at.UnixMilli(), value.EpisodeID.String(), artifact.ID.String())
		default:
			return nil
		}
		if err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, artifact.ProjectID, at)
	})
}

func (s *Store) UpdateEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, expectedRevision int64, at time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return s.withTransaction(ctx, "update Episode reference media", func(tx *sql.Tx) error {
		// Recheck bounds against the persisted source, not caller-supplied
		// Artifact metadata. Trimming never changes that source record.
		artifact, err := transactionArtifact(ctx, tx, value.Artifact.ID)
		if err != nil {
			return err
		}
		if artifact.ProjectID != value.Artifact.ProjectID {
			return ErrArtifactNotFound
		}
		checked := value
		checked.Artifact = artifact
		if err := checked.Validate(); err != nil {
			return err
		}
		var previousName string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM episode_reference_media WHERE episode_id=? AND artifact_id=? AND revision=? AND deleted_at_ms IS NULL`, value.EpisodeID.String(), value.Artifact.ID.String(), expectedRevision).Scan(&previousName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEpisodeReferenceMediaRevisionConflict
			}
			return err
		}
		if previousName != value.Name {
			if err := rewriteEpisodeReferenceMentions(ctx, tx, value.EpisodeID, value.Artifact.ID, previousName, value.Name, at); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE episode_reference_media SET name=?,description=?,trim_start_ms=?,trim_end_ms=?,revision=revision+1,updated_at_ms=? WHERE episode_id=? AND artifact_id=? AND revision=? AND deleted_at_ms IS NULL`, value.Name, value.Description, value.TrimStartMilliseconds, value.TrimEndMilliseconds, at.UnixMilli(), value.EpisodeID.String(), value.Artifact.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrEpisodeReferenceMediaRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.Artifact.ProjectID, at)
	})
}

func (s *Store) DeleteEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, expectedRevision int64, at time.Time) error {
	return s.withTransaction(ctx, "delete Episode reference media", func(tx *sql.Tx) error {
		if err := rewriteEpisodeReferenceMentions(ctx, tx, value.EpisodeID, value.Artifact.ID, value.Name, "", at); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE episode_reference_media SET revision=revision+1,updated_at_ms=?,deleted_at_ms=? WHERE episode_id=? AND artifact_id=? AND revision=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), value.EpisodeID.String(), value.Artifact.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrEpisodeReferenceMediaRevisionConflict); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE clip_reference_media SET deleted_at_ms=? WHERE artifact_id=? AND deleted_at_ms IS NULL AND clip_id IN (SELECT clip_id FROM clips WHERE episode_id=?)`, at.UnixMilli(), value.Artifact.ID.String(), value.EpisodeID.String())
		if err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.Artifact.ProjectID, at)
	})
}

func rewriteEpisodeReferenceMentions(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID, artifactID domain.ArtifactID, previousName, nextName string, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT clip.clip_id,clip.reference_video_prompt,clip.revision
FROM clips AS clip
JOIN clip_reference_media AS reference ON reference.clip_id=clip.clip_id
WHERE clip.episode_id=? AND reference.artifact_id=? AND clip.deleted_at_ms IS NULL AND reference.deleted_at_ms IS NULL`, episodeID.String(), artifactID.String())
	if err != nil {
		return err
	}
	type rewrite struct {
		clipID   string
		prompt   string
		revision int64
	}
	values := []rewrite{}
	for rows.Next() {
		var value rewrite
		if err := rows.Scan(&value.clipID, &value.prompt, &value.revision); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	replacement := ""
	if nextName != "" {
		replacement = "@" + nextName
	}
	for _, value := range values {
		prompt := strings.ReplaceAll(value.prompt, "@"+previousName, replacement)
		if prompt == value.prompt {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE clips SET reference_video_prompt=?,video_prompt=CASE WHEN generation_mode='asset_reference' THEN ? ELSE video_prompt END,revision=revision+1,updated_at_ms=? WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL`, prompt, prompt, at.UnixMilli(), value.clipID, value.revision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", value.clipID, value.revision+1, at); err != nil {
			return err
		}
	}
	return nil
}

func scanEpisodeReferenceMedia(row rowScanner) (domain.EpisodeReferenceMedia, error) {
	var episodeID, name, description, artifactID, projectID, kind, relativePath, sha, mime, filename string
	var trimStart, trimEnd, revision, created, updated, artifactCreated int64
	var deleted sql.NullInt64
	var byteSize, duration, fpsNumerator, fpsDenominator int64
	var width, height, sampleRate, channels, usage int
	err := row.Scan(&episodeID, &name, &description, &trimStart, &trimEnd, &revision, &created, &updated, &deleted, &artifactID, &projectID, &kind, &relativePath, &sha, &byteSize, &mime, &filename, &width, &height, &duration, &fpsNumerator, &fpsDenominator, &sampleRate, &channels, &artifactCreated, &usage)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	parsedEpisode, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	parsedArtifact, err := domain.ParseArtifactID(artifactID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	parsedProject, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.EpisodeReferenceMedia{}, err
	}
	artifact := domain.Artifact{ID: parsedArtifact, ProjectID: parsedProject, Kind: domain.MediaKind(kind), RelativePath: relativePath, SHA256: sha, ByteSize: byteSize, MIMEType: mime, OriginalFilename: filename, Width: width, Height: height, DurationMilliseconds: duration, FPSNumerator: fpsNumerator, FPSDenominator: fpsDenominator, AudioSampleRate: sampleRate, AudioChannels: channels, CreatedAt: time.UnixMilli(artifactCreated).UTC()}
	value := domain.EpisodeReferenceMedia{EpisodeID: parsedEpisode, Artifact: artifact, Name: name, Description: description, TrimStartMilliseconds: trimStart, TrimEndMilliseconds: trimEnd, UsageCount: usage, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC(), DeletedAt: nullableMilliseconds(deleted)}
	if err := value.Validate(); err != nil {
		return domain.EpisodeReferenceMedia{}, invalidStoredData("EpisodeReferenceMedia", err)
	}
	return value, nil
}
