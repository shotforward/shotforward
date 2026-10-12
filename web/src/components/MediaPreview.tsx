import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { artifactContentURL, type Artifact } from '../api/assets';
import { Dialog } from './Dialog';
import { mediaTrimRange, type MediaTrim } from '../mediaTrim';
import { TrimmedMediaPlayer } from './TrimmedMediaPlayer';
import '../styles/media-preview.css';

export interface MediaPreviewValue extends MediaTrim {
  artifact: Artifact;
  title: string;
}

export function MediaPreviewDialog({
  projectID,
  value,
  onClose,
}: {
  projectID: string;
  value: MediaPreviewValue | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const source = value ? artifactContentURL(projectID, value.artifact.artifact_id) : '';
  const dialogLabel = value
    ? t(value.artifact.kind === 'image' ? 'common.previewImage' : 'common.playMedia', { name: value.title })
    : '';
  return (
    <Dialog open={Boolean(value)} label={dialogLabel} onClose={onClose}>
      <section
        className={`dialog-panel media-preview-dialog media-preview-dialog--${value?.artifact.kind ?? 'image'}`}
      >
        <header className="dialog-header">
          <div>
            <p>
              {value?.artifact.kind === 'image'
                ? t('common.imagePreview')
                : value?.artifact.kind === 'video'
                  ? t('common.videoPreview')
                  : t('common.audioPreview')}
            </p>
            <h2>{value?.title}</h2>
          </div>
          <div className="media-preview-dialog__actions">
            {value?.artifact.kind === 'video' && (
              <button
                type="button"
                className="icon-button"
                aria-label={t('common.fullscreenVideo')}
                title={t('common.fullscreenVideo')}
                onClick={() => void video.current?.requestFullscreen?.()}
              >
                ⛶
              </button>
            )}
            <button type="button" className="icon-button" aria-label={t('common.close')} onClick={onClose}>
              ×
            </button>
          </div>
        </header>
        <div
          className={`media-preview-dialog__stage media-preview-dialog__stage--${value?.artifact.kind ?? 'image'}`}
        >
          {value?.artifact.kind === 'image' && <img src={source} alt={value.title} />}
          {value?.artifact.kind === 'video' && (
            <TrimmedMediaPlayer
              key={source}
              src={source}
              kind="video"
              duration={value.artifact.duration_ms}
              trim={value}
              autoPlay
              videoRef={video}
            />
          )}
          {value?.artifact.kind === 'audio' && (
            <div className="media-preview-dialog__audio">
              <span aria-hidden="true">♫</span>
              <strong>{value.title}</strong>
              <TrimmedMediaPlayer
                key={source}
                src={source}
                kind="audio"
                duration={value.artifact.duration_ms}
                trim={value}
                autoPlay
              />
            </div>
          )}
        </div>
      </section>
    </Dialog>
  );
}

export function MediaThumbnailButton({
  projectID,
  artifact,
  title,
  onPreview,
  className = '',
  trim,
}: {
  projectID: string;
  artifact: Artifact;
  title: string;
  onPreview: () => void;
  className?: string;
  trim?: MediaTrim;
}) {
  const { t } = useTranslation();
  const source = artifactContentURL(projectID, artifact.artifact_id);
  const range = mediaTrimRange(artifact, trim);
  const video = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    if (video.current && video.current.readyState >= 1) video.current.currentTime = range.start / 1000;
  }, [source, range.start]);
  return (
    <button
      type="button"
      className={`media-thumbnail ${className}`.trim()}
      aria-label={t(artifact.kind === 'image' ? 'common.previewImage' : 'common.playMedia', { name: title })}
      onClick={(event) => {
        event.stopPropagation();
        onPreview();
      }}
    >
      {artifact.kind === 'image' && <img src={source} alt="" loading="lazy" />}
      {artifact.kind === 'video' && (
        <video
          ref={video}
          src={source}
          muted
          preload="metadata"
          onLoadedMetadata={(event) => {
            event.currentTarget.currentTime = range.start / 1000;
          }}
        />
      )}
      {artifact.kind === 'audio' && (
        <span className="media-thumbnail__wave" aria-hidden="true">
          ▶ ︱▂▅▃▇▆▂︱
        </span>
      )}
      {artifact.kind !== 'image' && (
        <span className="media-thumbnail__play" aria-hidden="true">
          ▶
        </span>
      )}
      {artifact.kind !== 'image' && <small>{formatMediaDuration(range.duration)}</small>}
    </button>
  );
}

export function formatMediaDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
