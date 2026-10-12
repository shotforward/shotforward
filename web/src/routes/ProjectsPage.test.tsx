import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { APIError } from '../api/client';
import { ProjectsPage } from './ProjectsPage';

const mocks = vi.hoisted(() => ({
  listProjects: vi.fn(),
  openProject: vi.fn(),
  deleteProject: vi.fn(),
}));

vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, ...mocks };
});

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/projects']}>
        <Routes>
          <Route path="/projects" element={<ProjectsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ProjectsPage', () => {
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
    mocks.listProjects.mockResolvedValue({
      projects: [
        {
          project_id: 'prj_1',
          root_path: '/projects/red-mansion',
          display_name: '红楼梦',
          cover_cache_key: '',
          output_width: 720,
          output_height: 1280,
          episode_count: 3,
          project_updated_at: '2026-09-09T12:00:00Z',
        },
      ],
    });
    mocks.deleteProject.mockResolvedValue(undefined);
  });

  it('offers permanent project deletion with typed-name confirmation', async () => {
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /(?:Delete project|删除项目) 红楼梦/ }));
    expect(screen.getByRole('heading', { name: /(?:Delete|删除) [“"]红楼梦[”"]\?/ })).toBeInTheDocument();

    const confirmButton = screen.getByRole('button', {
      name: /Permanently delete project|永久删除项目/,
    });
    expect(confirmButton).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/红楼梦.*(?:confirm|确认)/i), {
      target: { value: '红楼梦' },
    });
    fireEvent.click(confirmButton);

    await waitFor(() => expect(mocks.deleteProject).toHaveBeenCalledWith('prj_1', '红楼梦'));
  });

  it('shows the API error code and request ID when deletion fails for another reason', async () => {
    mocks.deleteProject.mockRejectedValue(
      new APIError(404, {
        error: { code: 'not_found', message: 'Project unavailable', request_id: 'req_delete_1' },
      }),
    );
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /(?:Delete project|删除项目) 红楼梦/ }));
    fireEvent.change(screen.getByLabelText(/红楼梦.*(?:confirm|确认)/i), {
      target: { value: '红楼梦' },
    });
    fireEvent.click(screen.getByRole('button', { name: /Permanently delete project|永久删除项目/ }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/not_found/);
    expect(screen.getByRole('alert')).toHaveTextContent(/req_delete_1/);
  });

  it('keeps one create action and provides a recoverable search result state', async () => {
    renderPage();

    expect(await screen.findByRole('heading', { name: '红楼梦' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /New project|新建项目/i })).toHaveLength(1);

    fireEvent.change(screen.getByRole('searchbox', { name: /Search projects|搜索项目/i }), {
      target: { value: '不存在的项目' },
    });
    expect(screen.getByText(/No matching projects|没有找到匹配的项目/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /Clear search|清除搜索/i }));
    expect(screen.getByRole('heading', { name: '红楼梦' })).toBeInTheDocument();
  });
});
