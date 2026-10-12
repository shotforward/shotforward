package projectapp

import (
	"context"
	"errors"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type fakeIDs struct {
	projectID         domain.ProjectID
	episodeID         domain.EpisodeID
	assetID           domain.AssetID
	assetIdentityID   domain.AssetIdentityID
	projectErr        error
	episodeErr        error
	assetErr          error
	assetIdentityErr  error
	sceneID           domain.SceneID
	clipID            domain.ClipID
	sceneErr          error
	clipErr           error
	artifactID        domain.ArtifactID
	mediaVersionID    domain.MediaVersionID
	audioReferenceID  domain.AudioReferenceAssetID
	generationBatchID domain.GenerationBatchID
	generationJobID   domain.GenerationJobID
	exportJobID       domain.ExportJobID
}

func (f fakeIDs) NewProjectID() (domain.ProjectID, error) { return f.projectID, f.projectErr }
func (f fakeIDs) NewEpisodeID() (domain.EpisodeID, error) { return f.episodeID, f.episodeErr }
func (f fakeIDs) NewAssetID() (domain.AssetID, error)     { return f.assetID, f.assetErr }
func (f fakeIDs) NewAssetIdentityID() (domain.AssetIdentityID, error) {
	return f.assetIdentityID, f.assetIdentityErr
}
func (f fakeIDs) NewSceneID() (domain.SceneID, error)               { return f.sceneID, f.sceneErr }
func (f fakeIDs) NewClipID() (domain.ClipID, error)                 { return f.clipID, f.clipErr }
func (f fakeIDs) NewArtifactID() (domain.ArtifactID, error)         { return f.artifactID, nil }
func (f fakeIDs) NewMediaVersionID() (domain.MediaVersionID, error) { return f.mediaVersionID, nil }
func (f fakeIDs) NewAudioReferenceAssetID() (domain.AudioReferenceAssetID, error) {
	return f.audioReferenceID, nil
}
func (f fakeIDs) NewGenerationBatchID() (domain.GenerationBatchID, error) {
	return f.generationBatchID, nil
}
func (f fakeIDs) NewGenerationJobID() (domain.GenerationJobID, error) {
	return f.generationJobID, nil
}
func (f fakeIDs) NewExportJobID() (domain.ExportJobID, error) { return f.exportJobID, nil }

type fakeClock struct{ value time.Time }

func (f fakeClock) Now() time.Time { return f.value }

type fakeWorkspace struct {
	paths        ProjectPaths
	createErr    error
	openErr      error
	cleanupErr   error
	createCalls  int
	openCalls    int
	cleanupCalls int
	deleteCalls  int
}

func (f *fakeWorkspace) Delete(ProjectPaths) error {
	f.deleteCalls++
	return f.cleanupErr
}

func (f *fakeWorkspace) Create(string) (ProjectPaths, error) {
	f.createCalls++
	return f.paths, f.createErr
}

func (f *fakeWorkspace) Open(string) (ProjectPaths, error) {
	f.openCalls++
	return f.paths, f.openErr
}

func (f *fakeWorkspace) CleanupCreated(ProjectPaths) error {
	f.cleanupCalls++
	return f.cleanupErr
}

type fakeStore struct {
	project               domain.Project
	episode               domain.Episode
	episodes              []domain.EpisodeSummary
	initializeErr         error
	projectErr            error
	episodesErr           error
	episodeErr            error
	createEpisodeErr      error
	updateEpisodeErr      error
	updateProjectErr      error
	saveScriptErr         error
	closeErr              error
	initializeCalls       int
	projectCalls          int
	episodesCalls         int
	episodeCalls          int
	createEpisodeCalls    int
	updateEpisodeCalls    int
	updateProjectCalls    int
	saveScriptCalls       int
	closeCalls            int
	initializedWith       domain.Project
	initialEpisode        domain.Episode
	createdEpisode        domain.Episode
	updatedMetadata       domain.EpisodeMetadata
	savedScript           domain.Script
	expectedRevision      int64
	projectUpdatedAt      time.Time
	asset                 domain.EpisodeAsset
	episodeAssets         []domain.EpisodeAsset
	episodeReferenceMedia []domain.EpisodeReferenceMedia
	assetIdentity         domain.AssetIdentity
	assetErr              error
	episodeAssetsErr      error
	assetIdentityErr      error
	createAssetErr        error
	updateAssetErr        error
	updateIdentityErr     error
	detachIdentityErr     error
	createAssetCalls      int
	updateAssetCalls      int
	updateIdentityCalls   int
	detachIdentityCalls   int
	artifacts             map[domain.ArtifactID]domain.Artifact
	artifactSelected      *bool
}

func (f *fakeStore) Initialize(_ context.Context, project domain.Project, episode domain.Episode) error {
	f.initializeCalls++
	f.initializedWith = project
	f.initialEpisode = episode
	return f.initializeErr
}

func (f *fakeStore) InitializeProject(_ context.Context, project domain.Project) error {
	f.initializeCalls++
	f.initializedWith = project
	return f.initializeErr
}

func (f *fakeStore) Project(context.Context) (domain.Project, error) {
	f.projectCalls++
	return f.project, f.projectErr
}

func (f *fakeStore) UpdateProject(_ context.Context, project domain.Project, expectedRevision int64) error {
	f.updateProjectCalls++
	f.expectedRevision = expectedRevision
	if f.updateProjectErr != nil {
		return f.updateProjectErr
	}
	f.project = project
	return nil
}

func (f *fakeStore) Episodes(context.Context) ([]domain.EpisodeSummary, error) {
	f.episodesCalls++
	return f.episodes, f.episodesErr
}

func (f *fakeStore) DeletedEpisodes(context.Context) ([]domain.EpisodeSummary, error) {
	return nil, nil
}

func (f *fakeStore) Episode(_ context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	f.episodeCalls++
	if f.episodeErr != nil {
		return domain.Episode{}, f.episodeErr
	}
	if f.episode.ID != episodeID {
		return domain.Episode{}, ErrEpisodeNotFound
	}
	return f.episode, nil
}

func (f *fakeStore) EpisodeIncludingDeleted(ctx context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	return f.Episode(ctx, episodeID)
}

func (f *fakeStore) CreateEpisode(_ context.Context, episode domain.Episode, updatedAt time.Time) error {
	f.createEpisodeCalls++
	f.createdEpisode = episode
	f.projectUpdatedAt = updatedAt
	if f.createEpisodeErr != nil {
		return f.createEpisodeErr
	}
	f.episode = episode
	f.episodes = append(f.episodes, episode.Summary())
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) UpdateEpisode(_ context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, updatedAt time.Time) error {
	f.updateEpisodeCalls++
	f.updatedMetadata = metadata
	f.expectedRevision = expectedRevision
	f.projectUpdatedAt = updatedAt
	if f.updateEpisodeErr != nil {
		return f.updateEpisodeErr
	}
	f.episode.EpisodeMetadata = metadata
	f.episodes[0] = f.episode.Summary()
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) DeleteEpisode(_ context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, updatedAt time.Time) error {
	f.updatedMetadata, f.expectedRevision, f.projectUpdatedAt = metadata, expectedRevision, updatedAt
	return f.updateEpisodeErr
}

func (f *fakeStore) RestoreEpisode(_ context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, updatedAt time.Time) error {
	f.updatedMetadata, f.expectedRevision, f.projectUpdatedAt = metadata, expectedRevision, updatedAt
	if f.updateEpisodeErr != nil {
		return f.updateEpisodeErr
	}
	f.episode.EpisodeMetadata = metadata
	return nil
}

func (f *fakeStore) SaveScript(_ context.Context, _ domain.EpisodeID, script domain.Script, expectedRevision int64, updatedAt time.Time) error {
	f.saveScriptCalls++
	f.savedScript = script
	f.expectedRevision = expectedRevision
	f.projectUpdatedAt = updatedAt
	if f.saveScriptErr != nil {
		return f.saveScriptErr
	}
	f.episode.Script = script
	f.episode.UpdatedAt = updatedAt
	f.episodes[0] = f.episode.Summary()
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) Asset(_ context.Context, assetID domain.AssetID) (domain.EpisodeAsset, error) {
	if f.assetErr != nil {
		return domain.EpisodeAsset{}, f.assetErr
	}
	if f.asset.Asset.ID != assetID {
		return domain.EpisodeAsset{}, ErrAssetNotFound
	}
	return f.asset, nil
}

func (f *fakeStore) EpisodeAssets(_ context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.EpisodeAsset, error) {
	if f.episodeAssetsErr != nil {
		return nil, f.episodeAssetsErr
	}
	result := make([]domain.EpisodeAsset, 0)
	for _, asset := range f.episodeAssets {
		if asset.Asset.EpisodeID == episodeID && (includeDeleted || !asset.Asset.IsDeleted()) {
			result = append(result, asset)
		}
	}
	return result, nil
}

func (f *fakeStore) AssetIdentity(_ context.Context, identityID domain.AssetIdentityID) (domain.AssetIdentity, error) {
	if f.assetIdentityErr != nil {
		return domain.AssetIdentity{}, f.assetIdentityErr
	}
	if f.assetIdentity.ID == identityID {
		return f.assetIdentity, nil
	}
	if f.asset.Identity.ID == identityID {
		return f.asset.Identity, nil
	}
	return domain.AssetIdentity{}, ErrAssetIdentityNotFound
}

func (f *fakeStore) CreateAsset(_ context.Context, identity *domain.AssetIdentity, asset domain.Asset, updatedAt time.Time) error {
	f.createAssetCalls++
	f.projectUpdatedAt = updatedAt
	if f.createAssetErr != nil {
		return f.createAssetErr
	}
	if identity != nil {
		f.assetIdentity = *identity
	} else if f.assetIdentity.ID == "" {
		f.assetIdentity = f.asset.Identity
	}
	f.asset = domain.EpisodeAsset{Identity: f.assetIdentity, Asset: asset}
	f.episodeAssets = append(f.episodeAssets, f.asset)
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) CreateDerivedAsset(ctx context.Context, asset domain.Asset, _ domain.MediaVersionID, updatedAt time.Time) error {
	return f.CreateAsset(ctx, nil, asset, updatedAt)
}

func (f *fakeStore) UpdateAsset(_ context.Context, asset domain.Asset, expectedRevision int64, updatedAt time.Time) error {
	f.updateAssetCalls++
	f.expectedRevision = expectedRevision
	f.projectUpdatedAt = updatedAt
	if f.updateAssetErr != nil {
		return f.updateAssetErr
	}
	f.asset.Asset = asset
	if f.assetIdentity.ID == asset.IdentityID {
		f.asset.Identity = f.assetIdentity
	}
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) DeleteAsset(ctx context.Context, asset domain.Asset, expectedRevision int64, _ bool, updatedAt time.Time) error {
	return f.UpdateAsset(ctx, asset, expectedRevision, updatedAt)
}

func (f *fakeStore) UpdateAssetIdentity(_ context.Context, identity domain.AssetIdentity, expectedRevision int64, updatedAt time.Time) error {
	f.updateIdentityCalls++
	f.expectedRevision = expectedRevision
	f.projectUpdatedAt = updatedAt
	if f.updateIdentityErr != nil {
		return f.updateIdentityErr
	}
	f.assetIdentity = identity
	if f.asset.Asset.IdentityID == identity.ID {
		f.asset.Identity = identity
	}
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) DetachAssetIdentity(_ context.Context, identity domain.AssetIdentity, asset domain.Asset, expectedRevision int64, updatedAt time.Time) error {
	f.detachIdentityCalls++
	f.expectedRevision = expectedRevision
	f.projectUpdatedAt = updatedAt
	if f.detachIdentityErr != nil {
		return f.detachIdentityErr
	}
	f.assetIdentity = identity
	f.asset = domain.EpisodeAsset{Identity: identity, Asset: asset}
	f.project.UpdatedAt = updatedAt
	return nil
}

func (f *fakeStore) Storyboard(context.Context, domain.EpisodeID) (domain.Storyboard, error) {
	return domain.Storyboard{}, errors.New("not implemented")
}
func (f *fakeStore) Scene(context.Context, domain.SceneID) (domain.Scene, error) {
	return domain.Scene{}, errors.New("not implemented")
}
func (f *fakeStore) SceneDetails(context.Context, domain.SceneID) (domain.StoryboardScene, error) {
	return domain.StoryboardScene{}, errors.New("not implemented")
}
func (f *fakeStore) Clip(context.Context, domain.ClipID) (domain.ClipDetails, error) {
	return domain.ClipDetails{}, errors.New("not implemented")
}
func (f *fakeStore) CreateScene(context.Context, domain.Scene, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) UpdateScene(context.Context, domain.Scene, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) DeleteScene(context.Context, domain.Scene, int64, int64, bool, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) RestoreScene(context.Context, domain.Scene, int64, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) ReorderScenes(context.Context, domain.EpisodeID, []domain.SceneID, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) CreateClip(context.Context, domain.Clip, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) UpdateClip(context.Context, domain.Clip, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) MoveClip(context.Context, domain.Clip, domain.ClipID, domain.ClipID, int64, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) DeleteClip(context.Context, domain.Clip, int64, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) RestoreClip(context.Context, domain.Clip, int64, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) BindClipAsset(context.Context, domain.ClipID, domain.AssetID, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) UnbindClipAsset(context.Context, domain.ClipID, domain.AssetID, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) ReplaceClipAsset(context.Context, domain.ClipID, domain.AssetID, domain.AssetID, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) ReplaceClips(context.Context, []domain.Clip, []domain.ClipReplacement, int64, time.Time) error {
	return errors.New("not implemented")
}
func (f *fakeStore) ResetStoryboard(context.Context, domain.EpisodeID, int64, bool, time.Time) ([]domain.GenerationJob, bool, error) {
	return nil, false, errors.New("not implemented")
}
func (f *fakeStore) AssetUsage(context.Context, domain.AssetID) ([]domain.Clip, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeStore) MediaSlot(context.Context, domain.MediaSlotID, bool) (domain.MediaSlotDetails, error) {
	return domain.MediaSlotDetails{}, ErrMediaSlotNotFound
}
func (f *fakeStore) Artifact(_ context.Context, id domain.ArtifactID) (domain.Artifact, error) {
	artifact, ok := f.artifacts[id]
	if !ok {
		return domain.Artifact{}, ErrArtifactNotFound
	}
	return artifact, nil
}
func (f *fakeStore) ArtifactSelectedInEpisode(_ context.Context, id domain.ArtifactID, _ domain.EpisodeID) (bool, error) {
	if f.artifactSelected != nil {
		return *f.artifactSelected, nil
	}
	_, ok := f.artifacts[id]
	return ok, nil
}
func (f *fakeStore) ArtifactBySHA256(context.Context, domain.ProjectID, string) (domain.Artifact, error) {
	return domain.Artifact{}, ErrArtifactNotFound
}
func (f *fakeStore) ClipReferenceArtifacts(context.Context, domain.ClipID) ([]domain.Artifact, error) {
	return nil, nil
}
func (f *fakeStore) EpisodeReferenceMedia(context.Context, domain.EpisodeID, domain.ArtifactID, bool) (domain.EpisodeReferenceMedia, error) {
	return domain.EpisodeReferenceMedia{}, ErrArtifactNotFound
}
func (f *fakeStore) EpisodeReferenceMediaList(context.Context, domain.EpisodeID, bool) ([]domain.EpisodeReferenceMedia, error) {
	return append([]domain.EpisodeReferenceMedia(nil), f.episodeReferenceMedia...), nil
}
func (f *fakeStore) AgentConversationMediaList(context.Context, domain.EpisodeID, string) ([]domain.AgentConversationMedia, error) {
	return nil, nil
}
func (f *fakeStore) AgentConversationMediaForOperation(context.Context, domain.EpisodeID, string, string) ([]domain.AgentConversationMedia, error) {
	return nil, nil
}
func (f *fakeStore) PutAgentConversationMedia(context.Context, domain.AgentConversationMedia, time.Time) error {
	return nil
}
func (f *fakeStore) BindAgentConversationMedia(context.Context, domain.EpisodeID, string, string, string, []domain.ArtifactID, time.Time) error {
	return nil
}
func (f *fakeStore) PutEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, time.Time) error {
	return nil
}
func (f *fakeStore) UpdateEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, int64, time.Time) error {
	return nil
}
func (f *fakeStore) DeleteEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, int64, time.Time) error {
	return nil
}
func (f *fakeStore) AddClipReferenceArtifact(context.Context, domain.ClipID, domain.Artifact, time.Time) error {
	return nil
}
func (f *fakeStore) DeleteClipReferenceArtifact(context.Context, domain.ClipID, domain.ArtifactID, time.Time) error {
	return nil
}
func (f *fakeStore) AddMediaVersion(context.Context, domain.Artifact, domain.MediaVersion, int64, bool, time.Time) error {
	return nil
}
func (f *fakeStore) SelectMediaVersion(context.Context, domain.MediaSlotID, domain.MediaVersionID, int64, time.Time) error {
	return nil
}
func (f *fakeStore) UpdateVideoTrim(context.Context, domain.MediaVersion, int64, time.Time) error {
	return nil
}
func (f *fakeStore) DeleteMediaVersion(context.Context, domain.MediaVersion, int64, int64, time.Time) error {
	return nil
}
func (f *fakeStore) RestoreMediaVersion(context.Context, domain.MediaVersion, int64, int64, time.Time) error {
	return nil
}
func (f *fakeStore) AudioReferenceAsset(context.Context, domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error) {
	return domain.AudioReferenceAsset{}, ErrAudioReferenceNotFound
}
func (f *fakeStore) EpisodeAudioReferenceAssets(context.Context, domain.EpisodeID, bool) ([]domain.AudioReferenceAsset, error) {
	return nil, nil
}
func (f *fakeStore) CreateAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, time.Time) error {
	return nil
}
func (f *fakeStore) UpdateAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error {
	return nil
}
func (f *fakeStore) DeleteAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error {
	return nil
}
func (f *fakeStore) RestoreAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error {
	return nil
}
func (f *fakeStore) ClipAudioReference(context.Context, domain.ClipID) (domain.AudioReferenceAssetID, error) {
	return "", nil
}
func (f *fakeStore) SetClipAudioReference(context.Context, domain.ClipID, domain.AudioReferenceAssetID, int64, time.Time) error {
	return nil
}
func (f *fakeStore) ClearClipAudioReference(context.Context, domain.ClipID, int64, time.Time) error {
	return nil
}
func (f *fakeStore) GenerationBatch(context.Context, domain.GenerationBatchID) (domain.GenerationBatchDetails, error) {
	return domain.GenerationBatchDetails{}, ErrGenerationBatchNotFound
}
func (f *fakeStore) GenerationBatchByOperationKey(context.Context, domain.ProjectID, string) (domain.GenerationBatchDetails, error) {
	return domain.GenerationBatchDetails{}, ErrGenerationBatchNotFound
}
func (f *fakeStore) GenerationJob(context.Context, domain.GenerationJobID) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, ErrGenerationJobNotFound
}
func (f *fakeStore) GenerationJobsByStates(context.Context, []domain.GenerationJobState, int) ([]domain.GenerationJob, error) {
	return nil, nil
}
func (f *fakeStore) RecentGenerationJobs(context.Context, int) ([]domain.GenerationJob, error) {
	return nil, nil
}
func (f *fakeStore) CreateGenerationBatch(context.Context, domain.GenerationBatch, []domain.GenerationJob, time.Time) error {
	return nil
}
func (f *fakeStore) ClaimGenerationJob(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) RecordGenerationOperation(context.Context, domain.GenerationJobID, int64, string, string, string, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) UpdateGenerationProgress(context.Context, domain.GenerationJobID, int64, string, string, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) CancelGenerationJob(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) RequestGenerationStop(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) FinishGenerationJob(context.Context, domain.GenerationJobID, int64, domain.GenerationJobState, string, string, time.Time) (domain.GenerationJob, error) {
	return domain.GenerationJob{}, nil
}
func (f *fakeStore) CompleteGenerationJob(context.Context, domain.GenerationJobID, int64, domain.Artifact, domain.MediaVersionID, time.Time) error {
	return nil
}
func (f *fakeStore) ExportJob(context.Context, domain.ExportJobID) (domain.ExportJob, error) {
	return domain.ExportJob{}, ErrExportJobNotFound
}
func (f *fakeStore) ExportJobByOperationKey(context.Context, domain.ProjectID, string) (domain.ExportJob, error) {
	return domain.ExportJob{}, ErrExportJobNotFound
}
func (f *fakeStore) ExportJobsByStates(context.Context, []domain.ExportJobState, int) ([]domain.ExportJob, error) {
	return nil, nil
}
func (f *fakeStore) RecentExportJobs(context.Context, int) ([]domain.ExportJob, error) {
	return nil, nil
}
func (f *fakeStore) CreateExportJob(context.Context, domain.ExportJob) error { return nil }
func (f *fakeStore) ClaimExportJob(context.Context, domain.ExportJobID, int64, time.Time) (domain.ExportJob, error) {
	return domain.ExportJob{}, nil
}
func (f *fakeStore) UpdateExportProgress(context.Context, domain.ExportJobID, int64, string, string, time.Time) (domain.ExportJob, error) {
	return domain.ExportJob{}, nil
}
func (f *fakeStore) RequestExportCancel(context.Context, domain.ExportJobID, int64, time.Time) (domain.ExportJob, error) {
	return domain.ExportJob{}, nil
}
func (f *fakeStore) FinishExportJob(context.Context, domain.ExportJobID, int64, domain.ExportJobState, string, string, string, time.Time) (domain.ExportJob, error) {
	return domain.ExportJob{}, nil
}

