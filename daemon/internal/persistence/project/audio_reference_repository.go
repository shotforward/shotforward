package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const audioReferenceColumns = `SELECT audio_reference_asset_id,project_id,episode_id,name,character_asset_id,revision,created_at_ms,updated_at_ms,deleted_at_ms FROM audio_reference_assets`

func (s *Store) AudioReferenceAsset(ctx context.Context, id domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	if err := id.Validate(); err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	value, err := scanAudioReference(db.QueryRowContext(ctx, audioReferenceColumns+" WHERE audio_reference_asset_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AudioReferenceAsset{}, ErrAudioReferenceNotFound
	}
	return value, err
}

func (s *Store) EpisodeAudioReferenceAssets(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.AudioReferenceAsset, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	if err := episodeID.Validate(); err != nil {
		return nil, err
	}
	predicate := " AND deleted_at_ms IS NULL"
	if includeDeleted {
		predicate = ""
	}
	rows, err := db.QueryContext(ctx, audioReferenceColumns+" WHERE episode_id=?"+predicate+" ORDER BY created_at_ms,audio_reference_asset_id", episodeID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []domain.AudioReferenceAsset{}
	for rows.Next() {
		value, err := scanAudioReference(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ClipAudioReference(ctx context.Context, clipID domain.ClipID) (domain.AudioReferenceAssetID, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return "", err
	}
	if err := clipID.Validate(); err != nil {
		return "", err
	}
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT audio_reference_asset_id FROM clip_audio_references WHERE clip_id=?", clipID.String()).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return domain.ParseAudioReferenceAssetID(raw)
}

func scanAudioReference(row rowScanner) (domain.AudioReferenceAsset, error) {
	var id, projectID, episodeID, name string
	var character sql.NullString
	var revision, created, updated int64
	var deleted sql.NullInt64
	if err := row.Scan(&id, &projectID, &episodeID, &name, &character, &revision, &created, &updated, &deleted); err != nil {
		return domain.AudioReferenceAsset{}, err
	}
	parsedID, err := domain.ParseAudioReferenceAssetID(id)
	if err != nil {
		return domain.AudioReferenceAsset{}, invalidStoredData("AudioReferenceAsset ID", err)
	}
	parsedProjectID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.AudioReferenceAsset{}, invalidStoredData("AudioReferenceAsset Project", err)
	}
	parsedEpisodeID, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.AudioReferenceAsset{}, invalidStoredData("AudioReferenceAsset Episode", err)
	}
	var characterID domain.AssetID
	if character.Valid {
		characterID, err = domain.ParseAssetID(character.String)
		if err != nil {
			return domain.AudioReferenceAsset{}, invalidStoredData("AudioReferenceAsset Character", err)
		}
	}
	var deletedAt *time.Time
	if deleted.Valid {
		value := time.UnixMilli(deleted.Int64).UTC()
		deletedAt = &value
	}
	value := domain.AudioReferenceAsset{ID: parsedID, ProjectID: parsedProjectID, EpisodeID: parsedEpisodeID, Name: name, CharacterAssetID: characterID, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC(), DeletedAt: deletedAt}
	if err := value.Validate(); err != nil {
		return domain.AudioReferenceAsset{}, invalidStoredData("AudioReferenceAsset", err)
	}
	return value, nil
}
