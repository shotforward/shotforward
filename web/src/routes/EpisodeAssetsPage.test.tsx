import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { EpisodeAssetsPage } from './EpisodeAssetsPage';
import { AssetDetailPanel } from '../components/AssetDetailPanel';

const mocks = vi.hoisted(() => ({
  getEpisode: vi.fn(),
  createAsset: vi.fn(),
  listEpisodeAssets: vi.fn(),
  getProjectReferenceImages: vi.fn(),
  getMediaSlot: vi.fn(),
  renameAssetIdentity: vi.fn(),
  updateAssetPrompt: vi.fn(),
  listEpisodeReferenceMedia: vi.fn(),
  uploadEpisodeReferenceMedia: vi.fn(),
  updateEpisodeReferenceMedia: vi.fn(),
  deleteEpisodeReferenceMedia: vi.fn(),
  checkGenerationReadiness: vi.fn(),
  submitGeneration: vi.fn(),
  getTaskSnapshot: vi.fn(),
  startAgentWorkflow: vi.fn(),
  listProviders: vi.fn(),
}));

vi.mock('../api/episodes', () => ({ getEpisode: mocks.getEpisode }));
vi.mock('../api/assets', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/assets')>();
  return {
    ...original,
    createAsset: mocks.createAsset,
    listEpisodeAssets: mocks.listEpisodeAssets,
    getProjectReferenceImages: mocks.getProjectReferenceImages,
    getMediaSlot: mocks.getMediaSlot,
    renameAssetIdentity: mocks.renameAssetIdentity,
    updateAssetPrompt: mocks.updateAssetPrompt,
    listEpisodeReferenceMedia: mocks.listEpisodeReferenceMedia,
    uploadEpisodeReferenceMedia: mocks.uploadEpisodeReferenceMedia,
    updateEpisodeReferenceMedia: mocks.updateEpisodeReferenceMedia,
    deleteEpisodeReferenceMedia: mocks.deleteEpisodeReferenceMedia,
  };
});
vi.mock('../api/generation', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/generation')>();
  return {
    ...original,
    checkGenerationReadiness: mocks.checkGenerationReadiness,
    submitGeneration: mocks.submitGeneration,
  };
});
vi.mock('../api/tasks', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/tasks')>();
  return { ...original, getTaskSnapshot: mocks.getTaskSnapshot };
});
vi.mock('../api/agent', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/agent')>();
  return { ...original, startAgentWorkflow: mocks.startAgentWorkflow };
});
vi.mock('../api/providers', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/providers')>();
  return { ...original, listProviders: mocks.listProviders };
});
vi.mock('../components/AgentDock', () => ({
  AgentDock: ({
    draftRequest,
  }: {
    draftRequest?: {
      text?: string;
      reference?: { kind: 'asset' | 'uploaded_media'; value: { identity?: { name: string }; name?: string } };
    };
  }) => (
    <aside>
      Agent
      {draftRequest?.text !== undefined && (
        <textarea aria-label="Agent input" readOnly value={draftRequest.text} />
      )}
      {draftRequest?.reference && (
        <output data-testid="agent-reference">
          {draftRequest.reference.kind}:
          {draftRequest.reference.value.identity?.name ?? draftRequest.reference.value.name}
        </output>
      )}
    </aside>
  ),
}));

const asset = {
  asset_id: 'asset_1',
  project_id: 'project_1',
  episode_id: 'episode_1',
  identity: {
    asset_identity_id: 'identity_1',
    project_id: 'project_1',
    kind: 'character',
    name: '贾探春',
    revision: 1,
    created_at: '2026-09-06T00:00:00Z',
    updated_at: '2026-09-06T00:00:00Z',
  },
  prompt: '古典写实人物',
  revision: 1,
  primary_image_slot_id: '',
  created_at: '2026-09-06T00:00:00Z',
  updated_at: '2026-09-06T00:00:00Z',
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/projects/project_1/episodes/episode_1/assets']}>
        <Routes>
          <Route path="/projects/:projectId/episodes/:episodeId/assets" element={<EpisodeAssetsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, client };
}