func (f *fakeStore) Close() error {
	f.closeCalls++
	return f.closeErr
}

type fakeStoreFactory struct {
	createStore ProjectStore
	openStore   ProjectStore
	createErr   error
	openErr     error
	createCalls int
	openCalls   int
}

func (f *fakeStoreFactory) Create(context.Context, string) (ProjectStore, error) {
	f.createCalls++
	return f.createStore, f.createErr
}

func (f *fakeStoreFactory) Open(context.Context, string) (ProjectStore, error) {
	f.openCalls++
	return f.openStore, f.openErr
}

type fakeLibrary struct {
	err         error
	records     []ProjectLibraryRecord
	project     ProjectCatalogEntry
	deleteCalls int
}

type fakeScriptImporter struct {
	result ImportedScript
	err    error
	path   string
	calls  int
}

func (f *fakeScriptImporter) Import(_ context.Context, path string) (ImportedScript, error) {
	f.calls++
	f.path = path
	return f.result, f.err
}

func (f *fakeLibrary) SyncProject(_ context.Context, record ProjectLibraryRecord) error {
	f.records = append(f.records, record)
	return f.err
}

func (f *fakeLibrary) Project(context.Context, domain.ProjectID) (ProjectCatalogEntry, error) {
	return f.project, f.err
}
func (f *fakeLibrary) CanDeleteProject(context.Context, domain.ProjectID) error { return f.err }
func (f *fakeLibrary) CanDeleteEpisode(context.Context, domain.ProjectID, domain.EpisodeID) error {
	return f.err
}
func (f *fakeLibrary) DeleteProject(context.Context, domain.ProjectID) error {
	f.deleteCalls++
	return f.err
}

