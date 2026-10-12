import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { EpisodeResult } from '../api/episodes';
import '../i18n';
import { rememberRecentEpisode } from '../recentEpisodes';
import { AppShell } from './AppShell';

const mocks = vi.hoisted(() => ({
  getEpisode: vi.fn(),
  getTaskSnapshot: vi.fn(),
  openProject: vi.fn(),
  getRuntime: vi.fn(),
}));

vi.mock('../api/runtime', () => ({ getRuntime: mocks.getRuntime }));

vi.mock('../api/episodes', () => ({ getEpisode: mocks.getEpisode }));
vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, openProject: mocks.openProject };
});
vi.mock('../api/tasks', () => ({
  getTaskSnapshot: mocks.getTaskSnapshot,
  useTaskEvents: vi.fn(),
}));

const episodeResult: EpisodeResult = {
  project: {
    project_id: 'prj_1',
    name: '红楼梦',
    description: '',
    output_width: 720,
    output_height: 1280,
    revision: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-09-08T08:00:00Z',
  },
  episode: {
    episode_id: 'ep_1',
    project_id: 'prj_1',
    sequence_number: 1,
    title: '第 1 集',
    target_duration_ms: 60_000,
    revision: 1,
    script_revision: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-09-08T08:00:00Z',
    script_updated_at: '2026-09-08T08:00:00Z',
    script: { markdown: '', revision: 1, updated_at: '2026-09-08T08:00:00Z', character_count: 0 },
  },
  episodes: [],
};

describe('AppShell project navigation', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.getTaskSnapshot.mockResolvedValue({ cursor: '', tasks: [] });
    mocks.getRuntime.mockResolvedValue({ runtime: { health: 'ready' }, models: [] });
    mocks.getEpisode.mockResolvedValue(episodeResult);
    mocks.openProject.mockResolvedValue({
      project: episodeResult.project,
      episodes: [episodeResult.episode],
    });
  });

  it('starts runtime initialization in the background without blocking the authorized app content', async () => {
    mocks.getRuntime.mockReturnValue(new Promise(() => {}));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { unmount } = render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/projects']}>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/projects" element={<div>Project library</div>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(screen.getByText('Project library')).toBeInTheDocument();
    await waitFor(() => expect(mocks.getRuntime).toHaveBeenCalledTimes(1));
    expect(client.getQueryState(['agent-runtime'])?.fetchStatus).toBe('fetching');
    expect(mocks.getTaskSnapshot).toHaveBeenCalledTimes(1);
    unmount();
    client.clear();
  });

  it('uses Projects as the primary entry and groups recent work beneath it', async () => {
    rememberRecentEpisode(
      episodeResult,
      '/projects/prj_1/episodes/ep_1/storyboard/clips/clip_1',
      '2026-09-08T08:00:00Z',
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/projects']}>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/projects" element={<div>Project library</div>} />
              <Route
                path="/projects/:projectId/episodes/:episodeId/storyboard/clips/:clipId"
                element={<div>Episode workspace</div>}
              />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(screen.getByLabelText(/最近创作|Recent work/i)).toHaveTextContent('红楼梦');
    expect(screen.queryByRole('button', { name: /工作台|Workspace/i })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /^项目$|^Projects$/i })).toHaveClass('is-active');
    fireEvent.click(screen.getByRole('link', { name: /第 1 集/ }));

    await waitFor(() => expect(mocks.openProject).toHaveBeenCalledWith('prj_1'));
    expect(await screen.findByText('Episode workspace')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /第 1 集/ })).toHaveClass('is-active');
    expect(screen.getByRole('link', { name: /^项目$|^Projects$/i })).not.toHaveClass('is-active');
    expect(screen.getByRole('link', { name: /设置|Settings/i })).toHaveClass('studio-nav__settings-link');
    expect(screen.getByRole('link', { name: /设置|Settings/i }).closest('nav')).toHaveClass('studio-nav');
    expect(document.querySelector('.studio-sidebar .language-switch')).not.toBeInTheDocument();
  });

  it('keeps a recent episode as a direct, clickable navigation link', async () => {
    rememberRecentEpisode(
      episodeResult,
      '/projects/prj_1/episodes/ep_1/storyboard/clips/clip_1',
      '2026-09-08T08:00:00Z',
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/projects']}>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/projects" element={<div>Project library</div>} />
              <Route
                path="/projects/:projectId/episodes/:episodeId/storyboard/clips/:clipId"
                element={<div>Episode workspace</div>}
              />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const recent = screen.getByRole('link', { name: /第 1 集/ });
    expect(recent).toHaveAttribute('href', '/projects/prj_1/episodes/ep_1/storyboard/clips/clip_1');
    fireEvent.click(recent);

    expect(await screen.findByText('Episode workspace')).toBeInTheDocument();
    await waitFor(() => expect(mocks.openProject).toHaveBeenCalledWith('prj_1'));
  });
});
