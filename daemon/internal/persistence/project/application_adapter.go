package projectstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

// ApplicationStoreFactory adapts explicit project SQLite creation/opening to
// the Application ProjectStoreFactory port.
type ApplicationStoreFactory struct{}

func (ApplicationStoreFactory) Create(ctx context.Context, databasePath string) (projectapp.ProjectStore, error) {
	store, err := Open(ctx, databasePath, sharedsqlite.CreateNew)
	if err != nil {
		return nil, mapApplicationOpenError("create", err)
	}
	return &applicationStore{store: store}, nil
}

func (ApplicationStoreFactory) Open(ctx context.Context, databasePath string) (projectapp.ProjectStore, error) {
	store, err := Open(ctx, databasePath, sharedsqlite.OpenExisting)
	if err != nil {
		return nil, mapApplicationOpenError("open", err)
	}
	return &applicationStore{store: store}, nil
}

// applicationStore keeps persistence-specific failures behind the Application
// port while leaving the concrete Store usable in repository tests.
type applicationStore struct {
	store *Store
}

func (s *applicationStore) Initialize(ctx context.Context, project domain.Project, episode domain.Episode) error {
	return s.store.Initialize(ctx, project, episode)
}

func (s *applicationStore) InitializeProject(ctx context.Context, project domain.Project) error {
	return s.store.InitializeProject(ctx, project)
}

func (s *applicationStore) Project(ctx context.Context) (domain.Project, error) {
	return s.store.Project(ctx)
}

func (s *applicationStore) UpdateProject(ctx context.Context, project domain.Project, expectedRevision int64) error {
	return mapApplicationMutationError("update Project", s.store.UpdateProject(ctx, project, expectedRevision))
}

func (s *applicationStore) Episodes(ctx context.Context) ([]domain.EpisodeSummary, error) {
	return s.store.Episodes(ctx)
}

func (s *applicationStore) DeletedEpisodes(ctx context.Context) ([]domain.EpisodeSummary, error) {
	return s.store.DeletedEpisodes(ctx)
}

func (s *applicationStore) Episode(ctx context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	episode, err := s.store.Episode(ctx, episodeID)
	return episode, mapApplicationMutationError("read Episode", err)
}

func (s *applicationStore) EpisodeIncludingDeleted(ctx context.Context, episodeID domain.EpisodeID) (domain.Episode, error) {
	episode, err := s.store.EpisodeIncludingDeleted(ctx, episodeID)
	return episode, mapApplicationMutationError("read Episode including deleted", err)
}

func (s *applicationStore) CreateEpisode(ctx context.Context, episode domain.Episode, projectUpdatedAt time.Time) error {
	return mapApplicationMutationError("create Episode", s.store.CreateEpisode(ctx, episode, projectUpdatedAt))
}

func (s *applicationStore) UpdateEpisode(
	ctx context.Context,
	metadata domain.EpisodeMetadata,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	return mapApplicationMutationError(
		"update Episode",
		s.store.UpdateEpisode(ctx, metadata, expectedRevision, projectUpdatedAt),
	)
}

func (s *applicationStore) DeleteEpisode(ctx context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, updatedAt time.Time) error {
	return mapApplicationMutationError("delete Episode", s.store.DeleteEpisode(ctx, metadata, expectedRevision, updatedAt))
}

func (s *applicationStore) RestoreEpisode(ctx context.Context, metadata domain.EpisodeMetadata, expectedRevision int64, updatedAt time.Time) error {
	return mapApplicationMutationError("restore Episode", s.store.RestoreEpisode(ctx, metadata, expectedRevision, updatedAt))
}

func (s *applicationStore) SaveScript(
	ctx context.Context,
	episodeID domain.EpisodeID,
	script domain.Script,
	expectedRevision int64,
	projectUpdatedAt time.Time,
) error {
	return mapApplicationMutationError(
		"save Episode script",
		s.store.SaveScript(ctx, episodeID, script, expectedRevision, projectUpdatedAt),
	)
}

func (s *applicationStore) Asset(ctx context.Context, assetID domain.AssetID) (domain.EpisodeAsset, error) {
	asset, err := s.store.Asset(ctx, assetID)
	return asset, mapApplicationMutationError("read Asset", err)
}

