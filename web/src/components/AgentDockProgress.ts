import { useEffect, useState } from 'react';
import type { AgentTurn } from '../api/agent';

export function useAgentTurnElapsed(sessionID: string | undefined, turn: AgentTurn | undefined) {
  const timestamp = turn?.started_at || turn?.created_at;
  const startedAt = timestamp ? Date.parse(timestamp) : Number.NaN;
  const live = Boolean(
    sessionID && turn && !['succeeded', 'completed', 'failed', 'interrupted'].includes(turn.status),
  );
  const key = live && Number.isFinite(startedAt) ? `${sessionID}/${turn!.id}/${startedAt}` : '';
  const [clock, setClock] = useState({ key, now: Date.now() });
  useEffect(() => {
    if (!key) return;
    const tick = () => setClock({ key, now: Date.now() });
    tick();
    const interval = window.setInterval(tick, 1_000);
    return () => window.clearInterval(interval);
  }, [key]);
  return key
    ? Math.max(0, Math.floor(((clock.key === key ? clock.now : Date.now()) - startedAt) / 1_000))
    : undefined;
}

export function formatAgentElapsed(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  const remaining = String(seconds % 60).padStart(2, '0');
  return `${minutes}:${remaining}`;
}
