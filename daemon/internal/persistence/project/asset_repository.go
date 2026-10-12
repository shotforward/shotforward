package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrAssetNotFound         = errors.New("asset not found")
	ErrAssetIdentityNotFound = errors.New("asset identity not found")
)

const episodeAssetColumns = `
SELECT i.asset_identity_id, i.project_id, i.kind, i.name,
       i.revision, i.created_at_ms, i.updated_at_ms,
       a.asset_id, a.project_id, a.episode_id, a.asset_identity_id,
       a.derived_from_asset_id, a.prompt, a.revision,
       a.created_at_ms, a.updated_at_ms, a.deleted_at_ms
FROM assets a
JOIN asset_identities i ON i.asset_identity_id = a.asset_identity_id
`

// Asset returns one Asset and its current shared identity, including deleted
// Assets so callers can inspect and restore recycle-bin entries.
func (s *Store) Asset(ctx context.Context, assetID domain.AssetID) (domain.EpisodeAsset, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("read Asset: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("read Asset: %w", err)
	}
	if err := assetID.Validate(); err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("read Asset: %w", err)
	}
	asset, err := scanEpisodeAsset(database.QueryRowContext(ctx, episodeAssetColumns+`
WHERE a.asset_id = ?
`, assetID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EpisodeAsset{}, fmt.Errorf("read Asset %q: %w", assetID, ErrAssetNotFound)
	}
	if err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("read Asset %q: %w", assetID, err)
	}
	return asset, nil
}

// EpisodeAssets lists active Assets by default. Deleted entries can be
// requested explicitly for a recycle-bin view.
func (s *Store) EpisodeAssets(
	ctx context.Context,
	episodeID domain.EpisodeID,
	includeDeleted bool,
) ([]domain.EpisodeAsset, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Episode Assets: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("list Episode Assets: %w", err)
	}
	if err := episodeID.Validate(); err != nil {
		return nil, fmt.Errorf("list Episode Assets: %w", err)
	}
	deletedPredicate := "AND a.deleted_at_ms IS NULL"
	if includeDeleted {
		deletedPredicate = ""
	}
	rows, err := database.QueryContext(ctx, episodeAssetColumns+`
WHERE a.episode_id = ? `+deletedPredicate+`
ORDER BY i.kind ASC, i.name ASC, a.asset_id ASC
`, episodeID.String())
	if err != nil {
		return nil, fmt.Errorf("list Episode Assets: query: %w", err)
	}
	defer rows.Close()

	assets := make([]domain.EpisodeAsset, 0)
	for rows.Next() {
		asset, err := scanEpisodeAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("list Episode Assets: %w", err)
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Episode Assets: iterate: %w", err)
	}
	return assets, nil
}

