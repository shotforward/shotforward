import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams, useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  createAsset,
  deleteAsset,
  deleteEpisodeReferenceMedia,
  getMediaSlot,
  listEpisodeAssets,
  listEpisodeReferenceMedia,
  renameAssetIdentity,
  selectMediaVersion,
  updateAssetPrompt,
  updateEpisodeReferenceMedia,
  uploadEpisodeReferenceMedia,
  uploadMedia,
  type AssetKind,
  type EpisodeAsset,
  type EpisodeReferenceMedia,
} from '../api/assets';
import { getEpisode } from '../api/episodes';
import {
  checkGenerationReadiness,
  submitGeneration,
  type GenerationJob,
  type GenerationTarget,
} from '../api/generation';
import { getTaskSnapshot, type TaskState } from '../api/tasks';
import { AgentDock, type AgentDraftRequest } from '../components/AgentDock';
import { AgentPromptActionButton } from '../components/AgentPromptActionButton';
import { AssetReferencePicker, type AssetReferenceSelection } from '../components/AssetReferencePicker';
import { AssetReferenceControls, AssetReferenceLimitWarning } from '../components/AssetReferenceControls';
import { imageReferenceMaximum, imageReferenceLimitMessage } from '../assetImageReferences';
import { Dialog } from '../components/Dialog';
import { ImageLightbox, ImagePreviewButton, type ImagePreviewValue } from '../components/ImagePreview';
import { MediaPreviewDialog, MediaThumbnailButton, type MediaPreviewValue } from '../components/MediaPreview';
import { MediaTrimEditor } from '../components/MediaTrimEditor';
import { mediaTrimRange, savedMediaTrim, validMediaTrim } from '../mediaTrim';
import { AssetKindIcon, PlusIcon, SearchIcon } from '../components/Icons';
import { EpisodeWorkspaceLoading, ProjectTabs } from '../components/ProjectTabs';
import { useAgentTemplates } from '../useAgentTemplates';
import { truncateUnicode } from '../text';
import { taskFailureMessage } from '../taskFailure';
import { businessErrorCode, businessErrorMessage } from '../businessErrors';
import { RechargeAction } from '../components/RechargeAction';
import '../styles/episode-assets.css';
import '../styles/media-workspaces.css';
import { isProviderAvailable, listProviders } from '../api/providers';
import { setImageGeneratorPreference, useImageGeneratorPreference } from '../preferences';
import { Select } from '../components/Select';

const kinds: AssetKind[] = ['character', 'location', 'prop', 'style'];

function imageAspectStyle(width: number, height: number): CSSProperties | undefined {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return undefined;
  }
  return { aspectRatio: `${width} / ${height}` };
}

function imageAspectRatio(width: number, height: number): string {
  let left = Math.abs(Math.round(width));
  let right = Math.abs(Math.round(height));
  while (right) {
    [left, right] = [right, left % right];
  }
  const divisor = left || 1;
  return `${Math.round(width) / divisor}:${Math.round(height) / divisor}`;
}

function constrainedImagePreviewStyle(width: number, height: number): CSSProperties | undefined {
  const style = imageAspectStyle(width, height);
  if (!style) return undefined;

  const ratio = width / height;
  const viewportWidth = Number((ratio * 52).toFixed(4));
  const maximumWidth = Number((ratio * 520).toFixed(2));
  return {
    ...style,
    width: `min(100%, ${viewportWidth}dvh, ${maximumWidth}px)`,
  };
}

