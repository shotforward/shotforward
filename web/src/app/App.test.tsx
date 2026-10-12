import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { requestJSON } from '../api/client';
import { App, queryClient } from './App';

describe('App', () => {
  beforeEach(() => {
    queryClient.clear();
    window.history.replaceState({}, '', '/projects');
    delete window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__;
    vi.restoreAllMocks();
  });

  it('automatically establishes the desktop session without showing a pairing code', async () => {
    window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__ = {
      pairingCode: 'ABC234',
      instanceID: 'instance-desktop',
    };
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    let completeBootstrap: (() => void) | undefined;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes('/api/v1/session')) {
        return Promise.resolve(json({ error: { code: 'authentication_required' } }, 401));
      }
      if (url.includes('/api/v1/bootstrap')) {
        expect(init?.method).toBe('POST');
        expect(JSON.parse(String(init?.body))).toEqual({ pairing_code: 'ABC234' });
        return new Promise<Response>((resolve) => {
          completeBootstrap = () => resolve(json({ csrf_token: 'desktop-csrf-token' }));
        });
      }
      if (url.includes('/api/v1/tasks/snapshot')) {
        return Promise.resolve(json({ stream_epoch: 'epoch', cursor: '', tasks: [] }));
      }
      if (url.includes('/api/v1/projects')) return Promise.resolve(json({ projects: [] }));
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);

    render(
      <StrictMode>
        <App />
      </StrictMode>,
    );

    await waitFor(() => expect(completeBootstrap).toBeTypeOf('function'));
    expect(screen.queryByLabelText(/配对码|Pairing code/i)).not.toBeInTheDocument();
    await act(async () => completeBootstrap?.());
    expect(await screen.findByRole('navigation', { name: /主导航|Primary navigation/i })).toBeInTheDocument();
    expect(screen.queryByLabelText(/配对码|Pairing code/i)).not.toBeInTheDocument();
    expect(window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__).toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/bootstrap',
      expect.objectContaining({ method: 'POST', credentials: 'same-origin' }),
    );
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).includes('/api/v1/bootstrap')),
    ).toHaveLength(1);
  });

  it('asks for a pairing code when no local session exists', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { code: 'authentication_required' } }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );
    render(<App />);
    expect(await screen.findByRole('heading', { name: /ShotForward|连接本机/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/配对码|Pairing code/i)).toBeInTheDocument();
  });

  it('loads authoritative projects after restoring a session', async () => {
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/session'))
        return Promise.resolve(json({ csrf_token: 'csrf-token', instance_id: 'instance' }));
      if (url.includes('/api/v1/tasks/snapshot'))
        return Promise.resolve(json({ stream_epoch: 'epoch', cursor: '', tasks: [] }));
      if (url.includes('/api/v1/projects'))
        return Promise.resolve(
          json({
            projects: [
              {
                project_id: 'prj_1',
                root_path: '/projects/mountain',
                display_name: 'Mountain Story',
                cover_cache_key: '',
                output_width: 720,
                output_height: 1280,
                episode_count: 2,
                project_updated_at: '2026-09-05T12:00:00Z',
              },
            ],
          }),
        );
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);
    expect(await screen.findByText('Mountain Story', {}, { timeout: 5_000 })).toBeInTheDocument();
    expect(document.querySelector('.project-card__cover')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /^项目$|^Projects$/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /工作台|Workspace/i })).not.toBeInTheDocument();
    const sort = screen.getByRole('combobox', { name: /排序项目|Sort projects/i });
    fireEvent.click(sort);
    expect(screen.getByRole('option', { name: /项目名称|Project name/i })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('option', { name: /项目名称|Project name/i }));
    expect(sort).toHaveAttribute('aria-expanded', 'false');
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/api/v1/session'))).toBe(true);
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/api/v1/projects'))).toBe(true);
  });

  it('loads the manual pairing form on demand and opens the studio after submitting its code', async () => {
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/api/v1/session') {
        return Promise.resolve(json({ error: { code: 'authentication_required' } }, 401));
      }
      if (url === '/api/v1/bootstrap') {
        expect(init?.method).toBe('POST');
        expect(JSON.parse(String(init?.body))).toEqual({ pairing_code: 'ABC234' });
        return Promise.resolve(json({ csrf_token: 'manual-csrf-token' }));
      }
      if (url === '/api/v1/tasks/snapshot') {
        return Promise.resolve(json({ stream_epoch: 'epoch', cursor: '', tasks: [] }));
      }
      if (url === '/api/v1/projects') return Promise.resolve(json({ projects: [] }));
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.change(await screen.findByLabelText(/配对码|Pairing code/i), { target: { value: 'ABC234' } });
    fireEvent.click(screen.getByRole('button', { name: /连接工作室|Connect studio/i }));
    expect(await screen.findByRole('navigation', { name: /主导航|Primary navigation/i })).toBeInTheDocument();
    expect(screen.queryByLabelText(/配对码|Pairing code/i)).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/bootstrap')).toHaveLength(1);
  });

  it('returns to pairing when the active daemon session expires', async () => {
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    let sessionExpired = false;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (sessionExpired) {
          return Promise.resolve(json({ error: { code: 'authentication_required' } }, 401));
        }
        if (url.includes('/api/v1/session')) {
          return Promise.resolve(json({ csrf_token: 'csrf-token', instance_id: 'instance' }));
        }
        if (url.includes('/api/v1/tasks/snapshot')) {
          return Promise.resolve(json({ stream_epoch: 'epoch', cursor: '', tasks: [] }));
        }
        if (url.includes('/api/v1/projects')) return Promise.resolve(json({ projects: [] }));
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    render(<App />);
    expect(await screen.findByRole('navigation', { name: /主导航|Primary navigation/i })).toBeInTheDocument();
    sessionExpired = true;
    await act(async () => {
      await expect(requestJSON('/api/v1/projects')).rejects.toMatchObject({ status: 401 });
    });

    expect(await screen.findByLabelText(/配对码|Pairing code/i)).toBeInTheDocument();
  });
});
