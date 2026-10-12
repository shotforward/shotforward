import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { Select } from '../components/Select';
import { ImageLightbox, ImagePreviewButton, type ImagePreviewValue } from '../components/ImagePreview';
import { VideoLightbox, type VideoPreviewValue } from '../components/VideoPreview';
import { artifactContentURL } from '../api/assets';
import { listProjects } from '../api/projects';
import {
  cancelExportTask,
  changeGenerationTask,
  getTaskSnapshot,
  retryGenerationTask,
  useTaskConnectionStatus,
  type TaskItem,
  type TaskState,
} from '../api/tasks';
import { saveDownload } from '../desktop';
import { taskFailureMessage } from '../taskFailure';
import { RechargeAction } from '../components/RechargeAction';
import '../styles/task-center.css';

type TaskFilter = 'all' | 'active' | 'failed' | 'succeeded';
type TaskTypeFilter = 'all' | 'image' | 'video' | 'export';
const activeStates: TaskState[] = ['queued', 'running', 'cancel_requested', 'stop_requested'];

export function TaskCenterPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const snapshot = useQuery({ queryKey: ['tasks'], queryFn: getTaskSnapshot, refetchInterval: 30_000 });
  const projects = useQuery({ queryKey: ['projects'], queryFn: listProjects, staleTime: 60_000 });
  const connected = useTaskConnectionStatus();
  const [filter, setFilter] = useState<TaskFilter>('all');
  const [typeFilter, setTypeFilter] = useState<TaskTypeFilter>('all');
  const [projectFilter, setProjectFilter] = useState('all');
  const [search, setSearch] = useState('');
  const [selectedID, setSelectedID] = useState('');
  const [previewImage, setPreviewImage] = useState<ImagePreviewValue>();
  const [previewVideo, setPreviewVideo] = useState<VideoPreviewValue>();
  const tasks = snapshot.data?.tasks ?? [];
  const projectNames = useMemo(
    () => new Map(projects.data?.projects.map((item) => [item.project_id, item.display_name])),
    [projects.data],
  );
  const filtered = useMemo(
    () =>
      tasks.filter((task) => {
        const label =
          `${taskLabel(task, t)} ${taskTargetLabel(task, t)} ${task.scene_name ?? ''}`.toLocaleLowerCase();
        const project =
          `${task.project_name ?? projectNames.get(task.project_id) ?? ''} ${task.episode_title ?? ''}`.toLocaleLowerCase();
        const query = search.trim().toLocaleLowerCase();
        const taskType = task.task_type === 'export_job' ? 'export' : (task.target_media_kind ?? 'video');
        return (
          (filter === 'all' ||
            (filter === 'active' && activeStates.includes(task.state)) ||
            task.state === filter) &&
          (typeFilter === 'all' || taskType === typeFilter) &&
          (projectFilter === 'all' || task.project_id === projectFilter) &&
          (!query || label.includes(query) || project.includes(query) || task.task_id.includes(query))
        );
      }),
    [filter, projectFilter, projectNames, search, t, tasks, typeFilter],
  );
  const selected = filtered.find((task) => task.task_id === selectedID) ?? filtered[0];
  const action = useMutation<unknown, Error, { task: TaskItem; name: 'cancel' | 'stop' | 'retry' }>({
    mutationFn: ({ task, name }: { task: TaskItem; name: 'cancel' | 'stop' | 'retry' }) =>
      name === 'retry'
        ? retryGenerationTask(task)
        : task.task_type === 'export_job'
          ? cancelExportTask(task)
          : changeGenerationTask(task, name),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
  });
  const counts = {
    active: tasks.filter((task) => activeStates.includes(task.state)).length,
    failed: tasks.filter((task) => task.state === 'failed').length,
    succeeded: tasks.filter((task) => task.state === 'succeeded').length,
  };

  if (snapshot.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (snapshot.isError)
    return (
      <div className="page message-state">
        <h2>{t('tasks.loadFailed')}</h2>
        <button className="button" onClick={() => void snapshot.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );
  return (
    <div className="task-page">
      <header className="task-heading">
        <div>
          <h1>{t('tasks.title')}</h1>
          <p>{t('tasks.description')}</p>
        </div>
        <span className={connected ? 'is-connected' : ''}>
          <i />
          {connected ? t('tasks.live') : t('tasks.polling')}
        </span>
      </header>
      <section className="task-metrics">
        <button
          className={filter === 'all' ? 'active' : ''}
          aria-pressed={filter === 'all'}
          onClick={() => setFilter('all')}
        >
          <span>
            <i className="metric-dot" />
            {t('tasks.all')}
          </span>
          <strong>{tasks.length}</strong>
        </button>
        <button
          className={filter === 'active' ? 'active' : ''}
          aria-pressed={filter === 'active'}
          onClick={() => setFilter('active')}
        >
          <span>
            <i className="metric-dot metric-dot--active" />
            {t('tasks.active')}
          </span>
          <strong>{counts.active}</strong>
        </button>
        <button
          className={filter === 'failed' ? 'active' : ''}
          aria-pressed={filter === 'failed'}
          onClick={() => setFilter('failed')}
        >
          <span>
            <i className="metric-dot metric-dot--failed" />
            {t('tasks.needsAttention')}
          </span>
          <strong>{counts.failed}</strong>
        </button>
        <button
          className={filter === 'succeeded' ? 'active' : ''}
          aria-pressed={filter === 'succeeded'}
          onClick={() => setFilter('succeeded')}
        >
          <span>
            <i className="metric-dot metric-dot--done" />
            {t('tasks.completed')}
          </span>
          <strong>{counts.succeeded}</strong>
        </button>
      </section>
      <section className="task-filterbar" aria-label={t('tasks.clearFilters')}>
        <label className="task-search">
          <span aria-hidden="true">⌕</span>
          <input
            type="search"
            aria-label={t('tasks.search')}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('tasks.search')}
          />
        </label>
        <Select
          ariaLabel={t('tasks.allTypes')}
          value={typeFilter}
          options={[
            { value: 'all', label: t('tasks.allTypes') },
            { value: 'image', label: t('tasks.imageType') },
            { value: 'video', label: t('tasks.videoType') },
            { value: 'export', label: t('tasks.exportType') },
          ]}
          compact
          onChange={setTypeFilter}
        />
        <Select
          ariaLabel={t('tasks.allProjects')}
          value={projectFilter}
          options={[
            { value: 'all', label: t('tasks.allProjects') },
            ...(projects.data?.projects.map((project) => ({
              value: project.project_id,
              label: project.display_name,
            })) ?? []),
          ]}
          compact
          onChange={setProjectFilter}
        />
        {(search || typeFilter !== 'all' || projectFilter !== 'all') && (
          <button
            onClick={() => {
              setSearch('');
              setTypeFilter('all');
              setProjectFilter('all');
            }}
          >
            {t('tasks.clearFilters')}
          </button>
        )}
        <span>{t('tasks.resultCount', { count: filtered.length })}</span>
      </section>
      <div className="task-workspace">
        <main className="task-table">
          <header>
            <span>{t('tasks.task')}</span>
            <span>{t('tasks.project')}</span>
            <span>{t('tasks.status')}</span>
            <span>{t('tasks.updated')}</span>
          </header>
          {filtered.length ? (
            filtered.map((task) => (
              <article
                key={task.task_id}
                className={`task-row${task.task_id === selected?.task_id ? ' active' : ''}`}
              >
                <button
                  type="button"
                  className="task-row__select"
                  aria-label={taskTargetLabel(task, t)}
                  onClick={() => setSelectedID(task.task_id)}
                />
                <TaskIcon
                  task={task}
                  onPreview={(value) => {
                    setSelectedID(task.task_id);
                    setPreviewImage(value);
                  }}
                />
                <span className="task-row__summary">
                  <strong>{taskTargetLabel(task, t)}</strong>
                  <small>
                    {taskLabel(task, t)} · {providerLabel(task)}
                  </small>
                </span>
                <span className="task-row__project">
                  <strong>
                    {task.project_name ?? projectNames.get(task.project_id) ?? t('tasks.unknownProject')}
                  </strong>
                  <small>{taskContextLabel(task)}</small>
                </span>
                <span className={`task-state task-state--${task.state}`}>
                  <i />
                  {t(`tasks.states.${task.state}`)}
                </span>
                <time>{formatRelative(task.updated_at, t)}</time>
              </article>
            ))
          ) : (
            <div className="task-empty">
              <h2>{t('tasks.emptyTitle')}</h2>
              <p>{t('tasks.emptyHint')}</p>
            </div>
          )}
        </main>
        {selected ? (
          <aside className="task-detail">
            <header>
              <div>
                <span>{t('tasks.details')}</span>
                <h2>{taskTargetLabel(selected, t)}</h2>
              </div>
              <span className={`task-state task-state--${selected.state}`}>
                <i />
                {t(`tasks.states.${selected.state}`)}
              </span>
            </header>
            <dl>
              <div>
                <dt>{t('tasks.project')}</dt>
                <dd>
                  {selected.project_name ?? projectNames.get(selected.project_id) ?? selected.project_id}
                  {selected.episode_title ? ` / ${selected.episode_title}` : ''}
                </dd>
              </div>
              <div>
                <dt>{t('tasks.target')}</dt>
                <dd>{taskTargetPath(selected, t)}</dd>
              </div>
              <div>
                <dt>{t('tasks.type')}</dt>
                <dd>
                  {selected.task_type === 'export_job'
                    ? t('tasks.export')
                    : t(`tasks.media.${selected.target_media_kind ?? 'video'}`)}
                </dd>
              </div>
              <div>
                <dt>{t('tasks.provider')}</dt>
                <dd>{providerLabel(selected)}</dd>
              </div>
              <div>
                <dt>{t('tasks.taskID')}</dt>
                <dd className="task-monospace">{selected.task_id}</dd>
              </div>
              <div>
                <dt>{t('tasks.submitted')}</dt>
                <dd>{formatDate(selected.created_at)}</dd>
              </div>
              <div>
                <dt>{t('tasks.elapsed')}</dt>
                <dd>{elapsed(selected)}</dd>
              </div>
              {selected.progress_stage && (
                <div>
                  <dt>{t('tasks.stage')}</dt>
                  <dd>{selected.progress_message || selected.progress_stage}</dd>
                </div>
              )}
            </dl>
            <section className="task-progress-card">
              <h3>{t('tasks.progress')}</h3>
              <div className={`task-progress-visual state-${selected.state}`}>
                <span />
              </div>
              <p>{selected.progress_message || selected.progress_stage || t('tasks.noProgress')}</p>
            </section>
            {selected.state === 'failed' && (
              <div className="task-failure">
                <strong>{t('tasks.failedTitle')}</strong>
                <p>{taskFailureMessage(selected, t, i18n.resolvedLanguage)}</p>
                <RechargeAction code={selected.failure_code} />
              </div>
            )}
            <section className="task-timeline">
              <h3>{t('tasks.timeline')}</h3>
              <div className="done">
                <i />
                {t('tasks.submittedAt', { time: formatDate(selected.created_at) })}
              </div>
              {selected.started_at && (
                <div className={selected.finished_at ? 'done' : 'current'}>
                  <i />
                  {t('tasks.startedAt', { time: formatDate(selected.started_at) })}
                </div>
              )}
              {selected.finished_at && (
                <div className={selected.state === 'succeeded' ? 'done' : 'failed'}>
                  <i />
                  {t('tasks.finishedAt', { time: formatDate(selected.finished_at) })}
                </div>
              )}
            </section>
            <footer>
              {taskAction(selected) && (
                <button
                  className="button"
                  disabled={action.isPending}
                  onClick={() => action.mutate({ task: selected, name: taskAction(selected)! })}
                >
                  {t(`tasks.actions.${taskAction(selected)!}`)}
                </button>
              )}
              {selected.state === 'succeeded' && (
                <Link className="button" to={resultLocation(selected)}>
                  {t('tasks.openResult')}
                </Link>
              )}
              {resultURL(selected) &&
                (selected.task_type === 'export_job' || selected.target_media_kind === 'video') && (
                  <button
                    className="button button--secondary"
                    type="button"
                    onClick={() =>
                      setPreviewVideo({
                        src: resultURL(selected),
                        title: taskTargetLabel(selected, t),
                      })
                    }
                  >
                    {t('common.enlargeVideo')}
                  </button>
                )}
              {resultURL(selected) && (
                <button
                  className="button button--secondary"
                  type="button"
                  onClick={() =>
                    void saveDownload(resultURL(selected), taskDownloadName(selected)).catch(() => undefined)
                  }
                >
                  {t('tasks.download')}
                </button>
              )}
            </footer>
            {action.isError && <p className="form-error">{t('tasks.actionFailed')}</p>}
          </aside>
        ) : (
          <aside className="task-detail task-detail--empty">{t('tasks.select')}</aside>
        )}
      </div>
      <ImageLightbox value={previewImage} onClose={() => setPreviewImage(undefined)} />
      <VideoLightbox value={previewVideo} onClose={() => setPreviewVideo(undefined)} />
    </div>
  );
}

