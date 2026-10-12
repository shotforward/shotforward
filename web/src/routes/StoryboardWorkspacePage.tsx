import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  attachClipReferenceMedia,
  artifactContentURL,
  deleteClipReferenceMedia,
  extractVideoFrame,
  getMediaSlot,
  listClipReferenceMedia,
  listEpisodeAssets,
  listEpisodeReferenceMedia,
  selectMediaVersion,
  uploadEpisodeReferenceMedia,
  uploadMedia,
  type Artifact,
  type EpisodeAsset,
  type EpisodeReferenceMedia,
  type MediaSlotDetails,
} from '../api/assets';
import { APIError, createOperationKey } from '../api/client';
import { getEpisode } from '../api/episodes';
import {
  checkGenerationReadiness,
  generationSubmissionErrorMessage,
  MissingAssetReferenceMediaError,
  resolvePromptTranslation,
  submitGeneration,
  type GenerationReference,
  type GenerationTarget,
} from '../api/generation';
import {
  isProviderAvailable,
  listProviders,
  providerReferenceInputsReady,
  type ProviderModelCapabilities,
} from '../api/providers';
import {
  bindClipAsset,
  allVideoPromptText,
  createClip,
  createScene,
  deleteClip,
  deleteScene,
  getStoryboard,
  updateVideoPromptFamily,
  unbindClipAsset,
  updateClip,
  videoPromptFamily,
  videoPromptProfile,
  videoPromptText,
  videoPromptVariant,
  type Clip,
  type ClipFields,
  type Scene,
} from '../api/storyboard';
import { Dialog } from '../components/Dialog';
import { AgentDock, type AgentDraftRequest } from '../components/AgentDock';
import { AgentPromptActionButton } from '../components/AgentPromptActionButton';
import { AssetDetailPanel } from '../components/AssetDetailPanel';
import { MediaPreviewDialog, type MediaPreviewValue } from '../components/MediaPreview';
import type { MediaTrim } from '../mediaTrim';
import { VideoExpandButton, VideoLightbox, type VideoPreviewValue } from '../components/VideoPreview';
import {
  ArrowRightIcon,
  ChevronIcon,
  MoreHorizontalIcon,
  OutlineIcon,
  SplitIcon,
  StoryboardIcon,
  TrashIcon,
} from '../components/Icons';
import { EpisodeWorkspaceLoading, ProjectTabs } from '../components/ProjectTabs';
import { Select as StudioSelect } from '../components/Select';
import {
  appendAgentTemplateGuidance,
  defaultAgentTemplates,
  renderAgentTemplate as interpolateAgentTemplate,
  renderStoryboardVideoModelGuidance,
  type AgentTemplateKey,
} from '../agentTemplates';
import { useAgentTemplates } from '../useAgentTemplates';
import { changeGenerationTask, getTaskSnapshot, type TaskItem } from '../api/tasks';
import { saveDownload } from '../desktop';
import { splitPromptAssetMentions } from '../storyboardPromptReferences';
import { taskFailureMessage } from '../taskFailure';
import { businessErrorCode } from '../businessErrors';
import { RechargeAction } from '../components/RechargeAction';
import { setPreferredVideoModel } from '../preferences';
import type { AgentVideoPromptFocusTarget } from '../api/agent';
import { useStoryboardOperations } from './useStoryboardOperations';
import { formatVideoTaskElapsed, videoTaskProviderLabel } from './storyboardVideoTask';
import {
  preferredVideoModelChoice,
  resolveVideoPromptTarget,
  videoModelChoices,
  type VideoModelChoice,
} from '../videoPromptTarget';
export { preferredVideoModelChoice, videoModelChoices } from '../videoPromptTarget';
import '../styles/storyboard-select.css';
import '../styles/media-workspaces.css';

export { splitPromptAssetMentions } from '../storyboardPromptReferences';
export { MissingAssetReferenceMediaError } from '../api/generation';

type GenerationKind = 'first_image' | 'last_image' | 'video';

type SaveClipRequest = {
  clip: Clip;
  fields: ClipFields;
  assets: EpisodeAsset[] | undefined;
  uploadedMedia: EpisodeReferenceMedia[] | undefined;
};
type GenerationRequest = {
  kind: GenerationKind;
  clip: Clip;
  generatorID: string;
  providerModelID?: string;
  fields?: ClipFields;
  uploadedReferences?: Array<GenerationReference & { name?: string }>;
  continuation?: VideoContinuationInput;
  controller: AbortController;
  operationKey: string;
  media: {
    first: MediaSlotDetails | undefined;
    last: MediaSlotDetails | undefined;
    video: MediaSlotDetails | undefined;
    previousClip: Clip | undefined;
    nextClip: Clip | undefined;
    nextFirst: MediaSlotDetails | undefined;
    assets: AssetReferenceMedia[];
    assetCatalog: EpisodeAsset[] | undefined;
    uploadedMedia: EpisodeReferenceMedia[] | undefined;
  };
};
type ReferencePromptTarget = GenerationKind;
const maximumVideoReferenceDurationMilliseconds = 15_000;
const automaticPreviousFrameFilename = 'previous-video-last-frame.png';
const automaticNextFrameFilename = 'next-video-first-frame.png';
// Automatic continuation silently adds the previous generated video as a
// billable reference. Keep the implementation available for a later rollout,
// but require users to select and upload video references explicitly for now.
const previousVideoContinuationEnabled = false;
const FrameInput = lazy(() => import('../components/FrameInput'));
const VideoModelHelp = lazy(() => import('../components/VideoModelHelp'));
const GenerationWorkflowSelect = lazy(() => import('../components/GenerationWorkflowSelect'));
const ReferenceLibraryDialog = lazy(() => import('../components/ReferenceLibraryDialog'));
const AssetPreviewDialog = lazy(() => import('../components/AssetPreviewDialog'));

export interface AssetReferenceMedia {
  asset: EpisodeAsset;
  media: MediaSlotDetails | undefined;
}

export interface UploadedReferenceMedia {
  reference: GenerationReference & { name?: string };
  artifact: Artifact;
  filename: string;
}

export interface VideoContinuationInput {
  artifact: Artifact;
  clip: Pick<Clip, 'clip_id' | 'position' | 'title'>;
}

