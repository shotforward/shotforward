import { QueryClient } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentRuntimeKind, RuntimeResponse } from './api/runtime';
import {
  agentRuntimeKey,
  agentRuntimeOptions,
  changeAgentRuntime,
  refreshAgentRuntime,
  warmAgentRuntime,
} from './agentRuntime';

const mocks = vi.hoisted(() => ({ getRuntime: vi.fn(), selectAgentRuntime: vi.fn() }));
vi.mock('./api/runtime', () => mocks);
const clients: QueryClient[] = [];
function client() {
  const value = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  clients.push(value);
  return value;
}
function response(
  kind: AgentRuntimeKind,
  health: RuntimeResponse['runtime']['health'] = 'ready',
): RuntimeResponse {
  return {
    active_runtime_kind: kind,
    target_runtime_kind: kind,
    runtime: { health, version: '', minimum_version: '', failure_code: '', failure_summary: '' },
    models: [{ id: `${kind}-model`, display_name: kind, runtime_kind: kind }],
    request_id: 'req_test',
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe('shared Agent runtime initialization', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => {
    clients.splice(0).forEach((value) => value.clear());
    vi.useRealTimers();
  });

  it('warms without waiting, shares a fresh cache, polls starting, and stops at ready', async () => {
    vi.useFakeTimers();
    const cache = client();
    mocks.getRuntime
      .mockResolvedValueOnce(response('user_codex', 'starting'))
      .mockResolvedValue(response('user_codex'));
    const stop = warmAgentRuntime(cache);
    expect(typeof stop).toBe('function');
    await vi.advanceTimersByTimeAsync(0);
    expect(cache.getQueryData<RuntimeResponse>(agentRuntimeKey())?.runtime.health).toBe('starting');
    await vi.advanceTimersByTimeAsync(1_000);
    expect(cache.getQueryData<RuntimeResponse>(agentRuntimeKey())?.runtime.health).toBe('ready');
    await cache.fetchQuery(agentRuntimeOptions(cache));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(mocks.getRuntime).toHaveBeenCalledTimes(2);
    stop();
  });

  it('refreshes the selected service into the default cache but keeps inactive service models separate', async () => {
    const cache = client();
    cache.setQueryData(agentRuntimeKey(), response('shotforward'));
    cache.setQueryData(agentRuntimeKey('shotforward'), response('shotforward'));
    cache.setQueryData(agentRuntimeKey('user_codex'), response('user_codex'));
    const refreshed = {
      ...response('shotforward'),
      models: [{ id: 'new-cloud', display_name: 'Cloud', runtime_kind: 'shotforward' as const }],
    };
    mocks.getRuntime.mockResolvedValueOnce(refreshed).mockResolvedValueOnce(response('user_codex'));
    await refreshAgentRuntime(cache, 'shotforward');
    await refreshAgentRuntime(cache, 'user_codex');
    expect(cache.getQueryData(agentRuntimeKey())).toEqual(refreshed);
    expect(cache.getQueryData<RuntimeResponse>(agentRuntimeKey('user_codex'))?.active_runtime_kind).toBe(
      'shotforward',
    );
    expect(mocks.getRuntime).toHaveBeenCalledWith({ runtimeKind: 'shotforward', refresh: true });
  });

  it('does not let an old default request undo a service switch', async () => {
    const cache = client();
    const old = deferred<RuntimeResponse>();
    mocks.getRuntime.mockReturnValue(old.promise);
    cache.setQueryData(agentRuntimeKey('shotforward'), response('shotforward'));
    const loading = cache.fetchQuery(agentRuntimeOptions(cache));
    mocks.selectAgentRuntime.mockResolvedValue(response('shotforward'));
    await changeAgentRuntime(cache, 'shotforward');
    old.resolve(response('user_codex'));
    await loading;
    expect(cache.getQueryData<RuntimeResponse>(agentRuntimeKey())?.active_runtime_kind).toBe('shotforward');
    expect(cache.getQueryData<RuntimeResponse>(agentRuntimeKey())?.models[0]?.id).toBe('shotforward-model');
  });

  it('discards refresh and selection callbacks after authentication clears the cache', async () => {
    const cache = client();
    cache.setQueryData(agentRuntimeKey('user_codex'), response('user_codex'));
    const refreshing = deferred<RuntimeResponse>();
    const selecting = deferred<RuntimeResponse>();
    mocks.getRuntime.mockReturnValue(refreshing.promise);
    mocks.selectAgentRuntime.mockReturnValue(selecting.promise);
    const refresh = refreshAgentRuntime(cache, 'user_codex');
    const selection = changeAgentRuntime(cache, 'user_codex');
    cache.clear();
    refreshing.resolve(response('user_codex'));
    selecting.resolve(response('user_codex'));
    await Promise.all([refresh, selection]);
    expect(cache.getQueryCache().getAll()).toHaveLength(0);
  });
});
