import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { APIError } from '../api/client';
import { deleteProject, getProject, updateProject } from '../api/projects';
import { Dialog } from '../components/Dialog';
import { ProjectManagementHeader } from '../components/ProjectManagementHeader';
import { forgetRecentProject, renameRecentProject } from '../recentEpisodes';

export function ProjectSettingsPage() {
  const { projectId = '' } = useParams();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const nameInput = useRef<HTMLInputElement>(null);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [confirmation, setConfirmation] = useState('');
  const project = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => getProject(projectId),
    enabled: Boolean(projectId),
  });
  const deletion = useMutation({
    mutationFn: () => deleteProject(projectId, confirmation),
    onSuccess: async () => {
      forgetRecentProject(projectId);
      queryClient.removeQueries({ queryKey: ['project', projectId] });
      queryClient.removeQueries({ queryKey: ['project-trash', projectId] });
      await queryClient.invalidateQueries({ queryKey: ['projects'] });
      void navigate('/projects', { replace: true });
    },
  });
  useEffect(() => {
    if (!project.data) return;
    setName(project.data.project.name);
    setDescription(project.data.project.description);
  }, [project.data]);
  useEffect(() => {
    if (location.hash === '#project-name' && project.data) nameInput.current?.focus();
  }, [location.hash, project.data]);
  const update = useMutation({
    mutationFn: () =>
      updateProject(projectId, {
        name: name.trim(),
        description,
        expected_revision: project.data!.project.revision,
      }),
    onSuccess: async (result) => {
      queryClient.setQueryData(['project', projectId], {
        ...result,
        root_path: result.root_path ?? project.data?.root_path,
      });
      renameRecentProject(projectId, result.project.name);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['projects'] }),
        queryClient.invalidateQueries({
          predicate: (query) => query.queryKey[1] === projectId && query.queryKey[0] !== 'project',
        }),
      ]);
    },
  });
  const closeDeleteDialog = () => {
    if (deletion.isPending) return;
    deletion.reset();
    setConfirmation('');
    setDeleteOpen(false);
  };
  if (project.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (project.isError)
    return (
      <div className="page message-state">
        <h2>{t('projectSettings.loadFailed')}</h2>
        <button className="button" onClick={() => void project.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );
  const data = project.data.project;
  return (
    <div className="project-page">
      <ProjectManagementHeader projectID={projectId} projectName={data.name} activeSection="settings" />
      <main className="project-tool-content project-settings-content">
        <header className="project-tool-heading">
          <div>
            <h1>{t('projectSections.settings')}</h1>
            <p>{t('projectSettings.description')}</p>
          </div>
        </header>
        <section>
          <header>
            <h2>{t('projectSettings.overview')}</h2>
            <p>{t('projectSettings.overviewHint')}</p>
          </header>
          <form
            className="project-settings-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (name.trim()) update.mutate();
            }}
          >
            <label className="field" htmlFor="project-name">
              <span>{t('projects.name')}</span>
              <input
                ref={nameInput}
                id="project-name"
                value={name}
                onChange={(event) => {
                  update.reset();
                  setName(event.target.value);
                }}
                autoComplete="off"
                aria-invalid={!name.trim()}
              />
              {!name.trim() && <small className="form-error">{t('projectSettings.nameRequired')}</small>}
            </label>
            <label className="field" htmlFor="project-description">
              <span>{t('projects.description')}</span>
              <textarea
                id="project-description"
                value={description}
                onChange={(event) => {
                  update.reset();
                  setDescription(event.target.value);
                }}
                placeholder={t('projects.descriptionPlaceholder')}
              />
            </label>
            <label className="field" htmlFor="project-location">
              <span>{t('projects.location')}</span>
              <input id="project-location" value={project.data.root_path || '—'} readOnly />
              <small className="project-settings-form__hint">{t('projectSettings.storageUnchanged')}</small>
            </label>
            {update.isError && (
              <p className="form-error" role="alert">
                {update.error instanceof APIError && update.error.code === 'revision_conflict'
                  ? t('projectSettings.basicInfoConflict')
                  : t('projectSettings.basicInfoFailed')}
              </p>
            )}
            <div className="project-settings-form__actions">
              <button
                className="button"
                type="submit"
                disabled={
                  !name.trim() ||
                  update.isPending ||
                  (name.trim() === data.name && description === data.description)
                }
              >
                {update.isPending ? t('projectSettings.savingBasicInfo') : t('projectSettings.saveBasicInfo')}
              </button>
              {update.isSuccess && (
                <p className="project-settings-form__success" role="status">
                  {t('projectSettings.basicInfoSaved')}
                </p>
              )}
            </div>
          </form>
          <dl>
            <div>
              <dt>{t('projectSettings.episodes')}</dt>
              <dd>{project.data.episodes.length}</dd>
            </div>
          </dl>
        </section>
        <section>
          <header className="project-settings-section-heading">
            <div>
              <h2>{t('projectSettings.output')}</h2>
              <p>{t('projectSettings.outputHint')}</p>
            </div>
            <span>{t('projectSettings.fixed')}</span>
          </header>
          <dl>
            <div>
              <dt>{t('projectSettings.resolution')}</dt>
              <dd>
                {data.output_width} × {data.output_height}
              </dd>
            </div>
            <div>
              <dt>{t('projects.aspectRatio')}</dt>
              <dd>{ratio(data.output_width, data.output_height)}</dd>
            </div>
          </dl>
        </section>
        <section className="trash-entry">
          <div>
            <h2>{t('projectSettings.dataRecovery')}</h2>
            <p>{t('projectSettings.trashHint')}</p>
          </div>
          <Link className="button button--secondary" to={`/projects/${projectId}/trash`}>
            {t('projectSettings.openTrash')}
          </Link>
        </section>
        <section className="danger-zone">
          <div>
            <h2>{t('projectSettings.deleteProject')}</h2>
            <p>{t('projectSettings.deleteProjectHint')}</p>
          </div>
          <button
            className="button button--danger-quiet"
            type="button"
            onClick={() => {
              deletion.reset();
              setConfirmation('');
              setDeleteOpen(true);
            }}
          >
            {t('projectSettings.deleteProject')}
          </button>
        </section>
      </main>
      <Dialog open={deleteOpen} label={t('projectSettings.deleteProject')} onClose={closeDeleteDialog}>
        <div className="dialog-panel destructive-dialog">
          <header className="dialog-header">
            <div>
              <p>{t('projectSettings.permanentAction')}</p>
              <h2>{t('projectSettings.deleteProjectTitle', { name: data.name })}</h2>
            </div>
            <button
              className="icon-button"
              type="button"
              disabled={deletion.isPending}
              onClick={closeDeleteDialog}
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
                <dd>{data.name}</dd>
              </div>
              <div>
                <dt>{t('projects.location')}</dt>
                <dd>{project.data.root_path || '—'}</dd>
              </div>
            </dl>
            <label className="field">
              <span>{t('projectSettings.typeNameToConfirm', { name: data.name })}</span>
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
              onClick={closeDeleteDialog}
            >
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              type="button"
              disabled={confirmation !== data.name || deletion.isPending}
              onClick={() => deletion.mutate()}
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

function ratio(width: number, height: number) {
  const divisor = gcd(width, height);
  return `${width / divisor}:${height / divisor}`;
}
function gcd(left: number, right: number): number {
  return right ? gcd(right, left % right) : left;
}