export function StoryboardWorkspacePage() {
  const { projectId = '', episodeId = '', clipId = '' } = useParams();
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  const manualCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? {
        sceneDescription: '场景说明（可选）',
        sceneDescriptionPlaceholder: '描述场景用途、地点、时间或整体内容…',
        createClip: '创建视频单元',
        createClipFailed: '视频单元创建失败，Storyboard 可能已在其他窗口修改。',
        createWithAgent: '让 Agent 添加',
        manualStart: '手动开始',
      }
    : {
        sceneDescription: 'Scene description (optional)',
        sceneDescriptionPlaceholder: 'Describe the scene purpose, place, time, or overall content…',
        createClip: 'Create video unit',
        createClipFailed: 'The video unit could not be created. The storyboard may have changed elsewhere.',
        createWithAgent: 'Add with Agent',
        manualStart: 'Start manually',
      };
  const agentTemplates = useAgentTemplates();
  const queryClient = useQueryClient();
  const episode = useQuery({
    queryKey: ['episode', projectId, episodeId],
    queryFn: () => getEpisode(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const storyboard = useQuery({
    queryKey: ['storyboard', projectId, episodeId],
    queryFn: () => getStoryboard(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const assets = useQuery({
    queryKey: ['assets', projectId, episodeId],
    queryFn: () => listEpisodeAssets(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
    staleTime: 30_000,
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
  const providers = useQuery({
    queryKey: ['generation-providers'],
    queryFn: listProviders,
    staleTime: 30_000,
  });
  const [sceneID, setSceneID] = useState('');
  const [continuityFrameRequests, setContinuityFrameRequests] = useState<Record<string, number>>({});
  const continuityFrameAttempts = useRef(new Set<string>());
  const [deleteTarget, setDeleteTarget] = useState<'scene' | 'clip' | null>(null);
  const [sceneCreateOpen, setSceneCreateOpen] = useState(false);
  const [sceneName, setSceneName] = useState('');
  const [sceneDescription, setSceneDescription] = useState('');
  const [clipCreateOpen, setClipCreateOpen] = useState(false);
  const [clipTitle, setClipTitle] = useState('');
  const [clipDuration, setClipDuration] = useState(5);
  const [clipMode, setClipMode] = useState<ClipFields['generation_mode']>('reference');
  const [agentDraftRequest, setAgentDraftRequest] = useState<AgentDraftRequest>();
  const [focusedVideoPromptTarget, setFocusedVideoPromptTarget] = useState<AgentVideoPromptFocusTarget>();
  const updateFocusedVideoPromptTarget = useCallback((target: AgentVideoPromptFocusTarget | undefined) => {
    setFocusedVideoPromptTarget((current) =>
      JSON.stringify(current) === JSON.stringify(target) ? current : target,
    );
  }, []);
  const [sceneRailCollapsed, setSceneRailCollapsed] = useState(false);
  const [selectingClips, setSelectingClips] = useState(false);
  const [mergeClipIDs, setMergeClipIDs] = useState<string[]>([]);
  const mergeSelectionAnchor = useRef('');
  const openAgent = (text: string) => {
    setAgentDraftRequest((current) => ({ id: (current?.id ?? 0) + 1, text }));
  };

  const storyboardModelTarget = resolveVideoPromptTarget(providers.data?.providers ?? []);
  const storyboardModelGuidance = storyboardModelTarget
    ? renderStoryboardVideoModelGuidance(i18n.resolvedLanguage?.startsWith('zh') ? 'zh-CN' : 'en', {
        generatorID: storyboardModelTarget.generator_id,
        providerModelID: storyboardModelTarget.provider_model_id,
        ...(storyboardModelTarget.keyframe_provider_model_id
          ? { keyframeProviderModelID: storyboardModelTarget.keyframe_provider_model_id }
          : {}),
      })
    : '';

  const openAddClipAgent = (scene: Scene, placement?: { before?: Clip; after?: Clip }) => {
    const baseRequest = agentTemplates.render('storyboard.add_clip', {
      scene_title: scene.title,
      scene_id: scene.scene_id,
    });
    const placementRequest = placement?.before
      ? t('storyboard.agentInsertClipBeforeRequest', {
          clipTitle: placement.before.title,
          clipID: placement.before.clip_id,
        })
      : placement?.after
        ? t('storyboard.agentInsertClipAfterRequest', {
            clipTitle: placement.after.title,
            clipID: placement.after.clip_id,
          })
        : t('storyboard.agentAppendClipRequest');
    openAgent(
      appendAgentTemplateGuidance(
        baseRequest,
        [placementRequest, storyboardModelGuidance].filter(Boolean).join('\n\n'),
      ),
    );
  };

  const openSplitClipAgent = async (clip: Clip) => {
    const { clipSplitAgentRequest } = await import('../storyboardAgentRequests');
    openAgent(
      clipSplitAgentRequest(i18n.resolvedLanguage?.startsWith('zh') ? 'zh-CN' : 'en', {
        title: clip.title,
        clipID: clip.clip_id,
        duration: Math.round(clip.planned_duration_ms / 1000),
      }),
    );
  };

  const scenes = storyboard.data?.storyboard.scenes ?? [];
  const allClips = useMemo(() => scenes.flatMap((scene) => scene.clips), [scenes]);
  const selectedClip = allClips.find((clip) => clip.clip_id === clipId) ?? allClips[0];
  const selectedScene =
    scenes.find((scene) => scene.scene_id === (sceneID || selectedClip?.scene_id)) ?? scenes[0];
  const selectedClipIndex =
    selectedScene?.clips.findIndex((clip) => clip.clip_id === selectedClip?.clip_id) ?? -1;
  const previousClip = selectedClipIndex > 0 ? selectedScene?.clips[selectedClipIndex - 1] : undefined;
  const nextClip =
    selectedClipIndex >= 0 && selectedClipIndex + 1 < (selectedScene?.clips.length ?? 0)
      ? selectedScene?.clips[selectedClipIndex + 1]
      : undefined;
  const mergeClips = selectedScene?.clips.filter((clip) => mergeClipIDs.includes(clip.clip_id)) ?? [];
  const mergeClipIndexes = mergeClips.map((clip) => selectedScene!.clips.indexOf(clip));
  const mergeDurationMilliseconds = mergeClips.reduce(
    (duration, clip) => duration + clip.planned_duration_ms,
    0,
  );
  const mergeSelectionIsAdjacent =
    mergeClipIndexes.length >= 2 &&
    mergeClipIndexes.at(-1)! - mergeClipIndexes.at(0)! + 1 === mergeClipIndexes.length;
  const mergeSelectionIsValid = mergeSelectionIsAdjacent && mergeDurationMilliseconds <= 30_000;
  useEffect(() => {
    if (!clipId && selectedClip)
      navigate(`/projects/${projectId}/episodes/${episodeId}/storyboard/clips/${selectedClip.clip_id}`, {
        replace: true,
      });
  }, [clipId, episodeId, navigate, projectId, selectedClip]);
  useEffect(() => {
    if (selectedScene && selectedScene.scene_id !== sceneID) setSceneID(selectedScene.scene_id);
  }, [sceneID, selectedScene]);
  useEffect(() => {
    setSelectingClips(false);
    setMergeClipIDs([]);
    mergeSelectionAnchor.current = '';
  }, [selectedScene?.scene_id]);

  const startClipSelection = () => {
    const initialClip =
      selectedClip?.scene_id === selectedScene?.scene_id ? selectedClip : selectedScene?.clips[0];
    setMergeClipIDs(initialClip ? [initialClip.clip_id] : []);
    mergeSelectionAnchor.current = initialClip?.clip_id ?? '';
    setSelectingClips(true);
  };

  const stopClipSelection = () => {
    setSelectingClips(false);
    setMergeClipIDs([]);
    mergeSelectionAnchor.current = '';
  };

  const toggleClipSelection = (clip: Clip, extend: boolean) => {
    if (!selectedScene) return;
    if (extend && mergeSelectionAnchor.current) {
      const anchorIndex = selectedScene.clips.findIndex(
        (candidate) => candidate.clip_id === mergeSelectionAnchor.current,
      );
      const clipIndex = selectedScene.clips.indexOf(clip);
      if (anchorIndex >= 0 && clipIndex >= 0) {
        const start = Math.min(anchorIndex, clipIndex);
        const end = Math.max(anchorIndex, clipIndex);
        const range = selectedScene.clips.slice(start, end + 1).map((candidate) => candidate.clip_id);
        setMergeClipIDs((current) => [...new Set([...current, ...range])]);
        return;
      }
    }
    setMergeClipIDs((current) =>
      current.includes(clip.clip_id)
        ? current.filter((clipID) => clipID !== clip.clip_id)
        : [...current, clip.clip_id],
    );
    mergeSelectionAnchor.current = clip.clip_id;
  };

  const openMergeClipAgent = async () => {
    if (!selectedScene || !mergeSelectionIsValid) return;
    const clipList = mergeClips
      .map((clip) => `${String(clip.position).padStart(2, '0')} · ${clip.title} (clip_id: ${clip.clip_id})`)
      .join('\n');
    const { clipMergeAgentRequest } = await import('../storyboardAgentRequests');
    openAgent(
      clipMergeAgentRequest(i18n.resolvedLanguage?.startsWith('zh') ? 'zh-CN' : 'en', {
        sceneTitle: selectedScene.title,
        sceneID: selectedScene.scene_id,
        duration: Math.round(mergeDurationMilliseconds / 1000),
        clipList,
      }),
    );
    stopClipSelection();
  };

  const firstMedia = useQuery({
    queryKey: ['media-slot', projectId, selectedClip?.first_image_slot_id],
    queryFn: () => getMediaSlot(projectId, selectedClip!.first_image_slot_id),
    enabled: Boolean(selectedClip?.first_image_slot_id),
  });
  const lastMedia = useQuery({
    queryKey: ['media-slot', projectId, selectedClip?.last_image_slot_id],
    queryFn: () => getMediaSlot(projectId, selectedClip!.last_image_slot_id),
    enabled: Boolean(selectedClip?.last_image_slot_id),
  });
  const videoMedia = useQuery({
    queryKey: ['media-slot', projectId, selectedClip?.video_slot_id],
    queryFn: () => getMediaSlot(projectId, selectedClip!.video_slot_id),
    enabled: Boolean(selectedClip?.video_slot_id),
  });
  const previousVideoMedia = useQuery({
    queryKey: ['media-slot', projectId, previousClip?.video_slot_id],
    queryFn: () => getMediaSlot(projectId, previousClip!.video_slot_id),
    enabled: Boolean(previousClip?.video_slot_id),
  });
  const nextVideoMedia = useQuery({
    queryKey: ['media-slot', projectId, nextClip?.video_slot_id],
    queryFn: () => getMediaSlot(projectId, nextClip!.video_slot_id),
    enabled: Boolean(nextClip?.video_slot_id),
  });
  const nextFirstMedia = useQuery({
    queryKey: ['media-slot', projectId, nextClip?.first_image_slot_id],
    queryFn: () => getMediaSlot(projectId, nextClip!.first_image_slot_id),
    enabled: Boolean(nextClip?.first_image_slot_id && nextClip.generation_mode !== 'reference'),
  });
  useEffect(() => {
    if (
      !selectedClip ||
      selectedClip.generation_mode === 'reference' ||
      !previousClip ||
      !firstMedia.data ||
      firstMedia.data.slot.current_media_version_id ||
      !currentArtifact(previousVideoMedia.data)
    )
      return;
    const sourceArtifactID = currentArtifact(previousVideoMedia.data)!.artifact_id;
    const attemptKey = `${selectedClip.clip_id}:${firstMedia.data.slot.revision}:${sourceArtifactID}`;
    if (continuityFrameAttempts.current.has(attemptKey)) return;
    continuityFrameAttempts.current.add(attemptKey);
    const targetClipID = selectedClip.clip_id;
    setContinuityFrameRequests((current) => ({
      ...current,
      [targetClipID]: (current[targetClipID] ?? 0) + 1,
    }));
    void extractVideoFrame(
      projectId,
      selectedClip.first_image_slot_id,
      previousClip.video_slot_id,
      firstMedia.data.slot.revision,
    )
      .then((details) => {
        queryClient.setQueryData(['media-slot', projectId, details.slot.media_slot_id], details);
      })
      .catch(() => {
        continuityFrameAttempts.current.delete(attemptKey);
      })
      .finally(() => {
        setContinuityFrameRequests((current) => ({
          ...current,
          [targetClipID]: Math.max(0, (current[targetClipID] ?? 0) - 1),
        }));
      });
  }, [firstMedia.data, previousClip, previousVideoMedia.data, projectId, queryClient, selectedClip]);
  const clipReferenceMedia = useQuery({
    queryKey: ['clip-reference-media', projectId, episodeId, selectedClip?.clip_id],
    queryFn: () => listClipReferenceMedia(projectId, episodeId, selectedClip!.clip_id),
    enabled: Boolean(projectId && episodeId && selectedClip?.clip_id),
  });
  const episodeReferenceMedia = useQuery({
    queryKey: ['episode-reference-media', projectId, episodeId],
    queryFn: () => listEpisodeReferenceMedia(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const episodeAssets = assets.data?.assets ?? [];
  const assetMediaQueries = useQueries({
    queries: episodeAssets.map((asset) => ({
      queryKey: ['media-slot', projectId, asset.primary_image_slot_id],
      queryFn: () => getMediaSlot(projectId, asset.primary_image_slot_id!),
      enabled: Boolean(projectId && asset.primary_image_slot_id),
      staleTime: 30_000,
    })),
  });
  const episodeAssetReferenceMedia: AssetReferenceMedia[] = episodeAssets.map((asset, index) => ({
    asset,
    media: assetMediaQueries[index]?.data,
  }));
  const latestAssetImageTaskBySlot = useMemo(() => {
    const result = new Map<string, TaskItem>();
    for (const task of tasks.data?.tasks ?? []) {
      if (
        task.project_id === projectId &&
        task.episode_id === episodeId &&
        task.task_type === 'generation_job' &&
        task.target_media_kind === 'image' &&
        task.target_media_slot_id &&
        (!result.has(task.target_media_slot_id) ||
          task.updated_at > result.get(task.target_media_slot_id)!.updated_at)
      ) {
        result.set(task.target_media_slot_id, task);
      }
    }
    return result;
  }, [episodeId, projectId, tasks.data?.tasks]);
  const latestVideoTask = useMemo(() => {
    if (!selectedClip?.video_slot_id) return undefined;
    return (tasks.data?.tasks ?? []).reduce<TaskItem | undefined>((latest, task) => {
      if (
        task.task_type !== 'generation_job' ||
        task.project_id !== projectId ||
        task.episode_id !== episodeId ||
        task.target_media_kind !== 'video' ||
        task.target_media_slot_id !== selectedClip.video_slot_id
      )
        return latest;
      if (
        !latest ||
        task.created_at > latest.created_at ||
        (task.created_at === latest.created_at && task.task_id > latest.task_id)
      )
        return task;
      return latest;
    }, undefined);
  }, [episodeId, projectId, selectedClip?.video_slot_id, tasks.data?.tasks]);
  const boundAssetReferenceMedia = (selectedClip?.asset_references ?? [])
    .map((reference) => episodeAssetReferenceMedia.find((item) => item.asset.asset_id === reference.asset_id))
    .filter((item): item is AssetReferenceMedia => Boolean(item));
  const persistedReferenceMedia = useMemo(
    () =>
      (clipReferenceMedia.data?.artifacts ?? []).map((artifact) =>
        uploadedReferenceFromArtifact(
          artifact,
          episodeReferenceMedia.data?.reference_media.find(
            (item) => item.artifact.artifact_id === artifact.artifact_id,
          )?.name,
          episodeReferenceMedia.data?.reference_media.find(
            (item) => item.artifact.artifact_id === artifact.artifact_id,
          ),
        ),
      ),
    [clipReferenceMedia.data?.artifacts, episodeReferenceMedia.data?.reference_media],
  );
  const cacheMediaSlot = (details: MediaSlotDetails) =>
    queryClient.setQueryData(['media-slot', projectId, details.slot.media_slot_id], details);
  const uploadFrame = async (kind: 'first_image' | 'last_image', file: File) => {
    if (!selectedClip) throw new Error('clip is required');
    const details = kind === 'first_image' ? firstMedia.data : lastMedia.data;
    const slotID =
      kind === 'first_image' ? selectedClip.first_image_slot_id : selectedClip.last_image_slot_id;
    const result = await uploadMedia(projectId, slotID, file, details?.slot.revision ?? 1, true);
    cacheMediaSlot(result);
    return result;
  };
  const uploadReference = async (file: File): Promise<UploadedReferenceMedia> => {
    if (!selectedClip) throw new Error('clip is required');
    const role = referenceRoleForFile(file);
    const kind = role === 'image_reference' ? 'image' : role === 'video_reference' ? 'video' : 'audio';
    const { reference_media: uploaded } = await uploadEpisodeReferenceMedia(projectId, episodeId, file, kind);
    await queryClient.invalidateQueries({ queryKey: ['episode-reference-media', projectId, episodeId] });
    return uploadedReferenceFromArtifact(uploaded.artifact, uploaded.name, uploaded);
  };
  const removeReference = async (artifactID: string) => {
    if (!selectedClip) return;
    await deleteClipReferenceMedia(projectId, episodeId, selectedClip.clip_id, artifactID);
    await queryClient.invalidateQueries({
      queryKey: ['clip-reference-media', projectId, episodeId, selectedClip.clip_id],
    });
  };
  const refreshStoryboard = async () => {
    await queryClient.invalidateQueries({ queryKey: ['storyboard', projectId, episodeId] });
  };
  const createSceneMutation = useMutation({
    mutationFn: () =>
      createScene(
        projectId,
        episodeId,
        sceneName.trim(),
        sceneDescription.trim(),
        storyboard.data!.storyboard.revision,
      ),
    onSuccess: async (result) => {
      setSceneCreateOpen(false);
      setSceneName('');
      setSceneDescription('');
      setSceneID(result.scene.scene_id);
      await refreshStoryboard();
    },
  });
  const createClipMutation = useMutation({
    mutationFn: () =>
      createClip(
        projectId,
        episodeId,
        selectedScene!.scene_id,
        {
          title: clipTitle.trim(),
          description: '',
          dialogue: '',
          creative_intent: '',
          planned_duration_seconds: clipDuration,
          first_image_prompt: '',
          last_image_prompt: '',
          video_prompts: { first_image: '', first_last_image: '', reference: '' },
          generation_mode: clipMode,
          continue_from_previous: false,
        },
        storyboard.data!.storyboard.revision,
      ),
    onSuccess: async (result) => {
      setClipCreateOpen(false);
      setClipTitle('');
      setClipDuration(5);
      setClipMode('reference');
      navigate(`/projects/${projectId}/episodes/${episodeId}/storyboard/clips/${result.clip.clip_id}`);
      await refreshStoryboard();
    },
  });
  const removeScene = useMutation({
    mutationFn: () =>
      deleteScene(
        projectId,
        episodeId,
        selectedScene!.scene_id,
        selectedScene!.revision,
        storyboard.data!.storyboard.revision,
      ),
    onSuccess: async () => {
      setDeleteTarget(null);
      setSceneID('');
      navigate(`/projects/${projectId}/episodes/${episodeId}/storyboard`, { replace: true });
      await refreshStoryboard();
      await queryClient.invalidateQueries({ queryKey: ['project-trash', projectId] });
    },
  });
  const removeClip = useMutation({
    mutationFn: () =>
      deleteClip(
        projectId,
        episodeId,
        selectedClip!.clip_id,
        selectedClip!.revision,
        storyboard.data!.storyboard.revision,
      ),
    onSuccess: async () => {
      const sibling =
        selectedScene?.clips[selectedClipIndex + 1] ?? selectedScene?.clips[selectedClipIndex - 1];
      setDeleteTarget(null);
      navigate(
        sibling
          ? `/projects/${projectId}/episodes/${episodeId}/storyboard/clips/${sibling.clip_id}`
          : `/projects/${projectId}/episodes/${episodeId}/storyboard`,
        { replace: true },
      );
      await refreshStoryboard();
      await queryClient.invalidateQueries({ queryKey: ['project-trash', projectId] });
    },
  });
  const {
    scope: mutationScope,
    controllers: generationControllers,
    generation: focusedGeneration,
    cancellation: focusedCancellation,
    submitting,
    saving,
    preparingEnglishPrompt,
    error: clipOperationError,
    errorSource,
    notice: jobNotice,
  } = useStoryboardOperations<GenerationRequest, SaveClipRequest>({
    projectID: projectId,
    episodeID: episodeId,
    clipID: selectedClip?.clip_id,
    taskID: latestVideoTask?.task_id,
  });
  const saveClip = useMutation({
    mutationKey: ['storyboard-save', mutationScope],
    mutationFn: async ({
      clip,
      fields,
      assets: assetSnapshot,
      uploadedMedia: uploadSnapshot,
    }: SaveClipRequest) => {
      const projectId = clip.project_id;
      const episodeId = clip.episode_id;
      // Unloaded catalogs are not empty catalogs: never remove bindings just because a query is pending.
      const assets = assetSnapshot ?? (await listEpisodeAssets(projectId, episodeId)).assets;
      const uploadedMedia =
        uploadSnapshot ?? (await listEpisodeReferenceMedia(projectId, episodeId)).reference_media;
      let revision = clip.revision;
      const prompt = clipReferencePromptText(fields);
      const previousPrompt = clipReferencePromptText(clipToFields(clip));
      const existingAssetIDs = new Set(clip.asset_references.map((item) => item.asset_id));
      const desiredAssetIDs = new Set(
        assets
          .filter(
            (asset) =>
              prompt.includes(`@${asset.identity.name}`) ||
              (existingAssetIDs.has(asset.asset_id) && !previousPrompt.includes(`@${asset.identity.name}`)),
          )
          .map((asset) => asset.asset_id),
      );
      for (const assetID of desiredAssetIDs) {
        if (existingAssetIDs.has(assetID)) continue;
        const result = await bindClipAsset(projectId, episodeId, clip.clip_id, assetID, revision);
        revision = result.clip.revision;
      }
      for (const assetID of existingAssetIDs) {
        if (desiredAssetIDs.has(assetID)) continue;
        const result = await unbindClipAsset(projectId, episodeId, clip.clip_id, assetID, revision);
        revision = result.clip.revision;
      }
      const referenceMedia =
        queryClient.getQueryData<Awaited<ReturnType<typeof listClipReferenceMedia>>>([
          'clip-reference-media',
          projectId,
          episodeId,
          clip.clip_id,
        ]) ?? (await listClipReferenceMedia(projectId, episodeId, clip.clip_id));
      const existingUploadIDs = new Set(referenceMedia.artifacts.map((item) => item.artifact_id));
      const desiredUploadIDs = new Set(
        uploadedMedia
          .filter(
            (item) =>
              prompt.includes(`@${item.name}`) ||
              (existingUploadIDs.has(item.artifact.artifact_id) && !previousPrompt.includes(`@${item.name}`)),
          )
          .map((item) => item.artifact.artifact_id),
      );
      for (const artifactID of desiredUploadIDs) {
        if (!existingUploadIDs.has(artifactID))
          await attachClipReferenceMedia(projectId, episodeId, clip.clip_id, artifactID);
      }
      for (const artifactID of existingUploadIDs) {
        if (!desiredUploadIDs.has(artifactID))
          await deleteClipReferenceMedia(projectId, episodeId, clip.clip_id, artifactID);
      }
      return updateClip(projectId, episodeId, clip.clip_id, fields, revision);
    },
    onSuccess: async (_result, { clip }) => {
      await queryClient.invalidateQueries({ queryKey: ['storyboard', clip.project_id, clip.episode_id] });
    },
    onSettled: async (_result, _error, { clip }) => {
      await queryClient.invalidateQueries({
        queryKey: ['clip-reference-media', clip.project_id, clip.episode_id, clip.clip_id],
      });
      await queryClient.invalidateQueries({
        queryKey: ['episode-reference-media', clip.project_id, clip.episode_id],
      });
    },
  });
  const generation = useMutation({
    mutationKey: ['storyboard-generation', mutationScope],
    mutationFn: async ({
      kind,
      clip,
      generatorID,
      providerModelID,
      fields,
      uploadedReferences,
      continuation,
      controller,
      operationKey,
      media,
    }: GenerationRequest) => {
      const projectId = clip.project_id;
      const episodeId = clip.episode_id;
      const signal = controller.signal;
      signal.throwIfAborted();
      const targetClip = fields
        ? (
            await saveClip.mutateAsync({
              clip,
              fields,
              assets: media.assetCatalog,
              uploadedMedia: media.uploadedMedia,
            })
          ).clip
        : clip;
      signal.throwIfAborted();
      let generationFirstMedia = media.first;
      let generationLastMedia = media.last;
      if (kind === 'video' && targetClip.generation_mode !== 'reference') {
        const refreshed = await import('../api/generationKeyframes').then((module) =>
          module.refreshGenerationKeyframes({
            projectID: projectId,
            generationMode:
              targetClip.generation_mode === 'first_last_image' ? 'first_last_image' : 'first_image',
            firstFrameSlotID: targetClip.first_image_slot_id,
            lastFrameSlotID: targetClip.last_image_slot_id,
            ...(media.previousClip ? { previousVideoSlotID: media.previousClip.video_slot_id } : {}),
            ...(media.nextClip?.generation_mode !== 'reference' && media.nextFirst
              ? { nextFirstFrameMedia: media.nextFirst }
              : {}),
            ...(media.nextClip ? { nextVideoSlotID: media.nextClip.video_slot_id } : {}),
            firstMedia: generationFirstMedia,
            lastMedia: generationLastMedia,
          }),
        );
        generationFirstMedia = refreshed.first;
        generationLastMedia = refreshed.last;
        for (const details of [generationFirstMedia, generationLastMedia]) {
          if (details)
            queryClient.setQueryData(['media-slot', projectId, details.slot.media_slot_id], details);
        }
      }
      signal.throwIfAborted();
      const generationAssetMedia = targetClip.asset_references
        .map((reference) => media.assets.find((item) => item.asset.asset_id === reference.asset_id))
        .filter((item): item is AssetReferenceMedia => Boolean(item));
      const target = buildGenerationTarget(
        kind,
        targetClip,
        generatorID,
        generationFirstMedia,
        generationLastMedia,
        media.video,
        generationAssetMedia,
        uploadedReferences,
        providerModelID,
        continuation,
      );
      await checkGenerationReadiness(projectId, episodeId, target, signal);
      signal.throwIfAborted();
      const promptTranslation =
        kind === 'video' && generatorID.startsWith('video.h3.')
          ? await resolvePromptTranslation(signal)
          : undefined;
      signal.throwIfAborted();
      const result = await submitGeneration(projectId, episodeId, target, {
        operationKey,
        signal,
        ...(promptTranslation ? { promptTranslation } : {}),
      });
      signal.throwIfAborted();
      return result;
    },
    onSuccess: async (_result, variables) => {
      if (variables.controller.signal.aborted) return;
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
    onError: async (error, variables) => {
      if (variables.controller.signal.aborted) {
        await queryClient.invalidateQueries({ queryKey: ['tasks'] });
        return;
      }
      if (!(error instanceof APIError) || error.code !== 'revision_conflict') return;
      const slotID =
        variables.kind === 'first_image'
          ? variables.clip.first_image_slot_id
          : variables.kind === 'last_image'
            ? variables.clip.last_image_slot_id
            : variables.clip.video_slot_id;
      await queryClient.invalidateQueries({ queryKey: ['media-slot', variables.clip.project_id, slotID] });
    },
    onSettled: async (_result, _error, variables) => {
      if (generationControllers.current.get(variables.clip.clip_id) === variables.controller) {
        generationControllers.current.delete(variables.clip.clip_id);
      }
      await queryClient.invalidateQueries({
        queryKey: ['storyboard', variables.clip.project_id, variables.clip.episode_id],
      });
    },
  });
  const cancelQueuedGeneration = useMutation({
    mutationKey: ['storyboard-cancel-generation', mutationScope],
    mutationFn: (task: TaskItem) => changeGenerationTask(task, 'cancel'),
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
  });
  useEffect(() => {
    if (latestVideoTask?.state !== 'succeeded' || !selectedClip?.video_slot_id) return;
    void queryClient.invalidateQueries({
      queryKey: ['media-slot', projectId, selectedClip.video_slot_id],
    });
  }, [
    latestVideoTask?.result_media_version_id,
    latestVideoTask?.state,
    projectId,
    queryClient,
    selectedClip?.video_slot_id,
  ]);

  const videoProviders =
    providers.data?.providers.filter(
      (profile) => profile.generator_id.startsWith('video.') && isProviderAvailable(profile),
    ) ?? [];
  if (episode.isPending || storyboard.isPending)
    return (
      <EpisodeWorkspaceLoading
        projectID={projectId}
        episodeID={episodeId}
        projectName={episode.data?.project.name}
        episodeTitle={episode.data?.episode.title}
      />
    );
  if (episode.isError || storyboard.isError)
    return (
      <div className="page message-state">
        <h2>{t('storyboard.loadFailed')}</h2>
        <button
          className="button"
          onClick={() => {
            void episode.refetch();
            void storyboard.refetch();
          }}
        >
          {t('common.retry')}
        </button>
      </div>
    );
  return (
    <div className="creative-page storyboard-page">
      <header className="creative-heading">
        <div>
          <span className="creative-heading__project">{episode.data.project.name}</span>
          <strong>{episode.data.episode.title}</strong>
        </div>
      </header>
      <ProjectTabs projectID={projectId} episodeID={episodeId} />
      <div className={`storyboard-layout${sceneRailCollapsed ? ' is-scene-rail-collapsed' : ''}`}>
        <aside className={`scene-rail${sceneRailCollapsed ? ' is-collapsed' : ''}`}>
          <header>
            <div className="scene-rail__heading">
              <strong>{t('storyboard.scenes')}</strong>
              <small>
                {t('storyboard.episodeDuration', {
                  duration: formatMediaDuration(storyboard.data.storyboard.planned_duration_ms),
                })}
              </small>
            </div>
            <span>{scenes.length}</span>
            {!sceneRailCollapsed && (
              <button
                type="button"
                className="scene-rail__add"
                aria-label={t('storyboard.newScene')}
                title={t('storyboard.newScene')}
                onClick={() => {
                  createSceneMutation.reset();
                  setSceneCreateOpen(true);
                }}
              >
                ＋
              </button>
            )}
            {!sceneRailCollapsed && (
              <button
                type="button"
                className="workspace-rail-toggle"
                aria-label={t('storyboard.collapseScenes')}
                aria-expanded="true"
                title={t('storyboard.collapseScenes')}
                onClick={() => setSceneRailCollapsed(true)}
              >
                <ChevronIcon />
              </button>
            )}
          </header>
          {scenes.map((scene) => (
            <button
              key={scene.scene_id}
              className={scene.scene_id === selectedScene?.scene_id ? 'active' : ''}
              onClick={() => setSceneID(scene.scene_id)}
            >
              <span>{String(scene.position).padStart(2, '0')}</span>
              <div>
                <strong>{scene.title}</strong>
                <small>
                  {t('storyboard.sceneListMeta', {
                    clips: t('storyboard.clipCount', { count: scene.clips.length }),
                    duration: formatMediaDuration(scenePlannedDuration(scene)),
                  })}
                </small>
              </div>
            </button>
          ))}
        </aside>
        <main className="clip-board">
          {sceneRailCollapsed && (
            <div className="storyboard-rail-toolbar">
              <button
                type="button"
                className="workspace-rail-reopen__button"
                aria-label={t('storyboard.expandScenes')}
                aria-expanded="false"
                onClick={() => setSceneRailCollapsed(false)}
              >
                <OutlineIcon />
                <span>{t('storyboard.showScenes')}</span>
              </button>
            </div>
          )}
          {selectedScene ? (
            <>
              <header>
                <div className="scene-heading">
                  <div className="scene-heading-meta">
                    <span className="scene-heading__eyebrow">
                      {t('storyboard.sceneLabel', { count: selectedScene.position })}
                    </span>
                    <span className="scene-heading__duration">
                      {t('storyboard.duration', {
                        value: formatMediaDuration(scenePlannedDuration(selectedScene)),
                      })}
                    </span>
                    {storyboard.data.duration_warning && (
                      <em className="scene-heading__warning">{t('storyboard.durationWarning')}</em>
                    )}
                  </div>
                  <h1 className="storyboard-heading storyboard-heading--primary">{selectedScene.title}</h1>
                  <p>{selectedScene.description || t('storyboard.sceneNoDescription')}</p>
                </div>
                <button
                  className="scene-action scene-action--delete"
                  type="button"
                  onClick={() => setDeleteTarget('scene')}
                >
                  <span aria-hidden="true">⌫</span>
                  {t('storyboard.deleteScene')}
                </button>
              </header>
              <div className="clip-board__toolbar">
                {selectingClips ? (
                  <span className={`clip-selection-summary${mergeClipIDs.length ? ' has-selection' : ''}`}>
                    {mergeClipIDs.length
                      ? t('storyboard.mergeSelectionSummary', {
                          count: mergeClipIDs.length,
                          duration: Math.round(mergeDurationMilliseconds / 1000),
                        })
                      : t('storyboard.mergeSelectionEmpty')}
                    {mergeClipIDs.length >= 2 && !mergeSelectionIsAdjacent && (
                      <small>{t('storyboard.mergeSelectionAdjacent')}</small>
                    )}
                    {mergeSelectionIsAdjacent && mergeDurationMilliseconds > 30_000 && (
                      <small>{t('storyboard.mergeSelectionDuration')}</small>
                    )}
                  </span>
                ) : (
                  <h2 className="clip-board__count storyboard-heading storyboard-heading--primary">
                    {t('storyboard.clipCount', { count: selectedScene.clips.length })}
                  </h2>
                )}
                <div className="scene-actions">
                  {selectingClips ? (
                    <>
                      <button
                        className="scene-action scene-action--merge"
                        type="button"
                        disabled={!mergeSelectionIsValid}
                        title={t('storyboard.mergeWithAgentHint')}
                        onClick={openMergeClipAgent}
                      >
                        <span aria-hidden="true">✦</span>
                        {t('storyboard.mergeSelectedClips')}
                      </button>
                      <button
                        className="scene-action scene-action--cancel-selection"
                        type="button"
                        onClick={stopClipSelection}
                      >
                        {t('common.cancel')}
                      </button>
                    </>
                  ) : (
                    <>
                      {selectedScene.clips.length >= 2 && (
                        <button
                          className="scene-action scene-action--select"
                          type="button"
                          title={t('storyboard.selectClipsToMergeHint')}
                          onClick={startClipSelection}
                        >
                          <span aria-hidden="true">⇉</span>
                          {t('storyboard.selectClipsToMerge')}
                        </button>
                      )}
                      <button
                        className="scene-action"
                        type="button"
                        onClick={() => {
                          createClipMutation.reset();
                          setClipCreateOpen(true);
                        }}
                      >
                        <span aria-hidden="true">＋</span>
                        {t('storyboard.newClip')}
                      </button>
                      <button
                        className="scene-action scene-action--agent"
                        type="button"
                        title={
                          selectedClip?.scene_id === selectedScene.scene_id
                            ? t('storyboard.newClipAfterHint', { title: selectedClip.title })
                            : t('storyboard.newClipAtEndHint')
                        }
                        onClick={() =>
                          openAddClipAgent(
                            selectedScene,
                            selectedClip?.scene_id === selectedScene.scene_id
                              ? { after: selectedClip }
                              : undefined,
                          )
                        }
                      >
                        <span aria-hidden="true">✦</span>
                        {selectedClip?.scene_id === selectedScene.scene_id
                          ? `${t('storyboard.newClipAfterSelected')} · Agent`
                          : manualCopy.createWithAgent}
                      </button>
                    </>
                  )}
                </div>
              </div>
              <div className={`clip-grid${selectingClips ? ' is-selecting' : ''}`}>
                {selectedScene.clips.map((clip) => (
                  <div className="clip-grid__item" key={clip.clip_id}>
                    {!selectingClips && (
                      <button
                        type="button"
                        className="clip-insert-before"
                        aria-label={t('storyboard.insertClipBefore', { title: clip.title })}
                        title={t('storyboard.insertClipBefore', { title: clip.title })}
                        onClick={() => openAddClipAgent(selectedScene, { before: clip })}
                      >
                        <span aria-hidden="true">＋</span>
                      </button>
                    )}
                    <button
                      type="button"
                      className={`clip-grid__card${
                        selectingClips
                          ? mergeClipIDs.includes(clip.clip_id)
                            ? ' merge-selected'
                            : ''
                          : clip.clip_id === selectedClip?.clip_id
                            ? ' active'
                            : ''
                      }`}
                      {...(selectingClips ? { 'aria-pressed': mergeClipIDs.includes(clip.clip_id) } : {})}
                      onClick={(event) => {
                        if (selectingClips) {
                          toggleClipSelection(clip, event.shiftKey);
                          return;
                        }
                        navigate(
                          `/projects/${projectId}/episodes/${episodeId}/storyboard/clips/${clip.clip_id}`,
                        );
                      }}
                    >
                      {selectingClips && (
                        <i className="clip-grid__selection-mark" aria-hidden="true">
                          {mergeClipIDs.includes(clip.clip_id) ? '✓' : ''}
                        </i>
                      )}
                      <ClipFrame projectID={projectId} clip={clip} />
                      <span>
                        <strong>
                          <span className="clip-grid__index">{String(clip.position).padStart(2, '0')}</span>
                          <span className="clip-grid__title">{clip.title}</span>
                        </strong>
                        <small>
                          {Math.round(clip.planned_duration_ms / 1000)}s ·{' '}
                          {t(`storyboard.modes.${clip.generation_mode}`)}
                        </small>
                      </span>
                    </button>
                  </div>
                ))}
              </div>
              {!selectingClips && selectedClip && selectedClip.scene_id === selectedScene.scene_id && (
                <ClipFocusCard
                  key={selectedClip.clip_id}
                  projectID={projectId}
                  episodeID={episodeId}
                  clip={selectedClip}
                  firstMedia={firstMedia.data}
                  lastMedia={lastMedia.data}
                  videoMedia={videoMedia.data}
                  {...(previousClip ? { previousClip } : {})}
                  {...(previousVideoMedia.data ? { previousVideoMedia: previousVideoMedia.data } : {})}
                  {...(nextClip ? { nextClip } : {})}
                  {...(nextClip?.generation_mode !== 'reference' && nextFirstMedia.data
                    ? { nextFirstMedia: nextFirstMedia.data }
                    : {})}
                  {...(nextVideoMedia.data ? { nextVideoMedia: nextVideoMedia.data } : {})}
                  references={boundAssetReferenceMedia}
                  mentionReferences={episodeAssetReferenceMedia}
                  assetGenerationTasks={latestAssetImageTaskBySlot}
                  {...(latestVideoTask ? { latestVideoTask } : {})}
                  availableUploadedMedia={episodeReferenceMedia.data?.reference_media ?? []}
                  videoProviders={videoProviders}
                  images={providers.data?.providers ?? []}
                  saving={saving}
                  submitting={submitting && focusedGeneration?.variables?.kind === 'video'}
                  preparingEnglishPrompt={preparingEnglishPrompt}
                  onCancelPromptPreparation={() => focusedGeneration?.variables?.controller.abort()}
                  {...(submitting && focusedGeneration?.variables?.kind
                    ? { submittingKind: focusedGeneration.variables.kind }
                    : {})}
                  busy={saving || submitting || Boolean(continuityFrameRequests[selectedClip.clip_id])}
                  error={clipOperationError}
                  {...(errorSource ? { errorSource } : {})}
                  notice={jobNotice}
                  onSave={(fields) =>
                    saveClip.mutate({
                      clip: selectedClip,
                      fields,
                      assets: assets.data?.assets,
                      uploadedMedia: episodeReferenceMedia.data?.reference_media,
                    })
                  }
                  onGenerate={(
                    kind,
                    generatorID,
                    fields,
                    uploadedReferences,
                    providerModelID,
                    continuation,
                  ) => {
                    // Register synchronously so a double click cannot start another request for this Clip.
                    if (generationControllers.current.has(selectedClip.clip_id)) return;
                    const controller = new AbortController();
                    generationControllers.current.set(selectedClip.clip_id, controller);
                    generation.mutate({
                      kind,
                      clip: selectedClip,
                      generatorID,
                      ...(providerModelID ? { providerModelID } : {}),
                      ...(fields ? { fields } : {}),
                      ...(uploadedReferences ? { uploadedReferences } : {}),
                      ...(continuation ? { continuation } : {}),
                      controller,
                      operationKey: createOperationKey(),
                      media: {
                        first: firstMedia.data,
                        last: lastMedia.data,
                        video: videoMedia.data,
                        previousClip,
                        nextClip,
                        nextFirst: nextFirstMedia.data,
                        assets: episodeAssetReferenceMedia,
                        assetCatalog: assets.data?.assets,
                        uploadedMedia: episodeReferenceMedia.data?.reference_media,
                      },
                    });
                  }}
                  onCancelQueuedVideo={(task) => cancelQueuedGeneration.mutate(task)}
                  cancellingVideo={focusedCancellation?.status === 'pending'}
                  cancelVideoError={focusedCancellation?.status === 'error'}
                  onUploadFrame={uploadFrame}
                  onUploadReference={uploadReference}
                  persistedReferenceMedia={persistedReferenceMedia}
                  onRemoveUploadedReference={removeReference}
                  onVideoMediaChange={cacheMediaSlot}
                  onOpenAgent={(text) => {
                    if (text) openAgent(text);
                  }}
                  onVideoPromptTargetChange={updateFocusedVideoPromptTarget}
                  onSplit={() => openSplitClipAgent(selectedClip)}
                  onDelete={() => setDeleteTarget('clip')}
                  renderAgentTemplate={agentTemplates.render}
                />
              )}
            </>
          ) : (
            <div className="storyboard-empty">
              <span className="storyboard-empty__icon" aria-hidden="true">
                <StoryboardIcon />
              </span>
              <h2>{t('storyboard.emptyTitle')}</h2>
              <p>{t('storyboard.emptyHint')}</p>
              <div className="storyboard-empty__actions">
                <button
                  className="storyboard-manual-initialize"
                  type="button"
                  onClick={() => {
                    createSceneMutation.reset();
                    setSceneCreateOpen(true);
                  }}
                >
                  <span aria-hidden="true">＋</span>
                  {manualCopy.manualStart}
                </button>
                <button
                  className="storyboard-agent-initialize"
                  type="button"
                  onClick={() =>
                    openAgent(
                      appendAgentTemplateGuidance(
                        agentTemplates.render('storyboard.plan'),
                        storyboardModelGuidance,
                      ),
                    )
                  }
                >
                  <span aria-hidden="true">✦</span>
                  {t('storyboard.agentInitializeStoryboard')}
                  <ArrowRightIcon />
                </button>
              </div>
            </div>
          )}
        </main>
        <section className="storyboard-side-panel storyboard-side-panel--agent-only">
          <AgentDock
            projectID={projectId}
            episodeID={episodeId}
            context="storyboard"
            contextTitle={`${episode.data.project.name} / ${episode.data.episode.title}`}
            focus={{
              ...(selectedScene ? { scene_id: selectedScene.scene_id } : {}),
              ...(selectedClip && selectedClip.scene_id === selectedScene?.scene_id
                ? { clip_id: selectedClip.clip_id }
                : {}),
            }}
            {...(storyboardModelTarget
              ? {
                  videoPromptTarget: {
                    ...storyboardModelTarget,
                    ...(focusedVideoPromptTarget &&
                    focusedVideoPromptTarget.clip_id === selectedClip?.clip_id &&
                    selectedClip?.scene_id === selectedScene?.scene_id
                      ? { focus: focusedVideoPromptTarget }
                      : {}),
                  },
                }
              : {})}
            {...(agentDraftRequest ? { draftRequest: agentDraftRequest } : {})}
            onDraftApplied={(id) =>
              setAgentDraftRequest((current) => (current?.id === id ? undefined : current))
            }
          />
        </section>
      </div>
      <Dialog
        open={sceneCreateOpen}
        label={t('storyboard.newScene')}
        onClose={() => setSceneCreateOpen(false)}
      >
        <form
          className="dialog-panel"
          onSubmit={(event) => {
            event.preventDefault();
            if (sceneName.trim()) createSceneMutation.mutate();
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{episode.data.episode.title}</p>
              <h2>{t('storyboard.newScene')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setSceneCreateOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <label className="field">
              {t('storyboard.sceneName')}
              <input
                required
                autoFocus
                value={sceneName}
                onChange={(event) => setSceneName(event.target.value)}
              />
            </label>
            <label className="field">
              {manualCopy.sceneDescription}
              <textarea
                value={sceneDescription}
                placeholder={manualCopy.sceneDescriptionPlaceholder}
                onChange={(event) => setSceneDescription(event.target.value)}
              />
            </label>
            {createSceneMutation.isError && <p className="form-error">{t('storyboard.createFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button
              type="button"
              className="button button--secondary"
              onClick={() => setSceneCreateOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button className="button" disabled={!sceneName.trim() || createSceneMutation.isPending}>
              {createSceneMutation.isPending ? t('common.loading') : t('storyboard.createScene')}
            </button>
          </footer>
        </form>
      </Dialog>
      <Dialog open={clipCreateOpen} label={manualCopy.createClip} onClose={() => setClipCreateOpen(false)}>
        <form
          className="dialog-panel"
          onSubmit={(event) => {
            event.preventDefault();
            if (clipTitle.trim() && clipDuration >= 4 && clipDuration <= 30) createClipMutation.mutate();
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{selectedScene?.title}</p>
              <h2>{manualCopy.createClip}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setClipCreateOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <label className="field">
              {t('storyboard.clipName')}
              <input
                required
                autoFocus
                value={clipTitle}
                onChange={(event) => setClipTitle(event.target.value)}
              />
            </label>
            <label className="field">
              {t('storyboard.durationSeconds')}
              <input
                type="number"
                min="4"
                max="30"
                step="1"
                required
                value={clipDuration}
                onChange={(event) => setClipDuration(Number(event.target.value))}
              />
            </label>
            <label className="field">
              {t('storyboard.mode')}
              <select
                value={clipMode}
                onChange={(event) => setClipMode(event.target.value as ClipFields['generation_mode'])}
              >
                <option value="first_image">{t('storyboard.modes.first_image')}</option>
                <option value="first_last_image">{t('storyboard.modes.first_last_image')}</option>
                <option value="reference">{t('storyboard.modes.reference')}</option>
              </select>
            </label>
            {createClipMutation.isError && <p className="form-error">{manualCopy.createClipFailed}</p>}
          </div>
          <footer className="dialog-footer">
            <button
              type="button"
              className="button button--secondary"
              onClick={() => setClipCreateOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              className="button"
              disabled={
                !clipTitle.trim() || clipDuration < 4 || clipDuration > 30 || createClipMutation.isPending
              }
            >
              {createClipMutation.isPending ? t('common.loading') : manualCopy.createClip}
            </button>
          </footer>
        </form>
      </Dialog>
      <Dialog
        open={deleteTarget !== null}
        label={t(deleteTarget === 'clip' ? 'storyboard.deleteClipTitle' : 'storyboard.deleteSceneTitle')}
        onClose={() => setDeleteTarget(null)}
      >
        <div className="dialog-panel">
          <header className="dialog-header">
            <div>
              <p>{deleteTarget === 'clip' ? selectedClip?.title : selectedScene?.title}</p>
              <h2>
                {t(deleteTarget === 'clip' ? 'storyboard.deleteClipTitle' : 'storyboard.deleteSceneTitle')}
              </h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.close')}
              onClick={() => setDeleteTarget(null)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <p>{t(deleteTarget === 'clip' ? 'storyboard.deleteClipHint' : 'storyboard.deleteSceneHint')}</p>
            {(removeScene.isError || removeClip.isError) && (
              <p className="form-error">{t('storyboard.deleteFailed')}</p>
            )}
          </div>
          <footer className="dialog-footer">
            <button className="button button--secondary" onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              disabled={removeScene.isPending || removeClip.isPending}
              onClick={() => (deleteTarget === 'clip' ? removeClip.mutate() : removeScene.mutate())}
            >
              {removeScene.isPending || removeClip.isPending
                ? t('storyboard.deleting')
                : t('storyboard.deleteConfirm')}
            </button>
          </footer>
        </div>
      </Dialog>
    </div>
  );
}

function currentArtifact(details: MediaSlotDetails | undefined) {
  const version = details?.versions.find(
    (item) => item.media_version_id === details.slot.current_media_version_id,
  );
  return details?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
}

function videoReferenceRange(artifact: Artifact, fromEnd = false) {
  const trimEnd = artifact.duration_ms;
  const trimStart = fromEnd ? Math.max(0, trimEnd - maximumVideoReferenceDurationMilliseconds) : 0;
  return {
    trim_start_ms: trimStart,
    trim_end_ms: Math.min(trimEnd, trimStart + maximumVideoReferenceDurationMilliseconds),
  };
}

export function uploadedReferenceFromArtifact(
  artifact: Artifact,
  name?: string,
  trim?: MediaTrim,
): UploadedReferenceMedia {
  const role =
    artifact.kind === 'image'
      ? ('image_reference' as const)
      : artifact.kind === 'video'
        ? ('video_reference' as const)
        : ('audio_reference' as const);
  return {
    artifact,
    filename: name || artifact.original_filename,
    reference:
      role === 'image_reference'
        ? { role, artifact_id: artifact.artifact_id, name: name || artifact.original_filename }
        : {
            role,
            artifact_id: artifact.artifact_id,
            // 0/0 asks the daemon to resolve the latest Episode library selection.
            // Do not silently truncate uploaded videos to the first 15 seconds.
            trim_start_ms: trim?.trim_start_ms ?? 0,
            trim_end_ms: trim?.trim_end_ms ?? 0,
            name: name || artifact.original_filename,
          },
  };
}

export function referenceRoleForFile(file: Pick<File, 'name' | 'type'>): GenerationReference['role'] {
  const extension = file.name.toLowerCase().split('.').pop() ?? '';
  if (file.type.startsWith('image/') || ['jpg', 'jpeg', 'png', 'webp'].includes(extension))
    return 'image_reference';
  if (file.type.startsWith('audio/') || ['mp3', 'wav', 'm4a'].includes(extension)) return 'audio_reference';
  if (file.type.startsWith('video/') || ['mp4', 'mov', 'webm'].includes(extension)) return 'video_reference';
  throw new Error('unsupported reference media');
}

export function buildGenerationTarget(
  kind: GenerationKind,
  clip: Clip,
  generatorID: string,
  first: MediaSlotDetails | undefined,
  last: MediaSlotDetails | undefined,
  video: MediaSlotDetails | undefined,
  assetMedia: AssetReferenceMedia[],
  uploadedReferences: Array<GenerationReference & { name?: string }> = [],
  providerModelID?: string,
  continuation?: VideoContinuationInput,
): GenerationTarget {
  const uploadedImageCount = uploadedReferences.filter((item) => item.role === 'image_reference').length;
  const promptFamily = videoPromptFamily(generatorID);
  const promptVariant = videoPromptVariant(clip.video_prompts[clip.generation_mode], promptFamily);
  const videoPrompt = promptVariant?.prompt ?? '';
  const readyAssetReferences = usableAssetReferences(
    assetMedia,
    generatorID,
    Math.max(0, referenceLimit(generatorID, providerModelID) - uploadedImageCount),
    kind === 'video' ? videoPrompt : '',
  );
  if (kind !== 'video') {
    const isLast = kind === 'last_image';
    const frameMedia = isLast ? last : first;
    const firstFrameArtifact = currentArtifact(first);
    const framePrompt = isLast ? clip.last_image_prompt : clip.first_image_prompt;
    const namedReferences = orderByPromptMention(
      [
        ...uploadedReferences.flatMap((reference) =>
          reference.role === 'image_reference' && reference.name && framePrompt.includes(`@${reference.name}`)
            ? [{ name: reference.name, artifactID: reference.artifact_id }]
            : [],
        ),
        ...readyAssetReferences.flatMap(({ asset, artifact }) =>
          framePrompt.includes(`@${asset.identity.name}`)
            ? [{ name: asset.identity.name, artifactID: artifact.artifact_id }]
            : [],
        ),
      ],
      framePrompt,
      (item) => item.name,
    );
    const referenceIDs = [
      ...(isLast && firstFrameArtifact ? [firstFrameArtifact.artifact_id] : []),
      ...namedReferences.map((item) => item.artifactID),
    ].filter((artifactID, index, values) => values.indexOf(artifactID) === index);
    if (referenceIDs.length === 0 && readyAssetReferences[0])
      referenceIDs.push(readyAssetReferences[0].artifact.artifact_id);
    return {
      media_slot_id: isLast ? clip.last_image_slot_id : clip.first_image_slot_id,
      expected_slot_revision: frameMedia?.slot.revision ?? 0,
      prompt: isLast ? clip.last_image_prompt : clip.first_image_prompt,
      generator_id: generatorID,
      references: referenceIDs.map((artifactID) => ({ role: 'image_reference', artifact_id: artifactID })),
      parameters: {},
    };
  }
  const references: GenerationReference[] = [];
  const firstArtifact = currentArtifact(first);
  const lastArtifact = currentArtifact(last);
  if (clip.generation_mode === 'reference') {
    const mentionedAssetIDs = new Set(
      splitPromptAssetMentions(
        videoPrompt,
        assetMedia.map(({ asset }) => ({ asset_id: asset.asset_id, name: asset.identity.name })),
      ).flatMap((part) => (part.assetID ? [part.assetID] : [])),
    );
    const missingAssetNames = assetMedia.flatMap(({ asset, media }) =>
      mentionedAssetIDs.has(asset.asset_id) && !currentArtifact(media) ? [asset.identity.name] : [],
    );
    if (missingAssetNames.length > 0) throw new MissingAssetReferenceMediaError(missingAssetNames);
    if (continuation) {
      references.push({
        role: 'video_reference',
        artifact_id: continuation.artifact.artifact_id,
        ...videoReferenceRange(continuation.artifact, true),
      });
    }
    const uploadedInputs = uploadedReferences.map(({ name = '', ...reference }) => ({
      key: `upload:${reference.artifact_id}`,
      name,
      reference,
    }));
    const assetInputs = readyAssetReferences.map(({ asset, artifact }) => ({
      key: `asset:${asset.asset_id}`,
      name: asset.identity.name,
      reference: {
        role: 'image_reference' as const,
        artifact_id: artifact.artifact_id,
      },
    }));
    const orderedInputs = orderByPromptMention(
      [...uploadedInputs, ...assetInputs],
      videoPrompt,
      (item) => item.name,
    );
    references.push(...orderedInputs.map(({ reference }) => reference));
    const included = new Set<string>();
    for (let index = 0; index < references.length;) {
      const reference = references[index]!;
      if (included.has(reference.artifact_id)) references.splice(index, 1);
      else {
        included.add(reference.artifact_id);
        index += 1;
      }
    }
  } else {
    if (firstArtifact) references.push({ role: 'first_image', artifact_id: firstArtifact.artifact_id });
    if (clip.generation_mode === 'first_last_image' && lastArtifact)
      references.push({ role: 'last_image', artifact_id: lastArtifact.artifact_id });
  }
  return {
    media_slot_id: clip.video_slot_id,
    expected_slot_revision: video?.slot.revision ?? 0,
    prompt: adaptVideoPrompt(
      videoPrompt,
      clip.generation_mode,
      generatorID,
      readyAssetReferences,
      assetMedia,
      uploadedReferences,
      Boolean(continuation),
    ),
    generation_mode: clip.generation_mode,
    generator_id: generatorID,
    ...(providerModelID ? { provider_model_id: providerModelID } : {}),
    requested_duration_seconds: Math.round(clip.planned_duration_ms / 1000),
    references,
    parameters: {},
  };
}

function referenceLimit(generatorID: string, providerModelID = '', capabilities?: ProviderModelCapabilities) {
  const dynamicLimit = capabilities?.inputs?.find((input) => input.role === 'reference_image');
  if (dynamicLimit) return dynamicLimit.maximum_count;
  if (generatorID === 'video.seedance-2.5.volcengine-cn')
    return !providerModelID || providerModelID.includes('seedance-2-5') ? 30 : 9;
  if (generatorID === 'video.seedance-2.5.byteplus-global')
    return !providerModelID || providerModelID.includes('seedance-2-5') ? 30 : 9;
  if (generatorID === 'video.wan-3.0.official') return 10;
  if (generatorID.startsWith('video.h3.')) return 9;
  return 9;
}

function supportsPreviousVideoContinuation(generatorID: string) {
  return (
    previousVideoContinuationEnabled &&
    (generatorID.includes('seedance-2.5') ||
      generatorID === 'video.wan-3.0.official' ||
      generatorID.startsWith('video.h3.'))
  );
}

function usableAssetReferences(
  assetMedia: AssetReferenceMedia[],
  generatorID: string,
  limit = referenceLimit(generatorID),
  prompt = '',
) {
  return orderByPromptMention(assetMedia, prompt, ({ asset }) => asset.identity.name)
    .map(({ asset, media }) => ({ asset, artifact: currentArtifact(media) }))
    .filter(
      (item): item is { asset: EpisodeAsset; artifact: NonNullable<ReturnType<typeof currentArtifact>> } =>
        Boolean(item.artifact),
    )
    .slice(0, limit);
}

function orderByPromptMention<T>(values: T[], prompt: string, name: (value: T) => string) {
  if (!prompt) return [...values];
  const candidates = values.map((value, position) => ({
    value,
    position,
    name: name(value),
    mention: Number.POSITIVE_INFINITY,
  }));
  for (let offset = 0; offset < prompt.length;) {
    const marker = prompt.indexOf('@', offset);
    if (marker < 0) break;
    const match = candidates
      .filter((candidate) => candidate.name && prompt.startsWith(`@${candidate.name}`, marker))
      .sort((left, right) => right.name.length - left.name.length)[0];
    if (match) {
      if (!Number.isFinite(match.mention)) match.mention = marker;
      offset = marker + match.name.length + 1;
    } else offset = marker + 1;
  }
  return candidates
    .sort((left, right) => {
      return left.mention - right.mention || left.position - right.position;
    })
    .map(({ value }) => value);
}

export function assetMentionAt(value: string, cursor: number) {
  const beforeCursor = value.slice(0, cursor);
  const start = beforeCursor.lastIndexOf('@');
  if (start < 0) return null;
  const query = beforeCursor.slice(start + 1);
  if (/[@\s，。；：、！？,.!?;:()（）]/.test(query)) return null;
  return { start, end: cursor, query };
}

export function completeAssetMention(value: string, start: number, end: number, assetName: string) {
  const suffix = value.slice(end);
  const spacer = suffix === '' || !/^\s/.test(suffix) ? ' ' : '';
  return `${value.slice(0, start)}@${assetName}${spacer}${suffix}`;
}

export function clipDefinitionIssue(
  fields: ClipFields,
  _references: Array<Pick<Clip['asset_references'][number], 'name'>>,
  _promptOverride?: string,
) {
  if (
    !Number.isInteger(fields.planned_duration_seconds) ||
    fields.planned_duration_seconds < 4 ||
    fields.planned_duration_seconds > 30
  )
    return 'duration';
  return null;
}

export function generatorDurationRange(
  generatorID: string,
  modelID = '',
  capabilities?: ProviderModelCapabilities,
) {
  if (capabilities?.duration) {
    return {
      minimum: capabilities.duration.minimum_seconds,
      maximum: capabilities.duration.maximum_seconds,
      step: capabilities.duration.step_seconds,
    };
  }
  if (generatorID === 'video.h3.shotforward') return { minimum: 4, maximum: 15 };
  if (generatorID.startsWith('video.h3.'))
    return { minimum: modelID === 'MiniMax-H3-Max' ? 5 : 4, maximum: 15 };
  return { minimum: 4, maximum: 30 };
}

export function generatorDurationIssue(
  seconds: number,
  generatorID: string,
  modelID = '',
  capabilities?: ProviderModelCapabilities,
) {
  if (!generatorID) return false;
  const range = generatorDurationRange(generatorID, modelID, capabilities);
  return (
    !Number.isInteger(seconds) ||
    seconds < range.minimum ||
    seconds > range.maximum ||
    ('step' in range && range.step > 0 && (seconds - range.minimum) % range.step !== 0)
  );
}

function providerReferenceLabel(
  generatorID: string,
  role: GenerationReference['role'],
  index: number,
  prompt = '',
) {
  if (generatorID.startsWith('video.h3.')) {
    if (role === 'image_reference') return `<Picture ${index}>`;
    if (role === 'video_reference') return `<Video ${index}>`;
    return `<Audio ${index}>`;
  }
  if (generatorID === 'video.seedance-2.5.volcengine-cn') {
    if (role === 'image_reference') return `@图片${index}`;
    if (role === 'video_reference') return `@视频${index}`;
    return `@音频${index}`;
  }
  if (generatorID === 'video.seedance-2.5.byteplus-global') {
    if (role === 'image_reference') return `@Image ${index}`;
    if (role === 'video_reference') return `@Video ${index}`;
    return `@Audio ${index}`;
  }
  if (generatorID === 'video.wan-3.0.official' && /[\u3400-\u9fff]/.test(prompt)) {
    if (role === 'image_reference') return `图${index}`;
    if (role === 'video_reference') return `视频${index}`;
    return `音频${index}`;
  }
  if (role === 'image_reference') return `Image ${index}`;
  if (role === 'video_reference') return `Video ${index}`;
  return `Audio ${index}`;
}

export function adaptVideoPrompt(
  prompt: string,
  mode: Clip['generation_mode'],
  generatorID: string,
  references: ReturnType<typeof usableAssetReferences>,
  allAssets: Array<Pick<AssetReferenceMedia, 'asset'>> = references,
  uploadedReferences: Array<GenerationReference & { name?: string }> = [],
  continueFromPrevious = false,
) {
  const normalized = prompt.trim();
  const orderedReferences = orderByPromptMention(references, normalized, ({ asset }) => asset.identity.name);
  const orderedAssets = orderByPromptMention(allAssets, normalized, ({ asset }) => asset.identity.name);
  const referencedIDs = new Set(
    mode === 'reference' ? orderedReferences.map(({ asset }) => asset.asset_id) : [],
  );
  const textOnlyAssets = orderedAssets.filter(({ asset }) => !referencedIDs.has(asset.asset_id));
  const withTextAssets = textOnlyAssets.reduce(
    (result, { asset }) => result.split(`@${asset.identity.name}`).join(asset.identity.name),
    normalized,
  );
  const textDefinitions = textOnlyAssets
    .filter(({ asset }) => normalized.includes(`@${asset.identity.name}`) && asset.prompt.trim())
    .map(({ asset }) => `${asset.identity.name}：${asset.prompt.trim()}`);
  const resolved = textDefinitions.length
    ? `未提供参考图的资产文字设定：${textDefinitions.join('；')}。\n${withTextAssets}`
    : withTextAssets;
  if (
    mode !== 'reference' ||
    (!continueFromPrevious && orderedReferences.length === 0 && uploadedReferences.length === 0)
  )
    return resolved;
  const entries = orderByPromptMention(
    [
      ...uploadedReferences.flatMap((reference) =>
        reference.name ? [{ name: reference.name, role: reference.role }] : [],
      ),
      ...orderedReferences.map(({ asset }) => ({
        name: asset.identity.name,
        role: 'image_reference' as const,
      })),
    ],
    normalized,
    (entry) => entry.name,
  );
  const indexes = new Map<GenerationReference['role'], number>();
  if (continueFromPrevious) indexes.set('video_reference', 1);
  const seenNames = new Set<string>();
  const mappings: string[] = [];
  let compiled = resolved;
  for (const entry of entries) {
    if (seenNames.has(entry.name)) continue;
    seenNames.add(entry.name);
    const index = (indexes.get(entry.role) ?? 0) + 1;
    indexes.set(entry.role, index);
    const label = providerReferenceLabel(generatorID, entry.role, index, normalized);
    mappings.push(
      generatorID === 'video.wan-3.0.official' && !/[\u3400-\u9fff]/.test(normalized)
        ? `${label} is “${entry.name}”`
        : `${label} 是“${entry.name}”`,
    );
    compiled = compiled.split(`@${entry.name}`).join(label);
  }
  if (mappings.length > 0 && !generatorID.startsWith('video.h3.'))
    compiled =
      generatorID === 'video.wan-3.0.official' && !/[\u3400-\u9fff]/.test(normalized)
        ? `Reference mapping: ${mappings.join('; ')}.\n${compiled}`
        : `参考素材对应关系：${mappings.join('；')}。\n${compiled}`;
  if (!continueFromPrevious) return compiled;
  if (generatorID === 'video.seedance-2.5.volcengine-cn')
    return `生成 @视频1 之后的内容：从结尾状态自然继续，保持人物身份、服装、场景、光线、动作、运镜和声音连续。\n${compiled}`;
  if (generatorID === 'video.seedance-2.5.byteplus-global')
    return `Continue forward from the ending of @Video 1. Preserve character identity, costume, scene, lighting, motion, camera movement, and sound continuity.\n${compiled}`;
  if (generatorID === 'video.wan-3.0.official')
    return `Extend Video 1 forward from its ending state. Preserve character identity, costume, scene, lighting, motion, camera movement, and sound continuity.\n${compiled}`;
  if (generatorID.startsWith('video.h3.'))
    return `[video continuation] Continue naturally from the end of <Video 1>, preserving character identity, costume, scene, lighting, motion, camera movement, and sound.\n${compiled}`;
  return `承接视频 1 的结尾自然继续，保持人物身份、服装、场景、光线、动作、运镜和声音连续。\n${compiled}`;
}

function ClipFrame({ projectID, clip }: { projectID: string; clip: Clip }) {
  const firstFrameMedia = useQuery({
    queryKey: ['media-slot', projectID, clip.first_image_slot_id],
    queryFn: () => getMediaSlot(projectID, clip.first_image_slot_id),
    enabled: Boolean(projectID && clip.first_image_slot_id),
    staleTime: 30_000,
  });
  const videoMedia = useQuery({
    queryKey: ['media-slot', projectID, clip.video_slot_id],
    queryFn: () => getMediaSlot(projectID, clip.video_slot_id),
    enabled: Boolean(projectID && clip.video_slot_id),
    staleTime: 30_000,
  });
  const currentVideoArtifact = currentArtifact(videoMedia.data);
  const currentFirstFrameArtifact = currentArtifact(firstFrameMedia.data);
  const videoArtifact = currentVideoArtifact?.kind === 'video' ? currentVideoArtifact : undefined;
  const firstFrameArtifact =
    currentFirstFrameArtifact?.kind === 'image' ? currentFirstFrameArtifact : undefined;
  return (
    <div className={`clip-frame${videoArtifact ? ' has-video' : ''}`}>
      {videoArtifact ? (
        <>
          <video
            src={artifactContentURL(projectID, videoArtifact.artifact_id)}
            muted
            playsInline
            preload="metadata"
            onLoadedMetadata={(event) => {
              event.currentTarget.currentTime = videoThumbnailTime(videoArtifact.duration_ms);
            }}
          />
          <span className="clip-frame__video-mark" aria-hidden="true">
            ▶
          </span>
        </>
      ) : firstFrameArtifact ? (
        <img src={artifactContentURL(projectID, firstFrameArtifact.artifact_id)} alt="" />
      ) : (
        <span>＋</span>
      )}
    </div>
  );
}

const generationModeOptions: Clip['generation_mode'][] = ['first_image', 'first_last_image', 'reference'];

const activeVideoTaskStates: TaskItem['state'][] = ['queued', 'running', 'stop_requested'];
const activeImageTaskStates: TaskItem['state'][] = [
  'queued',
  'running',
  'stop_requested',
  'cancel_requested',
];

function LatestVideoGeneration({
  projectID,
  task,
  cancelling,
  cancelError,
  regenerateDisabled,
  onCancel,
  onOpenResult,
  onRegenerate,
}: {
  projectID: string;
  task: TaskItem;
  cancelling: boolean;
  cancelError: boolean;
  regenerateDisabled: boolean;
  onCancel: () => void;
  onOpenResult: () => void;
  onRegenerate: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [expanded, setExpanded] = useState(true);
  const [videoPreview, setVideoPreview] = useState<VideoPreviewValue>();
  const [now, setNow] = useState(Date.now());
  const previousTaskID = useRef(task.task_id);
  useEffect(() => {
    if (previousTaskID.current === task.task_id) return;
    previousTaskID.current = task.task_id;
    setExpanded(true);
  }, [task.task_id]);
  useEffect(() => {
    if (task.state === 'failed') setExpanded(true);
  }, [task.state]);
  useEffect(() => {
    if (!activeVideoTaskStates.includes(task.state)) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [task.state]);

  const provider = videoTaskProviderLabel(task);
  const status = t(`tasks.states.${task.state}`);
  const elapsed = formatVideoTaskElapsed(task, now);
  const message = videoTaskMessage(task, t, i18n.resolvedLanguage);
  const generationStarted =
    Boolean(task.started_at) ||
    ['running', 'stop_requested', 'succeeded', 'failed', 'stopped'].includes(task.state);
  const resultURL = task.result_artifact_id ? artifactContentURL(projectID, task.result_artifact_id) : '';

  return (
    <section className={`latest-video-generation state-${task.state}`}>
      <button
        type="button"
        className="latest-video-generation__summary"
        aria-expanded={expanded}
        onClick={() => setExpanded((current) => !current)}
      >
        <span className="latest-video-generation__state-dot" aria-hidden="true" />
        <span>
          <strong>{t('storyboard.latestVideoGeneration')}</strong>
          <small>
            {status} · {elapsed}
          </small>
        </span>
        <span className="latest-video-generation__provider">{provider}</span>
        <span
          className={`latest-video-generation__chevron${expanded ? ' is-expanded' : ''}`}
          aria-hidden="true"
        >
          ⌄
        </span>
      </button>
      {expanded && (
        <div className="latest-video-generation__body">
          <ol className="latest-video-generation__steps" aria-label={t('storyboard.generationProgress')}>
            <li className={task.state === 'queued' ? 'is-current' : 'is-done'}>
              <i />
              {t('storyboard.generationQueuedStep')}
            </li>
            <li
              className={
                task.state === 'running' || task.state === 'stop_requested'
                  ? 'is-current'
                  : generationStarted
                    ? task.state === 'failed' || task.state === 'stopped'
                      ? 'is-failed'
                      : 'is-done'
                    : ''
              }
            >
              <i />
              {t('storyboard.generationRunningStep')}
            </li>
            <li
              className={
                task.state === 'succeeded'
                  ? 'is-done'
                  : task.state === 'failed' || task.state === 'stopped'
                    ? 'is-failed'
                    : ''
              }
            >
              <i />
              {t('storyboard.generationCompletedStep')}
            </li>
          </ol>

          {task.state === 'succeeded' && resultURL ? (
            <div className="latest-video-generation__video video-expand-host">
              <video controls preload="metadata" src={resultURL} />
              <VideoExpandButton
                onClick={() =>
                  setVideoPreview({ src: resultURL, title: t('storyboard.latestVideoGeneration') })
                }
              />
            </div>
          ) : (
            <div className="latest-video-generation__progress">
              {activeVideoTaskStates.includes(task.state) && <span aria-hidden="true" />}
              <strong aria-live="polite">{message}</strong>
              {task.state === 'failed' && <RechargeAction code={task.failure_code} />}
              <small>{task.provider_model_id || task.generator_id || '—'}</small>
            </div>
          )}

          <footer>
            <div>
              {task.state === 'queued' && <small>{t('storyboard.queuedCanCancel')}</small>}
              {(task.state === 'running' || task.state === 'stop_requested') && (
                <small>{t('storyboard.runningCannotCancel')}</small>
              )}
              {cancelError && <small className="is-error">{t('storyboard.cancelQueueFailed')}</small>}
            </div>
            <div>
              {task.state === 'queued' && (
                <button
                  className="button button--secondary"
                  type="button"
                  disabled={cancelling}
                  onClick={onCancel}
                >
                  {cancelling ? t('storyboard.cancellingQueue') : t('storyboard.cancelQueue')}
                </button>
              )}
              {task.state === 'failed' && (
                <button
                  className="button button--secondary"
                  type="button"
                  disabled={regenerateDisabled}
                  onClick={onRegenerate}
                >
                  {t('storyboard.regenerateVideo')}
                </button>
              )}
              {task.state === 'succeeded' && (
                <>
                  <button
                    className="button button--secondary"
                    type="button"
                    onClick={() =>
                      void saveDownload(resultURL, `shotforward-${task.task_id}.mp4`).catch(() => undefined)
                    }
                  >
                    {t('storyboard.downloadVideo')}
                  </button>
                  <button className="button button--primary" type="button" onClick={onOpenResult}>
                    {t('storyboard.openVideoResult')}
                  </button>
                </>
              )}
              <a className="button button--secondary" href="/tasks">
                {t('storyboard.openTasks')}
              </a>
            </div>
          </footer>
        </div>
      )}
      <VideoLightbox value={videoPreview} onClose={() => setVideoPreview(undefined)} />
    </section>
  );
}

type Translate = ReturnType<typeof useTranslation>['t'];

function videoTaskMessage(task: TaskItem, t: Translate, language?: string) {
  if (task.state === 'queued') return t('storyboard.waitingForGeneration');
  if (task.state === 'running' || task.state === 'stop_requested')
    return task.progress_message || t('storyboard.providerGeneratingVideo');
  if (task.state === 'succeeded') return t('storyboard.videoGenerationSucceeded');
  if (task.state === 'failed') return taskFailureMessage(task, t, language);
  if (task.state === 'cancelled') return t('storyboard.videoGenerationCancelled');
  return t('storyboard.videoGenerationStopped');
}

export function ClipFocusCard({
  projectID,
  episodeID = '',
  clip,
  firstMedia,
  lastMedia,
  videoMedia,
  previousClip,
  previousVideoMedia,
  nextClip,
  nextFirstMedia,
  nextVideoMedia,
  references,
  mentionReferences,
  assetGenerationTasks,
  latestVideoTask,
  availableUploadedMedia = [],
  videoProviders,
  images = [],
  saving,
  submitting = false,
  submittingKind,
  preparingEnglishPrompt = false,
  onCancelPromptPreparation,
  busy,
  error,
  errorSource,
  notice,
  onSave,
  onGenerate,
  onCancelQueuedVideo,
  cancellingVideo = false,
  cancelVideoError = false,
  onUploadFrame,
  persistedReferenceMedia,
  onVideoMediaChange,
  onOpenAgent,
  onVideoPromptTargetChange,
  onSplit,
  onDelete,
  renderAgentTemplate,
}: {
  projectID: string;
  episodeID?: string;
  clip: Clip;
  firstMedia: MediaSlotDetails | undefined;
  lastMedia: MediaSlotDetails | undefined;
  videoMedia: MediaSlotDetails | undefined;
  previousClip?: Clip;
  previousVideoMedia?: MediaSlotDetails;
  nextClip?: Clip;
  nextFirstMedia?: MediaSlotDetails;
  nextVideoMedia?: MediaSlotDetails;
  references: AssetReferenceMedia[];
  mentionReferences?: AssetReferenceMedia[];
  assetGenerationTasks?: ReadonlyMap<string, TaskItem>;
  latestVideoTask?: TaskItem;
  availableUploadedMedia?: EpisodeReferenceMedia[];
  videoProviders: Awaited<ReturnType<typeof listProviders>>['providers'];
  images?: Awaited<ReturnType<typeof listProviders>>['providers'];
  saving: boolean;
  submitting?: boolean;
  submittingKind?: GenerationKind;
  preparingEnglishPrompt?: boolean;
  onCancelPromptPreparation?: () => void;
  busy: boolean;
  error: unknown;
  errorSource?: 'save' | 'generation';
  notice: string;
  onSave: (fields: ClipFields) => void;
  onGenerate: (
    kind: GenerationKind,
    generatorID: string,
    fields?: ClipFields,
    uploadedReferences?: Array<GenerationReference & { name?: string }>,
    providerModelID?: string,
    continuation?: VideoContinuationInput,
  ) => void;
  onCancelQueuedVideo?: (task: TaskItem) => void;
  cancellingVideo?: boolean;
  cancelVideoError?: boolean;
  onUploadFrame?: (kind: 'first_image' | 'last_image', file: File) => Promise<MediaSlotDetails>;
  onUploadReference?: (file: File) => Promise<UploadedReferenceMedia>;
  persistedReferenceMedia?: UploadedReferenceMedia[];
  onRemoveUploadedReference?: (artifactID: string) => Promise<void>;
  onVideoMediaChange?: (details: MediaSlotDetails) => void;
  onOpenAgent: (text?: string) => void;
  onVideoPromptTargetChange?: (target: AgentVideoPromptFocusTarget | undefined) => void;
  onSplit?: () => void;
  onDelete?: () => void;
  renderAgentTemplate?: (key: AgentTemplateKey, variables?: Record<string, string | number>) => string;
}) {
  const { t, i18n } = useTranslation();
  const [fields, setFields] = useState<ClipFields>(clipToFields(clip));
  const baselineFieldsRef = useRef(JSON.stringify(clipToFields(clip)));
  const submittedFieldsRef = useRef('');
  const videoModelOptions = videoModelChoices(videoProviders, fields.generation_mode);
  const initialVideoOption = preferredVideoModelChoice(videoModelOptions);
  const [videoGeneratorChoice, setVideoGeneratorChoice] = useState(initialVideoOption?.value ?? '');
  const selectedVideoOption =
    videoModelOptions.find((item) => item.value === videoGeneratorChoice) ?? initialVideoOption;
  const videoGenerator = selectedVideoOption?.generatorID ?? '';
  const videoProviderModel = selectedVideoOption?.modelID;
  useEffect(() => {
    onVideoPromptTargetChange?.(
      videoGenerator && videoProviderModel
        ? {
            clip_id: clip.clip_id,
            generation_mode: fields.generation_mode,
            generator_id: videoGenerator,
            provider_model_id: videoProviderModel,
          }
        : undefined,
    );
  }, [clip.clip_id, fields.generation_mode, videoGenerator, videoProviderModel, onVideoPromptTargetChange]);
  const selectedPromptFamily = videoPromptFamily(videoGenerator);
  const [uploadedReferenceMedia, setUploadedReferenceMedia] = useState<UploadedReferenceMedia[]>([]);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState('');
  const [activeView, setActiveView] = useState<'input' | 'result'>('input');
  const [keyframeMode, setKeyframeMode] = useState<Exclude<Clip['generation_mode'], 'reference'>>(
    clip.generation_mode === 'first_last_image' ? 'first_last_image' : 'first_image',
  );
  const [promptExpanded, setPromptExpanded] = useState(false);
  const [promptEditing, setPromptEditing] = useState(false);
  const [viewedVideoTaskID, setViewedVideoTaskID] = useState('');
  const [referencePickerTarget, setReferencePickerTarget] = useState<ReferencePromptTarget>();
  const [selectedAssetID, setSelectedAssetID] = useState('');
  const [previewMedia, setPreviewMedia] = useState<MediaPreviewValue>();
  const [videoPreview, setVideoPreview] = useState<VideoPreviewValue>();
  const clipActionsRef = useRef<HTMLDetailsElement>(null);
  const [videoVersionSelecting, setVideoVersionSelecting] = useState(false);
  const [videoVersionSelectionFailed, setVideoVersionSelectionFailed] = useState(false);
  const chooseVideoVersion = async (versionID: string) => {
    if (!videoMedia || videoVersionSelecting) return;
    setVideoVersionSelecting(true);
    setVideoVersionSelectionFailed(false);
    try {
      const details = await selectMediaVersion(
        projectID,
        videoMedia.slot.media_slot_id,
        versionID,
        videoMedia.slot.revision,
      );
      onVideoMediaChange?.(details);
    } catch {
      setVideoVersionSelectionFailed(true);
    } finally {
      setVideoVersionSelecting(false);
    }
  };
  useEffect(() => {
    const incoming = clipToFields(clip);
    const incomingSignature = JSON.stringify(incoming);
    const previousBaseline = baselineFieldsRef.current;
    baselineFieldsRef.current = incomingSignature;
    setFields((current) => {
      const currentSignature = JSON.stringify(current);
      const submittedSignature = submittedFieldsRef.current;
      const hasNewerLocalEdit =
        currentSignature !== previousBaseline && currentSignature !== submittedSignature;
      if (hasNewerLocalEdit) return current;
      if (incomingSignature === submittedSignature) submittedFieldsRef.current = '';
      return incoming;
    });
  }, [clip]);
  useEffect(() => {
    setUploadedReferenceMedia(persistedReferenceMedia ?? []);
    setUploadError('');
    setActiveView('input');
    setKeyframeMode(clip.generation_mode === 'first_last_image' ? 'first_last_image' : 'first_image');
    setPromptExpanded(false);
    setPromptEditing(false);
    setSelectedAssetID('');
    setReferencePickerTarget(undefined);
  }, [clip.clip_id, persistedReferenceMedia]);
  useEffect(() => setViewedVideoTaskID(''), [clip.clip_id]);
  useEffect(() => {
    const closeActions = (event: PointerEvent) => {
      if (!clipActionsRef.current?.contains(event.target as Node)) {
        clipActionsRef.current?.removeAttribute('open');
      }
    };
    document.addEventListener('pointerdown', closeActions);
    return () => document.removeEventListener('pointerdown', closeActions);
  }, []);
  useEffect(() => {
    if (!videoGeneratorChoice && initialVideoOption) setVideoGeneratorChoice(initialVideoOption.value);
  }, [videoGeneratorChoice, initialVideoOption]);
  const dirty = JSON.stringify(fields) !== JSON.stringify(clipToFields(clip));
  const allReferencePrompts = clipReferencePromptText(fields);
  const baselineReferencePrompts = clipReferencePromptText(clipToFields(clip));
  const allAssetReferences = mentionReferences ?? references;
  const selectedAssetReference = allAssetReferences.find(({ asset }) => asset.asset_id === selectedAssetID);
  const existingAssetIDs = new Set(references.map(({ asset }) => asset.asset_id));
  const effectiveAssetReferences = allAssetReferences.filter(
    ({ asset }) =>
      allReferencePrompts.includes(`@${asset.identity.name}`) ||
      (existingAssetIDs.has(asset.asset_id) && !baselineReferencePrompts.includes(`@${asset.identity.name}`)),
  );
  const persistedUploadIDs = new Set(
    (persistedReferenceMedia ?? []).map(({ artifact }) => artifact.artifact_id),
  );
  const effectiveLibraryUploads = availableUploadedMedia.filter(
    (item) =>
      allReferencePrompts.includes(`@${item.name}`) ||
      (persistedUploadIDs.has(item.artifact.artifact_id) &&
        !baselineReferencePrompts.includes(`@${item.name}`)),
  );
  const knownLibraryIDs = new Set(availableUploadedMedia.map((item) => item.artifact.artifact_id));
  const legacyUploads = uploadedReferenceMedia.filter(
    (item) => !knownLibraryIDs.has(item.artifact.artifact_id),
  );
  const promptUploadedMedia = [
    ...availableUploadedMedia,
    ...legacyUploads.map((item) => ({
      name: item.filename,
      artifact: item.artifact,
      trim_start_ms: item.reference.trim_start_ms ?? 0,
      trim_end_ms: item.reference.trim_end_ms ?? 0,
    })),
  ];
  const effectiveUploadedMedia = [
    ...effectiveLibraryUploads.map((item) => uploadedReferenceFromArtifact(item.artifact, item.name, item)),
    ...legacyUploads,
  ];
  const activePrompt = videoPromptText(fields.video_prompts[fields.generation_mode], selectedPromptFamily);
  const mentionedNames = [
    ...allAssetReferences.map(({ asset }) => asset.identity.name),
    ...availableUploadedMedia.map((item) => item.name),
    ...uploadedReferenceMedia.map(({ filename }) => filename),
  ]
    .filter((name, index, names) => names.indexOf(name) === index)
    .filter((name) => activePrompt.includes(`@${name}`));
  const definitionIssue = clipDefinitionIssue(
    fields,
    mentionedNames.map((name) => ({ name })),
    activePrompt,
  );
  const firstArtifact = currentArtifact(firstMedia);
  const lastArtifact = currentArtifact(lastMedia);
  const nextFirstArtifact =
    nextClip?.generation_mode === 'reference' ? undefined : currentArtifact(nextFirstMedia);
  const firstFrameIsAutomatic = firstArtifact?.original_filename === automaticPreviousFrameFilename;
  const lastFrameIsAutomatic = lastArtifact?.original_filename === automaticNextFrameFilename;
  const effectiveLastArtifact =
    lastArtifact && !lastFrameIsAutomatic ? lastArtifact : (nextFirstArtifact ?? lastArtifact);
  const videoArtifact = currentArtifact(videoMedia);
  const videoVersions = (videoMedia?.versions ?? [])
    .filter((version) => !version.deleted_at)
    .sort((left, right) => right.sequence_number - left.sequence_number);
  const currentVideoVersion = videoVersions.find(
    (version) => version.media_version_id === videoMedia?.slot.current_media_version_id,
  );
  const previousVideoArtifact = currentArtifact(previousVideoMedia);
  const nextVideoArtifact = currentArtifact(nextVideoMedia);
  const continuationRequested =
    previousVideoContinuationEnabled &&
    fields.generation_mode === 'reference' &&
    fields.continue_from_previous;
  const continuationProviderSupported = supportsPreviousVideoContinuation(videoGenerator);
  const continuation =
    continuationRequested && continuationProviderSupported && previousClip && previousVideoArtifact
      ? { clip: previousClip, artifact: previousVideoArtifact }
      : undefined;
  const latestVideoResultURL =
    latestVideoTask?.state === 'succeeded' && latestVideoTask.result_artifact_id
      ? artifactContentURL(projectID, latestVideoTask.result_artifact_id)
      : '';
  const displayedVideoURL = videoArtifact
    ? artifactContentURL(projectID, videoArtifact.artifact_id)
    : latestVideoResultURL;
  const latestVideoTaskActive = Boolean(
    latestVideoTask && activeVideoTaskStates.includes(latestVideoTask.state),
  );
  const firstFrameTask = assetGenerationTasks?.get(clip.first_image_slot_id);
  const lastFrameTask = assetGenerationTasks?.get(clip.last_image_slot_id);
  const firstFrameGenerating = Boolean(
    submittingKind === 'first_image' ||
    (firstFrameTask && activeImageTaskStates.includes(firstFrameTask.state)),
  );
  const lastFrameGenerating = Boolean(
    submittingKind === 'last_image' || (lastFrameTask && activeImageTaskStates.includes(lastFrameTask.state)),
  );
  const uploadedReferences = effectiveUploadedMedia.map(({ reference }) => reference);
  const uploadedImageCount = uploadedReferences.filter((item) => item.role === 'image_reference').length;
  const readyReferences = usableAssetReferences(
    effectiveAssetReferences,
    videoGenerator,
    Math.max(
      0,
      referenceLimit(videoGenerator, videoProviderModel, selectedVideoOption?.capabilities) -
        uploadedImageCount,
    ),
    activePrompt,
  );
  const referenceInputCounts: Record<string, number> = {
    image_reference: readyReferences.length,
    video_reference: continuation ? 1 : 0,
    audio_reference: 0,
  };
  for (const reference of uploadedReferences)
    referenceInputCounts[reference.role] = (referenceInputCounts[reference.role] ?? 0) + 1;
  const copySources = generationModeOptions.filter(
    (mode) =>
      mode !== fields.generation_mode &&
      videoPromptText(fields.video_prompts[mode], selectedPromptFamily).trim(),
  );
  const videoInputsReady =
    fields.generation_mode === 'reference'
      ? providerReferenceInputsReady(selectedVideoOption?.capabilities, referenceInputCounts)
      : Boolean(
          (firstArtifact || previousVideoArtifact) &&
          (fields.generation_mode !== 'first_last_image' || effectiveLastArtifact || nextVideoArtifact),
        );
  const videoDurationRange = generatorDurationRange(
    videoGenerator,
    videoProviderModel,
    selectedVideoOption?.capabilities,
  );
  const needsModelPromptAdaptation = !activePrompt.trim();
  const clipActionsBusy = busy || dirty;
  const videoDurationInvalid = generatorDurationIssue(
    fields.planned_duration_seconds,
    videoGenerator,
    videoProviderModel,
    selectedVideoOption?.capabilities,
  );
  const videoGenerationUnavailable =
    !videoGenerator ||
    needsModelPromptAdaptation ||
    !videoMedia ||
    !videoInputsReady ||
    (continuationRequested && !continuationProviderSupported) ||
    (continuationRequested && !continuation) ||
    Boolean(definitionIssue) ||
    videoDurationInvalid;
  const generationStatusReady = !videoGenerationUnavailable;
  const generationStatus = (() => {
    if (!videoGenerator) return t('storyboard.noProvider');
    if (!videoInputsReady) {
      if (fields.generation_mode === 'reference') return t('storyboard.inputNotReady');
      if (!firstArtifact && !previousVideoArtifact) return t('storyboard.generateFirstFrame');
      if (fields.generation_mode === 'first_last_image' && !effectiveLastArtifact && !nextVideoArtifact)
        return t('storyboard.generateLastFrame');
      return t('storyboard.inputNotReady');
    }
    if (needsModelPromptAdaptation) return t('storyboard.agentAdaptPrompt');
    if (videoDurationInvalid || definitionIssue || !videoMedia) return t('storyboard.inputNotReady');
    return t('storyboard.inputReady');
  })();
  useEffect(() => {
    if (activeView === 'result' && latestVideoTask?.state === 'succeeded')
      setViewedVideoTaskID(latestVideoTask.task_id);
  }, [activeView, latestVideoTask?.state, latestVideoTask?.task_id]);
  useEffect(() => {
    if (!dirty || busy || definitionIssue) return;
    const signature = JSON.stringify(fields);
    if (submittedFieldsRef.current === signature) return;
    const timeout = window.setTimeout(() => {
      submittedFieldsRef.current = signature;
      onSave(fields);
    }, 700);
    return () => window.clearTimeout(timeout);
  }, [busy, definitionIssue, dirty, fields, onSave]);
  const setActivePrompt = (value: string) => {
    if (!selectedPromptFamily) {
      setFields({
        ...fields,
        video_prompts: { ...fields.video_prompts, [fields.generation_mode]: value },
      });
      return;
    }
    setFields({
      ...fields,
      video_prompts: {
        ...fields.video_prompts,
        [fields.generation_mode]: updateVideoPromptFamily(
          fields.video_prompts[fields.generation_mode],
          selectedPromptFamily,
          value,
          {
            profile: videoPromptProfile(selectedPromptFamily, fields.generation_mode),
            generator_id: videoGenerator,
            provider_model_id: videoProviderModel ?? '',
          },
        ),
      },
    });
  };
  const referencePickerPrompt =
    referencePickerTarget === 'first_image'
      ? fields.first_image_prompt
      : referencePickerTarget === 'last_image'
        ? fields.last_image_prompt
        : activePrompt;
  const setReferencePickerPrompt = (value: string) => {
    if (referencePickerTarget === 'first_image') setFields({ ...fields, first_image_prompt: value });
    else if (referencePickerTarget === 'last_image') setFields({ ...fields, last_image_prompt: value });
    else setActivePrompt(value);
  };
  const togglePickerReference = (name: string) => {
    const mention = `@${name}`;
    setReferencePickerPrompt(
      referencePickerPrompt.includes(mention)
        ? referencePickerPrompt.split(mention).join('').replace(/ {2,}/g, ' ')
        : `${referencePickerPrompt.trimEnd()}${referencePickerPrompt.trim() ? '\n' : ''}${mention} `,
    );
  };
  const openAgentForPrompt = (kind: 'first_image' | 'last_image' | 'video') => {
    const key =
      kind === 'first_image'
        ? 'prompt.first_frame'
        : kind === 'last_image'
          ? 'prompt.last_frame'
          : selectedPromptFamily
            ? (`prompt.video.adapt.${selectedPromptFamily}` as AgentTemplateKey)
            : 'prompt.video';
    const variables = {
      clip_title: clip.title,
      clip_id: clip.clip_id,
      generation_mode: t(`storyboard.modes.${fields.generation_mode}`),
      generation_mode_id: fields.generation_mode,
      generator_id: videoGenerator,
      provider_model_id: videoProviderModel ?? '',
      prompt_family: selectedPromptFamily ?? '',
      prompt_profile: selectedPromptFamily
        ? videoPromptProfile(selectedPromptFamily, fields.generation_mode)
        : '',
    };
    onOpenAgent(
      renderAgentTemplate
        ? renderAgentTemplate(key, variables)
        : interpolateAgentTemplate(defaultAgentTemplates.en[key], variables),
    );
  };
  const submit = (kind: GenerationKind, generatorID: string, providerModelID?: string) => {
    if (kind === 'video') {
      if (continuation)
        onGenerate(
          kind,
          generatorID,
          dirty ? fields : undefined,
          uploadedReferences,
          providerModelID,
          continuation,
        );
      else if (providerModelID)
        onGenerate(kind, generatorID, dirty ? fields : undefined, uploadedReferences, providerModelID);
      else onGenerate(kind, generatorID, dirty ? fields : undefined, uploadedReferences);
      return;
    }
    onGenerate(kind, generatorID, dirty ? fields : undefined, uploadedReferences, providerModelID);
  };
  const uploadFrameFile = async (kind: 'first_image' | 'last_image', file: File) => {
    if (!onUploadFrame) return;
    setUploading(true);
    setUploadError('');
    try {
      await onUploadFrame(kind, file);
    } catch (error) {
      setUploadError(referenceUploadError(error));
    } finally {
      setUploading(false);
    }
  };
  const referenceUploadError = (error: unknown) => {
    if (error instanceof APIError) {
      if (error.status === 413 || error.code === 'media_upload_too_large')
        return t('storyboard.mediaUploadTooLarge');
      if (error.code === 'unsupported_media_type') return t('storyboard.mediaUploadUnsupported');
      if (error.code === 'aspect_ratio_mismatch' || error.code === 'output_spec_mismatch')
        return t('storyboard.mediaUploadAspectMismatch');
      if (error.code === 'revision_conflict') return t('storyboard.mediaUploadStateChanged');
      if (
        error.code === 'invalid_media' ||
        error.code === 'unreadable_media' ||
        error.code === 'invalid_media_upload'
      )
        return t('storyboard.mediaUploadInvalid');
      if (error.code === 'media_kind_mismatch') return t('storyboard.mediaUploadKindMismatch');
      if (error.code === 'media_import_unavailable') return t('storyboard.mediaUploadUnavailable');
      if (error.status === 500) return t('storyboard.mediaUploadServerFailed');
      if (error.code === 'media_upload_failed' || [502, 503, 504].includes(error.status))
        return t('storyboard.mediaUploadInterrupted');
      return t('storyboard.mediaUploadUnknown', {
        code: error.code,
        request: error.requestID || '—',
      });
    }
    if (error instanceof Error && error.message === 'unsupported reference media')
      return t('storyboard.mediaUploadUnsupported');
    return t('storyboard.mediaUploadFailed');
  };
  return (
    <section className="clip-focus-card">
      <header className="clip-focus-header">
        <div>
          <small>{t('storyboard.clipLabel', { count: clip.position })}</small>
          <h2 className="storyboard-heading storyboard-heading--primary">{clip.title}</h2>
        </div>
        <div>
          <label className="clip-focus-duration">
            <span>{t('storyboard.durationShort')}</span>
            <input
              type="number"
              min={videoDurationRange.minimum}
              max={videoDurationRange.maximum}
              step={'step' in videoDurationRange ? videoDurationRange.step : 1}
              value={fields.planned_duration_seconds}
              aria-label={t('storyboard.durationSeconds')}
              onChange={(event) =>
                setFields({ ...fields, planned_duration_seconds: Number(event.target.value) })
              }
            />
            <em>{t('storyboard.secondsUnit')}</em>
          </label>
          <button
            type="button"
            className="clip-focus-split-button"
            disabled={clipActionsBusy || !onSplit}
            title={t(clipActionsBusy ? 'storyboard.clipActionsPending' : 'storyboard.splitClipHint')}
            onClick={() => onSplit?.()}
          >
            <SplitIcon />
            {t('storyboard.splitClip')}
          </button>
          <details
            ref={clipActionsRef}
            className="clip-focus-actions"
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.currentTarget.removeAttribute('open');
                event.currentTarget.querySelector('summary')?.focus();
              }
            }}
          >
            <summary
              role="button"
              aria-haspopup="menu"
              aria-label={t('storyboard.clipActions')}
              title={t('storyboard.clipActions')}
            >
              <MoreHorizontalIcon />
            </summary>
            <div role="menu">
              <button
                type="button"
                role="menuitem"
                className="is-danger"
                disabled={clipActionsBusy || !onDelete}
                title={clipActionsBusy ? t('storyboard.clipActionsPending') : undefined}
                onClick={(event) => {
                  event.currentTarget.closest('details')?.removeAttribute('open');
                  onDelete?.();
                }}
              >
                <TrashIcon />
                {t('storyboard.deleteClip')}
              </button>
            </div>
          </details>
        </div>
      </header>
      <section className="clip-focus-controls">
        <div className="clip-focus-mode">
          <Suspense fallback={<div className="generation-workflow-select is-loading" />}>
            <GenerationWorkflowSelect
              value={fields.generation_mode}
              keyframeMode={keyframeMode}
              onChange={(generationMode) => {
                if (generationMode !== 'reference') setKeyframeMode(generationMode);
                setFields({ ...fields, generation_mode: generationMode });
                setActiveView('input');
              }}
            />
          </Suspense>
        </div>
        <div className="field clip-focus-provider">
          <h3 className="clip-focus-provider__label storyboard-heading storyboard-heading--secondary">
            <span>{t('storyboard.videoProvider')}</span>
            {videoModelOptions.length > 0 && (
              <Suspense fallback={null}>
                <VideoModelHelp selected={selectedVideoOption} />
              </Suspense>
            )}
          </h3>
          <StudioSelect
            ariaLabel={t('storyboard.videoProvider')}
            value={selectedVideoOption?.value ?? ''}
            compact
            className="clip-focus-model-select"
            placeholder={t('storyboard.noProvider')}
            placeholderIcon="◇"
            disabled={videoModelOptions.length === 0}
            options={videoModelOptions.map((model) => ({
              value: model.value,
              label: model.label,
              description: model.description,
              icon: '◈',
            }))}
            onChange={(value) => {
              setVideoGeneratorChoice(value);
              setPreferredVideoModel(value);
            }}
          />
        </div>
      </section>
      <nav className="clip-focus-tabs" role="tablist" aria-label={t('storyboard.generation')}>
        <button
          type="button"
          role="tab"
          aria-selected={activeView === 'input'}
          className={activeView === 'input' ? 'is-active' : ''}
          onClick={() => setActiveView('input')}
        >
          {t('storyboard.inputPreview')}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeView === 'result'}
          className={activeView === 'result' ? 'is-active' : ''}
          onClick={() => {
            setActiveView('result');
            if (latestVideoTask?.state === 'succeeded') setViewedVideoTaskID(latestVideoTask.task_id);
          }}
        >
          {t('storyboard.videoResult')}
          {latestVideoTaskActive && (
            <span className={`clip-focus-tab-status state-${latestVideoTask!.state}`}>
              {t(`tasks.states.${latestVideoTask!.state}`)}
            </span>
          )}
          {latestVideoTask?.state === 'succeeded' &&
            activeView !== 'result' &&
            viewedVideoTaskID !== latestVideoTask.task_id && (
              <span className="clip-focus-tab-status state-succeeded">{t('storyboard.newResult')}</span>
            )}
        </button>
      </nav>
      <div className="clip-focus-view" role="tabpanel">
        {activeView === 'result' ? (
          <section className={`clip-focus-result${displayedVideoURL ? ' has-video' : ''}`}>
            <div className="clip-focus-result__stage" data-surface="stage">
              <div
                className="clip-focus-result__player video-expand-host"
                style={
                  videoArtifact?.width && videoArtifact.height
                    ? {
                        aspectRatio: `${videoArtifact.width} / ${videoArtifact.height}`,
                        maxWidth:
                          videoArtifact.width < videoArtifact.height
                            ? '300px'
                            : videoArtifact.width === videoArtifact.height
                              ? '520px'
                              : '760px',
                      }
                    : undefined
                }
              >
                {displayedVideoURL ? (
                  <>
                    <video controls preload="metadata" src={displayedVideoURL} />
                    <VideoExpandButton
                      onClick={() => setVideoPreview({ src: displayedVideoURL, title: clip.title })}
                    />
                  </>
                ) : (
                  <div className="clip-focus-result__empty">
                    <span>▷</span>
                    <strong>{t('storyboard.videoMissing')}</strong>
                    <small>{t('storyboard.videoResultHint')}</small>
                  </div>
                )}
              </div>
            </div>
            {videoArtifact && (
              <div className="clip-focus-result__metadata">
                <span>
                  {videoArtifact.width} × {videoArtifact.height}
                </span>
                <span>{formatMediaDuration(videoArtifact.duration_ms)}</span>
              </div>
            )}
            {videoVersions.length > 0 && (
              <section className="video-version-picker" aria-label={t('storyboard.videoVersions')}>
                <header>
                  <strong>{t('storyboard.videoVersions')}</strong>
                  <span>{videoVersions.length}</span>
                </header>
                <div>
                  {videoVersions.map((version) => {
                    const artifact = videoMedia?.artifacts.find(
                      (item) => item.artifact_id === version.artifact_id,
                    );
                    const active = version.media_version_id === currentVideoVersion?.media_version_id;
                    return (
                      <button
                        type="button"
                        key={version.media_version_id}
                        className={active ? 'is-active' : ''}
                        aria-pressed={active}
                        aria-label={t('storyboard.selectVideoVersion', {
                          count: version.sequence_number,
                        })}
                        disabled={active || videoVersionSelecting}
                        onClick={() => void chooseVideoVersion(version.media_version_id)}
                      >
                        <span
                          className="video-version-picker__thumb"
                          style={
                            artifact?.width && artifact.height
                              ? { aspectRatio: `${artifact.width} / ${artifact.height}` }
                              : undefined
                          }
                        >
                          {artifact ? (
                            <video
                              src={artifactContentURL(projectID, artifact.artifact_id)}
                              muted
                              playsInline
                              preload="metadata"
                              onLoadedMetadata={(event) => {
                                event.currentTarget.currentTime = videoThumbnailTime(artifact.duration_ms);
                              }}
                            />
                          ) : (
                            <i>▷</i>
                          )}
                        </span>
                        <strong>V{version.sequence_number}</strong>
                        <small>{artifact ? `${artifact.width} × ${artifact.height}` : '—'}</small>
                        {active && <em>{t('storyboard.currentVideoVersion')}</em>}
                      </button>
                    );
                  })}
                </div>
              </section>
            )}
            {videoVersionSelectionFailed && (
              <p className="form-error">{t('storyboard.selectVideoVersionFailed')}</p>
            )}
          </section>
        ) : (
          <>
            <section
              className={`clip-focus-prompt-editor ${promptExpanded ? 'is-expanded' : 'is-collapsed'}`}
            >
              <header>
                <div>
                  <h3 className="storyboard-heading storyboard-heading--secondary">
                    {t('storyboard.videoPrompt')}
                  </h3>
                  <small>
                    {t(
                      selectedPromptFamily === 'h3' && fields.generation_mode === 'reference'
                        ? 'storyboard.h3ReferencePromptHint'
                        : `storyboard.promptModeHints.${fields.generation_mode}`,
                    )}
                  </small>
                </div>
                <div className="clip-focus-prompt-editor__actions">
                  {previousVideoContinuationEnabled && fields.generation_mode === 'reference' && (
                    <button
                      className={`clip-continuation-toggle${continuationRequested ? ' is-active' : ''}`}
                      type="button"
                      aria-pressed={continuationRequested}
                      disabled={
                        busy || !continuationProviderSupported || !previousClip || !previousVideoArtifact
                      }
                      title={t(
                        !continuationProviderSupported
                          ? 'storyboard.continuePreviousProviderHint'
                          : !previousClip
                            ? 'storyboard.continuePreviousFirstClipHint'
                            : !previousVideoArtifact
                              ? 'storyboard.continuePreviousMissingVideoHint'
                              : 'storyboard.continuePreviousHint',
                      )}
                      onClick={() =>
                        setFields({
                          ...fields,
                          continue_from_previous: !fields.continue_from_previous,
                        })
                      }
                    >
                      <span aria-hidden="true">↪</span>
                      {t('storyboard.continuePrevious')}
                    </button>
                  )}
                  {!activePrompt.trim() && copySources.length > 0 && (
                    <StudioSelect
                      ariaLabel={t('storyboard.copyPrompt')}
                      value=""
                      placeholder={t('storyboard.copyPrompt')}
                      placeholderIcon="⇄"
                      compact
                      options={copySources.map((mode) => ({
                        value: mode,
                        label: t(`storyboard.copyPromptFrom`, {
                          mode: t(`storyboard.modes.${mode}`),
                        }),
                        icon: '↳',
                      }))}
                      onChange={(source) =>
                        setActivePrompt(videoPromptText(fields.video_prompts[source], selectedPromptFamily))
                      }
                    />
                  )}
                  <AgentPromptActionButton
                    aria-label={t(
                      selectedPromptFamily ? 'storyboard.agentAdaptPrompt' : 'storyboard.agentEditPrompt',
                    )}
                    onClick={() => openAgentForPrompt('video')}
                  >
                    {t(selectedPromptFamily ? 'storyboard.agentAdaptPrompt' : 'storyboard.agentEditPrompt')}
                  </AgentPromptActionButton>
                  {promptExpanded && (
                    <button
                      className="clip-focus-prompt-editor__toggle"
                      type="button"
                      aria-pressed={promptEditing}
                      onClick={() => setPromptEditing((current) => !current)}
                    >
                      {t(promptEditing ? 'storyboard.finishChoosing' : 'script.edit')}
                    </button>
                  )}
                  <button
                    className="clip-focus-prompt-editor__toggle"
                    type="button"
                    aria-expanded={promptExpanded}
                    onClick={() => {
                      if (promptExpanded) setPromptEditing(false);
                      setPromptExpanded((current) => !current);
                    }}
                  >
                    {t(promptExpanded ? 'storyboard.collapsePrompt' : 'storyboard.expandPrompt')}
                  </button>
                </div>
              </header>
              <div className="clip-focus-prompt-editor__body">
                {promptExpanded && continuation && (
                  <div className="clip-continuation-summary" role="status">
                    <span className="clip-continuation-summary__icon" aria-hidden="true">
                      ▶
                    </span>
                    <span>
                      <strong>
                        {t('storyboard.continuePreviousSource', {
                          position: String(continuation.clip.position).padStart(2, '0'),
                          title: continuation.clip.title,
                        })}
                      </strong>
                      <small>
                        {t(
                          continuation.artifact.duration_ms > maximumVideoReferenceDurationMilliseconds
                            ? 'storyboard.continuePreviousTailHint'
                            : 'storyboard.continuePreviousFullHint',
                        )}
                      </small>
                    </span>
                    <span className="clip-continuation-summary__instruction">
                      {t('storyboard.continuePreviousInstruction')}
                    </span>
                  </div>
                )}
                <AssetMentionEditor
                  projectID={projectID}
                  value={activePrompt}
                  editing={promptExpanded && promptEditing}
                  previewClassName={
                    promptExpanded ? 'clip-focus-prompt-editor__preview' : 'clip-focus-prompt-editor__summary'
                  }
                  references={allAssetReferences}
                  uploadedMedia={promptUploadedMedia}
                  showMentionedAssets={false}
                  showLabel={false}
                  onOpenAsset={setSelectedAssetID}
                  onChange={setActivePrompt}
                />
                {!activePrompt.trim() && promptEditing && (
                  <p className="clip-focus-prompt-editor__empty-hint">
                    <span aria-hidden="true">i</span>
                    {t('storyboard.emptyModePrompt')}
                  </p>
                )}
              </div>
            </section>
            <footer className="clip-focus-generation">
              <div className="clip-focus-generation__status">
                <p className={generationStatusReady ? 'is-ready' : ''}>
                  <span>{generationStatusReady ? '✓' : '!'}</span>
                  {generationStatus}
                </p>
                <small className={saving ? 'is-saving' : ''}>
                  {t(
                    saving
                      ? 'storyboard.autoSaving'
                      : dirty
                        ? 'storyboard.autoSavePending'
                        : 'storyboard.autoSaved',
                  )}
                </small>
              </div>
              <div>
                <button
                  className="button button--primary clip-focus-generation__submit"
                  type="button"
                  disabled={busy || latestVideoTaskActive || videoGenerationUnavailable}
                  onClick={() => submit('video', videoGenerator, videoProviderModel)}
                >
                  {t(
                    preparingEnglishPrompt
                      ? 'storyboard.preparingEnglishPrompt'
                      : submitting
                        ? 'storyboard.submittingVideo'
                        : latestVideoTask?.state === 'queued'
                          ? 'storyboard.videoQueued'
                          : latestVideoTask?.state === 'running' ||
                              latestVideoTask?.state === 'stop_requested'
                            ? 'storyboard.videoGenerating'
                            : latestVideoTask?.state === 'succeeded'
                              ? 'storyboard.regenerateVideo'
                              : 'storyboard.generateVideo',
                  )}
                </button>
                {preparingEnglishPrompt && (
                  <button
                    className="button button--secondary"
                    type="button"
                    onClick={onCancelPromptPreparation}
                  >
                    {t('storyboard.cancelPromptPreparation')}
                  </button>
                )}
              </div>
            </footer>
            {latestVideoTask && (
              <LatestVideoGeneration
                projectID={projectID}
                task={latestVideoTask}
                cancelling={cancellingVideo}
                cancelError={cancelVideoError}
                regenerateDisabled={busy || videoGenerationUnavailable}
                onCancel={() => onCancelQueuedVideo?.(latestVideoTask)}
                onOpenResult={() => {
                  setViewedVideoTaskID(latestVideoTask.task_id);
                  setActiveView('result');
                }}
                onRegenerate={() => submit('video', videoGenerator, videoProviderModel)}
              />
            )}
            {fields.generation_mode !== 'reference' && (
              <section className={`clip-focus-keyframes clip-focus-keyframes--${fields.generation_mode}`}>
                <header>
                  <div>
                    <strong>
                      {t(
                        fields.generation_mode === 'first_image'
                          ? 'storyboard.currentFirstFrame'
                          : 'storyboard.keyframePreparation',
                      )}
                    </strong>
                    <small>{t('storyboard.keyframeSubmitHint')}</small>
                  </div>
                </header>
                <div className="clip-focus-keyframes__grid">
                  <Suspense fallback={<div className="generation-frame-input is-loading" />}>
                    <FrameInput
                      projectID={projectID}
                      label={t('storyboard.firstFrame')}
                      promptLabel={t('storyboard.firstPrompt')}
                      prompt={fields.first_image_prompt}
                      artifact={firstArtifact}
                      mediaReady={Boolean(firstMedia)}
                      busy={busy || uploading || firstFrameGenerating}
                      generating={firstFrameGenerating}
                      dirty={false}
                      generateLabel={
                        firstArtifact
                          ? t('storyboard.regenerateFirstFrame')
                          : t('storyboard.generateFirstFrame')
                      }
                      uploadLabel={t('storyboard.uploadFirstFrame')}
                      sourceLabel={
                        previousClip && previousVideoArtifact && firstFrameIsAutomatic
                          ? t('storyboard.automaticFirstFrameSource', {
                              position: String(previousClip.position).padStart(2, '0'),
                              title: previousClip.title,
                            })
                          : ''
                      }
                      referenceAssets={allAssetReferences}
                      referenceUploads={availableUploadedMedia}
                      imageProviders={images}
                      onChooseReferences={() => setReferencePickerTarget('first_image')}
                      onRemoveReference={(name) => {
                        const mention = `@${name}`;
                        setFields({
                          ...fields,
                          first_image_prompt: fields.first_image_prompt
                            .split(mention)
                            .join('')
                            .replace(/ {2,}/g, ' '),
                        });
                      }}
                      onPreviewReference={(artifact, title) => setPreviewMedia({ artifact, title })}
                      onPromptChange={(firstImagePrompt) =>
                        setFields({ ...fields, first_image_prompt: firstImagePrompt })
                      }
                      onGenerate={(generatorID, providerModelID) =>
                        submit('first_image', generatorID, providerModelID)
                      }
                      onUpload={(file) => uploadFrameFile('first_image', file)}
                      onOpenAgent={() => openAgentForPrompt('first_image')}
                    />
                    {fields.generation_mode === 'first_last_image' && (
                      <FrameInput
                        projectID={projectID}
                        label={t('storyboard.lastFrame')}
                        promptLabel={t('storyboard.lastPrompt')}
                        prompt={fields.last_image_prompt}
                        artifact={effectiveLastArtifact}
                        mediaReady={Boolean(lastMedia && firstArtifact)}
                        busy={busy || uploading || lastFrameGenerating}
                        generating={lastFrameGenerating}
                        dirty={false}
                        generateLabel={
                          effectiveLastArtifact
                            ? t('storyboard.regenerateLastFrame')
                            : t('storyboard.generateLastFrame')
                        }
                        uploadLabel={t('storyboard.uploadLastFrame')}
                        sourceLabel={
                          nextClip && nextFirstArtifact && (!lastArtifact || lastFrameIsAutomatic)
                            ? t('storyboard.linkedLastFrameSource', {
                                position: String(nextClip.position).padStart(2, '0'),
                                title: nextClip.title,
                              })
                            : nextClip && nextVideoArtifact && (!lastArtifact || lastFrameIsAutomatic)
                              ? t('storyboard.automaticLastFrameSource', {
                                  position: String(nextClip.position).padStart(2, '0'),
                                  title: nextClip.title,
                                })
                              : ''
                        }
                        referenceAssets={allAssetReferences}
                        referenceUploads={availableUploadedMedia}
                        imageProviders={images}
                        {...(firstArtifact ? { reservedReferenceArtifactID: firstArtifact.artifact_id } : {})}
                        onChooseReferences={() => setReferencePickerTarget('last_image')}
                        onRemoveReference={(name) => {
                          const mention = `@${name}`;
                          setFields({
                            ...fields,
                            last_image_prompt: fields.last_image_prompt
                              .split(mention)
                              .join('')
                              .replace(/ {2,}/g, ' '),
                          });
                        }}
                        onPreviewReference={(artifact, title) => setPreviewMedia({ artifact, title })}
                        onPromptChange={(lastImagePrompt) =>
                          setFields({ ...fields, last_image_prompt: lastImagePrompt })
                        }
                        onGenerate={(generatorID, providerModelID) =>
                          submit('last_image', generatorID, providerModelID)
                        }
                        onUpload={(file) => uploadFrameFile('last_image', file)}
                        onOpenAgent={() => openAgentForPrompt('last_image')}
                      />
                    )}
                  </Suspense>
                </div>
                {fields.generation_mode === 'first_last_image' && !firstArtifact && (
                  <p className="info-note">{t('storyboard.lastFrameNeedsFirst')}</p>
                )}
              </section>
            )}
          </>
        )}
      </div>
      {videoDurationInvalid && (
        <p className="form-error">
          {videoGenerator === 'video.h3.shotforward'
            ? t('storyboard.shotforwardH3Duration')
            : t('storyboard.generatorDurationInvalid', {
                minimum: videoDurationRange.minimum,
                maximum: videoDurationRange.maximum,
              })}
        </p>
      )}
      {definitionIssue && <p className="form-error">{t('storyboard.clipDefinitionInvalid')}</p>}
      {uploadError && fields.generation_mode !== 'reference' && (
        <p className="form-error" role="alert">
          {uploadError}
        </p>
      )}
      {notice && (
        <p className="success-note">
          {notice} <Link to="/tasks">{t('storyboard.openTasks')}</Link>
        </p>
      )}
      {fields.generation_mode !== 'reference' &&
        [firstFrameTask, ...(fields.generation_mode === 'first_last_image' ? [lastFrameTask] : [])]
          .filter((task) => task?.state === 'failed')
          .map(
            (task) =>
              task && (
                <p className="form-error" role="alert" key={task.task_id}>
                  <strong>
                    {t(
                      task.target_media_slot_id === clip.first_image_slot_id
                        ? 'storyboard.firstFrame'
                        : 'storyboard.lastFrame',
                    )}
                    :{' '}
                  </strong>
                  {taskFailureMessage(task, t, i18n.resolvedLanguage)}
                  <RechargeAction code={task.failure_code} />
                </p>
              ),
          )}
      {Boolean(error) && (
        <p className="form-error" role="alert">
          {errorSource === 'generation'
            ? generationSubmissionErrorMessage(error, t, i18n.resolvedLanguage)
            : t('storyboard.operationFailed')}
          <RechargeAction code={businessErrorCode(error)} />
        </p>
      )}
      {referencePickerTarget && (
        <Suspense fallback={null}>
          <ReferenceLibraryDialog
            projectID={projectID}
            clipTitle={clip.title}
            target={referencePickerTarget}
            prompt={referencePickerPrompt}
            uploads={availableUploadedMedia}
            assets={allAssetReferences}
            onToggle={togglePickerReference}
            onClose={() => setReferencePickerTarget(undefined)}
          />
        </Suspense>
      )}
      <MediaPreviewDialog
        projectID={projectID}
        value={previewMedia}
        onClose={() => setPreviewMedia(undefined)}
      />
      <VideoLightbox value={videoPreview} onClose={() => setVideoPreview(undefined)} />
      {selectedAssetReference && episodeID && (
        <AssetDetailPanel
          projectID={projectID}
          episodeID={episodeID}
          asset={selectedAssetReference.asset}
          {...(assetGenerationTasks?.get(selectedAssetReference.asset.primary_image_slot_id)
            ? {
                generationTask: assetGenerationTasks.get(selectedAssetReference.asset.primary_image_slot_id)!,
              }
            : {})}
          onClose={() => setSelectedAssetID('')}
          onAgentEditPrompt={() => {
            const variables = {
              name: selectedAssetReference.asset.identity.name,
              asset_id: selectedAssetReference.asset.asset_id,
            };
            onOpenAgent(
              renderAgentTemplate
                ? renderAgentTemplate('asset.edit_prompt', variables)
                : interpolateAgentTemplate(defaultAgentTemplates.en['asset.edit_prompt'], variables),
            );
          }}
          onAssetRenamed={(previousName, nextName) => {
            const previousMention = `@${previousName}`;
            const nextMention = `@${nextName}`;
            setFields((current) => ({
              ...current,
              first_image_prompt: current.first_image_prompt.split(previousMention).join(nextMention),
              last_image_prompt: current.last_image_prompt.split(previousMention).join(nextMention),
              video_prompts: {
                first_image: current.video_prompts.first_image.split(previousMention).join(nextMention),
                first_last_image: current.video_prompts.first_last_image
                  .split(previousMention)
                  .join(nextMention),
                reference: current.video_prompts.reference.split(previousMention).join(nextMention),
              },
            }));
          }}
        />
      )}
    </section>
  );
}

function videoThumbnailTime(durationMilliseconds: number) {
  return Math.min(0.25, Math.max(0, durationMilliseconds / 1000 - 0.05));
}

function formatMediaDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

function scenePlannedDuration(scene: { clips: Clip[] }) {
  return scene.clips.reduce((total, clip) => total + clip.planned_duration_ms, 0);
}

function AssetMentionEditor({
  projectID,
  value,
  editing = true,
  previewClassName = 'asset-mention-highlight',
  references,
  uploadedMedia = [],
  showMentionedAssets = true,
  showLabel = true,
  onOpenAsset,
  onChange,
}: {
  projectID: string;
  value: string;
  editing?: boolean;
  previewClassName?: string;
  references: AssetReferenceMedia[];
  uploadedMedia?: Array<Pick<EpisodeReferenceMedia, 'name' | 'artifact'> & MediaTrim>;
  showMentionedAssets?: boolean;
  showLabel?: boolean;
  onOpenAsset?: (assetID: string) => void;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  const promptID = useId();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const [mention, setMention] = useState<ReturnType<typeof assetMentionAt>>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const [previewAssetID, setPreviewAssetID] = useState('');
  const [previewUploadID, setPreviewUploadID] = useState('');
  const candidates = [
    ...uploadedMedia.map((item) => ({
      key: `upload:${item.artifact.artifact_id}`,
      name: item.name,
      kind: item.artifact.kind,
      artifact: item.artifact,
      source: 'upload' as const,
    })),
    ...references.map((reference) => ({
      key: `asset:${reference.asset.asset_id}`,
      name: reference.asset.identity.name,
      kind: reference.asset.identity.kind,
      artifact: currentArtifact(reference.media),
      source: 'asset' as const,
      reference,
    })),
  ];
  const suggestions = mention
    ? candidates.filter(({ name }) => name.toLocaleLowerCase().startsWith(mention.query.toLocaleLowerCase()))
    : [];
  const mentionedReferences = references.filter(({ asset }) => value.includes(`@${asset.identity.name}`));
  const mentionedUploads = uploadedMedia.filter((item) => value.includes(`@${item.name}`));
  const previewReference = references.find(({ asset }) => asset.asset_id === previewAssetID);
  const promptParts = splitPromptAssetMentions(
    value,
    candidates.map((candidate) => ({
      asset_id: candidate.key,
      name: candidate.name,
    })),
  );

  useEffect(() => {
    const input = textarea.current;
    if (!editing || !input) return;
    const minimumHeight = 168;
    const maximumHeight = 420;
    input.style.height = '0px';
    const contentHeight = input.scrollHeight || minimumHeight;
    const nextHeight = Math.min(maximumHeight, Math.max(minimumHeight, contentHeight));
    input.style.height = `${nextHeight}px`;
    input.style.overflowY = contentHeight > maximumHeight ? 'auto' : 'hidden';
  }, [editing, value]);

  const refreshMention = (nextValue: string, cursor: number) => {
    const nextMention = assetMentionAt(nextValue, cursor);
    setMention(nextMention);
    setActiveIndex(0);
  };
  const complete = (name: string) => {
    if (!mention) return;
    const nextValue = completeAssetMention(value, mention.start, mention.end, name);
    const nextCursor = mention.start + name.length + 2;
    onChange(nextValue);
    setMention(null);
    requestAnimationFrame(() => {
      textarea.current?.focus();
      textarea.current?.setSelectionRange(nextCursor, nextCursor);
    });
  };

  return (
    <div className="asset-mention-editor">
      <div className="field">
        {showLabel && <label htmlFor={promptID}>{t('storyboard.videoPrompt')}</label>}
        {showLabel && <small className="field-hint">{t('storyboard.videoPromptHint')}</small>}
        {editing ? (
          <div className="asset-mention-input is-editing">
            <textarea
              id={promptID}
              ref={textarea}
              aria-label={t('storyboard.videoPrompt')}
              rows={5}
              value={value}
              aria-autocomplete="list"
              aria-expanded={suggestions.length > 0}
              onBlur={() => setMention(null)}
              onChange={(event) => {
                const nextValue = event.currentTarget.value;
                onChange(nextValue);
                refreshMention(nextValue, event.currentTarget.selectionStart);
              }}
              onClick={(event) => refreshMention(value, event.currentTarget.selectionStart)}
              onKeyDown={(event) => {
                if (event.nativeEvent.isComposing) return;
                if (!mention || suggestions.length === 0) return;
                if (event.key === 'ArrowDown') {
                  event.preventDefault();
                  setActiveIndex((index) => (index + 1) % suggestions.length);
                } else if (event.key === 'ArrowUp') {
                  event.preventDefault();
                  setActiveIndex((index) => (index - 1 + suggestions.length) % suggestions.length);
                } else if (event.key === 'Enter' || event.key === 'Tab') {
                  event.preventDefault();
                  complete(suggestions[Math.min(activeIndex, suggestions.length - 1)]!.name);
                } else if (event.key === 'Escape') {
                  event.preventDefault();
                  setMention(null);
                }
              }}
            />
          </div>
        ) : (
          <div
            className={previewClassName}
            tabIndex={previewClassName === 'clip-focus-prompt-editor__preview' ? 0 : undefined}
            aria-label={t('storyboard.videoPrompt')}
          >
            {value.trim()
              ? promptParts.map((part, index) => {
                  const candidate = candidates.find(
                    (item) => item.key === (part.referenceKey ?? part.assetID),
                  );
                  return candidate ? (
                    <button
                      type="button"
                      className={`asset-mention-highlight__reference${part.implicitAssetMention ? ' is-implicit' : ''}`}
                      aria-label={t('storyboard.previewPromptReference', { name: candidate.name })}
                      key={`${candidate.key}:${index}`}
                      onClick={() => {
                        if (candidate.source === 'asset') {
                          if (onOpenAsset) onOpenAsset(candidate.reference.asset.asset_id);
                          else setPreviewAssetID(candidate.reference.asset.asset_id);
                        } else setPreviewUploadID(candidate.artifact.artifact_id);
                      }}
                    >
                      {part.text}
                    </button>
                  ) : (
                    <span key={index}>{part.text}</span>
                  );
                })
              : t('storyboard.emptyModePrompt')}
          </div>
        )}
      </div>
      {editing && mention && suggestions.length > 0 && (
        <div
          className="asset-mention-suggestions"
          role="listbox"
          aria-label={t('storyboard.assetSuggestions')}
        >
          {suggestions.map((suggestion, index) => {
            return (
              <button
                type="button"
                role="option"
                aria-selected={index === activeIndex}
                className={index === activeIndex ? 'active' : ''}
                key={suggestion.key}
                onMouseDown={(event) => {
                  event.preventDefault();
                  complete(suggestion.name);
                }}
              >
                <span>
                  {suggestion.artifact?.kind === 'image' ? (
                    <img src={artifactContentURL(projectID, suggestion.artifact.artifact_id)} alt="" />
                  ) : suggestion.artifact?.kind === 'video' ? (
                    '▶'
                  ) : suggestion.artifact?.kind === 'audio' ? (
                    '♫'
                  ) : (
                    '◇'
                  )}
                </span>
                <strong>@{suggestion.name}</strong>
                <small>
                  {suggestion.source === 'upload'
                    ? t(`storyboard.mediaKinds.${suggestion.kind}`)
                    : t(`assets.kinds.${suggestion.kind}`)}
                </small>
              </button>
            );
          })}
        </div>
      )}
      {showMentionedAssets && mentionedReferences.length + mentionedUploads.length > 0 && (
        <div className="asset-mention-tokens" aria-label={t('storyboard.mentionedAssets')}>
          <small>{t('storyboard.mentionedAssets')}</small>
          <div>
            {mentionedUploads.map((item) => (
              <button
                type="button"
                key={item.artifact.artifact_id}
                onClick={() => setPreviewUploadID(item.artifact.artifact_id)}
              >
                {item.artifact.kind === 'image' ? (
                  <img src={artifactContentURL(projectID, item.artifact.artifact_id)} alt="" />
                ) : (
                  <span>{item.artifact.kind === 'video' ? '▶' : '♫'}</span>
                )}
                @{item.name}
              </button>
            ))}
            {mentionedReferences.map((reference) => {
              const artifact = currentArtifact(reference.media);
              return (
                <button
                  type="button"
                  key={reference.asset.asset_id}
                  onClick={() =>
                    onOpenAsset
                      ? onOpenAsset(reference.asset.asset_id)
                      : setPreviewAssetID(reference.asset.asset_id)
                  }
                >
                  {artifact ? (
                    <img src={artifactContentURL(projectID, artifact.artifact_id)} alt="" />
                  ) : (
                    <span>◇</span>
                  )}
                  @{reference.asset.identity.name}
                </button>
              );
            })}
          </div>
        </div>
      )}
      {previewReference && (
        <Suspense fallback={null}>
          <AssetPreviewDialog
            projectID={projectID}
            reference={previewReference}
            onClose={() => setPreviewAssetID('')}
          />
        </Suspense>
      )}
      <MediaPreviewDialog
        projectID={projectID}
        value={(() => {
          const item = uploadedMedia.find((candidate) => candidate.artifact.artifact_id === previewUploadID);
          return item
            ? {
                artifact: item.artifact,
                title: item.name,
                trim_start_ms: item.trim_start_ms ?? 0,
                trim_end_ms: item.trim_end_ms ?? 0,
              }
            : undefined;
        })()}
        onClose={() => setPreviewUploadID('')}
      />
    </div>
  );
}

function clipToFields(clip: Clip): ClipFields {
  return {
    title: clip.title,
    description: clip.description,
    dialogue: clip.dialogue,
    creative_intent: clip.creative_intent,
    planned_duration_seconds: Math.round(clip.planned_duration_ms / 1000),
    first_image_prompt: clip.first_image_prompt,
    last_image_prompt: clip.last_image_prompt,
    video_prompts: { ...clip.video_prompts },
    generation_mode: clip.generation_mode,
    continue_from_previous: previousVideoContinuationEnabled && Boolean(clip.continue_from_previous),
  };
}

function clipReferencePromptText(fields: ClipFields) {
  return [
    fields.first_image_prompt,
    fields.last_image_prompt,
    allVideoPromptText(fields.video_prompts.first_image),
    allVideoPromptText(fields.video_prompts.first_last_image),
    allVideoPromptText(fields.video_prompts.reference),
  ].join('\n');
}
