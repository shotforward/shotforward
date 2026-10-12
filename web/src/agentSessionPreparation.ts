import type { QueryClient } from '@tanstack/react-query';
import { createSession, type AgentSession } from './api/agent';
import { APIError, createOperationKey } from './api/client';
import type { AgentRuntimeKind, RuntimeResponse } from './api/runtime';
import { agentRuntimeKey } from './agentRuntime';

export interface AgentSessionPreparation {
  operationID: string;
  status: 'preparing' | 'ready' | 'failed';
  runtimeKind: AgentRuntimeKind;
  modelID: string;
  session?: AgentSession;
  error?: unknown;
}

export const sessionPreparationKey = (projectID: string, episodeID: string, runtimeKind: AgentRuntimeKind) =>
  ['agent-session-preparation', projectID, episodeID, runtimeKind] as const;

const requests = new WeakMap<
  QueryClient,
  Map<string, { operationID: string; promise: Promise<AgentSession> }>
>();

export function prepareAgentSession(
  client: QueryClient,
  projectID: string,
  episodeID: string,
  modelID: string,
  runtimeKind: AgentRuntimeKind,
) {
  const runtime =
    client.getQueryData<RuntimeResponse>(agentRuntimeKey(runtimeKind)) ??
    client.getQueryData<RuntimeResponse>(agentRuntimeKey());
  const snapshot =
    runtime?.runtimes?.[runtimeKind] ??
    ((runtime?.target_runtime_kind ?? runtime?.active_runtime_kind ?? 'user_codex') === runtimeKind
      ? runtime?.runtime
      : undefined);
  if (snapshot && snapshot.health !== 'ready')
    return Promise.reject(
      new APIError(409, { error: { code: 'runtime_unavailable', message: 'Runtime unavailable' } }),
    );
  const key = sessionPreparationKey(projectID, episodeID, runtimeKind);
  const scope = JSON.stringify(key);
  let pending = requests.get(client);
  if (!pending) {
    pending = new Map();
    requests.set(client, pending);
  }
  const existing = pending.get(scope);
  if (existing && client.getQueryData<AgentSessionPreparation>(key)?.operationID === existing.operationID)
    return existing.promise;
  const operationID = createOperationKey();
  const state: AgentSessionPreparation = { operationID, runtimeKind, modelID, status: 'preparing' };
  client.setQueryData(key, state);
  const current = () => client.getQueryData<AgentSessionPreparation>(key)?.operationID === operationID;
  const promise = createSession(projectID, episodeID, modelID, runtimeKind)
    .then(async ({ session }) => {
      if (!current()) return session;
      await client.cancelQueries({ queryKey: ['agent-sessions', projectID, episodeID], exact: true });
      if (!current()) return session;
      client.setQueryData<{ sessions: AgentSession[] }>(
        ['agent-sessions', projectID, episodeID],
        (value) => ({
          sessions: [session, ...(value?.sessions ?? []).filter((item) => item.id !== session.id)],
        }),
      );
      // Only history is seeded. The snapshot must capture a real server SSE cursor.
      client.setQueryData(['agent-conversation', session.id], {
        pages: [{ turns: [], next_cursor: '' }],
        pageParams: [''],
      });
      client.setQueryData(key, { ...state, status: 'ready', session });
      return session;
    })
    .catch((error: unknown) => {
      if (current()) client.setQueryData(key, { ...state, status: 'failed', error });
      throw error;
    })
    .finally(() => {
      if (pending.get(scope)?.operationID === operationID) pending.delete(scope);
    });
  pending.set(scope, { operationID, promise });
  return promise;
}

export function clearSessionPreparation(
  client: QueryClient,
  projectID: string,
  episodeID: string,
  runtimeKind: AgentRuntimeKind,
) {
  client.setQueryData(sessionPreparationKey(projectID, episodeID, runtimeKind), null);
}
