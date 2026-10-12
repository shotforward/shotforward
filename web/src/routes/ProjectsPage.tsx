import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { APIError } from '../api/client';
import { deleteProject, listProjects, openProject, type ProjectCard } from '../api/projects';
import { ArrowRightIcon, PlusIcon, SearchIcon, TrashIcon } from '../components/Icons';
import { Dialog } from '../components/Dialog';
import { Select } from '../components/Select';
import { forgetRecentProject } from '../recentEpisodes';
import { CreateProjectDialog } from './CreateProjectDialog';
import '../styles/projects.css';

function dateLabel(value: string | undefined, locale: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric', year: 'numeric' }).format(date);
}

function aspectRatio(width: number, height: number): string {
  if (width <= 0 || height <= 0) return '—';
  const divisor = (left: number, right: number): number => (right ? divisor(right, left % right) : left);
  const gcd = divisor(width, height);
  return `${width / gcd}:${height / gcd}`;
}

function ProjectTile({
  project,
  onOpen,
  onDelete,
  opening,
}: {
  project: ProjectCard;
  onOpen: () => void;
  onDelete: () => void;
  opening: boolean;
}) {
  const { t, i18n } = useTranslation();
  return (
    <article className="project-card">
      <button className="project-card__open" type="button" onClick={onOpen} disabled={opening}>
        <div className="project-card__body">
          <div className="project-card__specification">
            <span className="project-card__format">
              <i
                style={{
                  aspectRatio:
                    project.output_width > 0 && project.output_height > 0
                      ? `${project.output_width} / ${project.output_height}`
                      : '16 / 9',
                }}
                aria-hidden="true"
              />
              {aspectRatio(project.output_width, project.output_height)}
            </span>
            <span>
              {project.output_width} × {project.output_height}
            </span>
          </div>
          <div className="project-card__heading">
            <h2>{project.display_name}</h2>
            <p>{t('projects.fixedOutput')}</p>
          </div>
          <div className="project-card__meta">
            <span className="project-card__facts">
              <strong>{t('projects.episodeCount', { count: project.episode_count })}</strong>
              <span>{dateLabel(project.project_updated_at || project.last_opened_at, i18n.language)}</span>
            </span>
            <span className="project-card__continue">
              {t('projects.continue')}
              <ArrowRightIcon />
            </span>
          </div>
        </div>
      </button>
      <div className="project-card__actions">
        <button
          className="card-icon-action card-icon-action--delete"
          type="button"
          aria-label={t('projects.deleteProjectNamed', { name: project.display_name })}
          title={t('projectSettings.deleteProject')}
          onClick={onDelete}
        >
          <TrashIcon />
        </button>
      </div>
    </article>
  );
}

type ProjectSort = 'updated' | 'name';

function ProjectSortMenu({
  value,
  onChange,
}: {
  value: ProjectSort;
  onChange: (value: ProjectSort) => void;
}) {
  const { t } = useTranslation();
  const options: Array<{ value: ProjectSort; label: string }> = [
    { value: 'updated', label: t('projects.sortUpdated') },
    { value: 'name', label: t('projects.sortName') },
  ];
  return (
    <Select
      className="project-sort-select"
      ariaLabel={t('projects.sort')}
      value={value}
      options={options}
      compact
      onChange={onChange}
    />
  );
}

