package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const mediaSlotColumns = `SELECT media_slot_id,project_id,episode_id,owner_kind,owner_id,slot_kind,current_media_version_id,revision,created_at_ms,updated_at_ms FROM media_slots`
const artifactColumns = `SELECT artifact_id,project_id,media_kind,relative_path,sha256,byte_size,mime_type,original_filename,width,height,duration_ms,fps_numerator,fps_denominator,audio_sample_rate,audio_channels,remote_reference_url,remote_reference_expires_at_ms,created_at_ms FROM artifacts`
const mediaVersionColumns = `SELECT media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms FROM media_versions`

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) MediaSlot(ctx context.Context, id domain.MediaSlotID, includeDeleted bool) (domain.MediaSlotDetails, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return domain.MediaSlotDetails{}, err
	}
	if err := id.Validate(); err != nil {
		return domain.MediaSlotDetails{}, err
	}
	return readSnapshot(ctx, db, "read MediaSlot", func(tx *sql.Tx) (domain.MediaSlotDetails, error) {
		slot, err := transactionMediaSlot(ctx, tx, id)
		if err != nil {
			return domain.MediaSlotDetails{}, err
		}
		predicate := " AND deleted_at_ms IS NULL"
		if includeDeleted {
			predicate = ""
		}
		rows, err := tx.QueryContext(ctx, mediaVersionColumns+" WHERE media_slot_id=?"+predicate+" ORDER BY sequence_number DESC", id.String())
		if err != nil {
			return domain.MediaSlotDetails{}, err
		}
		versions := []domain.MediaVersion{}
		for rows.Next() {
			v, err := scanMediaVersion(rows)
			if err != nil {
				rows.Close()
				return domain.MediaSlotDetails{}, err
			}
			versions = append(versions, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.MediaSlotDetails{}, err
		}
		rows.Close()
		artifacts := map[domain.ArtifactID]domain.Artifact{}
		var current *domain.MediaVersion
		for i := range versions {
			a, err := transactionArtifact(ctx, tx, versions[i].ArtifactID)
			if err != nil {
				return domain.MediaSlotDetails{}, err
			}
			artifacts[a.ID] = a
			if versions[i].ID == slot.CurrentVersionID {
				copy := versions[i]
				current = &copy
			}
		}
		result := domain.MediaSlotDetails{Slot: slot, Current: current, Versions: versions, Artifacts: artifacts}
		if err := result.Validate(); err != nil {
			return domain.MediaSlotDetails{}, invalidStoredData("MediaSlot", err)
		}
		return result, nil
	})
}

func (s *Store) Artifact(ctx context.Context, id domain.ArtifactID) (domain.Artifact, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return domain.Artifact{}, err
	}
	if err := id.Validate(); err != nil {
		return domain.Artifact{}, err
	}
	a, err := scanArtifact(db.QueryRowContext(ctx, artifactColumns+" WHERE artifact_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrArtifactNotFound
	}
	return a, err
}

// ArtifactSelectedInEpisode reports whether an active MediaSlot in the target
// Episode currently selects the immutable Artifact, its upload library keeps
// it available, or a Clip keeps it as an explicit generation reference.
func (s *Store) ArtifactSelectedInEpisode(ctx context.Context, artifactID domain.ArtifactID, episodeID domain.EpisodeID) (bool, error) {
	if artifactID.Validate() != nil || episodeID.Validate() != nil {
		return false, domain.ErrInvalidArtifact
	}
	db, err := s.requireDatabase()
	if err != nil {
		return false, err
	}
	return artifactSelectedInEpisode(ctx, db, artifactID, episodeID)
}