func (s *applicationStore) EpisodeAssets(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.EpisodeAsset, error) {
	assets, err := s.store.EpisodeAssets(ctx, episodeID, includeDeleted)
	return assets, mapApplicationMutationError("list Episode Assets", err)
}

func (s *applicationStore) AssetIdentity(ctx context.Context, identityID domain.AssetIdentityID) (domain.AssetIdentity, error) {
	identity, err := s.store.AssetIdentity(ctx, identityID)
	return identity, mapApplicationMutationError("read Asset Identity", err)
}

func (s *applicationStore) DeletedStoryboardItems(ctx context.Context) ([]domain.StoryboardScene, []domain.ClipDetails, error) {
	scenes, clips, err := s.store.DeletedStoryboardItems(ctx)
	return scenes, clips, mapApplicationMutationError("list deleted Storyboard items", err)
}

func (s *applicationStore) CreateAsset(ctx context.Context, identity *domain.AssetIdentity, asset domain.Asset, updatedAt time.Time) error {
	return mapApplicationMutationError("create Asset", s.store.CreateAsset(ctx, identity, asset, updatedAt))
}

func (s *applicationStore) CreateDerivedAsset(ctx context.Context, asset domain.Asset, inheritedVersionID domain.MediaVersionID, updatedAt time.Time) error {
	return mapApplicationMutationError("create derived Asset", s.store.CreateDerivedAsset(ctx, asset, inheritedVersionID, updatedAt))
}

func (s *applicationStore) UpdateAsset(ctx context.Context, asset domain.Asset, expectedRevision int64, updatedAt time.Time) error {
	return mapApplicationMutationError("update Asset", s.store.UpdateAsset(ctx, asset, expectedRevision, updatedAt))
}

func (s *applicationStore) DeleteAsset(ctx context.Context, asset domain.Asset, expectedRevision int64, confirmReferenced bool, updatedAt time.Time) error {
	return mapApplicationMutationError("delete Asset", s.store.DeleteAsset(ctx, asset, expectedRevision, confirmReferenced, updatedAt))
}

func (s *applicationStore) UpdateAssetIdentity(ctx context.Context, identity domain.AssetIdentity, expectedRevision int64, updatedAt time.Time) error {
	return mapApplicationMutationError("update Asset Identity", s.store.UpdateAssetIdentity(ctx, identity, expectedRevision, updatedAt))
}

func (s *applicationStore) DetachAssetIdentity(ctx context.Context, identity domain.AssetIdentity, asset domain.Asset, expectedRevision int64, updatedAt time.Time) error {
	return mapApplicationMutationError(
		"detach Asset Identity",
		s.store.DetachAssetIdentity(ctx, identity, asset, expectedRevision, updatedAt),
	)
}

