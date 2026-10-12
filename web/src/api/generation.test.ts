import { beforeEach, describe, expect, it, vi } from 'vitest';
import { setPreferredAgentModelForRuntime } from '../preferences';
import { APIError } from './client';
import { checkGenerationReadiness, resolvePromptTranslation, submitGeneration } from './generation';

const mocks = vi.hoisted(() => ({ requestJSON: vi.fn(), getRuntime: vi.fn() }));
vi.mock('./client', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('./client')>()),
  createOperationKey: () => 'generated-operation',
  requestJSON: mocks.requestJSON,
}));
vi.mock('./runtime', () => ({ getRuntime: mocks.getRuntime }));

const target = {
  media_slot_id: 'slot_video',
  expected_slot_revision: 4,
  prompt: 'summary: 人物回头。',
  generator_id: 'video.h3.shotforward',
  generation_mode: 'reference',
  parameters: {},
};

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
});

describe('generation submission', () => {
  it('sends translation selection at batch level with a stable operation key and abort signal', async () => {
    const signal = new AbortController().signal;
    const selection = { runtime_kind: 'shotforward' as const, model_id: 'preferred_model' };
    await submitGeneration('project_1', 'episode_1', target, {
      operationKey: 'stable-operation',
      promptTranslation: selection,
      signal,
    });
    expect(mocks.requestJSON).toHaveBeenCalledWith('/api/v1/projects/project_1/generation/jobs', {
      method: 'POST',
      signal,
      body: JSON.stringify({
        episode_id: 'episode_1',
        operation_key: 'stable-operation',
        targets: [target],
        prompt_translation: selection,
      }),
    });
  });

  it('preserves existing image callers and keeps readiness free of translation requests', async () => {
    await submitGeneration('project_1', 'episode_1', { ...target, generator_id: 'image.test' });
    const submitted = JSON.parse(mocks.requestJSON.mock.calls[0]![1].body);
    expect(submitted.operation_key).toBe('generated-operation');
    expect(submitted).not.toHaveProperty('prompt_translation');
    const signal = new AbortController().signal;
    await checkGenerationReadiness('project_1', 'episode_1', target, signal);
    expect(mocks.requestJSON).toHaveBeenLastCalledWith('/api/v1/projects/project_1/generation/readiness', {
      method: 'POST',
      body: JSON.stringify({ episode_id: 'episode_1', target }),
      signal,
    });
    expect(mocks.getRuntime).not.toHaveBeenCalled();
  });
});

describe('translation Agent selection', () => {
  it('uses the current active runtime and its available preferred model', async () => {
    setPreferredAgentModelForRuntime('shotforward', 'preferred_model');
    const signal = new AbortController().signal;
    mocks.getRuntime.mockResolvedValue({
      active_runtime_kind: 'shotforward',
      runtime: { health: 'ready' },
      models: [{ runtime_kind: 'user_codex', id: 'wrong-account-model' }],
      runtime_models: {
        shotforward: [
          { runtime_kind: 'shotforward', id: 'default_model' },
          { runtime_kind: 'shotforward', id: 'preferred_model' },
        ],
      },
    });
    await expect(resolvePromptTranslation(signal)).resolves.toEqual({
      runtime_kind: 'shotforward',
      model_id: 'preferred_model',
    });
    expect(mocks.getRuntime).toHaveBeenCalledWith({ signal });
  });

  it('falls back to an available model within the captured runtime when the saved model is invalid', async () => {
    setPreferredAgentModelForRuntime('shotforward', 'removed_model');
    mocks.getRuntime.mockResolvedValue({
      active_runtime_kind: 'shotforward',
      runtime: { health: 'ready' },
      models: [
        { runtime_kind: 'user_codex', id: 'wrong-account-model' },
        { runtime_kind: 'shotforward', id: 'available_model' },
      ],
    });
    await expect(resolvePromptTranslation()).resolves.toEqual({
      runtime_kind: 'shotforward',
      model_id: 'available_model',
    });
  });

  it.each(['login_required', 'ready'])(
    'does not switch accounts when the active runtime is %s without a valid model',
    async (health) => {
      mocks.getRuntime.mockResolvedValue({
        active_runtime_kind: 'shotforward',
        runtime: { health },
        models: [{ runtime_kind: 'user_codex', id: 'other-account-model' }],
      });
      await expect(resolvePromptTranslation()).rejects.toMatchObject({
        code: 'prompt_translation_unavailable',
      } satisfies Partial<APIError>);
    },
  );

  it('stops after cancellation even if runtime discovery resolves late', async () => {
    const controller = new AbortController();
    mocks.getRuntime.mockImplementation(async () => {
      controller.abort();
      return {
        active_runtime_kind: 'shotforward',
        runtime: { health: 'ready' },
        models: [{ runtime_kind: 'shotforward', id: 'model' }],
      };
    });
    await expect(resolvePromptTranslation(controller.signal)).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('reports runtime discovery timeout as prompt preparation timeout', async () => {
    mocks.getRuntime.mockRejectedValue(new DOMException('Timed out', 'AbortError'));
    await expect(resolvePromptTranslation()).rejects.toMatchObject({
      code: 'prompt_translation_timeout',
      status: 504,
    });
  });

  it('reports runtime connection failure as preparation unavailability', async () => {
    mocks.getRuntime.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(resolvePromptTranslation()).rejects.toMatchObject({
      code: 'prompt_translation_unavailable',
      status: 503,
    });
  });
});
