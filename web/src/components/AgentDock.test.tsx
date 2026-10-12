import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useState } from 'react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { AgentDock, type AgentDraftRequest } from './AgentDock';
import { prepareAgentSession, sessionPreparationKey } from '../agentSessionPreparation';
import { APIError } from '../api/client';

const desktopMocks = vi.hoisted(() => ({ saveDownload: vi.fn() }));
vi.mock('../desktop', async () => ({
  ...(await vi.importActual<typeof import('../desktop')>('../desktop')),
  saveDownload: desktopMocks.saveDownload,
}));

class FakeEventSource {
  static latest: FakeEventSource | undefined;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Set<(event: Event) => void>>();

  constructor(public readonly url: string) {
    FakeEventSource.latest = this;
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const callback = listener as (event: Event) => void;
    const values = this.listeners.get(type) ?? new Set();
    values.add(callback);
    this.listeners.set(type, values);
  }

  removeEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.get(type)?.delete(listener as (event: Event) => void);
  }

  close() {}

  emit(type: string, value: unknown) {
    const event = new MessageEvent('message', { data: JSON.stringify(value) });
    this.listeners.get(type)?.forEach((listener) => listener(event));
  }
}

describe('AgentDock conversation history', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1440 });
    Object.defineProperty(HTMLDialogElement.prototype, 'showModal', {
      configurable: true,
      value() {
        this.setAttribute('open', '');
      },
    });
    Object.defineProperty(HTMLDialogElement.prototype, 'close', {
      configurable: true,
      value() {
        this.removeAttribute('open');
      },
    });
    FakeEventSource.latest = undefined;
    desktopMocks.saveDownload.mockReset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  it('replaces the streamed reply with canonical history when a turn completes', async () => {
    let messageReads = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn(messageReads ? 'completed' : 'running')],
            change_sets: [],
            cursor: messageReads ? 'epoch:2' : 'epoch:1',
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/messages')) {
          messageReads += 1;
          return json({
            turns: [
              {
                turn: turn(messageReads > 1 ? 'completed' : 'running'),
                messages:
                  messageReads > 1
                    ? [
                        { id: 'message-user', role: 'user', text: 'Previous question' },
                        {
                          id: 'message-agent',
                          role: 'assistant',
                          phase: 'final_answer',
                          text: 'Final reply',
                        },
                      ]
                    : [{ id: 'message-user', role: 'user', text: 'Previous question' }],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByText('Previous question')).toBeInTheDocument();
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());

    act(() => {
      FakeEventSource.latest!.emit('assistant.delta', {
        type: 'assistant.delta',
        session_id: 'ags_current',
        turn_id: 'agt_current',
        text_delta: 'Live reply',
      });
    });
    expect(await screen.findByText('Live reply')).toBeInTheDocument();

    act(() => {
      FakeEventSource.latest!.emit('turn.completed', {
        type: 'turn.completed',
        session_id: 'ags_current',
        turn_id: 'agt_current',
        turn_status: 'completed',
      });
    });
    expect(await screen.findByText('Final reply')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Live reply')).not.toBeInTheDocument());
  });

  it('shows only models returned for the globally selected Agent service', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            active_runtime_kind: 'user_codex',
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-local', display_name: 'Local model', runtime_kind: 'user_codex' }],
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: false });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const model = await screen.findByRole('combobox', {
      name: /后续对话使用的模型|Model for following turns/i,
    });
    fireEvent.click(model);

    expect(screen.getByRole('option', { name: 'Local model' })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'Cloud model' })).not.toBeInTheDocument();
    expect(screen.queryByText('shotforward/gpt-cloud')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Local Codex' })).toHaveAttribute('href', '/settings/agent');
  });

  it('explains Codex network failures instead of silently ignoring session creation', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-local', display_name: 'Local model', runtime_kind: 'user_codex' }],
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: false });
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: {
                  code: 'runtime_unavailable',
                  message: 'Agent Runtime is unavailable',
                  retryable: true,
                  request_id: 'req_debug_1',
                },
              }),
              { status: 503, headers: { 'Content-Type': 'application/json' } },
            ),
          );
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    await waitFor(() => expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /无法新建 Codex 会话|Could not create a Codex session/,
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/本机 VPN|local VPN/);
    expect(screen.getByRole('alert')).toHaveTextContent(/req_debug_1/);
    expect(
      screen.getByRole('link', { name: /打开 Codex Runtime 设置|Open Codex Runtime settings/ }),
    ).toHaveAttribute('href', '/settings/runtime');
  });

  it('opens a preparing composer immediately, deduplicates double clicks, keeps the draft, and sends only to the ready new session', async () => {
    let resolve!: (response: Response) => void;
    let creations = 0;
    let historyReads = 0;
    const submitted: string[] = [];
    const request = new Promise<Response>((done) => {
      resolve = done;
    });
    const fresh = { ...session('active'), id: 'ags_new', title: 'Fresh conversation' };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({
            active_runtime_kind: 'user_codex',
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
          });
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          creations += 1;
          return request;
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.includes('/snapshot'))
          return json({
            session: url.includes('ags_new') ? fresh : session('active'),
            turns: [],
            change_sets: [],
            cursor: 'real:cursor',
          });
        if (url.includes('/messages')) {
          if (url.includes('ags_new')) historyReads += 1;
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/inputs')) {
          submitted.push(url);
          return json({ action: 'queue_turn', turn: { ...turn('queued'), agent_session_id: 'ags_new' } });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const { client } = renderDock();
    await screen.findByRole('textbox');
    await waitFor(() => expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled());
    const create = screen.getByRole('button', { name: /新建会话|New session/i });
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Keep this draft' } });
    fireEvent.click(create);
    fireEvent.click(create);
    expect(await screen.findByText(/正在准备新会话|Preparing your new conversation/)).toBeInTheDocument();
    expect(screen.getByRole('textbox')).toHaveValue('Keep this draft');
    expect(screen.getByRole('button', { name: /发送|Send/i })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));
    expect(submitted).toHaveLength(0);
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Draft while preparing' } });
    await act(async () => {
      resolve(await json({ session: fresh }));
    });
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i })).toHaveTextContent(
        'Fresh conversation',
      ),
    );
    expect(creations).toBe(1);
    expect(screen.getByRole('textbox')).toHaveValue('Draft while preparing');
    expect(historyReads).toBe(0);
    expect(submitted).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));
    await waitFor(() =>
      expect(submitted).toEqual([expect.stringContaining('/agent-sessions/ags_new/inputs')]),
    );
    await waitFor(() => expect(historyReads).toBeGreaterThan(0));
    client.clear();
  });

  it('joins an episode background preparation and shows a persisted background failure with an editable draft', async () => {
    let reject!: (error: Error) => void;
    let creations = 0;
    const request = new Promise<Response>((_done, fail) => {
      reject = fail;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({
            active_runtime_kind: 'user_codex',
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
          });
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          creations += 1;
          return request;
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const background = prepareAgentSession(client, 'prj_1', 'ep_1', 'gpt-test', 'user_codex').catch(() => {});
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AgentDock projectID="prj_1" episodeID="ep_1" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(/正在准备新会话|Preparing your new conversation/)).toBeInTheDocument();
    const create = await screen.findByRole('button', { name: /新建会话|New session/i });
    expect(create).toBeDisabled();
    fireEvent.click(create);
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Try after recovery' } });
    await act(async () => {
      reject(
        new APIError(503, {
          error: {
            code: 'runtime_unavailable',
            message: 'Unavailable',
            retryable: true,
            request_id: 'req_background',
          },
        }),
      );
      await background;
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('req_background');
    expect(screen.getByRole('textbox')).toHaveValue('Try after recovery');
    expect(screen.getByRole('button', { name: /发送|Send/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /重试|Retry/i })).toBeEnabled();
    expect(creations).toBe(1);
    client.clear();
  });

  it('does not select a late prepared session or retain another episode draft after switching scope', async () => {
    let resolve!: (response: Response) => void;
    const request = new Promise<Response>((done) => {
      resolve = done;
    });
    const late = { ...session('active'), id: 'ags_late', title: 'Late old episode' };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({
            active_runtime_kind: 'user_codex',
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
          });
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') return request;
        if (url.includes('/agent-sessions')) return json({ sessions: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    function ScopeHarness() {
      const [episode, setEpisode] = useState('ep_1');
      return (
        <>
          <button onClick={() => setEpisode('ep_2')}>Other episode</button>
          <AgentDock projectID="prj_1" episodeID={episode} />
        </>
      );
    }
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <ScopeHarness />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));
    fireEvent.change(await screen.findByRole('textbox'), { target: { value: 'Old episode draft' } });
    fireEvent.click(screen.getByRole('button', { name: 'Other episode' }));
    await act(async () => {
      resolve(await json({ session: late }));
    });
    await waitFor(() =>
      expect(client.getQueryData(sessionPreparationKey('prj_1', 'ep_1', 'user_codex'))).toMatchObject({
        status: 'ready',
      }),
    );
    expect(screen.queryByText('Late old episode')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue('Old episode draft')).not.toBeInTheDocument();
    expect(client.getQueryData(['agent-sessions', 'prj_1', 'ep_2'])).toEqual({ sessions: [] });
    expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled();
    client.clear();
  });

  it('keeps a late local preparation out of the newly selected cloud service conversation', async () => {
    let resolveLocal!: (response: Response) => void;
    const localRequest = new Promise<Response>((done) => {
      resolveLocal = done;
    });
    const local = { ...session('active'), id: 'ags_late_local', title: 'Late local' };
    const cloud = {
      ...session('active'),
      id: 'ags_new_cloud',
      runtime_kind: 'shotforward',
      model_id: 'gpt-cloud',
      title: 'New cloud',
    };
    const creations: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({
            active_runtime_kind: 'user_codex',
            runtime: { health: 'ready' },
            models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
          });
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          const kind = JSON.parse(String(init.body)).runtime_kind as string;
          creations.push(kind);
          return kind === 'user_codex' ? localRequest : json({ session: cloud });
        }
        if (url.includes('/snapshot'))
          return json({ session: cloud, turns: [], change_sets: [], cursor: 'cloud:1' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const { client } = renderDock();
    await waitFor(() => expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));
    expect(await screen.findByText(/正在准备新会话|Preparing your new conversation/)).toBeInTheDocument();
    await act(async () => {
      client.setQueryData(['agent-runtime'], {
        active_runtime_kind: 'shotforward',
        runtime: { health: 'ready' },
        models: [{ id: 'gpt-cloud', display_name: 'Cloud', runtime_kind: 'shotforward' }],
      });
    });
    await waitFor(() => expect(screen.getByRole('button', { name: /新建会话|New session/i })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i })).toHaveTextContent(
        'New cloud',
      ),
    );
    await act(async () => {
      resolveLocal(await json({ session: local }));
    });
    await waitFor(() =>
      expect(client.getQueryData(sessionPreparationKey('prj_1', 'ep_1', 'user_codex'))).toMatchObject({
        status: 'ready',
      }),
    );
    expect(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i })).toHaveTextContent(
      'New cloud',
    );
    expect(screen.queryByText('Late local')).not.toBeInTheDocument();
    expect(creations).toEqual(['user_codex', 'shotforward']);
    client.clear();
  });

  it.each(['incompatible', 'not_installed', 'login_required', 'disconnected'])(
    'disables new sessions and offers setup when Codex is %s',
    async (health) => {
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL) => {
          const url = String(input);
          if (url.includes('/api/v1/agent/runtime')) {
            return json({
              runtime: {
                health,
                version: '0.146.0',
                minimum_version: '0.155.1',
                failure_code: 'codex_upgrade_required',
                failure_summary: 'Update local Codex',
              },
              models: [],
            });
          }
          if (url.includes('/api/v1/cloud/account')) return json({ signed_in: false });
          if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [] });
          return Promise.reject(new Error(`Unexpected request: ${url}`));
        }),
      );

      renderDock();

      const alert = await screen.findByRole('status');
      await waitFor(() => expect(alert).toHaveTextContent(/本机 Codex 不可用|Local Codex is unavailable/));
      expect(within(alert).getByRole('link', { name: /设置本机 Codex|Set up local Codex/ })).toHaveAttribute(
        'href',
        '/settings/agent',
      );
      expect(within(alert).getByRole('link', { name: /绑定 ShotForward|Bind ShotForward/ })).toHaveAttribute(
        'href',
        '/settings/account',
      );
      for (const button of screen.getAllByRole('button', { name: /新建会话|New session/i }))
        expect(button).toBeDisabled();
    },
  );

  it('replaces Send with Stop while a Turn is live and interrupts that Turn', async () => {
    let interrupted = false;
    let interruptRequest: { url: string; method?: string; body?: string } | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/turns/agt_current:interrupt')) {
          interruptRequest = { url, method: init?.method ?? 'GET', body: String(init?.body) };
          interrupted = true;
          return json({ turn: turn('interrupted') });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn(interrupted ? 'interrupted' : 'running')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn(interrupted ? 'interrupted' : 'running'),
                messages: [{ id: 'message-agent', role: 'assistant', text: 'Working reply' }],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByText('Working reply')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /发送|Send/i })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /停止|Stop/i }));

    await waitFor(() =>
      expect(interruptRequest).toEqual({
        url: '/api/v1/agent-sessions/ags_current/turns/agt_current:interrupt',
        method: 'POST',
        body: '{}',
      }),
    );
    expect(await screen.findByText(/已停止|Stopped/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /发送|Send/i })).toBeDisabled();
  });

  it('keeps a repeated optimistic message visible and omits the user label', async () => {
    let resolveSubmission!: (response: Response) => void;
    const submission = new Promise<Response>((resolve) => {
      resolveSubmission = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn('completed'),
                messages: [{ id: 'message-old-user', role: 'user', text: 'Repeated request' }],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/agent-sessions/ags_current/inputs')) return submission;
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByText('Repeated request')).toBeInTheDocument();
    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: 'Repeated request' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() => expect(screen.getAllByText('Repeated request')).toHaveLength(2));
    expect(screen.getByText(/正在连接运行时|Connecting to runtime/i)).toBeInTheDocument();
    expect(screen.queryByText(/等待执行|Queued/i)).not.toBeInTheDocument();
    expect(document.querySelectorAll('.conversation-message--user > span')).toHaveLength(0);

    resolveSubmission(
      new Response(JSON.stringify({ action: 'queue_turn', turn: turn('running') }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });

  it('replaces an optimistic greeting with one canonical user message and keeps it clean after reload', async () => {
    let submitted: Record<string, unknown> | undefined;
    let resolveSubmission!: (response: Response) => void;
    let completed = false;
    let messageReads = 0;
    const submission = new Promise<Response>((resolve) => {
      resolveSubmission = resolve;
    });
    const videoPromptTarget = {
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'MiniMax-H3-Turbo-Ref2VA',
      keyframe_provider_model_id: 'MiniMax-H3-Turbo-FL2VA',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: completed ? [turn('completed')] : [],
            change_sets: [],
            cursor: completed ? 'epoch:2' : 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          messageReads += 1;
          return json({
            turns: completed
              ? [
                  {
                    turn: turn('completed'),
                    messages: [
                      { id: 'message-user', role: 'user', text: 'Hi' },
                      { id: 'message-agent', role: 'assistant', phase: 'final_answer', text: '你好！' },
                    ],
                  },
                ]
              : [],
            next_cursor: '',
          });
        }
        if (url.includes('/agent-sessions/ags_current/inputs') && init?.method === 'POST') {
          submitted = JSON.parse(String(init.body)) as Record<string, unknown>;
          return submission;
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.includes('/episodes/ep_1/assets')) return json({ assets: [] });
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: false });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    const firstView = renderDock(undefined, undefined, videoPromptTarget);
    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: 'Hi' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));
    await waitFor(() => expect(submitted?.text).toBe('Hi'));
    expect(submitted?.video_prompt_target).toEqual(videoPromptTarget);
    expect(screen.getAllByText('Hi')).toHaveLength(1);
    expect(document.querySelector('.conversation-message--user.is-pending')).not.toBeNull();

    completed = true;
    resolveSubmission(
      new Response(JSON.stringify({ action: 'queue_turn', turn: turn('completed') }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    expect(await screen.findByText('你好！')).toBeInTheDocument();
    expect(screen.getAllByText('Hi')).toHaveLength(1);
    expect(document.querySelector('.conversation-message--user.is-pending')).toBeNull();
    expect(screen.queryByText(/ShotForward-selected video Prompt context/)).not.toBeInTheDocument();

    const readsBeforeReload = messageReads;
    firstView.unmount();
    firstView.client.clear();
    const reloadedView = renderDock(undefined, undefined, videoPromptTarget);
    expect(await screen.findByText('你好！')).toBeInTheDocument();
    expect(messageReads).toBeGreaterThan(readsBeforeReload);
    expect(screen.getAllByText('Hi')).toHaveLength(1);
    expect(document.querySelectorAll('.conversation-message--user')).toHaveLength(1);
    expect(screen.queryByText(/ShotForward-selected video Prompt context/)).not.toBeInTheDocument();
    reloadedView.unmount();
    reloadedView.client.clear();
  });

  it.each([true, false])(
    'keeps a repeated steer visible after POST and deduplicates its canonical message (operation IDs: %s)',
    async (withOperationIDs) => {
      let submitted: Record<string, unknown> | undefined;
      let persisted = false;
      let reads = 0;
      let resolveSubmission!: (response: Response) => void;
      const submission = new Promise<Response>((resolve) => {
        resolveSubmission = resolve;
      });
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
          const url = String(input);
          if (url.includes('/api/v1/agent/runtime'))
            return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
          if (url.includes('/snapshot'))
            return json({
              session: session('active'),
              turns: [turn('running')],
              change_sets: [],
              cursor: 'epoch:1',
            });
          if (url.includes('/messages')) {
            reads += 1;
            return json({
              turns: [
                {
                  turn: turn('running'),
                  messages: [
                    {
                      id: 'old-user',
                      role: 'user',
                      text: 'Keep the dialogue',
                      ...(withOperationIDs ? { client_operation_id: 'old-operation' } : {}),
                    },
                    { id: 'old-reply', role: 'assistant', text: 'Known reply before the steer' },
                    ...(persisted
                      ? [
                          {
                            id: 'new-user',
                            role: 'user',
                            text: 'Keep the dialogue',
                            ...(withOperationIDs
                              ? { client_operation_id: submitted?.client_operation_id }
                              : {}),
                          },
                        ]
                      : []),
                  ],
                },
              ],
              next_cursor: '',
            });
          }
          if (url.includes('/inputs')) {
            submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
            return submission;
          }
          if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
          return Promise.reject(new Error(`Unexpected request: ${url}`));
        }),
      );
      const view = renderDock();
      await screen.findByText('Known reply before the steer');
      const composer = screen.getByRole('textbox');
      fireEvent.change(composer, { target: { value: 'Keep the dialogue' } });
      fireEvent.submit(composer.closest('form')!);
      await waitFor(() => expect(submitted?.client_operation_id).toEqual(expect.any(String)));
      expect(screen.getAllByText('Keep the dialogue')).toHaveLength(2);
      const readsBeforeResponse = reads;
      await act(async () => resolveSubmission(await json({ action: 'steer_turn', turn: turn('running') })));
      await waitFor(() => expect(reads).toBeGreaterThan(readsBeforeResponse));
      expect(screen.getAllByText('Keep the dialogue')).toHaveLength(2);
      const messages = [...document.querySelectorAll('.conversation-message')].map(
        (node) => node.textContent,
      );
      expect(messages).toEqual([
        'Keep the dialogue',
        'AgentKnown reply before the steer',
        'Keep the dialogue',
      ]);
      persisted = true;
      await act(() => view.client.invalidateQueries({ queryKey: ['agent-conversation', 'ags_current'] }));
      expect(screen.getAllByText('Keep the dialogue')).toHaveLength(2);
      expect(document.querySelectorAll('.conversation-message--user')).toHaveLength(2);
      view.unmount();
      view.client.clear();
    },
  );

  it('keeps the first queued input before a reply while Runtime history still omits the input', async () => {
    let submitted = false;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: submitted ? [turn('running')] : [],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages'))
          return json({
            turns: submitted
              ? [
                  {
                    turn: turn('running'),
                    messages: [
                      { id: 'reply', role: 'assistant', text: 'Reply without Runtime user history' },
                    ],
                  },
                ]
              : [],
            next_cursor: '',
          });
        if (url.includes('/inputs')) {
          submitted = true;
          return json({ action: 'queue_turn', turn: turn('running') });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const view = renderDock();
    const composer = await screen.findByRole('textbox');
    fireEvent.change(composer, { target: { value: 'Identify the assets' } });
    fireEvent.submit(composer.closest('form')!);
    await screen.findByText('Reply without Runtime user history');
    expect([...document.querySelectorAll('.conversation-message')].map((node) => node.textContent)).toEqual([
      'Identify the assets',
      'AgentReply without Runtime user history',
    ]);
    view.unmount();
    view.client.clear();
  });

  it('restores durable user input after remount even when the reply only exists in SSE', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: [turn('running')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages'))
          return json({
            turns: [
              {
                turn: turn('running'),
                messages: [
                  {
                    id: 'persisted-user',
                    role: 'user',
                    client_operation_id: 'persisted-operation',
                    text: 'Identify the current episode assets',
                  },
                ],
              },
            ],
            next_cursor: '',
          });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const first = renderDock();
    await screen.findByText('Identify the current episode assets');
    first.unmount();
    first.client.clear();
    const second = renderDock();
    await screen.findByText('Identify the current episode assets');
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    act(() =>
      FakeEventSource.latest!.emit('assistant.delta', {
        type: 'assistant.delta',
        session_id: 'ags_current',
        turn_id: 'agt_current',
        text_delta: 'Reading the historical character settings',
      }),
    );
    await screen.findByText('Reading the historical character settings');
    expect(screen.getAllByText('Identify the current episode assets')).toHaveLength(1);
    expect([...document.querySelectorAll('.conversation-message')].map((node) => node.textContent)).toEqual([
      'Identify the current episode assets',
      'AgentReading the historical character settings',
    ]);
    second.unmount();
    second.client.clear();
  });

  it('merges an older full Runtime Turn with a first-page user-only fallback without losing the reply', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages')) {
          const older = url.includes('cursor=older');
          return json({
            turns: [
              {
                turn: turn(older ? 'running' : 'completed'),
                messages: [
                  {
                    id: older ? 'runtime-input' : 'local-input',
                    role: 'user',
                    client_operation_id: 'durable-operation',
                    text: older ? 'Internal context followed by the original text' : 'Original user text',
                  },
                  ...(older
                    ? [{ id: 'runtime-reply', role: 'assistant', text: 'Previously persisted reply' }]
                    : []),
                ],
              },
            ],
            next_cursor: older ? '' : 'older',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const view = renderDock();
    await screen.findByText('Original user text');
    fireEvent.click(screen.getByRole('button', { name: /加载更早|Load earlier/i }));
    await screen.findByText('Previously persisted reply');
    expect(screen.getAllByText('Original user text')).toHaveLength(1);
    expect(screen.queryByText('Internal context followed by the original text')).not.toBeInTheDocument();
    expect(document.querySelectorAll('.conversation-message--user')).toHaveLength(1);
    expect(screen.getByText(/已完成|Completed/i)).toBeInTheDocument();
    view.unmount();
    view.client.clear();
  });

  it.each([
    {
      name: 'keeps replies around steers when the newest page catches up',
      older: [
        { id: 'old-q', role: 'user', client_operation_id: 'op-q', text: 'Initial queue' },
        { id: 'old-steer', role: 'user', client_operation_id: 'op-steer', text: 'Later steer' },
      ],
      current: [
        { id: 'q', role: 'user', client_operation_id: 'op-q', text: 'Initial queue' },
        { id: 'reply1', role: 'assistant', text: 'Reply before steer' },
        { id: 'steer', role: 'user', client_operation_id: 'op-steer', text: 'Later steer' },
        { id: 'reply2', role: 'assistant', text: 'Reply after steer' },
      ],
      expected: ['Initial queue', 'AgentReply before steer', 'Later steer', 'AgentReply after steer'],
    },
    {
      name: 'keeps a newest queue before older assistant-only history',
      older: [{ id: 'reply1', role: 'assistant', text: 'Reply before steer' }],
      current: [{ id: 'q', role: 'user', client_operation_id: 'op-q', text: 'Initial queue' }],
      expected: ['Initial queue', 'AgentReply before steer'],
    },
    {
      name: 'inserts older-only replies before the next shared steer anchor',
      older: [
        { id: 'q', role: 'user', text: 'Initial queue' },
        { id: 'reply1', role: 'assistant', text: 'Reply before steer' },
        { id: 'steer', role: 'user', text: 'Later steer' },
        { id: 'reply2', role: 'assistant', text: 'Reply after steer' },
      ],
      current: [
        { id: 'q', role: 'user', text: 'Initial queue' },
        { id: 'steer', role: 'user', text: 'Later steer' },
      ],
      expected: ['Initial queue', 'AgentReply before steer', 'Later steer', 'AgentReply after steer'],
    },
  ])('$name', async ({ older, current, expected }) => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages')) {
          const earlier = url.includes('cursor=older');
          return json({
            turns: [{ turn: turn(earlier ? 'running' : 'completed'), messages: earlier ? older : current }],
            next_cursor: earlier ? '' : 'older',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const view = renderDock();
    await screen.findByText('Initial queue');
    fireEvent.click(screen.getByRole('button', { name: /加载更早|Load earlier/i }));
    await waitFor(() =>
      expect([...document.querySelectorAll('.conversation-message')].map((node) => node.textContent)).toEqual(
        expected,
      ),
    );
    expect(screen.getByText(/已完成|Completed/i)).toBeInTheDocument();
    view.unmount();
    view.client.clear();
  });

  it('places a pending steer after the current SSE-only reply', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: [turn('running')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages'))
          return json({
            turns: [
              {
                turn: turn('running'),
                messages: [{ id: 'first-user', role: 'user', text: 'The original request' }],
              },
            ],
            next_cursor: '',
          });
        if (url.includes('/inputs')) return new Promise<Response>(() => {});
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const view = renderDock();
    await screen.findByText('The original request');
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    act(() =>
      FakeEventSource.latest!.emit('assistant.delta', {
        type: 'assistant.delta',
        session_id: 'ags_current',
        turn_id: 'agt_current',
        text_delta: 'Current streamed reply',
      }),
    );
    await screen.findByText('Current streamed reply');
    const composer = screen.getByRole('textbox');
    fireEvent.change(composer, { target: { value: 'An instruction after the reply' } });
    fireEvent.submit(composer.closest('form')!);
    await screen.findByText('An instruction after the reply');
    expect([...document.querySelectorAll('.conversation-message')].map((node) => node.textContent)).toEqual([
      'The original request',
      'AgentCurrent streamed reply',
      'An instruction after the reply',
    ]);
    view.unmount();
    view.client.clear();
  });

  it('uses the actual queue action if a running Turn completed during submission', async () => {
    let submitted = false;
    const newTurn = { ...turn('running'), id: 'agt_next', created_at: '2026-09-06T00:00:03Z' };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/snapshot'))
          return json({
            session: session('active'),
            turns: [turn(submitted ? 'completed' : 'running'), ...(submitted ? [newTurn] : [])],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/messages'))
          return json({
            turns: [
              {
                turn: turn(submitted ? 'completed' : 'running'),
                messages: [{ id: 'old-reply', role: 'assistant', text: 'Old Turn reply' }],
              },
              ...(submitted
                ? [
                    {
                      turn: newTurn,
                      messages: [{ id: 'new-reply', role: 'assistant', text: 'New Turn reply' }],
                    },
                  ]
                : []),
            ],
            next_cursor: '',
          });
        if (url.includes('/inputs')) {
          submitted = true;
          return json({ action: 'queue_turn', turn: newTurn });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const view = renderDock();
    await screen.findByText('Old Turn reply');
    const composer = screen.getByRole('textbox');
    fireEvent.change(composer, { target: { value: 'Next request' } });
    fireEvent.submit(composer.closest('form')!);
    await screen.findByText('New Turn reply');
    expect([...document.querySelectorAll('.conversation-message')].map((node) => node.textContent)).toEqual([
      'AgentOld Turn reply',
      'Next request',
      'AgentNew Turn reply',
    ]);
    view.unmount();
    view.client.clear();
  });

  it.each([
    {
      code: 'runtime_session_unavailable',
      message: /ShotForward runtime session is unavailable/,
      recharge: false,
    },
    { code: 'shotforward_negative_balance', message: /余额不足|balance is insufficient/, recharge: true },
    { code: 'shotforward_wallet_not_found', message: /钱包暂不可用|wallet is unavailable/, recharge: false },
  ])(
    'replaces the optimistic queue state with the persisted terminal failure $code',
    async ({ code, message, recharge }) => {
      let submitted = false;
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL) => {
          const url = String(input);
          if (url.includes('/api/v1/agent/runtime')) {
            return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
          }
          if (url.includes('/agent-sessions/ags_current/snapshot')) {
            return json({
              session: session('active'),
              turns: submitted
                ? [
                    {
                      ...turn('failed'),
                      failure: {
                        code,
                        summary: 'ShotForward runtime session is unavailable',
                      },
                    },
                  ]
                : [],
              change_sets: [],
              cursor: submitted ? 'epoch:2' : 'epoch:1',
            });
          }
          if (url.includes('/agent-sessions/ags_current/messages')) {
            return json({ turns: [], next_cursor: '' });
          }
          if (url.includes('/agent-sessions/ags_current/inputs')) {
            submitted = true;
            return json({ action: 'queue_turn', turn: turn('queued') });
          }
          if (url.includes('/episodes/ep_1/agent-sessions')) {
            return json({ sessions: [session('active')] });
          }
          return Promise.reject(new Error(`Unexpected request: ${url}`));
        }),
      );

      renderDock();
      const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
      fireEvent.change(composer, { target: { value: 'Continue the failed task' } });
      fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

      expect(await screen.findByText(/执行失败|Failed/i)).toBeInTheDocument();
      expect(screen.getByText(message)).toBeInTheDocument();
      expect(Boolean(screen.queryByRole('button', { name: /去充值|Recharge/ }))).toBe(recharge);
      expect(screen.queryByText(/等待执行|Queued/i)).not.toBeInTheDocument();
    },
  );

  it('places a linked prompt request in the composer and focuses it', async () => {
    stubActiveSession();
    const onDraftApplied = vi.fn();
    renderDock({ id: 1, text: 'Write the first_image_prompt for clip_1.' }, onDraftApplied);

    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    expect(composer).toHaveValue('Write the first_image_prompt for clip_1.');
    await waitFor(() => expect(composer).toHaveFocus());
    expect(onDraftApplied).toHaveBeenCalledWith(1);
  });

  it('submits the currently selected project object as Turn focus', async () => {
    let submitted: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/generation-providers')) {
          return json({
            providers: [
              {
                generator_id: 'video.seedance-2.5.volcengine-cn',
                model_id: 'seedance-2-5-pro',
                enabled: true,
                health: 'ready',
              },
            ],
          });
        }
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: turn('queued') });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <div className="creative-layout">
            <AgentDock
              projectID="prj_1"
              episodeID="ep_1"
              context="assets"
              focus={{ asset_id: 'ast_selected' }}
            />
          </div>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    await waitFor(() => expect(client.getQueryData(['generation-providers'])).toBeDefined());
    fireEvent.change(composer, { target: { value: 'Improve this asset' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() => expect(submitted?.focus).toEqual({ asset_id: 'ast_selected' }));
    expect(submitted?.text).toBe('Improve this asset');
    expect(submitted?.video_prompt_target).toEqual({
      generator_id: 'video.seedance-2.5.volcengine-cn',
      provider_model_id: 'seedance-2-5-pro',
      keyframe_provider_model_id: 'seedance-2-5-pro',
    });
  });

  it('freezes running Turn context but uses the current selection for new and queued Turns', async () => {
    const submitted: Array<Record<string, unknown>> = [];
    let status = '';
    let nextStatus = 'running';
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime'))
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        if (url.includes('/agent-sessions/ags_current/snapshot'))
          return json({
            session: session('active'),
            turns: status ? [turn(status)] : [],
            change_sets: [],
            cursor: 'epoch:1',
          });
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/agent-sessions/ags_current/inputs')) {
          const action = status === 'running' ? 'steer_turn' : 'queue_turn';
          status = nextStatus;
          submitted.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return json({ action, turn: turn(status) });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.includes('/episodes/ep_1/assets')) return json({ assets: [] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );
    const firstTarget = {
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'h3-ref2va',
      keyframe_provider_model_id: 'h3-fl2va',
      focus: {
        clip_id: 'clip_1',
        generation_mode: 'reference' as const,
        generator_id: 'video.h3.shotforward',
        provider_model_id: 'h3-ref2va',
      },
    };
    const nextTarget = {
      generator_id: 'video.seedance-2.5.volcengine-cn',
      provider_model_id: 'seedance-2-5-pro',
      keyframe_provider_model_id: 'seedance-2-5-pro',
      focus: {
        clip_id: 'clip_2',
        generation_mode: 'first_image' as const,
        generator_id: 'video.seedance-2.5.volcengine-cn',
        provider_model_id: 'seedance-2-5-pro',
      },
    };
    function Harness() {
      const [changed, setChanged] = useState(false);
      return (
        <>
          <button onClick={() => setChanged((current) => !current)}>Change video model</button>
          <AgentDock
            projectID="prj_1"
            episodeID="ep_1"
            focus={{ clip_id: changed ? 'clip_2' : 'clip_1' }}
            videoPromptTarget={changed ? nextTarget : firstTarget}
          />
        </>
      );
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <Harness />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: 'Split this clip' } });
    fireEvent.submit(composer.closest('form')!);
    await waitFor(() => expect(submitted).toHaveLength(1));
    await screen.findByRole('button', { name: /停止|Stop/i });
    fireEvent.click(screen.getByRole('button', { name: 'Change video model' }));
    fireEvent.change(composer, { target: { value: 'Keep the original dialogue' } });
    fireEvent.submit(composer.closest('form')!);
    await waitFor(() => expect(submitted).toHaveLength(2));
    expect(submitted[0]?.video_prompt_target).toEqual(firstTarget);
    expect(submitted[1]?.video_prompt_target).toEqual(firstTarget);
    expect(submitted[1]?.focus).toEqual({ clip_id: 'clip_1' });
    await waitFor(() => expect(composer).toHaveValue(''));
    status = 'completed';
    await act(() => client.invalidateQueries({ queryKey: ['agent-session', 'ags_current'] }));
    await screen.findByRole('button', { name: /发送|Send/i });
    nextStatus = 'queued';
    fireEvent.change(composer, { target: { value: 'Improve this video prompt' } });
    fireEvent.submit(composer.closest('form')!);
    await waitFor(() => expect(submitted).toHaveLength(3));
    expect(submitted[2]?.video_prompt_target).toEqual(nextTarget);
    expect(submitted[2]?.focus).toEqual({ clip_id: 'clip_2' });
    await waitFor(() =>
      expect(
        client.getQueryData<{ turns: Array<{ status: string }> }>(['agent-session', 'ags_current'])?.turns[0]
          ?.status,
      ).toBe('queued'),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Change video model' }));
    fireEvent.change(composer, { target: { value: 'Split the newly selected clip' } });
    fireEvent.submit(composer.closest('form')!);
    await waitFor(() => expect(submitted).toHaveLength(4));
    expect(submitted[3]?.video_prompt_target).toEqual(firstTarget);
    expect(submitted[3]?.focus).toEqual({ clip_id: 'clip_1' });
    expect(submitted.map((input) => input.text)).toEqual([
      'Split this clip',
      'Keep the original dialogue',
      'Improve this video prompt',
      'Split the newly selected clip',
    ]);
  });

  it('keeps a new session quiet while its first Turn history is not ready yet', async () => {
    let historyRequests = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn('running')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          historyRequests += 1;
          if (historyRequests === 1) {
            return Promise.resolve(
              new Response(JSON.stringify({ error: { code: 'history_not_ready' } }), {
                status: 503,
                headers: { 'Content-Type': 'application/json' },
              }),
            );
          }
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i)).toBeInTheDocument();
    await waitFor(() => expect(historyRequests).toBe(2));
    expect(
      screen.queryByText(/对话历史暂时无法加载|Conversation history is temporarily unavailable/i),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/此会话还没有对话消息|This session has no conversation messages yet/i),
    ).not.toBeInTheDocument();
  });

  it('keeps an optimistic user message right-aligned while the Agent is answering', async () => {
    let submitted = false;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/agent-sessions/ags_current/inputs') && init?.method === 'POST') {
          submitted = true;
          return json({ action: 'queue_turn', turn: turn('running') });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: '120秒，抖音，专业运动，真人模特' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() => expect(submitted).toBe(true));
    const message = await screen.findByText('120秒，抖音，专业运动，真人模特');
    expect(message.closest('.conversation-message--user')?.parentElement).toHaveClass(
      'conversation-turn--optimistic',
    );
  });

  it('polls a live Turn to completion when its SSE completion event is missed', async () => {
    let snapshotReads = 0;
    let completed = false;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          snapshotReads += 1;
          completed = snapshotReads > 1;
          return json({
            session: session('active'),
            turns: [turn(completed ? 'completed' : 'running')],
            change_sets: [],
            cursor: `epoch:${snapshotReads}`,
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn(completed ? 'completed' : 'running'),
                messages: completed
                  ? [
                      { id: 'message-user', role: 'user', text: 'Fast request' },
                      { id: 'message-agent', role: 'assistant', text: 'Finished without refresh' },
                    ]
                  : [{ id: 'message-user', role: 'user', text: 'Fast request' }],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByText('Fast request')).toBeInTheDocument();
    expect(await screen.findByText(/正在处理|Working/i)).toBeInTheDocument();
    expect(await screen.findByText('Finished without refresh', {}, { timeout: 2_500 })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/正在处理|Working/i)).not.toBeInTheDocument());
  });

  it('grows the composer with its text and caps it at the maximum height', async () => {
    stubActiveSession();
    renderDock();

    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    Object.defineProperty(composer, 'scrollHeight', { configurable: true, value: 96 });
    fireEvent.change(composer, { target: { value: 'First line\nSecond line\nThird line' } });
    await waitFor(() => expect(composer).toHaveStyle({ height: '96px', overflowY: 'hidden' }));

    Object.defineProperty(composer, 'scrollHeight', { configurable: true, value: 240 });
    fireEvent.change(composer, { target: { value: `${'A long request '.repeat(30)}!` } });
    await waitFor(() => expect(composer).toHaveStyle({ height: '144px', overflowY: 'auto' }));
  });

  it('submits the selected model on the next turn without creating another session', async () => {
    let submitted: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: { health: 'ready' },
            models: [
              { id: 'gpt-test', display_name: 'Test' },
              { id: 'gpt-next', display_name: 'Next' },
            ],
          });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.includes('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: { ...turn('queued'), model_id: 'gpt-next' } });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const model = await screen.findByRole('combobox', {
      name: /后续对话使用的模型|Model for following turns/i,
    });
    expect(model.closest('.agent-dock__identity')).toBeInTheDocument();
    fireEvent.click(model);
    fireEvent.click(screen.getByRole('option', { name: 'Next' }));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Use the next model' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() => expect(submitted?.model_id).toBe('gpt-next'));
  });

  it('creates a compatible ShotForward session and sends when the runtime changes', async () => {
    let created: Record<string, unknown> | undefined;
    let submitted: Record<string, unknown> | undefined;
    const requests: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: { health: 'ready' },
            models: [
              { id: 'gpt-local', display_name: 'Local model', runtime_kind: 'user_codex' },
              { id: 'gpt-cloud', display_name: 'Cloud model', runtime_kind: 'shotforward' },
            ],
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_cloud/inputs')) {
          requests.push('submit');
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({
            action: 'queue_turn',
            turn: { ...turn('queued'), id: 'agt_cloud', agent_session_id: 'ags_cloud' },
          });
        }
        if (url.includes('/agent-sessions/ags_cloud/snapshot')) {
          return json({
            session: {
              ...session('active'),
              id: 'ags_cloud',
              model_id: 'gpt-cloud',
              runtime_kind: 'shotforward',
            },
            turns: [],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_cloud/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          requests.push('create');
          created = JSON.parse(String(init.body)) as Record<string, unknown>;
          return json({
            session: {
              ...session('active'),
              id: 'ags_cloud',
              title: 'Cloud conversation',
              model_id: 'gpt-cloud',
              runtime_kind: 'shotforward',
            },
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    const currentTarget = {
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'h3-ref2va',
      keyframe_provider_model_id: 'h3-fl2va',
    };
    const { client } = renderDock(undefined, undefined, currentTarget);
    const model = await screen.findByRole('combobox', {
      name: /后续对话使用的模型|Model for following turns/i,
    });
    fireEvent.click(model);
    fireEvent.click(screen.getByRole('option', { name: 'Cloud model' }));

    const composer = screen.getByPlaceholderText(/描述你想分析|Describe what you want/i);
    const form = composer.closest('form')!;
    fireEvent.change(composer, { target: { value: 'Use ShotForward cloud' } });
    expect(within(form).queryByRole('button', { name: /新建会话|New session/i })).not.toBeInTheDocument();
    const send = within(form).getByRole('button', { name: /发送|Send/i });
    expect(send).toBeEnabled();
    await act(async () => {
      client.setQueryData(['agent-session', 'ags_current'], {
        session: session('active'),
        turns: [turn('running')],
        change_sets: [],
        cursor: 'epoch:1',
      });
    });
    fireEvent.submit(form);

    await waitFor(() =>
      expect(created).toMatchObject({ model_id: 'gpt-cloud', runtime_kind: 'shotforward' }),
    );
    await waitFor(() =>
      expect(submitted).toMatchObject({ model_id: 'gpt-cloud', text: 'Use ShotForward cloud' }),
    );
    expect(requests).toEqual(['create', 'submit']);
    expect(submitted?.video_prompt_target).toEqual(currentTarget);
  });

  it('dismisses the session menu from an outside pointer press or Escape', async () => {
    stubActiveSession();
    renderDock();

    const trigger = await screen.findByRole('button', {
      name: /切换 Agent 会话|Switch Agent session/i,
    });
    fireEvent.click(trigger);
    expect(screen.getByRole('dialog', { name: /最近会话|Recent sessions/i })).toBeInTheDocument();

    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole('dialog', { name: /最近会话|Recent sessions/i })).not.toBeInTheDocument();

    fireEvent.click(trigger);
    const menu = screen.getByRole('dialog', { name: /最近会话|Recent sessions/i });
    fireEvent.pointerDown(menu);
    expect(menu).toBeInTheDocument();

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(screen.queryByRole('dialog', { name: /最近会话|Recent sessions/i })).not.toBeInTheDocument();
  });

  it('creates one unified Agent session from the selected model ID', async () => {
    let created: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: { health: 'ready' },
            models: [
              { id: 'gpt-local', display_name: 'Local model', runtime_kind: 'user_codex' },
              { id: 'gpt-cloud', display_name: 'Cloud model', runtime_kind: 'shotforward' },
            ],
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_cloud/snapshot')) {
          return json({
            session: {
              ...session('active'),
              id: 'ags_cloud',
              title: 'Cloud conversation',
              model_id: 'gpt-cloud',
              runtime_kind: 'shotforward',
            },
            turns: [],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_cloud/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          created = JSON.parse(String(init.body)) as Record<string, unknown>;
          return json({
            session: {
              ...session('active'),
              id: 'ags_cloud',
              title: 'Cloud conversation',
              model_id: 'gpt-cloud',
              runtime_kind: 'shotforward',
            },
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const model = await screen.findByRole('combobox', {
      name: /后续对话使用的模型|Model for following turns/i,
    });
    fireEvent.click(model);
    expect(screen.queryByText('local/gpt-local')).not.toBeInTheDocument();
    expect(screen.queryByText('shotforward/gpt-cloud')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('option', { name: 'Cloud model' }));

    const headerActions = document.querySelector<HTMLElement>('.agent-session-actions')!;
    expect(headerActions).toBeInTheDocument();
    fireEvent.click(within(headerActions).getByRole('button', { name: /新建会话|New session/i }));

    await waitFor(() =>
      expect(created).toMatchObject({ model_id: 'gpt-cloud', runtime_kind: 'shotforward' }),
    );
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i })).toHaveTextContent(
        'Cloud conversation',
      ),
    );

    fireEvent.click(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }));
    const menu = screen.getByRole('dialog', { name: /最近会话|Recent sessions/i });
    expect(menu).toHaveTextContent('Cloud conversation');
    expect(menu).not.toHaveTextContent('Local Codex Agent');
    expect(menu).not.toHaveTextContent('ShotForward Agent');
  });

  it('defaults a new ShotForward session to GPT-6 Luna instead of inheriting Astra', async () => {
    let created: Record<string, unknown> | undefined;
    const astraSession = {
      ...session('active'),
      model_id: 'gpt-6-astra',
      runtime_kind: 'shotforward',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            active_runtime_kind: 'shotforward',
            runtime: { health: 'ready' },
            models: [
              { id: 'gpt-6-astra', display_name: 'GPT-6 Astra', runtime_kind: 'shotforward' },
              { id: 'gpt-6-luna', display_name: 'GPT-6 Luna', runtime_kind: 'shotforward' },
              { id: 'gpt-6-sol', display_name: 'GPT-6 Sol', runtime_kind: 'shotforward' },
            ],
          });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: astraSession, turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
          created = JSON.parse(String(init.body)) as Record<string, unknown>;
          return json({
            session: {
              ...astraSession,
              id: 'ags_luna',
              title: 'Luna conversation',
              model_id: 'gpt-6-luna',
            },
          });
        }
        if (url.includes('/agent-sessions/ags_luna/snapshot')) {
          return json({
            session: { ...astraSession, id: 'ags_luna', model_id: 'gpt-6-luna' },
            turns: [],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_luna/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [astraSession] });
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const headerActions = await waitFor(() => {
      const value = document.querySelector<HTMLElement>('.agent-session-actions');
      expect(value).toBeInTheDocument();
      return value!;
    });
    fireEvent.click(within(headerActions).getByRole('button', { name: /新建会话|New session/i }));

    await waitFor(() =>
      expect(created).toMatchObject({ model_id: 'gpt-6-luna', runtime_kind: 'shotforward' }),
    );
  });

  it('reopens the composer when a persisted historical session is resumed', async () => {
    let listReads = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({ turns: [], next_cursor: '' });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          listReads += 1;
          return json({ sessions: [session(listReads === 1 ? 'history_unavailable' : 'active')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByRole('textbox')).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(1));
    expect(
      screen.queryByText(/历史会话无法继续|historical session cannot continue/i),
    ).not.toBeInTheDocument();
  });

  it('shows detached sessions as readable history without a composer', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('history_unavailable'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn('completed'),
                messages: [
                  { id: 'message-user', role: 'user', text: '**User markdown stays literal**' },
                  {
                    id: 'message-agent',
                    role: 'assistant',
                    text: '## Saved in Codex history\n\n- **Structured output**\n\n<script>alert(1)</script>\n\n[unsafe](javascript:alert(1))',
                  },
                ],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) {
          return json({ sessions: [session('history_unavailable')] });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByText('Saved in Codex history')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: 'Saved in Codex history' })).toBeInTheDocument();
    expect(screen.getByText('Structured output').tagName).toBe('STRONG');
    expect(screen.getByText('**User markdown stays literal**').querySelector('strong')).toBeNull();
    expect(document.querySelector('script')).not.toBeInTheDocument();
    expect(screen.getByText('unsafe').closest('a')).toBeNull();
    expect(screen.getByText(/历史会话无法继续|historical session cannot continue/i)).toBeInTheDocument();
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();

    const separator = screen.getByRole('separator', { name: /调整 Agent|Resize Agent/i });
    const dock = document.querySelector<HTMLElement>('.agent-dock')!;
    vi.spyOn(dock, 'getBoundingClientRect').mockReturnValue({
      width: 320,
      height: 700,
      x: 900,
      y: 0,
      top: 0,
      right: 1220,
      bottom: 700,
      left: 900,
      toJSON: () => ({}),
    });
    Object.assign(separator, {
      setPointerCapture: vi.fn(),
      hasPointerCapture: vi.fn(() => true),
      releasePointerCapture: vi.fn(),
    });
    fireEvent(separator, pointerEvent('pointerdown', 900));
    fireEvent(separator, pointerEvent('pointermove', 820));
    fireEvent(separator, pointerEvent('pointerup', 820));
    const layout = document.querySelector<HTMLElement>('.creative-layout')!;
    expect(layout.style.getPropertyValue('--agent-dock-width')).toBe('400px');
    expect(window.localStorage.getItem('shotforward.agent-dock-width')).toBe('400');

    fireEvent.doubleClick(separator);
    expect(layout.style.getPropertyValue('--agent-dock-width')).toBe('320px');
    fireEvent.keyDown(separator, { key: 'ArrowLeft' });
    expect(layout.style.getPropertyValue('--agent-dock-width')).toBe('336px');
    expect(window.localStorage.getItem('shotforward.agent-dock-width')).toBe('336');

    expect(screen.queryByRole('button', { name: /收起 Agent|Collapse Agent/i })).not.toBeInTheDocument();
  });

  it('renders an applied ChangeSet with Undo inside its turn', async () => {
    const requests: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        if (url.includes(':undo')) {
          return json({
            change_set: {
              id: 'cst_inverse',
              project_id: 'prj_1',
              episode_id: 'ep_1',
              agent_session_id: 'ags_current',
              agent_turn_id: 'agt_current',
              title: 'Undo cst_change',
              status: 'applied',
              operation_count: 1,
              revision: 4,
              reverts_change_set_id: 'cst_change',
            },
          });
        }
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [
              {
                id: 'cst_change',
                project_id: 'prj_1',
                episode_id: 'ep_1',
                agent_session_id: 'ags_current',
                agent_turn_id: 'agt_current',
                title: '撰写黛玉进府',
                summary: '写入本集剧本',
                status: 'applied',
                operation_count: 1,
                revision: 4,
              },
            ],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn('completed'),
                messages: [
                  { id: 'message-user', role: 'user', text: '写一个黛玉进府的剧本' },
                  { id: 'message-agent', role: 'assistant', text: '剧本已写入项目。' },
                ],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.includes('/api/v1/projects/prj_1/episodes/ep_1')) return json({});
        return json({});
      }),
    );

    renderDock();
    expect(await screen.findByText('撰写黛玉进府')).toBeInTheDocument();
    const undo = screen.getByRole('button', { name: /撤销|Undo/i });
    fireEvent.click(undo);
    await waitFor(() =>
      expect(requests.some((url) => url.includes('/change-sets/cst_change:undo'))).toBe(true),
    );

    const history = document.querySelector<HTMLElement>('.agent-history')!;
    Object.defineProperties(history, {
      scrollHeight: { configurable: true, value: 1000 },
      clientHeight: { configurable: true, value: 300 },
    });
    history.scrollTop = 100;
    fireEvent.scroll(history);
    expect(await screen.findByRole('button', { name: /回到最新|Back to latest/i })).toBeInTheDocument();
  });

  it('replaces existing composer text with a new action draft without sending it', async () => {
    stubActiveSession();
    const applied: number[] = [];

    function Harness() {
      const [draft, setDraft] = useState({ id: 1, text: 'First template' });
      return (
        <>
          <button onClick={() => setDraft({ id: 2, text: 'Replacement template' })}>Next template</button>
          <AgentDock
            projectID="prj_1"
            episodeID="ep_1"
            draftRequest={draft}
            onDraftApplied={(id) => applied.push(id)}
          />
        </>
      );
    }

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <div className="creative-layout">
            <Harness />
          </div>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    await waitFor(() => expect(composer).toHaveValue('First template'));
    fireEvent.change(composer, { target: { value: 'User draft that should be replaced' } });
    fireEvent.click(screen.getByRole('button', { name: 'Next template' }));
    await waitFor(() => {
      expect(composer).toHaveValue('Replacement template');
      expect(applied).toContain(2);
    });
    expect(document.querySelector('.agent-history')).not.toHaveTextContent('Replacement template');
  });

  it('uploads an image and submits it as a conversation-only attachment', async () => {
    let uploadedFileName = '';
    let uploadOperation = '';
    let submitted: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.endsWith('/episodes/ep_1/assets')) return json({ assets: [] });
        if (url.endsWith('/agent-sessions/ags_current/media')) {
          const body = init?.body as FormData;
          uploadedFileName = (body.get('file') as File).name;
          uploadOperation = String(body.get('client_operation_id'));
          return json({ media: conversationMedia('art_upload', 'reference.png', 'uploaded') });
        }
        if (url.endsWith('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: turn('queued') });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    const file = new File(['image-data'], 'reference.png', { type: 'image/png' });
    fireEvent.change(document.querySelector<HTMLInputElement>('.agent-media-file-input')!, {
      target: { files: [file] },
    });

    expect(await screen.findByTitle('reference.png')).toBeInTheDocument();
    expect(uploadedFileName).toBe('reference.png');
    expect(uploadOperation).not.toBe('');
    const composer = screen.getByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: 'Use this composition' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() =>
      expect(submitted).toMatchObject({
        text: 'Use this composition',
        attachments: [{ artifact_id: 'art_upload', name: 'reference.png', origin: 'uploaded' }],
      }),
    );
    expect(submitted?.client_operation_id).toBe(uploadOperation);
  });

  it.each([
    ['upload', 'preparing'],
    ['asset', 'preparing'],
    ['upload', 'episode'],
    ['asset', 'episode'],
    ['upload', 'session'],
    ['asset', 'session'],
  ] as const)(
    'ignores a delayed %s reply after changing the receiving context to %s',
    async (source, transition) => {
      let resolveMedia!: (response: Response) => void;
      let resolvePreparation!: (response: Response) => void;
      const mediaRequest = new Promise<Response>((done) => {
        resolveMedia = done;
      });
      const preparationRequest = new Promise<Response>((done) => {
        resolvePreparation = done;
      });
      const other = { ...session('active'), id: 'ags_other', title: 'Other conversation' };
      const newEpisode = {
        ...session('active'),
        id: 'ags_ep2',
        episode_id: 'ep_2',
        title: 'Other episode conversation',
      };
      const fresh = { ...session('active'), id: 'ags_new', title: 'New conversation' };
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
          const url = String(input);
          if (url.includes('/api/v1/agent/runtime'))
            return json({
              active_runtime_kind: 'user_codex',
              runtime: { health: 'ready' },
              models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
            });
          if (url.endsWith('/agent-sessions/ags_current/media') || url.includes('/media-slots/slot_asset?'))
            return mediaRequest;
          if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST')
            return preparationRequest;
          if (url.includes('/episodes/ep_1/agent-sessions'))
            return json({ sessions: [session('active'), other] });
          if (url.includes('/episodes/ep_2/agent-sessions')) return json({ sessions: [newEpisode] });
          if (url.includes('/snapshot'))
            return json({
              session: url.includes('ags_ep2')
                ? newEpisode
                : url.includes('ags_other')
                  ? other
                  : url.includes('ags_new')
                    ? fresh
                    : session('active'),
              turns: [],
              change_sets: [],
              cursor: 'real:1',
            });
          if (url.includes('/messages')) return json({ turns: [], next_cursor: '' });
          if (url.endsWith('/assets')) return json({ assets: [episodeAsset()] });
          if (url.endsWith('/reference-media')) return json({ reference_media: [] });
          return Promise.reject(new Error(`Unexpected request: ${url}`));
        }),
      );
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      function ScopeHarness() {
        const [episode, setEpisode] = useState('ep_1');
        return (
          <>
            <button onClick={() => setEpisode('ep_2')}>Switch episode</button>
            <AgentDock projectID="prj_1" episodeID={episode} />
          </>
        );
      }
      render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <ScopeHarness />
          </MemoryRouter>
        </QueryClientProvider>,
      );
      const composer = await screen.findByRole('textbox');
      if (source === 'upload') {
        fireEvent.change(document.querySelector<HTMLInputElement>('.agent-media-file-input')!, {
          target: { files: [new File(['image'], 'late.png', { type: 'image/png' })] },
        });
      } else {
        fireEvent.change(composer, { target: { value: '@', selectionStart: 1 } });
        fireEvent.click(await screen.findByRole('button', { name: /Hero/ }));
      }
      if (transition === 'preparing') {
        fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));
        await screen.findByText(/正在准备新会话|Preparing your new conversation/);
      } else if (transition === 'episode') {
        fireEvent.click(screen.getByRole('button', { name: 'Switch episode' }));
        await waitFor(() =>
          expect(
            screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }),
          ).toHaveTextContent('Other episode conversation'),
        );
      } else {
        fireEvent.click(screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }));
        fireEvent.click(screen.getByRole('button', { name: /Other conversation/ }));
        await waitFor(() =>
          expect(
            screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }),
          ).toHaveTextContent('Other conversation'),
        );
      }
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'New context draft' } });
      await act(async () => {
        resolveMedia(
          await json(
            source === 'upload'
              ? { media: conversationMedia('art_late', 'late.png', 'uploaded') }
              : mediaSlot('slot_asset', 'art_late'),
          ),
        );
      });
      if (transition === 'preparing') {
        await act(async () => {
          resolvePreparation(await json({ session: fresh }));
        });
        await waitFor(() =>
          expect(
            screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }),
          ).toHaveTextContent('New conversation'),
        );
      }
      expect(screen.getByRole('textbox')).toHaveValue('New context draft');
      expect(document.querySelector('.agent-attachments')).not.toBeInTheDocument();
      expect(screen.queryByTitle('late.png')).not.toBeInTheDocument();
      expect(screen.queryByTitle('Hero')).not.toBeInTheDocument();
      expect(
        screen.getByRole('button', { name: /上传图片到当前对话|Upload images to this conversation/i }),
      ).toBeEnabled();
      client.clear();
    },
  );

  it.each(['success', 'failure'] as const)(
    'does not let an old send %s alter a new conversation draft or attachments',
    async (outcome) => {
      let resolveSend!: (response: Response) => void;
      const sendRequest = new Promise<Response>((done) => {
        resolveSend = done;
      });
      let created = false;
      const fresh = { ...session('active'), id: 'ags_new', title: 'Fresh conversation' };
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
          const url = String(input);
          if (url.includes('/api/v1/agent/runtime'))
            return json({
              active_runtime_kind: 'user_codex',
              runtime: { health: 'ready' },
              models: [{ id: 'gpt-test', display_name: 'Test', runtime_kind: 'user_codex' }],
            });
          if (url.endsWith('/agent-sessions/ags_current/inputs')) return sendRequest;
          if (url.includes('/episodes/ep_1/agent-sessions') && init?.method === 'POST') {
            created = true;
            return json({ session: fresh });
          }
          if (url.includes('/episodes/ep_1/agent-sessions'))
            return json({ sessions: created ? [fresh, session('active')] : [session('active')] });
          if (url.includes('/snapshot'))
            return json({
              session: url.includes('ags_new') ? fresh : session('active'),
              turns: [],
              change_sets: [],
              cursor: 'real:1',
            });
          if (url.includes('/messages')) return json({ turns: [], next_cursor: '' });
          if (url.endsWith('/agent-sessions/ags_new/media'))
            return json({ media: conversationMedia('art_new', 'new.png', 'uploaded') });
          if (url.endsWith('/assets')) return json({ assets: [] });
          return Promise.reject(new Error(`Unexpected request: ${url}`));
        }),
      );
      const { client } = renderDock();
      fireEvent.change(await screen.findByRole('textbox'), { target: { value: 'Old request' } });
      fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));
      await waitFor(() => expect(screen.getByRole('textbox')).toHaveValue(''));
      fireEvent.click(screen.getByRole('button', { name: /新建会话|New session/i }));
      await waitFor(() =>
        expect(
          screen.getByRole('button', { name: /切换 Agent 会话|Switch Agent session/i }),
        ).toHaveTextContent('Fresh conversation'),
      );
      fireEvent.change(document.querySelector<HTMLInputElement>('.agent-media-file-input')!, {
        target: { files: [new File(['image'], 'new.png', { type: 'image/png' })] },
      });
      expect(await screen.findByTitle('new.png')).toBeInTheDocument();
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'New conversation draft' } });
      await act(async () => {
        resolveSend(
          outcome === 'success'
            ? await json({ action: 'queue_turn', turn: turn('queued') })
            : new Response(
                JSON.stringify({ error: { code: 'runtime_unavailable', message: 'Old send failed' } }),
                { status: 503, headers: { 'Content-Type': 'application/json' } },
              ),
        );
      });
      expect(screen.getByRole('textbox')).toHaveValue('New conversation draft');
      expect(screen.getByTitle('new.png')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /发送|Send/i })).toBeEnabled();
      client.clear();
    },
  );

  it('references the current Asset image and includes the @ mention in the submitted turn', async () => {
    let submitted: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.endsWith('/episodes/ep_1/assets')) return json({ assets: [episodeAsset()] });
        if (url.endsWith('/episodes/ep_1/reference-media')) return json({ reference_media: [] });
        if (url.includes('/media-slots/slot_asset?include_deleted=false')) {
          return json(mediaSlot('slot_asset', 'art_asset'));
        }
        if (url.endsWith('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: turn('queued') });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    const composer = await screen.findByPlaceholderText(/描述你想分析|Describe what you want/i);
    fireEvent.change(composer, { target: { value: '@', selectionStart: 1 } });
    expect(await screen.findByRole('button', { name: /Hero/ })).toBeInTheDocument();

    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole('button', { name: /Hero/ })).not.toBeInTheDocument();

    const prompt = 'Please refine @';
    fireEvent.change(composer, { target: { value: prompt, selectionStart: prompt.length } });
    fireEvent.click(await screen.findByRole('button', { name: /Hero/ }));

    await waitFor(() => expect(composer).toHaveValue('Please refine @Hero '));
    expect(screen.getByTitle('Hero')).toBeInTheDocument();
    fireEvent.change(composer, { target: { value: 'Please refine @Hero lighting' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() =>
      expect(submitted).toMatchObject({
        text: 'Please refine @Hero lighting',
        attachments: [{ artifact_id: 'art_asset', name: 'Hero', origin: 'asset' }],
      }),
    );
  });

  it('references an uploaded library image from the @ picker', async () => {
    let submitted: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.endsWith('/episodes/ep_1/assets')) return json({ assets: [] });
        if (url.endsWith('/episodes/ep_1/reference-media')) {
          return json({ reference_media: [episodeReferenceMedia()] });
        }
        if (url.endsWith('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: turn('queued') });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    fireEvent.click(
      await screen.findByRole('button', {
        name: /引用资产或上传素材|Reference an Asset or uploaded medium/i,
      }),
    );
    fireEvent.click(await screen.findByRole('button', { name: /造型参考/ }));

    const composer = screen.getByPlaceholderText(/描述你想分析|Describe what you want/i);
    await waitFor(() => expect(composer).toHaveValue('@造型参考 '));
    expect(screen.getByTitle('造型参考')).toBeInTheDocument();
    fireEvent.change(composer, { target: { value: '@造型参考 调整画面' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() =>
      expect(submitted).toMatchObject({
        text: '@造型参考 调整画面',
        attachments: [{ artifact_id: 'art_library', name: '造型参考', origin: 'asset' }],
      }),
    );
  });

  it('references uploaded audio by name without attaching its binary to the conversation', async () => {
    let submitted: Record<string, unknown> | undefined;
    const audio = {
      ...episodeReferenceMedia(),
      name: '布布配音',
      artifact: {
        ...episodeReferenceMedia().artifact,
        artifact_id: 'art_audio',
        kind: 'audio' as const,
        mime_type: 'audio/mpeg',
        original_filename: 'bubu.mp3',
        width: 0,
        height: 0,
        duration_ms: 15_000,
      },
    };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) return json({ turns: [], next_cursor: '' });
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.endsWith('/episodes/ep_1/assets')) return json({ assets: [] });
        if (url.endsWith('/episodes/ep_1/reference-media')) return json({ reference_media: [audio] });
        if (url.endsWith('/agent-sessions/ags_current/inputs')) {
          submitted = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return json({ action: 'queue_turn', turn: turn('queued') });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderDock();
    fireEvent.click(
      await screen.findByRole('button', {
        name: /引用资产或上传素材|Reference an Asset or uploaded medium/i,
      }),
    );
    fireEvent.click(await screen.findByRole('button', { name: /布布配音/ }));

    const composer = screen.getByPlaceholderText(/描述你想分析|Describe what you want/i);
    await waitFor(() => expect(composer).toHaveValue('@布布配音 '));
    expect(screen.queryByTitle('布布配音')).not.toBeInTheDocument();
    fireEvent.change(composer, { target: { value: '@布布配音 绑定到当前视频单元' } });
    fireEvent.click(screen.getByRole('button', { name: /发送|Send/i }));

    await waitFor(() => expect(submitted).toMatchObject({ text: '@布布配音 绑定到当前视频单元' }));
    expect(submitted).not.toHaveProperty('attachments');
  });

  it('keeps a generated image in the conversation while saving and continuing with it', async () => {
    const assignments: Array<{ slot: string; body: Record<string, unknown> }> = [];
    const createdAssets: Record<string, unknown>[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (url.includes('/api/v1/agent/runtime')) {
          return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
        }
        if (url.includes('/api/v1/cloud/account')) return json({ signed_in: true });
        if (url.includes('/agent-sessions/ags_current/snapshot')) {
          return json({
            session: session('active'),
            turns: [turn('completed')],
            change_sets: [],
            cursor: 'epoch:1',
          });
        }
        if (url.includes('/agent-sessions/ags_current/messages')) {
          return json({
            turns: [
              {
                turn: turn('completed'),
                messages: [{ id: 'message-agent', role: 'assistant', text: 'Generated a result.' }],
                media: [conversationMedia('art_generated', 'Generated image', 'generated')],
              },
            ],
            next_cursor: '',
          });
        }
        if (url.includes('/episodes/ep_1/agent-sessions')) return json({ sessions: [session('active')] });
        if (url.endsWith('/episodes/ep_1/assets') && method === 'POST') {
          createdAssets.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return json({ asset: episodeAsset('ast_new', 'Created result', 'slot_new') });
        }
        if (url.endsWith('/episodes/ep_1/assets')) return json({ assets: [episodeAsset()] });
        const slotID = url.match(/\/media-slots\/(slot_(?:first|last|asset|new))/)?.[1];
        if (slotID && method === 'POST' && url.endsWith('/artifacts')) {
          assignments.push({ slot: slotID, body: JSON.parse(String(init?.body)) as Record<string, unknown> });
          return json(mediaSlot(slotID, 'art_generated', 2));
        }
        if (slotID && url.includes('?include_deleted=true')) return json(mediaSlot(slotID, '', 1));
        return Promise.reject(new Error(`Unexpected request: ${method} ${url}`));
      }),
    );

    renderDock();
    expect(await screen.findByRole('img', { name: 'Generated image' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /下载|Download/i }));
    expect(desktopMocks.saveDownload).toHaveBeenCalledWith(
      '/api/v1/projects/prj_1/artifacts/art_generated/content',
      'Generated image.png',
    );
    expect(
      screen.queryByRole('button', { name: /应用到视频单元|Apply to video unit/i }),
    ).not.toBeInTheDocument();

    expect(screen.getByRole('img', { name: 'Generated image' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /保存为资产|Save as Asset/i }));
    const saveDialog = await screen.findByRole('dialog', { name: /保存为资产|Save as Asset/i });
    fireEvent.change(within(saveDialog).getByLabelText(/资产名称|Asset name/i), {
      target: { value: 'Created result' },
    });
    expect(within(saveDialog).getByRole('combobox', { name: /资产类型|Asset type/i })).toHaveTextContent(
      /道具|Prop/i,
    );
    fireEvent.click(within(saveDialog).getByRole('button', { name: /创建资产|Create Asset/i }));
    await waitFor(() =>
      expect(createdAssets).toEqual([{ kind: 'prop', name: 'Created result', prompt: '' }]),
    );
    await waitFor(() => expect(assignments[0]?.slot).toBe('slot_new'));

    expect(screen.getByRole('img', { name: 'Generated image' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /保存为资产|Save as Asset/i }));
    const versionDialog = await screen.findByRole('dialog', { name: /保存为资产|Save as Asset/i });
    fireEvent.click(
      within(versionDialog).getByRole('button', {
        name: /新增版本|Add version/i,
      }),
    );
    fireEvent.click(
      within(versionDialog).getByRole('combobox', {
        name: /选择已有资产|Choose an existing Asset/i,
      }),
    );
    fireEvent.click(await within(versionDialog).findByRole('option', { name: 'Hero' }));
    fireEvent.click(within(versionDialog).getByRole('button', { name: /保存新版本|Save new version/i }));
    await waitFor(() => expect(assignments[1]?.slot).toBe('slot_asset'));

    fireEvent.click(screen.getByRole('button', { name: /继续修改|Continue editing/i }));
    expect(screen.getByTitle('Generated image')).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'Generated image' })).toBeInTheDocument();
  });
});

