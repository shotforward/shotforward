import { beforeEach, describe, expect, it } from 'vitest';
import type { EpisodeResult } from './api/episodes';
import {
  getEpisodeWorkspacePath,
  parseEpisodeRoute,
  readRecentEpisodes,
  renameRecentProject,
  rememberRecentEpisode,
} from './recentEpisodes';

const episodeResult = (projectId: string, episodeId: string, title: string): EpisodeResult => ({
  project: {
    project_id: projectId,
    name: `Project ${projectId}`,
    description: '',
    output_width: 720,
    output_height: 1280,
    revision: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  episode: {
    episode_id: episodeId,
    project_id: projectId,
    sequence_number: 1,
    title,
    target_duration_ms: 60_000,
    revision: 1,
    script_revision: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    script_updated_at: '2026-01-01T00:00:00Z',
    script: { markdown: '', revision: 1, updated_at: '2026-01-01T00:00:00Z', character_count: 0 },
  },
  episodes: [],
});

describe('recent episodes', () => {
  beforeEach(() => window.localStorage.clear());

  it('parses episode workspace paths including focused storyboard clips', () => {
    expect(parseEpisodeRoute('/projects/prj_1/episodes/ep_1/storyboard/clips/clip_1')).toEqual({
      projectId: 'prj_1',
      episodeId: 'ep_1',
      section: 'storyboard',
    });
    expect(parseEpisodeRoute('/projects/prj_1')).toBeUndefined();
  });

  it('keeps each episode once and moves its latest workspace to the front', () => {
    rememberRecentEpisode(
      episodeResult('prj_1', 'ep_1', 'Episode one'),
      '/projects/prj_1/episodes/ep_1/script',
      '2026-09-08T08:00:00Z',
    );
    rememberRecentEpisode(
      episodeResult('prj_2', 'ep_2', 'Episode two'),
      '/projects/prj_2/episodes/ep_2/assets',
      '2026-09-08T09:00:00Z',
    );
    rememberRecentEpisode(
      episodeResult('prj_1', 'ep_1', 'Episode one renamed'),
      '/projects/prj_1/episodes/ep_1/preview',
      '2026-09-08T10:00:00Z',
    );

    expect(readRecentEpisodes()).toMatchObject([
      { projectId: 'prj_1', episodeTitle: 'Episode one renamed', section: 'preview' },
      { projectId: 'prj_2', episodeTitle: 'Episode two', section: 'assets' },
    ]);
    expect(getEpisodeWorkspacePath('prj_1', 'ep_1')).toBe('/projects/prj_1/episodes/ep_1/preview');
    expect(getEpisodeWorkspacePath('prj_1', 'ep_new')).toBe('/projects/prj_1/episodes/ep_new/script');
  });

  it('ignores malformed persisted values', () => {
    window.localStorage.setItem('shotforward.recent-episodes', '{broken');
    expect(readRecentEpisodes()).toEqual([]);
  });

  it('updates cached project names without changing recent episode order or paths', () => {
    rememberRecentEpisode(
      episodeResult('prj_1', 'ep_1', 'Episode one'),
      '/projects/prj_1/episodes/ep_1/script',
      '2026-09-08T08:00:00Z',
    );

    renameRecentProject('prj_1', 'Renamed project');

    expect(readRecentEpisodes()).toMatchObject([
      {
        projectId: 'prj_1',
        projectName: 'Renamed project',
        path: '/projects/prj_1/episodes/ep_1/script',
      },
    ]);
  });
});
