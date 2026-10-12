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
}

describe('useAgentEvents', () => {
  it('retains tool business errors through turn completion, clears on a new turn and isolates sessions', () => {
    const client = new QueryClient();
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, rerender, unmount } = renderHook(({ id }) => useAgentEvents(id, 'epoch:1'), {
      wrapper,
      initialProps: { id: 'ags_1' },
    });
    act(() =>
      FakeEventSource.latest!.emit('tool.completed', {
        type: 'tool.completed',
        failure_code: 'shotforward_insufficient_points',
      }),
    );
    act(() => FakeEventSource.latest!.emit('turn.completed', { type: 'turn.completed' }));
    expect(result.current.toolFailureCode).toBe('shotforward_insufficient_points');
    act(() => FakeEventSource.latest!.emit('turn.started', { type: 'turn.started' }));
    expect(result.current.toolFailureCode).toBeUndefined();
    act(() =>
      FakeEventSource.latest!.emit('tool.completed', {
        type: 'tool.completed',
        failure_code: 'shotforward_insufficient_points',
      }),
    );
    rerender({ id: 'ags_2' });
    expect(result.current.toolFailureCode).toBeUndefined();
    unmount();
    client.clear();
  });
  beforeEach(() => {
    FakeEventSource.latest = undefined;
    vi.stubGlobal('EventSource', FakeEventSource);
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
