import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { readRecentEpisodes, rememberRecentEpisode } from '../recentEpisodes';
import type { EpisodeResult } from '../api/episodes';
import { ProjectSettingsPage } from './ProjectSettingsPage';

const mocks = vi.hoisted(() => ({
  getProject: vi.fn(),
  updateProject: vi.fn(),
  deleteProject: vi.fn(),
}));

vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, ...mocks };
});

const projectResult = {
  project: {
    project_id: 'prj_1',
    name: '旧项目名',
    description: '旧简介',
    output_width: 720,
    output_height: 1280,
    revision: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-09-20T08:00:00Z',
  },
  episodes: [],
  root_path: '/projects/story',
};

describe('ProjectSettingsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.getProject.mockResolvedValue(projectResult);
    mocks.updateProject.mockResolvedValue({
      ...projectResult,
      project: {
        ...projectResult.project,
        name: '新项目名',
        description: '新简介',
        revision: 2,
      },
    });
  });

  it('updates project metadata and refreshes the cached recent-work name', async () => {
    rememberRecentEpisode(
      {
        project: projectResult.project,
        episode: {
          episode_id: 'ep_1',
          project_id: 'prj_1',
          sequence_number: 1,
          title: '第 1 集',
          target_duration_ms: 60_000,
          revision: 1,
          script_revision: 1,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-09-20T08:00:00Z',
          script_updated_at: '2026-09-20T08:00:00Z',
          script: { markdown: '', revision: 1, updated_at: '2026-09-20T08:00:00Z', character_count: 0 },
        },
        episodes: [],
      } satisfies EpisodeResult,
      '/projects/prj_1/episodes/ep_1/script',
    );
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/projects/prj_1/settings#project-name']}>
          <Routes>
            <Route path="/projects/:projectId/settings" element={<ProjectSettingsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const name = await screen.findByLabelText(/^(项目名称|Project name)$/i);
    expect(name).toHaveFocus();
    fireEvent.change(name, { target: { value: '新项目名' } });
    fireEvent.change(screen.getByLabelText(/^(项目描述|Description)$/i), { target: { value: '新简介' } });
    fireEvent.click(screen.getByRole('button', { name: /保存更改|Save changes/i }));

    await waitFor(() =>
      expect(mocks.updateProject).toHaveBeenCalledWith('prj_1', {
        name: '新项目名',
        description: '新简介',
        expected_revision: 1,
      }),
    );
    expect(await screen.findByRole('status')).toHaveTextContent(/已保存|saved/i);
    expect(readRecentEpisodes()[0]?.projectName).toBe('新项目名');
    expect(screen.getByDisplayValue('/projects/story')).toHaveAttribute('readonly');
  });
});