func artifactSelectedInEpisode(ctx context.Context, query rowQuerier, artifactID domain.ArtifactID, episodeID domain.EpisodeID) (bool, error) {
	var selected int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM media_versions AS version
		JOIN media_slots AS slot ON slot.media_slot_id=version.media_slot_id
		WHERE version.artifact_id=? AND slot.episode_id=?
		  AND version.deleted_at_ms IS NULL AND slot.current_media_version_id=version.media_version_id
		UNION ALL
		SELECT 1 FROM clip_reference_media AS reference
		JOIN clips AS clip ON clip.clip_id=reference.clip_id
		WHERE reference.artifact_id=? AND clip.episode_id=?
		  AND reference.deleted_at_ms IS NULL AND clip.deleted_at_ms IS NULL
		UNION ALL
		SELECT 1 FROM episode_reference_media AS reference
		WHERE reference.artifact_id=? AND reference.episode_id=?
		  AND reference.deleted_at_ms IS NULL
	)`, artifactID.String(), episodeID.String(), artifactID.String(), episodeID.String(), artifactID.String(), episodeID.String()).Scan(&selected)
	return selected != 0, err
}
func (s *Store) ArtifactBySHA256(ctx context.Context, projectID domain.ProjectID, sha string) (domain.Artifact, error) {
	db, err := s.requireDatabase()
	if err != nil {
		return domain.Artifact{}, err
	}
	if err := projectID.Validate(); err != nil {
		return domain.Artifact{}, err
	}
	if len(sha) != 64 {
		return domain.Artifact{}, domain.ErrInvalidArtifact
	}
	for _, value := range sha {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return domain.Artifact{}, domain.ErrInvalidArtifact
		}
	}
	a, err := scanArtifact(db.QueryRowContext(ctx, artifactColumns+" WHERE project_id=? AND sha256=?", projectID.String(), sha))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrArtifactNotFound
	}
	return a, err
}

func transactionMediaSlot(ctx context.Context, tx *sql.Tx, id domain.MediaSlotID) (domain.MediaSlot, error) {
	s, err := scanMediaSlot(tx.QueryRowContext(ctx, mediaSlotColumns+" WHERE media_slot_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaSlot{}, ErrMediaSlotNotFound
	}
	return s, err
}
func transactionArtifact(ctx context.Context, tx *sql.Tx, id domain.ArtifactID) (domain.Artifact, error) {
	a, err := scanArtifact(tx.QueryRowContext(ctx, artifactColumns+" WHERE artifact_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrArtifactNotFound
	}
	return a, err
}
func transactionMediaVersion(ctx context.Context, tx *sql.Tx, id domain.MediaVersionID) (domain.MediaVersion, error) {
	v, err := scanMediaVersion(tx.QueryRowContext(ctx, mediaVersionColumns+" WHERE media_version_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaVersion{}, ErrMediaVersionNotFound
	}
	return v, err
}

func scanMediaSlot(row rowScanner) (domain.MediaSlot, error) {
	var id, pid, eid, ownerKind, ownerID, kind string
	var current sql.NullString
	var revision, created, updated int64
	if err := row.Scan(&id, &pid, &eid, &ownerKind, &ownerID, &kind, &current, &revision, &created, &updated); err != nil {
		return domain.MediaSlot{}, err
	}
	parsed, err := domain.ParseMediaSlotID(id)
	if err != nil {
		return domain.MediaSlot{}, invalidStoredData("MediaSlot ID", err)
	}
	projectID, err := domain.ParseProjectID(pid)
	if err != nil {
		return domain.MediaSlot{}, invalidStoredData("MediaSlot Project", err)
	}
	episodeID, err := domain.ParseEpisodeID(eid)
	if err != nil {
		return domain.MediaSlot{}, invalidStoredData("MediaSlot Episode", err)
	}
	var currentID domain.MediaVersionID
	if current.Valid {
		currentID, err = domain.ParseMediaVersionID(current.String)
		if err != nil {
			return domain.MediaSlot{}, invalidStoredData("current MediaVersion", err)
		}
	}
	result := domain.MediaSlot{ID: parsed, ProjectID: projectID, EpisodeID: episodeID, OwnerKind: domain.MediaOwnerKind(ownerKind), OwnerID: ownerID, Kind: domain.MediaSlotKind(kind), CurrentVersionID: currentID, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC()}
	if err := invalidMediaOwner(result.OwnerKind, result.Kind); err != nil {
		return domain.MediaSlot{}, invalidStoredData("MediaSlot owner", err)
	}
	if err := result.Validate(); err != nil {
		return domain.MediaSlot{}, invalidStoredData("MediaSlot", err)
	}
	return result, nil
}
func scanArtifact(row rowScanner) (domain.Artifact, error) {
	var id, pid, kind, path, sha, mime, filename string
	var size int64
	var width, height, sample, channels int
	var duration, fpsN, fpsD, created int64
	var remoteURL sql.NullString
	var remoteExpires sql.NullInt64
	if err := row.Scan(&id, &pid, &kind, &path, &sha, &size, &mime, &filename, &width, &height, &duration, &fpsN, &fpsD, &sample, &channels, &remoteURL, &remoteExpires, &created); err != nil {
		return domain.Artifact{}, err
	}
	parsed, err := domain.ParseArtifactID(id)
	if err != nil {
		return domain.Artifact{}, invalidStoredData("Artifact ID", err)
	}
	projectID, err := domain.ParseProjectID(pid)
	if err != nil {
		return domain.Artifact{}, invalidStoredData("Artifact Project", err)
	}
	a := domain.Artifact{ID: parsed, ProjectID: projectID, Kind: domain.MediaKind(kind), RelativePath: path, SHA256: sha, ByteSize: size, MIMEType: mime, OriginalFilename: filename, Width: width, Height: height, DurationMilliseconds: duration, FPSNumerator: fpsN, FPSDenominator: fpsD, AudioSampleRate: sample, AudioChannels: channels, RemoteReferenceURL: remoteURL.String, CreatedAt: time.UnixMilli(created).UTC()}
	if remoteExpires.Valid {
		expiresAt := time.UnixMilli(remoteExpires.Int64).UTC()
		a.RemoteReferenceExpiresAt = &expiresAt
	}
	if err := a.Validate(); err != nil {
		return domain.Artifact{}, invalidStoredData("Artifact", err)
	}
	return a, nil
}
func scanMediaVersion(row rowScanner) (domain.MediaVersion, error) {
	var id, sid, aid string
	var seq int
	var trimIn, trimOut, revision, created, updated int64
	var deleted sql.NullInt64
	if err := row.Scan(&id, &sid, &aid, &seq, &trimIn, &trimOut, &revision, &created, &updated, &deleted); err != nil {
		return domain.MediaVersion{}, err
	}
	parsed, err := domain.ParseMediaVersionID(id)
	if err != nil {
		return domain.MediaVersion{}, invalidStoredData("MediaVersion ID", err)
	}
	slotID, err := domain.ParseMediaSlotID(sid)
	if err != nil {
		return domain.MediaVersion{}, invalidStoredData("MediaVersion Slot", err)
	}
	artifactID, err := domain.ParseArtifactID(aid)
	if err != nil {
		return domain.MediaVersion{}, invalidStoredData("MediaVersion Artifact", err)
	}
	var deletedAt *time.Time
	if deleted.Valid {
		v := time.UnixMilli(deleted.Int64).UTC()
		deletedAt = &v
	}
	v := domain.MediaVersion{ID: parsed, SlotID: slotID, ArtifactID: artifactID, SequenceNumber: seq, TrimInMilliseconds: trimIn, TrimOutMilliseconds: trimOut, Revision: revision, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC(), DeletedAt: deletedAt}
	if err := v.Validate(); err != nil {
		return domain.MediaVersion{}, invalidStoredData("MediaVersion", err)
	}
	return v, nil
}

func invalidMediaOwner(ownerKind domain.MediaOwnerKind, slotKind domain.MediaSlotKind) error {
	if ownerKind == domain.MediaOwnerAsset && slotKind == domain.MediaSlotAssetPrimaryImage {
		return nil
	}
	if ownerKind == domain.MediaOwnerClip && (slotKind == domain.MediaSlotClipFirstImage || slotKind == domain.MediaSlotClipLastImage || slotKind == domain.MediaSlotClipVideo || slotKind == domain.MediaSlotAudioPrimary) {
		return nil
	}
	if ownerKind == domain.MediaOwnerAudioReference && slotKind == domain.MediaSlotAudioPrimary {
		return nil
	}
	return fmt.Errorf("invalid owner/slot pairing")
}