export function EpisodeAssetsPage() {
  const { projectId = '', episodeId = '' } = useParams();
  const { t, i18n } = useTranslation();
  const serviceCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? {
        label: '图像生成服务',
        codex: '本机 Codex 订阅',
        cloud: 'ShotForward 云服务',
        checking: '正在检查服务…',
        unavailable: '暂无可用服务',
      }
    : {
        label: 'Image generation service',
        codex: 'Local Codex subscription',
        cloud: 'ShotForward Cloud',
        checking: 'Checking services…',
        unavailable: 'No available service',
      };
  const agentTemplates = useAgentTemplates();
  const queryClient = useQueryClient();
  const imageGenerator = useImageGeneratorPreference();
  const providers = useQuery({ queryKey: ['generation-providers'], queryFn: listProviders });
  const availableImageProviders =
    providers.data?.providers.filter(
      (item) => item.generator_id.startsWith('image.') && isProviderAvailable(item),
    ) ?? [];
  const imageProviderOptions = availableImageProviders.map((item) => ({
    value: item.generator_id,
    label:
      item.generator_id === 'image.codex'
        ? serviceCopy.codex
        : item.generator_id === 'image.shotforward'
          ? serviceCopy.cloud
          : item.generator_id,
  }));
  const activeImageGenerator = imageProviderOptions.some((item) => item.value === imageGenerator)
    ? imageGenerator
    : (imageProviderOptions[0]?.value ?? '');
  const cloudImage = availableImageProviders.find((item) => item.generator_id === 'image.shotforward');
  const cloudImageModel =
    cloudImage?.models?.find((item) => item.is_default)?.model_id ?? cloudImage?.model_id;
  const maximumReferences = imageReferenceMaximum(
    providers.data?.providers.find((item) => item.generator_id === activeImageGenerator),
    activeImageGenerator === 'image.shotforward' ? cloudImageModel : undefined,
  );
  const [searchParams] = useSearchParams();
  const episode = useQuery({
    queryKey: ['episode', projectId, episodeId],
    queryFn: () => getEpisode(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const assets = useQuery({
    queryKey: ['assets', projectId, episodeId],
    queryFn: () => listEpisodeAssets(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const referenceMedia = useQuery({
    queryKey: ['episode-reference-media', projectId, episodeId],
    queryFn: () => listEpisodeReferenceMedia(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const [assetSection, setAssetSection] = useState<'generated' | 'uploaded'>('generated');
  const [kind, setKind] = useState<AssetKind | 'all'>('all');
  const [search, setSearch] = useState('');
  const [selectedID, setSelectedID] = useState(searchParams.get('asset') ?? '');
  const [mediaOpen, setMediaOpen] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [createKind, setCreateKind] = useState<AssetKind>('character');
  const [createName, setCreateName] = useState('');
  const [createPrompt, setCreatePrompt] = useState('');
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [assetMenuOpen, setAssetMenuOpen] = useState(false);
  const [assetDraftDirty, setAssetDraftDirty] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [discardTargetID, setDiscardTargetID] = useState('');
  const [mediaFile, setMediaFile] = useState<File>();
  const [referenceOpen, setReferenceOpen] = useState(false);
  const [referenceSelection, setReferenceSelection] = useState<AssetReferenceSelection[]>([]);
  const [referenceUploading, setReferenceUploading] = useState(false);
  const [submittedImageJob, setSubmittedImageJob] = useState<GenerationJob>();
  const [trackedImageJobID, setTrackedImageJobID] = useState('');
  const [agentDraftRequest, setAgentDraftRequest] = useState<AgentDraftRequest>();
  const [previewImage, setPreviewImage] = useState<ImagePreviewValue>();
  const requestAgentDraft = (request: Omit<AgentDraftRequest, 'id'>) =>
    setAgentDraftRequest((current) => ({ id: (current?.id ?? 0) + 1, ...request }));
  const openAgent = (text: string) => requestAgentDraft({ text });
  const referenceAssetWithAgent = (asset: EpisodeAsset) =>
    requestAgentDraft({ reference: { kind: 'asset', value: asset } });
  const referenceMediaWithAgent = (value: EpisodeReferenceMedia) =>
    requestAgentDraft({ reference: { kind: 'uploaded_media', value } });

  const visibleAssets = useMemo(
    () =>
      (assets.data?.assets ?? []).filter(
        (asset) =>
          (kind === 'all' || asset.identity.kind === kind) &&
          asset.identity.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()),
      ),
    [assets.data, kind, search],
  );
  const selected = (assets.data?.assets ?? []).find((asset) => asset.asset_id === selectedID);

  const media = useQuery({
    queryKey: ['media-slot', projectId, selected?.primary_image_slot_id],
    queryFn: () => getMediaSlot(projectId, selected!.primary_image_slot_id),
    enabled: Boolean(projectId && selected?.primary_image_slot_id),
  });
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: getTaskSnapshot,
    enabled: Boolean(projectId && episodeId),
    refetchInterval: 3_000,
  });
  const refreshedMediaResults = useRef(new Set<string>());
  useEffect(() => {
    for (const task of tasks.data?.tasks ?? []) {
      if (
        task.task_type !== 'generation_job' ||
        task.state !== 'succeeded' ||
        task.project_id !== projectId ||
        task.episode_id !== episodeId ||
        !task.target_media_slot_id
      )
        continue;
      const resultKey = `${task.task_id}:${task.result_media_version_id ?? task.revision}`;
      if (refreshedMediaResults.current.has(resultKey)) continue;
      refreshedMediaResults.current.add(resultKey);
      void queryClient.invalidateQueries({
        queryKey: ['media-slot', projectId, task.target_media_slot_id],
        exact: true,
      });
    }
  }, [episodeId, projectId, queryClient, tasks.data?.tasks]);
  const activeImageTasksBySlot = useMemo(() => {
    const result = new Map<string, TaskState>();
    for (const task of tasks.data?.tasks ?? []) {
      if (
        task.project_id === projectId &&
        task.episode_id === episodeId &&
        task.task_type === 'generation_job' &&
        task.target_media_kind === 'image' &&
        task.target_media_slot_id &&
        ['queued', 'running', 'stop_requested'].includes(task.state) &&
        !result.has(task.target_media_slot_id)
      ) {
        result.set(task.target_media_slot_id, task.state);
      }
    }
    return result;
  }, [episodeId, projectId, tasks.data?.tasks]);
  const currentVersion = media.data?.versions.find(
    (version) => version.media_version_id === media.data.slot.current_media_version_id,
  );
  const currentArtifact = media.data?.artifacts.find(
    (artifact) => artifact.artifact_id === currentVersion?.artifact_id,
  );
  const liveImageTask = tasks.data?.tasks.find(
    (task) =>
      task.task_type === 'generation_job' &&
      task.task_id === (trackedImageJobID || submittedImageJob?.generation_job_id) &&
      task.target_media_slot_id === selected?.primary_image_slot_id,
  );
  const existingActiveImageTask = tasks.data?.tasks.find(
    (task) =>
      task.task_type === 'generation_job' &&
      task.target_media_kind === 'image' &&
      task.target_media_slot_id === selected?.primary_image_slot_id &&
      ['queued', 'running', 'stop_requested'].includes(task.state),
  );
  const imageTask = liveImageTask ?? existingActiveImageTask;
  const imageTaskState = imageTask?.state ?? submittedImageJob?.state;
  const imageTaskActive = Boolean(
    imageTaskState && ['queued', 'running', 'stop_requested'].includes(imageTaskState),
  );
  const mediaVersions = media.data?.versions.filter((version) => !version.deleted_at) ?? [];
  const mediaArtifacts = media.data?.artifacts ?? [];
  const currentMediaVersionID = media.data?.slot.current_media_version_id;
  const updateDetails = useMutation({
    mutationFn: async ({ asset, name, prompt }: { asset: EpisodeAsset; name: string; prompt: string }) => {
      if (name !== asset.identity.name)
        await renameAssetIdentity(projectId, asset.identity.asset_identity_id, name, asset.identity.revision);
      if (prompt !== asset.prompt) await updateAssetPrompt(projectId, asset.asset_id, prompt, asset.revision);
    },
    onSuccess: async () => {
      await assets.refetch();
    },
  });
  const create = useMutation({
    mutationFn: () => createAsset(projectId, episodeId, createKind, createName.trim(), createPrompt.trim()),
    onSuccess: async (result) => {
      setCreateOpen(false);
      setCreateName('');
      setCreatePrompt('');
      setSelectedID(result.asset.asset_id);
      await assets.refetch();
    },
  });
  const importImage = useMutation({
    mutationFn: () =>
      uploadMedia(projectId, selected!.primary_image_slot_id, mediaFile!, media.data!.slot.revision),
    onSuccess: (result) => {
      queryClient.setQueryData(['media-slot', projectId, selected?.primary_image_slot_id], result);
      setMediaFile(undefined);
      setMediaOpen(false);
    },
  });
  const selectVersion = useMutation({
    mutationFn: (versionID: string) =>
      selectMediaVersion(projectId, selected!.primary_image_slot_id, versionID, media.data!.slot.revision),
    onSuccess: (result) =>
      queryClient.setQueryData(['media-slot', projectId, selected?.primary_image_slot_id], result),
  });
  const remove = useMutation({
    mutationFn: () => deleteAsset(projectId, selected!.asset_id, selected!.revision),
    onSuccess: async () => {
      setDeleteOpen(false);
      setAssetDraftDirty(false);
      setSelectedID('');
      await assets.refetch();
      await queryClient.invalidateQueries({ queryKey: ['project-trash', projectId] });
    },
  });
  const generateImage = useMutation({
    mutationFn: async () => {
      if (referenceSelection.length > maximumReferences || referenceUploading) {
        throw new Error(imageReferenceLimitMessage(i18n.resolvedLanguage, maximumReferences));
      }
      const references = referenceSelection.map(({ artifact }) => ({
        role: 'image_reference' as const,
        artifact_id: artifact.artifact_id,
      }));
      const target: GenerationTarget = {
        media_slot_id: selected!.primary_image_slot_id,
        expected_slot_revision: media.data!.slot.revision,
        prompt: selected!.prompt,
        generator_id: activeImageGenerator,
        ...(activeImageGenerator === 'image.shotforward' && cloudImageModel
          ? { provider_model_id: cloudImageModel }
          : {}),
        references,
        parameters: {},
      };
      await checkGenerationReadiness(projectId, episodeId, target);
      return submitGeneration(projectId, episodeId, target);
    },
    onSuccess: async (result) => {
      setSubmittedImageJob(result.jobs[0]);
      setTrackedImageJobID(result.jobs[0]?.generation_job_id ?? '');
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
  });
  const completeAssetSelection = (targetID: string) => {
    setAssetDraftDirty(false);
    setAssetMenuOpen(false);
    setDiscardOpen(false);
    setDiscardTargetID('');
    setSelectedID(targetID);
  };
  const requestAssetSelection = (targetID: string) => {
    if (targetID === selectedID || updateDetails.isPending) return;
    if (assetDraftDirty) {
      setDiscardTargetID(targetID);
      setDiscardOpen(true);
      return;
    }
    completeAssetSelection(targetID);
  };
  const requestCloseAsset = () => requestAssetSelection('');

  useEffect(() => {
    if (!selected || mediaOpen || referenceOpen || deleteOpen || discardOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || updateDetails.isPending) return;
      event.preventDefault();
      if (assetMenuOpen) {
        setAssetMenuOpen(false);
        return;
      }
      if (assetDraftDirty) {
        setDiscardTargetID('');
        setDiscardOpen(true);
      } else {
        setSelectedID('');
      }
    };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [
    assetDraftDirty,
    assetMenuOpen,
    deleteOpen,
    discardOpen,
    mediaOpen,
    referenceOpen,
    selected,
    updateDetails.isPending,
  ]);

  useEffect(() => {
    setReferenceSelection([]);
    setReferenceOpen(false);
    setSubmittedImageJob(undefined);
    setTrackedImageJobID('');
  }, [selectedID]);

  useEffect(() => {
    if (!trackedImageJobID && existingActiveImageTask) {
      setTrackedImageJobID(existingActiveImageTask.task_id);
    }
  }, [existingActiveImageTask, trackedImageJobID]);

  const countFor = (assetKind: AssetKind) =>
    (assets.data?.assets ?? []).filter((asset) => asset.identity.kind === assetKind).length;

  if (episode.isPending || assets.isPending)
    return (
      <EpisodeWorkspaceLoading
        projectID={projectId}
        episodeID={episodeId}
        projectName={episode.data?.project.name}
        episodeTitle={episode.data?.episode.title}
      />
    );
  if (episode.isError || assets.isError)
    return (
      <div className="page message-state">
        <h2>{t('assets.loadFailed')}</h2>
        <button
          className="button"
          onClick={() => {
            void episode.refetch();
            void assets.refetch();
          }}
        >
          {t('common.retry')}
        </button>
      </div>
    );
  return (
    <div className="creative-page assets-page">
      <header className="creative-heading">
        <div>
          <span className="creative-heading__project">{episode.data.project.name}</span>
          <strong>{episode.data.episode.title}</strong>
        </div>
      </header>
      <ProjectTabs projectID={projectId} episodeID={episodeId} />
      <div className="assets-agent-layout">
        <main className="assets-workspace">
          <nav className="asset-source-tabs" aria-label={t('assets.sourceTabsLabel')}>
            <button
              className={assetSection === 'generated' ? 'active' : ''}
              onClick={() => setAssetSection('generated')}
            >
              {t('assets.generatedMedia')} <span>{assets.data.assets.length}</span>
            </button>
            <button
              className={assetSection === 'uploaded' ? 'active' : ''}
              onClick={() => setAssetSection('uploaded')}
            >
              {t('assets.uploadedMedia')} <span>{referenceMedia.data?.reference_media.length ?? 0}</span>
            </button>
          </nav>
          {assetSection === 'generated' ? (
            <>
              <aside className="asset-catalog">
                <header>
                  <div>
                    <h2>{t('assets.title')}</h2>
                    <p>{t('assets.count', { count: assets.data.assets.length })}</p>
                  </div>
                  <label className="search-field asset-search">
                    <SearchIcon />
                    <input
                      aria-label={t('assets.search')}
                      value={search}
                      onChange={(event) => setSearch(event.target.value)}
                      placeholder={t('assets.search')}
                    />
                  </label>
                  <div className="asset-page-actions">
                    <button
                      type="button"
                      className="button asset-manual-add"
                      onClick={() => {
                        create.reset();
                        setCreateOpen(true);
                      }}
                    >
                      <span aria-hidden="true">＋</span> {t('assets.new')}
                    </button>
                    <button
                      type="button"
                      className="button asset-agent-add"
                      onClick={() => openAgent(agentTemplates.render('asset.add'))}
                    >
                      <span aria-hidden="true">＋</span> {t('assets.addWithAgent')}
                    </button>
                    <button
                      type="button"
                      className="button button--secondary asset-storyboard-action"
                      onClick={() => openAgent(agentTemplates.render('storyboard.plan'))}
                    >
                      {t('storyboard.agentInitializeStoryboard')}
                      <span aria-hidden="true">→</span>
                    </button>
                  </div>
                </header>
                <nav className="asset-kind-tabs">
                  <button className={kind === 'all' ? 'active' : ''} onClick={() => setKind('all')}>
                    {t('assets.all')}
                    <span>{assets.data.assets.length}</span>
                  </button>
                  {kinds.map((item) => (
                    <button
                      key={item}
                      className={kind === item ? 'active' : ''}
                      onClick={() => setKind(item)}
                    >
                      {t(`assets.kinds.${item}`)}
                      <span>{countFor(item)}</span>
                    </button>
                  ))}
                </nav>
                <div className="asset-list">
                  {visibleAssets.map((asset) => (
                    <article
                      key={asset.asset_id}
                      className={`asset-list-card${asset.asset_id === selected?.asset_id ? ' active' : ''}`}
                    >
                      <button
                        type="button"
                        className="asset-list-card__select"
                        onClick={() => requestAssetSelection(asset.asset_id)}
                      >
                        <AssetCardContent
                          projectID={projectId}
                          asset={asset}
                          generationState={activeImageTasksBySlot.get(asset.primary_image_slot_id)}
                        />
                      </button>
                      <button
                        type="button"
                        className="asset-agent-reference asset-agent-reference--compact"
                        title={t('assets.referenceWithAgent', { name: asset.identity.name })}
                        aria-label={t('assets.referenceToAgent')}
                        onClick={() => referenceAssetWithAgent(asset)}
                      >
                        <span aria-hidden="true">@</span> {t('assets.referenceToAgent')}
                      </button>
                    </article>
                  ))}
                </div>
              </aside>
              {selected ? (
                <>
                  <button
                    type="button"
                    className="asset-detail-backdrop"
                    aria-label={t('common.close')}
                    onClick={requestCloseAsset}
                    disabled={updateDetails.isPending}
                  />
                  <section
                    className="asset-detail"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="asset-detail-title"
                  >
                    <header>
                      <div>
                        <span className="eyebrow">{t(`assets.kinds.${selected.identity.kind}`)}</span>
                        <h1 id="asset-detail-title">{selected.identity.name}</h1>
                      </div>
                      <div className="asset-detail__controls">
                        <span className="revision">v{selected.revision}</span>
                        <div className="asset-detail-menu">
                          <button
                            type="button"
                            className="icon-button asset-detail-menu__trigger"
                            aria-label={t('assets.moreActions')}
                            aria-expanded={assetMenuOpen}
                            onClick={() => setAssetMenuOpen((open) => !open)}
                          >
                            ···
                          </button>
                          {assetMenuOpen && (
                            <div className="asset-detail-menu__popover" role="menu">
                              <button
                                type="button"
                                role="menuitem"
                                onClick={() => {
                                  setAssetMenuOpen(false);
                                  remove.reset();
                                  setDeleteOpen(true);
                                }}
                              >
                                {t('assets.delete')}
                              </button>
                            </div>
                          )}
                        </div>
                        <button
                          type="button"
                          className="icon-button asset-detail__close"
                          aria-label={t('common.close')}
                          onClick={requestCloseAsset}
                          disabled={updateDetails.isPending}
                        >
                          ×
                        </button>
                      </div>
                    </header>
                    <div className="asset-detail-grid">
                      <section className="asset-media-panel">
                        <div
                          className={`asset-preview${currentArtifact ? ' asset-preview--fitted' : ''}`}
                          style={
                            currentArtifact
                              ? constrainedImagePreviewStyle(currentArtifact.width, currentArtifact.height)
                              : undefined
                          }
                        >
                          {currentArtifact ? (
                            <ImagePreviewButton
                              className="asset-preview__image-trigger"
                              src={artifactContentURL(projectId, currentArtifact.artifact_id)}
                              alt={selected.identity.name}
                              onPreview={() =>
                                setPreviewImage({
                                  src: artifactContentURL(projectId, currentArtifact.artifact_id),
                                  title: selected.identity.name,
                                  width: currentArtifact.width,
                                  height: currentArtifact.height,
                                })
                              }
                            />
                          ) : (
                            <div>
                              <span>◇</span>
                              <strong>{t('assets.noImage')}</strong>
                              <small>{t('assets.noImageHint')}</small>
                            </div>
                          )}
                          {(generateImage.isPending || imageTaskActive) && (
                            <div className="asset-generation-overlay" role="status">
                              <span className="spinner" />
                              <strong>{t('assets.imageRunning')}</strong>
                            </div>
                          )}
                        </div>
                        {currentArtifact && currentArtifact.width > 0 && currentArtifact.height > 0 && (
                          <dl className="asset-image-metadata">
                            <div>
                              <dt>{t('projects.aspectRatio')}</dt>
                              <dd>{imageAspectRatio(currentArtifact.width, currentArtifact.height)}</dd>
                            </div>
                            <div>
                              <dt>{t('assets.resolution')}</dt>
                              <dd>
                                {currentArtifact.width} × {currentArtifact.height} px
                              </dd>
                            </div>
                          </dl>
                        )}
                        <div className="version-strip">
                          <header>
                            <div className="version-strip__heading">
                              <strong>{t('assets.versions')}</strong>
                              <span className="version-strip__count">{mediaVersions.length}</span>
                            </div>
                            <button
                              type="button"
                              className="asset-import-action"
                              onClick={() => {
                                importImage.reset();
                                setMediaOpen(true);
                              }}
                              disabled={!media.data || imageTaskActive}
                            >
                              <PlusIcon />
                              {t('assets.importImage')}
                            </button>
                          </header>
                          {media.isError ? (
                            <p className="form-error">{t('assets.mediaFailed')}</p>
                          ) : (
                            <div
                              className={mediaVersions.length ? undefined : 'version-strip__versions--empty'}
                            >
                              {mediaVersions.length ? (
                                mediaVersions.map((version) => {
                                  const artifact = mediaArtifacts.find(
                                    (item) => item.artifact_id === version.artifact_id,
                                  );
                                  return (
                                    <button
                                      key={version.media_version_id}
                                      className={
                                        version.media_version_id === currentMediaVersionID ? 'active' : ''
                                      }
                                      onClick={() => selectVersion.mutate(version.media_version_id)}
                                      disabled={selectVersion.isPending}
                                    >
                                      <span>
                                        {artifact ? (
                                          <img
                                            src={artifactContentURL(projectId, artifact.artifact_id)}
                                            alt=""
                                            style={{ objectFit: 'contain', objectPosition: 'center' }}
                                          />
                                        ) : (
                                          '◇'
                                        )}
                                      </span>
                                      <small>V{version.sequence_number}</small>
                                    </button>
                                  );
                                })
                              ) : (
                                <div className="version-strip__empty">
                                  <span aria-hidden="true">◇</span>
                                  <span>
                                    <strong>{t('assets.noVersions')}</strong>
                                    <small>{t('assets.noVersionsHint')}</small>
                                  </span>
                                </div>
                              )}
                            </div>
                          )}
                        </div>
                      </section>
                      <AssetEditor
                        asset={selected}
                        busy={updateDetails.isPending}
                        error={updateDetails.isError}
                        onDirtyChange={setAssetDraftDirty}
                        onSave={(name, prompt) => updateDetails.mutate({ asset: selected, name, prompt })}
                        onEdit={() => updateDetails.reset()}
                        onAgentEditPrompt={() =>
                          openAgent(
                            agentTemplates.render('asset.edit_prompt', {
                              name: selected.identity.name,
                              asset_id: selected.asset_id,
                            }),
                          )
                        }
                        generationControls={
                          <div className="asset-prompt-generation">
                            <div className="asset-prompt-generation__actions">
                              <Select
                                ariaLabel={serviceCopy.label}
                                value={activeImageGenerator}
                                options={imageProviderOptions}
                                placeholder={
                                  providers.isPending ? serviceCopy.checking : serviceCopy.unavailable
                                }
                                compact
                                disabled={
                                  providers.isPending ||
                                  imageProviderOptions.length === 0 ||
                                  generateImage.isPending ||
                                  imageTaskActive
                                }
                                onChange={(value) =>
                                  setImageGeneratorPreference(value as 'image.codex' | 'image.shotforward')
                                }
                              />
                              <AssetReferenceControls
                                projectID={projectId}
                                episodeID={episodeId}
                                values={referenceSelection}
                                maximum={maximumReferences}
                                disabled={
                                  !media.data ||
                                  generateImage.isPending ||
                                  imageTaskActive ||
                                  referenceUploading
                                }
                                onChange={setReferenceSelection}
                                onOpen={() => setReferenceOpen(true)}
                              />
                              <button
                                type="button"
                                className="button button--primary asset-generate-action"
                                disabled={
                                  media.isPending ||
                                  generateImage.isPending ||
                                  imageTaskActive ||
                                  !media.data ||
                                  !activeImageGenerator ||
                                  assetDraftDirty ||
                                  referenceUploading ||
                                  referenceSelection.length > maximumReferences ||
                                  (!selected.prompt.trim() && !referenceSelection.length)
                                }
                                onClick={() => generateImage.mutate()}
                              >
                                <span aria-hidden="true">✦</span>
                                {generateImage.isPending
                                  ? t('assets.generatingImage')
                                  : t('assets.generateImage')}
                              </button>
                            </div>
                            <AssetReferenceLimitWarning
                              count={referenceSelection.length}
                              maximum={maximumReferences}
                            />
                            <p className="asset-generation-note">
                              {assetDraftDirty
                                ? t('assets.saveBeforeGenerate')
                                : referenceSelection.length
                                  ? t('assets.withReferenceHint')
                                  : t('assets.withoutReferenceHint')}
                            </p>
                            {imageTask?.state === 'succeeded' && (
                              <p className="asset-generation-note is-success">
                                {t('assets.imageSucceeded')} <Link to="/tasks">{t('assets.openTasks')}</Link>
                              </p>
                            )}
                            {(generateImage.isError || imageTask?.state === 'failed') && (
                              <div className="form-error asset-generation-error" role="alert">
                                <span>
                                  {imageTask?.state === 'failed'
                                    ? taskFailureMessage(imageTask, t, i18n.resolvedLanguage)
                                    : (businessErrorMessage(businessErrorCode(generateImage.error), t) ??
                                      generationRequestErrorMessage(
                                        generateImage.error,
                                        t('assets.imageGenerationFailed'),
                                      ))}
                                </span>
                                <RechargeAction
                                  code={
                                    imageTask?.state === 'failed'
                                      ? imageTask.failure_code
                                      : businessErrorCode(generateImage.error)
                                  }
                                />
                                {imageTask?.state === 'failed' && (
                                  <Link to="/tasks">{t('assets.openTasks')}</Link>
                                )}
                              </div>
                            )}
                          </div>
                        }
                      />
                    </div>
                  </section>
                </>
              ) : null}
            </>
          ) : (
            <UploadedMediaLibrary
              projectID={projectId}
              episodeID={episodeId}
              values={referenceMedia.data?.reference_media ?? []}
              loading={referenceMedia.isPending}
              error={referenceMedia.isError}
              onRefresh={() => referenceMedia.refetch()}
              onReference={referenceMediaWithAgent}
            />
          )}
        </main>
        <AgentDock
          projectID={projectId}
          episodeID={episodeId}
          context="assets"
          contextTitle={`${episode.data.project.name} / ${episode.data.episode.title}`}
          focus={selected ? { asset_id: selected.asset_id } : {}}
          {...(agentDraftRequest ? { draftRequest: agentDraftRequest } : {})}
          onDraftApplied={(id) =>
            setAgentDraftRequest((current) => (current?.id === id ? undefined : current))
          }
        />
      </div>
      {selected && (
        <AssetReferencePicker
          open={referenceOpen}
          projectID={projectId}
          episodeID={episodeId}
          asset={selected}
          media={media.data}
          value={referenceSelection}
          maximumReferences={maximumReferences}
          onUploadBusyChange={setReferenceUploading}
          onClose={() => setReferenceOpen(false)}
          onSelect={setReferenceSelection}
        />
      )}
      <Dialog open={createOpen} label={t('assets.createTitle')} onClose={() => setCreateOpen(false)}>
        <form
          className="dialog-panel"
          onSubmit={(event) => {
            event.preventDefault();
            if (createName.trim()) create.mutate();
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{episode.data.episode.title}</p>
              <h2>{t('assets.createTitle')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setCreateOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <label className="field">
              {t('assets.kind')}
              <select value={createKind} onChange={(event) => setCreateKind(event.target.value as AssetKind)}>
                {kinds.map((item) => (
                  <option key={item} value={item}>
                    {t(`assets.kinds.${item}`)}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              {t('assets.name')}
              <input
                required
                autoFocus
                value={createName}
                onChange={(event) => setCreateName(event.target.value)}
              />
            </label>
            <label className="field">
              {t('assets.prompt')}
              <textarea
                value={createPrompt}
                placeholder={t('assets.promptPlaceholder')}
                onChange={(event) => setCreatePrompt(event.target.value)}
              />
            </label>
            <small>{t('assets.episodeScope')}</small>
            {create.isError && <p className="form-error">{t('assets.createFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button type="button" className="button button--secondary" onClick={() => setCreateOpen(false)}>
              {t('common.cancel')}
            </button>
            <button className="button" disabled={!createName.trim() || create.isPending}>
              {create.isPending ? t('common.loading') : t('assets.create')}
            </button>
          </footer>
        </form>
      </Dialog>
      <Dialog open={mediaOpen} label={t('assets.importImage')} onClose={() => setMediaOpen(false)}>
        <form
          className="dialog-panel"
          onSubmit={(event) => {
            event.preventDefault();
            importImage.mutate();
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{selected?.identity.name}</p>
              <h2>{t('assets.importImage')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setMediaOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <label className="field asset-reference-file">
              {t('assets.imageFile')}
              <input
                type="file"
                required
                accept="image/jpeg,image/png,image/webp"
                onChange={(event) => setMediaFile(event.target.files?.[0])}
              />
              <small>{mediaFile?.name || t('assets.imageFileHint')}</small>
            </label>
            {importImage.isError && <p className="form-error">{t('assets.importFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button type="button" className="button button--secondary" onClick={() => setMediaOpen(false)}>
              {t('common.cancel')}
            </button>
            <button className="button" disabled={!media.data || importImage.isPending || !mediaFile}>
              {importImage.isPending ? t('common.loading') : t('assets.import')}
            </button>
          </footer>
        </form>
      </Dialog>
      <Dialog open={deleteOpen} label={t('assets.deleteTitle')} onClose={() => setDeleteOpen(false)}>
        <div className="dialog-panel">
          <header className="dialog-header">
            <div>
              <p>{selected?.identity.name}</p>
              <h2>{t('assets.deleteTitle')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setDeleteOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <p>{t('assets.deleteHint')}</p>
            <p className="info-note">{t('assets.deleteReferenceHint')}</p>
            {remove.isError && <p className="form-error">{t('assets.deleteFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button className="button button--secondary" onClick={() => setDeleteOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending ? t('assets.deleting') : t('assets.deleteConfirm')}
            </button>
          </footer>
        </div>
      </Dialog>
      <Dialog
        open={discardOpen}
        label={t('assets.discardTitle')}
        onClose={() => {
          setDiscardOpen(false);
          setDiscardTargetID('');
        }}
      >
        <div className="dialog-panel">
          <header className="dialog-header">
            <div>
              <p>{selected?.identity.name}</p>
              <h2>{t('assets.discardTitle')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => {
                setDiscardOpen(false);
                setDiscardTargetID('');
              }}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <p>{t('assets.discardHint')}</p>
          </div>
          <footer className="dialog-footer">
            <button
              type="button"
              className="button button--secondary"
              onClick={() => {
                setDiscardOpen(false);
                setDiscardTargetID('');
              }}
            >
              {t('common.cancel')}
            </button>
            <button
              type="button"
              className="button button--danger"
              onClick={() => completeAssetSelection(discardTargetID)}
            >
              {t('assets.discardConfirm')}
            </button>
          </footer>
        </div>
      </Dialog>
      <ImageLightbox value={previewImage} onClose={() => setPreviewImage(undefined)} />
    </div>
  );
}

function UploadedMediaLibrary({
  projectID,
  episodeID,
  values,
  loading,
  error,
  onRefresh,
  onReference,
}: {
  projectID: string;
  episodeID: string;
  values: EpisodeReferenceMedia[];
  loading: boolean;
  error: boolean;
  onRefresh: () => Promise<unknown>;
  onReference: (value: EpisodeReferenceMedia) => void;
}) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<'all' | 'image' | 'video' | 'audio'>('all');
  const [search, setSearch] = useState('');
  const [selectedID, setSelectedID] = useState('');
  const [preview, setPreview] = useState<MediaPreviewValue>();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [trimStart, setTrimStart] = useState(0);
  const [trimEnd, setTrimEnd] = useState(0);
  const selected = values.find((item) => item.artifact.artifact_id === selectedID);
  const visible = values.filter(
    (item) =>
      (kind === 'all' || item.artifact.kind === kind) &&
      `${item.name} ${item.artifact.original_filename}`
        .toLocaleLowerCase()
        .includes(search.trim().toLocaleLowerCase()),
  );
  const upload = useMutation({
    mutationFn: async (files: File[]) => {
      for (const file of files) {
        const mediaKind = referenceMediaKindForFile(file);
        await uploadEpisodeReferenceMedia(projectID, episodeID, file, mediaKind);
      }
    },
    onSuccess: async () => {
      await onRefresh();
    },
  });
  const update = useMutation({
    mutationFn: () => {
      const args = [
        projectID,
        episodeID,
        selected!.artifact.artifact_id,
        name.trim(),
        description,
        selected!.revision,
      ] as const;
      return selected!.artifact.kind === 'image'
        ? updateEpisodeReferenceMedia(...args)
        : updateEpisodeReferenceMedia(
            ...args,
            savedMediaTrim(trimStart, trimEnd, selected!.artifact.duration_ms),
          );
    },
    onSuccess: async ({ reference_media }) => {
      setSelectedID(reference_media.artifact.artifact_id);
      await onRefresh();
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      deleteEpisodeReferenceMedia(projectID, episodeID, selected!.artifact.artifact_id, selected!.revision),
    onSuccess: async () => {
      setDeleteOpen(false);
      setSelectedID('');
      await onRefresh();
    },
  });
  useEffect(() => {
    setName(selected?.name ?? '');
    setDescription(selected?.description ?? '');
    const range = selected ? mediaTrimRange(selected.artifact, selected) : { start: 0, end: 0 };
    setTrimStart(range.start);
    setTrimEnd(range.end);
  }, [selected]);
  const selectedRange = selected ? mediaTrimRange(selected.artifact, selected) : undefined;
  const trimValid =
    !selected ||
    selected.artifact.kind === 'image' ||
    validMediaTrim(trimStart, trimEnd, selected.artifact.duration_ms);
  const dirty = Boolean(
    selected &&
    (name !== selected.name ||
      description !== selected.description ||
      (selected.artifact.kind !== 'image' &&
        (trimStart !== selectedRange?.start || trimEnd !== selectedRange?.end))),
  );
  const previewItem = values.find((item) => item.artifact.artifact_id === preview?.artifact.artifact_id);

  return (
    <section className="uploaded-media-library">
      <header className="uploaded-media-library__header">
        <div>
          <h2>{t('assets.uploadedMedia')}</h2>
          <p>{t('assets.uploadedMediaDescription')}</p>
        </div>
        <label className="search-field asset-search">
          <SearchIcon />
          <input
            aria-label={t('assets.searchUploadedMedia')}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('assets.searchUploadedMedia')}
          />
        </label>
        <label className={`button uploaded-media-upload${upload.isPending ? ' is-disabled' : ''}`}>
          <span aria-hidden="true">＋</span> {t('assets.uploadMedia')}
          <input
            type="file"
            multiple
            disabled={upload.isPending}
            accept="image/jpeg,image/png,image/webp,audio/mpeg,audio/wav,audio/mp4,video/mp4,video/quicktime,video/webm"
            aria-label={t('assets.uploadMedia')}
            onChange={(event) => {
              const files = Array.from(event.target.files ?? []);
              event.target.value = '';
              if (files.length) upload.mutate(files);
            }}
          />
        </label>
      </header>
      <nav className="uploaded-media-filters" aria-label={t('assets.mediaTypeFilter')}>
        {(['all', 'image', 'video', 'audio'] as const).map((item) => (
          <button key={item} className={kind === item ? 'active' : ''} onClick={() => setKind(item)}>
            {t(`assets.uploadKinds.${item}`)}
            <span>
              {item === 'all' ? values.length : values.filter((value) => value.artifact.kind === item).length}
            </span>
          </button>
        ))}
      </nav>
      {upload.isError && (
        <p className="form-error uploaded-media-library__error">{t('assets.uploadMediaFailed')}</p>
      )}
      {error ? (
        <section className="asset-empty-state">
          <div>
            <strong>{t('assets.uploadedMediaLoadFailed')}</strong>
            <button className="button button--secondary" onClick={() => void onRefresh()}>
              {t('common.retry')}
            </button>
          </div>
        </section>
      ) : loading ? (
        <section className="asset-empty-state">
          <div>
            <strong>{t('common.loading')}</strong>
          </div>
        </section>
      ) : visible.length ? (
        <div className="uploaded-media-grid">
          {visible.map((item) => (
            <article
              key={item.artifact.artifact_id}
              className={item.artifact.artifact_id === selectedID ? 'active' : ''}
            >
              <div className="uploaded-media-card__preview">
                <MediaThumbnailButton
                  projectID={projectID}
                  artifact={item.artifact}
                  trim={item}
                  title={item.name}
                  onPreview={() =>
                    setPreview({
                      artifact: item.artifact,
                      title: item.name,
                      trim_start_ms: item.trim_start_ms ?? 0,
                      trim_end_ms: item.trim_end_ms ?? 0,
                    })
                  }
                />
              </div>
              <button
                type="button"
                className="uploaded-media-card__body"
                onClick={() => setSelectedID(item.artifact.artifact_id)}
              >
                <strong title={item.name}>{item.name}</strong>
                <small>
                  {t(`assets.uploadKinds.${item.artifact.kind}`)}
                  {mediaSpecification(item.artifact, item)}
                </small>
                <span>{t('assets.mediaUsageCount', { count: item.usage_count })}</span>
              </button>
              <button
                type="button"
                className="asset-agent-reference"
                title={t('assets.referenceWithAgent', { name: item.name })}
                aria-label={t('assets.referenceToAgent')}
                onClick={() => onReference(item)}
              >
                <span aria-hidden="true">@</span> {t('assets.referenceToAgent')}
              </button>
            </article>
          ))}
        </div>
      ) : (
        <section className="asset-empty-state" role="status">
          <div>
            <span className="asset-empty-state__mark" aria-hidden="true">
              ＋
            </span>
            <strong>{values.length ? t('assets.noMatchingMedia') : t('assets.noUploadedMedia')}</strong>
            <p>{values.length ? t('assets.clearMediaFiltersHint') : t('assets.noUploadedMediaHint')}</p>
          </div>
        </section>
      )}
      {selected && (
        <>
          <button
            type="button"
            className="asset-detail-backdrop"
            aria-label={t('common.close')}
            onClick={() => setSelectedID('')}
          />
          <section
            className="asset-detail uploaded-media-detail"
            role="dialog"
            aria-modal="true"
            aria-labelledby="uploaded-media-detail-title"
          >
            <header>
              <div>
                <span className="eyebrow">{t(`assets.uploadKinds.${selected.artifact.kind}`)}</span>
                <h1 id="uploaded-media-detail-title">{selected.name}</h1>
              </div>
              <button
                type="button"
                className="icon-button asset-detail__close"
                aria-label={t('common.close')}
                onClick={() => setSelectedID('')}
              >
                ×
              </button>
            </header>
            <div className="uploaded-media-detail__content">
              {selected.artifact.kind === 'image' ? (
                <div className="uploaded-media-detail__preview">
                  <MediaThumbnailButton
                    projectID={projectID}
                    artifact={selected.artifact}
                    title={selected.name}
                    onPreview={() => setPreview({ artifact: selected.artifact, title: selected.name })}
                  />
                </div>
              ) : (
                <MediaTrimEditor
                  projectID={projectID}
                  artifact={selected.artifact}
                  start={trimStart}
                  end={trimEnd}
                  onChange={(start, end) => {
                    setTrimStart(start);
                    setTrimEnd(end);
                  }}
                />
              )}
              <label className="field">
                <span>{t('assets.mediaName')}</span>
                <input maxLength={40} value={name} onChange={(event) => setName(event.target.value)} />
              </label>
              <label className="field">
                <span>{t('assets.mediaDescription')}</span>
                <textarea
                  rows={5}
                  maxLength={2000}
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder={t('assets.mediaDescriptionPlaceholder')}
                />
              </label>
              <dl className="uploaded-media-metadata">
                <div>
                  <dt>{t('assets.originalFilename')}</dt>
                  <dd title={selected.artifact.original_filename}>{selected.artifact.original_filename}</dd>
                </div>
                {selected.artifact.width > 0 && (
                  <div>
                    <dt>{t('assets.resolution')}</dt>
                    <dd>
                      {selected.artifact.width} × {selected.artifact.height}
                    </dd>
                  </div>
                )}
                {selected.artifact.duration_ms > 0 && (
                  <div>
                    <dt>{t('assets.durationShort')}</dt>
                    <dd>{formatUploadedDuration(selected.artifact.duration_ms)}</dd>
                  </div>
                )}
                <div>
                  <dt>{t('assets.fileSize')}</dt>
                  <dd>{formatBytes(selected.artifact.byte_size)}</dd>
                </div>
                <div>
                  <dt>{t('assets.usage')}</dt>
                  <dd>{t('assets.mediaUsageCount', { count: selected.usage_count })}</dd>
                </div>
              </dl>
              <p className="info-note">{t('assets.chooseMediaFromClipHint')}</p>
              {update.isError && <p className="form-error">{t('assets.updateMediaFailed')}</p>}
            </div>
            <footer className="uploaded-media-detail__footer">
              <button className="button button--danger" type="button" onClick={() => setDeleteOpen(true)}>
                {t('assets.deleteMedia')}
              </button>
              <button
                className="button"
                type="button"
                disabled={!dirty || !trimValid || !name.trim() || update.isPending}
                onClick={() => update.mutate()}
              >
                {update.isPending ? t('assets.saving') : t('assets.save')}
              </button>
            </footer>
          </section>
        </>
      )}
      <MediaPreviewDialog
        projectID={projectID}
        value={
          preview && previewItem
            ? {
                artifact: previewItem.artifact,
                title: previewItem.name,
                trim_start_ms: previewItem.trim_start_ms ?? 0,
                trim_end_ms: previewItem.trim_end_ms ?? 0,
              }
            : preview
        }
        onClose={() => setPreview(undefined)}
      />
      <Dialog open={deleteOpen} label={t('assets.deleteMedia')} onClose={() => setDeleteOpen(false)}>
        <section className="dialog-panel">
          <header className="dialog-header">
            <div>
              <p>{selected?.name}</p>
              <h2>{t('assets.deleteMedia')}</h2>
            </div>
            <button
              className="icon-button"
              type="button"
              aria-label={t('common.close')}
              onClick={() => setDeleteOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <p>{t('assets.deleteMediaHint', { count: selected?.usage_count ?? 0 })}</p>
            {remove.isError && <p className="form-error">{t('assets.deleteMediaFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button className="button button--secondary" onClick={() => setDeleteOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t('assets.deleteMedia')}
            </button>
          </footer>
        </section>
      </Dialog>
    </section>
  );
}

function referenceMediaKindForFile(file: Pick<File, 'name' | 'type'>): 'image' | 'video' | 'audio' {
  const extension = file.name.toLocaleLowerCase().split('.').pop() ?? '';
  if (file.type.startsWith('image/') || ['jpg', 'jpeg', 'png', 'webp'].includes(extension)) return 'image';
  if (file.type.startsWith('video/') || ['mp4', 'mov', 'webm'].includes(extension)) return 'video';
  if (file.type.startsWith('audio/') || ['mp3', 'wav', 'm4a'].includes(extension)) return 'audio';
  throw new Error('unsupported reference media');
}

function mediaSpecification(artifact: EpisodeReferenceMedia['artifact'], trim?: EpisodeReferenceMedia) {
  if (artifact.kind === 'image') return ` · ${artifact.width} × ${artifact.height}`;
  return ` · ${formatUploadedDuration(mediaTrimRange(artifact, trim).duration)}`;
}

function formatUploadedDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

function formatBytes(value: number) {
  if (value < 1024 * 1024) return `${Math.max(1, Math.round(value / 1024))} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}

function AssetCardContent({
  projectID,
  asset,
  generationState,
}: {
  projectID: string;
  asset: EpisodeAsset;
  generationState?: TaskState | undefined;
}) {
  const { t } = useTranslation();
  const media = useQuery({
    queryKey: ['media-slot', projectID, asset.primary_image_slot_id],
    queryFn: () => getMediaSlot(projectID, asset.primary_image_slot_id),
    enabled: Boolean(projectID && asset.primary_image_slot_id),
    staleTime: 30_000,
  });
  const version = media.data?.versions.find(
    (item) => item.media_version_id === media.data.slot.current_media_version_id,
  );
  const artifact = media.data?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
  const generationLabel = generationState
    ? t(
        generationState === 'running'
          ? 'assets.imageRunning'
          : generationState === 'stop_requested'
            ? 'assets.imageStopping'
            : 'assets.imageQueued',
      )
    : '';
  return (
    <>
      <span
        className={`asset-thumb asset-thumb--${asset.identity.kind}${artifact ? ' asset-thumb--fitted' : ''}`}
      >
        {artifact ? (
          <img
            src={artifactContentURL(projectID, artifact.artifact_id)}
            alt={asset.identity.name}
            style={{ objectFit: 'contain', objectPosition: 'center' }}
          />
        ) : asset.primary_image_slot_id && media.isPending ? (
          <span className="asset-thumb__loading skeleton" aria-hidden="true" />
        ) : (
          <span className="asset-thumb__empty">
            <AssetKindIcon kind={asset.identity.kind} />
            <small>{t('assets.noImage')}</small>
          </span>
        )}
        {generationState && (
          <span className="asset-thumb__generation-state" role="status" aria-label={generationLabel}>
            <span className="spinner" aria-hidden="true" />
            <small>{generationLabel}</small>
          </span>
        )}
      </span>
      <span className="asset-card__meta">
        <strong className="asset-card__name" title={asset.identity.name}>
          {asset.identity.name}
        </strong>
        <span className="asset-card__details">
          <small className="asset-card__kind">{t(`assets.kinds.${asset.identity.kind}`)}</small>
        </span>
      </span>
    </>
  );
}

function generationRequestErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error) {
    const message = error.message.trim();
    if (message) return message;
  }
  return fallback;
}

function AssetEditor({
  asset,
  busy,
  error,
  onDirtyChange,
  onSave,
  onEdit,
  onAgentEditPrompt,
  generationControls,
}: {
  asset: EpisodeAsset;
  busy: boolean;
  error: boolean;
  onDirtyChange: (dirty: boolean) => void;
  onSave: (name: string, prompt: string) => void;
  onEdit: () => void;
  onAgentEditPrompt: () => void;
  generationControls: ReactNode;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(asset.identity.name);
  const [prompt, setPrompt] = useState(asset.prompt);
  const previousAsset = useRef(asset);
  const onSaveRef = useRef(onSave);
  const normalizedName = name.trim();
  const dirty = normalizedName !== asset.identity.name || prompt !== asset.prompt;

  useEffect(() => {
    onSaveRef.current = onSave;
  }, [onSave]);

  useEffect(() => {
    const previous = previousAsset.current;
    if (previous.asset_id !== asset.asset_id) {
      setName(asset.identity.name);
      setPrompt(asset.prompt);
    } else {
      setName((current) => (current === previous.identity.name ? asset.identity.name : current));
      setPrompt((current) => (current === previous.prompt ? asset.prompt : current));
    }
    previousAsset.current = asset;
  }, [asset]);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);

  useEffect(() => {
    if (!dirty || busy || error || !normalizedName) return;
    const timer = window.setTimeout(() => onSaveRef.current(normalizedName, prompt), 900);
    return () => window.clearTimeout(timer);
  }, [busy, dirty, error, normalizedName, prompt]);

  return (
    <section className="asset-editor">
      <label className="field">
        {t('assets.name')}
        <input
          value={name}
          required
          onChange={(event) => {
            onEdit();
            setName(event.target.value);
          }}
        />
      </label>
      <div className="field field--grow">
        <div className="asset-field-heading">
          <label htmlFor={`asset-prompt-${asset.asset_id}`}>{t('assets.prompt')}</label>
          <AgentPromptActionButton onClick={onAgentEditPrompt} disabled={dirty || busy}>
            {t('assets.agentEditPrompt')}
          </AgentPromptActionButton>
        </div>
        <textarea
          id={`asset-prompt-${asset.asset_id}`}
          value={prompt}
          rows={12}
          onChange={(event) => {
            onEdit();
            setPrompt(truncateUnicode(event.target.value, 2000));
          }}
          placeholder={t('assets.promptPlaceholder')}
        />
      </div>
      {generationControls}
      <p className="asset-scope-note">{t('assets.episodeScope')}</p>
      <p className={`asset-autosave-state${error ? ' is-error' : ''}`} role={error ? 'alert' : undefined}>
        {error
          ? t('assets.saveFailed')
          : busy
            ? t('assets.saving')
            : dirty
              ? t('assets.waitingToSave')
              : t('assets.saved')}
      </p>
    </section>
  );
}
