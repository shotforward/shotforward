import type { EpisodeResult } from './api/episodes';

const STORAGE_KEY = 'shotforward.recent-episodes';
const MAX_RECENT_EPISODES = 8;
export const RECENT_EPISODES_CHANGED_EVENT = 'shotforward:recent-episodes-changed';

export type EpisodeWorkspaceSection = 'script' | 'assets' | 'storyboard' | 'preview';

export interface RecentEpisode {
  projectId: string;
  projectName: string;
  episodeId: string;
  episodeTitle: string;
  sequenceNumber: number;
  path: string;
  section: EpisodeWorkspaceSection;
  visitedAt: string;
}

export interface EpisodeRoute {
  projectId: string;
  episodeId: string;
  section: EpisodeWorkspaceSection;
}

const episodePathPattern =
  /^\/projects\/([^/]+)\/episodes\/([^/]+)\/(script|assets|storyboard|preview)(?:\/.*)?$/;

export function parseEpisodeRoute(pathname: string): EpisodeRoute | undefined {
  const match = episodePathPattern.exec(pathname);
  if (!match) return undefined;
  return {
    projectId: decodeURIComponent(match[1]!),
    episodeId: decodeURIComponent(match[2]!),
    section: match[3]! as EpisodeWorkspaceSection,
  };
}

function isRecentEpisode(value: unknown): value is RecentEpisode {
  if (!value || typeof value !== 'object') return false;
  const item = value as Partial<RecentEpisode>;
  return (
    typeof item.projectId === 'string' &&
    typeof item.projectName === 'string' &&
    typeof item.episodeId === 'string' &&
    typeof item.episodeTitle === 'string' &&
    typeof item.sequenceNumber === 'number' &&
    typeof item.path === 'string' &&
    parseEpisodeRoute(item.path)?.projectId === item.projectId &&
    parseEpisodeRoute(item.path)?.episodeId === item.episodeId &&
    ['script', 'assets', 'storyboard', 'preview'].includes(item.section ?? '') &&
    typeof item.visitedAt === 'string' &&
    !Number.isNaN(new Date(item.visitedAt).getTime())
  );
}

export function readRecentEpisodes(): RecentEpisode[] {
  try {
    const stored = JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '[]') as unknown;
    if (!Array.isArray(stored)) return [];
    return stored.filter(isRecentEpisode).slice(0, MAX_RECENT_EPISODES);
  } catch {
    return [];
  }
}

export function rememberRecentEpisode(
  result: EpisodeResult,
  pathname: string,
  visitedAt = new Date().toISOString(),
): RecentEpisode[] {
  const route = parseEpisodeRoute(pathname);
  if (
    !route ||
    route.projectId !== result.project.project_id ||
    route.episodeId !== result.episode.episode_id
  )
    return readRecentEpisodes();

  const item: RecentEpisode = {
    projectId: route.projectId,
    projectName: result.project.name,
    episodeId: route.episodeId,
    episodeTitle: result.episode.title,
    sequenceNumber: result.episode.sequence_number,
    path: pathname,
    section: route.section,
    visitedAt,
  };
  const recent = readRecentEpisodes().filter(
    (candidate) => candidate.projectId !== item.projectId || candidate.episodeId !== item.episodeId,
  );
  const next = [item, ...recent].slice(0, MAX_RECENT_EPISODES);
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  return next;
}

export function getEpisodeWorkspacePath(projectId: string, episodeId: string): string {
  return (
    readRecentEpisodes().find((item) => item.projectId === projectId && item.episodeId === episodeId)?.path ??
    `/projects/${encodeURIComponent(projectId)}/episodes/${encodeURIComponent(episodeId)}/script`
  );
}

export function forgetRecentEpisode(projectId: string, episodeId: string): RecentEpisode[] {
  const recent = readRecentEpisodes().filter(
    (item) => item.projectId !== projectId || item.episodeId !== episodeId,
  );
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(recent));
  window.dispatchEvent(new Event(RECENT_EPISODES_CHANGED_EVENT));
  return recent;
}

export function forgetRecentProject(projectId: string): RecentEpisode[] {
  const recent = readRecentEpisodes().filter((item) => item.projectId !== projectId);
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(recent));
  window.dispatchEvent(new Event(RECENT_EPISODES_CHANGED_EVENT));
  return recent;
}

export function renameRecentProject(projectId: string, projectName: string): RecentEpisode[] {
  const recent = readRecentEpisodes().map((item) =>
    item.projectId === projectId ? { ...item, projectName } : item,
  );
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(recent));
  window.dispatchEvent(new Event(RECENT_EPISODES_CHANGED_EVENT));
  return recent;
}
