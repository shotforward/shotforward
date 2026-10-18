import { beforeEach, describe, expect, it, vi } from 'vitest';
import { compactSessionContext, startAgentWorkflow, submitAgentInput } from './agent';

const mocks = vi.hoisted(() => ({
  requestJSON: vi.fn(),
  getRuntime: vi.fn(),
}));

vi.mock('./client', () => ({
  createOperationKey: () => 'operation_1',
  requestJSON: mocks.requestJSON,
}));
vi.mock('./runtime', () => ({ getRuntime: mocks.getRuntime }));

const activeSession = {
  id: 'session_1',
  project_id: 'project_1',
  episode_id: 'episode_1',
  model_id: 'model_1',
  title: '',
  status: 'active',
  revision: 1,
  created_at: '2026-09-06T00:00:00Z',
  updated_at: '2026-09-06T00:00:00Z',
};

describe('startAgentWorkflow', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
  });

  it('requests context compaction for exactly the selected session without a data envelope', async () => {
    mocks.requestJSON.mockResolvedValue({ context_compacted: true });

    await expect(compactSessionContext('session/with space')).resolves.toEqual({ context_compacted: true });

    expect(mocks.requestJSON).toHaveBeenCalledExactlyOnceWith(
      '/api/v1/agent-sessions/session%2Fwith%20space/compact',
      { method: 'POST', body: '{}' },
    );
  });

  it('sends trusted model context separately from the user text for ordinary requests', async () => {
    mocks.requestJSON.mockResolvedValue({ action: 'queued', turn: { id: 'turn_1' } });
    const target = {
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'MiniMax-H3-Ref2VA',
      keyframe_provider_model_id: 'MiniMax-H3-FL2VA',
      focus: {
        clip_id: 'clip_1',
        generation_mode: 'first_image' as const,
        generator_id: 'video.seedance-2.5.volcengine-cn',
        provider_model_id: 'seedance-2-5-pro',
      },
    };
    await submitAgentInput(
      'session_1',
      'Split this clip',
      'model_1',
      { clip_id: 'clip_1' },
      [],
      'operation_1',
      target,
    );
    const input = JSON.parse(mocks.requestJSON.mock.calls[0]?.[1].body);
    expect(input.text).toBe('Split this clip');
    expect(input.focus).toEqual({ clip_id: 'clip_1' });
    expect(input.video_prompt_target).toEqual(target);
  });

  it('sends the workflow prompt to the active episode session', async () => {
    mocks.requestJSON
      .mockResolvedValueOnce({ sessions: [activeSession] })
      .mockResolvedValueOnce({ action: 'queued', turn: { id: 'turn_1' } });

    const result = await startAgentWorkflow('project_1', 'episode_1', 'extract assets');

    expect(result.session).toEqual(activeSession);
    expect(mocks.getRuntime).not.toHaveBeenCalled();
    expect(mocks.requestJSON).toHaveBeenLastCalledWith('/api/v1/agent-sessions/session_1/inputs', {
      method: 'POST',
      body: JSON.stringify({
        client_operation_id: 'operation_1',
        model_id: 'model_1',
        text: 'extract assets',
        focus: {},
      }),
    });
  });

  it('creates a default session before sending when the episode has no active session', async () => {
    mocks.requestJSON
      .mockResolvedValueOnce({ sessions: [] })
      .mockResolvedValueOnce({ session: activeSession })
      .mockResolvedValueOnce({ action: 'queued', turn: { id: 'turn_1' } });
    mocks.getRuntime.mockResolvedValue({
      runtime: { health: 'ready' },
      models: [{ id: 'model_1', display_name: 'Model 1' }],
    });

    await startAgentWorkflow('project_1', 'episode_1', 'generate storyboard');

    expect(mocks.requestJSON).toHaveBeenNthCalledWith(
      2,
      '/api/v1/projects/project_1/episodes/episode_1/agent-sessions',
      {
        method: 'POST',
        body: JSON.stringify({
          model_id: 'model_1',
          runtime_kind: 'user_codex',
          web_search_disabled: false,
        }),
      },
    );
    expect(mocks.requestJSON).toHaveBeenNthCalledWith(
      3,
      '/api/v1/agent-sessions/session_1/inputs',
      expect.objectContaining({ method: 'POST' }),
    );
  });

  it('sends the selected project object as the Turn focus', async () => {
    mocks.requestJSON
      .mockResolvedValueOnce({ sessions: [activeSession] })
      .mockResolvedValueOnce({ action: 'queued', turn: { id: 'turn_1' } });

    await startAgentWorkflow('project_1', 'episode_1', 'improve this asset', {
      asset_id: 'ast_1',
    });

    expect(mocks.requestJSON).toHaveBeenLastCalledWith('/api/v1/agent-sessions/session_1/inputs', {
      method: 'POST',
      body: JSON.stringify({
        client_operation_id: 'operation_1',
        model_id: 'model_1',
        text: 'improve this asset',
        focus: { asset_id: 'ast_1' },
      }),
    });
  });
});