func (s *applicationStore) Storyboard(ctx context.Context, episodeID domain.EpisodeID) (domain.Storyboard, error) {
	value, err := s.store.Storyboard(ctx, episodeID)
	return value, mapApplicationMutationError("read Storyboard", err)
}
func (s *applicationStore) Scene(ctx context.Context, sceneID domain.SceneID) (domain.Scene, error) {
	value, err := s.store.Scene(ctx, sceneID)
	return value, mapApplicationMutationError("read Scene", err)
}
func (s *applicationStore) SceneDetails(ctx context.Context, sceneID domain.SceneID) (domain.StoryboardScene, error) {
	value, err := s.store.SceneDetails(ctx, sceneID)
	return value, mapApplicationMutationError("read Scene details", err)
}
func (s *applicationStore) Clip(ctx context.Context, clipID domain.ClipID) (domain.ClipDetails, error) {
	value, err := s.store.Clip(ctx, clipID)
	return value, mapApplicationMutationError("read Clip", err)
}
func (s *applicationStore) CreateScene(ctx context.Context, value domain.Scene, revision int64, at time.Time) error {
	return mapApplicationMutationError("create Scene", s.store.CreateScene(ctx, value, revision, at))
}
func (s *applicationStore) UpdateScene(ctx context.Context, value domain.Scene, revision int64, at time.Time) error {
	return mapApplicationMutationError("update Scene", s.store.UpdateScene(ctx, value, revision, at))
}
func (s *applicationStore) DeleteScene(ctx context.Context, value domain.Scene, revision, storyboardRevision int64, confirmNonEmpty bool, at time.Time) error {
	return mapApplicationMutationError("delete Scene", s.store.DeleteScene(ctx, value, revision, storyboardRevision, confirmNonEmpty, at))
}
func (s *applicationStore) RestoreScene(ctx context.Context, value domain.Scene, revision, storyboardRevision int64, at time.Time) error {
	return mapApplicationMutationError("restore Scene", s.store.RestoreScene(ctx, value, revision, storyboardRevision, at))
}
func (s *applicationStore) ReorderScenes(ctx context.Context, episodeID domain.EpisodeID, orderedIDs []domain.SceneID, revision int64, at time.Time) error {
	return mapApplicationMutationError("reorder Scenes", s.store.ReorderScenes(ctx, episodeID, orderedIDs, revision, at))
}
func (s *applicationStore) CreateClip(ctx context.Context, value domain.Clip, revision int64, at time.Time) error {
	return mapApplicationMutationError("create Clip", s.store.CreateClip(ctx, value, revision, at))
}
func (s *applicationStore) UpdateClip(ctx context.Context, value domain.Clip, revision int64, at time.Time) error {
	return mapApplicationMutationError("update Clip", s.store.UpdateClip(ctx, value, revision, at))
}
func (s *applicationStore) MoveClip(ctx context.Context, value domain.Clip, beforeID, afterID domain.ClipID, revision, storyboardRevision int64, at time.Time) error {
	return mapApplicationMutationError("move Clip", s.store.MoveClip(ctx, value, beforeID, afterID, revision, storyboardRevision, at))
}
func (s *applicationStore) DeleteClip(ctx context.Context, value domain.Clip, revision, storyboardRevision int64, at time.Time) error {
	return mapApplicationMutationError("delete Clip", s.store.DeleteClip(ctx, value, revision, storyboardRevision, at))
}
func (s *applicationStore) RestoreClip(ctx context.Context, value domain.Clip, revision, storyboardRevision int64, at time.Time) error {
	return mapApplicationMutationError("restore Clip", s.store.RestoreClip(ctx, value, revision, storyboardRevision, at))
}
func (s *applicationStore) BindClipAsset(ctx context.Context, clipID domain.ClipID, assetID domain.AssetID, revision int64, at time.Time) error {
	return mapApplicationMutationError("bind Clip Asset", s.store.BindClipAsset(ctx, clipID, assetID, revision, at))
}
func (s *applicationStore) UnbindClipAsset(ctx context.Context, clipID domain.ClipID, assetID domain.AssetID, revision int64, at time.Time) error {
	return mapApplicationMutationError("unbind Clip Asset", s.store.UnbindClipAsset(ctx, clipID, assetID, revision, at))
}
func (s *applicationStore) ReplaceClipAsset(ctx context.Context, clipID domain.ClipID, oldAssetID, newAssetID domain.AssetID, revision int64, at time.Time) error {
	return mapApplicationMutationError("replace Clip Asset", s.store.ReplaceClipAsset(ctx, clipID, oldAssetID, newAssetID, revision, at))
}
func (s *applicationStore) ReplaceClips(ctx context.Context, sources []domain.Clip, replacements []domain.ClipReplacement, revision int64, at time.Time) error {
	return mapApplicationMutationError("replace Clips", s.store.ReplaceClips(ctx, sources, replacements, revision, at))
}
func (s *applicationStore) ResetStoryboard(ctx context.Context, episodeID domain.EpisodeID, revision int64, requestStops bool, at time.Time) ([]domain.GenerationJob, bool, error) {
	jobs, reset, err := s.store.ResetStoryboard(ctx, episodeID, revision, requestStops, at)
	return jobs, reset, mapApplicationMutationError("reset Storyboard", err)
}
func (s *applicationStore) AssetUsage(ctx context.Context, assetID domain.AssetID) ([]domain.Clip, error) {
	value, err := s.store.AssetUsage(ctx, assetID)
	return value, mapApplicationMutationError("read Asset usage", err)
}