describe('EpisodeAssetsPage asset details', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.setAttribute('open', '');
    };
    HTMLDialogElement.prototype.close = function close() {
      this.removeAttribute('open');
      this.dispatchEvent(new Event('close'));
    };
  });

  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.removeItem('shotforward.image-generator');
    mocks.getEpisode.mockResolvedValue({
      project: {
        project_id: 'project_1',
        name: '红楼梦',
        output_width: 1920,
        output_height: 1080,
      },
      episode: { episode_id: 'episode_1', title: '第 1 集' },
      episodes: [],
    });
    mocks.listEpisodeAssets.mockResolvedValue({ assets: [asset] });
    mocks.createAsset.mockResolvedValue({ asset });
    mocks.getProjectReferenceImages.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episodes: [
        {
          episode_id: 'episode_1',
          project_id: 'project_1',
          sequence_number: 1,
          title: '第 1 集',
          target_duration_ms: 60_000,
          revision: 1,
          script_revision: 1,
          created_at: '2026-09-06T00:00:00Z',
          updated_at: '2026-09-06T00:00:00Z',
          script_updated_at: '2026-09-06T00:00:00Z',
        },
      ],
      images: [],
    });
    mocks.getMediaSlot.mockResolvedValue({
      slot: {
        media_slot_id: 'slot_1',
        episode_id: 'episode_1',
        owner_kind: 'asset',
        owner_id: 'asset_1',
        kind: 'asset_primary_image',
        current_media_version_id: 'version_1',
        revision: 3,
        updated_at: '2026-09-06T00:00:00Z',
      },
      versions: [
        {
          media_version_id: 'version_1',
          artifact_id: 'artifact_1',
          sequence_number: 1,
          trim_in_ms: 0,
          trim_out_ms: 0,
          revision: 1,
          created_at: '2026-09-06T00:00:00Z',
          updated_at: '2026-09-06T00:00:00Z',
        },
      ],
      artifacts: [
        {
          artifact_id: 'artifact_1',
          kind: 'image',
          mime_type: 'image/png',
          original_filename: 'reference.png',
          byte_size: 100,
          width: 720,
          height: 1280,
          duration_ms: 0,
          created_at: '2026-09-06T00:00:00Z',
        },
      ],
    });
    mocks.checkGenerationReadiness.mockResolvedValue({ ready: true, slot_revision: 3 });
    mocks.renameAssetIdentity.mockResolvedValue({});
    mocks.updateAssetPrompt.mockResolvedValue({});
    mocks.listEpisodeReferenceMedia.mockResolvedValue({ reference_media: [] });
    mocks.uploadEpisodeReferenceMedia.mockResolvedValue({});
    mocks.updateEpisodeReferenceMedia.mockResolvedValue({});
    mocks.deleteEpisodeReferenceMedia.mockResolvedValue(undefined);
    mocks.submitGeneration.mockResolvedValue({ batch: {}, jobs: [{ generation_job_id: 'job_1' }] });
    mocks.getTaskSnapshot.mockResolvedValue({ stream_epoch: 'epoch', cursor: 'epoch:1', tasks: [] });
    mocks.startAgentWorkflow.mockResolvedValue({
      session: { id: 'session_1' },
      turn: { id: 'turn_1' },
    });
    mocks.listProviders.mockResolvedValue({
      providers: [
        {
          generator_id: 'image.codex',
          endpoint_preset: 'codex_subscription',
          model_id: '',
          resource_id: '',
          workspace_id: '',
          enabled: true,
          concurrency_limit: 5,
          credential_configured: false,
          capability_version: 'codex-image-v1',
          health: 'ready',
          failure_code: '',
          revision: 1,
          created_at: '2026-09-06T00:00:00Z',
          updated_at: '2026-09-06T00:00:00Z',
        },
      ],
    });
  });

  it('opens the primary asset image in a large preview', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    expect(details).toHaveAttribute('aria-modal', 'true');
    expect(await within(details).findByRole('button', { name: /导入图片|Import image/ })).toHaveClass(
      'asset-import-action',
    );
    expect(within(details).getByRole('button', { name: /生成图片|Generate image/ })).toHaveClass(
      'button--primary',
      'asset-generate-action',
    );
    fireEvent.click(
      within(details).getByRole('button', {
        name: /放大预览贾探春|Preview 贾探春/,
      }),
    );

    expect(screen.getByRole('dialog', { name: /放大预览贾探春|Preview 贾探春/ })).toHaveAttribute('open');
  });

  it('uses a compact import action and a quiet empty media-version state', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    mocks.getMediaSlot.mockResolvedValue({
      slot: {
        media_slot_id: 'slot_1',
        episode_id: 'episode_1',
        owner_kind: 'asset',
        owner_id: 'asset_1',
        kind: 'asset_primary_image',
        current_media_version_id: '',
        revision: 1,
        updated_at: '2026-09-06T00:00:00Z',
      },
      versions: [],
      artifacts: [],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    expect(within(details).getByText(/暂无媒体版本|No media versions yet/)).toBeInTheDocument();
    expect(within(details).getByRole('button', { name: /导入图片|Import image/ })).toHaveClass(
      'asset-import-action',
    );
  });

  it('removes dimensions from the card and shows them in the asset detail', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    renderPage();

    const assetCard = await screen.findByRole('button', { name: /贾探春/ });
    await waitFor(() => expect(mocks.getMediaSlot).toHaveBeenCalled());
    expect(within(assetCard).queryByText(/720 × 1280/)).not.toBeInTheDocument();
    expect(assetCard.querySelector('.asset-thumb')).not.toHaveAttribute('style');
    expect(assetCard.querySelector('.asset-thumb img')).toHaveStyle({ objectFit: 'contain' });

    fireEvent.click(assetCard);
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    const metadata = details.querySelector('.asset-image-metadata');
    expect(metadata).not.toBeNull();
    expect(within(metadata as HTMLElement).getByText('9:16')).toBeInTheDocument();
    expect(within(metadata as HTMLElement).getByText('720 × 1280 px')).toBeInTheDocument();
  });

  it('fits the asset detail preview to the image dimensions', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    const preview = details.querySelector('.asset-preview');

    expect(preview).toHaveClass('asset-preview--fitted');
    expect(preview).toHaveStyle({
      aspectRatio: '720 / 1280',
      width: 'min(100%, 29.25dvh, 292.5px)',
    });
    expect(details.querySelector('.version-strip img')).toHaveStyle({
      objectFit: 'contain',
      objectPosition: 'center',
    });
  });

  it('shows only connected image generation services with localized labels', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    mocks.listProviders.mockResolvedValue({
      providers: [
        {
          generator_id: 'image.codex',
          enabled: true,
          health: 'ready',
          models: [],
        },
        {
          generator_id: 'image.shotforward',
          enabled: true,
          health: 'unavailable',
          models: [{ model_id: 'sf-image', health: 'unavailable' }],
        },
      ],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    const service = within(details).getByRole('combobox', {
      name: /图像生成服务|Image generation service/i,
    });
    fireEvent.click(service);

    expect(
      within(details).getByRole('option', { name: /本机 Codex 订阅|Local Codex subscription/i }),
    ).toBeInTheDocument();
    expect(
      within(details).queryByRole('option', { name: /ShotForward 云服务|ShotForward Cloud/i }),
    ).not.toBeInTheDocument();
  });

  it('manages uploaded images, audio, and video in a separate media library', async () => {
    const base = {
      episode_id: 'episode_1',
      description: '',
      usage_count: 0,
      revision: 1,
      created_at: '2026-09-10T00:00:00Z',
      updated_at: '2026-09-10T00:00:00Z',
    };
    const artifactBase = {
      byte_size: 1024,
      width: 0,
      height: 0,
      duration_ms: 0,
      created_at: '2026-09-10T00:00:00Z',
    };
    const uploaded = [
      {
        ...base,
        name: '人物参考',
        description: '夜景服装参考',
        usage_count: 2,
        artifact: {
          ...artifactBase,
          artifact_id: 'artifact_upload_image',
          kind: 'image',
          mime_type: 'image/png',
          original_filename: '人物参考.png',
          width: 720,
          height: 1280,
        },
      },
      {
        ...base,
        name: '旁白',
        artifact: {
          ...artifactBase,
          artifact_id: 'artifact_upload_audio',
          kind: 'audio',
          mime_type: 'audio/wav',
          original_filename: '旁白.wav',
          duration_ms: 12_000,
        },
      },
      {
        ...base,
        name: '动作参考',
        usage_count: 1,
        artifact: {
          ...artifactBase,
          artifact_id: 'artifact_upload_video',
          kind: 'video',
          mime_type: 'video/mp4',
          original_filename: '动作参考.mp4',
          width: 1280,
          height: 720,
          duration_ms: 8_000,
        },
      },
    ];
    mocks.listEpisodeReferenceMedia.mockResolvedValue({ reference_media: uploaded });
    mocks.updateEpisodeReferenceMedia.mockResolvedValue({
      reference_media: {
        ...uploaded[0],
        name: '宝玉人物参考',
        description: '外景服装参考',
        revision: 2,
      },
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /Uploaded media|上传素材/ }));
    const previewImage = await screen.findByRole('button', {
      name: /Preview 人物参考|放大预览人物参考/,
    });
    expect(screen.getByRole('button', { name: /Play 旁白|播放旁白/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Play 动作参考|播放动作参考/ })).toBeInTheDocument();
    const referenceButtons = screen.getAllByRole('button', {
      name: /^引用$|^Reference$/,
    });
    expect(referenceButtons).toHaveLength(3);
    fireEvent.click(referenceButtons[1]!);
    expect(screen.getByTestId('agent-reference')).toHaveTextContent('uploaded_media:旁白');
    fireEvent.click(referenceButtons[0]!);
    expect(screen.getByTestId('agent-reference')).toHaveTextContent('uploaded_media:人物参考');

    fireEvent.click(previewImage);
    expect(screen.getByRole('dialog', { name: /Preview 人物参考|放大预览人物参考/ })).toHaveAttribute('open');

    fireEvent.click(screen.getByRole('button', { name: /^人物参考/ }));
    const details = screen.getByRole('dialog', { name: '人物参考' });
    fireEvent.change(within(details).getByLabelText(/Media name|素材名称/), {
      target: { value: '宝玉人物参考' },
    });
    fireEvent.change(within(details).getByLabelText(/Description|素材说明/), {
      target: { value: '外景服装参考' },
    });
    fireEvent.click(within(details).getByRole('button', { name: /Save changes|保存修改/ }));

    await waitFor(() =>
      expect(mocks.updateEpisodeReferenceMedia).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        'artifact_upload_image',
        '宝玉人物参考',
        '外景服装参考',
        1,
      ),
    );
  });

  it.each(['audio', 'video'] as const)(
    'saves and reopens the Episode %s selection, validates dirty edits, and resets non-destructively',
    async (kind) => {
      let value = {
        episode_id: 'episode_1',
        name: '声音动作参考',
        description: '',
        usage_count: 3,
        revision: 1,
        created_at: '2026-10-11T00:00:00Z',
        updated_at: '2026-10-11T00:00:00Z',
        trim_start_ms: 3000,
        trim_end_ms: 11000,
        effective_duration_ms: 8000,
        artifact: {
          artifact_id: 'shared_artifact',
          kind,
          mime_type: kind === 'audio' ? 'audio/wav' : 'video/mp4',
          original_filename: 'original',
          byte_size: 1024,
          width: 0,
          height: 0,
          duration_ms: 15000,
          created_at: '2026-10-11T00:00:00Z',
        },
      };
      const originalArtifact = { ...value.artifact };
      mocks.listEpisodeReferenceMedia.mockImplementation(async () => ({ reference_media: [value] }));
      mocks.updateEpisodeReferenceMedia.mockImplementation(
        async (_project, _episode, _artifact, name, description, revision, trim) => {
          expect(revision).toBe(value.revision);
          value = { ...value, name, description, ...trim, revision: revision + 1 };
          return { reference_media: value };
        },
      );
      renderPage();
      fireEvent.click(await screen.findByRole('button', { name: /Uploaded media|上传素材/ }));
      fireEvent.click(await screen.findByRole('button', { name: /^声音动作参考/ }));
      let details = screen.getByRole('dialog', { name: '声音动作参考' });
      const start = within(details).getByRole('spinbutton', { name: /Start \(seconds\)|开始时间/ });
      const end = within(details).getByRole('spinbutton', { name: /End \(seconds\)|结束时间/ });
      const save = within(details).getByRole('button', { name: /Save changes|保存修改/ });
      expect(start).toHaveValue(3);
      expect(end).toHaveValue(11);
      expect(save).toBeDisabled();
      fireEvent.change(start, { target: { value: '12' } });
      expect(save).toBeDisabled();
      expect(within(details).getByRole('alert')).toBeInTheDocument();
      fireEvent.change(start, { target: { value: '4.125' } });
      fireEvent.change(end, { target: { value: '9.375' } });
      expect(save).toBeEnabled();
      fireEvent.click(save);
      await waitFor(() =>
        expect(mocks.updateEpisodeReferenceMedia).toHaveBeenLastCalledWith(
          'project_1',
          'episode_1',
          'shared_artifact',
          '声音动作参考',
          '',
          1,
          { trim_start_ms: 4125, trim_end_ms: 9375 },
        ),
      );
      await waitFor(() => expect(save).toBeDisabled());
      fireEvent.click(within(details).getByRole('button', { name: /Close|关闭/ }));
      fireEvent.click(screen.getByRole('button', { name: /^声音动作参考/ }));
      details = screen.getByRole('dialog', { name: '声音动作参考' });
      expect(within(details).getByRole('spinbutton', { name: /Start \(seconds\)|开始时间/ })).toHaveValue(
        4.125,
      );
      expect(within(details).getByRole('spinbutton', { name: /End \(seconds\)|结束时间/ })).toHaveValue(
        9.375,
      );
      fireEvent.click(
        within(details).getByRole('button', { name: /Restore full original|恢复完整原始素材/ }),
      );
      fireEvent.click(within(details).getByRole('button', { name: /Save changes|保存修改/ }));
      await waitFor(() =>
        expect(mocks.updateEpisodeReferenceMedia).toHaveBeenLastCalledWith(
          'project_1',
          'episode_1',
          'shared_artifact',
          '声音动作参考',
          '',
          2,
          { trim_start_ms: 0, trim_end_ms: 0 },
        ),
      );
      expect(value.artifact).toEqual(originalArtifact);
    },
  );

  it('does not force the first asset open and closes clean details with Escape', async () => {
    renderPage();

    const assetCard = await screen.findByRole('button', { name: /贾探春/ });
    expect(within(assetCard).getByText(/还没有主图|No primary image yet/)).toBeInTheDocument();
    expect(within(assetCard).getByText(/角色|Character/)).toBeInTheDocument();
    expect(document.querySelector('.asset-detail')).not.toBeInTheDocument();

    fireEvent.click(assetCard);
    expect(await screen.findByRole('dialog', { name: '贾探春' })).toBeInTheDocument();

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '贾探春' })).not.toBeInTheDocument());
  });

  it('asks before closing details with unsaved edits', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });

    fireEvent.change(within(details).getByLabelText(/名称|Name/), { target: { value: '探春' } });
    fireEvent.click(within(details).getByRole('button', { name: /关闭|Close/ }));

    const confirmation = await screen.findByRole('dialog', { name: /放弃未保存|Discard unsaved/ });
    expect(details).toBeInTheDocument();
    fireEvent.click(within(confirmation).getByRole('button', { name: /放弃修改|Discard changes/ }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: '贾探春' })).not.toBeInTheDocument());
  });

  it('automatically saves edits without a save button', async () => {
    const updatedAsset = { ...asset, prompt: '古典写实人物，青绿色服饰', revision: 2 };
    mocks.listEpisodeAssets
      .mockResolvedValueOnce({ assets: [asset] })
      .mockResolvedValue({ assets: [updatedAsset] });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });

    expect(within(details).queryByRole('button', { name: /保存修改|Save changes/ })).not.toBeInTheDocument();
    fireEvent.change(within(details).getByLabelText(/视觉描述与提示词|Visual description and prompt/), {
      target: { value: updatedAsset.prompt },
    });

    expect(within(details).getByText(/编辑中，将自动保存|Editing.*automatically/)).toBeInTheDocument();
    await waitFor(() => expect(mocks.updateAssetPrompt).toHaveBeenCalledTimes(1), { timeout: 2_000 });
    expect(mocks.updateAssetPrompt).toHaveBeenCalledWith('project_1', 'asset_1', updatedAsset.prompt, 1);
    await waitFor(() =>
      expect(within(details).getByText(/已自动保存|Saved automatically/)).toBeInTheDocument(),
    );
  });

  it('places the current asset editing request in the Agent composer without sending it', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });

    fireEvent.click(within(details).getByRole('button', { name: /Agent 修改|Edit with Agent/ }));

    expect(mocks.startAgentWorkflow).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Agent input') as HTMLTextAreaElement).value).toMatch(
      /贾探春[\s\S]*asset_1/,
    );
  });

  it('keeps manual and Agent asset creation as separate actions', async () => {
    renderPage();

    const manualAdd = await screen.findByRole('button', { name: /新建资产|New asset/ });
    const addAsset = screen.getByRole('button', { name: /让 Agent 创建|Create with Agent/ });
    const planStoryboard = screen.getByRole('button', {
      name: /使用 Agent 规划故事板|Plan storyboard with Agent/,
    });
    expect(addAsset.closest('.asset-catalog > header')).toBeInTheDocument();
    expect(planStoryboard.closest('.asset-catalog > header')).toBeInTheDocument();
    expect(addAsset.compareDocumentPosition(planStoryboard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(document.querySelector('.creative-heading__actions')).not.toBeInTheDocument();
    fireEvent.click(addAsset);

    expect((screen.getByLabelText('Agent input') as HTMLTextAreaElement).value).toMatch(
      /角色|地点|道具|视觉风格|character|location|prop|visual style/i,
    );
    expect(screen.queryByRole('dialog', { name: /新建视频资产|Create video asset/ })).toBeNull();

    fireEvent.click(manualAdd);
    const dialog = screen.getByRole('dialog', { name: /新建视频资产|Create video asset/ });
    fireEvent.change(within(dialog).getByRole('textbox', { name: /^名称$|^Name$/ }), {
      target: { value: '无剧本道具' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: /创建资产|Create asset/ }));

    await waitFor(() =>
      expect(mocks.createAsset).toHaveBeenCalledWith('project_1', 'episode_1', 'character', '无剧本道具', ''),
    );
  });

  it('sends an asset card to the Agent composer as a reference without opening its details', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    renderPage();

    const assetCard = await screen.findByRole('button', { name: /贾探春/ });
    const card = assetCard.closest('article');
    expect(card).not.toBeNull();
    const reference = within(card as HTMLElement).getByRole('button', {
      name: /^引用$|^Reference$/,
    });
    expect(reference).toHaveClass('asset-agent-reference--compact');
    const title = within(assetCard).getByText('贾探春');
    await waitFor(() => expect(mocks.getMediaSlot).toHaveBeenCalled());
    expect(within(card as HTMLElement).queryByText(/720 × 1280/)).not.toBeInTheDocument();
    expect(title).toHaveClass('asset-card__name');
    expect(title).toHaveAttribute('title', '贾探春');
    expect(title.closest('.asset-card__meta')).toBeInTheDocument();
    expect(
      (card as HTMLElement).querySelector('.asset-card__meta')!.compareDocumentPosition(reference) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    fireEvent.click(reference);

    expect(screen.getByTestId('agent-reference')).toHaveTextContent('asset:贾探春');
    expect(screen.queryByRole('dialog', { name: '贾探春' })).not.toBeInTheDocument();
  });

  it('places the storyboard planning request in the Agent composer without sending it', async () => {
    renderPage();

    fireEvent.click(
      await screen.findByRole('button', {
        name: /使用 Agent 规划故事板|Plan storyboard with Agent/,
      }),
    );

    expect(mocks.startAgentWorkflow).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Agent input') as HTMLTextAreaElement).value).toMatch(/故事板|Storyboard/);
  });

  it('keeps the empty asset canvas clear while preserving the header creation actions', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({ assets: [] });
    renderPage();

    expect(await screen.findByRole('button', { name: /新建资产|New asset/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /让 Agent 创建|Create with Agent/ })).toBeInTheDocument();
    expect(screen.queryByText(/当前视频暂无资产|No assets in this video/i)).not.toBeInTheDocument();
  });

  it('automatically submits an image reference when the user adds one', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    expect(picker).toHaveAttribute('open');
    const confirmReference = within(picker).getByRole('button', {
      name: /使用这张图|Use this image/i,
    });
    expect(confirmReference.closest('.asset-reference-picker__footer')).toBeInTheDocument();
    expect(confirmReference).toHaveClass('button--primary');
    fireEvent.click(confirmReference);
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));

    await waitFor(() => expect(mocks.submitGeneration).toHaveBeenCalledTimes(1));
    expect(mocks.checkGenerationReadiness).toHaveBeenCalledWith(
      'project_1',
      'episode_1',
      expect.objectContaining({
        media_slot_id: 'slot_1',
        expected_slot_revision: 3,
        prompt: '古典写实人物',
        generator_id: 'image.codex',
        references: [{ role: 'image_reference', artifact_id: 'artifact_1' }],
      }),
    );
  });

  it('keeps adding references available, preserves selection order, and removes each image separately', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({ assets: [{ ...asset, primary_image_slot_id: 'slot_1' }] });
    const media = await mocks.getMediaSlot();
    mocks.getMediaSlot.mockResolvedValue({
      ...media,
      versions: [1, 2, 3].map((number) => ({
        ...media.versions[0],
        media_version_id: `version_${number}`,
        artifact_id: `artifact_${number}`,
        sequence_number: number,
      })),
      artifacts: [1, 2, 3].map((number) => ({ ...media.artifacts[0], artifact_id: `artifact_${number}` })),
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V2|Choose V2/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用参考图|Use reference images/i }));
    expect(
      within(details).getByRole('button', { name: /添加参考图.*2\/5|Add reference.*2\/5/ }),
    ).toBeEnabled();
    expect(details.querySelectorAll('.asset-reference-chip')).toHaveLength(2);

    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    expect(within(picker).getByRole('button', { name: /选择 V2|Choose V2/i })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V3|Choose V3/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用参考图|Use reference images/i }));
    fireEvent.click(within(details).getByRole('button', { name: /移除参考图.*V1|Remove reference.*V1/i }));
    expect(details.querySelectorAll('.asset-reference-chip')).toHaveLength(2);
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));
    await waitFor(() => expect(mocks.submitGeneration).toHaveBeenCalledTimes(1));
    expect(mocks.submitGeneration).toHaveBeenCalledWith(
      'project_1',
      'episode_1',
      expect.objectContaining({
        references: [
          { role: 'image_reference', artifact_id: 'artifact_2' },
          { role: 'image_reference', artifact_id: 'artifact_3' },
        ],
      }),
    );
  });

  it('also supports multiple ordered references in the Storyboard asset detail panel', async () => {
    const media = await mocks.getMediaSlot();
    mocks.getMediaSlot.mockResolvedValue({
      ...media,
      versions: [
        media.versions[0],
        {
          ...media.versions[0],
          media_version_id: 'version_2',
          artifact_id: 'artifact_2',
          sequence_number: 2,
        },
      ],
      artifacts: [media.artifacts[0], { ...media.artifacts[0], artifact_id: 'artifact_2' }],
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const panel = (nextAsset = { ...asset, primary_image_slot_id: 'slot_1' }) => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AssetDetailPanel
            projectID="project_1"
            episodeID="episode_1"
            asset={nextAsset as never}
            onClose={vi.fn()}
          />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const view = render(panel());
    const details = screen.getByRole('dialog', { name: '贾探春' });
    const addReference = within(details).getByRole('button', { name: /添加参考图|Add reference/ });
    await waitFor(() => expect(addReference).toBeEnabled());
    fireEvent.click(addReference);
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V2|Choose V2/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用参考图|Use reference images/i }));
    expect(details.querySelectorAll('.asset-reference-chip')).toHaveLength(2);
    expect(within(details).getByRole('button', { name: /添加参考图|Add reference/ })).toBeEnabled();
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));
    await waitFor(() =>
      expect(mocks.submitGeneration).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        expect.objectContaining({
          references: [
            { role: 'image_reference', artifact_id: 'artifact_2' },
            { role: 'image_reference', artifact_id: 'artifact_1' },
          ],
        }),
      ),
    );
    view.rerender(
      panel({
        ...asset,
        asset_id: 'asset_2',
        primary_image_slot_id: 'slot_2',
        identity: { ...asset.identity, name: 'Other asset' },
      }),
    );
    expect(
      screen.getByRole('dialog', { name: 'Other asset' }).querySelectorAll('.asset-reference-chip'),
    ).toHaveLength(0);
  });

  it('clears references when opening a different asset', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [
        { ...asset, primary_image_slot_id: 'slot_1' },
        {
          ...asset,
          asset_id: 'asset_2',
          primary_image_slot_id: 'slot_2',
          identity: { ...asset.identity, name: '林黛玉' },
        },
      ],
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用这张图|Use this image/i }));
    fireEvent.click(within(details).getByRole('button', { name: /关闭|Close/i }));
    fireEvent.click(screen.getByRole('button', { name: /林黛玉/ }));
    const otherDetails = await screen.findByRole('dialog', { name: '林黛玉' });
    expect(otherDetails.querySelectorAll('.asset-reference-chip')).toHaveLength(0);
    fireEvent.click(within(otherDetails).getByRole('button', { name: /生成图片|Generate image/ }));
    await waitFor(() =>
      expect(mocks.submitGeneration).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        expect.objectContaining({ references: [] }),
      ),
    );
  });

  it.each(['assets page', 'storyboard panel'])(
    'keeps %s usable with the legacy null inputs response and submits multiple references',
    async (entry) => {
      mocks.listEpisodeAssets.mockResolvedValue({ assets: [{ ...asset, primary_image_slot_id: 'slot_1' }] });
      const catalog = await mocks.listProviders();
      mocks.listProviders.mockResolvedValue({
        providers: [
          {
            ...catalog.providers[0],
            model_id: 'codex-image',
            models: [
              {
                model_id: 'codex-image',
                is_default: true,
                health: 'ready',
                capabilities: { inputs: null, generation_modes: null, output: { sizes: null } },
              },
            ],
          },
        ],
      });
      const media = await mocks.getMediaSlot();
      mocks.getMediaSlot.mockResolvedValue({
        ...media,
        versions: [
          media.versions[0],
          {
            ...media.versions[0],
            media_version_id: 'version_2',
            artifact_id: 'artifact_2',
            sequence_number: 2,
          },
        ],
        artifacts: [media.artifacts[0], { ...media.artifacts[0], artifact_id: 'artifact_2' }],
      });
      if (entry === 'assets page') {
        renderPage();
        fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
      } else {
        const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
        render(
          <QueryClientProvider client={client}>
            <MemoryRouter>
              <AssetDetailPanel
                projectID="project_1"
                episodeID="episode_1"
                asset={{ ...asset, primary_image_slot_id: 'slot_1' } as never}
                onClose={vi.fn()}
              />
            </MemoryRouter>
          </QueryClientProvider>,
        );
      }
      const details = await screen.findByRole('dialog', { name: '贾探春' });
      const addReference = within(details).getByRole('button', { name: /添加参考图|Add reference/ });
      await waitFor(() => expect(addReference).toBeEnabled());
      fireEvent.click(addReference);
      const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
      fireEvent.click(within(picker).getByRole('button', { name: /选择 V2|Choose V2/i }));
      fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
      fireEvent.click(within(picker).getByRole('button', { name: /使用参考图|Use reference images/i }));
      expect(details.querySelectorAll('.asset-reference-chip')).toHaveLength(2);
      expect(addReference).toBeEnabled();
      fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));
      await waitFor(() =>
        expect(mocks.submitGeneration).toHaveBeenCalledWith(
          'project_1',
          'episode_1',
          expect.objectContaining({
            references: [
              { role: 'image_reference', artifact_id: 'artifact_2' },
              { role: 'image_reference', artifact_id: 'artifact_1' },
            ],
          }),
        ),
      );
    },
  );

  it('retains chosen images and blocks generation when a new provider has a lower capability limit', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({ assets: [{ ...asset, primary_image_slot_id: 'slot_1' }] });
    const media = await mocks.getMediaSlot();
    mocks.getMediaSlot.mockResolvedValue({
      ...media,
      versions: [
        media.versions[0],
        {
          ...media.versions[0],
          media_version_id: 'version_2',
          artifact_id: 'artifact_2',
          sequence_number: 2,
        },
      ],
      artifacts: [media.artifacts[0], { ...media.artifacts[0], artifact_id: 'artifact_2' }],
    });
    const catalog = await mocks.listProviders();
    const codex = catalog.providers[0];
    mocks.listProviders.mockResolvedValue({
      providers: [
        codex,
        {
          ...codex,
          generator_id: 'image.shotforward',
          model_id: 'cloud_image',
          models: [
            {
              model_id: 'cloud_image',
              is_default: true,
              health: 'ready',
              capabilities: {
                inputs: [{ role: 'reference_image', maximum_count: 5 }],
                maximum_total_inputs: 1,
              },
            },
          ],
        },
      ],
    });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V2|Choose V2/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用参考图|Use reference images/i }));
    fireEvent.click(
      within(details).getByRole('combobox', { name: /图像生成服务|Image generation service/i }),
    );
    fireEvent.click(screen.getByRole('option', { name: /ShotForward 云服务|ShotForward Cloud/i }));
    expect(details.querySelectorAll('.asset-reference-chip')).toHaveLength(2);
    expect(within(details).getByRole('alert')).toHaveTextContent(/最多支持 1 张|at most 1 reference/i);
    expect(within(details).getByRole('button', { name: /生成图片|Generate image/ })).toBeDisabled();
    expect(mocks.submitGeneration).not.toHaveBeenCalled();
    fireEvent.click(within(details).getByRole('button', { name: /移除参考图.*V2|Remove reference.*V2/i }));
    expect(within(details).queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));
    await waitFor(() =>
      expect(mocks.submitGeneration).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        expect.objectContaining({
          generator_id: 'image.shotforward',
          provider_model_id: 'cloud_image',
          references: [{ role: 'image_reference', artifact_id: 'artifact_1' }],
        }),
      ),
    );
  });

  it('shows the concrete readiness failure instead of replacing it with a generic message', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    mocks.checkGenerationReadiness.mockRejectedValue(
      new Error('generator does not support this reference image'),
    );
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(picker).getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用这张图|Use this image/i }));
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));

    expect(await within(details).findByRole('alert')).toHaveTextContent(
      'generator does not support this reference image',
    );
    expect(mocks.submitGeneration).not.toHaveBeenCalled();
  });

  it('finds and selects a different Episode variant from the unified project library', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    const otherArtifact = {
      artifact_id: 'artifact_other_episode',
      kind: 'image',
      mime_type: 'image/png',
      original_filename: 'tanchun-episode-3.png',
      byte_size: 120,
      width: 720,
      height: 1280,
      duration_ms: 0,
      created_at: '2026-09-08T00:00:00Z',
    };
    mocks.getProjectReferenceImages.mockResolvedValue({
      project: { project_id: 'project_1', name: '红楼梦' },
      episodes: [
        {
          episode_id: 'episode_1',
          project_id: 'project_1',
          sequence_number: 1,
          title: '第 1 集',
          target_duration_ms: 60_000,
          revision: 1,
          script_revision: 1,
          created_at: '2026-09-06T00:00:00Z',
          updated_at: '2026-09-06T00:00:00Z',
          script_updated_at: '2026-09-06T00:00:00Z',
        },
        {
          episode_id: 'episode_3',
          project_id: 'project_1',
          sequence_number: 3,
          title: '凤姐提醒',
          target_duration_ms: 60_000,
          revision: 1,
          script_revision: 1,
          created_at: '2026-09-08T00:00:00Z',
          updated_at: '2026-09-08T00:00:00Z',
          script_updated_at: '2026-09-08T00:00:00Z',
        },
      ],
      images: [
        {
          key: 'asset:asset_episode_3',
          source: 'asset',
          name: '贾探春',
          episode: {
            episode_id: 'episode_3',
            project_id: 'project_1',
            sequence_number: 3,
            title: '凤姐提醒',
            updated_at: '2026-09-08T00:00:00Z',
          },
          artifact: otherArtifact,
          updated_at: '2026-09-08T00:00:00Z',
          asset: {
            ...asset,
            asset_id: 'asset_episode_3',
            episode_id: 'episode_3',
            primary_image_slot_id: 'slot_episode_3',
          },
        },
      ],
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.change(within(picker).getByRole('textbox', { name: /搜索资产名|Search assets/i }), {
      target: { value: '凤姐提醒' },
    });
    fireEvent.click(await within(picker).findByRole('button', { name: /贾探春.*凤姐提醒/i }));
    fireEvent.click(within(picker).getByRole('button', { name: /使用这张图|Use this image/i }));
    fireEvent.click(within(details).getByRole('button', { name: /生成图片|Generate image/ }));

    await waitFor(() =>
      expect(mocks.checkGenerationReadiness).toHaveBeenCalledWith(
        'project_1',
        'episode_1',
        expect.objectContaining({
          references: [{ role: 'image_reference', artifact_id: 'artifact_other_episode' }],
        }),
      ),
    );
  });

  it('uploads a new reference into the Episode library and selects it', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    const uploadedArtifact = {
      artifact_id: 'artifact_uploaded_reference',
      kind: 'image',
      mime_type: 'image/png',
      original_filename: 'costume.png',
      byte_size: 128,
      width: 720,
      height: 1280,
      duration_ms: 0,
      created_at: '2026-09-08T00:00:00Z',
    };
    mocks.uploadEpisodeReferenceMedia.mockResolvedValue({
      reference_media: {
        episode_id: 'episode_1',
        name: '服装参考',
        description: '',
        usage_count: 0,
        revision: 1,
        created_at: '2026-09-08T00:00:00Z',
        updated_at: '2026-09-08T00:00:00Z',
        artifact: uploadedArtifact,
      },
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: /贾探春/ }));
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /添加参考图|Add reference/ }));
    const picker = await screen.findByRole('dialog', { name: /选择参考图|Choose reference images/ });
    const file = new File(['image'], 'costume.png', { type: 'image/png' });
    const uploadInput = within(picker).getByLabelText(/上传图片|Upload image/i);
    expect(uploadInput).toHaveClass('sr-only');
    expect(within(picker).getByRole('button', { name: /上传图片|Upload image/i })).toHaveClass(
      'asset-reference-picker__upload',
    );
    fireEvent.change(uploadInput, {
      target: { files: [file] },
    });

    await waitFor(() =>
      expect(mocks.uploadEpisodeReferenceMedia).toHaveBeenCalledWith('project_1', 'episode_1', file, 'image'),
    );
    expect(await within(picker).findByText('服装参考')).toBeInTheDocument();
    fireEvent.click(within(picker).getByRole('button', { name: /使用这张图|Use this image/i }));
    expect(within(details).getByText('服装参考')).toBeInTheDocument();
  });

  it('shows an in-place progress overlay for the active image task', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    mocks.getTaskSnapshot.mockResolvedValue({
      stream_epoch: 'epoch',
      cursor: 'epoch:2',
      tasks: [
        {
          task_type: 'generation_job',
          task_id: 'job_active',
          project_id: 'project_1',
          episode_id: 'episode_1',
          state: 'running',
          progress_stage: 'generating',
          progress_message: 'Rendering image',
          failure_code: '',
          revision: 2,
          created_at: '2026-09-06T00:00:00Z',
          updated_at: '2026-09-06T00:00:01Z',
          target_media_slot_id: 'slot_1',
          target_media_kind: 'image',
        },
      ],
    });
    renderPage();

    const assetCard = await screen.findByRole('button', { name: /贾探春/ });
    expect(within(assetCard).getByRole('status')).toHaveTextContent(/正在生成图片|Generating image/);

    fireEvent.click(assetCard);
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    expect(within(details).getByRole('status')).toHaveTextContent(/正在生成图片|Generating image/);
    expect(within(details).getByRole('status')).not.toHaveTextContent('Rendering image');
  });

  it('fills an asset card when generation finishes after its details are closed', async () => {
    mocks.listEpisodeAssets.mockResolvedValue({
      assets: [{ ...asset, primary_image_slot_id: 'slot_1' }],
    });
    const emptyMedia = {
      slot: {
        media_slot_id: 'slot_1',
        episode_id: 'episode_1',
        owner_kind: 'asset',
        owner_id: 'asset_1',
        kind: 'asset_primary_image',
        current_media_version_id: '',
        revision: 1,
        updated_at: '2026-09-20T00:00:00Z',
      },
      versions: [],
      artifacts: [],
    };
    const generatedMedia = {
      slot: {
        ...emptyMedia.slot,
        current_media_version_id: 'version_2',
        revision: 2,
        updated_at: '2026-09-20T00:00:02Z',
      },
      versions: [
        {
          media_version_id: 'version_2',
          artifact_id: 'artifact_2',
          sequence_number: 1,
          trim_in_ms: 0,
          trim_out_ms: 0,
          revision: 1,
          created_at: '2026-09-20T00:00:02Z',
          updated_at: '2026-09-20T00:00:02Z',
        },
      ],
      artifacts: [
        {
          artifact_id: 'artifact_2',
          kind: 'image',
          mime_type: 'image/png',
          original_filename: 'generated.png',
          byte_size: 100,
          width: 720,
          height: 1280,
          duration_ms: 0,
          created_at: '2026-09-20T00:00:02Z',
        },
      ],
    };
    let generationFinished = false;
    mocks.getMediaSlot.mockImplementation(() =>
      Promise.resolve(generationFinished ? generatedMedia : emptyMedia),
    );
    const { client } = renderPage();

    const card = await screen.findByRole('button', { name: /贾探春/ });
    expect(await within(card).findByText(/还没有主图|No primary image/i)).toBeInTheDocument();
    fireEvent.click(card);
    const details = await screen.findByRole('dialog', { name: '贾探春' });
    fireEvent.click(within(details).getByRole('button', { name: /关闭|Close/i }));
    expect(screen.queryByRole('dialog', { name: '贾探春' })).toBeNull();
    await waitFor(() => expect(client.getQueryData(['tasks'])).toBeDefined());
    generationFinished = true;
    act(() => {
      client.setQueryData(['tasks'], {
        stream_epoch: 'epoch',
        cursor: 'epoch:2',
        tasks: [
          {
            task_type: 'generation_job',
            task_id: 'job_1',
            project_id: 'project_1',
            episode_id: 'episode_1',
            state: 'succeeded',
            progress_stage: 'succeeded',
            progress_message: '',
            failure_code: '',
            revision: 2,
            result_media_version_id: 'version_2',
            target_media_slot_id: 'slot_1',
            target_media_kind: 'image',
            created_at: '2026-09-20T00:00:00Z',
            updated_at: '2026-09-20T00:00:02Z',
          },
        ],
      });
    });

    expect(await within(card).findByRole('img', { name: '贾探春' })).toBeInTheDocument();
    expect(mocks.getMediaSlot.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});
