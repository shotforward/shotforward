import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { APIError } from '../api/client';
import type { Clip } from '../api/storyboard';
import type { TaskItem } from '../api/tasks';
import { StoryboardWorkspacePage } from './StoryboardWorkspacePage';

const mocks = vi.hoisted(() => ({
  getStoryboard: vi.fn(),
  updateClip: vi.fn(),
  getMediaSlot: vi.fn(),
  listProviders: vi.fn(),
  readiness: vi.fn(),
  resolveTranslation: vi.fn(),
  submit: vi.fn(),
  getTaskSnapshot: vi.fn(),
  changeTask: vi.fn(),
}));
vi.mock('../api/episodes', () => ({
  getEpisode: async () => ({ project: { name: 'Project' }, episode: { title: 'Video' } }),
}));
vi.mock('../api/storyboard', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('../api/storyboard')>()),
  getStoryboard: mocks.getStoryboard,
  updateClip: mocks.updateClip,
}));
vi.mock('../api/assets', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('../api/assets')>()),
  listEpisodeAssets: async () => ({ assets: [] }),
  listClipReferenceMedia: async () => ({ artifacts: [] }),
  listEpisodeReferenceMedia: async () => ({ reference_media: [] }),
  getMediaSlot: mocks.getMediaSlot,
}));
vi.mock('../api/providers', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('../api/providers')>()),
  listProviders: mocks.listProviders,
}));
vi.mock('../api/generation', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('../api/generation')>()),
  checkGenerationReadiness: mocks.readiness,
  resolvePromptTranslation: mocks.resolveTranslation,
  submitGeneration: mocks.submit,
}));
vi.mock('../api/tasks', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('../api/tasks')>()),
  getTaskSnapshot: mocks.getTaskSnapshot,
  changeGenerationTask: mocks.changeTask,
}));
vi.mock('../components/AgentDock', () => ({ AgentDock: () => null }));
vi.mock('../components/ProjectTabs', () => ({
  ProjectTabs: () => null,
  EpisodeWorkspaceLoading: () => <p>Loading</p>,
}));
vi.mock('../useAgentTemplates', () => ({
  useAgentTemplates: () => ({ render: () => 'Agent request' }),
}));

const clip: Clip = {
  clip_id: 'clip_1',
  project_id: 'project_1',
  episode_id: 'episode_1',
  scene_id: 'scene_1',
  position: 1,
  title: 'Opening',
  description: '',
  dialogue: '',
  creative_intent: '',
  planned_duration_ms: 8_000,
  first_image_prompt: '',
  last_image_prompt: '',
  video_prompt: '人物缓缓回头。',
  video_prompts: { first_image: '', first_last_image: '', reference: '人物缓缓回头。' },
  generation_mode: 'reference',
  revision: 1,
  first_image_slot_id: 'slot_first',
  last_image_slot_id: 'slot_last',
  video_slot_id: 'slot_video',
  reference_audio_slot_id: 'slot_audio',
  asset_references: [],
  derived_from_clip_ids: [],
  created_at: '',
  updated_at: '',
};
const batch = { batch: {}, jobs: [{ generation_job_id: 'job_1' }] };
const secondClip: Clip = {
  ...clip,
  clip_id: 'clip_2',
  position: 2,
  title: 'Second unit',
  first_image_slot_id: 'slot_first_2',
  last_image_slot_id: 'slot_last_2',
  video_slot_id: 'slot_video_2',
  reference_audio_slot_id: 'slot_audio_2',
};
const thirdClip: Clip = {
  ...clip,
  clip_id: 'clip_3',
  position: 3,
  title: 'Third unit',
  first_image_slot_id: 'slot_first_3',
  last_image_slot_id: 'slot_last_3',
  video_slot_id: 'slot_video_3',
  reference_audio_slot_id: 'slot_audio_3',
};
const preparing = /Preparing English prompt|正在准备英文提示词/;
const cancelPreparation = /Cancel preparation|取消准备/;
const submitted = /1 task submitted|1 个任务已提交/;
const stoppedPreparation = /Stopped waiting for preparation or submission|已停止准备或提交等待/;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function setClips(clips: Clip[]) {
  mocks.getStoryboard.mockResolvedValue({
    storyboard: {
      episode_id: 'episode_1',
      revision: 1,
      planned_duration_ms: clips.reduce((duration, item) => duration + item.planned_duration_ms, 0),
      scenes: [
        {
          scene_id: 'scene_1',
          position: 1,
          title: 'Scene',
          description: '',
          clips,
        },
      ],
    },
  });
}

