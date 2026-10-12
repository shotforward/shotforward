import { requestJSON } from './client';

export interface ProjectCard {
  project_id: string;
  root_path: string;
  display_name: string;
  cover_cache_key: string;
  output_width: number;
  output_height: number;
  episode_count: number;
  last_opened_at?: string;
  project_updated_at: string;
}

interface ProjectListResponse {
  projects: ProjectCard[];
}

export interface Project {
  project_id: string;
  name: string;
  description: string;
  output_width: number;
  output_height: number;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface EpisodeSummary {
  episode_id: string;
  project_id: string;
  sequence_number: number;
  title: string;
  target_duration_ms: number;
  revision: number;
  script_revision: number;
  created_at: string;
  updated_at: string;
  script_updated_at: string;
  deleted_at?: string;
}

export interface ProjectResult {
  project: Project;
  episodes: EpisodeSummary[];
  root_path?: string;
}

export function deleteProject(projectID: string, confirmationName: string): Promise<void> {
  return requestJSON(`/api/v1/projects/${encodeURIComponent(projectID)}`, {
    method: 'DELETE',
    body: JSON.stringify({ confirmation_name: confirmationName }),
  });
}

export interface CreateProjectInput {
  root_path: string;
  name: string;
  description: string;
  output_width: number;
  output_height: number;
}

export function listProjects(): Promise<ProjectListResponse> {
  return requestJSON<ProjectListResponse>('/api/v1/projects');
}

export function createProject(input: CreateProjectInput): Promise<ProjectResult> {
  return requestJSON<ProjectResult>('/api/v1/projects', { method: 'POST', body: JSON.stringify(input) });
}

export function openProject(projectID: string): Promise<ProjectResult> {
  return requestJSON<ProjectResult>(`/api/v1/projects/${encodeURIComponent(projectID)}:open`, {
    method: 'POST',
    body: '{}',
  });
}

export function getProject(projectID: string): Promise<ProjectResult> {
  return requestJSON<ProjectResult>(`/api/v1/projects/${encodeURIComponent(projectID)}`);
}

export function updateProject(
  projectID: string,
  input: { name: string; description: string; expected_revision: number },
): Promise<ProjectResult> {
  return requestJSON<ProjectResult>(`/api/v1/projects/${encodeURIComponent(projectID)}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
  });
}

export function createEpisode(
  projectID: string,
  input: { title: string; target_duration_ms: number },
): Promise<{ project: Project; episode: EpisodeSummary; episodes: EpisodeSummary[] }> {
  return requestJSON(`/api/v1/projects/${encodeURIComponent(projectID)}/episodes`, {
    method: 'POST',
    body: JSON.stringify(input),
  });
}

export function deleteEpisode(
  projectID: string,
  episodeID: string,
  expectedRevision: number,
): Promise<ProjectResult> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}`,
    {
      method: 'DELETE',
      body: JSON.stringify({ expected_revision: expectedRevision }),
    },
  );
}

export function restoreEpisode(
  projectID: string,
  episodeID: string,
  expectedRevision: number,
): Promise<{ project: Project; episode: EpisodeSummary; episodes: EpisodeSummary[] }> {
  return requestJSON(
    `/api/v1/projects/${encodeURIComponent(projectID)}/episodes/${encodeURIComponent(episodeID)}:restore`,
    {
      method: 'POST',
      body: JSON.stringify({ expected_revision: expectedRevision }),
    },
  );
}
