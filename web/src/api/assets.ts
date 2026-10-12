import { requestJSON } from './client';
import { truncateUnicode } from '../text';
import { getProject, type EpisodeSummary, type Project } from './projects';

export type AssetKind = 'character' | 'location' | 'prop' | 'style';

export interface AssetIdentity {
  asset_identity_id: string;
  project_id: string;
  kind: AssetKind;
  name: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface EpisodeAsset {
  asset_id: string;
  project_id: string;
  episode_id: string;
  identity: AssetIdentity;
  prompt: string;
  revision: number;
  derived_from_asset_id?: string;
  primary_image_slot_id: string;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface AssetResult {
  project: Project;
  asset: EpisodeAsset;
}

export interface MediaSlot {
  media_slot_id: string;
  episode_id: string;
  owner_kind: string;
  owner_id: string;
  kind: string;
  current_media_version_id: string;
  revision: number;
  updated_at: string;
}

export interface MediaVersion {
  media_version_id: string;
  artifact_id: string;
  sequence_number: number;
  trim_in_ms: number;
  trim_out_ms: number;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface Artifact {
  artifact_id: string;
  kind: 'image' | 'video' | 'audio';
  mime_type: string;
  original_filename: string;
  byte_size: number;
  width: number;
  height: number;
  duration_ms: number;
  remote_reference_expires_at?: string;
  created_at: string;
}

export interface EpisodeReferenceMedia {
  episode_id: string;
  name: string;
  description: string;
  usage_count: number;
  revision: number;
  created_at: string;
  updated_at: string;
  artifact: Artifact;
  /** Episode-local selection; 0/0 (or absent on older responses) means the full original. */
  trim_start_ms?: number;
  trim_end_ms?: number;
  effective_duration_ms?: number;
}

export interface MediaSlotDetails {
  slot: MediaSlot;
  versions: MediaVersion[];
  artifacts: Artifact[];
}

export function listEpisodeAssets(
  projectID: string,
  episodeID: string,
  includeDeleted = false,
): Promise<{ assets: EpisodeAsset[] }> {
  const suffix = includeDeleted ? '?include_deleted=true' : '';
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/assets${suffix}`,
  );
}

export function createAsset(
  projectID: string,
  episodeID: string,
  kind: AssetKind,
  name: string,
  prompt: string,
): Promise<AssetResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/assets`,
    {
      method: 'POST',
      body: JSON.stringify({ kind, name, prompt: truncateUnicode(prompt, 2000) }),
    },
  );
}

export function updateAssetPrompt(
  projectID: string,
  assetID: string,
  prompt: string,
  expectedRevision: number,
): Promise<AssetResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/assets/${encodeURIComponent(assetID)}`,
    {
      method: 'PATCH',
      body: JSON.stringify({
        prompt: truncateUnicode(prompt, 2000),
        expected_revision: expectedRevision,
      }),
    },
  );
}

export function deleteAsset(
  projectID: string,
  assetID: string,
  expectedRevision: number,
): Promise<AssetResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/assets/${encodeURIComponent(assetID)}`,
    {
      method: 'DELETE',
      body: JSON.stringify({ expected_revision: expectedRevision, confirm_referenced: true }),
    },
  );
}

export function renameAssetIdentity(
  projectID: string,
  identityID: string,
  name: string,
  expectedRevision: number,
): Promise<{ project: Project; identity: AssetIdentity }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/asset-identities/${encodeURIComponent(identityID)}:rename`,
    { method: 'POST', body: JSON.stringify({ name, expected_revision: expectedRevision }) },
  );
}

export function getMediaSlot(
  projectID: string,
  slotID: string,
  includeDeleted = true,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}?include_deleted=${includeDeleted}`,
  );
}

export function importMedia(
  projectID: string,
  slotID: string,
  sourcePath: string,
  expectedSlotRevision: number,
  makeCurrent = true,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}/imports`,
    {
      method: 'POST',
      body: JSON.stringify({
        source_path: sourcePath,
        expected_slot_revision: expectedSlotRevision,
        make_current: makeCurrent,
      }),
    },
  );
}

export function uploadMedia(
  projectID: string,
  slotID: string,
  file: File,
  expectedSlotRevision: number,
  makeCurrent = true,
): Promise<MediaSlotDetails> {
  const body = new FormData();
  body.set('file', file);
  body.set('expected_slot_revision', String(expectedSlotRevision));
  body.set('make_current', String(makeCurrent));
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}/uploads`,
    { method: 'POST', body },
  );
}

