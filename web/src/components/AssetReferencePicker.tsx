import { useEffect, useMemo, useRef, useState, type DragEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  getProjectReferenceImages,
  uploadEpisodeReferenceMedia,
  type Artifact,
  type EpisodeAsset,
  type MediaSlotDetails,
  type ProjectReferenceImage,
} from '../api/assets';
import { Dialog } from './Dialog';
import { PlusIcon, SearchIcon } from './Icons';
import { Select } from './Select';
import {
  imageReferenceLimitMessage,
  uniqueImageReferences,
  type AssetReferenceSelection,
} from '../assetImageReferences';
import '../styles/asset-reference-picker.css';

export type { AssetReferenceSelection } from '../assetImageReferences';

const maximumReferenceBytes = 25 * 1024 * 1024;
const acceptedReferenceTypes = new Set(['image/jpeg', 'image/png', 'image/webp']);

interface AssetReferencePickerProps {
  open: boolean;
  projectID: string;
  episodeID: string;
  asset: EpisodeAsset;
  media: MediaSlotDetails | undefined;
  value: AssetReferenceSelection[];
  maximumReferences: number;
  onClose: () => void;
  onSelect: (value: AssetReferenceSelection[]) => void;
  onUploadBusyChange: (busy: boolean) => void;
}

function referenceFileSupported(file: File) {
  if (acceptedReferenceTypes.has(file.type)) return true;
  return /\.(?:jpe?g|png|webp)$/i.test(file.name);
}

function projectItemSelection(item: ProjectReferenceImage): AssetReferenceSelection {
  return {
    key: item.key,
    artifact: item.artifact,
    name: item.name,
    episodeID: item.episode.episode_id,
    episodeTitle: item.episode.title,
    source: item.source,
  };
}

