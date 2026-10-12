import { fireEvent, render, screen } from '@testing-library/react';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { ImageLightbox, ImagePreviewButton, type ImagePreviewValue } from './ImagePreview';

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
});

describe('image preview', () => {
  it('opens from an accessible image button and closes through the native Dialog cancel action', () => {
    const onPreview = vi.fn();
    const onClose = vi.fn();
    const value: ImagePreviewValue = {
      src: '/preview.png',
      title: '测试图片',
      width: 1280,
      height: 720,
    };
    const { rerender } = render(
      <>
        <ImagePreviewButton src={value.src} alt={value.title} onPreview={onPreview} />
        <ImageLightbox value={undefined} onClose={onClose} />
      </>,
    );

    fireEvent.click(screen.getByRole('button', { name: /放大预览测试图片|Preview 测试图片/ }));
    expect(onPreview).toHaveBeenCalledTimes(1);

    rerender(
      <>
        <ImagePreviewButton src={value.src} alt={value.title} onPreview={onPreview} />
        <ImageLightbox value={value} onClose={onClose} />
      </>,
    );
    const dialog = screen.getByRole('dialog', { name: /放大预览测试图片|Preview 测试图片/ });
    expect(dialog).toHaveAttribute('open');
    expect(screen.getByText('1280 × 720 · 16:9')).toBeInTheDocument();
    expect(dialog.querySelector('.image-lightbox__stage')).toHaveStyle({
      '--image-native-width': '1280px',
      '--image-aspect-ratio': '1.7777777777777777',
    });

    fireEvent(dialog, new Event('cancel', { cancelable: true }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
