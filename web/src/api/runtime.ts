import { requestJSON } from './client';

export interface RuntimeModel {
  id: string;
  display_name: string;
  runtime_kind: 'user_codex' | 'shotforward';
}

export type AgentRuntimeKind = 'user_codex' | 'shotforward';

export interface RuntimeSnapshot {
  health:
    | 'not_installed'
    | 'login_required'
    | 'starting'
    | 'ready'
    | 'incompatible'
    | 'configuration_error'
    | 'disconnected';
  version: string;
  minimum_version: string;
  failure_code: string;
  failure_summary: string;
  safe_execution_available?: boolean;
  safe_execution_backend?: string;
  safe_execution_failure_code?: string;
  safe_execution_failure_summary?: string;
}

export interface RuntimeResponse {
  active_runtime_kind?: AgentRuntimeKind;
  target_runtime_kind?: AgentRuntimeKind;
  runtime: RuntimeSnapshot;
  runtimes?: Partial<Record<AgentRuntimeKind, RuntimeSnapshot>>;
  models: RuntimeModel[];
  runtime_models?: Partial<Record<AgentRuntimeKind, RuntimeModel[]>>;
  network?: {
    proxy_url: string;
  };
  request_id: string;
}

export interface RuntimeRequestOptions {
  runtimeKind?: AgentRuntimeKind;
  refresh?: boolean;
  signal?: AbortSignal;
}

const runtimeRequestTimeoutMilliseconds = 35_000;

export function getRuntime(options: RuntimeRequestOptions = {}): Promise<RuntimeResponse> {
  const query = new URLSearchParams();
  if (options.runtimeKind) query.set('runtime_kind', options.runtimeKind);
  if (options.refresh) query.set('refresh', 'true');
  const suffix = query.size ? `?${query.toString()}` : '';
  return runtimeRequest(`/api/v1/agent/runtime${suffix}`, {}, options.signal);
}

export function selectAgentRuntime(runtimeKind: AgentRuntimeKind): Promise<RuntimeResponse> {
  return runtimeRequest('/api/v1/agent/runtime', {
    method: 'PUT',
    body: JSON.stringify({ runtime_kind: runtimeKind }),
  });
}

async function runtimeRequest(
  path: string,
  init: RequestInit,
  parentSignal?: AbortSignal,
): Promise<RuntimeResponse> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (parentSignal?.aborted) controller.abort();
  else parentSignal?.addEventListener('abort', abort, { once: true });
  const timer = globalThis.setTimeout(abort, runtimeRequestTimeoutMilliseconds);
  try {
    return await requestJSON(path, { ...init, signal: controller.signal });
  } finally {
    globalThis.clearTimeout(timer);
    parentSignal?.removeEventListener('abort', abort);
  }
}

export function getRuntimeNetwork(): Promise<{ network: { proxy_url: string } }> {
  return requestJSON('/api/v1/agent/runtime/network');
}

export function configureRuntimeNetwork(proxyURL: string): Promise<{ network: { proxy_url: string } }> {
  return requestJSON('/api/v1/agent/runtime/network', {
    method: 'PUT',
    body: JSON.stringify({ proxy_url: proxyURL.trim() }),
  });
}
