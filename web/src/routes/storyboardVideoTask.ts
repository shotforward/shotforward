import type { TaskItem } from '../api/tasks';

export function videoTaskProviderLabel(task: TaskItem) {
  const providers: Record<string, string> = {
    'video.h3.shotforward': 'shotforward/MiniMax-H3 · LoRA',
    'video.seedance-2.5.volcengine-cn': 'Seedance 2.5 · Volcengine China',
    'video.seedance-2.5.byteplus-global': 'Seedance 2.5 · BytePlus Global',
    'video.h3.minimax-cn': 'MiniMax H3 · China',
    'video.h3.official': 'MiniMax H3 · Global',
    'video.wan-3.0.official': 'Wan 3.0 · Alibaba Cloud',
  };
  return providers[task.generator_id ?? ''] ?? task.generator_id ?? '—';
}

export function formatVideoTaskElapsed(task: TaskItem, now: number) {
  const start = new Date(task.started_at || task.created_at).getTime();
  const end = task.finished_at ? new Date(task.finished_at).getTime() : now;
  const seconds = Math.max(0, Math.floor((end - start) / 1_000));
  const hours = Math.floor(seconds / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  const remainder = seconds % 60;
  return hours > 0
    ? `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`
    : `${String(minutes).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`;
}
