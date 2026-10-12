package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Store) CreateAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, updatedAt time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Revision != 1 || value.IsDeleted() || value.UpdatedAt != updatedAt {
		return errors.New("invalid initial AudioReferenceAsset")
	}
	return s.withTransaction(ctx, "create AudioReferenceAsset", func(tx *sql.Tx) error {
		if err := validateAudioCharacter(ctx, tx, value); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO audio_reference_assets (audio_reference_asset_id,project_id,episode_id,name,character_asset_id,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,?,?, ?,NULL)`, value.ID.String(), value.ProjectID.String(), value.EpisodeID.String(), value.Name, nullableAssetID(value.CharacterAssetID), value.Revision, value.CreatedAt.UnixMilli(), value.UpdatedAt.UnixMilli())
		if err != nil {
			return err
		}
		if err := insertFixedMediaSlot(ctx, tx, value.ProjectID, value.EpisodeID, domain.MediaOwnerAudioReference, value.ID.String(), domain.MediaSlotAudioPrimary, value.CreatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.ProjectID, updatedAt)
	})
}

func (s *Store) UpdateAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, expectedRevision int64, updatedAt time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || value.Revision != expectedRevision+1 || value.UpdatedAt != updatedAt || value.IsDeleted() {
		return errors.New("invalid updated AudioReferenceAsset")
	}
	return s.withTransaction(ctx, "update AudioReferenceAsset", func(tx *sql.Tx) error {
		current, err := transactionAudioReference(ctx, tx, value.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.IsDeleted() {
			return ErrAudioReferenceRevisionConflict
		}
		if current.ProjectID != value.ProjectID || current.EpisodeID != value.EpisodeID {
			return errors.New("cannot move AudioReferenceAsset")
		}
		if err := validateAudioCharacter(ctx, tx, value); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE audio_reference_assets SET name=?,character_asset_id=?,revision=?,updated_at_ms=? WHERE audio_reference_asset_id=? AND revision=?", value.Name, nullableAssetID(value.CharacterAssetID), value.Revision, updatedAt.UnixMilli(), value.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrAudioReferenceRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.ProjectID, updatedAt)
	})
}

func (s *Store) DeleteAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, expectedRevision int64, updatedAt time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.DeletedAt == nil || value.Revision != expectedRevision+1 || *value.DeletedAt != updatedAt || value.UpdatedAt != updatedAt {
		return errors.New("invalid deleted AudioReferenceAsset")
	}
	return s.withTransaction(ctx, "delete AudioReferenceAsset", func(tx *sql.Tx) error {
		current, err := transactionAudioReference(ctx, tx, value.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.IsDeleted() {
			return ErrAudioReferenceRevisionConflict
		}
		var references int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM clip_audio_references WHERE audio_reference_asset_id=?", value.ID.String()).Scan(&references); err != nil {
			return err
		}
		if references != 0 {
			return ErrAudioReferenceInUse
		}
		result, err := tx.ExecContext(ctx, "UPDATE audio_reference_assets SET revision=?,updated_at_ms=?,deleted_at_ms=? WHERE audio_reference_asset_id=? AND revision=?", value.Revision, updatedAt.UnixMilli(), updatedAt.UnixMilli(), value.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrAudioReferenceRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.ProjectID, updatedAt)
	})
}

func (s *Store) RestoreAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, expectedRevision int64, updatedAt time.Time) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.DeletedAt != nil || value.Revision != expectedRevision+1 || value.UpdatedAt != updatedAt {
		return errors.New("invalid restored AudioReferenceAsset")
	}
	return s.withTransaction(ctx, "restore AudioReferenceAsset", func(tx *sql.Tx) error {
		current, err := transactionAudioReference(ctx, tx, value.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || !current.IsDeleted() {
			return ErrAudioReferenceRevisionConflict
		}
		if err := validateAudioCharacter(ctx, tx, value); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE audio_reference_assets SET name=?,character_asset_id=?,revision=?,updated_at_ms=?,deleted_at_ms=NULL WHERE audio_reference_asset_id=? AND revision=?", value.Name, nullableAssetID(value.CharacterAssetID), value.Revision, updatedAt.UnixMilli(), value.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrAudioReferenceRevisionConflict); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, value.ProjectID, updatedAt)
	})
}

func (s *Store) SetClipAudioReference(ctx context.Context, clipID domain.ClipID, audioID domain.AudioReferenceAssetID, expectedClipRevision int64, updatedAt time.Time) error {
	return s.withTransaction(ctx, "set Clip audio reference", func(tx *sql.Tx) error {
		clip, err := transactionClip(ctx, tx, clipID)
		if err != nil {
			return err
		}
		if clip.Revision != expectedClipRevision {
			return ErrClipRevisionConflict
		}
		if clip.IsDeleted() {
			return errors.New("Clip is deleted")
		}
		audio, err := transactionAudioReference(ctx, tx, audioID)
		if err != nil {
			return err
		}
		if audio.IsDeleted() || audio.ProjectID != clip.ProjectID || audio.EpisodeID != clip.EpisodeID {
			return errors.New("AudioReferenceAsset is unavailable for Clip")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO clip_audio_references (clip_id,audio_reference_asset_id) VALUES (?,?) ON CONFLICT(clip_id) DO UPDATE SET audio_reference_asset_id=excluded.audio_reference_asset_id`, clipID.String(), audioID.String())
		if err != nil {
			return err
		}
		if err := incrementClipRevision(ctx, tx, clip, expectedClipRevision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), expectedClipRevision+1, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func (s *Store) ClearClipAudioReference(ctx context.Context, clipID domain.ClipID, expectedClipRevision int64, updatedAt time.Time) error {
	return s.withTransaction(ctx, "clear Clip audio reference", func(tx *sql.Tx) error {
		clip, err := transactionClip(ctx, tx, clipID)
		if err != nil {
			return err
		}
		if clip.Revision != expectedClipRevision {
			return ErrClipRevisionConflict
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM clip_audio_references WHERE clip_id=?", clipID.String())
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrAudioReferenceNotFound); err != nil {
			return err
		}
		if err := incrementClipRevision(ctx, tx, clip, expectedClipRevision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), expectedClipRevision+1, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func transactionAudioReference(ctx context.Context, tx *sql.Tx, id domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error) {
	value, err := scanAudioReference(tx.QueryRowContext(ctx, audioReferenceColumns+" WHERE audio_reference_asset_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AudioReferenceAsset{}, ErrAudioReferenceNotFound
	}
	return value, err
}

func validateAudioCharacter(ctx context.Context, tx *sql.Tx, value domain.AudioReferenceAsset) error {
	if value.CharacterAssetID == "" {
		return nil
	}
	var projectID, episodeID, kind string
	var deleted sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT assets.project_id,assets.episode_id,asset_identities.kind,assets.deleted_at_ms FROM assets JOIN asset_identities ON asset_identities.asset_identity_id=assets.asset_identity_id WHERE assets.asset_id=?`, value.CharacterAssetID.String()).Scan(&projectID, &episodeID, &kind, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAssetNotFound
	}
	if err != nil {
		return err
	}
	if deleted.Valid || projectID != value.ProjectID.String() || episodeID != value.EpisodeID.String() || kind != string(domain.AssetKindCharacter) {
		return errors.New("character Asset is unavailable")
	}
	return nil
}

func incrementClipRevision(ctx context.Context, tx *sql.Tx, clip domain.Clip, expected int64, updatedAt time.Time) error {
	result, err := tx.ExecContext(ctx, "UPDATE clips SET revision=revision+1,updated_at_ms=? WHERE clip_id=? AND revision=?", updatedAt.UnixMilli(), clip.ID.String(), expected)
	if err != nil {
		return err
	}
	return requireOne(result, ErrClipRevisionConflict)
}

func nullableAssetID(value domain.AssetID) any {
	if value == "" {
		return nil
	}
	return value.String()
}
