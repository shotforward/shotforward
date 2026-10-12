import { requestJSON } from './client';
import type { EpisodeAsset } from './assets';
import type { EpisodeSummary, Project } from './projects';
import { restoreEpisode } from './projects';
import type { Clip, Scene } from './storyboard';

export interface ProjectTrash {
  project: Project;
  episodes: EpisodeSummary[];
  assets: EpisodeAsset[];
  scenes: Scene[];
  clips: Clip[];
  storyboard_revisions: Record<string, number>;
}

export { restoreEpisode };

export function getProjectTrash(projectID: string): Promise<ProjectTrash> {
  return requestJSON(`/api/v1/projects/${encodeURIComponent(projectID)}/trash`);
}

export function restoreScene(
  projectID: string,
  episodeID: string,
  sceneID: string,
  expectedRevision: number,
  expectedStoryboardRevision: number,
): Promise<{ scene: Scene }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/scenes/${encodeURIComponent(sceneID)}:restore`,
    {
      method: 'POST',
      body: JSON.stringify({
        expected_revision: expectedRevision,
        expected_storyboard_revision: expectedStoryboardRevision,
        confirm_non_empty: false,
      }),
    },
  );
}

export function restoreClip(
  projectID: string,
  episodeID: string,
  clipID: string,
  expectedRevision: number,
  expectedStoryboardRevision: number,
): Promise<{ clip: Clip }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/clips/${encodeURIComponent(clipID)}:restore`,
    {
      method: 'POST',
      body: JSON.stringify({
        expected_revision: expectedRevision,
        expected_storyboard_revision: expectedStoryboardRevision,
      }),
    },
  );
}
