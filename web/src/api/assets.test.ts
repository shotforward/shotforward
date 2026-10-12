import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ requestJSON: vi.fn() }));

vi.mock('./client', () => ({ requestJSON: mocks.requestJSON }));

import { extractVideoFrame, getProjectReferenceImages, updateEpisodeReferenceMedia } from './assets';

const episode = (id: string, sequence: number, title: string) => ({
  episode_id: id,
  project_id: 'project_1',
  sequence_number: sequence,
  title,
  target_duration_ms: 60_000,
  revision: 1,
  script_revision: 1,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  script_updated_at: '2026-09-01T00:00:00Z',
});

const artifact = (id: string, kind: 'image' | 'video' = 'image') => ({
  artifact_id: id,
  kind,
  mime_type: kind === 'image' ? 'image/png' : 'video/mp4',
  original_filename: `${id}.${kind === 'image' ? 'png' : 'mp4'}`,
  byte_size: 100,
  width: 720,
  height: 1280,
  duration_ms: kind === 'image' ? 0 : 5_000,
  created_at: '2026-09-01T00:00:00Z',
});

const asset = (id: string, episodeID: string, slotID: string) => ({
  asset_id: id,
  project_id: 'project_1',
  episode_id: episodeID,
  identity: {
    asset_identity_id: 'identity_tanchun',
    project_id: 'project_1',
    kind: 'character',
    name: '贾探春',
    revision: 1,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
  },
  prompt: '',
  revision: 1,
  primary_image_slot_id: slotID,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: `2026-09-0${episodeID === 'episode_1' ? '1' : '2'}T00:00:00Z`,
});

describe('getProjectReferenceImages', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.requestJSON.mockImplementation((url: string) => {
      if (url === '/api/v1/projects/project_1') {
        return Promise.resolve({
          project: { project_id: 'project_1', name: '红楼梦' },
          episodes: [episode('episode_1', 1, '初入贾府'), episode('episode_3', 3, '凤姐提醒')],
        });
      }
      if (url.endsWith('/episodes/episode_1/assets')) {
        return Promise.resolve({ assets: [asset('asset_1', 'episode_1', 'slot_1')] });
      }
      if (url.endsWith('/episodes/episode_3/assets')) {
        return Promise.resolve({ assets: [asset('asset_3', 'episode_3', 'slot_3')] });
      }
      if (url.endsWith('/episodes/episode_1/reference-media')) {
        return Promise.resolve({
          reference_media: [
            {
              episode_id: 'episode_1',
              name: '服装参考',
              description: '',
              usage_count: 0,
              revision: 1,
              created_at: '2026-09-01T00:00:00Z',
              updated_at: '2026-09-01T00:00:00Z',
              artifact: artifact('upload_image'),
            },
            {
              episode_id: 'episode_1',
              name: '动作视频',
              description: '',
              usage_count: 0,
              revision: 1,
              created_at: '2026-09-01T00:00:00Z',
              updated_at: '2026-09-01T00:00:00Z',
              artifact: artifact('upload_video', 'video'),
            },
          ],
        });
      }
      if (url.endsWith('/episodes/episode_3/reference-media')) {
        return Promise.resolve({ reference_media: [] });
      }
      if (url.endsWith('/media-slots/slot_1?include_deleted=false')) {
        return Promise.resolve({
          slot: { media_slot_id: 'slot_1', current_media_version_id: 'version_current_1' },
          versions: [
            {
              media_version_id: 'version_old_1',
              artifact_id: 'asset_old_1',
              sequence_number: 1,
            },
            {
              media_version_id: 'version_current_1',
              artifact_id: 'asset_current_1',
              sequence_number: 2,
            },
          ],
          artifacts: [artifact('asset_old_1'), artifact('asset_current_1')],
        });
      }
      if (url.endsWith('/media-slots/slot_3?include_deleted=false')) {
        return Promise.resolve({
          slot: { media_slot_id: 'slot_3', current_media_version_id: 'version_current_3' },
          versions: [
            {
              media_version_id: 'version_current_3',
              artifact_id: 'asset_current_3',
              sequence_number: 1,
            },
          ],
          artifacts: [artifact('asset_current_3')],
        });
      }
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });
  });

  it('keeps each Episode variant and indexes only current generated images plus image uploads', async () => {
    const result = await getProjectReferenceImages('project_1');

    expect(result.images).toHaveLength(3);
    expect(
      result.images.map((item) => [item.key, item.episode.episode_id, item.artifact.artifact_id]),
    ).toEqual([
      ['asset:asset_1', 'episode_1', 'asset_current_1'],
      ['asset:asset_3', 'episode_3', 'asset_current_3'],
      ['upload:episode_1:upload_image', 'episode_1', 'upload_image'],
    ]);
    expect(result.images.some((item) => item.artifact.artifact_id === 'asset_old_1')).toBe(false);
    expect(result.images.some((item) => item.artifact.artifact_id === 'upload_video')).toBe(false);
  });
});

describe('extractVideoFrame', () => {
  it('posts the adjacent video slot and optimistic target revision', async () => {
    mocks.requestJSON.mockResolvedValue({ slot: { media_slot_id: 'slot_first' } });

    await extractVideoFrame('project 1', 'slot_first', 'slot_previous_video', 3);

    expect(mocks.requestJSON).toHaveBeenCalledWith(
      '/api/v1/projects/project%201/media-slots/slot_first/frame-extractions',
      {
        method: 'POST',
        body: JSON.stringify({
          source_video_media_slot_id: 'slot_previous_video',
          expected_target_revision: 3,
        }),
      },
    );
  });
});

describe('updateEpisodeReferenceMedia trim contract', () => {
  it('sends paired Episode trim metadata with the optimistic revision, without changing the Artifact', async () => {
    mocks.requestJSON.mockResolvedValue({ reference_media: {} });
    await updateEpisodeReferenceMedia('project 1', 'episode 1', 'audio 1', 'Voice', 'tone reference', 4, {
      trim_start_ms: 3125,
      trim_end_ms: 9375,
    });
    expect(mocks.requestJSON).toHaveBeenLastCalledWith(
      '/api/v1/projects/project%201/episodes/episode%201/reference-media/audio%201',
      {
        method: 'PATCH',
        body: JSON.stringify({
          name: 'Voice',
          description: 'tone reference',
          expected_revision: 4,
          trim_start_ms: 3125,
          trim_end_ms: 9375,
        }),
      },
    );
    await updateEpisodeReferenceMedia('project', 'episode', 'audio', 'Voice', '', 5, {
      trim_start_ms: 0,
      trim_end_ms: 0,
    });
    expect(JSON.parse(mocks.requestJSON.mock.lastCall![1].body)).toMatchObject({
      trim_start_ms: 0,
      trim_end_ms: 0,
      expected_revision: 5,
    });
  });
  it('omits trim fields for legacy name-only and image updates', async () => {
    mocks.requestJSON.mockResolvedValue({ reference_media: {} });
    await updateEpisodeReferenceMedia('project', 'episode', 'image', 'Photo', '', 1);
    expect(JSON.parse(mocks.requestJSON.mock.lastCall![1].body)).toEqual({
      name: 'Photo',
      description: '',
      expected_revision: 1,
    });
  });
});
