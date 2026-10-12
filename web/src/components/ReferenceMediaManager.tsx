import { useTranslation } from 'react-i18next';
import type { Artifact } from '../api/assets';
import type { TaskItem } from '../api/tasks';
import { MediaThumbnailButton } from './MediaPreview';
import type { MediaTrim } from '../mediaTrim';

export interface ReferenceMediaItem extends MediaTrim {
  key: string;
  name: string;
  artifact: Artifact | undefined;
  source: string;
  mappingLabel: string;
  purpose: string;
  generationTask?: TaskItem;
  onOpen?: () => void;
  onRemove?: () => void;
}

export default function ReferenceMediaManager({
  projectID,
  title,
  emptyLabel,
  items,
  uploading,
  automaticImages,
  uploads,
  onUpload,
  onPreview,
}: {
  projectID: string;
  title: string;
  emptyLabel: string;
  items: ReferenceMediaItem[];
  uploading: boolean;
  automaticImages: number;
  uploads: Artifact[];
  onUpload?: (file: File) => void;
  onPreview: (key: string) => void;
}) {
  const { t } = useTranslation();
  const images = automaticImages + uploads.filter((item) => item.kind === 'image').length;
  const audio = uploads.filter((item) => item.kind === 'audio').length;
  const video = uploads.filter((item) => item.kind === 'video').length;
  return (
    <div className="clip-focus-reference__manager">
      <section className="reference-media-group">
        <header>
          <strong>{title}</strong>
          <span>{items.length}</span>
        </header>
        {items.length === 0 && <p className="info-note">{emptyLabel}</p>}
        <div className="reference-media-grid">
          {items.map((item) => (
            <article className={`reference-media-card${item.onOpen ? ' is-asset' : ''}`} key={item.key}>
              {item.artifact ? (
                <MediaThumbnailButton
                  projectID={projectID}
                  artifact={item.artifact}
                  trim={item}
                  title={item.name}
                  className="reference-media-preview"
                  onPreview={() => onPreview(item.key)}
                />
              ) : (
                <div className="reference-media-preview">◇</div>
              )}
              <div>
                <strong title={item.name}>{item.name}</strong>
                <small>
                  {item.artifact
                    ? t(`storyboard.mediaKinds.${item.artifact.kind}`)
                    : t('storyboard.referenceMissing')}
                </small>
                <span>{item.source}</span>
              </div>
              {item.generationTask &&
                ['queued', 'running', 'stop_requested'].includes(item.generationTask.state) && (
                  <span className="reference-media-card__generation-state" role="status">
                    <span className="spinner" aria-hidden="true" />
                    <small>
                      {t(
                        item.generationTask.state === 'running'
                          ? 'assets.imageRunning'
                          : item.generationTask.state === 'stop_requested'
                            ? 'assets.imageStopping'
                            : 'assets.imageQueued',
                      )}
                    </small>
                  </span>
                )}
              {item.onOpen && (
                <button
                  type="button"
                  className="reference-media-card__open"
                  aria-label={`${t('storyboard.editDetails')} ${item.name}`}
                  onClick={item.onOpen}
                />
              )}
              {item.onRemove && (
                <button
                  type="button"
                  className="reference-media-card__remove"
                  aria-label={t('storyboard.removeUpload')}
                  onClick={item.onRemove}
                >
                  ×
                </button>
              )}
            </article>
          ))}
          <label className={`reference-media-upload-card${uploading ? ' is-disabled' : ''}`}>
            <span aria-hidden="true">＋</span>
            <strong>{t('storyboard.uploadReference')}</strong>
            <small>{t('storyboard.uploadReferenceFormats')}</small>
            <input
              type="file"
              accept="image/jpeg,image/png,image/webp,audio/mpeg,audio/wav,audio/mp4,video/mp4,video/quicktime,video/webm"
              disabled={uploading || !onUpload}
              aria-label={t('storyboard.uploadReference')}
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = '';
                if (file) onUpload?.(file);
              }}
            />
          </label>
        </div>
      </section>
      <p className="reference-media-summary">
        {t('storyboard.referenceSummary', { total: images + audio + video, images, audio, video })}
      </p>
    </div>
  );
}
