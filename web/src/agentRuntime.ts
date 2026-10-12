import { QueryObserver, queryOptions, type QueryClient } from '@tanstack/react-query';
import { getRuntime, selectAgentRuntime, type AgentRuntimeKind, type RuntimeResponse } from './api/runtime';

const selections = new WeakMap<QueryClient, number>();
export const agentRuntimeKey = (kind?: AgentRuntimeKind) =>
  kind ? (['agent-runtime', 'service', kind] as const) : (['agent-runtime'] as const);

export function cacheAgentRuntime(client: QueryClient, result: RuntimeResponse, defaultQuery = false) {
  const target = result.target_runtime_kind ?? result.active_runtime_kind;
  const active = client.getQueryData<RuntimeResponse>(agentRuntimeKey())?.active_runtime_kind;
  if (
    defaultQuery ||
    !result.target_runtime_kind ||
    (target === result.active_runtime_kind && (!active || target === active))
  )
    client.setQueryData(agentRuntimeKey(), result);
  else if (active) result = { ...result, active_runtime_kind: active };
  if (target)
    client.setQueryData(agentRuntimeKey(target), {
      ...result,
      target_runtime_kind: target,
      runtime: result.runtimes?.[target] ?? result.runtime,
      models: result.runtime_models?.[target] ?? result.models,
    });
  return result;
}

export function agentRuntimeOptions(client: QueryClient, kind?: AgentRuntimeKind) {
  const key = agentRuntimeKey(kind);
  return queryOptions({
    queryKey: key,
    queryFn: async ({ signal }) => {
      const owner = client.getQueryCache().find({ queryKey: key, exact: true });
      const selection = selections.get(client);
      const fetched = await getRuntime({ ...(kind ? { runtimeKind: kind } : {}), signal });
      const result = kind ? { ...fetched, target_runtime_kind: kind } : fetched;
      if (client.getQueryCache().find({ queryKey: key, exact: true }) !== owner) return result;
      if (selections.get(client) !== selection) {
        const current = client.getQueryData<RuntimeResponse>(agentRuntimeKey());
        if (!kind && current) return current;
        return current?.active_runtime_kind
          ? { ...result, active_runtime_kind: current.active_runtime_kind }
          : result;
      }
      return cacheAgentRuntime(client, result, !kind);
    },
    retry: false,
    refetchOnWindowFocus: 'always',
    refetchOnMount: 'always',
    staleTime: 30_000,
    refetchInterval: (query) => (query.state.data?.runtime.health === 'starting' ? 1_000 : false),
  });
}

export function warmAgentRuntime(client: QueryClient) {
  const observer = new QueryObserver(client, agentRuntimeOptions(client));
  return observer.subscribe(() => {});
}

export async function changeAgentRuntime(client: QueryClient, kind: AgentRuntimeKind) {
  const selection = (selections.get(client) ?? 0) + 1;
  selections.set(client, selection);
  const owner = client.getQueryCache().find({ queryKey: agentRuntimeKey(kind), exact: true });
  const result = await selectAgentRuntime(kind);
  if (
    owner &&
    selections.get(client) === selection &&
    client.getQueryCache().find({ queryKey: agentRuntimeKey(kind), exact: true }) === owner
  ) {
    client.setQueryData(agentRuntimeKey(), result);
    cacheAgentRuntime(client, result);
    for (const service of ['user_codex', 'shotforward'] as const) {
      client.setQueryData<RuntimeResponse>(agentRuntimeKey(service), (current) =>
        current && result.active_runtime_kind
          ? { ...current, active_runtime_kind: result.active_runtime_kind }
          : current,
      );
    }
  }
  return result;
}

export async function refreshAgentRuntime(client: QueryClient, kind: AgentRuntimeKind) {
  const key = agentRuntimeKey(kind);
  const owner = client.getQueryCache().find({ queryKey: key, exact: true });
  const selection = selections.get(client);
  const result = { ...(await getRuntime({ runtimeKind: kind, refresh: true })), target_runtime_kind: kind };
  if (owner && client.getQueryCache().find({ queryKey: key, exact: true }) === owner) {
    if (selections.get(client) === selection) cacheAgentRuntime(client, result);
    else {
      const active = client.getQueryData<RuntimeResponse>(agentRuntimeKey())?.active_runtime_kind;
      client.setQueryData(key, active ? { ...result, active_runtime_kind: active } : result);
    }
  }
  return result;
}
