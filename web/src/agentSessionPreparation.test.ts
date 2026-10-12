import { QueryClient } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentSession } from './api/agent';
import {
  prepareAgentSession,
  sessionPreparationKey,
  type AgentSessionPreparation,
} from './agentSessionPreparation';

const mocks = vi.hoisted(() => ({ createSession: vi.fn() }));
vi.mock('./api/agent', () => mocks);
function deferred() {
  let resolve!: (value: { session: AgentSession }) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<{ session: AgentSession }>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}
const session: AgentSession = {
  id: 'new',
  project_id: 'p',
  episode_id: 'e',
  runtime_kind: 'user_codex',
  model_id: 'gpt-test',
  title: '',
  status: 'active',
  revision: 1,
  created_at: '',
  updated_at: '',
};

describe('episode-scoped Agent session preparation', () => {
  beforeEach(() => vi.clearAllMocks());

  it('blocks unavailable runtime creation and permits a ready runtime without a balance check', async () => {
    const cache = new QueryClient();
    cache.setQueryData(['agent-runtime'], {
      active_runtime_kind: 'user_codex',
      runtime: { health: 'login_required' },
    });
    await expect(prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex')).rejects.toMatchObject({
      code: 'runtime_unavailable',
    });
    expect(mocks.createSession).not.toHaveBeenCalled();
    cache.setQueryData(['agent-runtime'], {
      active_runtime_kind: 'user_codex',
      runtime: { health: 'ready' },
    });
    mocks.createSession.mockResolvedValue({ session });
    await expect(prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex')).resolves.toEqual(session);
    expect(mocks.createSession).toHaveBeenCalledTimes(1);
    cache.clear();
  });

  it('joins background and manual requests, selects the new session first, and seeds only empty history', async () => {
    const cache = new QueryClient();
    const request = deferred();
    mocks.createSession.mockReturnValue(request.promise);
    cache.setQueryData(['agent-sessions', 'p', 'e'], { sessions: [{ ...session, id: 'old' }] });
    const first = prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex');
    const second = prepareAgentSession(cache, 'p', 'e', 'other-model', 'user_codex');
    expect(second).toBe(first);
    expect(mocks.createSession).toHaveBeenCalledTimes(1);
    request.resolve({ session });
    await first;
    expect(
      cache
        .getQueryData<{ sessions: AgentSession[] }>(['agent-sessions', 'p', 'e'])
        ?.sessions.map((item) => item.id),
    ).toEqual(['new', 'old']);
    expect(cache.getQueryData(['agent-conversation', 'new'])).toEqual({
      pages: [{ turns: [], next_cursor: '' }],
      pageParams: [''],
    });
    expect(cache.getQueryData(['agent-session', 'new'])).toBeUndefined();
    expect(
      cache.getQueryData<AgentSessionPreparation>(sessionPreparationKey('p', 'e', 'user_codex'))?.status,
    ).toBe('ready');
    cache.clear();
  });

  it('retains background errors for the workspace and supports an explicit retry', async () => {
    const cache = new QueryClient();
    const request = deferred();
    const error = new Error('offline');
    mocks.createSession.mockReturnValueOnce(request.promise).mockResolvedValueOnce({ session });
    const promise = prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex');
    request.reject(error);
    await expect(promise).rejects.toBe(error);
    expect(
      cache.getQueryData<AgentSessionPreparation>(sessionPreparationKey('p', 'e', 'user_codex')),
    ).toMatchObject({ status: 'failed', error });
    await prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex');
    expect(mocks.createSession).toHaveBeenCalledTimes(2);
    cache.clear();
  });

  it('records failures in success-side cache coordination instead of remaining preparing', async () => {
    const cache = new QueryClient();
    const error = new Error('Cache coordination failed');
    vi.spyOn(cache, 'cancelQueries').mockRejectedValue(error);
    mocks.createSession.mockResolvedValue({ session });
    await expect(prepareAgentSession(cache, 'p', 'e', 'gpt-test', 'user_codex')).rejects.toBe(error);
    expect(cache.getQueryData(sessionPreparationKey('p', 'e', 'user_codex'))).toMatchObject({
      status: 'failed',
      error,
    });
    cache.clear();
  });

  it('keeps episodes and services separate and ignores pre-logout results even after a new same-scope request', async () => {
    const cache = new QueryClient();
    const old = deferred();
    const fresh = deferred();
    mocks.createSession
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce({ session: { ...session, id: 'cloud', runtime_kind: 'shotforward' } })
      .mockReturnValueOnce(fresh.promise);
    const previous = prepareAgentSession(cache, 'p', 'e', 'local', 'user_codex');
    await prepareAgentSession(cache, 'p', 'other', 'cloud', 'shotforward');
    expect(cache.getQueryData(sessionPreparationKey('p', 'e', 'user_codex'))).toMatchObject({
      status: 'preparing',
    });
    cache.clear();
    const next = prepareAgentSession(cache, 'p', 'e', 'local', 'user_codex');
    old.resolve({ session: { ...session, id: 'stale' } });
    await previous;
    expect(cache.getQueryData(['agent-sessions', 'p', 'e'])).toBeUndefined();
    expect(cache.getQueryData(sessionPreparationKey('p', 'e', 'user_codex'))).toMatchObject({
      status: 'preparing',
    });
    fresh.resolve({ session });
    await next;
    expect(
      cache
        .getQueryData<{ sessions: AgentSession[] }>(['agent-sessions', 'p', 'e'])
        ?.sessions.map((item) => item.id),
    ).toEqual(['new']);
    cache.clear();
  });
});
