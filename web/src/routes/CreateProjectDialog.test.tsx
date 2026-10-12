import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { CreateProjectDialog } from './CreateProjectDialog';

vi.mock('../api/projects', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/projects')>();
  return { ...original, createProject: vi.fn() };
});

function renderDialog() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <CreateProjectDialog open onClose={vi.fn()} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('CreateProjectDialog', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.setAttribute('open', '');
    };
    HTMLDialogElement.prototype.close = function close() {
      this.removeAttribute('open');
    };
  });

  beforeEach(async () => {
    delete window.__SHOTFORWARD_DESKTOP__;
    await i18n.changeLanguage('zh-CN');
  });

  it('uses the native desktop folder picker and creates a dedicated project path', async () => {
    window.__SHOTFORWARD_DESKTOP__ = {
      selectProjectParent: vi.fn().mockResolvedValue('C:\\Users\\creator\\Videos'),
      openExternal: vi.fn(),
    };
    renderDialog();

    fireEvent.click(screen.getByRole('button', { name: '选择文件夹' }));
    await waitFor(() => expect(window.__SHOTFORWARD_DESKTOP__?.selectProjectParent).toHaveBeenCalledOnce());
    fireEvent.change(screen.getByLabelText('项目名称'), { target: { value: '山川:序章' } });

    await waitFor(() =>
      expect(screen.getByLabelText(/^保存位置/)).toHaveValue('C:\\Users\\creator\\Videos\\山川-序章'),
    );
  });

  it('shows only models compatible with the selected project resolution', () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText('项目名称'), { target: { value: '分辨率测试' } });
    fireEvent.change(screen.getByLabelText(/^保存位置/), { target: { value: '/tmp/resolution-test' } });
    fireEvent.click(screen.getByRole('button', { name: '下一步' }));

    expect(screen.getByText('Seedance 2.5')).toBeInTheDocument();
    expect(screen.getByText('Wan 3.0 Standard / Prime')).toBeInTheDocument();
    expect(screen.getByText('MiniMax H3')).toBeInTheDocument();
    expect(screen.getByText(/480P \/ 720P 项目生成 768P.*1080P 项目生成 2K/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('radio', { name: '1080p' }));

    expect(screen.queryByText('Seedance 2.5')).not.toBeInTheDocument();
    expect(screen.getByText('Wan 3.0 Standard / Prime')).toBeInTheDocument();
    expect(screen.getByText('MiniMax H3')).toBeInTheDocument();
    expect(screen.getByText('2K → 1080P')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('radio', { name: '480p' }));

    expect(screen.getByText('Seedance 2.5')).toBeInTheDocument();
    expect(screen.getByText('Wan 3.0 Standard / Prime')).toBeInTheDocument();
    expect(screen.getByText('MiniMax H3')).toBeInTheDocument();
    expect(screen.getByText('768P → 480P')).toBeInTheDocument();
  });
});
