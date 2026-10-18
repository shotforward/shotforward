import {
  agentWebSearchEnabled,
  preferredAgentModelForRuntime,
  setPreferredAgentModelForRuntime,
} from '../preferences';
import { createOperationKey, requestJSON } from './client';
import { getRuntime } from './runtime';
import type { GenerationMode } from './storyboard';

export interface AgentSession {
  id: string;
  project_id: string;
  episode_id: string;
  model_id: string;
  runtime_kind: 'user_codex' | 'shotforward';
  title: string;
  status: 'active' | 'history_unavailable' | 'archived';
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AgentTurn {
  id: string;
  agent_session_id: string;
  model_id: string;
  status: string;
  failure?: { code: string; summary: string };
  created_at: string;
  started_at?: string;
  completed_at?: string;
}

export interface AgentFocus {
  scene_id?: string;
  clip_id?: string;
  asset_id?: string;
}

export interface AgentVideoPromptFocusTarget {
  clip_id: string;
  generation_mode: GenerationMode;
  generator_id: string;
  provider_model_id: string;
}

export interface AgentVideoPromptTarget {
  generator_id: string;
  provider_model_id: string;
  keyframe_provider_model_id?: string;
  focus?: AgentVideoPromptFocusTarget;
}

export interface ChangeSetSummary {
  id: string;
  project_id: string;
  episode_id: string;
  agent_session_id: string;
  agent_turn_id: string;
  title: string;
  summary: string;
  status: string;
  operation_count: number;
  revision: number;
  reverts_change_set_id?: string;
  reverted_by_change_set_id?: string;
}

export interface ChangeSetOperation {
  id: string;
  position: number;
  type: string;
  target_kind: string;
  target_id: string;
  asset_prompt?: { asset_id: string; before: string; after: string };
  clip_prompts?: {
    clip_id: string;
    first_before: string;
    first_after: string;
    last_before: string;
    last_after: string;
    video_mode: GenerationMode;
    video_before: string;
    video_after: string;
    asset_ids_before?: string[];
    asset_ids_after?: string[];
    reference_artifact_ids_before?: string[];
    reference_artifact_ids_after?: string[];
  };
  asset_creation?: { asset_id: string; kind: string; name: string; prompt: string };
  asset_restoration?: { asset_id: string };
  scene_creation?: { scene_id: string; title: string; description: string };
  clip_creation?: {
    clip_id: string;
    title: string;
    description: string;
    dialogue: string;
    creative_intent: string;
  };
  clip_merge?: {
    source_clip_ids: string[];
    replacement_clip_id: string;
    title: string;
    planned_duration_seconds: number;
    asset_ids: string[];
    inherited_first_frame: boolean;
    inherited_last_frame: boolean;
  };
  clip_split?: {
    source_clip_id: string;
    source_title: string;
    source_planned_duration_seconds: number;
    replacements: Array<{ clip_id: string; title: string; planned_duration_seconds: number }>;
    inherited_first_frame: boolean;
    inherited_last_frame: boolean;
  };
  episode_script?: { episode_id: string; before: string; after: string };
  change_set_revert?: { original_change_set_id: string };
}

export interface SessionSnapshot {
  session: AgentSession;
  turns: AgentTurn[];
  change_sets: ChangeSetSummary[];
  cursor: string;
  context_compacting?: boolean;
}

export interface AgentConversationMessage {
  id: string;
  client_operation_id?: string;
  role: 'user' | 'assistant';
  phase?: 'commentary' | 'final_answer';
  text: string;
}

export interface AgentConversationMedia {
  artifact_id: string;
  name: string;
  origin: 'uploaded' | 'asset' | 'generated';
  mime_type: string;
  original_filename: string;
  byte_size: number;
  width: number;
  height: number;
  created_at: string;
}

export interface AgentMediaReference {
  artifact_id: string;
  name: string;
  origin: 'uploaded' | 'asset' | 'generated';
}

export interface AgentConversationTurn {
  turn: AgentTurn;
  messages: AgentConversationMessage[];
  media: AgentConversationMedia[];
}

export interface AgentConversationPage {
  turns: AgentConversationTurn[];
  next_cursor: string;
}

export type AgentEventType =
  | 'turn.queued'
  | 'turn.started'
  | 'turn.waiting_user'
  | 'turn.retrying'
  | 'assistant.delta'
  | 'tool.started'
  | 'tool.completed'
  | 'change_set.pending'
  | 'change_set.applied'
  | 'turn.completed'
  | 'runtime.error'
  | 'session.resync_required';

export interface AgentEvent {
  type: AgentEventType;
  session_id: string;
  turn_id?: string;
  change_set_id?: string;
  text_delta?: string;
  tool_name?: string;
  turn_status?: string;
  failure_code?: string;
  summary?: string;
  occurred_at: string;
}

export function listSessions(projectID: string, episodeID: string): Promise<{ sessions: AgentSession[] }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/agent-sessions`,
  );
}

export function createSession(
  projectID: string,
  episodeID: string,
  modelID: string,
  runtimeKind: 'user_codex' | 'shotforward' = 'user_codex',
): Promise<{ session: AgentSession }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/agent-sessions`,
    {
      method: 'POST',
      body: JSON.stringify({
        model_id: modelID,
        runtime_kind: runtimeKind,
        web_search_disabled: !agentWebSearchEnabled(),
      }),
    },
  );
}

