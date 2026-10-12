import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { AgentServiceSettingsPage } from './AgentServiceSettingsPage';

describe('Agent service settings', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('shows one selected endpoint and switches it through the daemon preference', async () => {
    const requests: Array<{ method: string; body: unknown }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/api/v1/agent/runtime')) {
          const method = init?.method ?? 'GET';
          if (method === 'PUT') {
            requests.push({ method, body: JSON.parse(String(init?.body)) });
            return json(runtimeResponseFor('user_codex', 'user_codex'));
          }
          const target = url.includes('runtime_kind=user_codex') ? 'user_codex' : 'shotforward';
          return json(runtimeResponseFor(target, 'shotforward'));
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage();
    const cloud = await screen.findByRole('radio', { name: /ShotForward Agent/i });
    const local = screen.getByRole('radio', { name: /本地 Codex|Local Codex/i });
    await waitFor(() => expect(cloud).toHaveAttribute('aria-checked', 'true'));
    expect(local).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByText(/切换后仅显示所选服务的对话记录|only conversations/i)).toBeInTheDocument();

    fireEvent.click(local);
    await waitFor(() => expect(local).toHaveAttribute('aria-checked', 'true'));
    expect(requests).toEqual([{ method: 'PUT', body: { runtime_kind: 'user_codex' } }]);
  });

  it('renders both services immediately while their independent model requests finish', async () => {
    let resolveCloud!: (response: Response) => void;
    const cloudResponse = new Promise<Response>((resolve) => {
      resolveCloud = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('runtime_kind=shotforward')) return cloudResponse;
        if (url.includes('runtime_kind=user_codex'))
          return json(runtimeResponseFor('user_codex', 'shotforward'));
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage();
    expect(screen.getByRole('radio', { name: /ShotForward Agent/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /本地 Codex|Local Codex/i })).toBeInTheDocument();
    expect(screen.getAllByText(/正在加载此服务|Loading this service/i).length).toBeGreaterThan(0);
    expect(await screen.findByText('Local')).toBeInTheDocument();

    resolveCloud(jsonResponse(runtimeResponseFor('shotforward', 'shotforward')));
    expect(await screen.findByText('Cloud')).toBeInTheDocument();
  });

  it('bypasses both model caches when the user explicitly refreshes', async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        urls.push(url);
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/api/v1/agent/runtime')) {
          const target = url.includes('runtime_kind=user_codex') ? 'user_codex' : 'shotforward';
          return json(runtimeResponseFor(target, 'shotforward'));
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage();
    const refresh = await screen.findByRole('button', { name: /^(刷新模型|Refresh models)$/i });
    urls.length = 0;
    fireEvent.click(refresh);

    await waitFor(() => {
      expect(urls).toEqual(
        expect.arrayContaining([
          expect.stringContaining('runtime_kind=shotforward&refresh=true'),
          expect.stringContaining('runtime_kind=user_codex&refresh=true'),
          expect.stringContaining('/api/v1/cloud/account'),
        ]),
      );
    });
  });
});

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AgentServiceSettingsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function runtimeResponse(active: 'shotforward' | 'user_codex') {
  const snapshots = {
    shotforward: {
      health: 'ready',
      version: 'cloud',
      minimum_version: '',
      failure_code: '',
      failure_summary: '',
    },
    user_codex: {
      health: 'ready',
      version: '0.200.0',
      minimum_version: '',
      failure_code: '',
      failure_summary: '',
    },
  } as const;
  return {
    active_runtime_kind: active,
    runtime: snapshots[active],
    runtimes: snapshots,
    runtime_models: {
      shotforward: [{ id: 'gpt-cloud', display_name: 'Cloud', runtime_kind: 'shotforward' }],
      user_codex: [{ id: 'gpt-local', display_name: 'Local', runtime_kind: 'user_codex' }],
    },
    models:
      active === 'shotforward'
        ? [{ id: 'gpt-cloud', display_name: 'Cloud', runtime_kind: 'shotforward' }]
        : [{ id: 'gpt-local', display_name: 'Local', runtime_kind: 'user_codex' }],
  };
}

function json(value: unknown) {
  return Promise.resolve(jsonResponse(value));
}

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function runtimeResponseFor(target: 'shotforward' | 'user_codex', active: 'shotforward' | 'user_codex') {
  const response = runtimeResponse(active);
  return {
    ...response,
    target_runtime_kind: target,
    runtime: response.runtimes[target],
    models: response.runtime_models[target],
    runtimes: { [target]: response.runtimes[target] },
    runtime_models: { [target]: response.runtime_models[target] },
  };
}
