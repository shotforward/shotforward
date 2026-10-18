import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { AgentEvent, AgentTurn } from './agent';

const eventTypes = [
  'turn.queued',
  'turn.started',
  'turn.waiting_user',
  'turn.retrying',
  'assistant.delta',
  'tool.started',
  'tool.completed',
  'change_set.pending',
  'change_set.applied',
  'turn.completed',
  'runtime.error',
  'session.resync_required',
];

type EventTurn = Pick<AgentTurn, 'id' | 'status'>;

export function useAgentEvents(
  sessionID: string | undefined,
  cursor: string | undefined,
  // null is an authoritative idle snapshot; undefined means not loaded yet.
  currentTurn?: EventTurn | null,
) {
  const queryClient = useQueryClient();
  const [assistant, setAssistant] = useState({ turnID: '', text: '' });
  const [activity, setActivity] = useState<{ sessionID: string; turnID: string; name: string }>();
  const [retrying, setRetrying] = useState<{ sessionID: string; turnID: string }>();
  const [toolFailure, setToolFailure] = useState<{ sessionID: string; code: string }>();
  const [connected, setConnected] = useState(false);
  const streamKey = sessionID && cursor ? sessionID : undefined;
  const view = useRef({ sessionID, currentTurn });
  view.current = { sessionID, currentTurn };
  const eventTurn = useRef<{ sessionID: string; turnID: string } | undefined>(undefined);
  const terminalTurns = useRef({ sessionID, ids: new Set<string>() });
  if (terminalTurns.current.sessionID !== sessionID) {
    terminalTurns.current = { sessionID, ids: new Set<string>() };
    eventTurn.current = undefined;
  }

  function isCurrentTurn(turnID: string | undefined) {
    const { sessionID: currentSessionID, currentTurn: authority } = view.current;
    if (!turnID || authority === null || terminalTurns.current.ids.has(turnID)) return false;
    if (authority) {
      return (
        authority.id === turnID &&
        !['succeeded', 'completed', 'failed', 'interrupted'].includes(authority.status)
      );
    }
    const fallback = eventTurn.current;
    return Boolean(fallback && fallback.sessionID === currentSessionID && fallback.turnID === turnID);
  }

  useEffect(() => {
    setAssistant({ turnID: '', text: '' });
    setActivity(undefined);
    setRetrying(undefined);
    if (!streamKey || !cursor) return;
    const source = new EventSource(
      `/api/v1/agent-sessions/${encodeURIComponent(streamKey)}/events?after=${encodeURIComponent(cursor)}`,
    );
    let active = true;
    source.onopen = () => {
      if (active && view.current.sessionID === streamKey) setConnected(true);
    };
    source.onerror = () => {
      if (active && view.current.sessionID === streamKey) setConnected(false);
    };
    const receive = (raw: Event) => {
      if (!active || view.current.sessionID !== streamKey) return;
      let event: AgentEvent;
      try {
        event = JSON.parse((raw as MessageEvent<string>).data) as AgentEvent;
      } catch {
        void queryClient.invalidateQueries({ queryKey: ['agent-session', streamKey] });
        return;
      }
      if (event.session_id !== streamKey) return;
      const authority = view.current.currentTurn;
      if (event.type === 'turn.started' && event.turn_id && authority === undefined) {
        eventTurn.current = { sessionID: streamKey, turnID: event.turn_id };
      }
      const matchesCurrentTurn = isCurrentTurn(event.turn_id);
      if (event.type === 'turn.retrying' && matchesCurrentTurn) {
        setRetrying({ sessionID: streamKey, turnID: event.turn_id! });
        setActivity(undefined);
      }
      if (
        matchesCurrentTurn &&
        (event.type === 'turn.started' ||
          (event.type === 'assistant.delta' && event.text_delta) ||
          ((event.type === 'tool.started' || event.type === 'tool.completed') && event.tool_name))
      ) {
        setRetrying(undefined);
      }
      if (event.type === 'assistant.delta' && event.text_delta)
        setAssistant((value) => ({
          turnID: event.turn_id ?? value.turnID,
          text:
            value.turnID && value.turnID !== event.turn_id
              ? event.text_delta!
              : value.text + event.text_delta,
        }));
      if (event.type === 'tool.started' && matchesCurrentTurn)
        setActivity({ sessionID: streamKey, turnID: event.turn_id!, name: event.tool_name ?? '' });
      if (event.type === 'tool.completed' && matchesCurrentTurn) setActivity(undefined);
      if (event.type === 'turn.started' && matchesCurrentTurn) {
        setActivity(undefined);
        setToolFailure(undefined);
      }
      if (event.type === 'turn.completed' && event.turn_id) {
        terminalTurns.current.ids.add(event.turn_id);
        setRetrying((value) => (value?.turnID === event.turn_id ? undefined : value));
        setActivity((value) => (value?.turnID === event.turn_id ? undefined : value));
      }
      if (event.type === 'tool.completed' && matchesCurrentTurn && event.failure_code)
        setToolFailure({ sessionID: streamKey, code: event.failure_code });
      if (
        event.type === 'turn.queued' ||
        event.type === 'turn.started' ||
        event.type === 'turn.completed' ||
        event.type === 'change_set.pending' ||
        event.type === 'change_set.applied' ||
        event.type === 'session.resync_required'
      ) {
        void queryClient.invalidateQueries({ queryKey: ['agent-session', streamKey] });
        void queryClient.invalidateQueries({ queryKey: ['agent-conversation', streamKey] });
      }
      if (event.type === 'change_set.applied') {
        // A ChangeSet may update Storyboard, Episode script, or Assets.  The
        // Agent stream is independent from those React Query caches, so make
        // the durable project write visible as soon as each semantic unit is
        // applied (not only after the whole Turn finishes).
        void queryClient.invalidateQueries({ queryKey: ['storyboard'] });
        void queryClient.invalidateQueries({ queryKey: ['episode'] });
        void queryClient.invalidateQueries({ queryKey: ['assets'] });
      }
    };
    eventTypes.forEach((type) => source.addEventListener(type, receive));
    return () => {
      active = false;
      eventTypes.forEach((type) => source.removeEventListener(type, receive));
      source.close();
      setConnected(false);
    };
  }, [queryClient, streamKey]);

  return {
    assistantText: assistant.text,
    assistantTurnID: assistant.turnID,
    activity:
      activity && activity.sessionID === sessionID && isCurrentTurn(activity.turnID) ? activity.name : '',
    retryingTurnID:
      retrying && retrying.sessionID === sessionID && isCurrentTurn(retrying.turnID)
        ? retrying.turnID
        : undefined,
    toolFailureCode: toolFailure?.sessionID === sessionID ? toolFailure?.code : undefined,
    connected,
    clearAssistantText: () => setAssistant({ turnID: '', text: '' }),
  };
}
