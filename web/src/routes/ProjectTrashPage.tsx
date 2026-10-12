import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { restoreAsset, type EpisodeAsset } from '../api/assets';
import type { EpisodeSummary } from '../api/projects';
import type { Clip, Scene } from '../api/storyboard';
import { getProjectTrash, restoreClip, restoreEpisode, restoreScene } from '../api/trash';
import { ProjectManagementHeader } from '../components/ProjectManagementHeader';
import { Select } from '../components/Select';
import '../styles/trash-polish.css';

type TrashKind = 'episode' | 'asset' | 'scene' | 'clip';
type TrashItem =
  | {
      kind: 'episode';
      id: string;
      episodeID: string;
      title: string;
      deletedAt: string;
      revision: number;
      raw: EpisodeSummary;
    }
  | {
      kind: 'asset';
      id: string;
      episodeID: string;
      title: string;
      deletedAt: string;
      revision: number;
      raw: EpisodeAsset;
    }
  | {
      kind: 'scene';
      id: string;
      episodeID: string;
      title: string;
      deletedAt: string;
      revision: number;
      childCount: number;
      raw: Scene;
    }
  | {
      kind: 'clip';
      id: string;
      episodeID: string;
      title: string;
      deletedAt: string;
      revision: number;
      raw: Clip;
    };