func (s *applicationStore) MediaSlot(ctx context.Context, id domain.MediaSlotID, includeDeleted bool) (domain.MediaSlotDetails, error) {
	value, err := s.store.MediaSlot(ctx, id, includeDeleted)
	return value, mapApplicationMutationError("read MediaSlot", err)
}
func (s *applicationStore) Artifact(ctx context.Context, id domain.ArtifactID) (domain.Artifact, error) {
	value, err := s.store.Artifact(ctx, id)
	return value, mapApplicationMutationError("read Artifact", err)
}
func (s *applicationStore) ArtifactSelectedInEpisode(ctx context.Context, artifactID domain.ArtifactID, episodeID domain.EpisodeID) (bool, error) {
	value, err := s.store.ArtifactSelectedInEpisode(ctx, artifactID, episodeID)
	return value, mapApplicationMutationError("check Episode Artifact selection", err)
}
func (s *applicationStore) ArtifactBySHA256(ctx context.Context, projectID domain.ProjectID, sha string) (domain.Artifact, error) {
	value, err := s.store.ArtifactBySHA256(ctx, projectID, sha)
	return value, mapApplicationMutationError("find Artifact", err)
}
func (s *applicationStore) ClipReferenceArtifacts(ctx context.Context, clipID domain.ClipID) ([]domain.Artifact, error) {
	value, err := s.store.ClipReferenceArtifacts(ctx, clipID)
	return value, mapApplicationMutationError("list Clip reference media", err)
}
func (s *applicationStore) EpisodeReferenceMedia(ctx context.Context, episodeID domain.EpisodeID, artifactID domain.ArtifactID, includeDeleted bool) (domain.EpisodeReferenceMedia, error) {
	value, err := s.store.EpisodeReferenceMedia(ctx, episodeID, artifactID, includeDeleted)
	return value, mapApplicationMutationError("read Episode reference media", err)
}
func (s *applicationStore) EpisodeReferenceMediaList(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.EpisodeReferenceMedia, error) {
	value, err := s.store.EpisodeReferenceMediaList(ctx, episodeID, includeDeleted)
	return value, mapApplicationMutationError("list Episode reference media", err)
}

func (s *applicationStore) AgentConversationMediaList(ctx context.Context, episodeID domain.EpisodeID, sessionID string) ([]domain.AgentConversationMedia, error) {
	return s.store.AgentConversationMediaList(ctx, episodeID, sessionID)
}

func (s *applicationStore) AgentConversationMediaForOperation(ctx context.Context, episodeID domain.EpisodeID, sessionID, operationID string) ([]domain.AgentConversationMedia, error) {
	return s.store.AgentConversationMediaForOperation(ctx, episodeID, sessionID, operationID)
}

func (s *applicationStore) PutAgentConversationMedia(ctx context.Context, value domain.AgentConversationMedia, at time.Time) error {
	return s.store.PutAgentConversationMedia(ctx, value, at)
}