function renderDock(
  draftRequest?: AgentDraftRequest,
  onDraftApplied?: (id: number) => void,
  videoPromptTarget?: Parameters<typeof AgentDock>[0]['videoPromptTarget'],
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <div className="creative-layout">
          <div />
          <main />
          <AgentDock
            projectID="prj_1"
            episodeID="ep_1"
            {...(draftRequest ? { draftRequest } : {})}
            {...(onDraftApplied ? { onDraftApplied } : {})}
            {...(videoPromptTarget ? { videoPromptTarget } : {})}
          />
        </div>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, client };
}

function episodeReferenceMedia() {
  return {
    episode_id: 'ep_1',
    name: '造型参考',
    description: '',
    usage_count: 0,
    revision: 1,
    created_at: '2026-09-06T00:00:00Z',
    updated_at: '2026-09-06T00:00:00Z',
    artifact: {
      artifact_id: 'art_library',
      kind: 'image' as const,
      mime_type: 'image/png',
      original_filename: 'look.png',
      byte_size: 123,
      width: 64,
      height: 36,
      duration_ms: 0,
      created_at: '2026-09-06T00:00:00Z',
    },
  };
}

function stubActiveSession() {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/agent/runtime')) {
        return json({ runtime: { health: 'ready' }, models: [{ id: 'gpt-test', display_name: 'Test' }] });
      }
      if (url.includes('/agent-sessions/ags_current/snapshot')) {
        return json({ session: session('active'), turns: [], change_sets: [], cursor: 'epoch:1' });
      }
      if (url.includes('/agent-sessions/ags_current/messages')) {
        return json({ turns: [], next_cursor: '' });
      }
      if (url.includes('/episodes/ep_1/agent-sessions')) {
        return json({ sessions: [session('active')] });
      }
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    }),
  );
}

