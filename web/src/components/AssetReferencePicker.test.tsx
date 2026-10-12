import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import type { Artifact, EpisodeAsset, MediaSlotDetails } from '../api/assets';
import { AssetReferencePicker, type AssetReferenceSelection } from './AssetReferencePicker';

const mocks = vi.hoisted(() => ({
  getProjectReferenceImages: vi.fn(),
  uploadEpisodeReferenceMedia: vi.fn(),
}));
vi.mock('../api/assets', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/assets')>()),
  getProjectReferenceImages: mocks.getProjectReferenceImages,
  uploadEpisodeReferenceMedia: mocks.uploadEpisodeReferenceMedia,
}));

const asset = { asset_id: 'asset_1', identity: { name: 'Character' } } as EpisodeAsset;
const artifact = (id: string) =>
  ({
    artifact_id: id,
    kind: 'image',
    mime_type: 'image/png',
    original_filename: `${id}.png`,
    byte_size: 100,
    width: 720,
    height: 1280,
    duration_ms: 0,
    created_at: '2026-10-04T00:00:00Z',
  }) as Artifact;
const media = {
  versions: [1, 2, 3, 4, 5, 6].map((number) => ({
    media_version_id: `version_${number}`,
    artifact_id: `artifact_${number}`,
    sequence_number: number,
  })),
  artifacts: [1, 2, 3, 4, 5, 6].map((number) => artifact(`artifact_${number}`)),
} as MediaSlotDetails;
const uploadResult = (name: string) => ({ reference_media: { name, artifact: artifact(name) } });

function renderPicker(value: AssetReferenceSelection[] = [], maximumReferences = 5) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props = {
    open: true,
    projectID: 'project_1',
    episodeID: 'episode_1',
    asset,
    media,
    value,
    maximumReferences,
    onSelect: vi.fn(),
    onClose: vi.fn(),
    onUploadBusyChange: vi.fn(),
  };
  const content = (overrides: Partial<typeof props> = {}) => (
    <QueryClientProvider client={client}>
      <AssetReferencePicker {...props} {...overrides} />
    </QueryClientProvider>
  );
  const view = render(content());
  return {
    ...view,
    ...props,
    client,
    changeProps: (overrides: Partial<typeof props>) => view.rerender(content(overrides)),
  };
}

