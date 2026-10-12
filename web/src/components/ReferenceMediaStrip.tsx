import { type Artifact } from '../api/assets';
import { MediaThumbnailButton } from './MediaPreview';
import type { MediaTrim } from '../mediaTrim';

export interface ReferenceStripItem extends MediaTrim {
  key: string;
  name: string;
  artifact: Artifact | undefined;
  mappingLabel: string;
  onOpen?: () => void;
}

export default function ReferenceMediaStrip({
  projectID,
  items,
  overflowLabel,
  onPreview,
  onManage,
}: {
  projectID: string;
  items: ReferenceStripItem[];
  overflowLabel: string;
  onPreview: (key: string) => void;
  onManage: () => void;
}) {
  const visible = items.slice(0, 4);
  return (
    <div className="reference-media-strip">
      {visible.map((item) => (
        <article className="reference-media-strip__item" key={item.key}>
          {item.artifact ? (
            <MediaThumbnailButton
              projectID={projectID}
              artifact={item.artifact}
              trim={item}
              title={item.name}
              onPreview={() => onPreview(item.key)}
            />
          ) : (
            <button
              type="button"
              className="reference-media-strip__missing"
              aria-label={item.name}
              onClick={item.onOpen}
            >
              ◇
            </button>
          )}
          <strong title={item.name}>{item.name}</strong>
          <small>{item.mappingLabel}</small>
        </article>
      ))}
      {items.length > visible.length && (
        <button type="button" className="reference-media-strip__overflow" onClick={onManage}>
          <strong>+{items.length - visible.length}</strong>
          <small>{overflowLabel}</small>
        </button>
      )}
    </div>
  );
}
