import { NavLink, Outlet, useLocation, useNavigate } from 'react-router';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Logo } from '../components/Logo';
import { getEpisode } from '../api/episodes';
import { openProject } from '../api/projects';
import { getTaskSnapshot, useTaskEvents, type TaskItem } from '../api/tasks';
import { taskNotificationsEnabled } from '../preferences';
import {
  forgetRecentEpisode,
  RECENT_EPISODES_CHANGED_EVENT,
  parseEpisodeRoute,
  readRecentEpisodes,
  rememberRecentEpisode,
  type RecentEpisode,
} from '../recentEpisodes';

export function AppShell() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  useEffect(() => {
    let active = true;
    let stop: (() => void) | undefined;
    void import('../agentRuntime')
      .then(({ warmAgentRuntime }) => {
        if (active) stop = warmAgentRuntime(queryClient);
      })
      .catch(() => {});
    return () => {
      active = false;
      stop?.();
    };
  }, [queryClient]);
  const notifiedTasks = useRef(new Set<string>());
  const [recentEpisodes, setRecentEpisodes] = useState(readRecentEpisodes);
  const tasks = useQuery({ queryKey: ['tasks'], queryFn: getTaskSnapshot, refetchInterval: 30_000 });
  const episodeRoute = parseEpisodeRoute(location.pathname);
  const activeEpisode = useQuery({
    queryKey: ['episode', episodeRoute?.projectId, episodeRoute?.episodeId],
    queryFn: () => getEpisode(episodeRoute!.projectId, episodeRoute!.episodeId),
    enabled: Boolean(episodeRoute),
  });
  const openingEpisode = useMutation({
    mutationFn: async (item: RecentEpisode) => ({ item, result: await openProject(item.projectId) }),
    onSuccess: ({ item, result }) => {
      if (!result.episodes.some((episode) => episode.episode_id === item.episodeId)) {
        setRecentEpisodes(forgetRecentEpisode(item.projectId, item.episodeId));
        void navigate('/projects');
        return;
      }
      queryClient.setQueryData(['project', result.project.project_id], result);
      void queryClient.invalidateQueries({
        queryKey: ['episode', item.projectId, item.episodeId],
      });
      void navigate(item.path);
    },
    onError: () => void navigate('/projects'),
  });
  const notify = useCallback(
    (task: TaskItem) => {
      if (
        !taskNotificationsEnabled() ||
        !['succeeded', 'failed', 'cancelled', 'stopped'].includes(task.state)
      )
        return;
      const notificationID = `${task.task_id}:${task.state}`;
      if (notifiedTasks.current.has(notificationID)) return;
      notifiedTasks.current.add(notificationID);
      const title =
        task.state === 'succeeded' ? t('notifications.taskCompleted') : t('notifications.taskNeedsAttention');
      const body =
        task.task_type === 'export_job' ? t('notifications.exportBody') : t('notifications.generationBody');
      new Notification(title, { body, tag: `shotforward-task-${task.task_id}` });
    },
    [t],
  );
  useTaskEvents(tasks.data?.cursor, notify);
  useEffect(() => {
    if (location.pathname !== '/') window.localStorage.setItem('shotforward.last-path', location.pathname);
  }, [location.pathname]);
  useEffect(() => {
    if (activeEpisode.data) setRecentEpisodes(rememberRecentEpisode(activeEpisode.data, location.pathname));
  }, [activeEpisode.data, location.pathname]);
  useEffect(() => {
    const refresh = () => setRecentEpisodes(readRecentEpisodes());
    window.addEventListener(RECENT_EPISODES_CHANGED_EVENT, refresh);
    return () => window.removeEventListener(RECENT_EPISODES_CHANGED_EVENT, refresh);
  }, []);
  const activeTaskCount =
    tasks.data?.tasks.filter((task) => ['queued', 'running', 'stop_requested'].includes(task.state)).length ??
    0;

  return (
    <div className="studio-shell">
      <aside className="studio-sidebar">
        <Logo />
        <nav className="studio-nav" aria-label={t('common.primaryNavigation')}>
          <NavLink
            to="/projects"
            className={`studio-nav__item${location.pathname.startsWith('/projects') && !episodeRoute ? ' is-active' : ''}`}
          >
            <span>{t('nav.projects')}</span>
          </NavLink>
          {recentEpisodes.length > 0 && (
            <div className="studio-nav__recents" aria-label={t('workspace.recentEpisodes')}>
              <span className="studio-nav__recents-title">{t('workspace.recentEpisodes')}</span>
              {recentEpisodes.slice(0, 3).map((item) => {
                const active =
                  episodeRoute?.projectId === item.projectId && episodeRoute.episodeId === item.episodeId;
                return (
                  <NavLink
                    key={`${item.projectId}:${item.episodeId}`}
                    to={item.path}
                    className={`studio-nav__item studio-nav__recent${active ? ' is-active' : ''}`}
                    onClick={(event) => {
                      event.preventDefault();
                      openingEpisode.mutate(item);
                    }}
                    aria-current={active ? 'page' : undefined}
                    title={`${item.projectName} · ${item.episodeTitle}`}
                  >
                    <span className="studio-nav__recent-copy">
                      <strong>{item.episodeTitle}</strong>
                      <small>
                        {item.projectName} · {t(`workspace.sections.${item.section}`)}
                      </small>
                    </span>
                  </NavLink>
                );
              })}
            </div>
          )}
          <NavLink
            to="/tasks"
            className={({ isActive }) => `studio-nav__item${isActive ? ' is-active' : ''}`}
          >
            <span>{t('nav.tasks')}</span>
            {activeTaskCount > 0 && (
              <span
                className="studio-nav__badge"
                aria-label={t('nav.activeTasks', { count: activeTaskCount })}
              >
                {activeTaskCount}
              </span>
            )}
          </NavLink>
          <NavLink
            to="/settings"
            className={({ isActive }) =>
              `studio-nav__item studio-nav__settings-link${isActive ? ' is-active' : ''}`
            }
          >
            <span>{t('nav.settings')}</span>
          </NavLink>
        </nav>
        <div className="studio-sidebar__footer">
          <span className="studio-tagline">
            {t('nav.tagline')}
            <small>IDEAS TO FRAMES</small>
          </span>
        </div>
      </aside>
      <main className="studio-main">
        <Outlet />
      </main>
    </div>
  );
}
