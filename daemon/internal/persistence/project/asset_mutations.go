package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrAssetRevisionConflict         = errors.New("asset revision conflict")
	ErrAssetIdentityRevisionConflict = errors.New("asset identity revision conflict")
	ErrAssetIdentityNameConflict     = errors.New("asset identity name conflict")
	ErrEpisodeAssetIdentityConflict  = errors.New("episode asset identity conflict")
	ErrInvalidAssetProvenance        = errors.New("invalid asset provenance")
	ErrAssetInUse                    = errors.New("asset is referenced by active Clips")
)

// CreateAsset atomically creates an Episode Asset and, when supplied, its new
// project-wide identity. A nil identity means the Asset uses an existing one.
func (s *Store) CreateAsset(
	ctx context.Context,
	identity *domain.AssetIdentity,
	asset domain.Asset,
	projectUpdatedAt time.Time,
) error {
	if err := asset.Validate(); err != nil {
		return fmt.Errorf("create Asset: %w", err)
	}
	if asset.IsDeleted() || asset.Revision != 1 {
		return errors.New("create Asset: a new Asset must be active at revision 1")
	}
	if identity != nil {
		if err := identity.Validate(); err != nil {
			return fmt.Errorf("create Asset: %w", err)
		}
		if identity.ID != asset.IdentityID || identity.ProjectID != asset.ProjectID || identity.Revision != 1 {
			return errors.New("create Asset: new Identity and Asset do not match")
		}
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil || asset.UpdatedAt != projectUpdatedAt {
		return errors.New("create Asset: Asset and Project timestamps must be the same valid value")
	}
	return s.withTransaction(ctx, "create Asset", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		if projectID != asset.ProjectID {
			return errors.New("Asset belongs to another Project")
		}
		if identity != nil {
			existingID, active, found, err := assetIdentityByName(ctx, transaction, identity.ProjectID, identity.Kind, identity.Name)
			if err != nil {
				return err
			}
			if found {
				if active {
					return ErrAssetIdentityNameConflict
				}
				asset.IdentityID = existingID
			} else {
				if err := insertAssetIdentity(ctx, transaction, *identity); err != nil {
					return err
				}
			}
		}
		if err := validateAssetReferences(ctx, transaction, asset, true); err != nil {
			return err
		}
		if err := ensureEpisodeIdentityAvailable(ctx, transaction, asset); err != nil {
			return err
		}
		if err := insertAsset(ctx, transaction, asset); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, transaction, "asset", asset.ID.String(), asset.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

// CreateDerivedAsset creates an inherited Episode Asset and reuses the source
// Asset's current image Artifact when one is selected. Historical versions are
// intentionally not copied.
func (s *Store) CreateDerivedAsset(ctx context.Context, asset domain.Asset, inheritedVersionID domain.MediaVersionID, projectUpdatedAt time.Time) error {
	if err := asset.Validate(); err != nil {
		return fmt.Errorf("create derived Asset: %w", err)
	}
	if asset.DerivedFromAssetID == "" || asset.IsDeleted() || asset.Revision != 1 || inheritedVersionID.Validate() != nil {
		return errors.New("create derived Asset: invalid initial state")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil || asset.UpdatedAt != projectUpdatedAt {
		return errors.New("create derived Asset: invalid timestamp")
	}
	return s.withTransaction(ctx, "create derived Asset", func(transaction *sql.Tx) error {
		projectID, err := transactionProjectID(ctx, transaction)
		if err != nil {
			return err
		}
		if projectID != asset.ProjectID {
			return errors.New("Asset belongs to another Project")
		}
		if err := validateAssetReferences(ctx, transaction, asset, true); err != nil {
			return err
		}
		if err := ensureEpisodeIdentityAvailable(ctx, transaction, asset); err != nil {
			return err
		}
		if err := insertAsset(ctx, transaction, asset); err != nil {
			return err
		}
		sourceSlotID, err := domain.FixedMediaSlotID(asset.DerivedFromAssetID.String(), domain.MediaSlotAssetPrimaryImage)
		if err != nil {
			return err
		}
		targetSlotID, err := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
		if err != nil {
			return err
		}
		var sourceVersionID sql.NullString
		if err := transaction.QueryRowContext(ctx, "SELECT current_media_version_id FROM media_slots WHERE media_slot_id=?", sourceSlotID.String()).Scan(&sourceVersionID); err != nil {
			return err
		}
		if sourceVersionID.Valid {
			var artifactID string
			if err := transaction.QueryRowContext(ctx, "SELECT artifact_id FROM media_versions WHERE media_version_id=? AND media_slot_id=? AND deleted_at_ms IS NULL", sourceVersionID.String, sourceSlotID.String()).Scan(&artifactID); err != nil {
				return err
			}
			if _, err := transaction.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,1,0,0,1,?,?,NULL)`, inheritedVersionID.String(), targetSlotID.String(), artifactID, projectUpdatedAt.UnixMilli(), projectUpdatedAt.UnixMilli()); err != nil {
				return err
			}
			if _, err := transaction.ExecContext(ctx, "UPDATE media_slots SET current_media_version_id=?,revision=2,updated_at_ms=? WHERE media_slot_id=?", inheritedVersionID.String(), projectUpdatedAt.UnixMilli(), targetSlotID.String()); err != nil {
				return err
			}
		}
		if err := markManualResourceHead(ctx, transaction, "asset", asset.ID.String(), asset.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, projectID, projectUpdatedAt)
	})
}

// UpdateAsset persists prompt, identity-link, or recycle-bin state changes
// while retaining immutable ownership and provenance fields.
func (s *Store) UpdateAsset(
	ctx context.Context,
	asset domain.Asset,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	if err := asset.Validate(); err != nil {
		return fmt.Errorf("update Asset: %w", err)
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || asset.Revision != expectedRevision+1 {
		return errors.New("update Asset: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil || asset.UpdatedAt != projectUpdatedAt {
		return errors.New("update Asset: Asset and Project timestamps must be the same valid value")
	}
	return s.withTransaction(ctx, "update Asset", func(transaction *sql.Tx) error {
		current, err := transactionAsset(ctx, transaction, asset.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrAssetRevisionConflict
		}
		if current.ProjectID != asset.ProjectID || current.EpisodeID != asset.EpisodeID ||
			current.CreatedAt != asset.CreatedAt || current.DerivedFromAssetID != asset.DerivedFromAssetID {
			return errors.New("immutable Asset fields changed")
		}
		if err := validateAssetReferences(ctx, transaction, asset, false); err != nil {
			return err
		}
		if !asset.IsDeleted() {
			if err := ensureEpisodeIdentityAvailable(ctx, transaction, asset); err != nil {
				return err
			}
		}
		var deletedAtMS any
		if asset.DeletedAt != nil {
			deletedAtMS = asset.DeletedAt.UnixMilli()
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE assets
SET asset_identity_id = ?, prompt = ?, revision = ?, updated_at_ms = ?, deleted_at_ms = ?
WHERE asset_id = ? AND revision = ?
`, asset.IdentityID.String(), asset.Prompt, asset.Revision, asset.UpdatedAt.UnixMilli(), deletedAtMS,
			asset.ID.String(), expectedRevision)
		if err != nil {
			return fmt.Errorf("write Asset: %w", err)
		}
		if err := requireChangedAsset(result); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, transaction, "asset", asset.ID.String(), asset.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, asset.ProjectID, projectUpdatedAt)
	})
}

// DeleteAsset performs the usage check and soft deletion in one transaction,
// so a Clip reference cannot appear between confirmation and mutation.
func (s *Store) DeleteAsset(
	ctx context.Context,
	asset domain.Asset,
	expectedRevision int64,
	confirmReferenced bool,
	projectUpdatedAt time.Time,
) error {
	if err := asset.Validate(); err != nil {
		return fmt.Errorf("delete Asset: %w", err)
	}
	if !asset.IsDeleted() || asset.DeletedAt == nil || *asset.DeletedAt != projectUpdatedAt {
		return errors.New("delete Asset: Asset must contain the operation deletion time")
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || asset.Revision != expectedRevision+1 {
		return errors.New("delete Asset: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil || asset.UpdatedAt != projectUpdatedAt {
		return errors.New("delete Asset: Asset and Project timestamps must be the same valid value")
	}
	return s.withTransaction(ctx, "delete Asset", func(transaction *sql.Tx) error {
		current, err := transactionAsset(ctx, transaction, asset.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrAssetRevisionConflict
		}
		if current.IsDeleted() {
			return errors.New("Asset is already deleted")
		}
		if current.ProjectID != asset.ProjectID || current.EpisodeID != asset.EpisodeID ||
			current.IdentityID != asset.IdentityID || current.CreatedAt != asset.CreatedAt ||
			current.DerivedFromAssetID != asset.DerivedFromAssetID || current.Prompt != asset.Prompt {
			return errors.New("immutable or unrelated Asset fields changed")
		}
		if !confirmReferenced {
			var referenceCount int
			if err := transaction.QueryRowContext(ctx, `
SELECT count(*)
FROM clip_asset_references r
JOIN clips c ON c.clip_id = r.clip_id
JOIN scenes s ON s.scene_id = c.scene_id
WHERE r.asset_id = ? AND c.deleted_at_ms IS NULL AND s.deleted_at_ms IS NULL
`, asset.ID.String()).Scan(&referenceCount); err != nil {
				return fmt.Errorf("inspect Asset usage: %w", err)
			}
			if referenceCount > 0 {
				return ErrAssetInUse
			}
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE assets
SET revision = ?, updated_at_ms = ?, deleted_at_ms = ?
WHERE asset_id = ? AND revision = ? AND deleted_at_ms IS NULL
`, asset.Revision, asset.UpdatedAt.UnixMilli(), asset.DeletedAt.UnixMilli(), asset.ID.String(), expectedRevision)
		if err != nil {
			return fmt.Errorf("write deleted Asset: %w", err)
		}
		if err := requireChangedAsset(result); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, transaction, "asset", asset.ID.String(), asset.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, asset.ProjectID, projectUpdatedAt)
	})
}

func (s *Store) UpdateAssetIdentity(
	ctx context.Context,
	identity domain.AssetIdentity,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("update Asset Identity: %w", err)
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 || identity.Revision != expectedRevision+1 {
		return errors.New("update Asset Identity: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil || identity.UpdatedAt != projectUpdatedAt {
		return errors.New("update Asset Identity: Identity and Project timestamps must be the same valid value")
	}
	return s.withTransaction(ctx, "update Asset Identity", func(transaction *sql.Tx) error {
		current, err := transactionAssetIdentity(ctx, transaction, identity.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrAssetIdentityRevisionConflict
		}
		if current.ProjectID != identity.ProjectID || current.Kind != identity.Kind || current.CreatedAt != identity.CreatedAt {
			return errors.New("immutable Asset Identity fields changed")
		}
		if err := ensureIdentityNameAvailable(ctx, transaction, identity); err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE asset_identities
SET name = ?, revision = ?, updated_at_ms = ?
WHERE asset_identity_id = ? AND revision = ?
`, identity.Name, identity.Revision, identity.UpdatedAt.UnixMilli(), identity.ID.String(), expectedRevision)
		if err != nil {
			return fmt.Errorf("write Asset Identity: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read affected Asset Identity count: %w", err)
		}
		if changed != 1 {
			return ErrAssetIdentityRevisionConflict
		}
		rows, err := transaction.QueryContext(ctx, `SELECT asset_id,revision FROM assets WHERE asset_identity_id=? AND deleted_at_ms IS NULL`, identity.ID.String())
		if err != nil {
			return err
		}
		type identityAsset struct {
			id       string
			revision int64
		}
		var affected []identityAsset
		for rows.Next() {
			var asset identityAsset
			if err := rows.Scan(&asset.id, &asset.revision); err != nil {
				rows.Close()
				return err
			}
			affected = append(affected, asset)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, asset := range affected {
			if err := markManualResourceHead(ctx, transaction, "asset", asset.id, asset.revision, projectUpdatedAt); err != nil {
				return err
			}
		}
		return updateProjectTimestamp(ctx, transaction, identity.ProjectID, projectUpdatedAt)
	})
}

// DetachAssetIdentity atomically creates a new Identity and relinks one Asset.
func (s *Store) DetachAssetIdentity(
	ctx context.Context,
	identity domain.AssetIdentity,
	asset domain.Asset,
	expectedAssetRevision int64,
	projectUpdatedAt time.Time,
) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("detach Asset Identity: %w", err)
	}
	if identity.Revision != 1 || identity.ID != asset.IdentityID || identity.ProjectID != asset.ProjectID {
		return errors.New("detach Asset Identity: new Identity and Asset do not match")
	}
	if err := asset.Validate(); err != nil {
		return fmt.Errorf("detach Asset Identity: %w", err)
	}
	if expectedAssetRevision < 1 || expectedAssetRevision == math.MaxInt64 || asset.Revision != expectedAssetRevision+1 {
		return errors.New("detach Asset Identity: expected and next revisions are invalid")
	}
	if err := validateMutationTime(projectUpdatedAt); err != nil ||
		identity.UpdatedAt != projectUpdatedAt || asset.UpdatedAt != projectUpdatedAt {
		return errors.New("detach Asset Identity: timestamps must be the same valid value")
	}
	return s.withTransaction(ctx, "detach Asset Identity", func(transaction *sql.Tx) error {
		current, err := transactionAsset(ctx, transaction, asset.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedAssetRevision {
			return ErrAssetRevisionConflict
		}
		if current.ProjectID != asset.ProjectID || current.EpisodeID != asset.EpisodeID ||
			current.CreatedAt != asset.CreatedAt || current.DerivedFromAssetID != asset.DerivedFromAssetID ||
			current.Prompt != asset.Prompt || (current.DeletedAt == nil) != (asset.DeletedAt == nil) {
			return errors.New("detach Asset Identity changed fields other than Identity")
		}
		if err := ensureIdentityNameAvailable(ctx, transaction, identity); err != nil {
			return err
		}
		if err := insertAssetIdentity(ctx, transaction, identity); err != nil {
			return err
		}
		if !asset.IsDeleted() {
			if err := ensureEpisodeIdentityAvailable(ctx, transaction, asset); err != nil {
				return err
			}
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE assets
SET asset_identity_id = ?, revision = ?, updated_at_ms = ?
WHERE asset_id = ? AND revision = ?
`, asset.IdentityID.String(), asset.Revision, asset.UpdatedAt.UnixMilli(), asset.ID.String(), expectedAssetRevision)
		if err != nil {
			return fmt.Errorf("relink detached Asset: %w", err)
		}
		if err := requireChangedAsset(result); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, transaction, "asset", asset.ID.String(), asset.Revision, projectUpdatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, transaction, asset.ProjectID, projectUpdatedAt)
	})
}

func insertAssetIdentity(ctx context.Context, transaction *sql.Tx, identity domain.AssetIdentity) error {
	_, err := transaction.ExecContext(ctx, `
INSERT INTO asset_identities (
    asset_identity_id, project_id, kind, name, revision, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, identity.ID.String(), identity.ProjectID.String(), string(identity.Kind), identity.Name,
		identity.Revision, identity.CreatedAt.UnixMilli(), identity.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert Asset Identity: %w", err)
	}
	return nil
}

func insertAsset(ctx context.Context, transaction *sql.Tx, asset domain.Asset) error {
	var source any
	if asset.DerivedFromAssetID != "" {
		source = asset.DerivedFromAssetID.String()
	}
	_, err := transaction.ExecContext(ctx, `
INSERT INTO assets (
    asset_id, project_id, episode_id, asset_identity_id, derived_from_asset_id,
    prompt, revision, created_at_ms, updated_at_ms, deleted_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
`, asset.ID.String(), asset.ProjectID.String(), asset.EpisodeID.String(), asset.IdentityID.String(), source,
		asset.Prompt, asset.Revision, asset.CreatedAt.UnixMilli(), asset.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert Asset: %w", err)
	}
	return insertFixedMediaSlot(ctx, transaction, asset.ProjectID, asset.EpisodeID, domain.MediaOwnerAsset, asset.ID.String(), domain.MediaSlotAssetPrimaryImage, asset.CreatedAt)
}

func ensureIdentityNameAvailable(ctx context.Context, transaction *sql.Tx, identity domain.AssetIdentity) error {
	var existing string
	err := transaction.QueryRowContext(ctx, `
SELECT asset_identity_id
FROM asset_identities
WHERE project_id = ? AND kind = ? AND name = ? AND asset_identity_id <> ?
`, identity.ProjectID.String(), string(identity.Kind), identity.Name, identity.ID.String()).Scan(&existing)
	if err == nil {
		return ErrAssetIdentityNameConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect Asset Identity name: %w", err)
	}
	return nil
}

// assetIdentityByName distinguishes an active project identity from one whose
// Assets are all in the recycle bin. Creation may reuse a dormant identity so
// deleted Assets neither appear in normal reads nor reserve their old name.
func assetIdentityByName(ctx context.Context, transaction *sql.Tx, projectID domain.ProjectID, kind domain.AssetKind, name string) (domain.AssetIdentityID, bool, bool, error) {
	var rawID string
	var active int
	err := transaction.QueryRowContext(ctx, `
SELECT i.asset_identity_id,
       EXISTS(SELECT 1 FROM assets a WHERE a.asset_identity_id=i.asset_identity_id AND a.deleted_at_ms IS NULL)
FROM asset_identities i
WHERE i.project_id=? AND i.kind=? AND i.name=?
`, projectID.String(), string(kind), name).Scan(&rawID, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("inspect Asset Identity name: %w", err)
	}
	id, err := domain.ParseAssetIdentityID(rawID)
	if err != nil {
		return "", false, false, invalidStoredData("Asset Identity ID", err)
	}
	return id, active != 0, true, nil
}

func ensureEpisodeIdentityAvailable(ctx context.Context, transaction *sql.Tx, asset domain.Asset) error {
	var existing string
	err := transaction.QueryRowContext(ctx, `
SELECT asset_id
FROM assets
WHERE episode_id = ? AND asset_identity_id = ? AND deleted_at_ms IS NULL AND asset_id <> ?
`, asset.EpisodeID.String(), asset.IdentityID.String(), asset.ID.String()).Scan(&existing)
	if err == nil {
		return ErrEpisodeAssetIdentityConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect Episode Asset Identity: %w", err)
	}
	return nil
}

func validateAssetReferences(ctx context.Context, transaction *sql.Tx, asset domain.Asset, validateProvenance bool) error {
	var episodeProjectID string
	var targetSequence int
	if err := transaction.QueryRowContext(ctx, `
SELECT project_id, sequence_number FROM episodes WHERE episode_id = ?
`, asset.EpisodeID.String()).Scan(&episodeProjectID, &targetSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEpisodeNotFound
		}
		return fmt.Errorf("read Asset Episode: %w", err)
	}
	if episodeProjectID != asset.ProjectID.String() {
		return errors.New("Asset Episode belongs to another Project")
	}
	var identityProjectID string
	if err := transaction.QueryRowContext(ctx, `
SELECT project_id FROM asset_identities WHERE asset_identity_id = ?
`, asset.IdentityID.String()).Scan(&identityProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAssetIdentityNotFound
		}
		return fmt.Errorf("read Asset Identity ownership: %w", err)
	}
	if identityProjectID != asset.ProjectID.String() {
		return errors.New("Asset Identity belongs to another Project")
	}
	if !validateProvenance || asset.DerivedFromAssetID == "" {
		return nil
	}
	var sourceProjectID, sourceIdentityID string
	var sourceSequence int
	var sourceDeletedAt sql.NullInt64
	if err := transaction.QueryRowContext(ctx, `
SELECT a.project_id, a.asset_identity_id, e.sequence_number, a.deleted_at_ms
FROM assets a
JOIN episodes e ON e.episode_id = a.episode_id
WHERE a.asset_id = ?
`, asset.DerivedFromAssetID.String()).Scan(
		&sourceProjectID, &sourceIdentityID, &sourceSequence, &sourceDeletedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAssetNotFound
		}
		return fmt.Errorf("read source Asset: %w", err)
	}
	if sourceProjectID != asset.ProjectID.String() || sourceIdentityID != asset.IdentityID.String() ||
		sourceSequence >= targetSequence || sourceDeletedAt.Valid {
		return ErrInvalidAssetProvenance
	}
	return nil
}

func transactionAsset(ctx context.Context, transaction *sql.Tx, assetID domain.AssetID) (domain.Asset, error) {
	var projectID, episodeID, identityID string
	var derivedFrom sql.NullString
	var prompt string
	var revision, createdAtMS, updatedAtMS int64
	var deletedAtMS sql.NullInt64
	if err := transaction.QueryRowContext(ctx, `
SELECT project_id, episode_id, asset_identity_id, derived_from_asset_id,
       prompt, revision, created_at_ms, updated_at_ms, deleted_at_ms
FROM assets WHERE asset_id = ?
`, assetID.String()).Scan(
		&projectID, &episodeID, &identityID, &derivedFrom,
		&prompt, &revision, &createdAtMS, &updatedAtMS, &deletedAtMS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Asset{}, ErrAssetNotFound
		}
		return domain.Asset{}, fmt.Errorf("read current Asset: %w", err)
	}
	ownerID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.Asset{}, invalidStoredData("Asset Project ID", err)
	}
	ownerEpisodeID, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.Asset{}, invalidStoredData("Asset Episode ID", err)
	}
	parsedIdentityID, err := domain.ParseAssetIdentityID(identityID)
	if err != nil {
		return domain.Asset{}, invalidStoredData("Asset Identity ID", err)
	}
	var sourceID domain.AssetID
	if derivedFrom.Valid {
		sourceID, err = domain.ParseAssetID(derivedFrom.String)
		if err != nil {
			return domain.Asset{}, invalidStoredData("Asset provenance", err)
		}
	}
	var deletedAt *time.Time
	if deletedAtMS.Valid {
		value := time.UnixMilli(deletedAtMS.Int64).UTC()
		deletedAt = &value
	}
	asset := domain.Asset{
		ID: assetID, ProjectID: ownerID, EpisodeID: ownerEpisodeID, IdentityID: parsedIdentityID,
		DerivedFromAssetID: sourceID, Prompt: prompt, Revision: revision,
		CreatedAt: time.UnixMilli(createdAtMS).UTC(), UpdatedAt: time.UnixMilli(updatedAtMS).UTC(), DeletedAt: deletedAt,
	}
	if err := asset.Validate(); err != nil {
		return domain.Asset{}, invalidStoredData("Asset", err)
	}
	return asset, nil
}

func transactionAssetIdentity(ctx context.Context, transaction *sql.Tx, identityID domain.AssetIdentityID) (domain.AssetIdentity, error) {
	identity, err := scanAssetIdentity(transaction.QueryRowContext(ctx, `
SELECT asset_identity_id, project_id, kind, name, revision, created_at_ms, updated_at_ms
FROM asset_identities WHERE asset_identity_id = ?
`, identityID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AssetIdentity{}, ErrAssetIdentityNotFound
	}
	return identity, err
}

func requireChangedAsset(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected Asset count: %w", err)
	}
	if changed != 1 {
		return ErrAssetRevisionConflict
	}
	return nil
}