export function assignArtifact(
  projectID: string,
  slotID: string,
  artifactID: string,
  expectedSlotRevision: number,
  makeCurrent = true,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}/artifacts`,
    {
      method: 'POST',
      body: JSON.stringify({
        artifact_id: artifactID,
        expected_slot_revision: expectedSlotRevision,
        make_current: makeCurrent,
      }),
    },
  );
}

export function extractVideoFrame(
  projectID: string,
  targetSlotID: string,
  sourceVideoSlotID: string,
  expectedTargetRevision: number,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(targetSlotID)}/frame-extractions`,
    {
      method: 'POST',
      body: JSON.stringify({
        source_video_media_slot_id: sourceVideoSlotID,
        expected_target_revision: expectedTargetRevision,
      }),
    },
  );
}

export function updateVideoTrim(
  projectID: string,
  slotID: string,
  versionID: string,
  trimInMilliseconds: number,
  trimOutMilliseconds: number,
  expectedVersionRevision: number,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}/versions/${encodeURIComponent(versionID)}`,
    {
      method: 'PATCH',
      body: JSON.stringify({
        trim_in_ms: trimInMilliseconds,
        trim_out_ms: trimOutMilliseconds,
        expected_version_revision: expectedVersionRevision,
      }),
    },
  );
}

export function listClipReferenceMedia(
  projectID: string,
  episodeID: string,
  clipID: string,
): Promise<{ artifacts: Artifact[] }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/reference-media`,
  );
}

export function uploadClipReferenceMedia(
  projectID: string,
  episodeID: string,
  clipID: string,
  file: File,
  kind: Artifact['kind'],
): Promise<{ artifact: Artifact }> {
  const body = new FormData();
  body.set('file', file);
  body.set('kind', kind);
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/reference-media`,
    { method: 'POST', body },
  );
}

export function deleteClipReferenceMedia(
  projectID: string,
  episodeID: string,
  clipID: string,
  artifactID: string,
): Promise<void> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/reference-media/${encodeURIComponent(artifactID)}`,
    { method: 'DELETE' },
  );
}

export function attachClipReferenceMedia(
  projectID: string,
  episodeID: string,
  clipID: string,
  artifactID: string,
): Promise<void> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/reference-media/${encodeURIComponent(artifactID)}`,
    { method: 'PUT' },
  );
}

export function listEpisodeReferenceMedia(
  projectID: string,
  episodeID: string,
): Promise<{ reference_media: EpisodeReferenceMedia[] }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/reference-media`,
  );
}

export function uploadEpisodeReferenceMedia(
  projectID: string,
  episodeID: string,
  file: File,
  kind: Artifact['kind'],
): Promise<{ reference_media: EpisodeReferenceMedia }> {
  const body = new FormData();
  body.set('file', file);
  body.set('kind', kind);
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/reference-media`,
    { method: 'POST', body },
  );
}

export function updateEpisodeReferenceMedia(
  projectID: string,
  episodeID: string,
  artifactID: string,
  name: string,
  description: string,
  expectedRevision: number,
  trim?: { trim_start_ms: number; trim_end_ms: number },
): Promise<{ reference_media: EpisodeReferenceMedia }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/reference-media/${encodeURIComponent(artifactID)}`,
    {
      method: 'PATCH',
      body: JSON.stringify({ name, description, expected_revision: expectedRevision, ...trim }),
    },
  );
}

export function deleteEpisodeReferenceMedia(
  projectID: string,
  episodeID: string,
  artifactID: string,
  expectedRevision: number,
): Promise<void> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/reference-media/${encodeURIComponent(artifactID)}`,
    { method: 'DELETE', body: JSON.stringify({ expected_revision: expectedRevision }) },
  );
}

export function selectMediaVersion(
  projectID: string,
  slotID: string,
  versionID: string,
  expectedSlotRevision: number,
): Promise<MediaSlotDetails> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/media-slots/${encodeURIComponent(slotID)}/selection`,
    {
      method: 'POST',
      body: JSON.stringify({ media_version_id: versionID, expected_slot_revision: expectedSlotRevision }),
    },
  );
}

