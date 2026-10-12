import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { NavLink } from 'react-router';
import { useTranslation } from 'react-i18next';
import { listEpisodeAssets } from '../api/assets';
import { getEpisode } from '../api/episodes';
import { getExportPreview } from '../api/export';
import { getStoryboard } from '../api/storyboard';

type WorkspaceSection = 'script' | 'assets' | 'storyboard' | 'preview';

export function ProjectTabs({ projectID, episodeID }: { projectID: string; episodeID: string }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const base = `/projects/${projectID}/episodes/${episodeID}`;
  const warmWorkspace = (section: WorkspaceSection) => {
    void queryClient.prefetchQuery({
      queryKey: ['episode', projectID, episodeID],
      queryFn: () => getEpisode(projectID, episodeID),
    });
    if (section === 'assets') {
      void import('../routes/EpisodeAssetsPage');
      void queryClient.prefetchQuery({
        queryKey: ['assets', projectID, episodeID],
        queryFn: () => listEpisodeAssets(projectID, episodeID),
      });
    }
    if (section === 'storyboard') {
      void import('../routes/StoryboardWorkspacePage');
      void queryClient.prefetchQuery({
        queryKey: ['storyboard', projectID, episodeID],
        queryFn: () => getStoryboard(projectID, episodeID),
      });
    }
    if (section === 'preview') {
      void import('../routes/EpisodePreviewPage');
      void queryClient.prefetchQuery({
        queryKey: ['preview', projectID, episodeID],
        queryFn: () => getExportPreview(projectID, episodeID),
      });
    }
    if (section === 'script') void import('../routes/ScriptWorkspacePage');
  };

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      void import('../routes/ScriptWorkspacePage');
      void import('../routes/EpisodeAssetsPage');
      void import('../routes/StoryboardWorkspacePage');
      void import('../routes/EpisodePreviewPage');
    }, 300);
    return () => window.clearTimeout(timeout);
  }, []);

  return (
    <nav className="project-tabs" aria-label={t('projects.workspaceNavigation')}>
      <NavLink
        to={`${base}/script`}
        onPointerEnter={() => warmWorkspace('script')}
        onFocus={() => warmWorkspace('script')}
      >
        {t('projects.script')}
      </NavLink>
      <NavLink
        to={`${base}/assets`}
        onPointerEnter={() => warmWorkspace('assets')}
        onFocus={() => warmWorkspace('assets')}
      >
        {t('projects.assets')}
      </NavLink>
      <NavLink
        to={`${base}/storyboard`}
        onPointerEnter={() => warmWorkspace('storyboard')}
        onFocus={() => warmWorkspace('storyboard')}
      >
        {t('projects.storyboard')}
      </NavLink>
      <NavLink
        to={`${base}/preview`}
        onPointerEnter={() => warmWorkspace('preview')}
        onFocus={() => warmWorkspace('preview')}
      >
        {t('projects.preview')}
      </NavLink>
    </nav>
  );
}

export function EpisodeWorkspaceLoading({
  projectID,
  episodeID,
  projectName,
  episodeTitle,
}: {
  projectID: string;
  episodeID: string;
  projectName?: string | undefined;
  episodeTitle?: string | undefined;
}) {
  const { t } = useTranslation();
  return (
    <div className="creative-page workspace-loading" aria-busy="true">
      <header className="creative-heading">
        <div>
          <span className="creative-heading__project">{projectName || 'ShotForward'}</span>
          <strong>{episodeTitle || t('projects.episodes')}</strong>
        </div>
      </header>
      <ProjectTabs projectID={projectID} episodeID={episodeID} />
      <div className="workspace-loading__content" role="status">
        <span className="spinner" aria-hidden="true" />
        <span>{t('common.loading')}</span>
      </div>
    </div>
  );
}
