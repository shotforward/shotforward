import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  artifactContentURL,
  type Artifact,
  type EpisodeAsset,
  type EpisodeReferenceMedia,
  type MediaSlotDetails,
} from '../api/assets';
import type { ProviderProfile } from '../api/providers';
import { useImageGeneratorPreference } from '../preferences';
import { AgentPromptActionButton } from './AgentPromptActionButton';
import { ImageLightbox, ImagePreviewButton } from './ImagePreview';

export interface FramePromptReference {
  key: string;
  name: string;
  artifact: Artifact;
}

function currentArtifact(media: MediaSlotDetails | undefined) {
  const currentID = media?.slot.current_media_version_id;
  const version = media?.versions.find((item) => item.media_version_id === currentID);
  return media?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
}

export function framePromptReferences(
  prompt: string,
  references: Array<{ asset: EpisodeAsset; media: MediaSlotDetails | undefined }>,
  uploadedMedia: EpisodeReferenceMedia[],
): FramePromptReference[] {
  const values = [
    ...uploadedMedia
      .filter((item) => item.artifact.kind === 'image' && prompt.includes(`@${item.name}`))
      .map((item) => ({
        key: `upload:${item.artifact.artifact_id}`,
        name: item.name,
        artifact: item.artifact,
      })),
    ...references.flatMap(({ asset, media }) => {
      const artifact = currentArtifact(media);
      return artifact?.kind === 'image' && prompt.includes(`@${asset.identity.name}`)
        ? [{ key: `asset:${asset.asset_id}`, name: asset.identity.name, artifact }]
        : [];
    }),
  ];
  return values.sort((left, right) => {
    const leftIndex = prompt.indexOf(`@${left.name}`);
    const rightIndex = prompt.indexOf(`@${right.name}`);
    return leftIndex - rightIndex;
  });
}