function TaskIcon({ task, onPreview }: { task: TaskItem; onPreview: (value: ImagePreviewValue) => void }) {
  if (task.target_media_kind === 'image' && task.result_artifact_id)
    return (
      <ImagePreviewButton
        className="task-icon task-icon--preview"
        src={artifactContentURL(task.project_id, task.result_artifact_id)}
        alt={task.target_name || task.result_artifact_id}
        loading="lazy"
        onPreview={() =>
          onPreview({
            src: artifactContentURL(task.project_id, task.result_artifact_id!),
            title: task.target_name || task.result_artifact_id!,
          })
        }
      />
    );
  const kind =
    task.task_type === 'export_job' ? 'export' : task.target_media_kind === 'image' ? 'image' : 'video';
  return (
    <span className={`task-icon task-icon--${kind}`} aria-hidden="true">
      <TaskTypeGlyph kind={kind} />
    </span>
  );
}

function TaskTypeGlyph({ kind }: { kind: 'export' | 'image' | 'video' }) {
  if (kind === 'export')
    return (
      <svg viewBox="0 0 24 24" focusable="false">
        <path d="M5.5 7.5v10a1.5 1.5 0 0 0 1.5 1.5h10a1.5 1.5 0 0 0 1.5-1.5v-3" />
        <path d="M12.5 5h6.5v6.5M19 5l-8 8" />
      </svg>
    );
  if (kind === 'image')
    return (
      <svg viewBox="0 0 24 24" focusable="false">
        <rect x="4" y="5" width="16" height="14" rx="2" />
        <circle cx="9" cy="10" r="1.5" />
        <path d="m6.5 17 4-4 2.7 2.7 1.8-1.8 2.5 3.1" />
      </svg>
    );
  return (
    <svg viewBox="0 0 24 24" focusable="false">
      <rect x="3.75" y="5" width="16.5" height="14" rx="2.25" />
      <path className="task-icon__play" d="m10 9 5 3-5 3Z" />
      <path className="task-icon__film" d="M7 5v2M17 5v2M7 17v2M17 17v2" />
    </svg>
  );
}
type Translate = ReturnType<typeof useTranslation>['t'];
function taskLabel(task: TaskItem, t: Translate) {
  return task.task_type === 'export_job'
    ? t('tasks.exportTask')
    : task.target_media_kind === 'image'
      ? t('tasks.imageTask')
      : t('tasks.videoTask');
}
export function taskTargetLabel(task: TaskItem, t: Translate) {
  if (task.task_type === 'export_job') return task.episode_title || t('tasks.exportTask');
  const role = task.slot_role ? t(`tasks.slotRoles.${task.slot_role}`) : taskLabel(task, t);
  return task.target_name ? `${task.target_name} · ${role}` : taskLabel(task, t);
}
export function taskContextLabel(task: TaskItem) {
  if (task.scene_name && task.target_name)
    return `${task.episode_title ? `${task.episode_title} · ` : ''}${task.scene_name}${task.clip_position ? ` / ${String(task.clip_position).padStart(2, '0')}` : ''}`;
  return task.episode_title || task.episode_id;
}
function taskTargetPath(task: TaskItem, t: Translate) {
  const pieces = [task.episode_title];
  if (task.scene_name)
    pieces.push(
      task.scene_position
        ? `${String(task.scene_position).padStart(2, '0')} ${task.scene_name}`
        : task.scene_name,
    );
  if (task.target_name) pieces.push(task.target_name);
  if (task.slot_role) pieces.push(t(`tasks.slotRoles.${task.slot_role}`));
  return pieces.filter(Boolean).join(' / ') || taskTargetLabel(task, t);
}
export function providerLabel(task: TaskItem) {
  if (task.task_type === 'export_job') return 'MP4';
  if (task.generator_id === 'image.codex') return 'Codex Image';
  return task.generator_id || '—';
}
function taskAction(task: TaskItem): 'cancel' | 'stop' | 'retry' | undefined {
  if (task.state === 'queued') return 'cancel';
  if (task.state === 'running' && task.task_type === 'generation_job' && task.stop_supported) return 'stop';
  if (task.state === 'failed' && task.task_type === 'generation_job') return 'retry';
  return undefined;
}
function resultURL(task: TaskItem) {
  if (task.state !== 'succeeded') return '';
  if (task.task_type === 'export_job' && task.output_relative_path)
    return `/api/v1/projects/${encodeURIComponent(task.project_id)}/exports/${encodeURIComponent(task.task_id)}/content`;
  if (task.result_artifact_id)
    return `/api/v1/projects/${encodeURIComponent(task.project_id)}/artifacts/${encodeURIComponent(task.result_artifact_id)}/content`;
  return '';
}

