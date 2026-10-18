import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { PropsWithChildren } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useAgentEvents } from './useAgentEvents';

class FakeEventSource {
  static latest: FakeEventSource | undefined;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Set<(event: Event) => void>>();

  constructor(public readonly url: string) {
    FakeEventSource.latest = this;
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener as (event: Event) => void);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.get(type)?.delete(listener as (event: Event) => void);
  }

  close() {}

  emit(type: string, body: unknown) {
    const event = new MessageEvent('message', { data: JSON.stringify(body) });
    this.listeners.get(type)?.forEach((listener) => listener(event));
  }

  listener(type: string) {
    return [...(this.listeners.get(type) ?? [])][0];
  }
}

describe('useAgentEvents', () => {
  it('retains tool business errors through turn completion, clears on a new turn and isolates sessions', () => {
    const client = new QueryClient();
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, rerender, unmount } = renderHook(({ id, turn }) => useAgentEvents(id, 'epoch:1', turn), {
      wrapper,
      initialProps: { id: 'ags_1', turn: { id: 'agt_1', status: 'running' } },
    });
    act(() =>
      FakeEventSource.latest!.emit('tool.completed', {
        type: 'tool.completed',
        session_id: 'ags_1',
        turn_id: 'agt_1',
        tool_name: 'shotforward/write',
        failure_code: 'shotforward_insufficient_points',
      }),
    );
    act(() =>
      FakeEventSource.latest!.emit('turn.completed', {
        type: 'turn.completed',
        session_id: 'ags_1',
        turn_id: 'agt_1',
      }),
    );
    expect(result.current.toolFailureCode).toBe('shotforward_insufficient_points');
    rerender({ id: 'ags_1', turn: { id: 'agt_2', status: 'running' } });
    act(() =>
      FakeEventSource.latest!.emit('turn.started', {
        type: 'turn.started',
        session_id: 'ags_1',
        turn_id: 'agt_2',
      }),
    );
    expect(result.current.toolFailureCode).toBeUndefined();
    act(() =>
      FakeEventSource.latest!.emit('tool.completed', {
        type: 'tool.completed',
        session_id: 'ags_1',
        turn_id: 'agt_2',
        tool_name: 'shotforward/write',
        failure_code: 'shotforward_insufficient_points',
      }),
    );
    rerender({ id: 'ags_2', turn: { id: 'agt_3', status: 'running' } });
    expect(result.current.toolFailureCode).toBeUndefined();
    unmount();
    client.clear();
  });
  beforeEach(() => {
    FakeEventSource.latest = undefined;
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  it.each([
    ['assistant.delta', { text_delta: 'Response resumed' }],
    ['tool.started', { tool_name: 'shotforward/write' }],
    ['tool.completed', { tool_name: 'shotforward/write' }],
    ['turn.started', {}],
  ])('clears a recoverable retry when current-turn %s arrives', (type, extra) => {
    const { result, unmount, client } = renderProgressEvents();
    emitProgressEvent('tool.started', { tool_name: 'shotforward/read' });
    expect(result.current.activity).toBe('shotforward/read');
    emitProgressEvent('turn.retrying');
    expect(result.current.retryingTurnID).toBe('agt_1');
    expect(result.current.activity).toBe('');
    emitProgressEvent(type, extra);
    expect(result.current.retryingTurnID).toBeUndefined();
    unmount();
    client.clear();
  });

  it('does not report a model retry from an EventSource transport error alone', () => {
    const { result, unmount, client } = renderProgressEvents();
    act(() => FakeEventSource.latest!.onerror?.());
    expect(result.current.connected).toBe(false);
    expect(result.current.retryingTurnID).toBeUndefined();
    unmount();
    client.clear();
  });

  it('ignores old-turn retries, old-turn output, wrong sessions, and empty recovery events', () => {
    const { result, unmount, client } = renderProgressEvents();
    emitProgressEvent('turn.retrying', {}, 'agt_old');
    expect(result.current.retryingTurnID).toBeUndefined();
    emitProgressEvent('turn.retrying');
    emitProgressEvent('assistant.delta', { text_delta: 'Old output' }, 'agt_old');
    emitProgressEvent('tool.completed', { tool_name: 'shotforward/write' }, 'agt_old');
    emitProgressEvent('turn.started', {}, 'agt_old');
    emitProgressEvent('turn.completed', {}, 'agt_old');
    emitProgressEvent('tool.started', { tool_name: 'shotforward/write' }, 'agt_1', 'ags_other');
    emitProgressEvent('assistant.delta', { text_delta: '' });
    emitProgressEvent('tool.started', {});
    expect(result.current.retryingTurnID).toBe('agt_1');
    expect(result.current.activity).toBe('');
    unmount();
    client.clear();
  });

  it.each(['completed', 'failed', 'interrupted', 'succeeded'])(
    'clears retry when the authoritative snapshot becomes %s without a completion SSE',
    (status) => {
      const { result, rerender, unmount, client } = renderProgressEvents();
      emitProgressEvent('turn.retrying');
      expect(result.current.retryingTurnID).toBe('agt_1');
      const source = FakeEventSource.latest;
      rerender({ sessionID: 'ags_1', turn: { id: 'agt_1', status } });
      expect(result.current.retryingTurnID).toBeUndefined();
      emitProgressEvent('turn.retrying');
      expect(result.current.retryingTurnID).toBeUndefined();
      expect(FakeEventSource.latest).toBe(source);
      unmount();
      client.clear();
    },
  );

  it('clears a retry immediately on terminal SSE and rejects its late replay despite a stale snapshot', () => {
    const { result, unmount, client } = renderProgressEvents();
    emitProgressEvent('turn.retrying');
    emitProgressEvent('turn.completed', { turn_status: 'failed' });
    expect(result.current.retryingTurnID).toBeUndefined();
    emitProgressEvent('turn.retrying');
    expect(result.current.retryingTurnID).toBeUndefined();
    unmount();
    client.clear();
  });

  it('isolates a newer turn without reconnecting and ignores previous-session callbacks after cleanup', () => {
    const { result, rerender, unmount, client } = renderProgressEvents();
    emitProgressEvent('turn.retrying');
    const source = FakeEventSource.latest!;
    const lateRetry = source.listener('turn.retrying')!;
    const lateOpen = source.onopen!;
    rerender({ sessionID: 'ags_1', turn: { id: 'agt_2', status: 'running' } });
    expect(result.current.retryingTurnID).toBeUndefined();
    expect(FakeEventSource.latest).toBe(source);
    emitProgressEvent('turn.retrying', {}, 'agt_2');
    emitProgressEvent('turn.retrying', {}, 'agt_1');
    expect(result.current.retryingTurnID).toBe('agt_2');
    rerender({ sessionID: 'ags_2', turn: { id: 'agt_3', status: 'running' } });
    emitProgressEvent('turn.retrying', {}, 'agt_3', 'ags_2');
    act(() => {
      lateRetry(
        new MessageEvent('message', {
          data: JSON.stringify({ type: 'turn.retrying', session_id: 'ags_1', turn_id: 'agt_1' }),
        }),
      );
      lateOpen();
    });
    expect(result.current.retryingTurnID).toBe('agt_3');
    expect(result.current.connected).toBe(false);
    unmount();
    client.clear();
  });

  it('tracks turn-start events before a snapshot is available and rejects an authoritative idle snapshot', () => {
    const { result, rerender, unmount, client } = renderProgressEvents();
    rerender({ sessionID: 'ags_1', turn: undefined });
    emitProgressEvent('turn.started');
    emitProgressEvent('turn.retrying');
    expect(result.current.retryingTurnID).toBe('agt_1');
    emitProgressEvent('turn.started', {}, 'agt_2');
    expect(result.current.retryingTurnID).toBeUndefined();
    emitProgressEvent('turn.retrying', {}, 'agt_1');
    expect(result.current.retryingTurnID).toBeUndefined();
    rerender({ sessionID: 'ags_1', turn: null });
    emitProgressEvent('turn.retrying', {}, 'agt_2');
    expect(result.current.retryingTurnID).toBeUndefined();
    unmount();
    client.clear();
  });

  it.each(['turn.queued', 'turn.started'])(
    'refreshes durable user messages and the session as soon as %s arrives',
    async (type) => {
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const keys = [
        ['agent-session', 'ags_1'],
        ['agent-conversation', 'ags_1'],
      ] as const;
      keys.forEach((key) => client.setQueryData(key, { cached: true }));
      client.setQueryData(['agent-conversation', 'ags_other'], { cached: true });
      const wrapper = ({ children }: PropsWithChildren) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      );
      const { unmount } = renderHook(() => useAgentEvents('ags_1', 'epoch:1'), { wrapper });
      act(() => FakeEventSource.latest!.emit(type, { type, session_id: 'ags_1' }));
      await waitFor(() => {
        keys.forEach((key) => expect(client.getQueryState(key)?.isInvalidated).toBe(true));
      });
      expect(client.getQueryState(['agent-conversation', 'ags_other'])?.isInvalidated).toBe(false);
      unmount();
      client.clear();
    },
  );

  it('invalidates project content immediately after an applied ChangeSet', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const keys = [
      ['storyboard', 'prj_1', 'ep_1'],
      ['episode', 'prj_1', 'ep_1'],
      ['assets', 'prj_1', 'ep_1'],
    ] as const;
    keys.forEach((key) => client.setQueryData(key, { cached: true }));
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );

    renderHook(() => useAgentEvents('ags_1', 'epoch:1'), { wrapper });
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    act(() => {
      FakeEventSource.latest!.emit('change_set.applied', {
        type: 'change_set.applied',
        session_id: 'ags_1',
        change_set_id: 'cst_1',
      });
    });

    await waitFor(() => {
      keys.forEach((key) => expect(client.getQueryState(key)?.isInvalidated).toBe(true));
    });
  });
});

function renderProgressEvents(
  turn: { id: string; status: string } | null | undefined = { id: 'agt_1', status: 'running' },
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const initialProps: {
    sessionID: string;
    turn: { id: string; status: string } | null | undefined;
  } = { sessionID: 'ags_1', turn };
  const view = renderHook(
    ({ sessionID, turn }: { sessionID: string; turn: { id: string; status: string } | null | undefined }) =>
      useAgentEvents(sessionID, 'epoch:1', turn),
    { wrapper, initialProps },
  );
  return { ...view, client };
}

function emitProgressEvent(
  type: string,
  extra: Record<string, string> = {},
  turnID = 'agt_1',
  sessionID = 'ags_1',
) {
  act(() =>
    FakeEventSource.latest!.emit(type, {
      type,
      session_id: sessionID,
      turn_id: turnID,
      occurred_at: '2026-10-18T08:00:00Z',
      ...extra,
    }),
  );
}
