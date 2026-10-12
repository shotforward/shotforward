package projectapp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// FindPriorAssetCandidates returns the newest active variant for each identity
// from Episodes preceding the target, excluding identities already present in
// the target Episode. It is a read-only projection for extraction review.
func (s *Service) FindPriorAssetCandidates(ctx context.Context, input FindPriorAssetCandidatesInput) ([]PriorAssetCandidate, error) {
	handle, err := s.assetProjectHandle(ctx, "find prior Asset candidates", input.ProjectID)
	if err != nil {
		return nil, err
	}
	target, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return nil, fmt.Errorf("find prior Asset candidates: %w", err)
	}
	if (input.Kind != "" && !input.Kind.IsValid()) ||
		(input.NameQuery != "" && domain.ValidateAssetIdentityName(input.NameQuery) != nil) {
		return nil, fmt.Errorf("find prior Asset candidates: %w", ErrInvalidRequest)
	}
	query := strings.ToLower(input.NameQuery)
	current, err := handle.Store.EpisodeAssets(ctx, input.EpisodeID, false)
	if err != nil {
		return nil, fmt.Errorf("find prior Asset candidates: %w", err)
	}
	present := make(map[domain.AssetIdentityID]struct{}, len(current))
	for _, value := range current {
		present[value.Identity.ID] = struct{}{}
	}
	episodes, err := handle.Store.Episodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("find prior Asset candidates: %w", err)
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].SequenceNumber > episodes[j].SequenceNumber })
	seen := make(map[domain.AssetIdentityID]struct{})
	result := make([]PriorAssetCandidate, 0)
	for _, episode := range episodes {
		if episode.SequenceNumber >= target.SequenceNumber {
			continue
		}
		assets, err := handle.Store.EpisodeAssets(ctx, episode.ID, false)
		if err != nil {
			return nil, fmt.Errorf("find prior Asset candidates: %w", err)
		}
		for _, value := range assets {
			if _, ok := present[value.Identity.ID]; ok {
				continue
			}
			if _, ok := seen[value.Identity.ID]; ok {
				continue
			}
			if (input.Kind != "" && value.Identity.Kind != input.Kind) || (query != "" && !strings.Contains(strings.ToLower(value.Identity.Name), query)) {
				continue
			}
			seen[value.Identity.ID] = struct{}{}
			result = append(result, PriorAssetCandidate{Asset: value, EpisodeSequence: episode.SequenceNumber})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EpisodeSequence != result[j].EpisodeSequence {
			return result[i].EpisodeSequence > result[j].EpisodeSequence
		}
		if result[i].Asset.Identity.Kind != result[j].Asset.Identity.Kind {
			return result[i].Asset.Identity.Kind < result[j].Asset.Identity.Kind
		}
		if result[i].Asset.Identity.Name != result[j].Asset.Identity.Name {
			return result[i].Asset.Identity.Name < result[j].Asset.Identity.Name
		}
		return result[i].Asset.Asset.ID.String() < result[j].Asset.Asset.ID.String()
	})
	return result, nil
}

func (s *Service) GetAsset(ctx context.Context, input GetAssetInput) (domain.EpisodeAsset, error) {
	handle, err := s.assetProjectHandle(ctx, "get Asset", input.ProjectID)
	if err != nil {
		return domain.EpisodeAsset{}, err
	}
	asset, err := assetForProject(ctx, handle.Store, input.ProjectID, input.AssetID)
	if err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("get Asset: %w", err)
	}
	return asset, nil
}

func (s *Service) ListEpisodeAssets(ctx context.Context, input ListEpisodeAssetsInput) ([]domain.EpisodeAsset, error) {
	handle, err := s.assetProjectHandle(ctx, "list Episode Assets", input.ProjectID)
	if err != nil {
		return nil, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return nil, fmt.Errorf("list Episode Assets: %w", err)
	}
	assets, err := handle.Store.EpisodeAssets(ctx, input.EpisodeID, input.IncludeDeleted)
	if err != nil {
		return nil, fmt.Errorf("list Episode Assets: %w", err)
	}
	for _, asset := range assets {
		if err := asset.Validate(); err != nil || asset.Asset.ProjectID != input.ProjectID || asset.Asset.EpisodeID != input.EpisodeID {
			return nil, fmt.Errorf("list Episode Assets: %w", ErrProjectDataInvalid)
		}
	}
	return assets, nil
}