func testDependencies(t testingT, root string) (Dependencies, *fakeWorkspace, *fakeStoreFactory, *fakeLibrary) {
	project, episode := testProjectResult(t)
	workspace := &fakeWorkspace{paths: ProjectPaths{Root: root, Database: root + "/project.db"}}
	store := &fakeStore{project: project, episode: episode, episodes: []domain.EpisodeSummary{episode.Summary()}}
	factory := &fakeStoreFactory{createStore: store, openStore: store}
	library := &fakeLibrary{project: ProjectCatalogEntry{
		ProjectID: project.ID, RootPath: root, DisplayName: project.Name,
	}}
	importer := &fakeScriptImporter{}
	return Dependencies{
		IDs:       fakeIDs{projectID: project.ID, episodeID: episode.ID},
		Clock:     fakeClock{value: project.CreatedAt},
		Workspace: workspace,
		Stores:    factory,
		Library:   library,
		Importer:  importer,
		Registry:  NewProjectStoreRegistry(),
	}, workspace, factory, library
}

type testingT interface {
	Helper()
	Fatal(args ...any)
}

func testProjectResult(t testingT) (domain.Project, domain.Episode) {
	t.Helper()
	projectID, err := domain.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	output, err := domain.NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 14, 0, 0, 123_000_000, time.UTC)
	project := domain.Project{
		ID: projectID, Name: "雨夜来信", Description: "简介", Output: output,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	episode := domain.Episode{
		EpisodeMetadata: domain.EpisodeMetadata{
			ID: episodeID, ProjectID: projectID, SequenceNumber: 1, Title: defaultEpisodeTitle,
			TargetDuration: defaultTargetDuration, Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
		Script: domain.Script{Markdown: "", Revision: 1, UpdatedAt: now},
	}
	return project, episode
}

var errInjected = errors.New("injected failure")
