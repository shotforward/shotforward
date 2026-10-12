import { fireEvent, render, screen } from '@testing-library/react';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { VideoExpandButton, VideoLightbox, type VideoPreviewValue } from './VideoPreview';

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

describe('video preview', () => {
  it('opens from an accessible expand button and supports native fullscreen', () => {
    const onPreview = vi.fn();
    const onClose = vi.fn();
    const value: VideoPreviewValue = { src: '/movie.mp4', title: '测试视频' };
    render(
      <>
        <VideoExpandButton onClick={onPreview} />
        <VideoLightbox value={value} onClose={onClose} />
      </>,
    );

    fireEvent.click(screen.getByRole('button', { name: /放大播放视频|Enlarge video/i }));
    expect(onPreview).toHaveBeenCalledOnce();
    expect(screen.getByRole('dialog', { name: /测试视频/ })).toBeInTheDocument();
    const video = document.querySelector<HTMLVideoElement>('.video-lightbox video')!;
    expect(video).toHaveAttribute('src', '/movie.mp4');
    const requestFullscreen = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(video, 'requestFullscreen', { configurable: true, value: requestFullscreen });
    fireEvent.click(screen.getByRole('button', { name: /全屏播放视频|Play video fullscreen/i }));
    expect(requestFullscreen).toHaveBeenCalledOnce();
  });
});
