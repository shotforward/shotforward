import { useEffect, useState, type CSSProperties, type SyntheticEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Dialog } from './Dialog';
import '../styles/image-preview.css';

interface ImagePreviewButtonProps {
  src: string;
  alt: string;
  onPreview: () => void;
  className?: string;
  imageClassName?: string;
  loading?: 'eager' | 'lazy';
  hintLabel?: string;
}

export function ImagePreviewButton({
  src,
  alt,
  onPreview,
  className = '',
  imageClassName,
  loading,
  hintLabel,
}: ImagePreviewButtonProps) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      className={`image-preview-trigger ${className}`.trim()}
      aria-label={t('common.previewImage', { name: alt || t('common.imagePreview') })}
      title={t('common.previewImage', { name: alt || t('common.imagePreview') })}
      onClick={(event) => {
        event.stopPropagation();
        onPreview();
      }}
    >
      <img src={src} alt={alt} className={imageClassName} loading={loading} />
      <span className={`image-preview-trigger__hint${hintLabel ? ' has-label' : ''}`} aria-hidden="true">
        {hintLabel || (
          <svg viewBox="0 0 24 24" focusable="false">
            <circle cx="10.5" cy="10.5" r="5.75" />
            <path d="m15 15 4 4M10.5 8v5M8 10.5h5" />
          </svg>
        )}
      </span>
    </button>
  );
}

export interface ImagePreviewValue {
  src: string;
  title: string;
  width?: number;
  height?: number;
}

interface ImageDimensions {
  width: number;
  height: number;
}

function validDimensions(width: number | undefined, height: number | undefined): ImageDimensions | undefined {
  if (!width || !height || width < 1 || height < 1) return undefined;
  return { width, height };
}

function greatestCommonDivisor(left: number, right: number): number {
  while (right) [left, right] = [right, left % right];
  return left;
}

function ratioLabel({ width, height }: ImageDimensions): string {
  const divisor = greatestCommonDivisor(width, height);
  const ratioWidth = width / divisor;
  const ratioHeight = height / divisor;
  return ratioWidth <= 30 && ratioHeight <= 30 ? `${ratioWidth}:${ratioHeight}` : '';
}

export function ImageLightbox({
  value,
  onClose,
}: {
  value: ImagePreviewValue | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const title = value?.title || t('common.imagePreview');
  const [dimensions, setDimensions] = useState<ImageDimensions | undefined>(() =>
    validDimensions(value?.width, value?.height),
  );

  useEffect(() => {
    setDimensions(validDimensions(value?.width, value?.height));
  }, [value?.src, value?.width, value?.height]);

  function readDimensions(event: SyntheticEvent<HTMLImageElement>) {
    const image = event.currentTarget;
    setDimensions(validDimensions(image.naturalWidth, image.naturalHeight));
  }

  const stageStyle = dimensions
    ? ({
        '--image-native-width': `${dimensions.width}px`,
        '--image-aspect-ratio': dimensions.width / dimensions.height,
      } as CSSProperties)
    : undefined;
  const ratio = dimensions ? ratioLabel(dimensions) : '';
  return (
    <Dialog open={Boolean(value)} label={t('common.previewImage', { name: title })} onClose={onClose}>
      <section className="image-lightbox">
        <header>
          <div className="image-lightbox__title">
            <strong>{title}</strong>
            {dimensions && (
              <small>
                {dimensions.width} × {dimensions.height}
                {ratio && ` · ${ratio}`}
              </small>
            )}
          </div>
          <button type="button" aria-label={t('common.close')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className={`image-lightbox__stage${dimensions ? ' is-measured' : ''}`} style={stageStyle}>
          {value && <img src={value.src} alt={value.title} decoding="async" onLoad={readDimensions} />}
        </div>
      </section>
    </Dialog>
  );
}
