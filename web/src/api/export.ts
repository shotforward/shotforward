import { createOperationKey, requestJSON } from './client';

export interface PreviewClip {
  scene_id: string;
  clip_id: string;
  position: number;
  media_slot_id: string;
  media_slot_revision: number;
  media_version_id: string;
  media_version_revision: number;
  artifact_id: string;
  trim_in_ms: number;
  trim_out_ms: number;
  duration_ms: number;
  media_url: string;
}

export interface SkippedClip {
  scene_id: string;
  clip_id: string;
  position: number;
  reason: string;
}
export interface ExportPreview {
  schema_version: number;
  storyboard_revision: number;
  output_width: number;
  output_height: number;
  export_duration_ms: number;
  included_clip_count: number;
  skipped_clip_count: number;
  clips: PreviewClip[];
  skipped_clips: SkippedClip[];
}

export interface ExportJob {
  export_job_id: string;
  project_id: string;
  episode_id: string;
  state: string;
  progress_stage: string;
  output_relative_path?: string;
  content_url?: string;
  revision: number;
  created_at: string;
}

export function getExportPreview(projectID: string, episodeID: string): Promise<{ preview: ExportPreview }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/preview`,
  );
}

export function submitExport(
  projectID: string,
  episodeID: string,
  expectedStoryboardRevision: number,
): Promise<{ job: ExportJob }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/exports`,
    {
      method: 'POST',
      body: JSON.stringify({
        operation_key: createOperationKey(),
        expected_storyboard_revision: expectedStoryboardRevision,
      }),
    },
  );
}
