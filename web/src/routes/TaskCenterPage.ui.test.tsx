import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { TaskCenterPage } from './TaskCenterPage';

const mocks = vi.hoisted(() => ({
  getTaskSnapshot: vi.fn(),
  listProjects: vi.fn(),
}));

vi.mock('../api/tasks', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/tasks')>();
  return {
    ...original,
    getTaskSnapshot: mocks.getTaskSnapshot,
    useTaskConnectionStatus: () => true,
  };
});
vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, listProjects: mocks.listProjects };
});

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

describe('TaskCenterPage image results', () => {
  beforeEach(() => {
    mocks.listProjects.mockResolvedValue({ projects: [] });
    mocks.getTaskSnapshot.mockResolvedValue({
      stream_epoch: 'epoch',
      cursor: 'epoch:1',
      tasks: [
        {
          task_type: 'generation_job',
          task_id: 'gjb_1',
          project_id: 'prj_1',
          project_name: '红楼梦',
          episode_id: 'ep_1',
          episode_title: '黛玉进府',
          target_media_slot_id: 'msl_1',
          target_media_kind: 'image',
          target_kind: 'asset',
          target_id: 'ast_1',
          target_name: '林黛玉',
          slot_role: 'primary_image',
          generator_id: 'image.codex',
          result_artifact_id: 'art_1',
          state: 'succeeded',
          stop_supported: false,
          progress_stage: 'succeeded',
          progress_message: '',
          failure_code: '',
          revision: 1,
          created_at: '2026-09-10T10:00:00Z',
          updated_at: '2026-09-10T10:04:00Z',
        },
      ],
    });
  });

  it('opens a large preview without losing the selected task row', async () => {
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <TaskCenterPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const preview = await screen.findByRole('button', {
      name: /放大预览林黛玉|Preview 林黛玉/,
    });
    fireEvent.click(preview);

    expect(screen.getByRole('dialog', { name: /放大预览林黛玉|Preview 林黛玉/ })).toHaveAttribute('open');
    expect(document.querySelector('.task-row.active')).toBeInTheDocument();
  });

  it('shows the sanitized official provider failure message', async () => {
    mocks.getTaskSnapshot.mockResolvedValue({
      stream_epoch: 'epoch',
      cursor: 'epoch:2',
      tasks: [
        {
          task_type: 'generation_job',
          task_id: 'gjb_failed',
          project_id: 'prj_1',
          project_name: 'English stories',
          episode_id: 'ep_1',
          episode_title: 'The old man and sea',
          target_media_slot_id: 'msl_1',
          target_media_kind: 'video',
          target_kind: 'clip',
          target_id: 'clp_1',
          target_name: 'An empty boat',
          slot_role: 'video',
          generator_id: 'video.seedance-2.5.volcengine-cn',
          state: 'failed',
          stop_supported: false,
          progress_stage: 'failed',
          progress_message: '',
          failure_code: 'provider_response_error',
          failure_message:
            'Seedance (HTTP 400): SensitiveContent: Reference image may contain a real person.',
          revision: 2,
          created_at: '2026-09-20T01:06:00Z',
          updated_at: '2026-09-20T01:08:00Z',
          started_at: '2026-09-20T01:06:00Z',
          finished_at: '2026-09-20T01:08:00Z',
        },
      ],
    });

    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <TaskCenterPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(
      await screen.findByText(
        'Seedance (HTTP 400): SensitiveContent: Reference image may contain a real person.',
      ),
    ).toBeInTheDocument();
  });

  it('shows a localized safe category instead of an internal translation key', async () => {
    mocks.getTaskSnapshot.mockResolvedValue({
      stream_epoch: 'epoch',
      cursor: 'epoch:3',
      tasks: [
        {
          task_type: 'generation_job',
          task_id: 'gjb_delivery_failed',
          project_id: 'prj_1',
          project_name: '红楼梦',
          episode_id: 'ep_1',
          episode_title: '黛玉葬花',
          target_media_slot_id: 'msl_1',
          target_media_kind: 'video',
          target_kind: 'clip',
          target_id: 'clp_1',
          target_name: '取锦囊袋',
          slot_role: 'video',
          generator_id: 'video.seedance-2.5.volcengine-cn',
          state: 'failed',
          stop_supported: false,
          progress_stage: 'failed',
          progress_message: '',
          failure_code: 'reference_delivery_login_required',
          failure_message: '',
          revision: 2,
          created_at: '2026-09-20T04:14:00Z',
          updated_at: '2026-09-20T04:14:04Z',
          started_at: '2026-09-20T04:14:00Z',
          finished_at: '2026-09-20T04:14:04Z',
        },
      ],
    });

    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <TaskCenterPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/ShotForward.*continuation|承接.*ShotForward/i)).toBeInTheDocument();
    expect(screen.queryByText(/tasks\.failures\./)).not.toBeInTheDocument();
  });
});