describe('AssetReferencePicker multiple references', () => {
  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute('open', '');
    };
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute('open');
      this.dispatchEvent(new Event('close'));
    };
  });
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getProjectReferenceImages.mockResolvedValue({
      episodes: [{ episode_id: 'episode_1', title: 'Episode 1', sequence_number: 1 }],
      images: [],
    });
    mocks.uploadEpisodeReferenceMedia.mockImplementation(async (_project, _episode, file: File) =>
      uploadResult(file.name),
    );
  });

  it('selects aliases by artifact ID and keeps one entry in click order', async () => {
    mocks.getProjectReferenceImages.mockResolvedValue({
      episodes: [],
      images: [
        {
          key: 'upload:alias',
          source: 'upload',
          name: 'Alias',
          artifact: artifact('artifact_1'),
          episode: { episode_id: 'episode_1', title: 'Episode 1', sequence_number: 1 },
          updated_at: '2026-10-04',
        },
      ],
    });
    const view = renderPicker();
    const dialog = screen.getByRole('dialog', { name: /选择参考图|Choose reference images/ });
    fireEvent.click(within(dialog).getByRole('button', { name: /选择 V2|Choose V2/i }));
    fireEvent.click(within(dialog).getByRole('button', { name: /选择 V1|Choose V1/i }));
    const alias = await within(dialog).findByRole('button', { name: /选择 Alias|Choose Alias/i });
    expect(alias).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(alias);
    expect(within(dialog).getByRole('button', { name: /选择 V1|Choose V1/i })).toHaveAttribute(
      'aria-pressed',
      'false',
    );
    fireEvent.click(alias);
    fireEvent.click(within(dialog).getByRole('button', { name: /使用参考图|Use reference images/i }));
    expect(view.onSelect).toHaveBeenCalledWith([
      expect.objectContaining({ artifact: expect.objectContaining({ artifact_id: 'artifact_2' }) }),
      expect.objectContaining({
        key: 'upload:alias',
        artifact: expect.objectContaining({ artifact_id: 'artifact_1' }),
      }),
    ]);
  });

  it('rejects the sixth choice without dropping the five already selected', async () => {
    const view = renderPicker();
    const dialog = screen.getByRole('dialog', { name: /选择参考图|Choose reference images/ });
    for (const number of [1, 2, 3, 4, 5, 6]) {
      fireEvent.click(
        within(dialog).getByRole('button', { name: new RegExp(`选择 V${number}|Choose V${number}`, 'i') }),
      );
    }
    expect(within(dialog).getByRole('button', { name: /选择 V6|Choose V6/i })).toHaveAttribute(
      'aria-pressed',
      'false',
    );
    expect(within(dialog).getByRole('alert')).toHaveTextContent(/最多支持 5 张|at most 5 reference/i);
    fireEvent.click(within(dialog).getByRole('button', { name: /使用参考图|Use reference images/i }));
    expect(
      view.onSelect.mock.calls[0]![0].map((value: AssetReferenceSelection) => value.artifact.artifact_id),
    ).toEqual([1, 2, 3, 4, 5].map((number) => `artifact_${number}`));
  });

  it('allows confirming removal of all existing references', () => {
    const view = renderPicker([
      {
        key: 'version:version_1',
        artifact: artifact('artifact_1'),
        name: 'V1',
        episodeID: 'episode_1',
        episodeTitle: '',
        source: 'version',
      },
    ]);
    fireEvent.click(screen.getByRole('button', { name: /选择 V1|Choose V1/i }));
    fireEvent.click(screen.getByRole('button', { name: /清除参考图|Clear reference images/i }));
    expect(view.onSelect).toHaveBeenCalledWith([]);
  });

  it('uploads all selected files in order and disables confirmation while uploading', async () => {
    let finishFirst!: (value: ReturnType<typeof uploadResult>) => void;
    mocks.uploadEpisodeReferenceMedia.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishFirst = resolve;
        }),
    );
    const view = renderPicker();
    const files = [
      new File(['a'], 'a.png', { type: 'image/png' }),
      new File(['b'], 'b.webp', { type: 'image/webp' }),
    ];
    const input = screen.getByLabelText(/上传图片|Upload image/i);
    expect(input).toHaveAttribute('multiple');
    fireEvent.change(input, { target: { files } });
    await waitFor(() => expect(view.onUploadBusyChange).toHaveBeenLastCalledWith(true));
    expect(screen.getByRole('button', { name: /使用这张图|Use this image/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /选择 V1|Choose V1/i })).toBeDisabled();
    await act(async () => {
      finishFirst(uploadResult('a.png'));
    });
    await waitFor(() => expect(view.onUploadBusyChange).toHaveBeenLastCalledWith(false));
    expect(mocks.uploadEpisodeReferenceMedia.mock.calls.map((call) => call[2])).toEqual(files);
    fireEvent.click(screen.getByRole('button', { name: /使用参考图|Use reference images/i }));
    expect(
      view.onSelect.mock.calls[0]![0].map((value: AssetReferenceSelection) => value.artifact.artifact_id),
    ).toEqual(['a.png', 'b.webp']);
  });

  it.each(['count', 'type', 'size'])(
    'validates the entire file batch before uploading: %s',
    async (invalid) => {
      renderPicker();
      const valid = new File(['a'], 'a.png', { type: 'image/png' });
      const tooLarge = new File(['b'], 'b.png', { type: 'image/png' });
      Object.defineProperty(tooLarge, 'size', { value: 26 * 1024 * 1024 });
      const files =
        invalid === 'count'
          ? Array.from({ length: 6 }, () => valid)
          : [valid, invalid === 'size' ? tooLarge : new File(['text'], 'b.txt', { type: 'text/plain' })];
      fireEvent.change(screen.getByLabelText(/上传图片|Upload image/i), { target: { files } });
      expect(await screen.findByRole('alert')).toBeInTheDocument();
      expect(mocks.uploadEpisodeReferenceMedia).not.toHaveBeenCalled();
    },
  );

  it('preserves completed uploads when a later file in the batch fails', async () => {
    mocks.uploadEpisodeReferenceMedia
      .mockResolvedValueOnce(uploadResult('a.png'))
      .mockRejectedValueOnce(new Error('upload failed'));
    const view = renderPicker();
    fireEvent.change(screen.getByLabelText(/上传图片|Upload image/i), {
      target: {
        files: [
          new File(['a'], 'a.png', { type: 'image/png' }),
          new File(['b'], 'b.png', { type: 'image/png' }),
        ],
      },
    });
    expect(await screen.findByRole('alert')).toHaveTextContent(/参考图导入失败|Reference import failed/i);
    expect(screen.getByRole('button', { name: /使用这张图|Use this image/i })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: /使用这张图|Use this image/i }));
    expect(view.onSelect).toHaveBeenCalledWith([expect.objectContaining({ name: 'a.png' })]);
  });

  it.each(['close', 'asset'])('does not attach a late upload to a new picker scope: %s', async (change) => {
    let finish!: (value: ReturnType<typeof uploadResult>) => void;
    mocks.uploadEpisodeReferenceMedia.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const view = renderPicker();
    const invalidate = vi.spyOn(view.client, 'invalidateQueries');
    fireEvent.change(screen.getByLabelText(/上传图片|Upload image/i), {
      target: { files: [new File(['a'], 'late.png', { type: 'image/png' })] },
    });
    await waitFor(() => expect(view.onUploadBusyChange).toHaveBeenLastCalledWith(true));
    if (change === 'close') {
      fireEvent.click(screen.getByRole('button', { name: /关闭|Close/i }));
      view.changeProps({ open: false });
      view.changeProps({ open: true });
    } else {
      view.changeProps({
        asset: { ...asset, asset_id: 'asset_2', identity: { ...asset.identity, name: 'Other' } },
      });
    }
    await act(async () => {
      finish(uploadResult('late.png'));
    });
    await waitFor(() => expect(view.onUploadBusyChange).toHaveBeenLastCalledWith(false));
    expect(screen.queryByText('late.png')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /使用这张图|Use this image/i })).toBeDisabled();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['project-reference-images', 'project_1'] });
  });
});
