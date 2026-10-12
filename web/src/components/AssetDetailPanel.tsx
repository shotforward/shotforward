import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  getMediaSlot,
  renameAssetIdentity,
  selectMediaVersion,
  updateAssetPrompt,
  uploadMedia,
  type EpisodeAsset,
} from '../api/assets';
import {
  checkGenerationReadiness,
  submitGeneration,
  type GenerationJob,
  type GenerationTarget,
} from '../api/generation';
import type { TaskItem } from '../api/tasks';
import { AgentPromptActionButton } from './AgentPromptActionButton';
import { AssetReferencePicker, type AssetReferenceSelection } from './AssetReferencePicker';
import { AssetReferenceControls, AssetReferenceLimitWarning } from './AssetReferenceControls';
import { imageReferenceMaximum, imageReferenceLimitMessage } from '../assetImageReferences';
import { Dialog } from './Dialog';
import { ImageLightbox, ImagePreviewButton, type ImagePreviewValue } from './ImagePreview';
import { PlusIcon } from './Icons';
import { listProviders } from '../api/providers';
import { setImageGeneratorPreference, useImageGeneratorPreference } from '../preferences';
import { Select } from './Select';

function constrainedImagePreviewStyle(width: number, height: number): CSSProperties | undefined {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return undefined;
  }

  const ratio = width / height;
  const viewportWidth = Number((ratio * 52).toFixed(4));
  const maximumWidth = Number((ratio * 520).toFixed(2));
  return {
    aspectRatio: `${width} / ${height}`,
    width: `min(100%, ${viewportWidth}dvh, ${maximumWidth}px)`,
  };
}

interface AssetDetailPanelProps {
  projectID: string;
  episodeID: string;
  asset: EpisodeAsset;
  generationTask?: TaskItem;
  onClose: () => void;
  onAssetRenamed?: (previousName: string, nextName: string) => void;
  onAgentEditPrompt?: () => void;
}

const activeTaskStates = ['queued', 'running', 'stop_requested'] as const;