async function selectClip(item: Clip) {
  const card = screen
    .getAllByRole('button', { name: new RegExp(item.title) })
    .find((button) => button.classList.contains('clip-grid__card'))!;
  fireEvent.click(card);
  await screen.findByRole('heading', { name: item.title });
}

function queuedTask(item: Clip): TaskItem {
  return {
    task_type: 'generation_job',
    task_id: `job_${item.clip_id}`,
    project_id: item.project_id,
    episode_id: item.episode_id,
    state: 'queued',
    progress_stage: 'queued',
    progress_message: '',
    failure_code: '',
    revision: 1,
    created_at: '2026-10-11T12:31:07Z',
    updated_at: '2026-10-11T12:31:07Z',
    target_media_slot_id: item.video_slot_id,
    generator_id: 'video.h3.shotforward',
    target_media_kind: 'video',
    target_kind: 'clip',
    target_id: item.clip_id,
  };
}

function provider(generatorID = 'video.h3.shotforward') {
  return {
    generator_id: generatorID,
    model_id: 'model',
    enabled: true,
    health: 'ready',
    models: [
      {
        model_id: 'model',
        health: 'ready',
        capabilities: {
          generation_modes: ['reference'],
          minimum_total_inputs: 0,
          maximum_total_inputs: 0,
          inputs: [],
        },
      },
    ],
  };
}

async function openWorkspace(initialClip = clip, waitUntilEnabled = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const result = render(
    <QueryClientProvider client={client}>
      <MemoryRouter
        initialEntries={[`/projects/project_1/episodes/episode_1/storyboard/clips/${initialClip.clip_id}`]}
      >
        <Routes>
          <Route
            path="/projects/:projectId/episodes/:episodeId/storyboard/clips/:clipId"
            element={<StoryboardWorkspacePage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  unmountWorkspace = result.unmount;
  if (!waitUntilEnabled) {
    await screen.findByRole('heading', { name: initialClip.title });
    return document.querySelector<HTMLButtonElement>('.clip-focus-generation__submit')!;
  }
  const generate = await screen.findByRole('button', { name: /^Generate video$|^生成视频$/i });
  if (waitUntilEnabled) await waitFor(() => expect(generate).toBeEnabled());
  return generate;
}

let unmountWorkspace: () => void;

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  setClips([clip]);
  mocks.listProviders.mockResolvedValue({ providers: [provider()] });
  mocks.readiness.mockResolvedValue({ ready: true, slot_revision: 3 });
  mocks.resolveTranslation.mockResolvedValue({ runtime_kind: 'shotforward', model_id: 'agent-model' });
  mocks.submit.mockResolvedValue(batch);
  mocks.getTaskSnapshot.mockResolvedValue({ tasks: [] });
  mocks.changeTask.mockResolvedValue({ job: {} });
  mocks.getMediaSlot.mockImplementation(async (_projectID, slotID) => ({
    slot: { media_slot_id: slotID, revision: 3, current_media_version_id: '' },
    artifacts: [],
    versions: [],
  }));
  mocks.updateClip.mockImplementation(async (_projectID, _episodeID, clipID, fields) => ({
    clip: {
      ...[clip, secondClip, thirdClip].find((item) => item.clip_id === clipID),
      ...fields,
      planned_duration_ms: fields.planned_duration_seconds * 1_000,
    },
  }));
});

