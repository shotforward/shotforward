import { requestJSON } from './client';

export type GenerationMode = 'first_image' | 'first_last_image' | 'reference';

export type VideoPrompts = Record<GenerationMode, string>;

export interface ClipAssetReference {
  asset_id: string;
  asset_identity_id: string;
  kind: string;
  name: string;
  asset_deleted: boolean;
  position: number;
}

export interface Clip {
  clip_id: string;
  project_id: string;
  episode_id: string;
  scene_id: string;
  position: number;
  title: string;
  description: string;
  dialogue: string;
  creative_intent: string;
  planned_duration_ms: number;
  first_image_prompt: string;
  last_image_prompt: string;
  video_prompt: string;
  video_prompts: VideoPrompts;
  generation_mode: GenerationMode;
  continue_from_previous?: boolean;
  revision: number;
  first_image_slot_id: string;
  last_image_slot_id: string;
  video_slot_id: string;
  reference_audio_slot_id: string;
  asset_references: ClipAssetReference[];
  derived_from_clip_ids: string[];
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface Scene {
  scene_id: string;
  project_id: string;
  episode_id: string;
  position: number;
  title: string;
  description: string;
  revision: number;
  clips: Clip[];
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface StoryboardResult {
  storyboard: { episode_id: string; revision: number; planned_duration_ms: number; scenes: Scene[] };
  target_duration_ms: number;
  duration_warning: boolean;
}

export interface ClipFields {
  title: string;
  description: string;
  dialogue: string;
  creative_intent: string;
  planned_duration_seconds: number;
  first_image_prompt: string;
  last_image_prompt: string;
  video_prompts: VideoPrompts;
  generation_mode: GenerationMode;
  continue_from_previous: boolean;
}

export function getStoryboard(projectID: string, episodeID: string): Promise<StoryboardResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/storyboard`,
  );
}

export function createScene(
  projectID: string,
  episodeID: string,
  title: string,
  description: string,
  expectedStoryboardRevision: number,
): Promise<{ scene: Scene }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/scenes`,
    {
      method: 'POST',
      body: JSON.stringify({
        title,
        description,
        source: null,
        expected_storyboard_revision: expectedStoryboardRevision,
      }),
    },
  );
}

export function createClip(
  projectID: string,
  episodeID: string,
  sceneID: string,
  fields: ClipFields,
  expectedStoryboardRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips`,
    {
      method: 'POST',
      body: JSON.stringify({
        scene_id: sceneID,
        expected_storyboard_revision: expectedStoryboardRevision,
        ...fields,
      }),
    },
  );
}

export function updateClip(
  projectID: string,
  episodeID: string,
  clipID: string,
  fields: ClipFields,
  expectedRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}`,
    { method: 'PATCH', body: JSON.stringify({ expected_revision: expectedRevision, ...fields }) },
  );
}

export function deleteScene(
  projectID: string,
  episodeID: string,
  sceneID: string,
  expectedRevision: number,
  expectedStoryboardRevision: number,
): Promise<{ scene: Scene }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/scenes/${encodeURIComponent(sceneID)}:delete`,
    {
      method: 'POST',
      body: JSON.stringify({
        expected_revision: expectedRevision,
        expected_storyboard_revision: expectedStoryboardRevision,
        confirm_non_empty: true,
      }),
    },
  );
}

export function deleteClip(
  projectID: string,
  episodeID: string,
  clipID: string,
  expectedRevision: number,
  expectedStoryboardRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}:delete`,
    {
      method: 'POST',
      body: JSON.stringify({
        expected_revision: expectedRevision,
        expected_storyboard_revision: expectedStoryboardRevision,
      }),
    },
  );
}

export function bindClipAsset(
  projectID: string,
  episodeID: string,
  clipID: string,
  assetID: string,
  expectedClipRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/asset-references`,
    {
      method: 'POST',
      body: JSON.stringify({ asset_id: assetID, expected_clip_revision: expectedClipRevision }),
    },
  );
}

export function unbindClipAsset(
  projectID: string,
  episodeID: string,
  clipID: string,
  assetID: string,
  expectedClipRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}/asset-references/${encodeURIComponent(assetID)}`,
    { method: 'DELETE', body: JSON.stringify({ expected_clip_revision: expectedClipRevision }) },
  );
}

