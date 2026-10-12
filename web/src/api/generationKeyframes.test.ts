import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MediaSlotDetails } from './assets';

const mocks = vi.hoisted(() => ({
  getMediaSlot: vi.fn(),
  extractVideoFrame: vi.fn(),
}));

vi.mock('./assets', async (loadOriginal) => ({
  ...(await loadOriginal<typeof import('./assets')>()),
  getMediaSlot: mocks.getMediaSlot,
  extractVideoFrame: mocks.extractVideoFrame,
}));

import { refreshGenerationKeyframes } from './generationKeyframes';

function media(slotID: string, revision: number, current = '') {
  return {
    slot: { media_slot_id: slotID, revision, current_media_version_id: current },
    versions: [],
    artifacts: [],
  } as unknown as MediaSlotDetails;
}

function namedMedia(slotID: string, revision: number, artifactID: string, filename: string) {
  return {
    slot: { media_slot_id: slotID, revision, current_media_version_id: `version_${artifactID}` },
    versions: [{ media_version_id: `version_${artifactID}`, artifact_id: artifactID }],
    artifacts: [{ artifact_id: artifactID, original_filename: filename }],
  } as unknown as MediaSlotDetails;
}

describe('refreshGenerationKeyframes', () => {
  beforeEach(() => vi.clearAllMocks());

  it('refreshes both endpoints from the latest adjacent video slots', async () => {
    const first = namedMedia(
      'slot_first',
      4,
      'old_first',
      'previous-video-last-frame.png',
    );
    const last = namedMedia('slot_last', 6, 'old_last', 'next-video-first-frame.png');
    const previous = media('slot_previous_video', 2, 'previous_video');
    const next = media('slot_next_video', 3, 'next_video');
    const refreshedFirst = media('slot_first', 5, 'new_first');
    const refreshedLast = media('slot_last', 7, 'new_last');
    mocks.getMediaSlot.mockImplementation(
      async (_projectID: string, slotID: string) =>
        ({ slot_first: first, slot_last: last, slot_previous_video: previous, slot_next_video: next })[
          slotID
        ],
    );
    mocks.extractVideoFrame.mockResolvedValueOnce(refreshedFirst).mockResolvedValueOnce(refreshedLast);

    await expect(
      refreshGenerationKeyframes({
        projectID: 'project_1',
        generationMode: 'first_last_image',
        firstFrameSlotID: 'slot_first',
        lastFrameSlotID: 'slot_last',
        previousVideoSlotID: 'slot_previous_video',
        nextVideoSlotID: 'slot_next_video',
        firstMedia: first,
        lastMedia: last,
      }),
    ).resolves.toEqual({ first: refreshedFirst, last: refreshedLast });
    expect(mocks.extractVideoFrame.mock.calls).toEqual([
      ['project_1', 'slot_first', 'slot_previous_video', 4],
      ['project_1', 'slot_last', 'slot_next_video', 6],
    ]);
  });

  it('keeps the current endpoint when an adjacent video has no generated version', async () => {
    const first = media('slot_first', 4, 'old_first');
    const previous = media('slot_previous_video', 1);
    mocks.getMediaSlot.mockResolvedValueOnce(first).mockResolvedValueOnce(previous);

    await expect(
      refreshGenerationKeyframes({
        projectID: 'project_1',
        generationMode: 'first_image',
        firstFrameSlotID: 'slot_first',
        lastFrameSlotID: 'slot_last',
        previousVideoSlotID: 'slot_previous_video',
        firstMedia: first,
        lastMedia: undefined,
      }),
    ).resolves.toEqual({ first, last: undefined });
    expect(mocks.extractVideoFrame).not.toHaveBeenCalled();
  });

  it('preserves manually selected endpoints instead of replacing them from adjacent videos', async () => {
    const first = namedMedia('slot_first', 4, 'manual_first', 'manual-first.png');
    const last = namedMedia('slot_last', 6, 'manual_last', 'manual-last.png');

    await expect(
      refreshGenerationKeyframes({
        projectID: 'project_1',
        generationMode: 'first_last_image',
        firstFrameSlotID: 'slot_first',
        lastFrameSlotID: 'slot_last',
        previousVideoSlotID: 'slot_previous_video',
        nextVideoSlotID: 'slot_next_video',
        firstMedia: first,
        lastMedia: last,
      }),
    ).resolves.toEqual({ first, last });
    expect(mocks.getMediaSlot).not.toHaveBeenCalled();
    expect(mocks.extractVideoFrame).not.toHaveBeenCalled();
  });

  it('uses the next video unit selected first frame before extracting from its generated video', async () => {
    const first = namedMedia('slot_first', 4, 'manual_first', 'manual-first.png');
    const nextFirst = namedMedia('slot_next_first', 3, 'next_first', 'next-first.png');

    await expect(
      refreshGenerationKeyframes({
        projectID: 'project_1',
        generationMode: 'first_last_image',
        firstFrameSlotID: 'slot_first',
        lastFrameSlotID: 'slot_last',
        nextFirstFrameMedia: nextFirst,
        nextVideoSlotID: 'slot_next_video',
        firstMedia: first,
        lastMedia: media('slot_last', 1),
      }),
    ).resolves.toEqual({ first, last: nextFirst });
    expect(mocks.getMediaSlot).not.toHaveBeenCalled();
    expect(mocks.extractVideoFrame).not.toHaveBeenCalled();
  });
});