func (s *Store) AssetIdentity(ctx context.Context, identityID domain.AssetIdentityID) (domain.AssetIdentity, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.AssetIdentity{}, fmt.Errorf("read Asset Identity: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.AssetIdentity{}, fmt.Errorf("read Asset Identity: %w", err)
	}
	if err := identityID.Validate(); err != nil {
		return domain.AssetIdentity{}, fmt.Errorf("read Asset Identity: %w", err)
	}
	identity, err := scanAssetIdentity(database.QueryRowContext(ctx, `
SELECT asset_identity_id, project_id, kind, name,
       revision, created_at_ms, updated_at_ms
FROM asset_identities
WHERE asset_identity_id = ?
`, identityID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AssetIdentity{}, fmt.Errorf("read Asset Identity %q: %w", identityID, ErrAssetIdentityNotFound)
	}
	if err != nil {
		return domain.AssetIdentity{}, fmt.Errorf("read Asset Identity %q: %w", identityID, err)
	}
	return identity, nil
}

func scanEpisodeAsset(row rowScanner) (domain.EpisodeAsset, error) {
	var identityID, identityProjectID, kind, name string
	var identityRevision, identityCreatedAtMS, identityUpdatedAtMS int64
	var assetID, assetProjectID, episodeID, assetIdentityID string
	var derivedFrom sql.NullString
	var prompt string
	var assetRevision, assetCreatedAtMS, assetUpdatedAtMS int64
	var deletedAtMS sql.NullInt64
	if err := row.Scan(
		&identityID, &identityProjectID, &kind, &name,
		&identityRevision, &identityCreatedAtMS, &identityUpdatedAtMS,
		&assetID, &assetProjectID, &episodeID, &assetIdentityID,
		&derivedFrom, &prompt, &assetRevision,
		&assetCreatedAtMS, &assetUpdatedAtMS, &deletedAtMS,
	); err != nil {
		return domain.EpisodeAsset{}, err
	}

	parsedIdentityID, err := domain.ParseAssetIdentityID(identityID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset Identity ID", err)
	}
	identityOwner, err := domain.ParseProjectID(identityProjectID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset Identity Project ID", err)
	}
	parsedAssetID, err := domain.ParseAssetID(assetID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset ID", err)
	}
	assetOwner, err := domain.ParseProjectID(assetProjectID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset Project ID", err)
	}
	parsedEpisodeID, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset Episode ID", err)
	}
	parsedAssetIdentityID, err := domain.ParseAssetIdentityID(assetIdentityID)
	if err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Asset Identity reference", err)
	}
	var sourceID domain.AssetID
	if derivedFrom.Valid {
		sourceID, err = domain.ParseAssetID(derivedFrom.String)
		if err != nil {
			return domain.EpisodeAsset{}, invalidStoredData("Asset provenance", err)
		}
	}
	var deletedAt *time.Time
	if deletedAtMS.Valid {
		value := time.UnixMilli(deletedAtMS.Int64).UTC()
		deletedAt = &value
	}
	result := domain.EpisodeAsset{
		Identity: domain.AssetIdentity{
			ID: parsedIdentityID, ProjectID: identityOwner, Kind: domain.AssetKind(kind), Name: name,
			Revision: identityRevision, CreatedAt: time.UnixMilli(identityCreatedAtMS).UTC(),
			UpdatedAt: time.UnixMilli(identityUpdatedAtMS).UTC(),
		},
		Asset: domain.Asset{
			ID: parsedAssetID, ProjectID: assetOwner, EpisodeID: parsedEpisodeID,
			IdentityID: parsedAssetIdentityID, DerivedFromAssetID: sourceID, Prompt: prompt,
			Revision: assetRevision, CreatedAt: time.UnixMilli(assetCreatedAtMS).UTC(),
			UpdatedAt: time.UnixMilli(assetUpdatedAtMS).UTC(), DeletedAt: deletedAt,
		},
	}
	if err := result.Validate(); err != nil {
		return domain.EpisodeAsset{}, invalidStoredData("Episode Asset", err)
	}
	return result, nil
}

func scanAssetIdentity(row rowScanner) (domain.AssetIdentity, error) {
	var identityID, projectID, kind, name string
	var revision, createdAtMS, updatedAtMS int64
	if err := row.Scan(&identityID, &projectID, &kind, &name, &revision, &createdAtMS, &updatedAtMS); err != nil {
		return domain.AssetIdentity{}, err
	}
	id, err := domain.ParseAssetIdentityID(identityID)
	if err != nil {
		return domain.AssetIdentity{}, invalidStoredData("Asset Identity ID", err)
	}
	ownerID, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.AssetIdentity{}, invalidStoredData("Asset Identity Project ID", err)
	}
	identity := domain.AssetIdentity{
		ID: id, ProjectID: ownerID, Kind: domain.AssetKind(kind), Name: name,
		Revision: revision, CreatedAt: time.UnixMilli(createdAtMS).UTC(),
		UpdatedAt: time.UnixMilli(updatedAtMS).UTC(),
	}
	if err := identity.Validate(); err != nil {
		return domain.AssetIdentity{}, invalidStoredData("Asset Identity", err)
	}
	return identity, nil
}
