package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrMediaSlotNotFound                     = errors.New("media slot not found")
	ErrArtifactNotFound                      = errors.New("artifact not found")
	ErrMediaVersionNotFound                  = errors.New("media version not found")
	ErrMediaSlotRevisionConflict             = errors.New("media slot revision conflict")
	ErrMediaVersionRevisionConflict          = errors.New("media version revision conflict")
	ErrMediaKindMismatch                     = errors.New("media kind does not match slot")
	ErrMediaAspectRatioMismatch              = errors.New("media aspect ratio does not match project output")
	ErrMediaOutputSpecMismatch               = errors.New("media dimensions do not match project output")
	ErrMediaVersionAlreadyExists             = errors.New("Artifact already has an active version in the slot")
	ErrAudioReferenceNotFound                = errors.New("audio reference asset not found")
	ErrAudioReferenceRevisionConflict        = errors.New("audio reference asset revision conflict")
	ErrAudioReferenceInUse                   = errors.New("audio reference asset is bound to a Clip")
	ErrEpisodeReferenceMediaRevisionConflict = errors.New("episode reference media revision conflict")
)

func insertFixedMediaSlot(ctx context.Context, tx *sql.Tx, projectID domain.ProjectID, episodeID domain.EpisodeID, ownerKind domain.MediaOwnerKind, ownerID string, kind domain.MediaSlotKind, at time.Time) error {
	id, err := domain.FixedMediaSlotID(ownerID, kind)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO media_slots (media_slot_id,project_id,episode_id,owner_kind,owner_id,slot_kind,current_media_version_id,revision,created_at_ms,updated_at_ms) VALUES (?,?,?,?,?,?,NULL,1,?,?)`, id.String(), projectID.String(), episodeID.String(), string(ownerKind), ownerID, string(kind), at.UnixMilli(), at.UnixMilli())
	return err
}

func (s *Store) AddMediaVersion(ctx context.Context, artifact domain.Artifact, version domain.MediaVersion, expectedSlotRevision int64, makeCurrent bool, updatedAt time.Time) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	if err := version.Validate(); err != nil {
		return err
	}
	if artifact.ID != version.ArtifactID || version.Revision != 1 || version.IsDeleted() {
		return errors.New("invalid initial MediaVersion")
	}
	if expectedSlotRevision < 1 || expectedSlotRevision == math.MaxInt64 || updatedAt != version.UpdatedAt {
		return errors.New("invalid MediaSlot revision or timestamp")
	}
	return s.withTransaction(ctx, "add MediaVersion", func(tx *sql.Tx) error {
		slot, err := transactionMediaSlot(ctx, tx, version.SlotID)
		if err != nil {
			return err
		}
		if slot.Revision != expectedSlotRevision {
			return ErrMediaSlotRevisionConflict
		}
		if err := validateMediaOwnerActive(ctx, tx, slot); err != nil {
			return err
		}
		if slot.ProjectID != artifact.ProjectID || slot.Kind.MediaKind() != artifact.Kind {
			return ErrMediaKindMismatch
		}
		if slot.Kind == domain.MediaSlotClipFirstImage || slot.Kind == domain.MediaSlotClipLastImage {
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
		}
		if err := validateMediaTrim(slot.Kind, artifact, version); err != nil {
			return err
		}
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT artifact_id FROM artifacts WHERE sha256=?", artifact.SHA256).Scan(&existing)
		if err == nil && existing != artifact.ID.String() {
			return errors.New("checksum belongs to another Artifact")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			if err := insertArtifact(ctx, tx, artifact); err != nil {
				return err
			}
		} else {
			stored, err := transactionArtifact(ctx, tx, artifact.ID)
			if err != nil {
				return err
			}
			if stored != artifact {
				return errors.New("Artifact metadata is immutable")
			}
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
		if version.SequenceNumber != count+1 {
			return errors.New("invalid MediaVersion sequence")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,?,?,?,?,NULL)`, version.ID.String(), version.SlotID.String(), version.ArtifactID.String(), version.SequenceNumber, version.TrimInMilliseconds, version.TrimOutMilliseconds, version.Revision, version.CreatedAt.UnixMilli(), version.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		var current any
		if makeCurrent {
			current = version.ID.String()
		} else if slot.CurrentVersionID != "" {
			current = slot.CurrentVersionID.String()
		}
		result, err := tx.ExecContext(ctx, "UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=? AND revision=?", current, updatedAt.UnixMilli(), slot.ID.String(), expectedSlotRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrMediaSlotRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, slot.ProjectID, updatedAt)
	})
}

