import { describe, expect, it } from 'vitest';
import type { TaskItem } from '../api/tasks';
import { providerLabel, resultLocation, taskContextLabel, taskTargetLabel } from './TaskCenterPage';

const task: TaskItem = {
  task_type: 'generation_job',
  task_id: 'gjb_1',
  generation_batch_id: 'gbt_1',
  project_id: 'prj_1',
  project_name: '红楼梦',
  episode_id: 'ep_1',
  episode_title: '黛玉进府',
  target_media_slot_id: 'msl_1',
  target_media_kind: 'image',
  target_kind: 'asset',
  target_id: 'ast_1',
  target_name: '林黛玉',
  slot_role: 'primary_image',
  scene_name: '荣国府门庭',
  clip_position: 1,
  generator_id: 'image.codex',
  state: 'succeeded',
  stop_supported: false,
  progress_stage: 'succeeded',
  progress_message: '',
  failure_code: '',
  revision: 1,
  created_at: '2026-09-10T10:00:00Z',
  updated_at: '2026-09-10T10:04:00Z',
};

describe('Task Center target context', () => {
  it('distinguishes tasks by target, slot role, and Episode location', () => {
    const translate = ((key: string) =>
      key === 'tasks.slotRoles.primary_image' ? '主图' : key) as Parameters<typeof taskTargetLabel>[1];

    expect(taskTargetLabel(task, translate)).toBe('林黛玉 · 主图');
    expect(taskContextLabel(task)).toBe('黛玉进府 · 荣国府门庭 / 01');
    expect(providerLabel(task)).toBe('Codex Image');
    expect(resultLocation(task)).toBe('/projects/prj_1/episodes/ep_1/assets?asset=ast_1');
  });
});
