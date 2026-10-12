import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { useEffect, useRef, useState } from 'react';
import { createEpisode, deleteEpisode, getProject, type EpisodeSummary } from '../api/projects';
import { agentRuntimeOptions } from '../agentRuntime';
import { prepareAgentSession } from '../agentSessionPreparation';
import { ArrowRightIcon, PlusIcon, TrashIcon } from '../components/Icons';
import { Dialog } from '../components/Dialog';
import { ProjectManagementHeader } from '../components/ProjectManagementHeader';
import { Select } from '../components/Select';
import { preferredAgentModelForRuntime, setPreferredAgentModelForRuntime } from '../preferences';
import { forgetRecentEpisode, getEpisodeWorkspacePath } from '../recentEpisodes';
import '../styles/project-overview.css';

function durationLabel(milliseconds: number, locale: string): string {
  const seconds = Math.round(milliseconds / 1000);
  if (seconds === 0) return '—';
  if (seconds >= 60) {
    const minutes = Math.floor(seconds / 60);
    const remainder = seconds % 60;
    return remainder
      ? `${minutes}:${String(remainder).padStart(2, '0')}`
      : new Intl.NumberFormat(locale, {
          style: 'unit',
          unit: 'minute',
          unitDisplay: 'short',
        }).format(minutes);
  }
  return new Intl.NumberFormat(locale, { style: 'unit', unit: 'second', unitDisplay: 'long' }).format(
    seconds,
  );
}

function parseDuration(value: string): number | undefined {
  const match = /^(\d{1,2}):([0-5]\d)$/.exec(value.trim());
  if (!match) return undefined;
  const seconds = Number(match[1]) * 60 + Number(match[2]);
  return seconds >= 15 && seconds <= 30 * 60 ? seconds : undefined;
}

function aspectRatio(width: number, height: number): string {
  const divisor = (left: number, right: number): number => (right ? divisor(right, left % right) : left);
  const gcd = divisor(width, height);
  return `${width / gcd}:${height / gcd}`;
}

function runtimeModelKey(model: { id: string; runtime_kind: 'user_codex' | 'shotforward' }): string {
  return `${model.runtime_kind}:${model.id}`;
}