describe('Storyboard generation preparation', () => {
  it('includes the current Agent selection only for H3 video submission', async () => {
    fireEvent.click(await openWorkspace());
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    const options = mocks.submit.mock.calls[0]![3];
    expect(options).toMatchObject({
      promptTranslation: { runtime_kind: 'shotforward', model_id: 'agent-model' },
    });
    expect(options.operationKey).toEqual(expect.any(String));
    expect(options.signal).toBe(mocks.readiness.mock.calls[0]![3]);
    expect(options.signal).toBe(mocks.resolveTranslation.mock.calls[0]![0]);
    expect(mocks.submit.mock.calls[0]![2].prompt).toBe('人物缓缓回头。');
    await screen.findByText(/1 task submitted|1 个任务已提交/);
  });

  it('skips translation for other video families', async () => {
    mocks.listProviders.mockResolvedValue({ providers: [provider('video.wan-3.0.official')] });
    fireEvent.click(await openWorkspace());
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    expect(mocks.resolveTranslation).not.toHaveBeenCalled();
    expect(mocks.submit.mock.calls[0]![3]).not.toHaveProperty('promptTranslation');
  });

  it('cancels preparation before submission even when Agent discovery resolves late', async () => {
    let resolve!: (selection: { runtime_kind: 'shotforward'; model_id: string }) => void;
    mocks.resolveTranslation.mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    fireEvent.click(await openWorkspace());
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(1));
    expect(
      screen.getByRole('button', { name: /Preparing English prompt|正在准备英文提示词/ }),
    ).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: /Cancel preparation|取消准备/ }));
    expect(mocks.resolveTranslation.mock.calls[0]![0].aborted).toBe(true);
    await act(async () => resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }));
    await screen.findByText(/Stopped waiting for preparation or submission|已停止准备或提交等待/);
    expect(mocks.submit).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('does not announce a queued generation when a cancelled submit resolves late', async () => {
    let resolve!: (value: typeof batch) => void;
    mocks.submit.mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    fireEvent.click(await openWorkspace());
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    const taskReadsBeforeCancellation = mocks.getTaskSnapshot.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: /Cancel preparation|取消准备/ }));
    expect(mocks.submit.mock.calls[0]![3].signal.aborted).toBe(true);
    await act(async () => resolve(batch));
    await screen.findByText(/Stopped waiting for preparation or submission|已停止准备或提交等待/);
    await waitFor(() =>
      expect(mocks.getTaskSnapshot.mock.calls.length).toBeGreaterThan(taskReadsBeforeCancellation),
    );
    expect(screen.queryByText(/1 task submitted|1 个任务已提交/)).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows translation failure as preparation failure without claiming a queued job', async () => {
    mocks.submit.mockRejectedValue(new APIError(502, { error: { code: 'prompt_translation_failed' } }));
    fireEvent.click(await openWorkspace());
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /Could not prepare the English prompt|英文提示词准备失败/,
    );
    expect(screen.queryByText(/1 task submitted|1 个任务已提交/)).not.toBeInTheDocument();
  });

  it('keeps preparation and its cancellation on the initiating unit across navigation', async () => {
    setClips([clip, secondClip, thirdClip]);
    const translation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    mocks.resolveTranslation.mockReturnValue(translation.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(1));
    const signal = mocks.resolveTranslation.mock.calls[0]![0];

    await selectClip(thirdClip);
    expect(screen.queryByRole('button', { name: preparing })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: cancelPreparation })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Generate video$|^生成视频$/i })).toBeEnabled();
    expect(signal.aborted).toBe(false);
    expect(mocks.submit).not.toHaveBeenCalled();

    await selectClip(secondClip);
    expect(screen.getByRole('button', { name: preparing })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: cancelPreparation }));
    expect(signal.aborted).toBe(true);
    await selectClip(thirdClip);
    await act(async () => translation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }));
    await waitFor(() => expect(mocks.getStoryboard).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(stoppedPreparation)).not.toBeInTheDocument();
    expect(mocks.submit).not.toHaveBeenCalled();
    await selectClip(secondClip);
    await screen.findByText(stoppedPreparation);
  });

  it('shows a late submission notice only on the originating unit', async () => {
    setClips([clip, secondClip, thirdClip]);
    const submission = deferred<typeof batch>();
    mocks.submit.mockReturnValue(submission.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    expect(mocks.submit.mock.calls[0]![2].media_slot_id).toBe(secondClip.video_slot_id);
    await selectClip(thirdClip);
    await act(async () => submission.resolve(batch));
    await waitFor(() => expect(mocks.getStoryboard).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: preparing })).not.toBeInTheDocument();
    await selectClip(secondClip);
    await screen.findByText(submitted);
  });

  it('shows a late preparation error only on the originating unit', async () => {
    setClips([clip, secondClip, thirdClip]);
    const submission = deferred<typeof batch>();
    mocks.submit.mockReturnValue(submission.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    await selectClip(thirdClip);
    await act(async () =>
      submission.reject(new APIError(502, { error: { code: 'prompt_translation_failed' } })),
    );
    await waitFor(() => expect(mocks.getStoryboard).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
    await selectClip(secondClip);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /Could not prepare the English prompt|英文提示词准备失败/,
    );
  });

  it('allows two units to prepare concurrently and cancels only the selected unit', async () => {
    setClips([clip, secondClip, thirdClip]);
    const secondTranslation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    const thirdTranslation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    mocks.resolveTranslation
      .mockReturnValueOnce(secondTranslation.promise)
      .mockReturnValueOnce(thirdTranslation.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(1));
    const secondSignal = mocks.resolveTranslation.mock.calls[0]![0];
    await selectClip(thirdClip);
    fireEvent.click(screen.getByRole('button', { name: /^Generate video$|^生成视频$/i }));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(2));
    const thirdSignal = mocks.resolveTranslation.mock.calls[1]![0];
    expect(thirdSignal).not.toBe(secondSignal);
    fireEvent.click(screen.getByRole('button', { name: cancelPreparation }));
    expect(thirdSignal.aborted).toBe(true);
    expect(secondSignal.aborted).toBe(false);
    await act(async () => thirdTranslation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }));
    await screen.findByText(stoppedPreparation);
    await selectClip(secondClip);
    expect(screen.getByRole('button', { name: preparing })).toBeDisabled();
    expect(screen.queryByText(stoppedPreparation)).not.toBeInTheDocument();
    await act(async () =>
      secondTranslation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }),
    );
    await screen.findByText(submitted);
    expect(mocks.submit).toHaveBeenCalledTimes(1);
    expect(mocks.submit.mock.calls[0]![2].media_slot_id).toBe(secondClip.video_slot_id);
    expect(mocks.submit.mock.calls[0]![3].signal).toBe(secondSignal);
    await selectClip(thirdClip);
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
    expect(screen.getByText(stoppedPreparation)).toBeInTheDocument();
  });

  it('does not clear another unit preparation when an earlier submission finishes', async () => {
    setClips([clip, secondClip, thirdClip]);
    const secondSubmission = deferred<typeof batch>();
    const thirdTranslation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    mocks.submit.mockReturnValueOnce(secondSubmission.promise);
    mocks.resolveTranslation
      .mockResolvedValueOnce({ runtime_kind: 'shotforward', model_id: 'agent-model' })
      .mockReturnValueOnce(thirdTranslation.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    await selectClip(thirdClip);
    fireEvent.click(screen.getByRole('button', { name: /^Generate video$|^生成视频$/i }));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(2));
    await act(async () => secondSubmission.resolve(batch));
    await waitFor(() => expect(mocks.getStoryboard).toHaveBeenCalledTimes(2));
    expect(screen.getByRole('button', { name: preparing })).toBeDisabled();
    expect(screen.getByRole('button', { name: cancelPreparation })).toBeEnabled();
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: cancelPreparation }));
    await act(async () => thirdTranslation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }));
    await screen.findByText(stoppedPreparation);
    expect(mocks.submit).toHaveBeenCalledTimes(1);
  });

  it('does not transfer a first-frame submission overlay to another unit', async () => {
    const secondKeyframeClip: Clip = {
      ...secondClip,
      generation_mode: 'first_image',
      first_image_prompt: '第二单元首帧。',
    };
    const thirdKeyframeClip: Clip = {
      ...thirdClip,
      generation_mode: 'first_image',
      first_image_prompt: '第三单元首帧。',
    };
    setClips([clip, secondKeyframeClip, thirdKeyframeClip]);
    const submission = deferred<typeof batch>();
    mocks.submit.mockReturnValue(submission.promise);
    mocks.listProviders.mockResolvedValue({
      providers: [
        {
          ...provider(),
          models: [
            {
              ...provider().models[0],
              capabilities: {
                ...provider().models[0]!.capabilities,
                generation_modes: ['reference', 'first_image'],
              },
            },
          ],
        },
        provider('image.codex'),
      ],
    });
    await openWorkspace(secondKeyframeClip, false);
    const firstFrameButton = await screen.findByRole('button', {
      name: /^Generate first frame$|^生成首帧$/i,
    });
    await waitFor(() => expect(firstFrameButton).toBeEnabled());
    fireEvent.click(firstFrameButton);
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    expect(mocks.submit.mock.calls[0]![2]).toMatchObject({
      media_slot_id: secondClip.first_image_slot_id,
      prompt: secondKeyframeClip.first_image_prompt,
    });
    expect(screen.getByRole('status')).toHaveTextContent(/Generating|生成中/);
    await selectClip(thirdKeyframeClip);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Generate first frame$|^生成首帧$/i })).toBeEnabled();
    expect(screen.queryByRole('button', { name: preparing })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: cancelPreparation })).not.toBeInTheDocument();
    await act(async () => submission.resolve(batch));
    await waitFor(() => expect(mocks.getStoryboard).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
  });

  it('keeps queued cancellation state and late failure with its own task', async () => {
    setClips([clip, secondClip, thirdClip]);
    const secondTask = queuedTask(secondClip);
    const thirdTask = queuedTask(thirdClip);
    mocks.getTaskSnapshot.mockResolvedValue({ tasks: [secondTask, thirdTask] });
    const cancellation = deferred<{ job: unknown }>();
    mocks.changeTask.mockReturnValue(cancellation.promise);
    await openWorkspace(secondClip, false);
    fireEvent.click(await screen.findByRole('button', { name: /^Cancel queued task$|^取消排队$/i }));
    await waitFor(() => expect(mocks.changeTask).toHaveBeenCalledWith(secondTask, 'cancel'));
    expect(screen.getByRole('button', { name: /Cancelling…|正在取消…/ })).toBeDisabled();
    await selectClip(thirdClip);
    expect(screen.queryByRole('button', { name: /Cancelling…|正在取消…/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Cancel queued task$|^取消排队$/i })).toBeEnabled();
    const taskReadsBeforeFailure = mocks.getTaskSnapshot.mock.calls.length;
    await act(async () => cancellation.reject(new Error('Task changed')));
    await waitFor(() =>
      expect(mocks.getTaskSnapshot.mock.calls.length).toBeGreaterThan(taskReadsBeforeFailure),
    );
    expect(screen.queryByText(/Cancellation failed|取消失败/)).not.toBeInTheDocument();
    await selectClip(secondClip);
    await screen.findByText(/Cancellation failed|取消失败/);
  });

  it('keeps auto-save pending state and late errors with the edited unit', async () => {
    setClips([clip, secondClip, thirdClip]);
    const save = deferred<{ clip: Clip }>();
    mocks.updateClip.mockReturnValue(save.promise);
    await openWorkspace(secondClip);
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '9' } });
    await waitFor(() => expect(mocks.updateClip).toHaveBeenCalledTimes(1));
    expect(mocks.updateClip.mock.calls[0]![2]).toBe(secondClip.clip_id);
    await screen.findByText(/Saving automatically|正在自动保存/);
    await selectClip(thirdClip);
    expect(screen.getByRole('spinbutton')).toHaveValue(8);
    expect(screen.queryByText(/Saving automatically|正在自动保存/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Generate video$|^生成视频$/i })).toBeEnabled();
    await act(async () => save.reject(new Error('Revision changed')));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    await selectClip(secondClip);
    await screen.findByRole('alert');
    expect(mocks.updateClip).toHaveBeenCalledTimes(1);
  });

  it('does not carry unsaved local fields into another unit after navigation', async () => {
    setClips([clip, secondClip, thirdClip]);
    await openWorkspace(secondClip);
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '9' } });
    await selectClip(thirdClip);
    expect(screen.getByRole('spinbutton')).toHaveValue(8);
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '10' } });
    await waitFor(() => expect(mocks.updateClip).toHaveBeenCalledTimes(1));
    expect(mocks.updateClip.mock.calls[0]![2]).toBe(thirdClip.clip_id);
    expect(mocks.updateClip.mock.calls[0]![3].planned_duration_seconds).toBe(10);
  });

  it('aborts every unit preparation when the workspace unmounts', async () => {
    setClips([clip, secondClip, thirdClip]);
    const secondTranslation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    const thirdTranslation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    mocks.resolveTranslation
      .mockReturnValueOnce(secondTranslation.promise)
      .mockReturnValueOnce(thirdTranslation.promise);
    fireEvent.click(await openWorkspace(secondClip));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(1));
    await selectClip(thirdClip);
    fireEvent.click(screen.getByRole('button', { name: /^Generate video$|^生成视频$/i }));
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(2));
    unmountWorkspace();
    expect(mocks.resolveTranslation.mock.calls[0]![0].aborted).toBe(true);
    expect(mocks.resolveTranslation.mock.calls[1]![0].aborted).toBe(true);
    await act(async () => {
      secondTranslation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' });
      thirdTranslation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' });
    });
    expect(mocks.submit).not.toHaveBeenCalled();
  });

  it('retains the clicked unit prompt and media snapshot when saving finishes after navigation', async () => {
    const source: Clip = {
      ...secondClip,
      video_prompt: '第二单元原提示词。',
      video_prompts: { ...secondClip.video_prompts, reference: '第二单元原提示词。' },
    };
    const other: Clip = {
      ...thirdClip,
      video_prompt: '第三单元不应提交。',
      video_prompts: { ...thirdClip.video_prompts, reference: '第三单元不应提交。' },
    };
    setClips([clip, source, other]);
    mocks.getMediaSlot.mockImplementation(async (_projectID, slotID) => ({
      slot: {
        media_slot_id: slotID,
        revision: slotID === source.video_slot_id ? 71 : 83,
        current_media_version_id: '',
      },
      artifacts: [],
      versions: [],
    }));
    const save = deferred<{ clip: Clip }>();
    mocks.updateClip.mockReturnValue(save.promise);
    const generate = await openWorkspace(source);
    fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '9' } });
    fireEvent.click(generate);
    await waitFor(() => expect(mocks.updateClip).toHaveBeenCalledTimes(1));
    expect(mocks.updateClip.mock.calls[0]![2]).toBe(source.clip_id);
    expect(mocks.updateClip.mock.calls[0]![3].planned_duration_seconds).toBe(9);
    await selectClip(other);
    expect(screen.getByRole('spinbutton')).toHaveValue(8);
    expect(mocks.submit).not.toHaveBeenCalled();
    await act(async () => save.resolve({ clip: { ...source, revision: 2, planned_duration_ms: 9_000 } }));
    await waitFor(() => expect(mocks.submit).toHaveBeenCalledTimes(1));
    expect(mocks.submit.mock.calls[0]![2]).toMatchObject({
      media_slot_id: source.video_slot_id,
      expected_slot_revision: 71,
      prompt: source.video_prompt,
      requested_duration_seconds: 9,
    });
    expect(mocks.readiness.mock.calls[0]![2]).toMatchObject({
      media_slot_id: source.video_slot_id,
      expected_slot_revision: 71,
    });
    expect(mocks.updateClip).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(submitted)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: preparing })).not.toBeInTheDocument();
  });

  it('does not start duplicate preparations when the same unit is clicked twice', async () => {
    const translation = deferred<{ runtime_kind: 'shotforward'; model_id: string }>();
    mocks.resolveTranslation.mockReturnValue(translation.promise);
    const generate = await openWorkspace();
    act(() => {
      fireEvent.click(generate);
      fireEvent.click(generate);
    });
    await waitFor(() => expect(mocks.resolveTranslation).toHaveBeenCalledTimes(1));
    expect(mocks.readiness).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: cancelPreparation }));
    await act(async () => translation.resolve({ runtime_kind: 'shotforward', model_id: 'agent-model' }));
    await screen.findByText(stoppedPreparation);
    expect(mocks.submit).not.toHaveBeenCalled();
  });
});
