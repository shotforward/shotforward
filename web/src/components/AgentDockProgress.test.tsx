import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentTurn } from '../api/agent';
import { formatAgentElapsed, useAgentTurnElapsed } from './AgentDockProgress';

describe('Agent turn elapsed time', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-10-18T08:00:00Z'));
  });
  afterEach(() => vi.useRealTimers());

  it('uses the actual started_at timestamp, ticks only while live, and cleans up at completion', () => {
    const turn = liveTurn();
    const { result, rerender, unmount } = renderHook(({ turn }) => useAgentTurnElapsed('ags_1', turn), {
      initialProps: { turn },
    });
    expect(result.current).toBe(65);
    expect(vi.getTimerCount()).toBe(1);
    act(() => vi.advanceTimersByTime(2_000));
    expect(result.current).toBe(67);
    rerender({ turn: { ...turn, status: 'completed' } });
    expect(result.current).toBeUndefined();
    expect(vi.getTimerCount()).toBe(0);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('falls back to created_at for older daemons without inventing a local start time', () => {
    const { started_at: _startedAt, ...turn } = liveTurn();
    const { result, unmount } = renderHook(() => useAgentTurnElapsed('ags_1', turn));
    expect(result.current).toBe(600);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('never carries elapsed state across sessions and removes its timer on unmount', () => {
    const { result, rerender, unmount } = renderHook(
      ({ sessionID, turn }) => useAgentTurnElapsed(sessionID, turn),
      { initialProps: { sessionID: 'ags_1', turn: liveTurn() } },
    );
    expect(result.current).toBe(65);
    rerender({
      sessionID: 'ags_2',
      turn: { ...liveTurn(), id: 'agt_2', agent_session_id: 'ags_2', started_at: '2026-10-18T07:59:55Z' },
    });
    expect(result.current).toBe(5);
    expect(vi.getTimerCount()).toBe(1);
    act(() => vi.advanceTimersByTime(1_000));
    expect(result.current).toBe(6);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not start timers for invalid timestamps, terminal turns, or missing turns', () => {
    const initialProps: { turn: AgentTurn | undefined } = {
      turn: { ...liveTurn(), started_at: 'invalid', created_at: 'invalid' },
    };
    const { result, rerender, unmount } = renderHook(
      ({ turn }: { turn: AgentTurn | undefined }) => useAgentTurnElapsed('ags_1', turn),
      { initialProps },
    );
    expect(result.current).toBeUndefined();
    expect(vi.getTimerCount()).toBe(0);
    rerender({ turn: { ...liveTurn(), status: 'failed' } });
    expect(result.current).toBeUndefined();
    expect(vi.getTimerCount()).toBe(0);
    rerender({ turn: undefined });
    expect(result.current).toBeUndefined();
    expect(vi.getTimerCount()).toBe(0);
    unmount();
  });

  it.each([
    [0, '0:00'],
    [59, '0:59'],
    [65, '1:05'],
    [3601, '60:01'],
  ])('formats %s actual seconds as %s', (seconds, formatted) => {
    expect(formatAgentElapsed(Number(seconds))).toBe(formatted);
  });
});

function liveTurn(): AgentTurn {
  return {
    id: 'agt_1',
    agent_session_id: 'ags_1',
    model_id: 'gpt-test',
    status: 'running',
    created_at: '2026-10-18T07:50:00Z',
    started_at: '2026-10-18T07:58:55Z',
  };
}