// CreateAsset creates a new narrative identity and its first Episode Asset.
func (s *Service) CreateAsset(ctx context.Context, input CreateAssetInput) (AssetResult, error) {
	handle, err := s.assetProjectHandle(ctx, "create Asset", input.ProjectID)
	if err != nil {
		return AssetResult{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w", err)
	}
	assetID, identityID, err := s.newAssetIDs()
	if err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	identity := domain.AssetIdentity{
		ID: identityID, ProjectID: input.ProjectID, Kind: input.Kind, Name: input.Name,
		Revision: initialRevision, CreatedAt: now, UpdatedAt: now,
	}
	asset := domain.Asset{
		ID: assetID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, IdentityID: identityID,
		Prompt: domain.NormalizeAssetPrompt(input.Prompt), Revision: initialRevision,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := identity.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w: %w", ErrInvalidRequest, err)
	}
	if err := asset.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w: %w", ErrInvalidRequest, err)
	}
	if err := episodeReferenceUploadNameAvailable(ctx, handle.Store, input.EpisodeID, "", identity.Name); err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w", err)
	}
	if err := handle.Store.CreateAsset(ctx, &identity, asset, now); err != nil {
		return AssetResult{}, fmt.Errorf("create Asset: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

// DeriveAsset creates an independent Asset in a later Episode and reuses only
// the source Asset's current image Artifact, not its complete version history.
func (s *Service) DeriveAsset(ctx context.Context, input DeriveAssetInput) (AssetResult, error) {
	handle, err := s.assetProjectHandle(ctx, "derive Asset", input.ProjectID)
	if err != nil {
		return AssetResult{}, err
	}
	source, err := assetForProject(ctx, handle.Store, input.ProjectID, input.SourceAssetID)
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", err)
	}
	if source.Asset.IsDeleted() {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", ErrAssetDeleted)
	}
	targetEpisode, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", err)
	}
	sourceEpisode, err := episodeForProject(ctx, handle.Store, input.ProjectID, source.Asset.EpisodeID)
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", err)
	}
	if sourceEpisode.SequenceNumber >= targetEpisode.SequenceNumber {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: source must belong to an earlier Episode", ErrInvalidAssetProvenance)
	}
	assetID, err := s.ids.NewAssetID()
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	inheritedVersionID, err := s.ids.NewMediaVersionID()
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	if err := inheritedVersionID.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	prompt := source.Asset.Prompt
	if input.Prompt != nil {
		prompt = *input.Prompt
	}
	asset := domain.Asset{
		ID: assetID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID,
		IdentityID: source.Identity.ID, DerivedFromAssetID: source.Asset.ID,
		Prompt: domain.NormalizeAssetPrompt(prompt), Revision: initialRevision,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := asset.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w: %w", ErrInvalidRequest, err)
	}
	if err := episodeReferenceUploadNameAvailable(ctx, handle.Store, input.EpisodeID, "", source.Identity.Name); err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", err)
	}
	if err := handle.Store.CreateDerivedAsset(ctx, asset, inheritedVersionID, now); err != nil {
		return AssetResult{}, fmt.Errorf("derive Asset: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) UpdateAssetPrompt(ctx context.Context, input UpdateAssetPromptInput) (AssetResult, error) {
	handle, current, err := s.mutableAsset(ctx, "update Asset prompt", input.ProjectID, input.AssetID, input.ExpectedRevision)
	if err != nil {
		return AssetResult{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("update Asset prompt: %w: %w", ErrProjectWriteFailed, err)
	}
	asset := current.Asset
	asset.Prompt = domain.NormalizeAssetPrompt(input.Prompt)
	asset.Revision++
	asset.UpdatedAt = now
	if err := asset.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("update Asset prompt: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.UpdateAsset(ctx, asset, input.ExpectedRevision, now); err != nil {
		return AssetResult{}, fmt.Errorf("update Asset prompt: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) RenameAssetIdentity(ctx context.Context, input RenameAssetIdentityInput) (AssetIdentityResult, error) {
	handle, err := s.assetProjectHandle(ctx, "rename Asset Identity", input.ProjectID)
	if err != nil {
		return AssetIdentityResult{}, err
	}
	if err := input.AssetIdentityID.Validate(); err != nil || input.ExpectedRevision < 1 {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w", ErrInvalidRequest)
	}
	identity, err := handle.Store.AssetIdentity(ctx, input.AssetIdentityID)
	if err != nil {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w", err)
	}
	if identity.ProjectID != input.ProjectID {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w", ErrProjectDataInvalid)
	}
	if identity.Revision != input.ExpectedRevision {
		return AssetIdentityResult{}, revisionConflict("rename Asset Identity", input.ExpectedRevision, identity.Revision)
	}
	if identity.Revision == math.MaxInt64 {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w: revision is exhausted", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w: %w", ErrProjectWriteFailed, err)
	}
	identity.Name = input.Name
	identity.Revision++
	identity.UpdatedAt = now
	if err := identity.Validate(); err != nil {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w: %w", ErrInvalidRequest, err)
	}
	if err := assetIdentityNameAvailableForUploads(ctx, handle.Store, identity.ID, identity.Name); err != nil {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w", err)
	}
	if err := handle.Store.UpdateAssetIdentity(ctx, identity, input.ExpectedRevision, now); err != nil {
		return AssetIdentityResult{}, fmt.Errorf("rename Asset Identity: %w", err)
	}
	return s.finishIdentityMutation(ctx, handle, identity.ID, now)
}

func (s *Service) RelinkAssetIdentity(ctx context.Context, input RelinkAssetIdentityInput) (AssetResult, error) {
	handle, current, err := s.mutableAsset(ctx, "relink Asset Identity", input.ProjectID, input.AssetID, input.ExpectedAssetRevision)
	if err != nil {
		return AssetResult{}, err
	}
	if err := input.TargetAssetIdentityID.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w: %w", ErrInvalidRequest, err)
	}
	target, err := handle.Store.AssetIdentity(ctx, input.TargetAssetIdentityID)
	if err != nil {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w", err)
	}
	if target.ProjectID != input.ProjectID {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w", ErrProjectDataInvalid)
	}
	if target.Kind != current.Identity.Kind {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w", ErrAssetKindMismatch)
	}
	if target.ID == current.Identity.ID {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w: target is already linked", ErrInvalidRequest)
	}
	if err := episodeReferenceUploadNameAvailable(ctx, handle.Store, current.Asset.EpisodeID, "", target.Name); err != nil {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w", err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w: %w", ErrProjectWriteFailed, err)
	}
	asset := current.Asset
	asset.IdentityID = target.ID
	asset.Revision++
	asset.UpdatedAt = now
	if err := handle.Store.UpdateAsset(ctx, asset, input.ExpectedAssetRevision, now); err != nil {
		return AssetResult{}, fmt.Errorf("relink Asset Identity: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) DetachAssetIdentity(ctx context.Context, input DetachAssetIdentityInput) (AssetResult, error) {
	handle, current, err := s.mutableAsset(ctx, "detach Asset Identity", input.ProjectID, input.AssetID, input.ExpectedAssetRevision)
	if err != nil {
		return AssetResult{}, err
	}
	identityID, err := s.ids.NewAssetIdentityID()
	if err != nil {
		return AssetResult{}, fmt.Errorf("detach Asset Identity: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("detach Asset Identity: %w: %w", ErrProjectWriteFailed, err)
	}
	identity := domain.AssetIdentity{
		ID: identityID, ProjectID: input.ProjectID, Kind: current.Identity.Kind, Name: input.NewName,
		Revision: initialRevision, CreatedAt: now, UpdatedAt: now,
	}
	asset := current.Asset
	asset.IdentityID = identity.ID
	asset.Revision++
	asset.UpdatedAt = now
	if err := identity.Validate(); err != nil {
		return AssetResult{}, fmt.Errorf("detach Asset Identity: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.DetachAssetIdentity(ctx, identity, asset, input.ExpectedAssetRevision, now); err != nil {
		return AssetResult{}, fmt.Errorf("detach Asset Identity: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) DeleteAsset(ctx context.Context, input DeleteAssetInput) (AssetResult, error) {
	handle, current, err := s.mutableAsset(ctx, "delete Asset", input.ProjectID, input.AssetID, input.ExpectedRevision)
	if err != nil {
		return AssetResult{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("delete Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	asset := current.Asset
	asset.DeletedAt = &now
	asset.Revision++
	asset.UpdatedAt = now
	if err := handle.Store.DeleteAsset(ctx, asset, input.ExpectedRevision, input.ConfirmReferenced, now); err != nil {
		return AssetResult{}, fmt.Errorf("delete Asset: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) RestoreAsset(ctx context.Context, input RestoreAssetInput) (AssetResult, error) {
	handle, err := s.assetProjectHandle(ctx, "restore Asset", input.ProjectID)
	if err != nil {
		return AssetResult{}, err
	}
	current, err := assetForProject(ctx, handle.Store, input.ProjectID, input.AssetID)
	if err != nil {
		return AssetResult{}, fmt.Errorf("restore Asset: %w", err)
	}
	if !current.Asset.IsDeleted() {
		return AssetResult{}, fmt.Errorf("restore Asset: %w", ErrAssetNotDeleted)
	}
	if current.Asset.Revision != input.ExpectedRevision || input.ExpectedRevision < 1 {
		return AssetResult{}, revisionConflict("restore Asset", input.ExpectedRevision, current.Asset.Revision)
	}
	if current.Asset.Revision == math.MaxInt64 {
		return AssetResult{}, fmt.Errorf("restore Asset: %w: revision is exhausted", ErrProjectWriteFailed)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return AssetResult{}, fmt.Errorf("restore Asset: %w: %w", ErrProjectWriteFailed, err)
	}
	asset := current.Asset
	asset.DeletedAt = nil
	asset.Revision++
	asset.UpdatedAt = now
	if err := handle.Store.UpdateAsset(ctx, asset, input.ExpectedRevision, now); err != nil {
		return AssetResult{}, fmt.Errorf("restore Asset: %w", err)
	}
	return s.finishAssetMutation(ctx, handle, asset.ID, now)
}

func (s *Service) assetProjectHandle(ctx context.Context, operation string, projectID domain.ProjectID) (ProjectHandle, error) {
	if err := validateContext(ctx); err != nil {
		return ProjectHandle{}, fmt.Errorf("%s: %w", operation, err)
	}
	handle, err := s.openProjectHandle(projectID)
	if err != nil {
		return ProjectHandle{}, fmt.Errorf("%s: %w", operation, err)
	}
	return handle, nil
}

func (s *Service) newAssetIDs() (domain.AssetID, domain.AssetIdentityID, error) {
	assetID, err := s.ids.NewAssetID()
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrProjectWriteFailed, err)
	}
	identityID, err := s.ids.NewAssetIdentityID()
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrProjectWriteFailed, err)
	}
	return assetID, identityID, nil
}

func (s *Service) mutableAsset(
	ctx context.Context,
	operation string,
	projectID domain.ProjectID,
	assetID domain.AssetID,
	expectedRevision int64,
) (ProjectHandle, domain.EpisodeAsset, error) {
	handle, err := s.assetProjectHandle(ctx, operation, projectID)
	if err != nil {
		return ProjectHandle{}, domain.EpisodeAsset{}, err
	}
	current, err := assetForProject(ctx, handle.Store, projectID, assetID)
	if err != nil {
		return ProjectHandle{}, domain.EpisodeAsset{}, fmt.Errorf("%s: %w", operation, err)
	}
	if current.Asset.IsDeleted() {
		return ProjectHandle{}, domain.EpisodeAsset{}, fmt.Errorf("%s: %w", operation, ErrAssetDeleted)
	}
	if expectedRevision < 1 || current.Asset.Revision != expectedRevision {
		return ProjectHandle{}, domain.EpisodeAsset{}, revisionConflict(operation, expectedRevision, current.Asset.Revision)
	}
	if current.Asset.Revision == math.MaxInt64 {
		return ProjectHandle{}, domain.EpisodeAsset{}, fmt.Errorf("%s: %w: revision is exhausted", operation, ErrProjectWriteFailed)
	}
	return handle, current, nil
}

func assetForProject(ctx context.Context, store ProjectStore, projectID domain.ProjectID, assetID domain.AssetID) (domain.EpisodeAsset, error) {
	if err := assetID.Validate(); err != nil {
		return domain.EpisodeAsset{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	asset, err := store.Asset(ctx, assetID)
	if err != nil {
		return domain.EpisodeAsset{}, err
	}
	if err := asset.Validate(); err != nil || asset.Asset.ProjectID != projectID {
		return domain.EpisodeAsset{}, fmt.Errorf("%w: Asset ownership is invalid", ErrProjectDataInvalid)
	}
	return asset, nil
}

func revisionConflict(operation string, expected, current int64) error {
	return fmt.Errorf("%s: %w: expected %d, current %d", operation, ErrRevisionConflict, expected, current)
}

func (s *Service) finishAssetMutation(
	ctx context.Context,
	handle ProjectHandle,
	assetID domain.AssetID,
	operationTime time.Time,
) (AssetResult, error) {
	projectResult, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return AssetResult{}, err
	}
	asset, err := assetForProject(ctx, handle.Store, handle.ProjectID, assetID)
	if err != nil {
		return AssetResult{}, err
	}
	result := AssetResult{Project: projectResult.Project, Asset: asset}
	if err := s.library.SyncProject(ctx, libraryRecord(handle.RootPath, projectResult, operationTime)); err != nil {
		return result, &RegistrationError{Category: ErrProjectUpdatedNotRegistered, RootPath: handle.RootPath, Cause: err}
	}
	return result, nil
}

func (s *Service) finishIdentityMutation(
	ctx context.Context,
	handle ProjectHandle,
	identityID domain.AssetIdentityID,
	operationTime time.Time,
) (AssetIdentityResult, error) {
	projectResult, err := s.readResult(ctx, handle.Store)
	if err != nil {
		return AssetIdentityResult{}, err
	}
	identity, err := handle.Store.AssetIdentity(ctx, identityID)
	if err != nil {
		return AssetIdentityResult{}, err
	}
	if identity.ProjectID != handle.ProjectID {
		return AssetIdentityResult{}, ErrProjectDataInvalid
	}
	result := AssetIdentityResult{Project: projectResult.Project, Identity: identity}
	if err := s.library.SyncProject(ctx, libraryRecord(handle.RootPath, projectResult, operationTime)); err != nil {
		return result, &RegistrationError{Category: ErrProjectUpdatedNotRegistered, RootPath: handle.RootPath, Cause: err}
	}
	return result, nil
}
