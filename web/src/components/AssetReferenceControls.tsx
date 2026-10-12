import { useTranslation } from 'react-i18next';
import { artifactContentURL } from '../api/assets';
import { imageReferenceLimitMessage, type AssetReferenceSelection } from '../assetImageReferences';

export function AssetReferenceControls({
  projectID,
  episodeID,
  values,
  maximum,
  disabled,
  onChange,
  onOpen,
}: {
  projectID: string;
  episodeID: string;
  values: AssetReferenceSelection[];
  maximum: number;
  disabled: boolean;
  onChange: (values: AssetReferenceSelection[]) => void;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      {values.map((value) => (
        <div className="asset-reference-chip" key={value.artifact.artifact_id}>
          <img
            src={artifactContentURL(projectID, value.artifact.artifact_id)}
            alt={t('assets.referenceImage')}
          />
          <span>
            {value.name}
            {value.episodeID !== episodeID && value.episodeTitle ? ` · ${value.episodeTitle}` : ''}
          </span>
          <button
            type="button"
            aria-label={`${t('assets.removeReference')}: ${value.name}`}
            onClick={() =>
              onChange(values.filter((item) => item.artifact.artifact_id !== value.artifact.artifact_id))
            }
            disabled={disabled}
          >
            ×
          </button>
        </div>
      ))}
      <button type="button" className="asset-add-reference" onClick={onOpen} disabled={disabled}>
        ＋ {t('assets.addReference')} ({values.length}/{maximum})
      </button>
    </>
  );
}

export function AssetReferenceLimitWarning({ count, maximum }: { count: number; maximum: number }) {
  const { i18n } = useTranslation();
  return count > maximum ? (
    <p className="form-error" role="alert">
      {imageReferenceLimitMessage(i18n.resolvedLanguage, maximum)}
    </p>
  ) : null;
}
