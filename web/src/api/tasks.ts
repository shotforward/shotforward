import { useEffect, useRef, useSyncExternalStore } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { createOperationKey, requestJSON } from './client';
import type { GenerationBatch } from './generation';

export type TaskState =
  | 'queued'
  | 'running'
  | 'stop_requested'
  | 'cancel_requested'
  | 'succeeded'
  | 'failed'
  | 'cancelled'
  | 'stopped';

export interface TaskItem {
  task_type: 'generation_job' | 'export_job';
  task_id: string;
  project_id: string;
  episode_id: string;
  state: TaskState;
  progress_stage: string;
  progress_message: string;
  failure_code: string;
  failure_message?: string;
  revision: number;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  generation_batch_id?: string;
  target_media_slot_id?: string;
  generator_id?: string;
  provider_model_id?: string;
  target_media_kind?: 'image' | 'video';
  stop_supported?: boolean;
  result_artifact_id?: string;
  result_media_version_id?: string;
  included_clip_count?: number;
  skipped_clip_count?: number;
  output_relative_path?: string;
  project_name?: string;
  episode_title?: string;
  target_kind?: 'asset' | 'clip' | 'episode' | 'audio_reference_asset';
  target_id?: string;
  target_name?: string;
  scene_id?: string;
  scene_name?: string;
  scene_position?: number;
  clip_position?: number;
  slot_role?: 'primary_image' | 'first_image' | 'last_image' | 'video' | 'primary_audio' | 'export';
}

export interface TaskSnapshot {
  stream_epoch: string;
  cursor: string;
  tasks: TaskItem[];
}

export function getTaskSnapshot(): Promise<TaskSnapshot> {
  return requestJSON('/api/v1/tasks/snapshot');
}

export function changeGenerationTask(task: TaskItem, action: 'cancel' | 'stop'): Promise<{ job: unknown }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(task.project_id)}/generation/jobs/${encodeURIComponent(task.task_id)}/${action}`,
    { method: 'POST', body: JSON.stringify({ expected_revision: task.revision }) },
  );
}

export function retryGenerationTask(task: TaskItem): Promise<GenerationBatch> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(task.project_id)}/generation/jobs/${encodeURIComponent(task.task_id)}/retry`,
    {
      method: 'POST',
      body: JSON.stringify({ expected_revision: task.revision, operation_key: createOperationKey() }),
    },
  );
}

export function cancelExportTask(task: TaskItem): Promise<{ job: unknown }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(task.project_id)}/exports/${encodeURIComponent(task.task_id)}/cancel`,
    { method: 'POST', body: JSON.stringify({ expected_revision: task.revision }) },
  );
}

const taskEvents = ['generation.job.updated', 'export.job.updated', 'tasks.resync_required'];
let taskStreamConnected = false;
const taskStreamListeners = new Set<() => void>();

function setTaskStreamConnected(next: boolean) {
  if (taskStreamConnected === next) return;
  taskStreamConnected = next;
  taskStreamListeners.forEach((listener) => listener());
}

export function useTaskConnectionStatus() {
  return useSyncExternalStore(
    (listener) => {
      taskStreamListeners.add(listener);
      return () => taskStreamListeners.delete(listener);
    },
    () => taskStreamConnected,
    () => false,
  );
}

export function useTaskEvents(cursor: string | undefined, onTask?: (task: TaskItem) => void) {
  const queryClient = useQueryClient();
  const onTaskRef = useRef(onTask);
  onTaskRef.current = onTask;
  useEffect(() => {
    if (!cursor) return;
    const source = new EventSource(`/api/v1/tasks/events?after=${encodeURIComponent(cursor)}`);
    source.onopen = () => setTaskStreamConnected(true);
    source.onerror = () => setTaskStreamConnected(false);
    const receive = (raw: Event) => {
      let payload: { type: string; task?: TaskItem };
      try {
        payload = JSON.parse((raw as MessageEvent<string>).data) as typeof payload;
      } catch {
        void queryClient.invalidateQueries({ queryKey: ['tasks'] });
        return;
      }
      if (payload.type === 'tasks.resync_required') {
        void queryClient.invalidateQueries({ queryKey: ['tasks'] });
        return;
      }
      if (!payload.task) return;
      onTaskRef.current?.(payload.task);
      queryClient.setQueryData<TaskSnapshot>(['tasks'], (current) =>
        current
          ? {
              ...current,
              tasks: [
                payload.task!,
                ...current.tasks.filter((item) => item.task_id !== payload.task!.task_id),
              ].sort((left, right) => right.updated_at.localeCompare(left.updated_at)),
            }
          : current,
      );
    };
    taskEvents.forEach((type) => source.addEventListener(type, receive));
    return () => {
      taskEvents.forEach((type) => source.removeEventListener(type, receive));
      source.close();
      setTaskStreamConnected(false);
    };
  }, [cursor, queryClient]);
}
