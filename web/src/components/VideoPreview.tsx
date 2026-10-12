import { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Dialog } from './Dialog';
import '../styles/video-preview.css';

export interface VideoPreviewValue {
  src: string;
  title: string;
}

export function VideoExpandButton({ onClick, className = '' }: { onClick: () => void; className?: string }) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      className={`video-expand-trigger ${className}`.trim()}
      aria-label={t('common.enlargeVideo')}
      title={t('common.enlargeVideo')}
      onClick={(event) => {
        event.stopPropagation();
        onClick();
      }}
    >
      <span aria-hidden="true">⛶</span>
    </button>
  );
}

export function VideoLightbox({
  value,
  onClose,
}: {
  value: VideoPreviewValue | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const title = value?.title || t('common.videoPreview');

  return (
    <Dialog open={Boolean(value)} label={t('common.playMedia', { name: title })} onClose={onClose}>
      <section className="video-lightbox">
        <header>
          <div>
            <small>{t('common.videoPreview')}</small>
            <strong>{title}</strong>
          </div>
          <div className="video-lightbox__actions">
            <button
              type="button"
              aria-label={t('common.fullscreenVideo')}
              title={t('common.fullscreenVideo')}
              onClick={() => void video.current?.requestFullscreen?.()}
            >
              ⛶
            </button>
            <button type="button" aria-label={t('common.close')} onClick={onClose}>
              ×
            </button>
          </div>
        </header>
        <div className="video-lightbox__stage">
          {value && (
            <video ref={video} key={value.src} src={value.src} controls autoPlay playsInline preload="auto" />
          )}
        </div>
      </section>
    </Dialog>
  );
}
