import { extractVideoFrame, getMediaSlot, type MediaSlotDetails } from './assets';

const automaticPreviousFrameFilename = 'previous-video-last-frame.png';
const automaticNextFrameFilename = 'next-video-first-frame.png';

function currentArtifactFilename(media: MediaSlotDetails | undefined) {
  const currentID = media?.slot.current_media_version_id;
  const version = media?.versions.find((item) => item.media_version_id === currentID);
  return media?.artifacts.find((item) => item.artifact_id === version?.artifact_id)?.original_filename;
}

function mayRefreshAutomaticFrame(media: MediaSlotDetails | undefined, filename: string) {
  return !media?.slot.current_media_version_id || currentArtifactFilename(media) === filename;
}

export async function refreshGenerationKeyframes({
  projectID,
  generationMode,
  firstFrameSlotID,
  lastFrameSlotID,
  previousVideoSlotID,
  nextFirstFrameMedia,
  nextVideoSlotID,
  firstMedia,
  lastMedia,
}: {
  projectID: string;
  generationMode: 'first_image' | 'first_last_image';
  firstFrameSlotID: string;
  lastFrameSlotID: string;
  previousVideoSlotID?: string;
  nextFirstFrameMedia?: MediaSlotDetails;
  nextVideoSlotID?: string;
  firstMedia: MediaSlotDetails | undefined;
  lastMedia: MediaSlotDetails | undefined;
}) {
  let first = firstMedia;
  let last = lastMedia;
  if (previousVideoSlotID && mayRefreshAutomaticFrame(first, automaticPreviousFrameFilename)) {
    const [target, source] = await Promise.all([
      getMediaSlot(projectID, firstFrameSlotID),
      getMediaSlot(projectID, previousVideoSlotID),
    ]);
    first = source.slot.current_media_version_id
      ? await extractVideoFrame(
          projectID,
          target.slot.media_slot_id,
          source.slot.media_slot_id,
          target.slot.revision,
        )
      : target;
  }
  if (
    generationMode === 'first_last_image' &&
    nextFirstFrameMedia?.slot.current_media_version_id &&
    mayRefreshAutomaticFrame(last, automaticNextFrameFilename)
  ) {
    last = nextFirstFrameMedia;
  } else if (
    generationMode === 'first_last_image' &&
    nextVideoSlotID &&
    mayRefreshAutomaticFrame(last, automaticNextFrameFilename)
  ) {
    const [target, source] = await Promise.all([
      getMediaSlot(projectID, lastFrameSlotID),
      getMediaSlot(projectID, nextVideoSlotID),
    ]);
    last = source.slot.current_media_version_id
      ? await extractVideoFrame(
          projectID,
          target.slot.media_slot_id,
          source.slot.media_slot_id,
          target.slot.revision,
        )
      : target;
  }
  return { first, last };
}
