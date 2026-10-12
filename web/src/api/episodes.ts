import { requestJSON } from './client';
import type { EpisodeSummary, Project } from './projects';

export interface Episode extends EpisodeSummary {
  script: {
    markdown: string;
    revision: number;
    updated_at: string;
    character_count: number;
  };
}

export interface EpisodeResult {
  project: Project;
  episode: Episode;
  episodes: EpisodeSummary[];
}

export function getEpisode(projectID: string, episodeID: string): Promise<EpisodeResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}`,
  );
}

export function saveScript(
  projectID: string,
  episodeID: string,
  markdown: string,
  expectedRevision: number,
): Promise<EpisodeResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/script`,
    {
      method: 'PUT',
      body: JSON.stringify({ markdown, expected_revision: expectedRevision }),
    },
  );
}

export interface ScriptImportResult extends EpisodeResult {
  format: string;
  character_count: number;
  diagnostics: Array<{ code: string; line: number; message: string }>;
}

export function importScript(
  projectID: string,
  episodeID: string,
  sourcePath: string,
  expectedRevision: number,
): Promise<ScriptImportResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}/script/imports`,
    {
      method: 'POST',
      body: JSON.stringify({ source_path: sourcePath, expected_revision: expectedRevision }),
    },
  );
}