export function artifactContentURL(projectID: string, artifactID: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectID)}/artifacts/${encodeURIComponent(artifactID)}/content`;
}

export interface ProjectAssetVariant {
  asset: EpisodeAsset;
  episode: EpisodeSummary;
}

export interface ProjectReferenceImage {
  key: string;
  source: 'asset' | 'upload';
  name: string;
  episode: EpisodeSummary;
  artifact: Artifact;
  updated_at: string;
  asset?: EpisodeAsset;
  reference_media?: EpisodeReferenceMedia;
}

export async function getProjectAssetVariants(
  projectID: string,
  includeDeleted = false,
): Promise<{ project: Project; episodes: EpisodeSummary[]; variants: ProjectAssetVariant[] }> {
  const scope = await getProject(projectID);
  const variants: ProjectAssetVariant[] = [];
  // Keep localhost request pressure bounded for projects with many episodes.
  for (let offset = 0; offset < scope.episodes.length; offset += 8) {
    const batch = scope.episodes.slice(offset, offset + 8);
    const results = await Promise.all(
      batch.map(async (episode) => ({
        episode,
        result: await listEpisodeAssets(projectID, episode.episode_id, includeDeleted),
      })),
    );
    results.forEach(({ episode, result }) =>
      result.assets.forEach((asset) => variants.push({ asset, episode })),
    );
  }
  return { ...scope, variants };
}

export async function getProjectReferenceImages(
  projectID: string,
): Promise<{ project: Project; episodes: EpisodeSummary[]; images: ProjectReferenceImage[] }> {
  const scope = await getProject(projectID);
  const episodes = scope.episodes.filter((episode) => !episode.deleted_at);
  const episodeSources: Array<{
    episode: EpisodeSummary;
    assets: EpisodeAsset[];
    uploads: EpisodeReferenceMedia[];
  }> = [];

  // A project can contain many Episodes. Keep both catalog and MediaSlot request
  // pressure bounded so opening the picker does not overwhelm the local daemon.
  for (let offset = 0; offset < episodes.length; offset += 6) {
    const batch = episodes.slice(offset, offset + 6);
    const results = await Promise.all(
      batch.map(async (episode) => {
        const [assets, uploads] = await Promise.all([
          listEpisodeAssets(projectID, episode.episode_id),
          listEpisodeReferenceMedia(projectID, episode.episode_id),
        ]);
        return {
          episode,
          assets: assets.assets,
          uploads: uploads.reference_media.filter((item) => item.artifact.kind === 'image'),
        };
      }),
    );
    episodeSources.push(...results);
  }

  const assetSources = episodeSources.flatMap(({ episode, assets }) =>
    assets.map((asset) => ({ episode, asset })),
  );
  const generated: ProjectReferenceImage[] = [];
  for (let offset = 0; offset < assetSources.length; offset += 12) {
    const batch = assetSources.slice(offset, offset + 12);
    const results = await Promise.all(
      batch.map(async ({ episode, asset }) => {
        try {
          const media = await getMediaSlot(projectID, asset.primary_image_slot_id, false);
          const version = media.versions.find(
            (item) => item.media_version_id === media.slot.current_media_version_id && !item.deleted_at,
          );
          const artifact = media.artifacts.find((item) => item.artifact_id === version?.artifact_id);
          if (!artifact || artifact.kind !== 'image') return undefined;
          return {
            key: `asset:${asset.asset_id}`,
            source: 'asset' as const,
            name: asset.identity.name,
            episode,
            artifact,
            updated_at: asset.updated_at,
            asset,
          };
        } catch {
          // One stale or unavailable MediaSlot should not make the whole project
          // library unusable. The affected card is simply omitted.
          return undefined;
        }
      }),
    );
    results.forEach((item) => {
      if (item) generated.push(item);
    });
  }

  const uploaded: ProjectReferenceImage[] = episodeSources.flatMap(({ episode, uploads }) =>
    uploads.map((reference_media) => ({
      key: `upload:${episode.episode_id}:${reference_media.artifact.artifact_id}`,
      source: 'upload' as const,
      name: reference_media.name,
      episode,
      artifact: reference_media.artifact,
      updated_at: reference_media.updated_at,
      reference_media,
    })),
  );

  return { ...scope, episodes, images: [...generated, ...uploaded] };
}

export function restoreAsset(
  projectID: string,
  assetID: string,
  expectedRevision: number,
): Promise<AssetResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/assets/${encodeURIComponent(assetID)}:restore`,
    { method: 'POST', body: JSON.stringify({ expected_revision: expectedRevision }) },
  );
}