export const videoPromptFamilies = ['h3', 'seedance', 'wan3'] as const;
export type VideoPromptFamily = (typeof videoPromptFamilies)[number];
export interface VideoPromptReference { key: string; purposes: string[]; note?: string }
export interface VideoPromptVariant {
  prompt: string;
  profile: string;
  generator_id: string;
  provider_model_id: string;
  asset_ids: string[];
  references: VideoPromptReference[];
}
export interface VideoPromptSet {
  v: 1;
  primary_family?: VideoPromptFamily;
  legacy_prompt?: string;
  families: Partial<Record<VideoPromptFamily, VideoPromptVariant>>;
}

export function videoPromptFamily(generatorID: string): VideoPromptFamily | undefined {
  if (generatorID.startsWith('video.h3.')) return 'h3';
  if (generatorID.includes('seedance')) return 'seedance';
  if (generatorID.includes('wan-3.0')) return 'wan3';
}

export function videoPromptProfile(family: VideoPromptFamily, mode: GenerationMode) {
  if (family === 'h3') return mode === 'reference' ? 'h3_ref2va@1' : 'h3_base@1';
  return family === 'seedance' ? 'seedance@1' : 'wan3@1';
}

export function parseVideoPromptSet(value: string): VideoPromptSet {
  const trimmed = value.trim();
  if (!trimmed) return { v: 1, families: {} };
  try {
    const candidate = JSON.parse(trimmed) as Partial<VideoPromptSet>;
    if (candidate.v !== 1 || !candidate.families || typeof candidate.families !== 'object')
      throw new Error('not a video prompt set');
    const families: VideoPromptSet['families'] = {};
    for (const family of videoPromptFamilies) {
      const entry = candidate.families[family];
      if (!entry || typeof entry.prompt !== 'string') continue;
      families[family] = {
        prompt: entry.prompt,
        profile: typeof entry.profile === 'string' ? entry.profile : '',
        generator_id: typeof entry.generator_id === 'string' ? entry.generator_id : '',
        provider_model_id: typeof entry.provider_model_id === 'string' ? entry.provider_model_id : '',
        asset_ids: Array.isArray(entry.asset_ids)
          ? entry.asset_ids.filter((item): item is string => typeof item === 'string')
          : [],
        references: Array.isArray(entry.references) ? entry.references as VideoPromptReference[] : [],
      };
    }
    return {
      v: 1,
      ...(videoPromptFamilies.includes(candidate.primary_family as VideoPromptFamily)
        ? { primary_family: candidate.primary_family as VideoPromptFamily }
        : {}),
      ...(typeof candidate.legacy_prompt === 'string' && candidate.legacy_prompt.trim()
        ? { legacy_prompt: candidate.legacy_prompt }
        : {}),
      families,
    };
  } catch {
    return { v: 1, legacy_prompt: value, families: {} };
  }
}

export function videoPromptVariant(value: string, family: VideoPromptFamily | undefined) {
  const set = parseVideoPromptSet(value);
  if (family && set.families[family]) return set.families[family];
  if (set.legacy_prompt !== undefined && Object.keys(set.families).length === 0)
    return {
      prompt: set.legacy_prompt, profile: 'legacy', generator_id: '', provider_model_id: '',
      asset_ids: [], references: [],
    } satisfies VideoPromptVariant;
}

export function videoPromptText(value: string, family: VideoPromptFamily | undefined) {
  return videoPromptVariant(value, family)?.prompt ?? '';
}

export function updateVideoPromptFamily(
  value: string,
  family: VideoPromptFamily,
  prompt: string,
  options: Partial<Omit<VideoPromptVariant, 'prompt'>> = {},
) {
  const set = parseVideoPromptSet(value);
  const existing = set.families[family];
  set.primary_family ??= family;
  set.families[family] = {
    prompt,
    profile: options.profile ?? existing?.profile ?? '',
    generator_id: options.generator_id ?? existing?.generator_id ?? '',
    provider_model_id: options.provider_model_id ?? existing?.provider_model_id ?? '',
    asset_ids: options.asset_ids ?? existing?.asset_ids ?? [],
    references: options.references ?? existing?.references ?? [],
  };
  return JSON.stringify(set);
}

export function allVideoPromptText(value: string) {
  const set = parseVideoPromptSet(value);
  return [set.legacy_prompt ?? '', ...videoPromptFamilies.map((family) => set.families[family]?.prompt ?? '')]
    .filter(Boolean)
    .join('\n');
}