export function ProjectsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const [sort, setSort] = useState<ProjectSort>('updated');
  const [createOpen, setCreateOpen] = useState(false);
  const [projectToDelete, setProjectToDelete] = useState<ProjectCard | null>(null);
  const [confirmation, setConfirmation] = useState('');
  const projects = useQuery({ queryKey: ['projects'], queryFn: listProjects, retry: 1 });
  const opening = useMutation({
    mutationFn: openProject,
    onSuccess: (result) => {
      queryClient.setQueryData(['project', result.project.project_id], result);
      void navigate(`/projects/${result.project.project_id}`);
    },
  });
  const deletion = useMutation({
    mutationFn: (project: ProjectCard) => deleteProject(project.project_id, confirmation),
    onSuccess: async (_, project) => {
      forgetRecentProject(project.project_id);
      queryClient.removeQueries({ queryKey: ['project', project.project_id] });
      queryClient.removeQueries({ queryKey: ['project-trash', project.project_id] });
      setProjectToDelete(null);
      setConfirmation('');
      await queryClient.invalidateQueries({ queryKey: ['projects'] });
    },
  });
  const closeProjectDeletion = () => {
    if (deletion.isPending) return;
    deletion.reset();
    setConfirmation('');
    setProjectToDelete(null);
  };
  const visibleProjects = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    const filtered = (projects.data?.projects ?? []).filter((project) =>
      project.display_name.toLocaleLowerCase().includes(query),
    );
    return [...filtered].sort((left, right) =>
      sort === 'name'
        ? left.display_name.localeCompare(right.display_name)
        : (right.project_updated_at || right.last_opened_at || '').localeCompare(
            left.project_updated_at || left.last_opened_at || '',
          ),
    );
  }, [projects.data, search, sort]);

  return (
    <div className="page projects-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">ShotForward Studio</p>
          <h1>{t('projects.subtitle')}</h1>
        </div>
      </header>
      <section className="projects-toolbar" aria-label={t('projects.title')}>
        <div className="projects-toolbar__heading">
          <h2>{t('projects.title')}</h2>
          {projects.isSuccess && <span>{t('projects.projectTotal', { count: visibleProjects.length })}</span>}
        </div>
        <div className="projects-toolbar__actions">
          <label className="search-field">
            <SearchIcon />
            <span className="sr-only">{t('projects.search')}</span>
            <input
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('projects.search')}
            />
          </label>
          <ProjectSortMenu value={sort} onChange={setSort} />
          <button className="button projects-new-button" onClick={() => setCreateOpen(true)}>
            <PlusIcon />
            {t('projects.newProject')}
          </button>
        </div>
      </section>
      {projects.isPending && (
        <div className="project-grid" aria-label={t('common.loading')}>
          {[0, 1, 2, 3].map((item) => (
            <div className="project-card skeleton" key={item} />
          ))}
        </div>
      )}
      {projects.isError && (
        <div className="message-state" role="alert">
          <h2>{t('projects.loadError')}</h2>
          <button className="button" onClick={() => void projects.refetch()}>
            {t('common.retry')}
          </button>
        </div>
      )}
      {projects.isSuccess && visibleProjects.length > 0 && (
        <div className="project-grid">
          {visibleProjects.map((project) => (
            <ProjectTile
              key={project.project_id}
              project={project}
              onOpen={() => opening.mutate(project.project_id)}
              onDelete={() => {
                deletion.reset();
                setConfirmation('');
                setProjectToDelete(project);
              }}
              opening={opening.isPending && opening.variables === project.project_id}
            />
          ))}
        </div>
      )}
      {opening.isError && (
        <p className="inline-error" role="alert">
          {t('projects.openFailed')}
        </p>
      )}
      {projects.isSuccess && visibleProjects.length === 0 && !search && (
        <div className="empty-state">
          <div className="empty-state__plus">
            <PlusIcon />
          </div>
          <h2>{t('projects.emptyTitle')}</h2>
          <p>{t('projects.emptyDescription')}</p>
          <button className="button button--primary" onClick={() => setCreateOpen(true)}>
            {t('projects.emptyAction')}
          </button>
        </div>
      )}
      {projects.isSuccess && visibleProjects.length === 0 && search && (
        <div className="projects-no-results">
          <strong>{t('projects.noSearchResults')}</strong>
          <p>{t('projects.noSearchResultsHint', { query: search.trim() })}</p>
          <button className="button button--secondary" type="button" onClick={() => setSearch('')}>
            {t('projects.clearSearch')}
          </button>
        </div>
      )}
      <CreateProjectDialog open={createOpen} onClose={() => setCreateOpen(false)} />
      <Dialog
        open={Boolean(projectToDelete)}
        label={t('projectSettings.deleteProject')}
        onClose={closeProjectDeletion}
      >
        <div className="dialog-panel destructive-dialog">
          <header className="dialog-header">
            <div>
              <p>{t('projectSettings.permanentAction')}</p>
              <h2>{t('projectSettings.deleteProjectTitle', { name: projectToDelete?.display_name })}</h2>
            </div>
            <button
              className="icon-button"
              type="button"
              disabled={deletion.isPending}
              onClick={closeProjectDeletion}
              aria-label={t('common.close')}
            >
              ×
            </button>
          </header>
          <div className="destructive-dialog__body">
            <p>{t('projectSettings.deleteProjectWarning')}</p>
            <dl className="delete-project-details">
              <div>
                <dt>{t('projects.name')}</dt>
                <dd>{projectToDelete?.display_name}</dd>
              </div>
              <div>
                <dt>{t('projects.location')}</dt>
                <dd>{projectToDelete?.root_path || '—'}</dd>
              </div>
            </dl>
            <label className="field">
              <span>{t('projectSettings.typeNameToConfirm', { name: projectToDelete?.display_name })}</span>
              <input
                autoFocus
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                autoComplete="off"
              />
            </label>
            {deletion.isError && (
              <p className="form-error" role="alert">
                {deletion.error instanceof APIError && deletion.error.code === 'invalid_state'
                  ? t('projectSettings.deleteProjectBlocked')
                  : t('projectSettings.deleteProjectFailed', {
                      code: deletion.error instanceof APIError ? deletion.error.code : 'unknown_error',
                      request: deletion.error instanceof APIError ? deletion.error.requestID || '—' : '—',
                    })}
              </p>
            )}
          </div>
          <footer className="dialog-footer">
            <button
              className="button button--secondary"
              type="button"
              disabled={deletion.isPending}
              onClick={closeProjectDeletion}
            >
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              type="button"
              disabled={
                !projectToDelete || confirmation !== projectToDelete.display_name || deletion.isPending
              }
              onClick={() => projectToDelete && deletion.mutate(projectToDelete)}
            >
              {deletion.isPending
                ? t('projectSettings.deletingProject')
                : t('projectSettings.deletePermanently')}
            </button>
          </footer>
        </div>
      </Dialog>
    </div>
  );
}
