import { useTranslation } from 'react-i18next';
import { artifactContentURL, type EpisodeAsset, type MediaSlotDetails } from '../api/assets';
import { Dialog } from './Dialog';
import { ImageLightbox } from './ImagePreview';

function currentArtifact(media: MediaSlotDetails | undefined) {
  const currentID = media?.slot.current_media_version_id;
  const version = media?.versions.find((item) => item.media_version_id === currentID);
  return media?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
}

export default function AssetPreviewDialog({
  projectID,
  reference,
  onClose,
}: {
  projectID: string;
  reference: { asset: EpisodeAsset; media: MediaSlotDetails | undefined };
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const artifact = currentArtifact(reference.media);
  if (artifact)
    return (
      <ImageLightbox
        value={{
          src: artifactContentURL(projectID, artifact.artifact_id),
          title: reference.asset.identity.name,
          width: artifact.width,
          height: artifact.height,
        }}
        onClose={onClose}
      />
    );
  return (
    <Dialog open label={reference.asset.identity.name} onClose={onClose}>
      <section className="dialog-panel storyboard-asset-preview">
        <header className="dialog-header">
          <div>
            <p>{t('storyboard.assetPreview')}</p>
            <h2>{reference.asset.identity.name}</h2>
          </div>
          <button className="icon-button" type="button" aria-label={t('common.close')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="storyboard-asset-preview__media">
          <div>
            <span>◇</span>
            <p>{t('storyboard.referenceMissing')}</p>
          </div>
        </div>
      </section>
    </Dialog>
  );
}
