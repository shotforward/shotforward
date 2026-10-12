import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import type { EpisodeAsset, EpisodeReferenceMedia, MediaSlotDetails } from '../api/assets';
import { APIError } from '../api/client';
import { updateVideoPromptFamily, type Clip } from '../api/storyboard';
import type { TaskItem } from '../api/tasks';
import { preferredVideoModel, setImageGeneratorPreference, setPreferredVideoModel } from '../preferences';
import { ClipFocusCard } from './StoryboardWorkspacePage';

function media(slotID: string, artifactID: string): MediaSlotDetails {
  return {
    slot: { media_slot_id: slotID, current_media_version_id: `version_${slotID}`, revision: 2 },
    versions: [{ media_version_id: `version_${slotID}`, artifact_id: artifactID }],
    artifacts: [{ artifact_id: artifactID }],
  } as MediaSlotDetails;
}

const clip = {
  clip_id: 'clip_1',
  project_id: 'project_1',
  episode_id: 'episode_1',
  scene_id: 'scene_1',
  position: 1,
  title: '开场',
  description: '',
  dialogue: '',
  creative_intent: '',
  planned_duration_ms: 8_000,
  first_image_prompt: '人物站在窗前',
  last_image_prompt: '人物回头看向镜头',
  video_prompt: '人物缓缓回头。',
  video_prompts: { first_image: '', first_last_image: '', reference: '人物缓缓回头。' },
  generation_mode: 'reference',
  revision: 1,
  first_image_slot_id: 'slot_first',
  last_image_slot_id: 'slot_last',
  video_slot_id: 'slot_video',
  reference_audio_slot_id: 'slot_reference_audio',
  asset_references: [],
  derived_from_clip_ids: [],
  created_at: '2026-09-07T00:00:00Z',
  updated_at: '2026-09-07T00:00:00Z',
} satisfies Clip;

function selectGenerationMode(mode: Clip['generation_mode']) {
  if (mode === 'reference') {
    fireEvent.click(screen.getByRole('radio', { name: /Reference driven|参考素材驱动/i }));
    return;
  }
  fireEvent.click(screen.getByRole('radio', { name: /Keyframe driven|关键帧驱动/i }));
  fireEvent.click(
    screen.getByRole('radio', {
      name: mode === 'first_image' ? /First frame only|仅首帧/i : /First \+ last frames|首帧 \+ 尾帧/i,
    }),
  );
}

function expandVideoPrompt() {
  fireEvent.click(screen.getByRole('button', { name: /Expand prompt|展开提示词/i }));
}

function editVideoPrompt() {
  expandVideoPrompt();
  fireEvent.click(screen.getByRole('button', { name: /^Edit$|^编辑$/i }));
}

