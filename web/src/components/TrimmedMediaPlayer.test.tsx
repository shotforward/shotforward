import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { TrimmedMediaPlayer } from './TrimmedMediaPlayer';
import { MediaPreviewDialog, MediaThumbnailButton } from './MediaPreview';
import ReferenceMediaPreviewDialog from './ReferenceMediaPreviewDialog';
import type { Artifact } from '../api/assets';

const artifact = {
  artifact_id: 'audio_1',
  kind: 'audio',
  duration_ms: 15_000,
  original_filename: 'voice.wav',
} as Artifact;

function playback(media: HTMLMediaElement) {
  let paused = true;
  Object.defineProperty(media, 'paused', { configurable: true, get: () => paused });
  const play = vi.spyOn(media, 'play').mockImplementation(async () => {
    paused = false;
    media.dispatchEvent(new Event('play'));
  });
  const pause = vi.spyOn(media, 'pause').mockImplementation(() => {
    paused = true;
    media.dispatchEvent(new Event('pause'));
  });
  return { play, pause };
}

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute('open');
  };
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('selection-relative audio and video playback', () => {
  it.each(['audio', 'video'] as const)(
    'bounds %s playback, seeks, and restarts without exposing native full-file controls',
    async (kind) => {
      const { container } = render(
        <TrimmedMediaPlayer
          src="/original"
          kind={kind}
          duration={15_000}
          trim={{ trim_start_ms: 3_000, trim_end_ms: 11_000 }}
        />,
      );
      const media = container.querySelector(kind)!;
      const { play, pause } = playback(media);
      fireEvent.loadedMetadata(media);
      expect(media.currentTime).toBe(3);
      expect(media).not.toHaveAttribute('controls');
      expect(media).not.toHaveAttribute('autoplay');
      const seek = screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ });
      expect(seek).toHaveAttribute('max', '8000');
      expect(screen.getByText('0:00 / 0:08')).toBeInTheDocument();
      fireEvent.change(seek, { target: { value: '2500' } });
      expect(media.currentTime).toBe(5.5);
      expect(screen.getByText('0:02.5 / 0:08')).toBeInTheDocument();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /Play selection|播放选区/ }));
      });
      expect(play).toHaveBeenCalledOnce();
      expect(screen.getByRole('button', { name: /Pause selection|暂停选区/ })).toBeInTheDocument();
      media.currentTime = 11.2;
      fireEvent.timeUpdate(media);
      expect(pause).toHaveBeenCalledOnce();
      expect(media.currentTime).toBe(11);
      expect(screen.getByText('0:08 / 0:08')).toBeInTheDocument();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /Play selection|播放选区/ }));
      });
      expect(media.currentTime).toBe(3);
      media.currentTime = 1;
      fireEvent.seeking(media);
      expect(media.currentTime).toBe(3);
      media.currentTime = 14;
      fireEvent.seeking(media);
      expect(media.currentTime).toBe(11);
      fireEvent.click(screen.getByRole('button', { name: /Mute selection|^静音$/ }));
      expect(media.muted).toBe(true);
    },
  );

  it('resets to the latest selection and clears all frame/timer watchers on pause, seek and unmount', async () => {
    vi.useFakeTimers();
    const { container, rerender, unmount } = render(
      <TrimmedMediaPlayer
        src="/original"
        kind="audio"
        duration={15_000}
        trim={{ trim_start_ms: 3_000, trim_end_ms: 11_000 }}
      />,
    );
    const media = container.querySelector('audio')!;
    playback(media);
    Object.defineProperty(media, 'readyState', { configurable: true, value: 1 });
    fireEvent.loadedMetadata(media);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Play selection|播放选区/ }));
    });
    expect(vi.getTimerCount()).toBe(2);
    fireEvent.change(screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ }), {
      target: { value: '7000' },
    });
    expect(vi.getTimerCount()).toBe(2);
    media.playbackRate = 2;
    fireEvent.rateChange(media);
    expect(vi.getTimerCount()).toBe(2);
    // Stalled media never stops early just because wall-clock time elapsed.
    act(() => vi.advanceTimersByTime(600));
    expect(media.paused).toBe(false);
    expect(vi.getTimerCount()).toBe(1);
    fireEvent.click(screen.getByRole('button', { name: /Pause selection|暂停选区/ }));
    expect(vi.getTimerCount()).toBe(0);
    rerender(
      <TrimmedMediaPlayer
        src="/original"
        kind="audio"
        duration={15_000}
        trim={{ trim_start_ms: 5_000, trim_end_ms: 9_000 }}
      />,
    );
    expect(media.currentTime).toBe(5);
    expect(screen.getByText('0:00 / 0:04')).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Play selection|播放选区/ }));
    });
    expect(vi.getTimerCount()).toBe(2);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('stops at the selection end even without a timeupdate event', async () => {
    vi.useFakeTimers();
    const { container } = render(
      <TrimmedMediaPlayer
        src="/original"
        kind="audio"
        duration={15_000}
        trim={{ trim_start_ms: 3_000, trim_end_ms: 11_000 }}
      />,
    );
    const media = container.querySelector('audio')!;
    const { pause } = playback(media);
    fireEvent.loadedMetadata(media);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Play selection|播放选区/ }));
    });
    media.currentTime = 11;
    act(() => vi.advanceTimersByTime(20));
    expect(pause).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('uses the same selection in library preview, reference preview, and video thumbnail', () => {
    const trim = { trim_start_ms: 3_000, trim_end_ms: 11_000 };
    const view = render(
      <MediaPreviewDialog
        projectID="project"
        value={{ artifact, title: 'voice', ...trim }}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ })).toHaveAttribute(
      'max',
      '8000',
    );
    view.unmount();
    const reference = render(
      <ReferenceMediaPreviewDialog
        projectID="project"
        items={[
          {
            key: 'upload:audio_1',
            name: 'voice',
            artifact,
            mappingLabel: 'Audio 1',
            purpose: 'voice',
            source: 'uploaded',
            ...trim,
          },
        ]}
        index={0}
        onIndexChange={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByRole('slider', { name: /Selection playback position|选区播放位置/ })).toHaveAttribute(
      'max',
      '8000',
    );
    reference.unmount();
    const thumbnail = render(
      <MediaThumbnailButton
        projectID="project"
        artifact={{ ...artifact, kind: 'video' }}
        trim={trim}
        title="video"
        onPreview={vi.fn()}
      />,
    );
    expect(screen.getByText('0:08')).toBeInTheDocument();
    const video = thumbnail.container.querySelector('video')!;
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBe(3);
    Object.defineProperty(video, 'readyState', { configurable: true, value: 1 });
    thumbnail.rerender(
      <MediaThumbnailButton
        projectID="project"
        artifact={{ ...artifact, kind: 'video' }}
        trim={{ trim_start_ms: 5000, trim_end_ms: 9000 }}
        title="video"
        onPreview={vi.fn()}
      />,
    );
    expect(video.currentTime).toBe(5);
    expect(screen.getByText('0:04')).toBeInTheDocument();
  });
});