function taskDownloadName(task: TaskItem) {
  if (task.task_type === 'export_job' || task.target_media_kind === 'video')
    return `shotforward-${task.task_id}.mp4`;
  return `shotforward-${task.task_id}`;
}

export function resultLocation(task: TaskItem) {
  const base = `/projects/${task.project_id}/episodes/${task.episode_id}`;
  if (task.task_type === 'export_job') return `${base}/preview`;
  if (task.target_kind === 'asset' && task.target_id)
    return `${base}/assets?asset=${encodeURIComponent(task.target_id)}`;
  if (task.target_kind === 'clip' && task.target_id)
    return `${base}/storyboard/clips/${encodeURIComponent(task.target_id)}`;
  return `${base}/storyboard`;
}
function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(
    new Date(value),
  );
}
export function formatRelative(value: string, t: Translate) {
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60_000));
  return minutes < 1
    ? t('tasks.now')
    : minutes < 60
      ? t('tasks.minutesAgo', { count: minutes })
      : formatDate(value);
}
function elapsed(task: TaskItem) {
  const start = task.started_at ? new Date(task.started_at).getTime() : new Date(task.created_at).getTime();
  const end = task.finished_at ? new Date(task.finished_at).getTime() : Date.now();
  const seconds = Math.max(0, Math.floor((end - start) / 1000));
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}
