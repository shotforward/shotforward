import { useEffect, useId, useRef } from 'react';
import { useMutationState, type MutationState } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { submitGeneration } from '../api/generation';
import type { updateClip } from '../api/storyboard';
import type { changeGenerationTask, TaskItem } from '../api/tasks';

type ClipOperation = { clip: { clip_id: string } };
type GenerationOperation = ClipOperation & {
  kind: string;
  generatorID: string;
  controller: AbortController;
};

export function useStoryboardOperations<Generation extends GenerationOperation, Save extends ClipOperation>({
  projectID,
  episodeID,
  clipID,
  taskID,
}: {
  projectID: string;
  episodeID: string;
  clipID: string | undefined;
  taskID: string | undefined;
}) {
  const { t } = useTranslation();
  const scope = useId();
  const controllers = useRef(new Map<string, AbortController>());
  useEffect(() => {
    const pending = controllers.current;
    return () => {
      for (const controller of pending.values()) controller.abort();
      pending.clear();
    };
  }, [projectID, episodeID]);

  // Observers follow only the last request. Subscribe to all requests with a stable page key,
  // then select the current Clip so navigation cannot hide or reassign another Clip's request.
  const generations = useMutationState<
    MutationState<Awaited<ReturnType<typeof submitGeneration>>, Error, Generation>
  >({ filters: { mutationKey: ['storyboard-generation', scope], exact: true } });
  const saves = useMutationState<MutationState<Awaited<ReturnType<typeof updateClip>>, Error, Save>>({
    filters: { mutationKey: ['storyboard-save', scope], exact: true },
  });
  const cancellations = useMutationState<
    MutationState<Awaited<ReturnType<typeof changeGenerationTask>>, Error, TaskItem>
  >({ filters: { mutationKey: ['storyboard-cancel-generation', scope], exact: true } });

  const generation = generations.filter((state) => state.variables?.clip.clip_id === clipID).at(-1);
  const save = saves.filter((state) => state.variables?.clip.clip_id === clipID).at(-1);
  const cancelled = generation?.variables?.controller.signal.aborted;
  const generationError = cancelled ? null : generation?.error;
  const submitting = generation?.status === 'pending';
  return {
    scope,
    controllers,
    generation,
    submitting,
    saving: save?.status === 'pending',
    preparingEnglishPrompt:
      submitting &&
      generation.variables?.kind === 'video' &&
      generation.variables.generatorID.startsWith('video.h3.'),
    error: save?.error ?? generationError,
    errorSource: save?.error ? ('save' as const) : generationError ? ('generation' as const) : undefined,
    notice: cancelled
      ? t('storyboard.promptPreparationCancelled')
      : generation?.status === 'success'
        ? t('storyboard.jobQueued', { count: generation.data?.jobs.length ?? 0 })
        : '',
    cancellation: cancellations.filter((state) => state.variables?.task_id === taskID).at(-1),
  };
}
