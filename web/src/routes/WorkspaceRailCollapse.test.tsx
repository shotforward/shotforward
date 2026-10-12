import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { EpisodePreviewPage } from './EpisodePreviewPage';
import { ScriptWorkspacePage } from './ScriptWorkspacePage';
import { StoryboardWorkspacePage } from './StoryboardWorkspacePage';

const mocks = vi.hoisted(() => ({
  getEpisode: vi.fn(),
  importScript: vi.fn(),
  getStoryboard: vi.fn(),
  createScene: vi.fn(),
  createClip: vi.fn(),
  deleteClip: vi.fn(),
  listEpisodeAssets: vi.fn(),
  getMediaSlot: vi.fn(),
  updateVideoTrim: vi.fn(),
  listProviders: vi.fn(),
  getExportPreview: vi.fn(),
  submitExport: vi.fn(),
  getTaskSnapshot: vi.fn(),
  getAgentTemplateSettings: vi.fn(),
}));

const storyboardVideoProvider = {
  generator_id: 'video.h3.shotforward',
  model_id: 'h3-ref2va',
  enabled: true,
  health: 'ready',
  models: [
    { model_id: 'h3-ref2va', health: 'ready' },
    { model_id: 'h3-fl2va', health: 'ready' },
  ],
};

vi.mock('../api/agentTemplates', () => ({
  getAgentTemplateSettings: mocks.getAgentTemplateSettings,
}));
vi.mock('../api/episodes', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/episodes')>();
  return { ...original, getEpisode: mocks.getEpisode, importScript: mocks.importScript };
});
vi.mock('../api/storyboard', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/storyboard')>();
  return {
    ...original,
    getStoryboard: mocks.getStoryboard,
    createScene: mocks.createScene,
    createClip: mocks.createClip,
    deleteClip: mocks.deleteClip,
  };
});
vi.mock('../api/assets', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/assets')>();
  return {
    ...original,
    listEpisodeAssets: mocks.listEpisodeAssets,
    getMediaSlot: mocks.getMediaSlot,
    updateVideoTrim: mocks.updateVideoTrim,
  };
});
vi.mock('../api/providers', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/providers')>();
  return { ...original, listProviders: mocks.listProviders };
});
vi.mock('../api/export', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/export')>();
  return {
    ...original,
    getExportPreview: mocks.getExportPreview,
    submitExport: mocks.submitExport,
  };
});
vi.mock('../api/tasks', () => ({
  getTaskSnapshot: mocks.getTaskSnapshot,
  useTaskEvents: vi.fn(),
}));
vi.mock('../components/AgentDock', () => ({
  AgentDock: ({
    draftRequest,
    videoPromptTarget,
  }: {
    draftRequest?: { text: string };
    videoPromptTarget?: unknown;
  }) => (
    <aside data-testid="agent-draft" data-video-prompt-target={JSON.stringify(videoPromptTarget)}>
      {draftRequest?.text ?? 'Agent'}
    </aside>
  ),
}));