export function AssetReferencePicker({
  open,
  projectID,
  episodeID,
  asset,
  media,
  value,
  maximumReferences,
  onClose,
  onSelect,
  onUploadBusyChange,
}: AssetReferencePickerProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const [search, setSearch] = useState('');
  const [episodeFilter, setEpisodeFilter] = useState('all');
  const [selection, setSelection] = useState<AssetReferenceSelection[]>([]);
  const [fileError, setFileError] = useState<'type' | 'size' | 'limit'>();
  const [dragging, setDragging] = useState(false);
  const selectionSession = useRef(0);
  const activeScope = useRef('');
  activeScope.current = `${open}:${projectID}:${episodeID}:${asset.asset_id}`;
  const catalog = useQuery({
    queryKey: ['project-reference-images', projectID],
    queryFn: () => getProjectReferenceImages(projectID),
    enabled: open && Boolean(projectID),
    staleTime: 30_000,
  });

  useEffect(() => {
    selectionSession.current += 1;
    if (!open) return;
    setSelection(uniqueImageReferences(value));
    setSearch('');
    setEpisodeFilter('all');
    setFileError(undefined);
    setDragging(false);
  }, [open, value, projectID, episodeID, asset.asset_id]);

  const upload = useMutation({
    mutationFn: async (request: {
      files: File[];
      projectID: string;
      episodeID: string;
      scope: string;
      session: number;
      episodeTitle: string;
    }) => {
      for (const file of request.files) {
        const { reference_media } = await uploadEpisodeReferenceMedia(
          request.projectID,
          request.episodeID,
          file,
          'image',
        );
        // Uploads remain in the library even if the picker was closed or its
        // asset changed. Only the original open picker can receive draft refs.
        if (activeScope.current !== request.scope || selectionSession.current !== request.session) continue;
        setSelection((current) =>
          uniqueImageReferences([
            ...current,
            {
              key: `upload:${request.episodeID}:${reference_media.artifact.artifact_id}`,
              artifact: reference_media.artifact,
              name: reference_media.name,
              episodeID: request.episodeID,
              episodeTitle: request.episodeTitle,
              source: 'upload',
            },
          ]),
        );
      }
    },
    onSettled: async (_result, _error, request) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['project-reference-images', request.projectID] }),
        queryClient.invalidateQueries({
          queryKey: ['episode-reference-media', request.projectID, request.episodeID],
        }),
      ]);
    },
  });

  useEffect(() => {
    onUploadBusyChange(upload.isPending);
    return () => onUploadBusyChange(false);
  }, [onUploadBusyChange, upload.isPending]);

  const chooseFiles = (files: File[]) => {
    if (!files.length || upload.isPending) return;
    upload.reset();
    setFileError(undefined);
    if (selection.length + files.length > maximumReferences) {
      setFileError('limit');
      return;
    }
    if (files.some((file) => !referenceFileSupported(file))) {
      setFileError('type');
      return;
    }
    if (files.some((file) => file.size > maximumReferenceBytes)) {
      setFileError('size');
      return;
    }
    upload.mutate({
      files,
      projectID,
      episodeID,
      scope: activeScope.current,
      session: selectionSession.current,
      episodeTitle: catalog.data?.episodes.find((episode) => episode.episode_id === episodeID)?.title ?? '',
    });
  };

  const toggleSelection = (value: AssetReferenceSelection) => {
    if (upload.isPending) return;
    const selected = selection.some((item) => item.artifact.artifact_id === value.artifact.artifact_id);
    setFileError(undefined);
    if (!selected && selection.length >= maximumReferences) {
      setFileError('limit');
      return;
    }
    setSelection((current) =>
      selected
        ? current.filter((item) => item.artifact.artifact_id !== value.artifact.artifact_id)
        : [...current, value],
    );
  };

  const currentVersions = useMemo(() => {
    if (!media) return [];
    return media.versions
      .filter((version) => !version.deleted_at)
      .sort((left, right) => right.sequence_number - left.sequence_number)
      .flatMap((version) => {
        const artifact = media.artifacts.find((item) => item.artifact_id === version.artifact_id);
        if (!artifact || artifact.kind !== 'image') return [];
        return [
          {
            key: `version:${version.media_version_id}`,
            artifact,
            name: `${asset.identity.name} · V${version.sequence_number}`,
            episodeID,
            episodeTitle:
              catalog.data?.episodes.find((episode) => episode.episode_id === episodeID)?.title ?? '',
            source: 'version' as const,
            version: version.sequence_number,
          },
        ];
      });
  }, [asset.identity.name, catalog.data?.episodes, episodeID, media]);

  const visibleImages = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    return (catalog.data?.images ?? [])
      .filter((item) => item.asset?.asset_id !== asset.asset_id)
      .filter((item) => episodeFilter === 'all' || item.episode.episode_id === episodeFilter)
      .filter((item) => {
        if (!query) return true;
        const sequence = String(item.episode.sequence_number).padStart(2, '0');
        return `${item.name} ${item.artifact.original_filename} ${item.episode.title} ${sequence}`
          .toLocaleLowerCase()
          .includes(query);
      })
      .sort((left, right) => {
        const leftCurrent = left.episode.episode_id === episodeID ? 1 : 0;
        const rightCurrent = right.episode.episode_id === episodeID ? 1 : 0;
        return (
          rightCurrent - leftCurrent ||
          right.updated_at.localeCompare(left.updated_at) ||
          left.episode.sequence_number - right.episode.sequence_number ||
          left.name.localeCompare(right.name)
        );
      });
  }, [asset.asset_id, catalog.data?.images, episodeFilter, episodeID, search]);

  const episodeOptions = [
    { value: 'all', label: t('assets.referenceAllEpisodes') },
    ...(catalog.data?.episodes ?? []).map((episode) => ({
      value: episode.episode_id,
      label:
        episode.episode_id === episodeID
          ? t('assets.referenceCurrentEpisode')
          : `${String(episode.sequence_number).padStart(2, '0')} · ${episode.title}`,
    })),
  ];
  const referenceLimitMessage = imageReferenceLimitMessage(i18n.resolvedLanguage, maximumReferences);
  const errorMessage =
    fileError === 'limit' || selection.length > maximumReferences
      ? referenceLimitMessage
      : fileError
        ? t(`assets.referenceFileErrors.${fileError}`)
        : upload.isError &&
            upload.variables?.scope === activeScope.current &&
            upload.variables?.session === selectionSession.current
          ? t('assets.referenceImportFailed')
          : '';

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragging(false);
    chooseFiles(Array.from(event.dataTransfer.files));
  };

  const confirmSelection = () => {
    if (upload.isPending || selection.length > maximumReferences) return;
    onSelect(selection);
    onClose();
  };

  const closePicker = () => {
    selectionSession.current += 1;
    onClose();
  };

  return (
    <Dialog open={open} label={t('assets.chooseReference')} onClose={closePicker}>
      <section className="dialog-panel asset-reference-picker">
        <header className="dialog-header asset-reference-picker__header">
          <div>
            <p>{asset.identity.name}</p>
            <h2>{t('assets.chooseReference')}</h2>
            <small>{t('assets.referenceExplanationCompact')}</small>
          </div>
          <button type="button" className="icon-button" aria-label={t('common.close')} onClick={closePicker}>
            ×
          </button>
        </header>

        <div className="asset-reference-picker__toolbar">
          <label className="search-field asset-reference-picker__search">
            <SearchIcon />
            <input
              value={search}
              aria-label={t('assets.referenceSearch')}
              placeholder={t('assets.referenceSearch')}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <Select
            compact
            ariaLabel={t('assets.referenceEpisodeFilter')}
            value={episodeFilter}
            options={episodeOptions}
            onChange={setEpisodeFilter}
          />
          <button
            type="button"
            className="button button--secondary asset-reference-picker__upload"
            disabled={upload.isPending}
            onClick={() => fileInput.current?.click()}
          >
            <PlusIcon />
            {t(upload.isPending ? 'assets.referenceUploading' : 'assets.referenceUpload')}
          </button>
          <input
            ref={fileInput}
            className="sr-only"
            type="file"
            multiple
            accept="image/jpeg,image/png,image/webp"
            aria-label={t('assets.referenceUpload')}
            disabled={upload.isPending}
            onChange={(event) => {
              chooseFiles(Array.from(event.target.files ?? []));
              event.target.value = '';
            }}
          />
        </div>

        {errorMessage && (
          <p className="form-error asset-reference-picker__error" role="alert">
            {errorMessage}
          </p>
        )}

        <div
          className={`asset-reference-picker__body${dragging ? ' is-dragging' : ''}`}
          onDragEnter={(event) => {
            event.preventDefault();
            if (!upload.isPending) setDragging(true);
          }}
          onDragOver={(event) => event.preventDefault()}
          onDragLeave={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false);
          }}
          onDrop={handleDrop}
        >
          {dragging && (
            <div className="asset-reference-picker__drop" aria-hidden="true">
              <PlusIcon />
              <strong>{t('assets.referenceDrop')}</strong>
              <small>{t('assets.referenceFileHint')}</small>
            </div>
          )}

          {currentVersions.length > 0 && (
            <section className="asset-reference-picker__section">
              <header>
                <strong>{t('assets.referenceCurrentVersions')}</strong>
                <span>{currentVersions.length}</span>
              </header>
              <div className="asset-reference-version-list">
                {currentVersions.map((item) => (
                  <ReferenceImageCard
                    key={item.key}
                    projectID={projectID}
                    artifact={item.artifact}
                    name={`V${item.version}`}
                    episodeLabel={t('assets.referenceCurrentAsset')}
                    sourceLabel={`${asset.identity.name} · V${item.version}`}
                    selected={selection.some(
                      (value) => value.artifact.artifact_id === item.artifact.artifact_id,
                    )}
                    disabled={upload.isPending}
                    onSelect={() => toggleSelection(item)}
                  />
                ))}
              </div>
            </section>
          )}

          <section className="asset-reference-picker__section asset-reference-picker__project">
            <header>
              <strong>{t('assets.referenceProjectLibrary')}</strong>
              {!catalog.isPending && !catalog.isError && <span>{visibleImages.length}</span>}
            </header>
            {catalog.isPending ? (
              <div className="asset-reference-picker__state" role="status">
                <span className="spinner" />
                <strong>{t('assets.referenceLoading')}</strong>
              </div>
            ) : catalog.isError ? (
              <div className="asset-reference-picker__state" role="alert">
                <strong>{t('assets.referenceLoadFailed')}</strong>
                <button
                  type="button"
                  className="button button--secondary"
                  onClick={() => void catalog.refetch()}
                >
                  {t('common.retry')}
                </button>
              </div>
            ) : visibleImages.length ? (
              <div className="asset-reference-project-grid">
                {visibleImages.map((item) => (
                  <ReferenceImageCard
                    key={item.key}
                    projectID={projectID}
                    artifact={item.artifact}
                    name={item.name}
                    episodeLabel={
                      item.episode.episode_id === episodeID
                        ? t('assets.referenceCurrentEpisode')
                        : `${String(item.episode.sequence_number).padStart(2, '0')} · ${item.episode.title}`
                    }
                    sourceLabel={t(
                      item.source === 'asset'
                        ? `assets.kinds.${item.asset!.identity.kind}`
                        : 'assets.referenceUploadedSource',
                    )}
                    selected={selection.some(
                      (value) => value.artifact.artifact_id === item.artifact.artifact_id,
                    )}
                    disabled={upload.isPending}
                    onSelect={() => toggleSelection(projectItemSelection(item))}
                  />
                ))}
              </div>
            ) : (
              <div className="asset-reference-picker__state">
                <strong>{t('assets.referenceEmpty')}</strong>
                <small>{t('assets.referenceEmptyHint')}</small>
                {(search || episodeFilter !== 'all') && (
                  <button
                    type="button"
                    className="button button--secondary"
                    onClick={() => {
                      setSearch('');
                      setEpisodeFilter('all');
                    }}
                  >
                    {t('assets.referenceClearFilters')}
                  </button>
                )}
              </div>
            )}
          </section>
        </div>

        <footer className="dialog-footer asset-reference-picker__footer">
          <div className="asset-reference-picker__selection">
            {selection.length ? (
              <>
                {selection.map((value) => (
                  <img
                    key={value.artifact.artifact_id}
                    src={artifactContentURL(projectID, value.artifact.artifact_id)}
                    alt=""
                    title={value.name}
                  />
                ))}
                <span>
                  <small>
                    {selection.length} / {maximumReferences}
                  </small>
                  <strong>{selection.map((value) => value.name).join(' · ')}</strong>
                </span>
              </>
            ) : (
              <small>{t('assets.referenceNoneSelected')}</small>
            )}
          </div>
          <button
            type="button"
            className="button button--primary asset-reference-picker__confirm"
            disabled={
              (!selection.length && !value.length) || selection.length > maximumReferences || upload.isPending
            }
            onClick={confirmSelection}
          >
            {!selection.length && value.length
              ? i18n.resolvedLanguage?.startsWith('zh')
                ? '清除参考图'
                : 'Clear reference images'
              : selection.length <= 1
                ? t('assets.referenceUse')
                : i18n.resolvedLanguage?.startsWith('zh')
                  ? `使用参考图 (${selection.length})`
                  : `Use reference images (${selection.length})`}
          </button>
          <button type="button" className="button button--secondary" onClick={closePicker}>
            {t('common.cancel')}
          </button>
        </footer>
      </section>
    </Dialog>
  );
}

function ReferenceImageCard({
  projectID,
  artifact,
  name,
  episodeLabel,
  sourceLabel,
  selected,
  disabled,
  onSelect,
}: {
  projectID: string;
  artifact: Artifact;
  name: string;
  episodeLabel: string;
  sourceLabel: string;
  selected: boolean;
  disabled: boolean;
  onSelect: () => void;
}) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      className={`asset-reference-image-card${selected ? ' is-selected' : ''}`}
      aria-pressed={selected}
      disabled={disabled}
      aria-label={t('assets.referenceChooseCard', { name, episode: episodeLabel })}
      onClick={onSelect}
    >
      <span className="asset-reference-image-card__preview">
        <img src={artifactContentURL(projectID, artifact.artifact_id)} alt="" loading="lazy" />
        {selected && <i aria-hidden="true">✓</i>}
      </span>
      <span className="asset-reference-image-card__copy">
        <strong title={name}>{name}</strong>
        <small title={episodeLabel}>{episodeLabel}</small>
        <em>{sourceLabel}</em>
      </span>
    </button>
  );
}
