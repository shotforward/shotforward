import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { ProjectOverviewPage } from './ProjectOverviewPage';
import { sessionPreparationKey, type AgentSessionPreparation } from '../agentSessionPreparation';

const mocks = vi.hoisted(() => ({
  getProject: vi.fn(),
  createEpisode: vi.fn(),
  getRuntime: vi.fn(),
  createSession: vi.fn(),
}));

vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, getProject: mocks.getProject, createEpisode: mocks.createEpisode };
});
vi.mock('../api/runtime', () => ({ getRuntime: mocks.getRuntime }));
vi.mock('../api/agent', () => ({ createSession: mocks.createSession }));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/projects/prj_1']}>
        <Routes>
          <Route path="/projects/:projectId" element={<ProjectOverviewPage />} />
          <Route path="/projects/:projectId/episodes/:episodeId/script" element={<PreparationStatus />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, client };
}

function PreparationStatus() {
  const preparation = useQuery<AgentSessionPreparation | null>({
    queryKey: sessionPreparationKey('prj_1', 'ep_1', 'shotforward'),
    queryFn: () => null,
    enabled: false,
  });
  return <div>Video workspace: {preparation.data?.status}</div>;
}

describe('ProjectOverviewPage', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.setAttribute('open', '');
    };
    HTMLDialogElement.prototype.close = function close() {
      this.removeAttribute('open');
      this.dispatchEvent(new Event('close'));
    };
  });

  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.getRuntime.mockResolvedValue({
      active_runtime_kind: 'shotforward',
      runtime: { health: 'ready' },
      models: [{ id: 'gpt-test', display_name: 'GPT Test', runtime_kind: 'shotforward' }],
    });
    mocks.getProject.mockResolvedValue({
      project: {
        project_id: 'prj_1',
        name: '红楼梦',
        description: '一部古典题材作品',
        output_width: 720,
        output_height: 1280,
        revision: 1,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-09-08T08:00:00Z',
      },
      episodes: [
        {
          episode_id: 'ep_1',
          project_id: 'prj_1',
          sequence_number: 1,
          title: '第 1 集',
          target_duration_ms: 60_000,
          revision: 1,
          script_revision: 2,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-09-08T08:00:00Z',
          script_updated_at: '2026-09-08T08:00:00Z',
        },
      ],
    });
  });

  it('shows real project metadata and the episode table without placeholder artwork', async () => {
    renderPage();

    expect(await screen.findByRole('heading', { name: '红楼梦' })).toBeInTheDocument();
    expect(screen.getByText('一部古典题材作品')).toBeInTheDocument();
    expect(screen.getByText('9:16 · 720 × 1280')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /第 1 集/ })).toHaveAttribute(
      'href',
      '/projects/prj_1/episodes/ep_1/script',
    );
    expect(document.querySelector('.project-heading__cover')).not.toBeInTheDocument();
    expect(document.querySelector('.episode-row__thumb')).not.toBeInTheDocument();
    expect(document.querySelector('.continue-card')).not.toBeInTheDocument();
    const navigation = screen.getByRole('navigation', { name: /项目管理|Project management/i });
    expect(within(navigation).getAllByRole('link')).toHaveLength(3);
    expect(within(navigation).getByRole('link', { name: /视频|Videos/i })).toHaveClass('active');
    expect(within(navigation).queryByRole('link', { name: /回收站|Trash/i })).not.toBeInTheDocument();
  });

  it('keeps an empty project intentional and creates the first video with duration and an Agent session', async () => {
    const project = (await mocks.getProject()).project;
    mocks.getProject.mockResolvedValue({ project, episodes: [] });
    mocks.createEpisode.mockResolvedValue({
      project,
      episode: {
        episode_id: 'ep_1',
        project_id: 'prj_1',
        sequence_number: 1,
        title: '视频 1',
        target_duration_ms: 30_000,
        revision: 1,
        script_revision: 1,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
        script_updated_at: '2026-01-01T00:00:00Z',
      },
      episodes: [],
    });
    mocks.createSession.mockResolvedValue({ session: { id: 'ags_1', model_id: 'gpt-test' } });

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /创建第一个视频|Create first video/i }));
    const dialog = screen.getByRole('dialog', { name: /新建视频|Create video/i });
    expect(within(dialog).getByText('红楼梦')).toBeInTheDocument();
    expect(within(dialog).getByText(/视频名称|Video name/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '00:30' }));
    fireEvent.click(screen.getByRole('button', { name: /创建并开始创作|Create and start/i }));

    await waitFor(() =>
      expect(mocks.createEpisode).toHaveBeenCalledWith('prj_1', {
        title: expect.stringMatching(/^(视频 1|Video 1)$/),
        target_duration_ms: 30_000,
      }),
    );
    expect(mocks.createSession).toHaveBeenCalledWith('prj_1', 'ep_1', 'gpt-test', 'shotforward');
  });

  it('navigates before background Agent preparation finishes and preserves its failure for the workspace', async () => {
    const existing = await mocks.getProject();
    mocks.createEpisode.mockResolvedValue({
      project: existing.project,
      episode: existing.episodes[0],
      episodes: existing.episodes,
    });
    let reject!: (error: Error) => void;
    mocks.createSession.mockReturnValue(
      new Promise((_resolve, fail) => {
        reject = fail;
      }),
    );
    const { client } = renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /新建视频|New video/i }));
    fireEvent.click(screen.getByRole('button', { name: /创建并开始创作|Create and start/i }));
    expect(await screen.findByText('Video workspace: preparing')).toBeInTheDocument();
    expect(mocks.createSession).toHaveBeenCalledTimes(1);
    await act(async () => {
      reject(new Error('offline'));
    });
    expect(await screen.findByText('Video workspace: failed')).toBeInTheDocument();
    expect(client.getQueryData(['project', 'prj_1'])).toMatchObject({ project: existing.project });
    client.clear();
  });
});