export default function FrameInput({
  projectID,
  label,
  promptLabel,
  prompt,
  artifact,
  mediaReady,
  busy,
  generating = false,
  dirty,
  generateLabel,
  uploadLabel,
  sourceLabel,
  referenceAssets = [],
  referenceUploads = [],
  imageProviders = [],
  reservedReferenceArtifactID,
  onPromptChange,
  onGenerate,
  onUpload,
  onChooseReferences,
  onRemoveReference,
  onPreviewReference,
  onRemove,
  onOpenAgent,
}: {
  projectID: string;
  label: string;
  promptLabel: string;
  prompt: string;
  artifact: Artifact | undefined;
  mediaReady: boolean;
  busy: boolean;
  generating?: boolean;
  dirty: boolean;
  generateLabel: string;
  uploadLabel: string;
  sourceLabel?: string;
  referenceAssets?: Array<{ asset: EpisodeAsset; media: MediaSlotDetails | undefined }>;
  referenceUploads?: EpisodeReferenceMedia[];
  imageProviders?: ProviderProfile[];
  reservedReferenceArtifactID?: string;
  onPromptChange: (value: string) => void;
  onGenerate: (generatorID: 'image.codex' | 'image.shotforward', providerModelID?: string) => void;
  onUpload?: (file: File) => void;
  onChooseReferences?: () => void;
  onRemoveReference?: (name: string) => void;
  onPreviewReference?: (artifact: Artifact, title: string) => void;
  onRemove?: () => void;
  onOpenAgent: () => void;
}) {
  const { t, i18n } = useTranslation();
  const preferredImageGenerator = useImageGeneratorPreference();
  const availableImageProviders = imageProviders.filter(
    (provider) =>
      provider.generator_id.startsWith('image.') &&
      provider.enabled &&
      provider.health === 'ready' &&
      (!provider.models?.length || provider.models.some((model) => model.health === 'ready')),
  );
  const references = framePromptReferences(prompt, referenceAssets, referenceUploads);
  const referenceIDs = references.map((reference) => reference.artifact.artifact_id);
  if (reservedReferenceArtifactID && !referenceIDs.includes(reservedReferenceArtifactID)) {
    referenceIDs.unshift(reservedReferenceArtifactID);
  }
  const effectiveReferenceCount = referenceIDs.length;
  const preferredImageProvider = availableImageProviders.find(
    (provider) => provider.generator_id === preferredImageGenerator,
  );
  const activeImageProvider = preferredImageProvider ?? availableImageProviders[0];
  const imageGenerator =
    activeImageProvider?.generator_id === 'image.shotforward' ? 'image.shotforward' : 'image.codex';
  const imageProviderModel =
    imageGenerator === 'image.shotforward'
      ? (activeImageProvider?.models?.find((model) => model.is_default)?.model_id ??
        activeImageProvider?.model_id)
      : undefined;
  const maximumReferences = 5;
  const referenceProvider = imageGenerator === 'image.shotforward' ? 'ShotForward Cloud' : 'Codex';
  const promptID = useId();
  const promptTextarea = useRef<HTMLTextAreaElement>(null);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [promptExpanded, setPromptExpanded] = useState(false);
  const previewURL = artifact ? artifactContentURL(projectID, artifact.artifact_id) : '';
  const linkedArtifact = Boolean(artifact && sourceLabel);
  const tooManyReferences = maximumReferences !== undefined && effectiveReferenceCount > maximumReferences;
  const reservedReferenceCount =
    reservedReferenceArtifactID &&
    !references.some((reference) => reference.artifact.artifact_id === reservedReferenceArtifactID)
      ? 1
      : 0;
  const visibleReferenceLimit = Math.max(0, maximumReferences - reservedReferenceCount);
  const referenceLimitReached = effectiveReferenceCount >= maximumReferences;
  useEffect(() => setPreviewOpen(false), [artifact?.artifact_id]);
  useEffect(() => {
    const input = promptTextarea.current;
    if (!promptExpanded || !input) return;
    const minimumHeight = 112;
    const maximumHeight = 300;
    input.style.height = '0px';
    const contentHeight = input.scrollHeight || minimumHeight;
    input.style.height = `${Math.min(maximumHeight, Math.max(minimumHeight, contentHeight))}px`;
    input.style.overflowY = contentHeight > maximumHeight ? 'auto' : 'hidden';
  }, [prompt, promptExpanded]);
  return (
    <article
      className={`generation-frame-input${artifact ? ' is-ready' : ''}${linkedArtifact ? ' is-linked' : ''}${generating ? ' is-generating' : ''}${promptExpanded ? ' is-expanded' : ''}`}
    >
      <header>
        <div className="generation-frame-input__title">
          <strong>{label}</strong>
          {sourceLabel && <small>{sourceLabel}</small>}
        </div>
        <div className="generation-frame-input__header-actions">
          <span className="generation-frame-input__status" role={generating ? 'status' : undefined}>
            <i aria-hidden="true">{!generating && artifact ? '✓' : ''}</i>
            {generating
              ? t('storyboard.videoGenerating')
              : artifact
                ? t(linkedArtifact ? 'storyboard.frameLinked' : 'storyboard.frameReady')
                : t('storyboard.frameMissing')}
          </span>
          {promptExpanded && (
            <AgentPromptActionButton onClick={onOpenAgent}>
              {t('storyboard.agentEditPrompt')}
            </AgentPromptActionButton>
          )}
          <button
            type="button"
            className="generation-frame-input__edit"
            aria-expanded={promptExpanded}
            onClick={() => setPromptExpanded((current) => !current)}
          >
            {promptExpanded
              ? i18n.resolvedLanguage?.startsWith('zh')
                ? '收起'
                : 'Collapse'
              : i18n.resolvedLanguage?.startsWith('zh')
                ? '编辑'
                : 'Edit'}
          </button>
          {onRemove && (
            <button type="button" className="generation-frame-input__remove" onClick={onRemove}>
              {t('storyboard.removeLastFrame')}
            </button>
          )}
        </div>
      </header>
      <div className="generation-frame-input__body">
        <div className="generation-frame-media">
          {artifact ? (
            <ImagePreviewButton
              src={previewURL}
              alt={label}
              className="generation-frame-preview"
              hintLabel={t('storyboard.previewFrame')}
              onPreview={() => setPreviewOpen(true)}
            />
          ) : (
            <div className="generation-frame-preview generation-frame-preview--empty" data-surface="stage">
              <span aria-hidden="true">＋</span>
              <small>{uploadLabel}</small>
            </div>
          )}
          {onUpload && (
            <label
              className={`generation-frame-replace${artifact ? '' : ' is-empty'}${busy ? ' is-disabled' : ''}`}
            >
              <span>{artifact ? t('storyboard.replaceFrame') : uploadLabel}</span>
              <input
                type="file"
                accept="image/jpeg,image/png,image/webp"
                disabled={busy}
                aria-label={uploadLabel}
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  event.target.value = '';
                  if (file) onUpload(file);
                }}
              />
            </label>
          )}
        </div>
        <div className="generation-frame-prompt-panel">
          {!promptExpanded && (
            <div className="generation-frame-prompt-summary">
              <strong>{promptLabel}</strong>
              <p>{prompt}</p>
              <small>
                {i18n.resolvedLanguage?.startsWith('zh')
                  ? `参考图 ${references.length} 张`
                  : `${references.length} reference images`}
              </small>
              {tooManyReferences && (
                <p className="generation-frame-references__error" role="alert">
                  {i18n.resolvedLanguage?.startsWith('zh')
                    ? `${referenceProvider} 最多支持 ${maximumReferences} 张参考图；请移除标红的多余参考图。`
                    : `${referenceProvider} supports up to ${maximumReferences} reference images. Remove the extra references marked in red.`}
                </p>
              )}
            </div>
          )}
          <div className={`field generation-frame-prompt${promptExpanded ? '' : ' is-collapsed'}`}>
            <div className="generation-frame-prompt__header">
              <label htmlFor={promptID}>{promptLabel}</label>
            </div>
            <textarea
              id={promptID}
              ref={promptTextarea}
              rows={4}
              value={prompt}
              onChange={(event) => onPromptChange(event.target.value)}
            />
            <div className="generation-frame-references">
              <div className="generation-frame-references__heading">
                <span>{t('storyboard.frameReferences')}</span>
                <small>
                  {`${referenceProvider} · ${
                    i18n.resolvedLanguage?.startsWith('zh')
                      ? `最多 ${maximumReferences} 张`
                      : `${maximumReferences} max`
                  }`}
                </small>
              </div>
              <div className="generation-frame-references__items">
                {references.slice(0, maximumReferences + 1).map((reference, index) => (
                  <span
                    className={`generation-frame-reference${index >= visibleReferenceLimit ? ' is-over-limit' : ''}`}
                    key={reference.key}
                  >
                    <button
                      type="button"
                      className="generation-frame-reference__preview"
                      aria-label={t('storyboard.previewPromptReference', { name: reference.name })}
                      title={reference.name}
                      onClick={() => onPreviewReference?.(reference.artifact, reference.name)}
                    >
                      <img src={artifactContentURL(projectID, reference.artifact.artifact_id)} alt="" />
                    </button>
                    <button
                      type="button"
                      className="generation-frame-reference__remove"
                      aria-label={t('storyboard.removeFrameReference', { name: reference.name })}
                      onClick={() => onRemoveReference?.(reference.name)}
                    >
                      ×
                    </button>
                  </span>
                ))}
                {references.length > maximumReferences + 1 && (
                  <span className="generation-frame-reference__overflow">
                    +{references.length - maximumReferences - 1}
                  </span>
                )}
                <button
                  type="button"
                  className="generation-frame-reference__add"
                  aria-label={t('storyboard.addFrameReference')}
                  title={
                    referenceLimitReached
                      ? i18n.resolvedLanguage?.startsWith('zh')
                        ? `${referenceProvider} 已达到 ${maximumReferences} 张参考图上限`
                        : `${referenceProvider} has reached its ${maximumReferences}-image limit`
                      : t('storyboard.addFrameReference')
                  }
                  disabled={referenceLimitReached}
                  onClick={onChooseReferences}
                >
                  ＋
                </button>
              </div>
              {tooManyReferences && (
                <p className="generation-frame-references__error" role="alert">
                  {i18n.resolvedLanguage?.startsWith('zh')
                    ? `${referenceProvider} 最多支持 ${maximumReferences} 张参考图；请移除标红的多余参考图。`
                    : `${referenceProvider} supports up to ${maximumReferences} reference images. Remove the extra references marked in red.`}
                </p>
              )}
            </div>
          </div>
          <div className="generation-frame-input__actions">
            <button
              type="button"
              className="button button--secondary"
              disabled={busy || generating || dirty || !mediaReady || !prompt.trim() || tooManyReferences}
              onClick={() => onGenerate(imageGenerator, imageProviderModel)}
            >
              {generating ? t('storyboard.videoGenerating') : generateLabel}
            </button>
          </div>
        </div>
      </div>
      <ImageLightbox
        value={
          previewOpen && artifact
            ? {
                src: previewURL,
                title: label,
                width: artifact.width,
                height: artifact.height,
              }
            : undefined
        }
        onClose={() => setPreviewOpen(false)}
      />
    </article>
  );
}
