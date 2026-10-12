import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  type EpisodeAsset,
  type EpisodeReferenceMedia,
  type MediaSlotDetails,
} from '../api/assets';
import { Dialog } from './Dialog';

function currentArtifact(media: MediaSlotDetails | undefined) {
  const currentID = media?.slot.current_media_version_id;
  const version = media?.versions.find((item) => item.media_version_id === currentID);
  return media?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
}

export default function ReferenceLibraryDialog({
  projectID,
  clipTitle,
  target,
  prompt,
  uploads,
  assets,
  onToggle,
  onClose,
}: {
  projectID: string;
  clipTitle: string;
  target: 'first_image' | 'last_image' | 'video';
  prompt: string;
  uploads: EpisodeReferenceMedia[];
  assets: Array<{ asset: EpisodeAsset; media: MediaSlotDetails | undefined }>;
  onToggle: (name: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const availableUploads = uploads.filter((item) => target === 'video' || item.artifact.kind === 'image');
  return (
    <Dialog open label={t('storyboard.chooseFromLibrary')} onClose={onClose}>
      <section className="dialog-panel reference-library-dialog">
        <header className="dialog-header">
          <div>
            <p>{clipTitle}</p>
            <h2>{t('storyboard.chooseFromLibrary')}</h2>
          </div>
          <button type="button" className="icon-button" aria-label={t('common.close')} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="reference-library-dialog__body">
          {availableUploads.length > 0 && (
            <section>
              <header>
                <strong>{t('assets.uploadedMedia')}</strong>
                <span>{availableUploads.length}</span>
              </header>
              <div>
                {availableUploads.map((item) => {
                  const selected = prompt.includes(`@${item.name}`);
                  return (
                    <button
                      key={item.artifact.artifact_id}
                      type="button"
                      className={selected ? 'active' : ''}
                      onClick={() => onToggle(item.name)}
                    >
                      <span>
                        {item.artifact.kind === 'image' ? (
                          <img src={artifactContentURL(projectID, item.artifact.artifact_id)} alt="" />
                        ) : item.artifact.kind === 'video' ? (
                          '▶'
                        ) : (
                          '♫'
                        )}
                      </span>
                      <strong>{item.name}</strong>
                      <small>{t(`storyboard.mediaKinds.${item.artifact.kind}`)}</small>
                      <i>{selected ? '✓' : '+'}</i>
                    </button>
                  );
                })}
              </div>
            </section>
          )}
          <section>
            <header>
              <strong>{t('assets.generatedMedia')}</strong>
              <span>{assets.length}</span>
            </header>
            <div>
              {assets.map(({ asset, media }) => {
                const artifact = currentArtifact(media);
                const selected = prompt.includes(`@${asset.identity.name}`);
                return (
                  <button
                    key={asset.asset_id}
                    type="button"
                    className={selected ? 'active' : ''}
                    onClick={() => onToggle(asset.identity.name)}
                  >
                    <span>
                      {artifact ? (
                        <img src={artifactContentURL(projectID, artifact.artifact_id)} alt="" />
                      ) : (
                        '◇'
                      )}
                    </span>
                    <strong>{asset.identity.name}</strong>
                    <small>{t(`assets.kinds.${asset.identity.kind}`)}</small>
                    <i>{selected ? '✓' : '+'}</i>
                  </button>
                );
              })}
            </div>
          </section>
        </div>
        <footer className="dialog-footer">
          <button type="button" className="button" onClick={onClose}>
            {t('storyboard.finishChoosing')}
          </button>
        </footer>
      </section>
    </Dialog>
  );
}