func (s *Store) SelectMediaVersion(ctx context.Context, slotID domain.MediaSlotID, versionID domain.MediaVersionID, expectedSlotRevision int64, updatedAt time.Time) error {
	return s.withTransaction(ctx, "select MediaVersion", func(tx *sql.Tx) error {
		slot, err := transactionMediaSlot(ctx, tx, slotID)
		if err != nil {
			return err
		}
		if slot.Revision != expectedSlotRevision {
			return ErrMediaSlotRevisionConflict
		}
		if err := validateMediaOwnerActive(ctx, tx, slot); err != nil {
			return err
		}
		version, err := transactionMediaVersion(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if version.SlotID != slotID || version.IsDeleted() {
			return errors.New("MediaVersion is unavailable for Slot")
		}
		result, err := tx.ExecContext(ctx, "UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=? AND revision=?", versionID.String(), updatedAt.UnixMilli(), slotID.String(), expectedSlotRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrMediaSlotRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, slot.ProjectID, updatedAt)
	})
}

func (s *Store) UpdateVideoTrim(ctx context.Context, version domain.MediaVersion, expectedRevision int64, updatedAt time.Time) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version.Revision != expectedRevision+1 || version.UpdatedAt != updatedAt {
		return errors.New("invalid version revision")
	}
	return s.withTransaction(ctx, "update video trim", func(tx *sql.Tx) error {
		current, err := transactionMediaVersion(ctx, tx, version.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrMediaVersionRevisionConflict
		}
		slot, err := transactionMediaSlot(ctx, tx, current.SlotID)
		if err != nil {
			return err
		}
		if err := validateMediaOwnerActive(ctx, tx, slot); err != nil {
			return err
		}
		artifact, err := transactionArtifact(ctx, tx, current.ArtifactID)
		if err != nil {
			return err
		}
		if slot.Kind != domain.MediaSlotClipVideo {
			return ErrMediaKindMismatch
		}
		if err := validateMediaTrim(slot.Kind, artifact, version); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE media_versions SET trim_in_ms=?,trim_out_ms=?,revision=?,updated_at_ms=? WHERE media_version_id=? AND revision=?", version.TrimInMilliseconds, version.TrimOutMilliseconds, version.Revision, updatedAt.UnixMilli(), version.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrMediaVersionRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, slot.ProjectID, updatedAt)
	})
}

func (s *Store) DeleteMediaVersion(ctx context.Context, version domain.MediaVersion, expectedVersionRevision, expectedSlotRevision int64, updatedAt time.Time) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version.DeletedAt == nil || version.Revision != expectedVersionRevision+1 || version.UpdatedAt != updatedAt || *version.DeletedAt != updatedAt {
		return errors.New("invalid deleted MediaVersion")
	}
	return s.changeMediaVersionDeletion(ctx, version, expectedVersionRevision, expectedSlotRevision, updatedAt, false)
}

func (s *Store) RestoreMediaVersion(ctx context.Context, version domain.MediaVersion, expectedVersionRevision, expectedSlotRevision int64, updatedAt time.Time) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version.DeletedAt != nil || version.Revision != expectedVersionRevision+1 || version.UpdatedAt != updatedAt {
		return errors.New("invalid restored MediaVersion")
	}
	return s.changeMediaVersionDeletion(ctx, version, expectedVersionRevision, expectedSlotRevision, updatedAt, true)
}