export function AssetDetailPanel({
  projectID,
  episodeID,
  asset,
  generationTask,
  onClose,
  onAssetRenamed,
  onAgentEditPrompt,
}: AssetDetailPanelProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const imageGenerator = useImageGeneratorPreference();
  const providers = useQuery({ queryKey: ['generation-providers'], queryFn: listProviders });
  const cloudImage = providers.data?.providers.find((item) => item.generator_id === 'image.shotforward');
  const cloudImageModel =
    cloudImage?.models?.find((item) => item.is_default)?.model_id ?? cloudImage?.model_id;
  const maximumReferences = imageReferenceMaximum(
    providers.data?.providers.find((item) => item.generator_id === imageGenerator),
    imageGenerator === 'image.shotforward' ? cloudImageModel : undefined,
  );
  const [name, setName] = useState(asset.identity.name);
  const [prompt, setPrompt] = useState(asset.prompt);
  const [mediaOpen, setMediaOpen] = useState(false);
  const [mediaFile, setMediaFile] = useState<File>();
  const [referenceOpen, setReferenceOpen] = useState(false);
  const [referenceSelection, setReferenceSelection] = useState<AssetReferenceSelection[]>([]);
  const [referenceUploading, setReferenceUploading] = useState(false);
  const [previewImage, setPreviewImage] = useState<ImagePreviewValue>();
  const [submittedJob, setSubmittedJob] = useState<GenerationJob>();
  const previousAsset = useRef(asset);
  const normalizedName = name.trim();
  const dirty = normalizedName !== asset.identity.name || prompt !== asset.prompt;

  const media = useQuery({
    queryKey: ['media-slot', projectID, asset.primary_image_slot_id],
    queryFn: () => getMediaSlot(projectID, asset.primary_image_slot_id),
    enabled: Boolean(projectID && asset.primary_image_slot_id),
  });
  const currentVersion = media.data?.versions.find(
    (version) => version.media_version_id === media.data.slot.current_media_version_id,
  );
  const currentArtifact = media.data?.artifacts.find(
    (artifact) => artifact.artifact_id === currentVersion?.artifact_id,
  );
  const mediaVersions = media.data?.versions.filter((version) => !version.deleted_at) ?? [];
  const mediaArtifacts = media.data?.artifacts ?? [];
  const currentMediaVersionID = media.data?.slot.current_media_version_id;
  const submittedTaskID = submittedJob?.generation_job_id;
  const effectiveTask =
    submittedJob && generationTask?.task_id !== submittedTaskID
      ? submittedJob
      : (generationTask ?? submittedJob);
  const effectiveTaskID = generationTask?.task_id ?? submittedJob?.generation_job_id;
  const taskActive = Boolean(
    effectiveTask && activeTaskStates.includes(effectiveTask.state as (typeof activeTaskStates)[number]),
  );

  const updateDetails = useMutation({
    mutationFn: async ({ nextName, nextPrompt }: { nextName: string; nextPrompt: string }) => {
      if (nextName !== asset.identity.name) {
        await renameAssetIdentity(
          projectID,
          asset.identity.asset_identity_id,
          nextName,
          asset.identity.revision,
        );
      }
      if (nextPrompt !== asset.prompt) {
        await updateAssetPrompt(projectID, asset.asset_id, nextPrompt, asset.revision);
      }
      return { previousName: asset.identity.name, nextName };
    },
    onSuccess: async ({ previousName, nextName }) => {
      await queryClient.invalidateQueries({ queryKey: ['assets', projectID, episodeID] });
      if (previousName !== nextName) onAssetRenamed?.(previousName, nextName);
    },
  });
  const importImage = useMutation({
    mutationFn: () =>
      uploadMedia(projectID, asset.primary_image_slot_id, mediaFile!, media.data!.slot.revision),
    onSuccess: (result) => {
      queryClient.setQueryData(['media-slot', projectID, asset.primary_image_slot_id], result);
      setMediaFile(undefined);
      setMediaOpen(false);
    },
  });
  const selectVersion = useMutation({
    mutationFn: (versionID: string) =>
      selectMediaVersion(projectID, asset.primary_image_slot_id, versionID, media.data!.slot.revision),
    onSuccess: (result) =>
      queryClient.setQueryData(['media-slot', projectID, asset.primary_image_slot_id], result),
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
        media_slot_id: asset.primary_image_slot_id,
        expected_slot_revision: media.data!.slot.revision,
        prompt: asset.prompt,
        generator_id: imageGenerator,
        ...(imageGenerator === 'image.shotforward' && cloudImageModel
          ? { provider_model_id: cloudImageModel }
          : {}),
        references,
        parameters: {},
      };
      await checkGenerationReadiness(projectID, episodeID, target);
      return submitGeneration(projectID, episodeID, target);
    },
    onSuccess: async (result) => {
      setSubmittedJob(result.jobs[0]);
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
  });

  useEffect(() => {
    const previous = previousAsset.current;
    if (previous.asset_id !== asset.asset_id) {
      setName(asset.identity.name);
      setPrompt(asset.prompt);
      setSubmittedJob(undefined);
      setReferenceSelection([]);
      setReferenceOpen(false);
    } else {
      setName((current) => (current === previous.identity.name ? asset.identity.name : current));
      setPrompt((current) => (current === previous.prompt ? asset.prompt : current));
    }
    previousAsset.current = asset;
  }, [asset]);

  useEffect(() => {
    if (!dirty || updateDetails.isPending || updateDetails.isError || !normalizedName) return;
    const timer = window.setTimeout(
      () => updateDetails.mutate({ nextName: normalizedName, nextPrompt: prompt }),
      900,
    );
    return () => window.clearTimeout(timer);
  }, [dirty, normalizedName, prompt, updateDetails.isError, updateDetails.isPending, updateDetails.mutate]);

  useEffect(() => {
    if (effectiveTask?.state !== 'succeeded') return;
    void media.refetch();
  }, [effectiveTask?.state, effectiveTaskID, media.refetch]);

  const requestClose = () => {
    if (updateDetails.isPending) return;
    if (dirty && !window.confirm(t('assets.discardHint'))) return;
    onClose();
  };
  return (
    <>
      <button
        type="button"
        className="asset-detail-backdrop storyboard-asset-detail-backdrop"
        aria-label={t('common.close')}
        onClick={requestClose}
      />
      <section
        className="asset-detail storyboard-asset-detail"
        role="dialog"
        aria-modal="true"
        aria-labelledby="storyboard-asset-detail-title"
      >
        <header>
          <div>
            <span className="eyebrow">{t(`assets.kinds.${asset.identity.kind}`)}</span>
            <h1 id="storyboard-asset-detail-title">{asset.identity.name}</h1>
          </div>
          <div className="asset-detail__controls">
            <span className="revision">v{asset.revision}</span>
            <button
              type="button"
              className="icon-button asset-detail__close"
              aria-label={t('common.close')}
              onClick={requestClose}
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
                  src={artifactContentURL(projectID, currentArtifact.artifact_id)}
                  alt={asset.identity.name}
                  onPreview={() =>
                    setPreviewImage({
                      src: artifactContentURL(projectID, currentArtifact.artifact_id),
                      title: asset.identity.name,
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
              {(generateImage.isPending || taskActive) && (
                <div className="asset-generation-overlay" role="status">
                  <span className="spinner" />
                  <strong>{t('assets.imageRunning')}</strong>
                </div>
              )}
            </div>
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
                  disabled={!media.data || taskActive}
                >
                  <PlusIcon />
                  {t('assets.importImage')}
                </button>
              </header>
              {media.isError ? (
                <p className="form-error">{t('assets.mediaFailed')}</p>
              ) : (
                <div className={mediaVersions.length ? undefined : 'version-strip__versions--empty'}>
                  {mediaVersions.length ? (
                    mediaVersions.map((version) => {
                      const artifact = mediaArtifacts.find(
                        (item) => item.artifact_id === version.artifact_id,
                      );
                      return (
                        <button
                          type="button"
                          key={version.media_version_id}
                          className={version.media_version_id === currentMediaVersionID ? 'active' : ''}
                          onClick={() => selectVersion.mutate(version.media_version_id)}
                          disabled={selectVersion.isPending || taskActive}
                        >
                          <span>
                            {artifact ? (
                              <img src={artifactContentURL(projectID, artifact.artifact_id)} alt="" />
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
          <section className="asset-editor">
            <label className="field">
              {t('assets.name')}
              <input
                value={name}
                required
                onChange={(event) => {
                  updateDetails.reset();
                  setName(event.target.value);
                }}
              />
            </label>
            <div className="field field--grow">
              <div className="asset-field-heading">
                <label htmlFor={`storyboard-asset-prompt-${asset.asset_id}`}>{t('assets.prompt')}</label>
                {onAgentEditPrompt && (
                  <AgentPromptActionButton
                    onClick={onAgentEditPrompt}
                    disabled={dirty || updateDetails.isPending}
                  >
                    {t('assets.agentEditPrompt')}
                  </AgentPromptActionButton>
                )}
              </div>
              <textarea
                id={`storyboard-asset-prompt-${asset.asset_id}`}
                value={prompt}
                maxLength={2000}
                placeholder={t('assets.promptPlaceholder')}
                onChange={(event) => {
                  updateDetails.reset();
                  setPrompt(event.target.value);
                }}
              />
            </div>
            <p className="asset-scope-note">{t('assets.episodeScope')}</p>
            <p className={`asset-autosave-state${updateDetails.isError ? ' is-error' : ''}`}>
              {t(
                updateDetails.isError
                  ? 'assets.saveFailed'
                  : updateDetails.isPending
                    ? 'assets.saving'
                    : dirty
                      ? 'assets.waitingToSave'
                      : 'assets.saved',
              )}
            </p>
            <div className="asset-prompt-generation">
              <div className="asset-prompt-generation__actions">
                <Select
                  ariaLabel="Image provider"
                  value={imageGenerator}
                  options={[
                    { value: 'image.codex', label: 'Codex subscription' },
                    { value: 'image.shotforward', label: 'ShotForward Cloud' },
                  ]}
                  compact
                  disabled={generateImage.isPending || taskActive}
                  onChange={(value) =>
                    setImageGeneratorPreference(value as 'image.codex' | 'image.shotforward')
                  }
                />
                <AssetReferenceControls
                  projectID={projectID}
                  episodeID={episodeID}
                  values={referenceSelection}
                  maximum={maximumReferences}
                  disabled={!media.data || generateImage.isPending || taskActive || referenceUploading}
                  onChange={setReferenceSelection}
                  onOpen={() => setReferenceOpen(true)}
                />
                <button
                  type="button"
                  className="button"
                  disabled={
                    !media.data ||
                    dirty ||
                    updateDetails.isPending ||
                    generateImage.isPending ||
                    taskActive ||
                    referenceUploading ||
                    referenceSelection.length > maximumReferences ||
                    (!asset.prompt.trim() && !referenceSelection.length)
                  }
                  onClick={() => generateImage.mutate()}
                >
                  {generateImage.isPending ? t('assets.generatingImage') : t('assets.generateImage')}
                </button>
              </div>
              <AssetReferenceLimitWarning count={referenceSelection.length} maximum={maximumReferences} />
              <p className="asset-generation-note">
                {dirty
                  ? t('assets.saveBeforeGenerate')
                  : referenceSelection.length
                    ? t('assets.withReferenceHint')
                    : t('assets.withoutReferenceHint')}
              </p>
              {effectiveTask?.state === 'succeeded' && (
                <p className="asset-generation-note is-success">
                  {t('assets.imageSucceeded')} <Link to="/tasks">{t('assets.openTasks')}</Link>
                </p>
              )}
              {(generateImage.isError || effectiveTask?.state === 'failed') && (
                <p className="form-error">
                  {t('assets.imageGenerationFailed')} <Link to="/tasks">{t('assets.openTasks')}</Link>
                </p>
              )}
            </div>
          </section>
        </div>
      </section>
      <AssetReferencePicker
        open={referenceOpen}
        projectID={projectID}
        episodeID={episodeID}
        asset={asset}
        media={media.data}
        value={referenceSelection}
        maximumReferences={maximumReferences}
        onUploadBusyChange={setReferenceUploading}
        onClose={() => setReferenceOpen(false)}
        onSelect={setReferenceSelection}
      />
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
              <p>{asset.identity.name}</p>
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
            <label className="field">
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
            <button className="button" disabled={!mediaFile || importImage.isPending}>
              {importImage.isPending ? t('common.loading') : t('assets.import')}
            </button>
          </footer>
        </form>
      </Dialog>
      <ImageLightbox value={previewImage} onClose={() => setPreviewImage(undefined)} />
    </>
  );
}