function session(status: 'active' | 'history_unavailable') {
  return {
    id: 'ags_current',
    project_id: 'prj_1',
    episode_id: 'ep_1',
    model_id: 'gpt-test',
    runtime_kind: 'user_codex',
    title: 'Conversation',
    status,
    revision: 1,
    created_at: '2026-09-06T00:00:00Z',
    updated_at: '2026-09-06T00:00:00Z',
  };
}

function turn(status: string) {
  return {
    id: 'agt_current',
    agent_session_id: 'ags_current',
    status,
    created_at: '2026-09-06T00:00:00Z',
    completed_at: status === 'completed' ? '2026-09-06T00:00:02Z' : undefined,
  };
}

function json(body: unknown) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
  );
}

function conversationMedia(artifactID: string, name: string, origin: 'uploaded' | 'asset' | 'generated') {
  return {
    artifact_id: artifactID,
    name,
    origin,
    mime_type: 'image/png',
    original_filename: `${name}.png`,
    byte_size: 123,
    width: 64,
    height: 36,
    created_at: '2026-09-06T00:00:00Z',
  };
}

function episodeAsset(assetID = 'ast_existing', name = 'Hero', slotID = 'slot_asset') {
  return {
    asset_id: assetID,
    project_id: 'prj_1',
    episode_id: 'ep_1',
    identity: {
      asset_identity_id: `identity_${assetID}`,
      project_id: 'prj_1',
      kind: 'character',
      name,
      revision: 1,
      created_at: '2026-09-06T00:00:00Z',
      updated_at: '2026-09-06T00:00:00Z',
    },
    prompt: '',
    revision: 1,
    primary_image_slot_id: slotID,
    created_at: '2026-09-06T00:00:00Z',
    updated_at: '2026-09-06T00:00:00Z',
  };
}

function mediaSlot(slotID: string, artifactID: string, revision = 1) {
  return {
    slot: {
      media_slot_id: slotID,
      current_media_version_id: artifactID ? `version_${artifactID}` : '',
      revision,
    },
    versions: artifactID
      ? [{ media_version_id: `version_${artifactID}`, artifact_id: artifactID, sequence_number: 1 }]
      : [],
    artifacts: artifactID
      ? [
          {
            artifact_id: artifactID,
            kind: 'image',
            mime_type: 'image/png',
            original_filename: `${artifactID}.png`,
            byte_size: 123,
            width: 64,
            height: 36,
            duration_ms: 0,
            created_at: '2026-09-06T00:00:00Z',
          },
        ]
      : [],
  };
}

function pointerEvent(type: string, clientX: number) {
  const event = new Event(type, { bubbles: true });
  Object.defineProperties(event, {
    pointerId: { value: 7 },
    clientX: { value: clientX },
  });
  return event;
}