beforeAll(async () => {
  // These tests exercise frame controls, not lazy-module download timing.
  // Load the real module before synchronous assertions on its controls.
  await import('../components/FrameInput');
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

beforeEach(() => window.localStorage.clear());

describe('ClipFocusCard generation controls', () => {
  it('uses the latest shared Episode range in @ previews and generation for multiple video units', () => {
    const onGenerate = vi.fn();
    const upload = {
      episode_id: 'episode_1',
      name: '声音参考',
      description: '',
      usage_count: 2,
      revision: 1,
      trim_start_ms: 3000,
      trim_end_ms: 11000,
      artifact: {
        artifact_id: 'shared_audio',
        kind: 'audio',
        duration_ms: 30000,
        original_filename: 'voice.wav',
      },
    } as EpisodeReferenceMedia;
    const card = (clipID: string, latest: EpisodeReferenceMedia) => (
      <ClipFocusCard
        projectID="project_1"
        episodeID="episode_1"
        clip={{
          ...clip,
          clip_id: clipID,
          video_prompts: { ...clip.video_prompts, reference: '人物说话，音色参考@声音参考。' },
        }}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'generated_video')}
        references={[]}
        availableUploadedMedia={[latest]}
        videoProviders={[
          {
            generator_id: 'video.seedance-2.5.volcengine-cn',
            model_id: 'doubao-seedance-2-5-251215',
            enabled: true,
            health: 'ready',
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={onGenerate}
        onOpenAgent={vi.fn()}
      />
    );
    const view = render(card('clip_1', upload));
    fireEvent.click(
      screen.getByRole('button', { name: /Preview prompt asset 声音参考|预览提示词资产 声音参考/ }),
    );
    expect(screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ })).toHaveAttribute(
      'max',
      '8000',
    );
    const latest = { ...upload, revision: 2, trim_start_ms: 5000, trim_end_ms: 9000 };
    view.rerender(card('clip_1', latest));
    expect(screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ })).toHaveAttribute(
      'max',
      '4000',
    );
    fireEvent.click(screen.getByRole('button', { name: /Close|关闭/ }));
    fireEvent.click(screen.getByRole('button', { name: /^Generate video$|^生成视频$/ }));
    expect(onGenerate.mock.lastCall?.[3]).toEqual([
      {
        role: 'audio_reference',
        artifact_id: 'shared_audio',
        name: '声音参考',
        trim_start_ms: 5000,
        trim_end_ms: 9000,
      },
    ]);
    view.rerender(card('clip_2', latest));
    fireEvent.click(screen.getByRole('button', { name: /^Generate video$|^生成视频$/ }));
    expect(onGenerate.mock.lastCall?.[3]).toEqual([
      {
        role: 'audio_reference',
        artifact_id: 'shared_audio',
        name: '声音参考',
        trim_start_ms: 5000,
        trim_end_ms: 9000,
      },
    ]);
    expect(upload.artifact.duration_ms).toBe(30000);
  });

  it('renders legacy null reference limits and output sizes without breaking the workspace', async () => {
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[
          {
            generator_id: 'video.h3.shotforward',
            model_id: 'h3-ref2va',
            enabled: true,
            health: 'ready',
            models: [
              {
                model_id: 'h3-ref2va',
                health: 'ready',
                capabilities: {
                  generation_modes: ['reference'],
                  inputs: null,
                  duration: null,
                  output: { sizes: null },
                },
              },
            ],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );
    expect(
      screen.getByRole('combobox', { name: /Video generation model|视频生成模型/i }),
    ).toBeInTheDocument();
    expect(await screen.findByRole('tooltip', { hidden: true })).toHaveTextContent(/720p.*4–15/);
    expect(screen.getByRole('button', { name: /Generate video|生成视频/i })).toBeDisabled();
  });

  it('shows the hosted LoRA model first and explains its 4–15 second range', async () => {
    const onGenerate = vi.fn();
    const onOpenAgent = vi.fn();
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[
          {
            generator_id: 'video.h3.official',
            model_id: 'MiniMax-H3',
            models: [{ model_id: 'MiniMax-H3', health: 'ready' }],
          } as never,
          {
            generator_id: 'video.h3.shotforward',
            model_id: 'h3-fl2va',
            enabled: true,
            health: 'ready',
            models: [
              { model_id: 'h3-fl2va', health: 'ready' },
              { model_id: 'h3-ref2va', health: 'ready' },
            ],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={onGenerate}
        onOpenAgent={onOpenAgent}
      />,
    );
    expect(screen.getByRole('heading', { level: 2, name: '开场' })).toHaveClass(
      'storyboard-heading--primary',
    );
    for (const name of [
      /^Video generation mode$|^视频生成模式$/i,
      /Video generation model|视频生成模型/i,
      /Video prompt|视频提示词/i,
    ]) {
      expect(screen.getByRole('heading', { level: 3, name })).toHaveClass('storyboard-heading--secondary');
    }
    expect(screen.getAllByText('MiniMax-H3').length).toBeGreaterThan(0);
    expect(
      await screen.findByRole('button', { name: /Video generation model|视频生成模型/i }),
    ).toBeInTheDocument();
    const modelHelp = screen.getByRole('tooltip', { hidden: true });
    expect(modelHelp).toHaveTextContent(/MiniMax-H3.*ShotForward Cloud.*720p.*4–15/);
    expect(modelHelp).not.toHaveTextContent(/MiniMax China/);
    expect(modelHelp.childElementCount).toBe(0);
    expect(screen.getByRole('button', { name: /Generate video|生成视频/i })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: /Edit with Agent|Agent 修改/i }));
    expect(onOpenAgent).toHaveBeenCalledWith(expect.stringContaining('generator_id=video.h3.shotforward'));
    expect(onOpenAgent).toHaveBeenCalledWith(expect.stringContaining('provider_model_id=h3-ref2va'));
    expect(onOpenAgent).toHaveBeenCalledWith(expect.stringContaining('profile=h3_ref2va@1'));
    expect(onGenerate).not.toHaveBeenCalled();
  });

  it('uses and updates the video model selected by the user', () => {
    setPreferredVideoModel('video.h3.official\u001fMiniMax-H3');
    const onOpenAgent = vi.fn();
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[
          {
            generator_id: 'video.h3.official',
            model_id: 'MiniMax-H3',
            models: [{ model_id: 'MiniMax-H3', health: 'ready' }],
          } as never,
          {
            generator_id: 'video.wan-3.0.official',
            model_id: 'wan3.0-video-prime',
            models: [{ model_id: 'wan3.0-video-prime', health: 'ready' }],
          } as never,
          {
            generator_id: 'video.h3.shotforward',
            model_id: 'h3-ref2va',
            enabled: true,
            health: 'ready',
            models: [{ model_id: 'h3-ref2va', health: 'ready' }],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={onOpenAgent}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: /Edit with Agent|Agent 修改/i }));
    expect(onOpenAgent).toHaveBeenCalledWith(expect.stringContaining('generator_id=video.h3.official'));

    fireEvent.click(screen.getByRole('combobox', { name: /Video generation model|视频生成模型/i }));
    fireEvent.click(screen.getByRole('option', { name: /Wan 3\.0 Prime/i }));
    expect(preferredVideoModel()).toBe('video.wan-3.0.official\u001fwan3.0-video-prime');
  });
  it('allows splitting short Clips while disabling split during a pending edit or operation', () => {
    const onSplit = vi.fn();
    const onDelete = vi.fn();
    const props = {
      projectID: 'project_1',
      firstMedia: undefined,
      lastMedia: undefined,
      videoMedia: undefined,
      references: [],
      videoProviders: [],
      saving: false,
      busy: false,
      error: false,
      notice: '',
      onSave: vi.fn(),
      onGenerate: vi.fn(),
      onOpenAgent: vi.fn(),
      onSplit,
      onDelete,
    };
    const view = render(<ClipFocusCard {...props} clip={clip} />);

    const split = screen.getByRole('button', { name: /拆分为多个单元|Split into units/i });
    expect(split).toBeEnabled();
    fireEvent.click(split);
    expect(onSplit).toHaveBeenCalledOnce();

    const actions = screen.getByRole('button', { name: /视频单元操作|Video unit actions/i });
    expect(screen.queryByRole('menu')).not.toBeVisible();
    fireEvent.click(actions);
    fireEvent.click(screen.getByRole('menuitem', { name: /删除视频单元|Delete video unit/i }));
    expect(onDelete).toHaveBeenCalledOnce();

    view.rerender(<ClipFocusCard {...props} clip={{ ...clip, planned_duration_ms: 6_000 }} />);
    expect(screen.getByRole('button', { name: /拆分为多个单元|Split into units/i })).toBeEnabled();
    view.rerender(<ClipFocusCard {...props} busy clip={{ ...clip, planned_duration_ms: 6_000 }} />);
    expect(screen.getByRole('button', { name: /拆分为多个单元|Split into units/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /拆分为多个单元|Split into units/i })).toHaveAttribute(
      'title',
      expect.stringMatching(/当前修改保存|current edit to save/i),
    );
    view.rerender(<ClipFocusCard {...props} clip={{ ...clip, planned_duration_ms: 6_000 }} />);
    fireEvent.change(screen.getByRole('spinbutton', { name: /时长.*秒|Duration.*seconds/i }), {
      target: { value: '7' },
    });
    expect(screen.getByRole('button', { name: /拆分为多个单元|Split into units/i })).toBeDisabled();
  });

  it('hides automatic previous-video continuation even for clips that previously enabled it', () => {
    const previousClip = { ...clip, clip_id: 'clip_previous', position: 1, title: '上一镜' };
    const currentClip = { ...clip, clip_id: 'clip_current', position: 2, continue_from_previous: true };
    const previousVideo = media('slot_previous_video', 'artifact_previous_video');
    previousVideo.artifacts[0] = {
      artifact_id: 'artifact_previous_video',
      kind: 'video',
      mime_type: 'video/mp4',
      original_filename: 'previous.mp4',
      byte_size: 1_000,
      width: 1280,
      height: 720,
      duration_ms: 22_000,
      created_at: '2026-09-07T00:00:00Z',
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={currentClip}
        previousClip={previousClip}
        previousVideoMedia={previousVideo}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[
          {
            generator_id: 'video.h3.official',
            model_id: 'MiniMax-H3',
            models: [{ model_id: 'MiniMax-H3', health: 'ready' }],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(screen.queryByRole('button', { name: /Continue previous|承接上一镜/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/final 15 seconds|结尾 15 秒/i)).not.toBeInTheDocument();
  });

  it('also hides automatic continuation for non-cloud providers', () => {
    const previousVideo = media('slot_previous_video', 'artifact_previous_video');
    previousVideo.artifacts[0] = {
      artifact_id: 'artifact_previous_video',
      kind: 'video',
      mime_type: 'video/mp4',
      original_filename: 'previous.mp4',
      byte_size: 1_000,
      width: 1280,
      height: 720,
      duration_ms: 20_000,
      created_at: '2026-09-07T00:00:00Z',
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{ ...clip, clip_id: 'clip_current', position: 2, continue_from_previous: false }}
        previousClip={{ ...clip, clip_id: 'clip_previous', position: 1, title: '上一镜' }}
        previousVideoMedia={previousVideo}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[
          {
            generator_id: 'video.seedance-2.5.byteplus-global',
            model_id: 'dreamina-seedance-2-5-260628',
            models: [{ model_id: 'dreamina-seedance-2-5-260628', health: 'ready' }],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(screen.queryByRole('button', { name: /Continue previous|承接上一镜/i })).not.toBeInTheDocument();
  });

  it('keeps the latest video task visible, cancellable only while queued, and previews its exact result', async () => {
    const onCancelQueuedVideo = vi.fn();
    const task: TaskItem = {
      task_type: 'generation_job',
      task_id: 'job_video_latest',
      generation_batch_id: 'batch_video_latest',
      project_id: 'project_1',
      episode_id: 'episode_1',
      target_media_slot_id: 'slot_video',
      target_media_kind: 'video',
      generator_id: 'video.wan-3.0.official',
      provider_model_id: 'wan3.0-video-prime',
      state: 'queued',
      stop_supported: false,
      progress_stage: 'queued',
      progress_message: '',
      failure_code: '',
      revision: 1,
      created_at: '2026-09-10T10:00:00Z',
      updated_at: '2026-09-10T10:00:00Z',
    };
    const card = (latestVideoTask: TaskItem) => (
      <ClipFocusCard
        projectID="project_1"
        episodeID="episode_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        latestVideoTask={latestVideoTask}
        videoProviders={[
          {
            generator_id: 'video.wan-3.0.official',
            model_id: 'wan3.0-video-prime',
            models: [{ model_id: 'wan3.0-video-prime', health: 'ready' }],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onCancelQueuedVideo={onCancelQueuedVideo}
        onOpenAgent={vi.fn()}
      />
    );
    const view = render(card(task));

    expect(screen.getByText(/Latest generation|最近一次生成/i)).toBeInTheDocument();
    const latest = document.querySelector<HTMLElement>('.latest-video-generation')!;
    expect(within(latest).getByText('wan3.0-video-prime')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Cancel queued task|取消排队/i }));
    expect(onCancelQueuedVideo).toHaveBeenCalledWith(task);

    const summary = screen.getByRole('button', { name: /Latest generation|最近一次生成/i });
    fireEvent.click(summary);
    expect(summary).toHaveAttribute('aria-expanded', 'false');
    expect(within(latest).queryByText('wan3.0-video-prime')).not.toBeInTheDocument();

    const running = {
      ...task,
      state: 'running' as const,
      revision: 2,
      started_at: '2026-09-10T10:00:01Z',
      updated_at: '2026-09-10T10:00:01Z',
    };
    view.rerender(card(running));
    expect(summary).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(summary);
    expect(
      screen.getByText(/Generation has started and cannot be cancelled|已经开始生成/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Cancel queued task|取消排队/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Generating|生成中/i })).toBeDisabled();
    fireEvent.click(summary);
    expect(summary).toHaveAttribute('aria-expanded', 'false');

    const succeeded = {
      ...running,
      state: 'succeeded' as const,
      revision: 3,
      result_artifact_id: 'artifact_video_latest',
      result_media_version_id: 'version_video_latest',
      finished_at: '2026-09-10T10:02:00Z',
      updated_at: '2026-09-10T10:02:00Z',
    };
    view.rerender(card(succeeded));
    expect(summary).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(summary);
    const preview = document.querySelector<HTMLVideoElement>('.latest-video-generation video');
    expect(preview?.src).toContain('/artifacts/artifact_video_latest/content');
    fireEvent.click(screen.getByRole('button', { name: /View video result|查看视频结果/i }));
    const resultBanner = document.querySelector<HTMLElement>('.clip-focus-result');
    expect(resultBanner).toHaveClass('has-video');
    expect(resultBanner?.querySelector<HTMLVideoElement>('video')?.src).toContain(
      '/artifacts/artifact_video_latest/content',
    );
  });

  it('shows a localized failure category in the latest video task', () => {
    const failedTask = {
      task_type: 'generation_job',
      task_id: 'job_video_failed',
      generation_batch_id: 'batch_video_failed',
      project_id: 'project_1',
      episode_id: 'episode_1',
      target_media_slot_id: 'slot_video',
      target_media_kind: 'video',
      generator_id: 'video.seedance-2.5.volcengine-cn',
      provider_model_id: 'doubao-seedance-2-0-mini-260615',
      state: 'failed',
      stop_supported: false,
      progress_stage: 'failed',
      progress_message: '',
      failure_code: 'reference_delivery_login_required',
      failure_message: '',
      revision: 2,
      created_at: '2026-09-20T04:14:00Z',
      updated_at: '2026-09-20T04:14:04Z',
      started_at: '2026-09-20T04:14:00Z',
      finished_at: '2026-09-20T04:14:04Z',
    } as TaskItem;

    render(
      <ClipFocusCard
        projectID="project_1"
        episodeID="episode_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        latestVideoTask={failedTask}
        videoProviders={[{ generator_id: 'video.seedance-2.5.volcengine-cn' } as never]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(screen.getByText(/ShotForward.*continuation|承接.*ShotForward/i)).toBeInTheDocument();
    expect(screen.queryByText(/tasks\.failures\./)).not.toBeInTheDocument();
  });

  it('shows every video version and preserves the selected artifact aspect ratio', () => {
    const videoMedia = {
      slot: { media_slot_id: 'slot_video', current_media_version_id: 'version_2', revision: 3 },
      versions: [
        { media_version_id: 'version_1', artifact_id: 'artifact_1', sequence_number: 1 },
        { media_version_id: 'version_2', artifact_id: 'artifact_2', sequence_number: 2 },
      ],
      artifacts: [
        { artifact_id: 'artifact_1', width: 1920, height: 1080, duration_ms: 6_000 },
        { artifact_id: 'artifact_2', width: 1080, height: 1920, duration_ms: 8_000 },
      ],
    } as MediaSlotDetails;
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={videoMedia}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole('tab', { name: /Video result|视频结果/i }));
    expect(document.querySelector('.clip-focus-result__stage')).toContainElement(
      document.querySelector('.clip-focus-result__player'),
    );
    expect(document.querySelector('.clip-focus-result__player')).toHaveStyle({
      aspectRatio: '1080 / 1920',
      maxWidth: '300px',
    });
    expect(document.querySelector('.clip-focus-result__player video')).toHaveAttribute(
      'src',
      '/api/v1/projects/project_1/artifacts/artifact_2/content',
    );
    fireEvent.click(screen.getByRole('button', { name: /Enlarge video|放大播放视频/i }));
    expect(screen.getByRole('dialog', { name: /开场/ })).toContainElement(
      document.querySelector('.video-lightbox video'),
    );
    fireEvent.click(screen.getByRole('button', { name: /^Close$|^关闭$/i }));
    expect(screen.getByRole('button', { name: /V1/i })).toBeEnabled();
    expect(screen.getByRole('button', { name: /V2/i })).toBeDisabled();
    expect(screen.getAllByText('1080 × 1920')).toHaveLength(2);
    const versionThumbnails = document.querySelectorAll<HTMLElement>('.video-version-picker__thumb');
    expect(versionThumbnails[0]).toHaveStyle({ aspectRatio: '1080 / 1920' });
    expect(versionThumbnails[1]).toHaveStyle({ aspectRatio: '1920 / 1080' });
    const thumbnailVideo = versionThumbnails[0]!.querySelector('video')!;
    fireEvent.loadedMetadata(thumbnailVideo);
    expect(thumbnailVideo.currentTime).toBe(0.25);
  });

  it('opens an asset card from an @Asset name in a structured H3 prompt', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onOpenAgent = vi.fn();
    const renderAgentTemplate = vi.fn(
      (key: string, variables?: Record<string, string | number>) =>
        `${key}:${variables?.asset_id}:${variables?.name}`,
    );
    const asset: EpisodeAsset = {
      asset_id: 'asset_lindaiyu',
      project_id: 'project_1',
      episode_id: 'episode_1',
      identity: {
        asset_identity_id: 'identity_lindaiyu',
        project_id: 'project_1',
        name: '林黛玉',
        kind: 'character',
        revision: 1,
        created_at: '2026-09-10T00:00:00Z',
        updated_at: '2026-09-10T00:00:00Z',
      },
      prompt: '古典人物，白色衣裙',
      revision: 1,
      primary_image_slot_id: 'slot_lindaiyu',
      created_at: '2026-09-10T00:00:00Z',
      updated_at: '2026-09-10T00:00:00Z',
    };
    const assetMedia = media('slot_lindaiyu', 'artifact_lindaiyu');
    client.setQueryData(['media-slot', 'project_1', 'slot_lindaiyu'], assetMedia);
    const task = {
      task_type: 'generation_job',
      task_id: 'job_asset_image',
      project_id: 'project_1',
      episode_id: 'episode_1',
      state: 'running',
      progress_stage: 'generating',
      progress_message: 'Rendering image',
      failure_code: '',
      revision: 2,
      created_at: '2026-09-10T00:00:00Z',
      updated_at: '2026-09-10T00:00:01Z',
      target_media_slot_id: 'slot_lindaiyu',
      target_media_kind: 'image',
    } as const;
    const h3Prompt =
      'subject_definitions:\n<Subject 1>: @林黛玉\nsummary:\nThe subject slowly looks back.\nretention_analysis:\nIdentity: fully_preserved\ndetailed_description:\n<Subject 1> looks toward the camera.\noverall_soundscape:\nNone\nnon_diegetic_music:\nNone';
    const promptClip = {
      ...clip,
      video_prompt: h3Prompt,
      video_prompts: {
        ...clip.video_prompts,
        reference: updateVideoPromptFamily('', 'h3', h3Prompt, {
          profile: 'h3_ref2va@1',
          generator_id: 'video.h3.shotforward',
          provider_model_id: 'h3-ref2va',
          asset_ids: ['asset_lindaiyu'],
        }),
      },
    };

    const card = (currentAsset: EpisodeAsset) => (
      <QueryClientProvider client={client}>
        <ClipFocusCard
          projectID="project_1"
          episodeID="episode_1"
          clip={promptClip}
          firstMedia={undefined}
          lastMedia={undefined}
          videoMedia={undefined}
          references={[{ asset: currentAsset, media: assetMedia } as never]}
          assetGenerationTasks={new Map([['slot_lindaiyu', task]])}
          videoProviders={[
            {
              generator_id: 'video.h3.shotforward',
              enabled: true,
              health: 'ready',
              models: [{ model_id: 'h3-ref2va', health: 'ready' }],
            } as never,
          ]}
          saving={false}
          busy={false}
          error={false}
          notice=""
          onSave={vi.fn()}
          onGenerate={vi.fn()}
          onOpenAgent={onOpenAgent}
          renderAgentTemplate={renderAgentTemplate}
        />
      </QueryClientProvider>
    );
    const view = render(card(asset));

    const assetMentions = screen.getAllByRole('button', {
      name: /预览提示词资产 林黛玉|Preview prompt asset 林黛玉/,
    });
    expect(assetMentions).toHaveLength(1);
    expect(assetMentions[0]).not.toHaveClass('is-implicit');
    fireEvent.click(assetMentions[0]!);
    const dialog = screen.getByRole('dialog', { name: /林黛玉/ });
    expect(screen.getByDisplayValue('古典人物，白色衣裙')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: /Generate image|生成图片/i })).toBeInTheDocument();
    expect(within(dialog).getByRole('status')).toHaveTextContent(/正在生成图片|Generating image/);
    expect(within(dialog).getByRole('status')).not.toHaveTextContent('Rendering image');
    fireEvent.click(within(dialog).getByRole('button', { name: /Agent 修改|Edit with Agent/i }));
    expect(renderAgentTemplate).toHaveBeenCalledWith('asset.edit_prompt', {
      name: '林黛玉',
      asset_id: 'asset_lindaiyu',
    });
    expect(onOpenAgent).toHaveBeenCalledWith('asset.edit_prompt:asset_lindaiyu:林黛玉');

    view.rerender(card({ ...asset, prompt: 'Agent 更新后的视觉提示词', revision: 2 }));
    expect(screen.getByDisplayValue('Agent 更新后的视觉提示词')).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole('button', { name: /Close|关闭/i }));
    editVideoPrompt();
    const editor = screen.getByLabelText(/Video prompt|视频提示词/i) as HTMLTextAreaElement;
    const tokenStart = editor.value.indexOf('@林黛玉');
    editor.focus();
    editor.setSelectionRange(tokenStart, tokenStart + '@林黛玉'.length);
    expect(editor.selectionStart).toBe(tokenStart);
    expect(editor.selectionEnd).toBe(tokenStart + '@林黛玉'.length);
    fireEvent.change(editor, {
      target: {
        value: editor.value.replace('@林黛玉', ''),
        selectionStart: tokenStart,
        selectionEnd: tokenStart,
      },
    });
    expect(editor.value).not.toContain('@林黛玉');
  });

  it('opens uploaded reference images in a large preview', async () => {
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{
          ...clip,
          video_prompts: {
            ...clip.video_prompts,
            reference: '@人物参考.png 与 @场景参考.png',
          },
        }}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        persistedReferenceMedia={[
          {
            filename: '人物参考.png',
            artifact: {
              artifact_id: 'artifact_image',
              kind: 'image',
              mime_type: 'image/png',
              original_filename: '人物参考.png',
              byte_size: 1024,
              width: 720,
              height: 1280,
              duration_ms: 0,
              created_at: '2026-09-10T00:00:00Z',
            },
            reference: { role: 'image_reference', artifact_id: 'artifact_image' },
          },
          {
            filename: '场景参考.png',
            artifact: {
              artifact_id: 'artifact_location',
              kind: 'image',
              mime_type: 'image/png',
              original_filename: '场景参考.png',
              byte_size: 2048,
              width: 1280,
              height: 720,
              duration_ms: 0,
              created_at: '2026-09-10T00:00:00Z',
            },
            reference: { role: 'image_reference', artifact_id: 'artifact_location' },
          },
        ]}
        onOpenAgent={vi.fn()}
      />,
    );

    const previewButton = screen.getByRole('button', {
      name: /预览提示词资产 人物参考\.png|Preview prompt asset 人物参考\.png/,
    });
    fireEvent.click(previewButton);
    const preview = await screen.findByRole('dialog', {
      name: /放大预览人物参考\.png|Preview 人物参考\.png/,
    });
    expect(preview).toHaveAttribute('open');
    fireEvent.click(screen.getByRole('button', { name: /Close|关闭/i }));
    fireEvent.click(
      screen.getByRole('button', {
        name: /预览提示词资产 场景参考\.png|Preview prompt asset 场景参考\.png/,
      }),
    );
    expect(screen.getByRole('dialog', { name: /场景参考\.png/ })).toHaveAttribute('open');
  });

  it('shows uploads first in @ suggestions and removes a prompt reference immediately', async () => {
    const onSave = vi.fn();
    const uploaded = {
      episode_id: 'episode_1',
      name: '构图参考',
      description: '',
      usage_count: 1,
      revision: 1,
      created_at: '2026-09-10T00:00:00Z',
      updated_at: '2026-09-10T00:00:00Z',
      artifact: {
        artifact_id: 'artifact_library_image',
        kind: 'image' as const,
        mime_type: 'image/png',
        original_filename: '构图参考.png',
        byte_size: 1024,
        width: 720,
        height: 1280,
        duration_ms: 0,
        created_at: '2026-09-10T00:00:00Z',
      },
    };
    const promptClip = {
      ...clip,
      video_prompt: '人物参考@构图参考。',
      video_prompts: { ...clip.video_prompts, reference: '人物参考@构图参考。' },
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={promptClip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[
          {
            asset: {
              asset_id: 'asset_generated',
              identity: { name: '林黛玉', kind: 'character' },
              prompt: '古典人物',
              primary_image_slot_id: '',
            },
            media: undefined,
          } as never,
        ]}
        availableUploadedMedia={[uploaded]}
        persistedReferenceMedia={[
          {
            filename: uploaded.name,
            artifact: uploaded.artifact,
            reference: {
              role: 'image_reference',
              artifact_id: uploaded.artifact.artifact_id,
              name: uploaded.name,
            },
          },
        ]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={onSave}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expandVideoPrompt();
    expect(
      screen.getByRole('button', {
        name: /Preview prompt asset 构图参考|预览提示词资产 构图参考/,
      }),
    ).toBeInTheDocument();
    expect(document.querySelector('.clip-focus-prompt-editor__preview')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /^Edit$|^编辑$/i }));
    const editor = screen.getByLabelText(/Video prompt|视频提示词/i);
    expect(editor.closest('.asset-mention-input')).toHaveClass('is-editing');
    fireEvent.change(editor, { target: { value: '@', selectionStart: 1 } });
    const options = screen.getAllByRole('option');
    expect(options[0]).toHaveTextContent('@构图参考');
    expect(options[1]).toHaveTextContent('@林黛玉');
    expect(
      screen.queryByRole('button', {
        name: /Preview prompt asset 构图参考|预览提示词资产 构图参考/,
      }),
    ).not.toBeInTheDocument();
    fireEvent.change(editor, { target: { value: '人物不再使用参考。' } });
    await waitFor(() => expect(onSave).toHaveBeenCalled(), { timeout: 1_500 });
  });

  it('previews prompt references while collapsed and exposes one native text layer while editing', () => {
    const uploaded = {
      episode_id: 'episode_1',
      name: '构图参考',
      description: '',
      usage_count: 1,
      revision: 1,
      created_at: '2026-09-10T00:00:00Z',
      updated_at: '2026-09-10T00:00:00Z',
      artifact: {
        artifact_id: 'artifact_library_image',
        kind: 'image' as const,
        mime_type: 'image/png',
        original_filename: '构图参考.png',
        byte_size: 1024,
        width: 720,
        height: 1280,
        duration_ms: 0,
        created_at: '2026-09-10T00:00:00Z',
      },
    };
    const promptClip = {
      ...clip,
      video_prompt: '@林黛玉走入@构图参考中的庭院。',
      video_prompts: { ...clip.video_prompts, reference: '@林黛玉走入@构图参考中的庭院。' },
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={promptClip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[
          {
            asset: {
              asset_id: 'asset_lindaiyu',
              identity: { name: '林黛玉', kind: 'character' },
              prompt: '古典人物',
              primary_image_slot_id: 'slot_lindaiyu',
            },
            media: media('slot_lindaiyu', 'artifact_lindaiyu'),
          } as never,
        ]}
        availableUploadedMedia={[uploaded]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    const assetMention = screen.getByRole('button', {
      name: /预览提示词资产 林黛玉|Preview prompt asset 林黛玉/,
    });
    const uploadMention = screen.getByRole('button', {
      name: /预览提示词资产 构图参考|Preview prompt asset 构图参考/,
    });
    expect(assetMention.closest('.clip-focus-prompt-editor__summary')).toBeInTheDocument();
    expect(uploadMention).toHaveTextContent('@构图参考');

    fireEvent.click(uploadMention);
    expect(screen.getByRole('dialog', { name: /放大预览构图参考|Preview 构图参考/ })).toHaveAttribute('open');
    fireEvent.click(screen.getByRole('button', { name: /Close|关闭/i }));

    expandVideoPrompt();
    const expandedUploadMention = screen.getByRole('button', {
      name: /预览提示词资产 构图参考|Preview prompt asset 构图参考/,
    });
    fireEvent.click(expandedUploadMention);
    expect(screen.getByRole('dialog', { name: /放大预览构图参考|Preview 构图参考/ })).toHaveAttribute('open');
    fireEvent.click(screen.getByRole('button', { name: /Close|关闭/i }));
    fireEvent.click(screen.getByRole('button', { name: /^Edit$|^编辑$/i }));
    const editor = screen.getByLabelText(/Video prompt|视频提示词/i);
    expect(editor.closest('.asset-mention-input')).toHaveClass('is-editing');
    expect(document.querySelector('.asset-mention-highlight')).not.toBeInTheDocument();
    fireEvent.change(editor, { target: { value: '走入庭院。', selectionStart: 5 } });
    expect(editor).toHaveValue('走入庭院。');
  });

  it('switches among all three modes and submits the visible keyframe task with pending edits', async () => {
    const onGenerate = vi.fn();
    const onOpenAgent = vi.fn();
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={media('slot_first', 'artifact_first')}
        lastMedia={media('slot_last', 'artifact_last')}
        videoMedia={media('slot_video', 'artifact_video')}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={onGenerate}
        onOpenAgent={onOpenAgent}
      />,
    );

    expect(screen.getByRole('tab', { name: /Input preview|输入预览/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(
      Array.from(document.querySelector('.clip-focus-view')!.children).map((element) => element.classList[0]),
    ).toEqual(['clip-focus-prompt-editor', 'clip-focus-generation']);
    expect(document.querySelector('.clip-focus-card__media')).not.toBeInTheDocument();
    expect(document.querySelector('.clip-focus-result')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Edit with Agent|Agent 修改/i }));
    expect(onOpenAgent).toHaveBeenLastCalledWith(expect.stringContaining('clip_1'));
    fireEvent.click(screen.getByRole('tab', { name: /Video result|视频结果/i }));
    expect(document.querySelector('.clip-focus-result video')).toBeInTheDocument();
    expect(document.querySelector('.clip-focus-reference')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /Input preview|输入预览/i }));

    selectGenerationMode('first_image');
    expect(
      Array.from(document.querySelector('.clip-focus-view')!.children).map((element) => element.classList[0]),
    ).toEqual(['clip-focus-prompt-editor', 'clip-focus-generation', 'clip-focus-keyframes']);
    expect(document.querySelector('.keyframe-preview')).not.toBeInTheDocument();
    expect(document.querySelectorAll('.generation-frame-input')).toHaveLength(1);
    expect(await screen.findByLabelText(/Upload first frame|上传首帧/i)).toBeInTheDocument();
    const firstFrame = document.querySelector<HTMLElement>('.generation-frame-input')!;
    const promptPanel = firstFrame.querySelector<HTMLElement>('.generation-frame-prompt-panel')!;
    expect(promptPanel).toContainElement(
      within(firstFrame).getByRole('button', { name: /Generate first frame|生成首帧/i }),
    );
    expect(promptPanel).toContainElement(firstFrame.querySelector('.generation-frame-prompt-summary'));
    fireEvent.click(within(firstFrame).getByRole('button', { name: /Preview first frame|放大预览首帧/i }));
    expect(screen.getByRole('dialog', { name: /Preview first frame|放大预览首帧/i })).toHaveAttribute('open');
    fireEvent.click(screen.getByRole('button', { name: /Close|关闭/i }));
    fireEvent.click(within(firstFrame).getByRole('button', { name: /^(Edit|编辑)$/i }));
    const firstFrameHeaderActions = firstFrame.querySelector<HTMLElement>(
      '.generation-frame-input__header-actions',
    )!;
    const firstFrameAgentAction = within(firstFrame).getByRole('button', {
      name: /Edit with Agent|Agent 修改/i,
    });
    expect(firstFrameHeaderActions).toContainElement(firstFrameAgentAction);
    expect(firstFrameHeaderActions).toContainElement(
      within(firstFrame).getByRole('button', { name: /^(Collapse|收起)$/i }),
    );
    expect(firstFrameHeaderActions).toContainElement(
      firstFrame.querySelector('.generation-frame-input__status'),
    );
    fireEvent.click(firstFrameAgentAction);
    expect(onOpenAgent).toHaveBeenLastCalledWith(expect.stringContaining('first_image_prompt'));
    fireEvent.click(screen.getByRole('button', { name: /Generate first frame|生成首帧/i }));

    expect(onGenerate).toHaveBeenLastCalledWith(
      'first_image',
      'image.codex',
      expect.objectContaining({ generation_mode: 'first_image' }),
      [],
      undefined,
    );

    selectGenerationMode('first_last_image');
    expect(
      Array.from(document.querySelector('.clip-focus-view')!.children).map((element) => element.classList[0]),
    ).toEqual(['clip-focus-prompt-editor', 'clip-focus-generation', 'clip-focus-keyframes']);
    expect(document.querySelector('.keyframe-preview')).not.toBeInTheDocument();
    expect(document.querySelectorAll('.generation-frame-input')).toHaveLength(2);
    expect(await screen.findByLabelText(/Upload last frame|上传尾帧/i)).toBeInTheDocument();
    const frameInputs = document.querySelectorAll('.generation-frame-input');
    fireEvent.click(within(frameInputs[1] as HTMLElement).getByRole('button', { name: /^(Edit|编辑)$/i }));
    fireEvent.click(
      within(frameInputs[1] as HTMLElement).getByRole('button', {
        name: /Edit with Agent|Agent 修改/i,
      }),
    );
    expect(onOpenAgent).toHaveBeenLastCalledWith(expect.stringContaining('last_image_prompt'));
    fireEvent.click(screen.getByRole('button', { name: /Generate last frame|生成尾帧/i }));

    expect(onGenerate).toHaveBeenLastCalledWith(
      'last_image',
      'image.codex',
      expect.objectContaining({ generation_mode: 'first_last_image' }),
      [],
      undefined,
    );
  });

  it.each(['slot_first', 'slot_last'])(
    'shows insufficient balance and recharge for failed frame %s',
    (slotID) => {
      const task: TaskItem = {
        task_type: 'generation_job',
        task_id: 'job_balance',
        generation_batch_id: 'batch_balance',
        project_id: 'project_1',
        episode_id: 'episode_1',
        target_media_slot_id: slotID,
        target_media_kind: 'image',
        generator_id: 'image.shotforward',
        state: 'failed',
        progress_stage: 'failed',
        progress_message: '',
        failure_code: 'shotforward_insufficient_points',
        failure_message: 'raw upstream',
        revision: 2,
        created_at: '2026-09-10T10:00:00Z',
        updated_at: '2026-09-10T10:00:01Z',
      };
      render(
        <ClipFocusCard
          projectID="project_1"
          clip={{ ...clip, generation_mode: 'first_last_image' }}
          firstMedia={undefined}
          lastMedia={undefined}
          videoMedia={undefined}
          references={[]}
          assetGenerationTasks={new Map([[slotID, task]])}
          videoProviders={[]}
          saving={false}
          busy={false}
          error={false}
          notice=""
          onSave={vi.fn()}
          onGenerate={vi.fn()}
          onOpenAgent={vi.fn()}
        />,
      );
      expect(screen.getByRole('alert')).toHaveTextContent(/余额不足|balance is insufficient/);
      expect(screen.getByRole('button', { name: /去充值|Recharge/ })).toBeInTheDocument();
      expect(screen.queryByText('raw upstream')).not.toBeInTheDocument();
    },
  );

  it('shows the first-frame banner as generating while its image task is active', () => {
    const task: TaskItem = {
      task_type: 'generation_job',
      task_id: 'job_first_frame',
      generation_batch_id: 'batch_first_frame',
      project_id: 'project_1',
      episode_id: 'episode_1',
      target_media_slot_id: 'slot_first',
      target_media_kind: 'image',
      generator_id: 'image.codex',
      state: 'running',
      progress_stage: 'generating',
      progress_message: '',
      failure_code: '',
      revision: 2,
      created_at: '2026-09-10T10:00:00Z',
      updated_at: '2026-09-10T10:00:01Z',
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{ ...clip, generation_mode: 'first_image' }}
        firstMedia={media('slot_first', 'artifact_first')}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        assetGenerationTasks={new Map([['slot_first', task]])}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    const frameInput = document.querySelector('.generation-frame-input') as HTMLElement;
    expect(frameInput).toHaveClass('is-generating');
    expect(within(frameInput).getByRole('status')).toHaveTextContent(/Generating|生成中/i);
    expect(within(frameInput).getByRole('button', { name: /Generating|生成中/i })).toBeDisabled();
  });

  it('blocks a sixth Codex frame reference before submission', () => {
    const onGenerate = vi.fn();
    const first = media('slot_first', 'artifact_first');
    const reference = (name: string, suffix: string) => {
      const value = media(`slot_${suffix}`, `artifact_${suffix}`);
      value.artifacts[0] = {
        ...value.artifacts[0],
        kind: 'image',
        mime_type: 'image/png',
      } as never;
      return {
        asset: {
          asset_id: `asset_${suffix}`,
          identity: { name, kind: 'character' },
          primary_image_slot_id: `slot_${suffix}`,
        },
        media: value,
      } as never;
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{
          ...clip,
          generation_mode: 'first_image',
          first_image_prompt: '@甲 @乙 @丙 @丁 @戊 @己',
        }}
        firstMedia={first}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[
          reference('甲', 'a'),
          reference('乙', 'b'),
          reference('丙', 'c'),
          reference('丁', 'd'),
          reference('戊', 'e'),
          reference('己', 'f'),
        ]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={onGenerate}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(screen.getByRole('alert')).toHaveTextContent(/最多支持 5 张|supports up to 5/i);
    const frameInput = document.querySelector('.generation-frame-input') as HTMLElement;
    fireEvent.click(within(frameInput).getByRole('button', { name: /^(Edit|编辑)$/i }));
    expect(
      within(frameInput).getByRole('button', { name: /Add reference image|添加参考图/i }),
    ).toBeDisabled();
    expect(frameInput.querySelectorAll('.generation-frame-reference.is-over-limit')).toHaveLength(1);
    expect(screen.getByRole('button', { name: /Generate first frame|生成首帧/i })).toBeDisabled();
    expect(onGenerate).not.toHaveBeenCalled();
  });

  it('keeps the preferred Codex image service for multiple references', () => {
    setImageGeneratorPreference('image.codex');
    const onGenerate = vi.fn();
    const reference = (name: string, suffix: string) => {
      const value = media(`slot_${suffix}`, `artifact_${suffix}`);
      value.artifacts[0] = {
        ...value.artifacts[0],
        kind: 'image',
        mime_type: 'image/png',
      } as never;
      return {
        asset: {
          asset_id: `asset_${suffix}`,
          identity: { name, kind: 'character' },
          primary_image_slot_id: `slot_${suffix}`,
        },
        media: value,
      };
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{ ...clip, generation_mode: 'first_image', first_image_prompt: '@甲与@乙并肩站立' }}
        firstMedia={media('slot_first', 'artifact_first')}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[reference('甲', 'a'), reference('乙', 'b')] as never}
        videoProviders={[]}
        images={[
          {
            generator_id: 'image.codex',
            enabled: true,
            health: 'ready',
          } as never,
          {
            generator_id: 'image.shotforward',
            enabled: true,
            health: 'ready',
            model_id: 'image-cloud',
            models: [{ model_id: 'image-cloud', is_default: true, health: 'ready' }],
          } as never,
        ]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={onGenerate}
        onOpenAgent={vi.fn()}
      />,
    );

    const frameInput = document.querySelector('.generation-frame-input') as HTMLElement;
    fireEvent.click(within(frameInput).getByRole('button', { name: /^(Edit|编辑)$/i }));
    expect(within(frameInput).getByText(/Codex ·/)).toHaveTextContent(/5/);
    fireEvent.click(screen.getByRole('button', { name: /Generate first frame|生成首帧/i }));
    expect(onGenerate).toHaveBeenCalledWith('first_image', 'image.codex', undefined, [], undefined);
  });

  it('shows the next video unit selected first frame as the default last frame', () => {
    const nextFirst = media('slot_next_first', 'artifact_next_first');
    const emptyLast = media('slot_last', 'unused');
    emptyLast.slot.current_media_version_id = '';
    emptyLast.versions = [];
    emptyLast.artifacts = [];
    nextFirst.artifacts[0] = {
      artifact_id: 'artifact_next_first',
      kind: 'image',
      mime_type: 'image/png',
      original_filename: 'next-first.png',
      byte_size: 1_000,
      width: 1280,
      height: 720,
      duration_ms: 0,
      created_at: '2026-09-07T00:00:00Z',
    };
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={{ ...clip, generation_mode: 'first_last_image' }}
        firstMedia={media('slot_first', 'artifact_first')}
        lastMedia={emptyLast}
        videoMedia={media('slot_video', 'artifact_video')}
        nextClip={{
          ...clip,
          clip_id: 'clip_next',
          position: 2,
          title: '下一镜',
          generation_mode: 'first_image',
        }}
        nextFirstMedia={nextFirst}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(
      screen.getByText(/关联 02 · 下一镜已选定的首帧|Linked to the selected first frame of 02 · 下一镜/i),
    ).toBeInTheDocument();
    const lastFrame = document.querySelectorAll<HTMLElement>('.generation-frame-input')[1]!;
    expect(lastFrame).toHaveClass('is-linked');
    expect(lastFrame.querySelector('.generation-frame-input__status')).toHaveTextContent(/Linked|已关联/i);
    expect(
      within(lastFrame).getByRole('button', { name: /Preview last frame|放大预览尾帧/i }),
    ).toHaveAttribute('type', 'button');
    expect(lastFrame.querySelector('img')).toHaveAttribute(
      'src',
      '/api/v1/projects/project_1/artifacts/artifact_next_first/content',
    );
  });

  it('automatically saves video prompt and duration edits', async () => {
    const onSave = vi.fn();
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={onSave}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    editVideoPrompt();
    fireEvent.change(screen.getByLabelText(/Video prompt|视频提示词/i), {
      target: { value: '人物快速转身，镜头向前推进。' },
    });
    fireEvent.change(screen.getByLabelText(/Planned duration|计划时长/i), {
      target: { value: '12' },
    });
    expect(screen.queryByRole('button', { name: /Save prompt and mode|保存提示词与模式/i })).toBeNull();
    await waitFor(
      () =>
        expect(onSave).toHaveBeenCalledWith(
          expect.objectContaining({
            planned_duration_seconds: 12,
            video_prompts: expect.objectContaining({ reference: '人物快速转身，镜头向前推进。' }),
          }),
        ),
      { timeout: 1_500 },
    );
  });

  it('keeps mode prompts independent and restores a previous draft when switching back', () => {
    const onSave = vi.fn();
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        videoProviders={[]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={onSave}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    editVideoPrompt();
    selectGenerationMode('first_image');
    expect(screen.getByLabelText(/Video prompt|视频提示词/i)).toHaveValue('');
    fireEvent.change(screen.getByLabelText(/Video prompt|视频提示词/i), {
      target: { value: '首帧模式专属提示词' },
    });
    selectGenerationMode('reference');
    expect(screen.getByLabelText(/Video prompt|视频提示词/i)).toHaveValue('人物缓缓回头。');
    selectGenerationMode('first_image');
    expect(screen.getByLabelText(/Video prompt|视频提示词/i)).toHaveValue('首帧模式专属提示词');
  });

  it('keeps reference mode focused on @ mentions without a separate reference-material section', () => {
    render(
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        videoProviders={[{ generator_id: 'video.seedance-2.5.byteplus-global' } as never]}
        saving={false}
        busy={false}
        error={false}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />,
    );

    expect(document.querySelector('.clip-focus-reference')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^(Manage|管理)$/i })).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Input preview|输入预览/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('shows the actual generation failure instead of reporting every error as a save conflict', () => {
    const card = (error: unknown, errorSource: 'save' | 'generation') => (
      <ClipFocusCard
        projectID="project_1"
        clip={clip}
        firstMedia={undefined}
        lastMedia={undefined}
        videoMedia={undefined}
        references={[]}
        videoProviders={[{ generator_id: 'video.wan-3.0.official' } as never]}
        saving={false}
        busy={false}
        error={error}
        errorSource={errorSource}
        notice=""
        onSave={vi.fn()}
        onGenerate={vi.fn()}
        onOpenAgent={vi.fn()}
      />
    );
    const view = render(
      card(
        new APIError(422, {
          error: { code: 'capability_unsupported', message: 'unsupported request' },
        }),
        'generation',
      ),
    );

    expect(screen.getByRole('alert')).toHaveTextContent(/does not support|不支持/i);

    view.rerender(
      card(
        new APIError(422, {
          error: {
            code: 'capability_unsupported',
            message: 'unsupported request',
            details: {
              constraint: 'reference_total_duration',
              role: 'audio_reference',
              actual: 29832,
              minimum: 0,
              maximum: 15000,
            },
          },
        }),
        'generation',
      ),
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/29\.832/);
    expect(screen.getByRole('alert')).toHaveTextContent(/15/);
    expect(screen.getByRole('alert')).toHaveTextContent(/audio|音频/i);

    view.rerender(
      card(
        new APIError(500, {
          error: { code: 'internal_error', message: 'failed', request_id: 'req_video_123' },
        }),
        'generation',
      ),
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/internal_error/);
    expect(screen.getByRole('alert')).toHaveTextContent(/req_video_123/);

    view.rerender(
      card(
        new APIError(409, {
          error: { code: 'revision_conflict', message: 'changed' },
        }),
        'save',
      ),
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/another window|其他窗口/i);
  });
});