func (s *Store) changeMediaVersionDeletion(ctx context.Context, version domain.MediaVersion, expectedVersionRevision, expectedSlotRevision int64, updatedAt time.Time, restore bool) error {
	return s.withTransaction(ctx, "change MediaVersion deletion", func(tx *sql.Tx) error {
		current, err := transactionMediaVersion(ctx, tx, version.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedVersionRevision || current.SlotID != version.SlotID || current.ArtifactID != version.ArtifactID {
			return ErrMediaVersionRevisionConflict
		}
		if restore == !current.IsDeleted() {
			return errors.New("MediaVersion has the wrong deletion state")
		}
		slot, err := transactionMediaSlot(ctx, tx, current.SlotID)
		if err != nil {
			return err
		}
		if err := validateMediaOwnerActive(ctx, tx, slot); err != nil {
			return err
		}
		if slot.Revision != expectedSlotRevision {
			return ErrMediaSlotRevisionConflict
		}
		if restore {
			var duplicate int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM media_versions WHERE media_slot_id=? AND artifact_id=? AND deleted_at_ms IS NULL", current.SlotID.String(), current.ArtifactID.String()).Scan(&duplicate)
			if err == nil {
				return ErrMediaVersionAlreadyExists
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE media_versions SET revision=?,updated_at_ms=?,deleted_at_ms=? WHERE media_version_id=? AND revision=?", version.Revision, updatedAt.UnixMilli(), nullableTime(version.DeletedAt), version.ID.String(), expectedVersionRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrMediaVersionRevisionConflict); err != nil {
			return err
		}
		var selected any
		if slot.CurrentVersionID != "" && !(current.ID == slot.CurrentVersionID && !restore) {
			selected = slot.CurrentVersionID.String()
		}
		result, err = tx.ExecContext(ctx, "UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=? AND revision=?", selected, updatedAt.UnixMilli(), slot.ID.String(), expectedSlotRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrMediaSlotRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, slot.ProjectID, updatedAt)
	})
}

func insertArtifact(ctx context.Context, tx *sql.Tx, a domain.Artifact) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO artifacts (artifact_id,project_id,media_kind,relative_path,sha256,byte_size,mime_type,original_filename,width,height,duration_ms,fps_numerator,fps_denominator,audio_sample_rate,audio_channels,remote_reference_url,remote_reference_expires_at_ms,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID.String(), a.ProjectID.String(), string(a.Kind), a.RelativePath, a.SHA256, a.ByteSize, a.MIMEType, a.OriginalFilename, a.Width, a.Height, a.DurationMilliseconds, a.FPSNumerator, a.FPSDenominator, a.AudioSampleRate, a.AudioChannels, nullableText(a.RemoteReferenceURL), nullableTime(a.RemoteReferenceExpiresAt), a.CreatedAt.UnixMilli())
	return err
}
func validateMediaTrim(kind domain.MediaSlotKind, a domain.Artifact, v domain.MediaVersion) error {
	if kind == domain.MediaSlotClipVideo {
		if v.TrimInMilliseconds < 0 || v.TrimOutMilliseconds <= v.TrimInMilliseconds || v.TrimOutMilliseconds > a.DurationMilliseconds {
			return errors.New("invalid video trim")
		}
	} else if v.TrimInMilliseconds != 0 || v.TrimOutMilliseconds != 0 {
		return errors.New("non-video versions cannot have trim")
	}
	return nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func validateMediaOwnerActive(ctx context.Context, tx *sql.Tx, slot domain.MediaSlot) error {
	var projectID, episodeID string
	var deleted sql.NullInt64
	var err error
	switch slot.OwnerKind {
	case domain.MediaOwnerAsset:
		err = tx.QueryRowContext(ctx, "SELECT project_id,episode_id,deleted_at_ms FROM assets WHERE asset_id=?", slot.OwnerID).Scan(&projectID, &episodeID, &deleted)
	case domain.MediaOwnerClip:
		err = tx.QueryRowContext(ctx, "SELECT project_id,episode_id,deleted_at_ms FROM clips WHERE clip_id=?", slot.OwnerID).Scan(&projectID, &episodeID, &deleted)
	case domain.MediaOwnerAudioReference:
		err = tx.QueryRowContext(ctx, "SELECT project_id,episode_id,deleted_at_ms FROM audio_reference_assets WHERE audio_reference_asset_id=?", slot.OwnerID).Scan(&projectID, &episodeID, &deleted)
	default:
		return errors.New("invalid MediaSlot owner")
	}
	if err != nil {
		return err
	}
	if deleted.Valid || projectID != slot.ProjectID.String() || episodeID != slot.EpisodeID.String() {
		return errors.New("MediaSlot owner is unavailable")
	}
	return nil
}
