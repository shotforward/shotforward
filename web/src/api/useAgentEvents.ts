import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { AgentEvent } from './agent';

const eventTypes = [
  'turn.queued',
  'turn.started',
  'turn.waiting_user',
  'assistant.delta',
  'tool.started',
  'tool.completed',
  'change_set.pending',
  'change_set.applied',
  'turn.completed',
  'runtime.error',
  'session.resync_required',
];

export function useAgentEvents(sessionID: string | undefined, cursor: string | undefined) {
  const queryClient = useQueryClient();
  const [assistant, setAssistant] = useState({ turnID: '', text: '' });
  const [activity, setActivity] = useState('');
  const [toolFailure, setToolFailure] = useState<{ sessionID: string; code: string }>();
  const [connected, setConnected] = useState(false);
  const streamKey = sessionID && cursor ? sessionID : undefined;

  useEffect(() => {
    setAssistant({ turnID: '', text: '' });
    setActivity('');
    if (!streamKey || !cursor) return;
    const source = new EventSource(
      `/api/v1/agent-sessions/${encodeURIComponent(streamKey)}/events?after=${encodeURIComponent(cursor)}`,
    );
    source.onopen = () => setConnected(true);
    source.onerror = () => setConnected(false);
    const receive = (raw: Event) => {
      let event: AgentEvent;
      try {
        event = JSON.parse((raw as MessageEvent<string>).data) as AgentEvent;
      } catch {
        void queryClient.invalidateQueries({ queryKey: ['agent-session', streamKey] });
        return;
      }
      if (event.type === 'assistant.delta' && event.text_delta)
        setAssistant((value) => ({
          turnID: event.turn_id ?? value.turnID,
          text:
            value.turnID && value.turnID !== event.turn_id
              ? event.text_delta!
              : value.text + event.text_delta,
        }));
      if (event.type === 'tool.started') setActivity(event.tool_name ?? '');
      if (event.type === 'tool.completed') setActivity('');
      if (event.type === 'turn.started') setToolFailure(undefined);
      if (event.type === 'tool.completed' && event.failure_code)
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
      eventTypes.forEach((type) => source.removeEventListener(type, receive));
      source.close();
      setConnected(false);
    };
  }, [queryClient, streamKey]);

  return {
    assistantText: assistant.text,
    assistantTurnID: assistant.turnID,
    activity,
    toolFailureCode: toolFailure?.sessionID === sessionID ? toolFailure?.code : undefined,
    connected,
    clearAssistantText: () => setAssistant({ turnID: '', text: '' }),
  };
}