export function ProjectOverviewPage() {
  const { projectId = '' } = useParams();
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const activeProject = useRef(projectId);
  activeProject.current = projectId;
  useEffect(() => {
    activeProject.current = projectId;
    return () => {
      activeProject.current = '';
    };
  }, [projectId]);
  const [episodeToDelete, setEpisodeToDelete] = useState<EpisodeSummary | null>(null);
  const [episodeCreateOpen, setEpisodeCreateOpen] = useState(false);
  const [episodeTitle, setEpisodeTitle] = useState('');
  const [episodeDuration, setEpisodeDuration] = useState('01:00');
  const [episodeModel, setEpisodeModel] = useState('');
  const query = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => getProject(projectId),
    enabled: Boolean(projectId),
    retry: 1,
  });
  const runtime = useQuery(agentRuntimeOptions(queryClient));
  const availableModels = runtime.data?.models ?? [];
  const agentReady = runtime.data?.runtime.health === 'ready' && availableModels.length > 0;
  const activeRuntimeKind = runtime.data?.active_runtime_kind ?? 'user_codex';
  const savedModel = preferredAgentModelForRuntime(activeRuntimeKind);
  const savedRuntimeModel = availableModels.find(
    (item) => item.runtime_kind === activeRuntimeKind && item.id === savedModel,
  );
  const selectedEpisodeModelKey =
    episodeModel ||
    (savedRuntimeModel ? runtimeModelKey(savedRuntimeModel) : '') ||
    (availableModels[0] ? runtimeModelKey(availableModels[0]) : '') ||
    '';
  const selectedRuntimeModel = availableModels.find(
    (item) => runtimeModelKey(item) === selectedEpisodeModelKey,
  );
  const episodeCreation = useMutation({
    mutationFn: async ({
      title,
      durationSeconds,
      modelID,
      runtimeKind,
    }: {
      title: string;
      durationSeconds: number;
      modelID: string;
      runtimeKind: 'user_codex' | 'shotforward';
    }) => {
      const owner = queryClient.getQueryCache().find({ queryKey: ['project', projectId], exact: true });
      const result = await createEpisode(projectId, {
        title,
        target_duration_ms: durationSeconds * 1000,
      });
      return { result, modelID, runtimeKind, owner };
    },
    onSuccess: ({ result, modelID, runtimeKind, owner }) => {
      if (
        activeProject.current !== projectId ||
        queryClient.getQueryCache().find({ queryKey: ['project', projectId], exact: true }) !== owner
      )
        return;
      queryClient.setQueryData(['project', projectId], {
        project: result.project,
        episodes: result.episodes,
      });
      void queryClient.invalidateQueries({ queryKey: ['projects'] });
      if (modelID) {
        setPreferredAgentModelForRuntime(runtimeKind, modelID);
        void prepareAgentSession(
          queryClient,
          projectId,
          result.episode.episode_id,
          modelID,
          runtimeKind,
        ).catch(() => {});
      }
      setEpisodeCreateOpen(false);
      void navigate(`/projects/${projectId}/episodes/${result.episode.episode_id}/script`);
    },
  });
  const episodeDeletion = useMutation({
    mutationFn: (episode: EpisodeSummary) => deleteEpisode(projectId, episode.episode_id, episode.revision),
    onSuccess: (result) => {
      if (episodeToDelete) forgetRecentEpisode(projectId, episodeToDelete.episode_id);
      queryClient.setQueryData(['project', projectId], result);
      void queryClient.invalidateQueries({ queryKey: ['project-trash', projectId] });
      setEpisodeToDelete(null);
    },
  });
  const closeEpisodeDeletion = () => {
    if (episodeDeletion.isPending) return;
    episodeDeletion.reset();
    setEpisodeToDelete(null);
  };

  if (query.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (query.isError)
    return (
      <div className="page message-state" role="alert">
        <h2>{t('projects.openFailed')}</h2>
        <button className="button" onClick={() => void query.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );

  const { project, episodes } = query.data;
  const nextEpisodeNumber =
    episodes.reduce((maximum, episode) => Math.max(maximum, episode.sequence_number), 0) + 1;
  const createCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? {
        title: '新建视频',
        action: '创建并开始创作',
        pending: '正在创建…',
        failed: '视频创建失败，请检查名称和目标成片时长后重试。',
        episodeTitle: '视频名称',
        titleHint: `可以留空，系统会命名为“视频 ${nextEpisodeNumber}”。`,
        titlePlaceholder: '例如：城市漫游、角色短片、视觉实验',
        durationHint: '使用 mm:ss 格式；可设置 00:15 至 30:00。',
        presets: '常用时长',
        agent: '默认 Agent 对话',
        agentHint: '视频创建后会在后台准备对话，你可以先开始创作；对话创建失败不会影响视频。',
        unavailable: 'Codex Runtime 当前不可用；视频仍会创建，稍后可手动新建对话。',
        emptyTitle: '这个项目还没有视频',
        emptyHint: '项目已经准备好。设置第一个视频的名称和目标成片时长后开始创作。',
        first: '创建第一个视频',
      }
    : {
        title: 'Create video',
        action: 'Create and start',
        pending: 'Creating…',
        failed: 'The video could not be created. Check its name and target length.',
        episodeTitle: 'Video name',
        titleHint: `Leave blank to name it “Video ${nextEpisodeNumber}”.`,
        titlePlaceholder: 'For example: City walk, Character short, Visual experiment',
        durationHint: 'Use mm:ss format; choose from 00:15 through 30:00.',
        presets: 'Common durations',
        agent: 'Default Agent conversation',
        agentHint:
          'The conversation is prepared in the background so you can start creating. If it fails, the video remains available.',
        unavailable:
          'Codex Runtime is unavailable. The video will still be created; start a conversation later.',
        emptyTitle: 'This project has no videos yet',
        emptyHint: 'The project is ready. Set the first video name and target length when you want to begin.',
        first: 'Create first video',
      };
  const durationSeconds = parseDuration(episodeDuration);
  const openEpisodeCreation = () => {
    episodeCreation.reset();
    setEpisodeTitle('');
    setEpisodeDuration('01:00');
    setEpisodeModel('');
    setEpisodeCreateOpen(true);
  };
  const totalDuration = episodes.reduce((sum, episode) => sum + episode.target_duration_ms, 0);
  return (
    <div className="project-page">
      <ProjectManagementHeader projectID={projectId} projectName={project.name} activeSection="episodes" />
      <main className="project-content">
        <section className="project-summary">
          <div className="project-summary__copy">
            <p>{project.description || t('projects.noDescription')}</p>
            <span>
              {t('projects.lastEdited', {
                date: new Intl.DateTimeFormat(i18n.language, {
                  month: 'short',
                  day: 'numeric',
                  hour: '2-digit',
                  minute: '2-digit',
                }).format(new Date(project.updated_at)),
              })}
            </span>
          </div>
          <dl className="project-summary__specs">
            <div>
              <dt>{t('projects.fixedOutput')}</dt>
              <dd>
                {aspectRatio(project.output_width, project.output_height)} · {project.output_width} ×{' '}
                {project.output_height}
              </dd>
            </div>
            <div>
              <dt>{t('projects.episodes')}</dt>
              <dd>{t('projects.episodeCount', { count: episodes.length })}</dd>
            </div>
            <div>
              <dt>{t('projects.totalDuration')}</dt>
              <dd>{durationLabel(totalDuration, i18n.language)}</dd>
            </div>
          </dl>
        </section>
        <section className="episode-section">
          <header>
            <div>
              <h2>{t('projects.episodes')}</h2>
              <p>{t('projects.episodeTotal', { count: episodes.length })}</p>
            </div>
            <div className="episode-section__actions">
              <button
                className="button episode-new-button"
                onClick={openEpisodeCreation}
                disabled={episodeCreation.isPending}
              >
                <PlusIcon />
                {t('projects.newEpisode')}
              </button>
            </div>
          </header>
          <div className="episode-table">
            <div className="episode-table__head" aria-hidden="true">
              <span>{t('projects.episodeColumn')}</span>
              <span>{t('projects.stage')}</span>
              <span>{t('projects.targetDuration')}</span>
              <span>{t('projects.updated')}</span>
            </div>
            {episodes.length === 0 ? (
              <div className="episode-empty-state">
                <span aria-hidden="true">
                  <PlusIcon />
                </span>
                <strong>{createCopy.emptyTitle}</strong>
                <p>{createCopy.emptyHint}</p>
                <button className="button button--primary" type="button" onClick={openEpisodeCreation}>
                  {createCopy.first}
                </button>
              </div>
            ) : (
              episodes.map((episode) => (
                <article className="episode-row-shell" key={episode.episode_id}>
                  <Link className="episode-row" to={getEpisodeWorkspacePath(projectId, episode.episode_id)}>
                    <div className="episode-row__identity">
                      <span className="episode-row__number">
                        {String(episode.sequence_number).padStart(2, '0')}
                      </span>
                      <div>
                        <strong>{episode.title}</strong>
                        <span>{t('projects.episode', { count: episode.sequence_number })}</span>
                      </div>
                    </div>
                    <div className="episode-row__stage">
                      <i className={episode.script_revision > 1 ? 'is-ready' : ''} aria-hidden="true" />
                      <strong className={episode.script_revision > 1 ? 'status-ready' : ''}>
                        {t(episode.script_revision > 1 ? 'projects.scriptReady' : 'projects.scriptEmpty')}
                      </strong>
                    </div>
                    <div className="episode-row__metric">
                      <strong>{durationLabel(episode.target_duration_ms, i18n.language)}</strong>
                    </div>
                    <div className="episode-row__metric">
                      <strong>
                        {new Intl.DateTimeFormat(i18n.language, { month: 'short', day: 'numeric' }).format(
                          new Date(episode.updated_at),
                        )}
                      </strong>
                    </div>
                  </Link>
                  <div className="episode-row__actions">
                    <button
                      className="card-icon-action card-icon-action--delete episode-delete-button"
                      type="button"
                      aria-label={t('projects.deleteEpisodeNamed', { name: episode.title })}
                      title={t('projects.moveEpisodeToTrash')}
                      onClick={() => {
                        episodeDeletion.reset();
                        setEpisodeToDelete(episode);
                      }}
                    >
                      <TrashIcon />
                    </button>
                    <span className="card-icon-action episode-row__chevron" aria-hidden="true">
                      <ArrowRightIcon />
                    </span>
                  </div>
                </article>
              ))
            )}
          </div>
        </section>
      </main>
      <Dialog
        open={episodeCreateOpen}
        label={createCopy.title}
        onClose={() => !episodeCreation.isPending && setEpisodeCreateOpen(false)}
      >
        <form
          className="dialog-panel episode-create-dialog"
          onSubmit={(event) => {
            event.preventDefault();
            if (durationSeconds)
              episodeCreation.mutate({
                title: episodeTitle.trim() || t('projects.defaultVideoTitle', { count: nextEpisodeNumber }),
                durationSeconds,
                modelID: selectedRuntimeModel?.id ?? '',
                runtimeKind: selectedRuntimeModel?.runtime_kind ?? 'user_codex',
              });
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{project.name}</p>
              <h2>{createCopy.title}</h2>
            </div>
            <button
              className="icon-button"
              type="button"
              disabled={episodeCreation.isPending}
              onClick={() => setEpisodeCreateOpen(false)}
              aria-label={t('common.close')}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <label className="field">
              {createCopy.episodeTitle}
              <input
                value={episodeTitle}
                onChange={(event) => setEpisodeTitle(event.target.value)}
                placeholder={createCopy.titlePlaceholder}
              />
              <small>{createCopy.titleHint}</small>
            </label>
            <label className="field">
              {t('projects.targetDuration')}
              <input
                required
                inputMode="numeric"
                value={episodeDuration}
                onChange={(event) => setEpisodeDuration(event.target.value)}
                placeholder="01:00"
                aria-invalid={!durationSeconds}
              />
              <small>{createCopy.durationHint}</small>
            </label>
            <div className="duration-presets" aria-label={createCopy.presets}>
              {['00:15', '00:30', '01:00', '05:00', '15:00', '30:00'].map((value) => (
                <button
                  type="button"
                  className={episodeDuration === value ? 'active' : ''}
                  key={value}
                  onClick={() => setEpisodeDuration(value)}
                >
                  {value}
                </button>
              ))}
            </div>
            <div className="episode-agent-setup">
              <div>
                <strong>{createCopy.agent}</strong>
                <small>{agentReady ? createCopy.agentHint : createCopy.unavailable}</small>
              </div>
              {agentReady && (
                <Select
                  ariaLabel={t('agent.model')}
                  value={selectedEpisodeModelKey}
                  options={availableModels.map((item) => ({
                    value: runtimeModelKey(item),
                    label: item.display_name,
                  }))}
                  onChange={setEpisodeModel}
                />
              )}
            </div>
            {episodeCreation.isError && <p className="form-error">{createCopy.failed}</p>}
          </div>
          <footer className="dialog-footer">
            <button
              className="button button--secondary"
              type="button"
              disabled={episodeCreation.isPending}
              onClick={() => setEpisodeCreateOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              className="button button--primary"
              disabled={!durationSeconds || episodeCreation.isPending || runtime.isPending}
            >
              {episodeCreation.isPending ? createCopy.pending : createCopy.action}
            </button>
          </footer>
        </form>
      </Dialog>
      <Dialog
        open={Boolean(episodeToDelete)}
        label={t('projects.deleteEpisodeTitle')}
        onClose={closeEpisodeDeletion}
      >
        <div className="dialog-panel destructive-dialog">
          <header className="dialog-header">
            <div>
              <p>{project.name}</p>
              <h2>{t('projects.deleteEpisodeTitle')}</h2>
            </div>
            <button
              className="icon-button"
              type="button"
              onClick={closeEpisodeDeletion}
              aria-label={t('common.close')}
            >
              ×
            </button>
          </header>
          <div className="destructive-dialog__body">
            <p>{t('projects.deleteEpisodeHint', { name: episodeToDelete?.title })}</p>
            <div className="recovery-note">{t('projects.deleteEpisodeRecovery')}</div>
            {episodeDeletion.isError && <p className="form-error">{t('projects.deleteEpisodeFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button
              className="button button--secondary"
              type="button"
              disabled={episodeDeletion.isPending}
              onClick={closeEpisodeDeletion}
            >
              {t('common.cancel')}
            </button>
            <button
              className="button button--danger"
              type="button"
              disabled={!episodeToDelete || episodeDeletion.isPending}
              onClick={() => episodeToDelete && episodeDeletion.mutate(episodeToDelete)}
            >
              {episodeDeletion.isPending ? t('projects.deletingEpisode') : t('projects.moveToTrash')}
            </button>
          </footer>
        </div>
      </Dialog>
    </div>
  );
}