export function ProjectTrashPage() {
  const { projectId = '' } = useParams();
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const trash = useQuery({
    queryKey: ['project-trash', projectId],
    queryFn: () => getProjectTrash(projectId),
    enabled: Boolean(projectId),
  });
  const [filter, setFilter] = useState<TrashKind | 'all'>('all');
  const [search, setSearch] = useState('');
  const [sort, setSort] = useState<'newest' | 'oldest'>('newest');
  const [selectedID, setSelectedID] = useState('');
  const items = useMemo<TrashItem[]>(() => {
    if (!trash.data) return [];
    const episodes: TrashItem[] = trash.data.episodes
      .filter((episode) => episode.deleted_at)
      .map((episode) => ({
        kind: 'episode',
        id: episode.episode_id,
        episodeID: episode.episode_id,
        title: episode.title,
        deletedAt: episode.deleted_at!,
        revision: episode.revision,
        raw: episode,
      }));
    const assets: TrashItem[] = trash.data.assets.map((asset) => ({
      kind: 'asset',
      id: asset.asset_id,
      episodeID: asset.episode_id,
      title: asset.identity.name,
      deletedAt: asset.deleted_at!,
      revision: asset.revision,
      raw: asset,
    }));
    const scenes: TrashItem[] = trash.data.scenes.map((scene) => ({
      kind: 'scene',
      id: scene.scene_id,
      episodeID: scene.episode_id,
      title: scene.title,
      deletedAt: scene.deleted_at!,
      revision: scene.revision,
      childCount: scene.clips.length,
      raw: scene,
    }));
    const clips: TrashItem[] = trash.data.clips.map((clip) => ({
      kind: 'clip',
      id: clip.clip_id,
      episodeID: clip.episode_id,
      title: clip.title,
      deletedAt: clip.deleted_at!,
      revision: clip.revision,
      raw: clip,
    }));
    return [...episodes, ...assets, ...scenes, ...clips].sort((left, right) =>
      right.deletedAt.localeCompare(left.deletedAt),
    );
  }, [trash.data]);
  const visible = items
    .filter((item) => filter === 'all' || item.kind === filter)
    .filter((item) => item.title.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
    .sort((left, right) =>
      sort === 'newest'
        ? right.deletedAt.localeCompare(left.deletedAt)
        : left.deletedAt.localeCompare(right.deletedAt),
    );
  const selected = visible.find((item) => `${item.kind}:${item.id}` === selectedID) ?? visible[0];
  const episodes = useMemo(
    () => new Map(trash.data?.episodes.map((episode) => [episode.episode_id, episode])),
    [trash.data],
  );
  const restore = useMutation({
    mutationFn: async (item: TrashItem) => {
      if (item.kind === 'episode') return restoreEpisode(projectId, item.id, item.revision);
      if (item.kind === 'asset') return restoreAsset(projectId, item.id, item.revision);
      const storyboardRevision = trash.data!.storyboard_revisions[item.episodeID] ?? 0;
      return item.kind === 'scene'
        ? restoreScene(projectId, item.episodeID, item.id, item.revision, storyboardRevision)
        : restoreClip(projectId, item.episodeID, item.id, item.revision, storyboardRevision);
    },
    onSuccess: async (_, item) => {
      setSelectedID('');
      await queryClient.invalidateQueries({ queryKey: ['project-trash', projectId] });
      if (item.kind === 'episode') await queryClient.invalidateQueries({ queryKey: ['project', projectId] });
      else if (item.kind === 'asset')
        await queryClient.invalidateQueries({ queryKey: ['assets', projectId, item.episodeID] });
      else await queryClient.invalidateQueries({ queryKey: ['storyboard', projectId, item.episodeID] });
    },
  });
  if (trash.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (trash.isError)
    return (
      <div className="page message-state">
        <h2>{t('trash.loadFailed')}</h2>
        <button className="button" onClick={() => void trash.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );
  return (
    <div className="project-page">
      <ProjectManagementHeader
        projectID={projectId}
        projectName={trash.data.project.name}
        activeSection="settings"
      />
      <main className="project-tool-content project-tool-content--wide trash-content">
        <header className="project-tool-heading trash-content-heading">
          <div>
            <Link className="project-tool-back-link" to={`/projects/${projectId}/settings`}>
              ← {t('trash.backToSettings')}
            </Link>
            <h1>{t('trash.title')}</h1>
            <p>{t('trash.noAutoDelete')}</p>
          </div>
          <span>{t('trash.count', { count: items.length })}</span>
        </header>
        <div className="trash-toolbar">
          <label>
            <span aria-hidden="true">⌕</span>
            <input
              type="search"
              aria-label={t('trash.search')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('trash.search')}
            />
          </label>
          <Select
            ariaLabel={t('trash.newest')}
            value={sort}
            options={[
              { value: 'newest', label: t('trash.newest') },
              { value: 'oldest', label: t('trash.oldest') },
            ]}
            compact
            onChange={setSort}
          />
        </div>
        <nav className="trash-filters">
          <button
            className={filter === 'all' ? 'active' : ''}
            aria-pressed={filter === 'all'}
            onClick={() => setFilter('all')}
          >
            {t('assets.all')} · {items.length}
          </button>
          {(['episode', 'asset', 'scene', 'clip'] as TrashKind[]).map((kind) => (
            <button
              key={kind}
              className={filter === kind ? 'active' : ''}
              aria-pressed={filter === kind}
              onClick={() => setFilter(kind)}
            >
              {t(`trash.kinds.${kind}`)} · {items.filter((item) => item.kind === kind).length}
            </button>
          ))}
        </nav>
        <div className="trash-layout">
          <main className="trash-list">
            <div className="trash-columns">
              <span>{t('trash.object')}</span>
              <span>{t('trash.location')}</span>
              <span>{t('trash.deletedAt')}</span>
            </div>
            {visible.length ? (
              visible.map((item) => (
                <button
                  key={`${item.kind}:${item.id}`}
                  className={
                    `${item.kind}:${item.id}` === `${selected?.kind}:${selected?.id}` ? 'active' : ''
                  }
                  aria-pressed={`${item.kind}:${item.id}` === `${selected?.kind}:${selected?.id}`}
                  onClick={() => setSelectedID(`${item.kind}:${item.id}`)}
                >
                  <span className={`trash-object-icon trash-object-icon--${item.kind}`}>
                    {item.kind === 'episode'
                      ? '集'
                      : item.kind === 'asset'
                        ? item.title.slice(0, 1)
                        : item.kind === 'scene'
                          ? '▤'
                          : '▶'}
                  </span>
                  <span>
                    <strong>{item.title}</strong>
                    <small>
                      {item.kind === 'scene'
                        ? t('trash.sceneWithClips', { count: item.childCount })
                        : t(`trash.kinds.${item.kind}`)}
                    </small>
                  </span>
                  <span>
                    {item.kind === 'episode'
                      ? trash.data.project.name
                      : (episodes.get(item.episodeID)?.title ?? item.episodeID)}
                  </span>
                  <time>{formatDate(item.deletedAt)}</time>
                </button>
              ))
            ) : (
              <div className="trash-empty">
                <h2>{t('trash.emptyTitle')}</h2>
                <p>{t('trash.emptyHint')}</p>
              </div>
            )}
          </main>
          {selected ? (
            <RestorePanel
              item={selected}
              episode={episodes.get(selected.episodeID)}
              projectName={trash.data.project.name}
              busy={restore.isPending}
              error={restore.isError}
              onRestore={() => restore.mutate(selected)}
            />
          ) : (
            <aside className="restore-panel restore-panel--empty">{t('trash.select')}</aside>
          )}
        </div>
      </main>
    </div>
  );
}

function RestorePanel({
  item,
  episode,
  projectName,
  busy,
  error,
  onRestore,
}: {
  item: TrashItem;
  episode: EpisodeSummary | undefined;
  projectName: string;
  busy: boolean;
  error: boolean;
  onRestore: () => void;
}) {
  const { t } = useTranslation();
  return (
    <aside className="restore-panel">
      <header>
        <span>{t('trash.restorePreview')}</span>
        <h2>{item.title}</h2>
      </header>
      <section>
        <h3>{t('trash.originalLocation')}</h3>
        <p>
          {item.kind === 'episode'
            ? `${projectName} / ${item.title}`
            : `${projectName} / ${episode?.title ?? item.episodeID} / ${t(`trash.kinds.${item.kind}`)}`}
        </p>
      </section>
      <section>
        <h3>{t('trash.restoreImpact')}</h3>
        <ul>
          {item.kind === 'asset' && (
            <>
              <li>{t('trash.restoreAsset')}</li>
              <li>{t('trash.restoreMedia')}</li>
              <li>{t('trash.restoreReferences')}</li>
            </>
          )}
          {item.kind === 'episode' && (
            <>
              <li>{t('trash.restoreEpisode')}</li>
              <li>{t('trash.restoreEpisodeContent')}</li>
              <li>{t('trash.restoreEpisodeSessions')}</li>
            </>
          )}
          {item.kind === 'scene' && (
            <>
              <li>{t('trash.restoreScene')}</li>
              <li>{t('trash.restoreSceneClips', { count: item.childCount })}</li>
              <li>{t('trash.restoreOrder')}</li>
            </>
          )}
          {item.kind === 'clip' && (
            <>
              <li>{t('trash.restoreClip')}</li>
              <li>{t('trash.restoreClipMedia')}</li>
              <li>{t('trash.restoreOrder')}</li>
            </>
          )}
        </ul>
      </section>
      <section>
        <h3>{t('trash.restoreChecks')}</h3>
        {item.kind !== 'episode' && (
          <p className="check-row">
            <span>✓</span>
            {t('trash.episodeExists')}
          </p>
        )}
        <p className="check-row">
          <span>✓</span>
          {t('trash.revisionChecked')}
        </p>
      </section>
      {error && <p className="form-error">{t('trash.restoreFailed')}</p>}
      <button className="button" disabled={busy} onClick={onRestore}>
        {busy ? t('trash.restoring') : t(`trash.restoreActions.${item.kind}`)}
      </button>
    </aside>
  );
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(
    new Date(value),
  );
}