export function getSessionSnapshot(sessionID: string): Promise<SessionSnapshot> {
  return requestJSON(`/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/snapshot`);
}

export function compactSessionContext(sessionID: string): Promise<{ context_compacted: true }> {
  return requestJSON(`/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/compact`, {
    method: 'POST',
    body: JSON.stringify({}),
  });
}

export function getSessionMessages(
  sessionID: string,
  cursor = '',
  limit = 20,
): Promise<AgentConversationPage> {
  const query = new URLSearchParams({ limit: String(limit) });
  if (cursor) query.set('cursor', cursor);
  return requestJSON(`/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/messages?${query.toString()}`);
}

export function submitAgentInput(
  sessionID: string,
  text: string,
  modelID: string,
  focus: AgentFocus = {},
  attachments: AgentMediaReference[] = [],
  clientOperationID = createOperationKey(),
  videoPromptTarget?: AgentVideoPromptTarget,
): Promise<{ action: string; turn: AgentTurn }> {
  const body = { client_operation_id: clientOperationID, model_id: modelID, text, focus } as {
    client_operation_id: string;
    model_id: string;
    text: string;
    focus: AgentFocus;
    attachments?: AgentMediaReference[];
    video_prompt_target?: AgentVideoPromptTarget;
  };
  if (attachments.length > 0) body.attachments = attachments;
  if (videoPromptTarget) body.video_prompt_target = videoPromptTarget;
  return requestJSON(`/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/inputs`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export function uploadAgentMedia(
  sessionID: string,
  clientOperationID: string,
  file: File,
): Promise<{ media: AgentConversationMedia }> {
  const body = new FormData();
  body.set('client_operation_id', clientOperationID);
  body.set('file', file);
  return requestJSON(`/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/media`, {
    method: 'POST',
    body,
  });
}

export function interruptAgentTurn(sessionID: string, turnID: string): Promise<{ turn: AgentTurn }> {
  return requestJSON(
    `/api/v1/agent-sessions/${encodeURIComponent(sessionID)}/turns/${encodeURIComponent(turnID)}:interrupt`,
    {
      method: 'POST',
      body: JSON.stringify({}),
    },
  );
}

export async function startAgentWorkflow(
  projectID: string,
  episodeID: string,
  text: string,
  focus: AgentFocus = {},
  videoPromptTarget?: AgentVideoPromptTarget,
): Promise<{ session: AgentSession; turn: AgentTurn }> {
  const listed = await listSessions(projectID, episodeID);
  let session = listed.sessions.find((item) => item.status === 'active');
  if (!session) {
    const runtime = await getRuntime();
    const runtimeKind = runtime.active_runtime_kind ?? 'user_codex';
    const preferred = preferredAgentModelForRuntime(runtimeKind);
    const selected =
      runtime.models.find((item) => item.runtime_kind === runtimeKind && item.id === preferred) ??
      runtime.models[0];
    const modelID = selected?.id;
    if (runtime.runtime.health !== 'ready' || !modelID) throw new Error('Agent runtime is not ready');
    if (!selected) throw new Error('Agent runtime is not ready');
    session = (await createSession(projectID, episodeID, modelID, selected.runtime_kind)).session;
    setPreferredAgentModelForRuntime(selected.runtime_kind, modelID);
  }
  const { turn } = await submitAgentInput(
    session.id,
    text,
    session.model_id,
    focus,
    [],
    createOperationKey(),
    videoPromptTarget,
  );
  return { session, turn };
}

export function getChangeSet(changeSetID: string): Promise<{ change_set: ChangeSetSummary }> {
  return requestJSON(`/api/v1/change-sets/${encodeURIComponent(changeSetID)}`);
}

export function getChangeSetOperations(changeSetID: string): Promise<{ operations: ChangeSetOperation[] }> {
  return requestJSON(`/api/v1/change-sets/${encodeURIComponent(changeSetID)}/operations?limit=100`);
}

export function resolveChangeSet(
  changeSetID: string,
  action: 'apply' | 'reject',
  revision: number,
): Promise<{ change_set: ChangeSetSummary }> {
  return requestJSON(`/api/v1/change-sets/${encodeURIComponent(changeSetID)}:${action}`, {
    method: 'POST',
    body: JSON.stringify({
      expected_revision: revision,
      operation_key: createOperationKey(),
      reason: action === 'reject' ? 'Rejected by user' : '',
    }),
  });
}

export function undoChangeSet(changeSetID: string): Promise<{ change_set: ChangeSetSummary }> {
  return requestJSON(`/api/v1/change-sets/${encodeURIComponent(changeSetID)}:undo`, {
    method: 'POST',
    body: JSON.stringify({ operation_key: createOperationKey() }),
  });
}
