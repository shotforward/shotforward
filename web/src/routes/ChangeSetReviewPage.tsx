import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  getChangeSet,
  getChangeSetOperations,
  resolveChangeSet,
  undoChangeSet,
  type ChangeSetOperation,
} from '../api/agent';
import '../styles/review-polish.css';

function OperationDiff({ operation }: { operation: ChangeSetOperation }) {
  const { t } = useTranslation();
  const before =
    operation.clip_merge?.source_clip_ids.join(' + ') ??
    (operation.clip_split
      ? t('changes.splitSource', {
          title: operation.clip_split.source_title,
          duration: operation.clip_split.source_planned_duration_seconds,
        })
      : undefined) ??
    operation.episode_script?.before ??
    operation.asset_prompt?.before ??
    operation.clip_prompts?.video_before ??
    '';
  const after =
    (operation.clip_merge
      ? t('changes.mergeResult', {
          title: operation.clip_merge.title,
          duration: operation.clip_merge.planned_duration_seconds,
          first: operation.clip_merge.inherited_first_frame ? t('changes.inherited') : t('changes.empty'),
          last: operation.clip_merge.inherited_last_frame ? t('changes.inherited') : t('changes.empty'),
        })
      : operation.clip_split
        ? `${operation.clip_split.replacements
            .map((item, index) =>
              t('changes.splitItem', {
                count: index + 1,
                title: item.title,
                duration: item.planned_duration_seconds,
              }),
            )
            .join('\n')}\n${t('changes.splitMedia', {
            first: operation.clip_split.inherited_first_frame ? t('changes.inherited') : t('changes.empty'),
            last: operation.clip_split.inherited_last_frame ? t('changes.inherited') : t('changes.empty'),
          })}`
        : undefined) ??
    operation.episode_script?.after ??
    operation.asset_prompt?.after ??
    operation.clip_prompts?.video_after ??
    operation.asset_creation?.prompt ??
    (operation.asset_restoration ? t('changes.restoreAssetResult') : undefined) ??
    operation.scene_creation?.description ??
    operation.clip_creation?.description ??
    operation.change_set_revert?.original_change_set_id ??
    '';
  const title =
    operation.clip_merge?.title ??
    operation.clip_split?.source_title ??
    operation.asset_creation?.name ??
    (operation.asset_restoration ? t('changes.restoreAsset') : undefined) ??
    operation.scene_creation?.title ??
    operation.clip_creation?.title ??
    (operation.episode_script ? t('changes.script') : undefined) ??
    `${operation.target_kind} · ${operation.type}`;
  return (
    <article className="diff-card">
      <header>
        <span>{operation.position}</span>
        <div>
          <small>{t('changes.selectedOperation')}</small>
          <strong>{title}</strong>
        </div>
        <small>
          {operation.target_kind} · {operation.type}
          {operation.clip_prompts?.video_mode && (
            <> · {t(`storyboard.modes.${operation.clip_prompts.video_mode}`)}</>
          )}
        </small>
      </header>
      {before && (
        <section className="diff-section is-before">
          <strong>{t('changes.before')}</strong>
          <div className="diff-line is-removed">
            <b>−</b>
            <p>{before}</p>
          </div>
        </section>
      )}
      <section className="diff-section is-after">
        <strong>{t('changes.after')}</strong>
        <div className="diff-line is-added">
          <b>+</b>
          <p>{after || t('changes.emptyValue')}</p>
        </div>
      </section>
      <footer>
        <span>{t('changes.target')}</span>
        <code>{operation.target_id || operation.target_kind}</code>
      </footer>
    </article>
  );
}

function operationTitle(operation: ChangeSetOperation, restoreAssetLabel: string) {
  return (
    operation.clip_merge?.title ??
    operation.clip_split?.source_title ??
    operation.asset_creation?.name ??
    (operation.asset_restoration ? restoreAssetLabel : undefined) ??
    operation.scene_creation?.title ??
    operation.clip_creation?.title ??
    `${operation.target_kind} · ${operation.type}`
  );
}