function renderRoute(path: string, element: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/projects/:projectId/episodes/:episodeId/*" element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

describe('workspace navigation rails', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.getMediaSlot.mockReset();
    delete window.__SHOTFORWARD_DESKTOP__;
    mocks.getEpisode.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episode: {
        episode_id: 'episode_1',
        title: '第 1 集',
        script: { markdown: '## 第一场', revision: 1 },
      },
      episodes: [],
    });
    mocks.importScript.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episode: {
        episode_id: 'episode_1',
        title: '第 1 集',
        script: { markdown: '## 导入剧本', revision: 2 },
      },
      episodes: [],
    });
    mocks.getStoryboard.mockResolvedValue({
      storyboard: { scenes: [], planned_duration_ms: 0, revision: 1 },
      duration_warning: '',
    });
    mocks.createScene.mockResolvedValue({
      scene: { scene_id: 'scene_new', position: 1, title: '新场景', description: '', revision: 1, clips: [] },
    });
    mocks.createClip.mockResolvedValue({ clip: { clip_id: 'clip_new' } });
    mocks.deleteClip.mockResolvedValue({});
    mocks.listEpisodeAssets.mockResolvedValue({ assets: [] });
    mocks.listProviders.mockResolvedValue({ providers: [] });
    mocks.getAgentTemplateSettings.mockImplementation((locale: string) =>
      Promise.resolve({ locale, overrides: {}, revision: 1 }),
    );
    mocks.getTaskSnapshot.mockResolvedValue({ stream_epoch: 'epoch_1', cursor: '1', tasks: [] });
    mocks.submitExport.mockResolvedValue({
      job: { export_job_id: 'export_1', project_id: 'project_1', episode_id: 'episode_1' },
    });
    mocks.getExportPreview.mockResolvedValue({
      preview: {
        storyboard_revision: 1,
        export_duration_ms: 0,
        included_clip_count: 0,
        skipped_clip_count: 0,
        output_width: 1920,
        output_height: 1080,
        clips: [],
        skipped_clips: [],
      },
    });
  });

  it('collapses from the outline header and reopens from the document toolbar', async () => {
    renderRoute('/projects/project_1/episodes/episode_1/script', <ScriptWorkspacePage />);

    const collapse = await screen.findByRole('button', {
      name: /收起内容结构|Collapse content outline/i,
    });
    const workspaceNavigation = screen.getByRole('navigation', { name: /视频创作|Video creation/i });
    expect(workspaceNavigation).not.toHaveTextContent(/概览|Overview/i);
    expect(workspaceNavigation.querySelector('a')).toHaveTextContent(/创作内容|Content/i);
    expect(document.querySelector('.creative-heading__project')).not.toBeInstanceOf(HTMLAnchorElement);
    expect(collapse.closest('.scene-outline > header')).toBeInTheDocument();
    expect(
      screen
        .getByRole('button', { name: /识别所需资产|Identify needed assets/i })
        .closest('.script-toolbar__actions'),
    ).toBeInTheDocument();
    expect(document.querySelector('.creative-heading__actions')).not.toBeInTheDocument();
    fireEvent.click(collapse);
    expect(document.querySelector('.creative-layout')).toHaveClass('is-outline-collapsed');
    expect(document.querySelector('.scene-outline')).toHaveClass('is-collapsed');

    const expand = screen.getByRole('button', { name: /展开内容结构|Expand content outline/i });
    expect(expand.closest('.script-toolbar')).toBeInTheDocument();
    fireEvent.click(expand);
    expect(document.querySelector('.creative-layout')).not.toHaveClass('is-outline-collapsed');
  });

  it('renders Markdown as a formatted preview and keeps source editing explicit', async () => {
    renderRoute('/projects/project_1/episodes/episode_1/script', <ScriptWorkspacePage />);

    expect(await screen.findByRole('heading', { level: 2, name: '第一场' })).toBeInTheDocument();
    expect(
      screen.queryByRole('textbox', { name: /创作内容编辑器|Creative content editor/i }),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /编辑|Edit/i }));
    expect(screen.getByRole('textbox', { name: /创作内容编辑器|Creative content editor/i })).toHaveValue(
      '## 第一场',
    );
  });

  it('offers script starting paths plus direct asset and storyboard routes for an empty script', async () => {
    mocks.getEpisode.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episode: {
        episode_id: 'episode_1',
        title: '黛玉葬花',
        script: { markdown: '', revision: 1 },
      },
      episodes: [],
    });
    renderRoute('/projects/project_1/episodes/episode_1/script', <ScriptWorkspacePage />);

    const heading = await screen.findByRole('heading', {
      name: /开始创作“黛玉葬花”|Start creating “黛玉葬花”/i,
    });
    const emptyState = heading.closest('.script-empty-state') as HTMLElement;
    const actions = within(emptyState).getAllByRole('button');
    expect(actions).toHaveLength(3);
    expect(actions.every((button) => button.parentElement?.classList.contains('script-empty-actions'))).toBe(
      true,
    );
    expect(within(emptyState).getByRole('link', { name: /创建资产|Create assets/i })).toHaveAttribute(
      'href',
      '/projects/project_1/episodes/episode_1/assets',
    );
    expect(within(emptyState).getByRole('link', { name: /创建故事板|Create storyboard/i })).toHaveAttribute(
      'href',
      '/projects/project_1/episodes/episode_1/storyboard',
    );
    expect(screen.queryByRole('group', { name: /内容显示模式|Content display mode/i })).toBeNull();

    fireEvent.click(within(emptyState).getByRole('button', { name: /与 Agent 一起创作|Create with Agent/i }));
    expect(screen.getByTestId('agent-draft')).toHaveTextContent(/创作内容|creative content/i);

    fireEvent.click(
      within(emptyState).getByRole('button', { name: /导入已有内容|Import existing content/i }),
    );
    const dialog = screen.getByRole('dialog', { name: /从本机文件导入|Import from a local file/i });
    expect(dialog).toBeInTheDocument();
    const cancelImport = within(dialog).getAllByRole('button', { name: /取消|Cancel/i })[0];
    expect(cancelImport).toBeDefined();
    fireEvent.click(cancelImport!);

    fireEvent.click(within(emptyState).getByRole('button', { name: /直接编写|Write directly/i }));
    const editor = screen.getByRole('textbox', {
      name: /创作内容编辑器|Creative content editor/i,
    });
    await waitFor(() => expect(editor).toHaveFocus());
  });

  it('opens the native script picker and immediately imports the selected file', async () => {
    const selectScriptFile = vi.fn().mockResolvedValue('/Users/creator/Documents/script.md');
    window.__SHOTFORWARD_DESKTOP__ = {
      selectProjectParent: vi.fn(),
      selectScriptFile,
      openExternal: vi.fn(),
    };
    renderRoute('/projects/project_1/episodes/episode_1/script', <ScriptWorkspacePage />);

    fireEvent.click(await screen.findByRole('button', { name: /导入内容|Import content/i }));

    await waitFor(() => expect(selectScriptFile).toHaveBeenCalledOnce());
    await waitFor(() =>
      expect(mocks.importScript).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        '/Users/creator/Documents/script.md',
        1,
      ),
    );
    expect(screen.queryByRole('dialog', { name: /从本机文件导入|Import from a local file/i })).toBeNull();
  });

  it('collapses from the scene header and reopens from the storyboard toolbar', async () => {
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '灯暖人孤',
            description: '幕内，贾母正房。',
            clips: [],
            revision: 1,
          },
        ],
        planned_duration_ms: 11_000,
        revision: 1,
      },
      duration_warning: 'under_target',
    });
    renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);

    const collapse = await screen.findByRole('button', {
      name: /收起场景列表|Collapse scene list/i,
    });
    expect(collapse.closest('.scene-rail > header')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: /增加视频单元|Add video unit/i }).closest('.clip-board__toolbar'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: /Agent 续写分镜|Continue with Agent/i }),
    ).not.toBeInTheDocument();
    expect(document.querySelector('.scene-rail__heading')).toHaveTextContent(/0:11/);
    expect(document.querySelector('.scene-rail > button small')).toHaveTextContent(/0:00/);
    expect(document.querySelector('.scene-heading-meta')).toHaveTextContent(/0:00/);
    expect(document.querySelector('.storyboard-meta')).not.toBeInTheDocument();
    fireEvent.click(collapse);
    expect(document.querySelector('.storyboard-layout')).toHaveClass('is-scene-rail-collapsed');
    expect(document.querySelector('.scene-rail')).toHaveClass('is-collapsed');

    const expand = screen.getByRole('button', { name: /展开场景列表|Expand scene list/i });
    expect(expand.closest('.storyboard-rail-toolbar')).toBeInTheDocument();
    fireEvent.click(expand);
    expect(document.querySelector('.storyboard-layout')).not.toHaveClass('is-scene-rail-collapsed');
  });

  it.each(['附加要求：', 'Additional requirements:'])(
    'keeps %s and user text last when adding before or after a Clip',
    async (heading) => {
      const supplement = `${heading}\nKeep the existing costumes.\nPreserve the closing dialogue.\n`;
      mocks.getAgentTemplateSettings.mockResolvedValue({
        locale: 'en',
        overrides: {
          'storyboard.add_clip': `Add a unit to {{scene_title}} (scene_id: {{scene_id}}).\n\n${supplement}`,
        },
        revision: 1,
      });
      mocks.listProviders.mockResolvedValue({ providers: [storyboardVideoProvider] });
      const clip = (id: string, position: number, title: string) => ({
        clip_id: id,
        project_id: 'project_1',
        episode_id: 'episode_1',
        scene_id: 'scene_1',
        position,
        title,
        description: '',
        dialogue: '',
        creative_intent: '',
        planned_duration_ms: 8_000,
        first_image_prompt: '',
        last_image_prompt: '',
        video_prompt: '',
        video_prompts: { first_image: '', first_last_image: '', reference: '' },
        generation_mode: 'reference',
        revision: 1,
        first_image_slot_id: '',
        last_image_slot_id: '',
        video_slot_id: '',
        reference_audio_slot_id: '',
        asset_references: [],
        derived_from_clip_ids: [],
        created_at: '2026-09-13T00:00:00Z',
        updated_at: '2026-09-13T00:00:00Z',
      });
      mocks.getStoryboard.mockResolvedValue({
        storyboard: {
          scenes: [
            {
              scene_id: 'scene_1',
              position: 1,
              title: '园门初见',
              description: '',
              clips: [clip('clip_1', 1, '开门见园'), clip('clip_2', 2, '空庭答谢')],
              revision: 1,
            },
          ],
          planned_duration_ms: 16_000,
          revision: 1,
        },
        duration_warning: '',
      });
      renderRoute(
        '/projects/project_1/episodes/episode_1/storyboard/clips/clip_1',
        <StoryboardWorkspacePage />,
      );
      await waitFor(() =>
        expect(screen.getByTestId('agent-draft')).toHaveAttribute(
          'data-video-prompt-target',
          expect.stringContaining('video.h3.shotforward'),
        ),
      );

      fireEvent.click(await screen.findByRole('button', { name: /在此后添加|Add after this/i }));
      expect(screen.getByTestId('agent-draft')).toHaveTextContent(/after_clip_id/i);
      expect(screen.getByTestId('agent-draft')).toHaveTextContent(/clip_1/i);
      const afterRequest = screen.getByTestId('agent-draft').textContent ?? '';
      expect(afterRequest).toContain('generator_id=video.h3.shotforward');
      expect(afterRequest.indexOf('after_clip_id')).toBeLessThan(afterRequest.indexOf(heading));
      expect(afterRequest.indexOf('generator_id=video.h3.shotforward')).toBeLessThan(
        afterRequest.indexOf(heading),
      );
      expect(afterRequest.slice(afterRequest.indexOf(heading))).toBe(supplement);

      fireEvent.click(screen.getByRole('button', { name: /在“开门见园”之前增加|before “开门见园”/i }));
      expect(screen.getByTestId('agent-draft')).toHaveTextContent(/before_clip_id/i);
      expect(screen.getByTestId('agent-draft')).toHaveTextContent(/clip_1/i);
      const beforeRequest = screen.getByTestId('agent-draft').textContent ?? '';
      expect(beforeRequest).toContain('generator_id=video.h3.shotforward');
      expect(beforeRequest.indexOf('before_clip_id')).toBeLessThan(beforeRequest.indexOf(heading));
      expect(beforeRequest.indexOf('generator_id=video.h3.shotforward')).toBeLessThan(
        beforeRequest.indexOf(heading),
      );
      expect(beforeRequest.slice(beforeRequest.indexOf(heading))).toBe(supplement);
      expect(document.querySelectorAll('.clip-insert-before')).toHaveLength(2);
    },
  );

  it.each(['附加要求：', 'Additional requirements:'])(
    'keeps %s and user text last when planning a storyboard',
    async (heading) => {
      const supplement = `${heading}\nOnly use the existing two characters.\n`;
      mocks.getAgentTemplateSettings.mockResolvedValue({
        locale: 'en',
        overrides: { 'storyboard.plan': `Plan this storyboard.\n\n${supplement}` },
        revision: 1,
      });
      mocks.listProviders.mockResolvedValue({ providers: [storyboardVideoProvider] });
      renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);
      await waitFor(() =>
        expect(screen.getByTestId('agent-draft')).toHaveAttribute(
          'data-video-prompt-target',
          expect.stringContaining('video.h3.shotforward'),
        ),
      );

      fireEvent.click(screen.getByRole('button', { name: /使用 Agent 规划|Plan storyboard with Agent/i }));

      const request = screen.getByTestId('agent-draft').textContent ?? '';
      expect(request).toContain('Plan this storyboard.');
      expect(request).toContain('generator_id=video.h3.shotforward');
      expect(request.indexOf('generator_id=video.h3.shotforward')).toBeLessThan(request.indexOf(heading));
      expect(request.slice(request.indexOf(heading))).toBe(supplement);
    },
  );

  it('keeps model and scene-end placement rules before the default additional requirements heading', async () => {
    mocks.listProviders.mockResolvedValue({ providers: [storyboardVideoProvider] });
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '园门初见',
            description: '',
            clips: [],
            revision: 1,
          },
        ],
        planned_duration_ms: 0,
        revision: 1,
      },
      duration_warning: '',
    });
    renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);
    await waitFor(() =>
      expect(screen.getByTestId('agent-draft')).toHaveAttribute(
        'data-video-prompt-target',
        expect.stringContaining('video.h3.shotforward'),
      ),
    );

    fireEvent.click(screen.getByRole('button', { name: /让 Agent 添加|Add with Agent/i }));

    const request = screen.getByTestId('agent-draft').textContent ?? '';
    const heading = request.includes('附加要求：') ? '附加要求：' : 'Additional requirements:';
    expect(request).toContain('scene_id: scene_1');
    expect(request).toContain('generator_id=video.h3.shotforward');
    expect(request).toMatch(/当前场景末尾|at the end of the current scene/);
    expect(request.indexOf('before_clip_id')).toBeLessThan(request.indexOf(heading));
    expect(request.indexOf('after_clip_id')).toBeLessThan(request.indexOf(heading));
    expect(request.indexOf('generator_id=video.h3.shotforward')).toBeLessThan(request.indexOf(heading));
    expect(request.endsWith(heading)).toBe(true);
  });

  it('uses the current video first frame for a storyboard card before its keyframe image', async () => {
    const clip = {
      clip_id: 'clip_video',
      project_id: 'project_1',
      episode_id: 'episode_1',
      scene_id: 'scene_1',
      position: 1,
      title: '已有成片的镜头',
      description: '',
      dialogue: '',
      creative_intent: '',
      planned_duration_ms: 8_000,
      first_image_prompt: '',
      last_image_prompt: '',
      video_prompt: '',
      video_prompts: { first_image: '', first_last_image: '', reference: '' },
      generation_mode: 'reference',
      revision: 1,
      first_image_slot_id: 'slot_first',
      last_image_slot_id: 'slot_last',
      video_slot_id: 'slot_video',
      reference_audio_slot_id: '',
      asset_references: [],
      derived_from_clip_ids: [],
      created_at: '2026-09-13T00:00:00Z',
      updated_at: '2026-09-13T00:00:00Z',
    };
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '成片场景',
            description: '',
            clips: [clip],
            revision: 1,
          },
        ],
        planned_duration_ms: 8_000,
        revision: 1,
      },
      duration_warning: '',
    });
    mocks.getMediaSlot.mockImplementation((_projectID: string, slotID: string) =>
      Promise.resolve(
        slotID === 'slot_video'
          ? {
              slot: { media_slot_id: slotID, current_media_version_id: 'version_video' },
              versions: [{ media_version_id: 'version_video', artifact_id: 'artifact_video' }],
              artifacts: [
                {
                  artifact_id: 'artifact_video',
                  kind: 'video',
                  duration_ms: 8_000,
                  width: 1280,
                  height: 720,
                },
              ],
            }
          : {
              slot: { media_slot_id: slotID, current_media_version_id: 'version_image' },
              versions: [{ media_version_id: 'version_image', artifact_id: 'artifact_image' }],
              artifacts: [{ artifact_id: 'artifact_image', kind: 'image', width: 1280, height: 720 }],
            },
      ),
    );

    renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);

    const card = await waitFor(() => {
      const element = document.querySelector<HTMLElement>('.clip-grid__card');
      expect(element).toBeInTheDocument();
      return element!;
    });
    const thumbnail = await waitFor(() => {
      const video = card.querySelector<HTMLVideoElement>('.clip-frame video');
      expect(video).toBeInTheDocument();
      return video!;
    });
    expect(thumbnail).toHaveAttribute('src', '/api/v1/projects/project_1/artifacts/artifact_video/content');
    expect(card.querySelector('.clip-frame img')).not.toBeInTheDocument();
    expect(card.querySelector('.clip-frame__video-mark')).toBeInTheDocument();
    fireEvent.loadedMetadata(thumbnail);
    expect(thumbnail.currentTime).toBe(0.25);
  });

  it('keeps Clip merging as a compact adjacent selection that hands exact IDs to Agent', async () => {
    const clip = (id: string, position: number, title: string, duration: number) => ({
      clip_id: id,
      project_id: 'project_1',
      episode_id: 'episode_1',
      scene_id: 'scene_1',
      position,
      title,
      description: '',
      dialogue: '',
      creative_intent: '',
      planned_duration_ms: duration * 1_000,
      first_image_prompt: '',
      last_image_prompt: '',
      video_prompt: '',
      video_prompts: { first_image: '', first_last_image: '', reference: '' },
      generation_mode: 'reference',
      revision: 1,
      first_image_slot_id: '',
      last_image_slot_id: '',
      video_slot_id: '',
      reference_audio_slot_id: '',
      asset_references: [],
      derived_from_clip_ids: [],
      created_at: '2026-09-13T00:00:00Z',
      updated_at: '2026-09-13T00:00:00Z',
    });
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '园门初见',
            description: '',
            clips: [
              clip('clip_1', 1, '开门见园', 6),
              clip('clip_2', 2, '空庭答谢', 7),
              clip('clip_3', 3, '凤姐提醒', 8),
            ],
            revision: 1,
          },
        ],
        planned_duration_ms: 21_000,
        revision: 1,
      },
      duration_warning: '',
    });
    renderRoute(
      '/projects/project_1/episodes/episode_1/storyboard/clips/clip_1',
      <StoryboardWorkspacePage />,
    );

    fireEvent.click(await screen.findByRole('button', { name: /选择合并|Select to merge/i }));
    const first = screen.getByRole('button', { name: /开门见园/i });
    const second = screen.getByRole('button', { name: /空庭答谢/i });
    const third = screen.getByRole('button', { name: /凤姐提醒/i });
    expect(first).toHaveAttribute('aria-pressed', 'true');
    expect(document.querySelector('.clip-focus-card')).not.toBeInTheDocument();
    expect(document.querySelectorAll('.clip-insert-before')).toHaveLength(0);

    fireEvent.click(third);
    expect(screen.getByText(/请选择连续项|Select adjacent units/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Agent 合并|Merge with Agent/i })).toBeDisabled();

    fireEvent.click(second);
    expect(screen.getByText(/已选 3 个 · 21 秒|3 selected · 21 sec/i)).toBeInTheDocument();
    const merge = screen.getByRole('button', { name: /Agent 合并|Merge with Agent/i });
    expect(merge).toBeEnabled();
    fireEvent.click(merge);

    const draft = screen.getByTestId('agent-draft');
    await waitFor(() => expect(draft).toHaveTextContent(/clip_1/));
    expect(draft).toHaveTextContent(/clip_2/);
    expect(draft).toHaveTextContent(/clip_3/);
    expect(draft).toHaveTextContent(/21/);
    expect(screen.queryByRole('button', { name: /Agent 合并|Merge with Agent/i })).not.toBeInTheDocument();
  });

  it('allows splitting a four-second Clip with independent replacement durations and keeps delete safe', async () => {
    mocks.listProviders.mockResolvedValue({
      providers: [
        {
          generator_id: 'video.h3.shotforward',
          model_id: 'h3-ref2va',
          enabled: true,
          health: 'ready',
          models: [
            { model_id: 'h3-ref2va', health: 'ready' },
            { model_id: 'h3-fl2va', health: 'ready' },
          ],
        },
        {
          generator_id: 'video.seedance-2.5.volcengine-cn',
          model_id: 'seedance-2-5-pro',
          enabled: true,
          health: 'ready',
        },
      ],
    });
    const clip = {
      clip_id: 'clip_1',
      project_id: 'project_1',
      episode_id: 'episode_1',
      scene_id: 'scene_1',
      position: 1,
      title: '空餐盘与神王的决定',
      description: '',
      dialogue: '',
      creative_intent: '',
      planned_duration_ms: 4_000,
      first_image_prompt: '',
      last_image_prompt: '',
      video_prompt: '',
      video_prompts: { first_image: '', first_last_image: '', reference: '' },
      generation_mode: 'reference',
      revision: 3,
      first_image_slot_id: '',
      last_image_slot_id: '',
      video_slot_id: '',
      reference_audio_slot_id: '',
      asset_references: [],
      derived_from_clip_ids: [],
      created_at: '2026-09-13T00:00:00Z',
      updated_at: '2026-09-13T00:00:00Z',
    };
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '奥林匹斯山巅',
            description: '',
            clips: [clip],
            revision: 2,
          },
        ],
        planned_duration_ms: 4_000,
        revision: 5,
      },
      duration_warning: '',
    });
    renderRoute(
      '/projects/project_1/episodes/episode_1/storyboard/clips/clip_1',
      <StoryboardWorkspacePage />,
    );

    const target = () =>
      JSON.parse(screen.getByTestId('agent-draft').getAttribute('data-video-prompt-target') || '{}');
    await waitFor(() =>
      expect(target().focus).toMatchObject({
        clip_id: 'clip_1',
        generation_mode: 'reference',
        generator_id: 'video.h3.shotforward',
        provider_model_id: 'h3-ref2va',
      }),
    );
    fireEvent.click(screen.getByRole('combobox', { name: /视频生成模型|Video generation model/i }));
    fireEvent.click(await screen.findByRole('option', { name: /Seedance 2.5/i }));
    await waitFor(() =>
      expect(target().focus).toMatchObject({
        clip_id: 'clip_1',
        generation_mode: 'reference',
        generator_id: 'video.seedance-2.5.volcengine-cn',
        provider_model_id: 'seedance-2-5-pro',
      }),
    );
    expect(target().generator_id).toBe('video.seedance-2.5.volcengine-cn');

    const split = await screen.findByRole('button', { name: /拆分为多个单元|Split into units/i });
    expect(split).toBeEnabled();
    fireEvent.click(split);
    await waitFor(() => expect(screen.getByTestId('agent-draft')).toHaveTextContent(/clip_1/));
    expect(screen.getByTestId('agent-draft')).toHaveTextContent(/4/);
    expect(screen.getByTestId('agent-draft')).toHaveTextContent(
      /总时长可以变化|combined duration may change/,
    );
    expect(screen.getByTestId('agent-draft')).toHaveTextContent(/append_clip_split_operation/);

    const actions = screen.getByRole('button', {
      name: /视频单元操作|Video unit actions/i,
    });
    fireEvent.click(actions);
    fireEvent.click(screen.getByRole('menuitem', { name: /删除视频单元|Delete video unit/i }));
    const dialog = screen.getByRole('dialog', { name: /删除视频单元|Delete video unit/i });
    expect(within(dialog).getByText(/空餐盘与神王的决定/)).toBeInTheDocument();
    expect(within(dialog).getAllByText(/回收站|trash/i)).not.toHaveLength(0);
    fireEvent.click(within(dialog).getByRole('button', { name: /移到回收站|Move to trash/i }));

    await waitFor(() =>
      expect(mocks.deleteClip).toHaveBeenCalledWith('project_1', 'episode_1', 'clip_1', 3, 5),
    );
  });

  it('lets an empty-script video start its storyboard manually or with Agent', async () => {
    mocks.getEpisode.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episode: {
        episode_id: 'episode_1',
        title: '无剧本视频',
        script: { markdown: '', revision: 1 },
      },
      episodes: [],
    });
    renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);

    const empty = (await screen.findByText(/为当前视频建立故事板|Build this video’s storyboard/i)).closest(
      '.storyboard-empty',
    ) as HTMLElement;
    expect(empty).toBeInTheDocument();
    expect(within(empty).getByText(/手动创建空场景|manually create a blank scene/i)).toBeInTheDocument();
    expect(
      within(empty).getByRole('button', { name: /使用 Agent 规划|Plan storyboard with Agent/i }),
    ).toHaveClass('storyboard-agent-initialize');
    fireEvent.click(within(empty).getByRole('button', { name: /手动开始|Start manually/i }));
    const dialog = screen.getByRole('dialog', { name: /新建场景|New scene/i });
    fireEvent.change(within(dialog).getByRole('textbox', { name: /场景名称|Scene name/i }), {
      target: { value: '纯视觉开场' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: /创建场景|Create scene/i }));
    await waitFor(() =>
      expect(mocks.createScene).toHaveBeenCalledWith('project_1', 'episode_1', '纯视觉开场', '', 1),
    );
    expect(empty.querySelector('.storyboard-empty__icon svg')).toBeInTheDocument();
  });

  it('creates a video unit manually in a scene without requiring a script', async () => {
    mocks.getEpisode.mockResolvedValue({
      project: { project_id: 'project_1', name: '视觉实验' },
      episode: {
        episode_id: 'episode_1',
        title: '无剧本视频',
        script: { markdown: '', revision: 1 },
      },
      episodes: [],
    });
    mocks.getStoryboard.mockResolvedValue({
      storyboard: {
        scenes: [
          {
            scene_id: 'scene_1',
            position: 1,
            title: '纯视觉开场',
            description: '',
            clips: [],
            revision: 1,
          },
        ],
        planned_duration_ms: 0,
        revision: 4,
      },
      duration_warning: false,
    });
    renderRoute('/projects/project_1/episodes/episode_1/storyboard', <StoryboardWorkspacePage />);

    fireEvent.click(await screen.findByRole('button', { name: /^增加视频单元$|^Add video unit$/i }));
    const dialog = screen.getByRole('dialog', { name: /创建视频单元|Create video unit/i });
    fireEvent.change(within(dialog).getByRole('textbox', { name: /视频单元名称|Video unit name/i }), {
      target: { value: '光影变化' },
    });
    fireEvent.change(within(dialog).getByRole('spinbutton', { name: /计划时长|Planned duration/i }), {
      target: { value: '7' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: /创建视频单元|Create video unit/i }));

    await waitFor(() =>
      expect(mocks.createClip).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        'scene_1',
        expect.objectContaining({
          title: '光影变化',
          planned_duration_seconds: 7,
          generation_mode: 'reference',
        }),
        4,
      ),
    );
  });

  it('keeps preview duration and the single download action in the preview status bar', async () => {
    renderRoute('/projects/project_1/episodes/episode_1/preview', <EpisodePreviewPage />);

    const exportButton = await screen.findByRole('button', {
      name: /下载 MP4|Download MP4/i,
    });
    expect(exportButton.closest('.preview-statusbar__actions')).toBeInTheDocument();
    expect(document.querySelector('.preview-statusbar__duration')).toHaveTextContent(/0:00/);
    expect(document.querySelector('.preview-heading-actions')).not.toBeInTheDocument();
    expect(document.querySelector('.video-canvas')).toHaveStyle({ aspectRatio: '1920 / 1080' });
    expect(document.querySelector('.transport')).not.toBeInTheDocument();
    const emptyPreview = document.querySelector<HTMLElement>('.preview-empty')!;
    expect(emptyPreview.querySelector('p')).not.toBeInTheDocument();
    expect(within(emptyPreview).queryByRole('link')).not.toBeInTheDocument();
  });

  it('shows a recoverable error when the desktop save dialog cannot start', async () => {
    mocks.getMediaSlot.mockResolvedValue({
      slot: { media_slot_id: 'slot_1', current_media_version_id: 'version_1' },
      versions: [{ media_version_id: 'version_1', artifact_id: 'artifact_1', revision: 1 }],
      artifacts: [{ artifact_id: 'artifact_1', duration_ms: 5_000 }],
    });
    mocks.getExportPreview.mockResolvedValue({
      preview: {
        storyboard_revision: 2,
        export_duration_ms: 5_000,
        included_clip_count: 1,
        skipped_clip_count: 0,
        output_width: 1920,
        output_height: 1080,
        clips: [
          {
            scene_id: 'scene_1',
            clip_id: 'clip_1',
            position: 1,
            media_slot_id: 'slot_1',
            media_slot_revision: 1,
            media_version_id: 'version_1',
            media_version_revision: 1,
            artifact_id: 'artifact_1',
            trim_in_ms: 0,
            trim_out_ms: 5_000,
            duration_ms: 5_000,
            media_url: '/media/one.mp4',
          },
        ],
        skipped_clips: [],
      },
    });
    mocks.submitExport.mockResolvedValue({
      job: {
        export_job_id: 'export_1',
        project_id: 'project_1',
        episode_id: 'episode_1',
        state: 'succeeded',
        output_relative_path: 'exports/export_1.mp4',
      },
    });
    window.__SHOTFORWARD_DESKTOP__ = {
      selectProjectParent: vi.fn(),
      openExternal: vi.fn(),
      saveDownload: vi.fn().mockRejectedValue(new Error('dialog unavailable')),
    };

    renderRoute('/projects/project_1/episodes/episode_1/preview', <EpisodePreviewPage />);
    fireEvent.click(await screen.findByRole('button', { name: /^下载 MP4$|^Download MP4$/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/MP4 保存失败|MP4 could not be saved/i);
  });

  it('preloads the next clip, saves precise trim points, and exposes a completed export', async () => {
    const clips = [
      {
        scene_id: 'scene_1',
        clip_id: 'clip_1',
        position: 1,
        media_slot_id: 'slot_1',
        media_slot_revision: 2,
        media_version_id: 'version_1',
        media_version_revision: 3,
        artifact_id: 'artifact_1',
        trim_in_ms: 1_000,
        trim_out_ms: 8_000,
        duration_ms: 7_000,
        media_url: '/media/one.mp4',
      },
      {
        scene_id: 'scene_1',
        clip_id: 'clip_2',
        position: 2,
        media_slot_id: 'slot_2',
        media_slot_revision: 1,
        media_version_id: 'version_2',
        media_version_revision: 1,
        artifact_id: 'artifact_2',
        trim_in_ms: 0,
        trim_out_ms: 6_000,
        duration_ms: 6_000,
        media_url: '/media/two.mp4',
      },
    ];
    mocks.getExportPreview.mockResolvedValue({
      preview: {
        storyboard_revision: 4,
        export_duration_ms: 13_000,
        included_clip_count: 2,
        skipped_clip_count: 0,
        output_width: 1080,
        output_height: 1920,
        clips,
        skipped_clips: [],
      },
    });
    const details = {
      slot: { media_slot_id: 'slot_1', current_media_version_id: 'version_1' },
      versions: [{ media_version_id: 'version_1', artifact_id: 'artifact_1', revision: 3 }],
      artifacts: [{ artifact_id: 'artifact_1', duration_ms: 10_000 }],
    };
    mocks.getMediaSlot.mockResolvedValue(details);
    mocks.updateVideoTrim.mockResolvedValue({
      ...details,
      versions: [
        {
          media_version_id: 'version_1',
          artifact_id: 'artifact_1',
          revision: 4,
          trim_in_ms: 1_500,
          trim_out_ms: 8_000,
        },
      ],
    });
    mocks.getTaskSnapshot.mockResolvedValue({
      stream_epoch: 'epoch_1',
      cursor: '2',
      tasks: [
        {
          task_type: 'export_job',
          task_id: 'export_1',
          project_id: 'project_1',
          episode_id: 'episode_1',
          state: 'succeeded',
          progress_stage: 'completed',
          progress_message: '',
          failure_code: '',
          revision: 2,
          created_at: '2026-09-14T00:00:00Z',
          updated_at: '2026-09-14T00:01:00Z',
          output_relative_path: 'exports/export_1.mp4',
        },
      ],
    });

    renderRoute('/projects/project_1/episodes/episode_1/preview', <EpisodePreviewPage />);

    await screen.findByText(/原视频时长|Source duration/i);
    expect(screen.queryByText(/^当前视频单元$|^Current video unit$/i)).not.toBeInTheDocument();
    expect(document.querySelector('.clip-preview-panel__summary header')).not.toBeInTheDocument();
    expect(document.querySelector('.clip-preview-panel__media video')).toHaveAttribute(
      'src',
      '/media/one.mp4',
    );
    const stageVideos = document.querySelectorAll('.video-canvas > video');
    expect(stageVideos).toHaveLength(2);
    expect(stageVideos[0]).toHaveClass('is-active');
    expect(stageVideos[1]).toHaveClass('is-buffering');
    expect(stageVideos[0]).not.toHaveAttribute('controls');
    expect(screen.queryByRole('button', { name: /Enlarge video|放大播放视频/i })).not.toBeInTheDocument();
    const activeVideo = stageVideos[0] as HTMLVideoElement;
    const play = vi.spyOn(activeVideo, 'play').mockResolvedValue(undefined);
    fireEvent.click(screen.getByRole('button', { name: /Play effective segment|播放有效片段/i }));
    expect(play).toHaveBeenCalledOnce();
    fireEvent.play(activeVideo);
    expect(screen.getByRole('button', { name: /Pause effective segment|暂停有效片段/i })).toBeInTheDocument();
    fireEvent.pause(activeVideo);
    play.mockRestore();
    expect(document.querySelector('.clip-player-controls__time')).toHaveTextContent('0:00 / 0:07');
    const muteButton = screen.getByRole('button', { name: /^Mute$|^静音$/i });
    expect(muteButton.querySelector('svg')).toBeInTheDocument();
    expect(muteButton).not.toHaveTextContent('🔊');
    fireEvent.click(muteButton);
    expect(screen.getByRole('button', { name: /Unmute|取消静音/i }).querySelector('svg')).toBeInTheDocument();
    document
      .querySelectorAll<HTMLElement>('.timeline-thumb')
      .forEach((banner) => expect(banner).toHaveStyle({ aspectRatio: '1080 / 1920' }));

    fireEvent.change(screen.getByLabelText(/入点|In point/i), { target: { value: '1500' } });
    await waitFor(
      () =>
        expect(mocks.updateVideoTrim).toHaveBeenCalledWith('project_1', 'slot_1', 'version_1', 1500, 8000, 3),
      { timeout: 2_000 },
    );
    expect(screen.queryByRole('button', { name: /应用裁剪|Apply trim/i })).not.toBeInTheDocument();

    const exportButton = await screen.findByRole('button', {
      name: /^下载 MP4$|^Download MP4$/i,
    });
    await waitFor(() => expect(exportButton).toBeEnabled());
    const automaticDownload = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => undefined);
    fireEvent.click(exportButton);
    await waitFor(() => expect(automaticDownload).toHaveBeenCalledOnce());
    expect(mocks.submitExport).toHaveBeenCalledOnce();
    expect(screen.queryByRole('link', { name: /下载 MP4|Download MP4/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /前往任务中心|Open task center/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Enlarge video|放大播放视频/i })).not.toBeInTheDocument();
    expect(document.querySelector('.video-lightbox')).not.toBeInTheDocument();
    automaticDownload.mockRestore();
  });
});