func (s *applicationStore) BindAgentConversationMedia(ctx context.Context, episodeID domain.EpisodeID, sessionID, operationID, turnID string, artifactIDs []domain.ArtifactID, at time.Time) error {
	return s.store.BindAgentConversationMedia(ctx, episodeID, sessionID, operationID, turnID, artifactIDs, at)
}
func (s *applicationStore) PutEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, at time.Time) error {
	return mapApplicationMutationError("put Episode reference media", s.store.PutEpisodeReferenceMedia(ctx, value, at))
}
func (s *applicationStore) UpdateEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, expected int64, at time.Time) error {
	return mapApplicationMutationError("update Episode reference media", s.store.UpdateEpisodeReferenceMedia(ctx, value, expected, at))
}
func (s *applicationStore) DeleteEpisodeReferenceMedia(ctx context.Context, value domain.EpisodeReferenceMedia, expected int64, at time.Time) error {
	return mapApplicationMutationError("delete Episode reference media", s.store.DeleteEpisodeReferenceMedia(ctx, value, expected, at))
}
func (s *applicationStore) AddClipReferenceArtifact(ctx context.Context, clipID domain.ClipID, artifact domain.Artifact, at time.Time) error {
	return mapApplicationMutationError("add Clip reference media", s.store.AddClipReferenceArtifact(ctx, clipID, artifact, at))
}
func (s *applicationStore) DeleteClipReferenceArtifact(ctx context.Context, clipID domain.ClipID, artifactID domain.ArtifactID, at time.Time) error {
	return mapApplicationMutationError("delete Clip reference media", s.store.DeleteClipReferenceArtifact(ctx, clipID, artifactID, at))
}
func (s *applicationStore) AddMediaVersion(ctx context.Context, artifact domain.Artifact, version domain.MediaVersion, revision int64, makeCurrent bool, at time.Time) error {
	return mapApplicationMutationError("add MediaVersion", s.store.AddMediaVersion(ctx, artifact, version, revision, makeCurrent, at))
}
func (s *applicationStore) SelectMediaVersion(ctx context.Context, slotID domain.MediaSlotID, versionID domain.MediaVersionID, revision int64, at time.Time) error {
	return mapApplicationMutationError("select MediaVersion", s.store.SelectMediaVersion(ctx, slotID, versionID, revision, at))
}
func (s *applicationStore) UpdateVideoTrim(ctx context.Context, version domain.MediaVersion, revision int64, at time.Time) error {
	return mapApplicationMutationError("update video trim", s.store.UpdateVideoTrim(ctx, version, revision, at))
}
func (s *applicationStore) DeleteMediaVersion(ctx context.Context, version domain.MediaVersion, versionRevision, slotRevision int64, at time.Time) error {
	return mapApplicationMutationError("delete MediaVersion", s.store.DeleteMediaVersion(ctx, version, versionRevision, slotRevision, at))
}
func (s *applicationStore) RestoreMediaVersion(ctx context.Context, version domain.MediaVersion, versionRevision, slotRevision int64, at time.Time) error {
	return mapApplicationMutationError("restore MediaVersion", s.store.RestoreMediaVersion(ctx, version, versionRevision, slotRevision, at))
}
func (s *applicationStore) AudioReferenceAsset(ctx context.Context, id domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error) {
	value, err := s.store.AudioReferenceAsset(ctx, id)
	return value, mapApplicationMutationError("read AudioReferenceAsset", err)
}
func (s *applicationStore) EpisodeAudioReferenceAssets(ctx context.Context, episodeID domain.EpisodeID, includeDeleted bool) ([]domain.AudioReferenceAsset, error) {
	value, err := s.store.EpisodeAudioReferenceAssets(ctx, episodeID, includeDeleted)
	return value, mapApplicationMutationError("list AudioReferenceAssets", err)
}
func (s *applicationStore) CreateAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, at time.Time) error {
	return mapApplicationMutationError("create AudioReferenceAsset", s.store.CreateAudioReferenceAsset(ctx, value, at))
}
func (s *applicationStore) UpdateAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, revision int64, at time.Time) error {
	return mapApplicationMutationError("update AudioReferenceAsset", s.store.UpdateAudioReferenceAsset(ctx, value, revision, at))
}
func (s *applicationStore) DeleteAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, revision int64, at time.Time) error {
	return mapApplicationMutationError("delete AudioReferenceAsset", s.store.DeleteAudioReferenceAsset(ctx, value, revision, at))
}
func (s *applicationStore) RestoreAudioReferenceAsset(ctx context.Context, value domain.AudioReferenceAsset, revision int64, at time.Time) error {
	return mapApplicationMutationError("restore AudioReferenceAsset", s.store.RestoreAudioReferenceAsset(ctx, value, revision, at))
}
func (s *applicationStore) ClipAudioReference(ctx context.Context, clipID domain.ClipID) (domain.AudioReferenceAssetID, error) {
	value, err := s.store.ClipAudioReference(ctx, clipID)
	return value, mapApplicationMutationError("read Clip audio reference", err)
}
func (s *applicationStore) SetClipAudioReference(ctx context.Context, clipID domain.ClipID, audioID domain.AudioReferenceAssetID, revision int64, at time.Time) error {
	return mapApplicationMutationError("set Clip audio reference", s.store.SetClipAudioReference(ctx, clipID, audioID, revision, at))
}
func (s *applicationStore) ClearClipAudioReference(ctx context.Context, clipID domain.ClipID, revision int64, at time.Time) error {
	return mapApplicationMutationError("clear Clip audio reference", s.store.ClearClipAudioReference(ctx, clipID, revision, at))
}
func (s *applicationStore) GenerationBatch(ctx context.Context, id domain.GenerationBatchID) (domain.GenerationBatchDetails, error) {
	value, err := s.store.GenerationBatch(ctx, id)
	return value, mapApplicationMutationError("read GenerationBatch", err)
}
func (s *applicationStore) GenerationBatchByOperationKey(ctx context.Context, projectID domain.ProjectID, key string) (domain.GenerationBatchDetails, error) {
	value, err := s.store.GenerationBatchByOperationKey(ctx, projectID, key)
	return value, mapApplicationMutationError("find GenerationBatch", err)
}
func (s *applicationStore) GenerationJob(ctx context.Context, id domain.GenerationJobID) (domain.GenerationJob, error) {
	value, err := s.store.GenerationJob(ctx, id)
	return value, mapApplicationMutationError("read GenerationJob", err)
}
func (s *applicationStore) GenerationJobsByStates(ctx context.Context, states []domain.GenerationJobState, limit int) ([]domain.GenerationJob, error) {
	value, err := s.store.GenerationJobsByStates(ctx, states, limit)
	return value, mapApplicationMutationError("list GenerationJobs", err)
}
func (s *applicationStore) RecentGenerationJobs(ctx context.Context, limit int) ([]domain.GenerationJob, error) {
	value, err := s.store.RecentGenerationJobs(ctx, limit)
	return value, mapApplicationMutationError("list recent GenerationJobs", err)
}
func (s *applicationStore) CreateGenerationBatch(ctx context.Context, batch domain.GenerationBatch, jobs []domain.GenerationJob, at time.Time) error {
	return mapApplicationMutationError("create GenerationBatch", s.store.CreateGenerationBatch(ctx, batch, jobs, at))
}
func (s *applicationStore) ClaimGenerationJob(ctx context.Context, id domain.GenerationJobID, revision int64, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.ClaimGenerationJob(ctx, id, revision, at)
	return value, mapApplicationMutationError("claim GenerationJob", err)
}
func (s *applicationStore) RecordGenerationOperation(ctx context.Context, id domain.GenerationJobID, revision int64, operationID, stage, message string, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.RecordGenerationOperation(ctx, id, revision, operationID, stage, message, at)
	return value, mapApplicationMutationError("record Generation operation", err)
}
func (s *applicationStore) UpdateGenerationProgress(ctx context.Context, id domain.GenerationJobID, revision int64, stage, message string, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.UpdateGenerationProgress(ctx, id, revision, stage, message, at)
	return value, mapApplicationMutationError("update Generation progress", err)
}
func (s *applicationStore) CancelGenerationJob(ctx context.Context, id domain.GenerationJobID, revision int64, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.CancelGenerationJob(ctx, id, revision, at)
	return value, mapApplicationMutationError("cancel GenerationJob", err)
}
func (s *applicationStore) RequestGenerationStop(ctx context.Context, id domain.GenerationJobID, revision int64, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.RequestGenerationStop(ctx, id, revision, at)
	return value, mapApplicationMutationError("request Generation stop", err)
}
func (s *applicationStore) FinishGenerationJob(ctx context.Context, id domain.GenerationJobID, revision int64, state domain.GenerationJobState, code, message string, at time.Time) (domain.GenerationJob, error) {
	value, err := s.store.FinishGenerationJob(ctx, id, revision, state, code, message, at)
	return value, mapApplicationMutationError("finish GenerationJob", err)
}
func (s *applicationStore) CompleteGenerationJob(ctx context.Context, id domain.GenerationJobID, revision int64, artifact domain.Artifact, versionID domain.MediaVersionID, at time.Time) error {
	return mapApplicationMutationError("complete GenerationJob", s.store.CompleteGenerationJob(ctx, id, revision, artifact, versionID, at))
}
func (s *applicationStore) ExportJob(ctx context.Context, id domain.ExportJobID) (domain.ExportJob, error) {
	value, err := s.store.ExportJob(ctx, id)
	return value, mapApplicationMutationError("read ExportJob", err)
}
func (s *applicationStore) ExportJobByOperationKey(ctx context.Context, projectID domain.ProjectID, key string) (domain.ExportJob, error) {
	value, err := s.store.ExportJobByOperationKey(ctx, projectID, key)
	return value, mapApplicationMutationError("read ExportJob by operation key", err)
}
func (s *applicationStore) ExportJobsByStates(ctx context.Context, states []domain.ExportJobState, limit int) ([]domain.ExportJob, error) {
	values, err := s.store.ExportJobsByStates(ctx, states, limit)
	return values, mapApplicationMutationError("list active ExportJobs", err)
}
func (s *applicationStore) RecentExportJobs(ctx context.Context, limit int) ([]domain.ExportJob, error) {
	values, err := s.store.RecentExportJobs(ctx, limit)
	return values, mapApplicationMutationError("list ExportJobs", err)
}
func (s *applicationStore) CreateExportJob(ctx context.Context, job domain.ExportJob) error {
	return mapApplicationMutationError("create ExportJob", s.store.CreateExportJob(ctx, job))
}
func (s *applicationStore) ClaimExportJob(ctx context.Context, id domain.ExportJobID, revision int64, at time.Time) (domain.ExportJob, error) {
	value, err := s.store.ClaimExportJob(ctx, id, revision, at)
	return value, mapApplicationMutationError("claim ExportJob", err)
}
func (s *applicationStore) UpdateExportProgress(ctx context.Context, id domain.ExportJobID, revision int64, stage, message string, at time.Time) (domain.ExportJob, error) {
	value, err := s.store.UpdateExportProgress(ctx, id, revision, stage, message, at)
	return value, mapApplicationMutationError("update ExportJob progress", err)
}
func (s *applicationStore) RequestExportCancel(ctx context.Context, id domain.ExportJobID, revision int64, at time.Time) (domain.ExportJob, error) {
	value, err := s.store.RequestExportCancel(ctx, id, revision, at)
	return value, mapApplicationMutationError("cancel ExportJob", err)
}
func (s *applicationStore) FinishExportJob(ctx context.Context, id domain.ExportJobID, revision int64, state domain.ExportJobState, output, code, message string, at time.Time) (domain.ExportJob, error) {
	value, err := s.store.FinishExportJob(ctx, id, revision, state, output, code, message, at)
	return value, mapApplicationMutationError("finish ExportJob", err)
}