export function ChangeSetReviewPage() {
  const { changeSetId = '' } = useParams();
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [selectedID, setSelectedID] = useState('');
  const changeSet = useQuery({
    queryKey: ['change-set', changeSetId],
    queryFn: () => getChangeSet(changeSetId),
  });
  const operations = useQuery({
    queryKey: ['change-set-operations', changeSetId],
    queryFn: () => getChangeSetOperations(changeSetId),
  });
  const resolve = useMutation({
    mutationFn: (action: 'apply' | 'reject') =>
      resolveChangeSet(changeSetId, action, changeSet.data!.change_set.revision),
    onSuccess: (result) => {
      queryClient.setQueryData(['change-set', changeSetId], result);
      void queryClient.invalidateQueries({
        queryKey: ['episode', result.change_set.project_id, result.change_set.episode_id],
      });
    },
  });
  const undo = useMutation({
    mutationFn: () => undoChangeSet(changeSetId),
    onSuccess: async (result) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['change-set', changeSetId] }),
        queryClient.invalidateQueries({
          queryKey: ['episode', result.change_set.project_id, result.change_set.episode_id],
        }),
        queryClient.invalidateQueries({
          queryKey: ['storyboard', result.change_set.project_id, result.change_set.episode_id],
        }),
        queryClient.invalidateQueries({
          queryKey: ['assets', result.change_set.project_id, result.change_set.episode_id],
        }),
      ]);
      queryClient.setQueryData(['change-set', result.change_set.id], result);
    },
  });
  if (changeSet.isPending || operations.isPending)
    return (
      <div className="page">
        <div className="overview-skeleton skeleton" />
      </div>
    );
  if (changeSet.isError || operations.isError)
    return (
      <div className="page message-state">
        <h2>{t('changes.loadFailed')}</h2>
      </div>
    );
  const item = changeSet.data.change_set;
  const selected =
    operations.data.operations.find((operation) => operation.id === selectedID) ??
    operations.data.operations[0];
  const pending = item.status === 'pending';
  const undoable =
    item.status === 'applied' && !item.reverts_change_set_id && !item.reverted_by_change_set_id;
  return (
    <div className="review-page">
      <header className="review-header">
        <div>
          <Link to={`/projects/${item.project_id}/episodes/${item.episode_id}/script`}>
            ← {t('changes.back')}
          </Link>
          <p className="eyebrow">ChangeSet</p>
          <h1>{item.title}</h1>
          <span>{item.summary}</span>
        </div>
        <div className={`review-status status-${item.status}`}>
          {t(`changes.status.${item.status}`, { defaultValue: item.status })}
        </div>
      </header>
      <div className="review-body">
        <aside className="review-operation-list">
          <header>
            <strong>{t('changes.operations')}</strong>
            <span>{t('changes.operationCount', { count: item.operation_count })}</span>
          </header>
          <nav>
            {operations.data.operations.map((operation) => (
              <button
                key={operation.id}
                className={operation.id === selected?.id ? 'active' : ''}
                aria-pressed={operation.id === selected?.id}
                onClick={() => setSelectedID(operation.id)}
              >
                <i>{operation.position}</i>
                <span>
                  <strong>{operationTitle(operation, t('changes.restoreAsset'))}</strong>
                  <small>{operation.target_kind}</small>
                </span>
              </button>
            ))}
          </nav>
          <p>{t('changes.atomicHint')}</p>
        </aside>
        <main>
          <header className="review-focus-heading">
            <div>
              <span>{t('changes.proposalSource')}</span>
              <strong>{selected ? operationTitle(selected, t('changes.restoreAsset')) : item.title}</strong>
            </div>
            <span>
              {t('changes.itemOf', { current: selected?.position ?? 0, total: item.operation_count })}
            </span>
          </header>
          {selected && <OperationDiff operation={selected} key={selected.id} />}
        </main>
        <aside className="review-summary">
          <h2>{t('changes.reviewSummary')}</h2>
          <dl>
            <div>
              <dt>{t('changes.source')}</dt>
              <dd>Agent</dd>
            </div>
            <div>
              <dt>{t('changes.scope')}</dt>
              <dd>{t('projects.episodeColumn')}</dd>
            </div>
            <div>
              <dt>{t('changes.operationCountLabel')}</dt>
              <dd>{item.operation_count}</dd>
            </div>
            <div>
              <dt>{t('changes.target')}</dt>
              <dd>{selected?.target_kind ?? '—'}</dd>
            </div>
          </dl>
          <section className="review-check">
            <h3>{t('changes.technicalCheck')}</h3>
            <p>
              <i>✓</i>
              {t('changes.technicalCheckPassed')}
            </p>
          </section>
          {(resolve.isError || undo.isError) && <p className="form-error">{t('changes.resolveFailed')}</p>}
        </aside>
      </div>
      {pending && (
        <footer className="review-actions">
          <p>{t('changes.applyHint')}</p>
          <div>
            <button className="button" onClick={() => resolve.mutate('reject')} disabled={resolve.isPending}>
              {t('changes.reject')}
            </button>
            <button
              className="button button--primary"
              onClick={() => resolve.mutate('apply')}
              disabled={resolve.isPending}
            >
              {resolve.isPending ? t('changes.processing') : t('changes.apply')}
            </button>
          </div>
        </footer>
      )}
      {undoable && (
        <footer className="review-actions">
          <p>{t('changes.undoHint')}</p>
          <button className="button" onClick={() => undo.mutate()} disabled={undo.isPending}>
            {undo.isPending ? t('changes.processing') : t('changes.undo')}
          </button>
        </footer>
      )}
    </div>
  );
}
