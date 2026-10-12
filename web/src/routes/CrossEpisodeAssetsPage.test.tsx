import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { CrossEpisodeAssetsPage } from './CrossEpisodeAssetsPage';

const assetMocks = vi.hoisted(() => ({
  getProjectAssetVariants: vi.fn(),
  getMediaSlot: vi.fn(),
}));

vi.mock('../api/assets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/assets')>();
  return {
    ...actual,
    getProjectAssetVariants: assetMocks.getProjectAssetVariants,
    getMediaSlot: assetMocks.getMediaSlot,
  };
});

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

describe('CrossEpisodeAssetsPage', () => {
  it('previews index and continuity images and exposes the full long prompt', async () => {
    const longPrompt =
      '【剧本依据】初入荣国府的年轻外孙女，敏感自持、怯而有礼，手攥素帕，面对陌生深宅既拘谨又坚韧。服装、发饰、光线与人物气质保持跨集连续。';
    assetMocks.getProjectAssetVariants.mockResolvedValue({
      project: {
        project_id: 'prj_1',
        name: '红楼梦',
        description: '',
        output_width: 1920,
        output_height: 1080,
        revision: 1,
        created_at: '2026-09-10T00:00:00Z',
        updated_at: '2026-09-10T00:00:00Z',
      },
      episodes: [
        {
          episode_id: 'ep_1',
          project_id: 'prj_1',
          sequence_number: 1,
          title: '黛玉进府',
          target_duration_ms: 120_000,
          revision: 1,
          script_revision: 1,
          created_at: '2026-09-10T00:00:00Z',
          updated_at: '2026-09-10T00:00:00Z',
          script_updated_at: '2026-09-10T00:00:00Z',
        },
      ],
      variants: [
        {
          episode: {
            episode_id: 'ep_1',
            project_id: 'prj_1',
            sequence_number: 1,
            title: '黛玉进府',
            target_duration_ms: 120_000,
            revision: 1,
            script_revision: 1,
            created_at: '2026-09-10T00:00:00Z',
            updated_at: '2026-09-10T00:00:00Z',
            script_updated_at: '2026-09-10T00:00:00Z',
          },
          asset: {
            asset_id: 'ast_1',
            project_id: 'prj_1',
            episode_id: 'ep_1',
            identity: {
              asset_identity_id: 'asi_1',
              project_id: 'prj_1',
              kind: 'character',
              name: '林黛玉',
              revision: 1,
              created_at: '2026-09-10T00:00:00Z',
              updated_at: '2026-09-10T00:00:00Z',
            },
            prompt: longPrompt,
            revision: 1,
            primary_image_slot_id: 'slot_1',
            created_at: '2026-09-10T00:00:00Z',
            updated_at: '2026-09-10T00:00:00Z',
          },
        },
      ],
    });
    assetMocks.getMediaSlot.mockResolvedValue({
      slot: {
        media_slot_id: 'slot_1',
        episode_id: 'ep_1',
        owner_kind: 'asset',
        owner_id: 'ast_1',
        kind: 'image',
        current_media_version_id: 'version_1',
        revision: 1,
        updated_at: '2026-09-10T00:00:00Z',
      },
      versions: [
        {
          media_version_id: 'version_1',
          artifact_id: 'artifact_1',
          sequence_number: 1,
          trim_in_ms: 0,
          trim_out_ms: 0,
          revision: 1,
          created_at: '2026-09-10T00:00:00Z',
          updated_at: '2026-09-10T00:00:00Z',
        },
      ],
      artifacts: [
        {
          artifact_id: 'artifact_1',
          kind: 'image',
          mime_type: 'image/png',
          original_filename: 'lin-daiyu.png',
          byte_size: 1024,
          width: 1024,
          height: 1536,
          duration_ms: 0,
          created_at: '2026-09-10T00:00:00Z',
        },
      ],
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/projects/prj_1/assets']}>
          <Routes>
            <Route path="/projects/:projectId/assets" element={<CrossEpisodeAssetsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await screen.findByRole('button', { name: /^(?:放大预览|Preview )林黛玉$/i }, { timeout: 5_000 });
    const continuityPreview = await screen.findByRole(
      'button',
      { name: /^(?:放大预览|Preview )林黛玉 · 黛玉进府$/i },
      { timeout: 5_000 },
    );
    expect(document.querySelector('.variant-prompt')).toHaveAttribute('tabindex', '0');
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent(longPrompt);

    fireEvent.click(continuityPreview);
    await waitFor(() =>
      expect(screen.getByRole('dialog', { name: /林黛玉.*黛玉进府/ })).toHaveAttribute('open'),
    );
    expect(screen.getByRole('dialog').querySelector('img')).toHaveAttribute(
      'src',
      '/api/v1/projects/prj_1/artifacts/artifact_1/content',
    );
  });
});