func (s *applicationStore) Close() error {
	return s.store.Close()
}

func mapApplicationMutationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var category error
	switch {
	case errors.Is(err, ErrProjectRevisionConflict):
		category = projectapp.ErrRevisionConflict
	case errors.Is(err, ErrEpisodeNotFound):
		category = projectapp.ErrEpisodeNotFound
	case errors.Is(err, ErrEpisodeNotDeleted):
		category = projectapp.ErrEpisodeNotDeleted
	case errors.Is(err, ErrEpisodeHasActiveGeneration):
		category = projectapp.ErrEpisodeHasActiveGeneration
	case errors.Is(err, ErrEpisodeHasActiveExport):
		category = projectapp.ErrEpisodeHasActiveExport
	case errors.Is(err, ErrAssetNotFound):
		category = projectapp.ErrAssetNotFound
	case errors.Is(err, ErrAssetIdentityNotFound):
		category = projectapp.ErrAssetIdentityNotFound
	case errors.Is(err, ErrSceneNotFound):
		category = projectapp.ErrSceneNotFound
	case errors.Is(err, ErrClipNotFound):
		category = projectapp.ErrClipNotFound
	case errors.Is(err, ErrSceneDeleted):
		category = projectapp.ErrSceneDeleted
	case errors.Is(err, ErrSceneNotEmpty):
		category = projectapp.ErrSceneNotEmpty
	case errors.Is(err, ErrSceneOrderMismatch):
		category = projectapp.ErrSceneOrderMismatch
	case errors.Is(err, ErrClipAssetAlreadyBound):
		category = projectapp.ErrClipAssetAlreadyBound
	case errors.Is(err, ErrClipAssetNotBound):
		category = projectapp.ErrClipAssetNotBound
	case errors.Is(err, ErrClipHasActiveGeneration):
		category = projectapp.ErrClipHasActiveGeneration
	case errors.Is(err, ErrClipStructureConflict):
		category = projectapp.ErrInvalidRequest
	case errors.Is(err, ErrMediaSlotNotFound):
		category = projectapp.ErrMediaSlotNotFound
	case errors.Is(err, ErrArtifactNotFound):
		category = projectapp.ErrArtifactNotFound
	case errors.Is(err, ErrMediaVersionNotFound):
		category = projectapp.ErrMediaVersionNotFound
	case errors.Is(err, ErrMediaKindMismatch):
		category = projectapp.ErrMediaKindMismatch
	case errors.Is(err, ErrMediaAspectRatioMismatch):
		category = projectapp.ErrMediaAspectRatioMismatch
	case errors.Is(err, ErrMediaOutputSpecMismatch):
		category = projectapp.ErrMediaOutputSpecMismatch
	case errors.Is(err, ErrMediaVersionAlreadyExists):
		category = projectapp.ErrMediaVersionAlreadyExists
	case errors.Is(err, ErrAudioReferenceNotFound):
		category = projectapp.ErrAudioReferenceNotFound
	case errors.Is(err, ErrAudioReferenceInUse):
		category = projectapp.ErrAudioReferenceInUse
	case errors.Is(err, ErrEpisodeReferenceMediaRevisionConflict):
		category = projectapp.ErrRevisionConflict
	case errors.Is(err, ErrGenerationBatchNotFound):
		category = projectapp.ErrGenerationBatchNotFound
	case errors.Is(err, ErrGenerationJobNotFound):
		category = projectapp.ErrGenerationJobNotFound
	case errors.Is(err, ErrGenerationOperationKeyConflict):
		category = projectapp.ErrGenerationOperationConflict
	case errors.Is(err, ErrGenerationSlotBusy):
		category = projectapp.ErrGenerationSlotBusy
	case errors.Is(err, ErrGenerationStopUnsupported):
		category = projectapp.ErrGenerationStopUnsupported
	case errors.Is(err, ErrGenerationJobStateConflict):
		category = projectapp.ErrGenerationJobStateConflict
	case errors.Is(err, domain.ErrInvalidGenerationSnapshot),
		errors.Is(err, domain.ErrInvalidGenerationJob):
		category = projectapp.ErrGenerationNotReady
	case errors.Is(err, ErrExportJobNotFound):
		category = projectapp.ErrExportJobNotFound
	case errors.Is(err, ErrExportJobStateConflict):
		category = projectapp.ErrExportJobStateConflict
	case errors.Is(err, ErrExportJobConflict):
		category = projectapp.ErrExportEpisodeBusy
	case errors.Is(err, domain.ErrInvalidExportSnapshot):
		category = projectapp.ErrRevisionConflict
	case errors.Is(err, ErrAssetIdentityNameConflict):
		category = projectapp.ErrAssetIdentityNameConflict
	case errors.Is(err, ErrAssetInUse):
		category = projectapp.ErrAssetInUse
	case errors.Is(err, ErrEpisodeAssetIdentityConflict):
		category = projectapp.ErrAssetAlreadyExists
	case errors.Is(err, ErrInvalidAssetProvenance):
		category = projectapp.ErrInvalidAssetProvenance
	case errors.Is(err, ErrEpisodeSequenceConflict),
		errors.Is(err, ErrEpisodeRevisionConflict),
		errors.Is(err, ErrScriptRevisionConflict),
		errors.Is(err, ErrAssetRevisionConflict),
		errors.Is(err, ErrAssetIdentityRevisionConflict),
		errors.Is(err, ErrSceneRevisionConflict),
		errors.Is(err, ErrClipRevisionConflict),
		errors.Is(err, ErrMediaSlotRevisionConflict),
		errors.Is(err, ErrMediaVersionRevisionConflict),
		errors.Is(err, ErrAudioReferenceRevisionConflict):
		category = projectapp.ErrRevisionConflict
	case errors.Is(err, ErrStoryboardRevisionConflict):
		category = projectapp.ErrStoryboardRevisionConflict
	case errors.Is(err, ErrInvalidStoredProjectData),
		errors.Is(err, ErrProjectNotInitialized):
		category = projectapp.ErrProjectDataInvalid
	default:
		category = projectapp.ErrProjectWriteFailed
	}
	return fmt.Errorf("%s: %w: %w", operation, category, err)
}

func mapApplicationOpenError(operation string, err error) error {
	var category error
	switch {
	case errors.Is(err, os.ErrPermission):
		category = projectapp.ErrProjectPathNotWritable
	case errors.Is(err, sharedsqlite.ErrDatabaseNotFound):
		category = projectapp.ErrProjectDatabaseNotFound
	case errors.Is(err, sharedsqlite.ErrSchemaTooNew):
		category = projectapp.ErrProjectSchemaTooNew
	case errors.Is(err, sharedsqlite.ErrMigrationFailed):
		category = projectapp.ErrProjectMigrationFailed
	case errors.Is(err, sharedsqlite.ErrInvalidDatabaseFile),
		errors.Is(err, sharedsqlite.ErrDatabaseKindMismatch):
		category = projectapp.ErrProjectDatabaseInvalid
	default:
		if operation == "create" {
			category = projectapp.ErrProjectCreateFailed
		} else {
			category = projectapp.ErrProjectDatabaseInvalid
		}
	}
	return fmt.Errorf("%s project database: %w: %w", operation, category, err)
}
