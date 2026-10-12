import { useId, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  getMediaSlot,
  getProjectAssetVariants,
  type AssetKind,
  type ProjectAssetVariant,
} from '../api/assets';
import { ProjectManagementHeader } from '../components/ProjectManagementHeader';
import { ImageLightbox, ImagePreviewButton, type ImagePreviewValue } from '../components/ImagePreview';
import { Select } from '../components/Select';
import '../styles/cross-episode-assets.css';

interface IdentityGroup {
  id: string;
  name: string;
  kind: AssetKind;
  variants: ProjectAssetVariant[];
}

export function CrossEpisodeAssetsPage() {
  const { projectId = '' } = useParams();
  const { t } = useTranslation();
  const index = useQuery({
    queryKey: ['project-assets', projectId],
    queryFn: () => getProjectAssetVariants(projectId),
    enabled: Boolean(projectId),
    staleTime: 30_000,
  });
  const [kind, setKind] = useState<AssetKind | 'all'>('all');
  const [search, setSearch] = useState('');
  const [episodeFilter, setEpisodeFilter] = useState('all');
  const [continuityFilter, setContinuityFilter] = useState<'all' | 'complete' | 'attention'>('all');
  const [selectedID, setSelectedID] = useState('');
  const [previewImage, setPreviewImage] = useState<ImagePreviewValue>();
  const groups = useMemo(() => {
    const map = new Map<string, IdentityGroup>();
    index.data?.variants.forEach((variant) => {
      const identity = variant.asset.identity;
      const group = map.get(identity.asset_identity_id) ?? {
        id: identity.asset_identity_id,
        name: identity.name,
        kind: identity.kind,
        variants: [],
      };
      group.variants.push(variant);
      map.set(group.id, group);
    });
    return [...map.values()].sort(
      (left, right) => left.kind.localeCompare(right.kind) || left.name.localeCompare(right.name),
    );
  }, [index.data]);
  const visible = groups
    .filter((group) => kind === 'all' || group.kind === kind)
    .filter((group) => group.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
    .filter(
      (group) =>
        episodeFilter === 'all' ||
        group.variants.some((variant) => variant.episode.episode_id === episodeFilter),
    )
    .filter((group) => {
      if (continuityFilter === 'complete') return group.variants.length === index.data?.episodes.length;
      if (continuityFilter === 'attention') return group.variants.length < (index.data?.episodes.length ?? 0);
      return true;
    });
  const selected = visible.find((group) => group.id === selectedID) ?? visible[0];
  if (index.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (index.isError)
    return (
      <div className="page message-state">
        <h2>{t('crossAssets.loadFailed')}</h2>
        <button className="button" onClick={() => void index.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );
  return (
    <div className="project-page">
      <ProjectManagementHeader
        projectID={projectId}
        projectName={index.data.project.name}
        activeSection="assets"
      />
      <main className="project-tool-content project-tool-content--wide">
        <header className="project-tool-heading">
          <div>
            <h1>{t('crossAssets.title')}</h1>
            <p>{t('crossAssets.description')}</p>
            <span className="project-tool-note">{t('crossAssets.assurance')}</span>
          </div>
        </header>
        <div className="cross-assets-toolbar">
          <nav aria-label={t('crossAssets.assetTypes')}>
            <button
              className={kind === 'all' ? 'active' : ''}
              aria-pressed={kind === 'all'}
              onClick={() => setKind('all')}
            >
              {t('assets.all')} <span>{groups.length}</span>
            </button>
            {(['character', 'location', 'prop', 'style'] as AssetKind[]).map((item) => (
              <button
                key={item}
                className={kind === item ? 'active' : ''}
                aria-pressed={kind === item}
                onClick={() => setKind(item)}
              >
                {t(`assets.kinds.${item}`)}{' '}
                <span>{groups.filter((group) => group.kind === item).length}</span>
              </button>
            ))}
          </nav>
          <div className="cross-assets-toolbar__filters">
            <label>
              <span aria-hidden="true">⌕</span>
              <input
                type="search"
                aria-label={t('crossAssets.search')}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('crossAssets.search')}
              />
            </label>
            <Select
              ariaLabel={t('crossAssets.allEpisodes')}
              value={episodeFilter}
              options={[
                { value: 'all', label: t('crossAssets.allEpisodes') },
                ...index.data.episodes.map((episode) => ({
                  value: episode.episode_id,
                  label: episode.title,
                })),
              ]}
              compact
              onChange={setEpisodeFilter}
            />
            <Select
              ariaLabel={t('crossAssets.allContinuity')}
              value={continuityFilter}
              options={[
                { value: 'all', label: t('crossAssets.allContinuity') },
                { value: 'complete', label: t('crossAssets.complete') },
                { value: 'attention', label: t('crossAssets.attention') },
              ]}
              compact
              onChange={setContinuityFilter}
            />
          </div>
        </div>
        <div className="cross-assets-layout">
          <aside className="identity-index">
            <header>
              <strong>{t('crossAssets.identities')}</strong>
              <span>{visible.length}</span>
            </header>
            <div>
              {visible.map((group) => (
                <div
                  className={`identity-index-item${group.id === selected?.id ? ' active' : ''}`}
                  key={group.id}
                >
                  {group.variants[0] && (
                    <VariantThumbnail
                      projectID={projectId}
                      variant={group.variants[0]}
                      fallback={group.name.slice(0, 1)}
                      title={group.name}
                      onPreview={setPreviewImage}
                    />
                  )}
                  <button
                    type="button"
                    className="identity-index-select"
                    aria-pressed={group.id === selected?.id}
                    onClick={() => setSelectedID(group.id)}
                  >
                    <span>
                      <strong>{group.name}</strong>
                      <small>{t('crossAssets.variants', { count: group.variants.length })}</small>
                      <em className={group.variants.length === index.data.episodes.length ? 'ready' : ''}>
                        <i />
                        {group.variants.length === index.data.episodes.length
                          ? t('crossAssets.complete')
                          : t('crossAssets.attention')}
                      </em>
                    </span>
                  </button>
                </div>
              ))}
            </div>
          </aside>
          {selected ? (
            <section className="continuity-view">
              <header>
                <div>
                  <span>{t(`assets.kinds.${selected.kind}`)}</span>
                  <h2>{selected.name}</h2>
                  <p>
                    {t('crossAssets.firstAppearance', {
                      count: Math.min(...selected.variants.map((variant) => variant.episode.sequence_number)),
                    })}
                  </p>
                </div>
                <div className="continuity-summary">
                  <strong>
                    {t('crossAssets.coverage', {
                      current: selected.variants.length,
                      total: index.data.episodes.length,
                    })}
                  </strong>
                  <span className={selected.variants.length === index.data.episodes.length ? 'ready' : ''}>
                    <i />
                    {selected.variants.length === index.data.episodes.length
                      ? t('crossAssets.allCovered')
                      : t('crossAssets.missingEpisodes', {
                          count: index.data.episodes.length - selected.variants.length,
                        })}
                  </span>
                  <small>{t('crossAssets.editAtSource')}</small>
                </div>
              </header>
              <div className="continuity-heading">
                <div>
                  <h3>{t('crossAssets.continuity')}</h3>
                  <p>{t('crossAssets.continuityHint')}</p>
                </div>
              </div>
              <div className="variant-list">
                {index.data.episodes.map((episode) => {
                  const variant = selected.variants.find(
                    (item) => item.episode.episode_id === episode.episode_id,
                  );
                  return variant ? (
                    <VariantRow
                      key={episode.episode_id}
                      projectID={projectId}
                      variant={variant}
                      onPreview={setPreviewImage}
                    />
                  ) : (
                    <div className="variant-row is-missing" key={episode.episode_id}>
                      <div className="variant-episode">
                        <span>{String(episode.sequence_number).padStart(2, '0')}</span>
                        <div>
                          <strong>{episode.title}</strong>
                          <small>{t('crossAssets.missing')}</small>
                        </div>
                      </div>
                      <div className="variant-missing-mark">—</div>
                    </div>
                  );
                })}
              </div>
            </section>
          ) : (
            <section className="continuity-view continuity-view--empty">
              <h2>{t('crossAssets.emptyTitle')}</h2>
              <p>{t('crossAssets.emptyHint')}</p>
            </section>
          )}
        </div>
      </main>
      <ImageLightbox value={previewImage} onClose={() => setPreviewImage(undefined)} />
    </div>
  );
}

function VariantRow({
  projectID,
  variant,
  onPreview,
}: {
  projectID: string;
  variant: ProjectAssetVariant;
  onPreview: (value: ImagePreviewValue) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="variant-row">
      <div className="variant-episode">
        <span>{String(variant.episode.sequence_number).padStart(2, '0')}</span>
        <div>
          <strong>{variant.episode.title}</strong>
          <small>
            {t('crossAssets.updated', {
              date: new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(
                new Date(variant.asset.updated_at),
              ),
            })}
          </small>
        </div>
      </div>
      <VariantThumbnail
        projectID={projectID}
        variant={variant}
        title={`${variant.asset.identity.name} · ${variant.episode.title}`}
        onPreview={onPreview}
      />
      <VariantPrompt prompt={variant.asset.prompt} />
      <VariantStatus projectID={projectID} variant={variant} />
      <Link
        className="button button--secondary"
        to={`/projects/${projectID}/episodes/${variant.episode.episode_id}/assets?asset=${encodeURIComponent(variant.asset.asset_id)}`}
      >
        {t('crossAssets.openEpisode')}
      </Link>
    </div>
  );
}

function VariantPrompt({ prompt }: { prompt: string }) {
  const { t } = useTranslation();
  const tooltipID = useId();
  const text = prompt || t('crossAssets.noPrompt');
  const expandable = Boolean(prompt && prompt.length > 48);
  return (
    <div
      className={`variant-prompt${expandable ? ' is-expandable' : ''}`}
      tabIndex={expandable ? 0 : undefined}
      aria-describedby={expandable ? tooltipID : undefined}
    >
      <p>{text}</p>
      {expandable && (
        <div className="variant-prompt__popover" id={tooltipID} role="tooltip">
          <strong>{t('crossAssets.fullPrompt')}</strong>
          <p>{text}</p>
          <small>{t('crossAssets.fullPromptHint')}</small>
        </div>
      )}
    </div>
  );
}

function useVariantArtifact(projectID: string, variant: ProjectAssetVariant) {
  const media = useQuery({
    queryKey: ['media-slot', projectID, variant.asset.primary_image_slot_id],
    queryFn: () => getMediaSlot(projectID, variant.asset.primary_image_slot_id),
    staleTime: 60_000,
  });
  const version = media.data?.versions.find(
    (item) => item.media_version_id === media.data.slot.current_media_version_id,
  );
  const artifact = media.data?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
  return artifact;
}

function VariantThumbnail({
  projectID,
  variant,
  fallback = '◇',
  title = '',
  onPreview,
}: {
  projectID: string;
  variant: ProjectAssetVariant;
  fallback?: string;
  title?: string;
  onPreview?: (value: ImagePreviewValue) => void;
}) {
  const artifact = useVariantArtifact(projectID, variant);
  const src = artifact ? artifactContentURL(projectID, artifact.artifact_id) : '';
  if (artifact && onPreview) {
    return (
      <ImagePreviewButton
        className="variant-image variant-image--preview"
        src={src}
        alt={title}
        loading="lazy"
        onPreview={() => onPreview({ src, title, width: artifact.width, height: artifact.height })}
      />
    );
  }
  return (
    <div className="variant-image">
      {artifact ? <img src={src} alt="" loading="lazy" /> : <span>{fallback}</span>}
    </div>
  );
}

function VariantStatus({ projectID, variant }: { projectID: string; variant: ProjectAssetVariant }) {
  const { t } = useTranslation();
  const artifact = useVariantArtifact(projectID, variant);
  return (
    <span className={artifact ? 'variant-status ready' : 'variant-status'}>
      <i />
      {artifact ? t('crossAssets.imageReady') : t('crossAssets.imageMissing')}
    </span>
  );
}
