import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { artifactContentURL, type Artifact } from '../api/assets';
import { Dialog } from './Dialog';
import type { MediaTrim } from '../mediaTrim';
import { TrimmedMediaPlayer } from './TrimmedMediaPlayer';
import '../styles/reference-media-preview.css';

export interface ReferencePreviewItem extends MediaTrim {
  key: string;
  name: string;
  artifact: Artifact;
  mappingLabel: string;
  purpose: string;
  source: string;
}

export default function ReferenceMediaPreviewDialog({
  projectID,
  items,
  index,
  onIndexChange,
  onClose,
}: {
  projectID: string;
  items: ReferencePreviewItem[];
  index: number | undefined;
  onIndexChange: (index: number) => void;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const copy = i18n.resolvedLanguage?.startsWith('zh')
    ? {
        previous: '上一个参考素材',
        next: '下一个参考素材',
        info: '素材信息',
        mapping: '模型映射',
        purpose: '用途',
        source: '来源',
        readOnly: '预览不会更改素材绑定',
      }
    : {
        previous: 'Previous reference',
        next: 'Next reference',
        info: 'Media information',
        mapping: 'Model mapping',
        purpose: 'Purpose',
        source: 'Source',
        readOnly: 'Previewing does not change media bindings',
      };
  const item = index === undefined ? undefined : items[index];
  const source = item ? artifactContentURL(projectID, item.artifact.artifact_id) : '';
  const move = (offset: number) => {
    if (index === undefined || items.length < 2) return;
    onIndexChange((index + offset + items.length) % items.length);
  };

  useEffect(() => {
    if (!item || index === undefined || items.length < 2) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'ArrowLeft') onIndexChange((index - 1 + items.length) % items.length);
      if (event.key === 'ArrowRight') onIndexChange((index + 1) % items.length);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [index, item, items.length, onIndexChange]);

  return (
    <Dialog
      open={Boolean(item)}
      label={
        item
          ? t(item.artifact.kind === 'image' ? 'common.previewImage' : 'common.playMedia', {
              name: item.name,
            })
          : ''
      }
      onClose={onClose}
    >
      <section className="dialog-panel reference-preview-dialog">
        <header className="dialog-header">
          <div>
            <p>{item?.mappingLabel}</p>
            <h2>{item?.name}</h2>
          </div>
          <div className="reference-preview-dialog__header-actions">
            {item?.artifact.kind === 'video' && (
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
        {item && (
          <div className="reference-preview-dialog__body">
            <div className={`reference-preview-dialog__stage is-${item.artifact.kind}`}>
              {item.artifact.kind === 'image' && <img src={source} alt={item.name} />}
              {item.artifact.kind === 'video' && (
                <TrimmedMediaPlayer
                  key={source}
                  src={source}
                  kind="video"
                  duration={item.artifact.duration_ms}
                  trim={item}
                  autoPlay
                  videoRef={video}
                />
              )}
              {item.artifact.kind === 'audio' && (
                <div className="reference-preview-dialog__audio">
                  <span aria-hidden="true">♫</span>
                  <strong>{item.name}</strong>
                  <TrimmedMediaPlayer
                    key={source}
                    src={source}
                    kind="audio"
                    duration={item.artifact.duration_ms}
                    trim={item}
                    autoPlay
                  />
                </div>
              )}
              {items.length > 1 && (
                <>
                  <button
                    type="button"
                    className="reference-preview-dialog__previous"
                    aria-label={copy.previous}
                    onClick={() => move(-1)}
                  >
                    ‹
                  </button>
                  <button
                    type="button"
                    className="reference-preview-dialog__next"
                    aria-label={copy.next}
                    onClick={() => move(1)}
                  >
                    ›
                  </button>
                </>
              )}
              <span className="reference-preview-dialog__position">
                {(index ?? 0) + 1} / {items.length}
              </span>
            </div>
            <aside>
              <h3>{copy.info}</h3>
              <dl>
                <div>
                  <dt>{copy.mapping}</dt>
                  <dd>{item.mappingLabel}</dd>
                </div>
                <div>
                  <dt>{copy.purpose}</dt>
                  <dd>{item.purpose}</dd>
                </div>
                <div>
                  <dt>{copy.source}</dt>
                  <dd>{item.source}</dd>
                </div>
              </dl>
              <p>
                <span aria-hidden="true">i</span>
                {copy.readOnly}
              </p>
            </aside>
          </div>
        )}
      </section>
    </Dialog>
  );
}
