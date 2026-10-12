import type { TFunction } from 'i18next';
import { businessErrorCode, businessErrorMessage } from '../businessErrors';
import { preferredAgentModelForRuntime } from '../preferences';
import { generationCapabilityMessage } from '../taskFailure';
import { APIError, createOperationKey, requestJSON } from './client';
import { getRuntime, type AgentRuntimeKind } from './runtime';

export interface PromptTranslation {
  runtime_kind: AgentRuntimeKind;
  model_id: string;
}

export interface GenerationSubmissionOptions {
  promptTranslation?: PromptTranslation;
  operationKey?: string;
  signal?: AbortSignal;
}

export class MissingAssetReferenceMediaError extends Error {
  readonly assetNames: string[];

  constructor(assetNames: string[]) {
    super(`missing image media for referenced assets: ${assetNames.join(', ')}`);
    this.name = 'MissingAssetReferenceMediaError';
    this.assetNames = assetNames;
  }
}

const promptTranslationErrorKeys: Record<string, string> = {
  prompt_translation_unavailable: 'storyboard.promptTranslationUnavailable',
  prompt_translation_timeout: 'storyboard.promptTranslationTimeout',
  prompt_translation_invalid: 'storyboard.promptTranslationInvalid',
  prompt_translation_selection_changed: 'storyboard.promptTranslationSelectionChanged',
};

function promptTranslationErrorMessage(code: string, t: TFunction): string | undefined {
  if (!code.startsWith('prompt_translation_')) return;
  return t(promptTranslationErrorKeys[code] ?? 'storyboard.promptTranslationFailed');
}

export function generationSubmissionErrorMessage(error: unknown, t: TFunction, language?: string): string {
  if (error instanceof MissingAssetReferenceMediaError)
    return t('storyboard.generationAssetImageMissing', { names: error.assetNames.join(', ') });
  const businessMessage = businessErrorMessage(businessErrorCode(error), t);
  if (businessMessage) return businessMessage;
  if (error instanceof APIError) {
    const translationMessage = promptTranslationErrorMessage(error.code, t);
    if (translationMessage) return translationMessage;
    if (error.code === 'revision_conflict') return t('storyboard.generationStateChanged');
    if (error.code === 'generation_not_ready') return t('storyboard.generationFailed');
    if (error.code === 'capability_unsupported')
      return (
        generationCapabilityMessage(error.details, language) ??
        t('storyboard.generationCapabilityUnsupported')
      );
    if (error.code === 'provider_unavailable') return t('storyboard.generationProviderUnavailable');
    if (error.code === 'active_slot_job_exists') return t('storyboard.generationAlreadyActive');
    return t('storyboard.generationUnknown', { code: error.code, request: error.requestID || '—' });
  }
  if (error instanceof TypeError) return t('storyboard.generationConnectionFailed');
  return t('storyboard.generationUnknown', { code: 'unknown_error', request: '—' });
}

export async function resolvePromptTranslation(signal?: AbortSignal): Promise<PromptTranslation> {
  let runtime;
  try {
    runtime = await getRuntime({ ...(signal ? { signal } : {}) });
  } catch (error) {
    signal?.throwIfAborted();
    const timedOut =
      error !== null && typeof error === 'object' && 'name' in error && error.name === 'AbortError';
    throw new APIError(timedOut ? 504 : 503, {
      error: {
        code: timedOut ? 'prompt_translation_timeout' : 'prompt_translation_unavailable',
        message: timedOut ? 'Agent runtime discovery timed out' : 'Agent runtime is unavailable',
      },
    });
  }
  signal?.throwIfAborted();
  const kind = runtime.active_runtime_kind ?? 'user_codex';
  const health = runtime.runtimes?.[kind]?.health ?? runtime.runtime.health;
  const models = (runtime.runtime_models?.[kind] ?? runtime.models).filter(
    (model) => model.runtime_kind === kind,
  );
  const preferred = preferredAgentModelForRuntime(kind);
  const selected = models.find((model) => model.id === preferred) ?? models[0];
  if (health !== 'ready' || !selected) {
    throw new APIError(503, {
      error: { code: 'prompt_translation_unavailable', message: 'Agent runtime is not ready' },
    });
  }
  return { runtime_kind: kind, model_id: selected.id };
}

export interface GenerationReference {
  role: 'first_image' | 'last_image' | 'image_reference' | 'video_reference' | 'audio_reference';
  artifact_id: string;
  trim_start_ms?: number;
  trim_end_ms?: number;
}

export interface GenerationTarget {
  media_slot_id: string;
  expected_slot_revision: number;
  prompt: string;
  generation_mode?: string;
  generator_id: string;
  provider_model_id?: string;
  requested_duration_seconds?: number;
  references?: GenerationReference[];
  parameters: Record<string, unknown>;
}

export interface GenerationJob {
  generation_job_id: string;
  generation_batch_id: string;
  project_id: string;
  episode_id: string;
  target_media_slot_id: string;
  state: 'queued' | 'running' | 'stop_requested' | 'succeeded' | 'failed' | 'cancelled' | 'stopped';
  generator_id: string;
  stop_supported: boolean;
  progress_stage: string;
  progress_message: string;
  failure_code: string;
  failure_message: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface GenerationBatch {
  batch: {
    generation_batch_id: string;
    project_id: string;
    episode_id: string;
    operation_key: string;
    created_at: string;
  };
  jobs: GenerationJob[];
}

export function checkGenerationReadiness(
  projectID: string,
  episodeID: string,
  target: GenerationTarget,
  signal?: AbortSignal,
): Promise<{ ready: true; slot_revision: number }> {
  return requestJSON(`/api/v1/projects/${encodeURIComponent(projectID)}/generation/readiness`, {
    method: 'POST',
    body: JSON.stringify({ episode_id: episodeID, target }),
    ...(signal ? { signal } : {}),
  });
}

export function submitGeneration(
  projectID: string,
  episodeID: string,
  target: GenerationTarget,
  options: GenerationSubmissionOptions = {},
): Promise<GenerationBatch> {
  return requestJSON(`/api/v1/projects/${encodeURIComponent(projectID)}/generation/jobs`, {
    method: 'POST',
    body: JSON.stringify({
      episode_id: episodeID,
      operation_key: options.operationKey ?? createOperationKey(),
      targets: [target],
      ...(options.promptTranslation ? { prompt_translation: options.promptTranslation } : {}),
    }),
    ...(options.signal ? { signal: options.signal } : {}),
  });
}
