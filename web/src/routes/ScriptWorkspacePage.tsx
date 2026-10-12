import { useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { getEpisode, importScript, saveScript } from '../api/episodes';
import { AgentDock, type AgentDraftRequest } from '../components/AgentDock';
import { AgentMarkdown } from '../components/AgentMarkdown';
import { Dialog } from '../components/Dialog';
import { hasDesktopBridge, selectScriptFile } from '../desktop';
import {
  ArrowRightIcon,
  ChevronIcon,
  OutlineIcon,
  PencilIcon,
  SparklesIcon,
  UploadIcon,
} from '../components/Icons';
import { EpisodeWorkspaceLoading, ProjectTabs } from '../components/ProjectTabs';
import { useAgentTemplates } from '../useAgentTemplates';
import '../styles/script-workspace.css';

export function parseScriptOutline(markdown: string) {
  return markdown.split('\n').flatMap((line, index) => {
    const match = /^(##|###)\s+(.+?)\s*$/.exec(line);
    const title = match?.[2]?.trim();
    return match?.[1] && title ? [{ depth: match[1].length, title, line: index + 1 }] : [];
  });
}

interface AgentSetupFailure {
  code: string;
  message: string;
  requestID?: string;
}

function agentSetupFailureText(failure: AgentSetupFailure, language: string | undefined): string {
  const chinese = language?.startsWith('zh');
  const localized: Record<string, [string, string]> = {
    invalid_argument: [
      '所选模型与 Agent 运行环境不匹配。',
      'The selected model does not match its Agent runtime.',
    ],
    runtime_unavailable: ['Agent Runtime 暂时不可用。', 'The Agent runtime is temporarily unavailable.'],
    interrupted: ['Agent 初始化请求被中断。', 'The Agent setup request was interrupted.'],
    external_failure: ['Agent 会话初始化失败。', 'The Agent conversation could not be initialized.'],
  };
  const known = localized[failure.code];
  const reason = known ? known[chinese ? 0 : 1] : failure.message;
  const request = failure.requestID
    ? chinese
      ? ` 请求编号：${failure.requestID}。`
      : ` Request ID: ${failure.requestID}.`
    : '';
  return chinese
    ? `视频已创建，但默认 Agent 对话未能准备：${reason}${request} 点击关闭，之后可在右侧手动新建。`
    : `The video was created, but its default Agent conversation was not prepared: ${reason}${request} Click to dismiss and start one manually later.`;
}

export function ScriptWorkspacePage() {
  const { projectId = '', episodeId = '' } = useParams();
  const location = useLocation();
  const { t, i18n } = useTranslation();
  const agentTemplates = useAgentTemplates();
  const queryClient = useQueryClient();
  const episode = useQuery({
    queryKey: ['episode', projectId, episodeId],
    queryFn: () => getEpisode(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const [markdown, setMarkdown] = useState('');
  const [dirty, setDirty] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [sourcePath, setSourcePath] = useState('');
  const [importPickerFailed, setImportPickerFailed] = useState(false);
  const [agentDraftRequest, setAgentDraftRequest] = useState<AgentDraftRequest>();
  const navigationState = location.state as {
    agentSetupFailure?: AgentSetupFailure;
    agentSetupFailed?: boolean;
  } | null;
  const [agentSetupWarning, setAgentSetupWarning] = useState<AgentSetupFailure | undefined>(() => {
    if (navigationState?.agentSetupFailure) return navigationState.agentSetupFailure;
    if (navigationState?.agentSetupFailed) {
      return { code: 'unknown_error', message: 'The Agent conversation could not be prepared.' };
    }
    return undefined;
  });
  const [outlineCollapsed, setOutlineCollapsed] = useState(false);
  const [viewMode, setViewMode] = useState<'edit' | 'preview'>('preview');
  const [emptyStateDismissed, setEmptyStateDismissed] = useState(false);
  const loadedRevision = useRef(0);
  const editor = useRef<HTMLTextAreaElement>(null);
  const save = useMutation({
    mutationFn: ({ value, revision }: { value: string; revision: number }) =>
      saveScript(projectId, episodeId, value, revision),
    onSuccess: (result) => {
      loadedRevision.current = result.episode.script.revision;
      setDirty(false);
      queryClient.setQueryData(['episode', projectId, episodeId], result);
      queryClient.setQueryData(['project', projectId], {
        project: result.project,
        episodes: result.episodes,
      });
    },
  });
  const importer = useMutation({
    mutationFn: (path: string) => importScript(projectId, episodeId, path, loadedRevision.current),
    onSuccess: (result) => {
      setMarkdown(result.episode.script.markdown);
      loadedRevision.current = result.episode.script.revision;
      setDirty(false);
      setSourcePath('');
      setImportPickerFailed(false);
      setImportOpen(false);
      queryClient.setQueryData(['episode', projectId, episodeId], result);
      queryClient.setQueryData(['project', projectId], {
        project: result.project,
        episodes: result.episodes,
      });
    },
  });
  const extractAssets = useMutation({
    mutationFn: async () => {
      if (dirty) await save.mutateAsync({ value: markdown, revision: loadedRevision.current });
    },
    onSuccess: () => {
      setAgentDraftRequest((current) => ({
        id: (current?.id ?? 0) + 1,
        text: agentTemplates.render('script.extract_assets'),
      }));
    },
  });

  useEffect(() => {
    if (!episode.data || dirty) return;
    setMarkdown(episode.data.episode.script.markdown);
    loadedRevision.current = episode.data.episode.script.revision;
  }, [dirty, episode.data]);

  useEffect(() => {
    setEmptyStateDismissed(false);
    setOutlineCollapsed(false);
    setViewMode('preview');
  }, [episodeId]);

  useEffect(() => {
    if (!dirty || save.isPending || save.isError || loadedRevision.current < 1) return;
    const timeout = window.setTimeout(
      () => save.mutate({ value: markdown, revision: loadedRevision.current }),
      900,
    );
    return () => window.clearTimeout(timeout);
  }, [dirty, markdown, save.isError, save.isPending, save.mutate]);

  const outline = useMemo(() => parseScriptOutline(markdown), [markdown]);
  const focusEditorLine = (line: number) => {
    const offset = markdown
      .split('\n')
      .slice(0, line - 1)
      .reduce((total, value) => total + value.length + 1, 0);
    setViewMode('edit');
    window.requestAnimationFrame(() => {
      editor.current?.focus();
      editor.current?.setSelectionRange(offset, offset);
    });
  };
  const insertHeading = (prefix: '## ' | '### ') => {
    const target = editor.current;
    if (!target) return;
    const start = target.selectionStart;
    const lineStart = markdown.lastIndexOf('\n', Math.max(0, start - 1)) + 1;
    setMarkdown(`${markdown.slice(0, lineStart)}${prefix}${markdown.slice(lineStart)}`);
    setDirty(true);
    save.reset();
    window.requestAnimationFrame(() => {
      target.focus();
      target.setSelectionRange(start + prefix.length, start + prefix.length);
    });
  };

  const startWriting = () => {
    setEmptyStateDismissed(true);
    setViewMode('edit');
    window.requestAnimationFrame(() => editor.current?.focus());
  };
  const openImport = async () => {
    importer.reset();
    setImportPickerFailed(false);
    if (!hasDesktopBridge()) {
      setImportOpen(true);
      return;
    }
    try {
      const selected = await selectScriptFile();
      if (selected) importer.mutate(selected);
    } catch {
      setImportPickerFailed(true);
    }
  };
  const startWithAgent = () => {
    setAgentDraftRequest((current) => ({
      id: (current?.id ?? 0) + 1,
      text: agentTemplates.render('script.create'),
    }));
  };

  const showEmptyState =
    !emptyStateDismissed && !markdown.trim() && !(episode.data?.episode.script.markdown ?? '').trim();

  if (episode.isPending) return <EpisodeWorkspaceLoading projectID={projectId} episodeID={episodeId} />;
  if (episode.isError)
    return (
      <div className="page message-state">
        <h2>{t('script.loadFailed')}</h2>
        <button className="button" onClick={() => void episode.refetch()}>
          {t('common.retry')}
        </button>
      </div>
    );
  const data = episode.data;
  const emptyCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? {
        title: `开始创作“${data.episode.title}”`,
        description: '选择一种方式开始，之后都可以继续编辑。',
        write: '直接编写',
        writeHint: '打开编辑器，从空白内容开始',
        import: '导入已有内容',
        importHint: '支持 Markdown 和纯文本',
        agent: '与 Agent 一起创作',
        agentHint: '根据你的想法起草剧本、文案或创意说明',
        skip: '暂不准备创作内容，也可以直接开始',
        assets: '创建资产',
        storyboard: '创建故事板',
      }
    : {
        title: `Start creating “${data.episode.title}”`,
        description: 'Choose a starting point. You can continue editing whichever path you take.',
        write: 'Write directly',
        writeHint: 'Open the editor and begin with blank content',
        import: 'Import existing content',
        importHint: 'Supports Markdown and plain text',
        agent: 'Create with Agent',
        agentHint: 'Develop a script, copy, or creative brief from your ideas',
        skip: 'Skip content preparation and start directly',
        assets: 'Create assets',
        storyboard: 'Create storyboard',
      };
  return (
    <div className="creative-page">
      <header className="creative-heading">
        <div>
          <span className="creative-heading__project">{data.project.name}</span>
          <strong>{data.episode.title}</strong>
        </div>
      </header>
      <ProjectTabs projectID={projectId} episodeID={episodeId} />
      <div className={`creative-layout${outlineCollapsed ? ' is-outline-collapsed' : ''}`}>
        <aside className={`scene-outline${outlineCollapsed ? ' is-collapsed' : ''}`}>
          <header>
            <strong>{t('script.outline')}</strong>
            <span>{t('script.characterCount', { count: markdown.length })}</span>
            {!outlineCollapsed && !showEmptyState && (
              <button
                type="button"
                className="workspace-rail-toggle"
                aria-label={t('script.collapseOutline')}
                aria-expanded="true"
                title={t('script.collapseOutline')}
                onClick={() => setOutlineCollapsed(true)}
              >
                <ChevronIcon />
              </button>
            )}
          </header>
          {outline.length ? (
            <nav className="outline-list">
              {outline.map((item) => (
                <button
                  key={`${item.line}-${item.title}`}
                  className={item.depth === 3 ? 'is-nested' : ''}
                  onClick={() => focusEditorLine(item.line)}
                >
                  <span className="outline-list__marker">
                    {item.depth === 2 ? String(item.line).padStart(2, '0') : '—'}
                  </span>
                  <span className="outline-list__label">{item.title}</span>
                </button>
              ))}
            </nav>
          ) : (
            <div className="outline-empty">{t('script.outlineHint')}</div>
          )}
        </aside>
        <main className="script-editor">
          {!showEmptyState && (
            <div className="script-toolbar">
              {outlineCollapsed && (
                <div className="workspace-rail-reopen">
                  <button
                    type="button"
                    className="workspace-rail-reopen__button"
                    aria-label={t('script.expandOutline')}
                    aria-expanded="false"
                    onClick={() => setOutlineCollapsed(false)}
                  >
                    <OutlineIcon />
                    <span>{t('script.showOutline')}</span>
                  </button>
                  <span className="workspace-rail-reopen__divider" aria-hidden="true" />
                </div>
              )}
              <div className="script-view-switch" role="group" aria-label={t('script.viewMode')}>
                <button
                  type="button"
                  className={viewMode === 'edit' ? 'active' : ''}
                  aria-pressed={viewMode === 'edit'}
                  onClick={() => setViewMode('edit')}
                >
                  {t('script.edit')}
                </button>
                <button
                  type="button"
                  className={viewMode === 'preview' ? 'active' : ''}
                  aria-pressed={viewMode === 'preview'}
                  onClick={() => setViewMode('preview')}
                >
                  {t('script.preview')}
                </button>
              </div>
              {viewMode === 'edit' && (
                <div className="script-format-actions" aria-label={t('script.formatting')}>
                  <button type="button" onClick={() => insertHeading('## ')}>
                    H2
                  </button>
                  <button type="button" onClick={() => insertHeading('### ')}>
                    H3
                  </button>
                </div>
              )}
              <div className="script-toolbar__actions">
                <button
                  type="button"
                  className={`save-state${save.isError ? ' is-error' : ''}`}
                  disabled={!save.isError}
                  onClick={() => save.mutate({ value: markdown, revision: loadedRevision.current })}
                >
                  {save.isPending
                    ? t('script.saving')
                    : save.isError
                      ? t('script.saveFailed')
                      : dirty
                        ? t('script.unsaved')
                        : t('script.saved')}
                </button>
                <button
                  type="button"
                  className="button script-import-action"
                  disabled={importer.isPending}
                  onClick={() => void openImport()}
                >
                  {importer.isPending ? t('script.importing') : t('script.import')}
                </button>
                <button
                  type="button"
                  className="button script-agent-action"
                  disabled={!markdown.trim() || save.isPending || extractAssets.isPending}
                  onClick={() => extractAssets.mutate()}
                >
                  <span aria-hidden="true">✦</span>
                  {extractAssets.isPending ? t('script.extractingAssets') : t('script.extractAssets')}
                </button>
              </div>
            </div>
          )}
          {showEmptyState ? (
            <section className="script-empty-state" aria-labelledby="script-empty-title">
              <div className="script-empty-state__content">
                <h2 id="script-empty-title">{emptyCopy.title}</h2>
                <p>{emptyCopy.description}</p>
                <div className="script-empty-actions">
                  <button type="button" onClick={startWriting}>
                    <span className="script-empty-action__icon">
                      <PencilIcon />
                    </span>
                    <span className="script-empty-action__copy">
                      <strong>{emptyCopy.write}</strong>
                      <small>{emptyCopy.writeHint}</small>
                    </span>
                    <ArrowRightIcon className="script-empty-action__arrow" />
                  </button>
                  <button type="button" disabled={importer.isPending} onClick={() => void openImport()}>
                    <span className="script-empty-action__icon">
                      <UploadIcon />
                    </span>
                    <span className="script-empty-action__copy">
                      <strong>{importer.isPending ? t('script.importing') : emptyCopy.import}</strong>
                      <small>{emptyCopy.importHint}</small>
                    </span>
                    <ArrowRightIcon className="script-empty-action__arrow" />
                  </button>
                  <button type="button" onClick={startWithAgent}>
                    <span className="script-empty-action__icon script-empty-action__icon--agent">
                      <SparklesIcon />
                    </span>
                    <span className="script-empty-action__copy">
                      <strong>{emptyCopy.agent}</strong>
                      <small>{emptyCopy.agentHint}</small>
                    </span>
                    <ArrowRightIcon className="script-empty-action__arrow" />
                  </button>
                </div>
                <nav className="script-empty-skip" aria-label={emptyCopy.skip}>
                  <span>{emptyCopy.skip}</span>
                  <Link to={`/projects/${projectId}/episodes/${episodeId}/assets`}>{emptyCopy.assets}</Link>
                  <Link to={`/projects/${projectId}/episodes/${episodeId}/storyboard`}>
                    {emptyCopy.storyboard}
                  </Link>
                </nav>
              </div>
            </section>
          ) : viewMode === 'edit' ? (
            <textarea
              ref={editor}
              value={markdown}
              onChange={(event) => {
                setEmptyStateDismissed(true);
                setMarkdown(event.target.value);
                setDirty(true);
                save.reset();
              }}
              aria-label={t('script.editor')}
              placeholder={t('script.placeholder')}
              spellCheck
            />
          ) : (
            <article className="script-preview" aria-label={t('script.preview')}>
              {markdown.trim() ? (
                <AgentMarkdown text={markdown} />
              ) : (
                <button type="button" onClick={() => setViewMode('edit')}>
                  {t('script.previewEmpty')}
                </button>
              )}
            </article>
          )}
        </main>
        <AgentDock
          projectID={projectId}
          episodeID={episodeId}
          context="script"
          contextTitle={`${data.project.name} / ${data.episode.title}`}
          {...(agentDraftRequest ? { draftRequest: agentDraftRequest } : {})}
          onDraftApplied={(id) =>
            setAgentDraftRequest((current) => (current?.id === id ? undefined : current))
          }
        />
      </div>
      <Dialog open={importOpen} label={t('script.importTitle')} onClose={() => setImportOpen(false)}>
        <form
          className="dialog-panel"
          onSubmit={(event) => {
            event.preventDefault();
            importer.mutate(sourcePath.trim());
          }}
        >
          <header className="dialog-header">
            <div>
              <p>{t('script.importEyebrow')}</p>
              <h2>{t('script.importTitle')}</h2>
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label={t('common.cancel')}
              onClick={() => setImportOpen(false)}
            >
              ×
            </button>
          </header>
          <div className="form-stack">
            <p className="info-note">{t('script.importWarning')}</p>
            <label className="field">
              {t('script.sourcePath')}
              <input
                required
                value={sourcePath}
                onChange={(event) => setSourcePath(event.target.value)}
                placeholder={t('script.sourcePathPlaceholder')}
              />
              <small>{t('script.sourcePathHint')}</small>
            </label>
            {importer.isError && <p className="form-error">{t('script.importFailed')}</p>}
          </div>
          <footer className="dialog-footer">
            <button type="button" className="button button--secondary" onClick={() => setImportOpen(false)}>
              {t('common.cancel')}
            </button>
            <button className="button" disabled={importer.isPending || !sourcePath.trim()}>
              {importer.isPending ? t('script.importing') : t('script.importConfirm')}
            </button>
          </footer>
        </form>
      </Dialog>
      {(importPickerFailed || (importer.isError && !importOpen)) && (
        <div className="workflow-toast is-error" role="alert">
          {t('script.importFailed')}
        </div>
      )}
      {extractAssets.isError && (
        <div className="workflow-toast is-error">{t('script.extractAssetsFailed')}</div>
      )}
      {agentSetupWarning && (
        <button
          className="workflow-toast is-error"
          type="button"
          onClick={() => setAgentSetupWarning(undefined)}
        >
          {agentSetupFailureText(agentSetupWarning, i18n.resolvedLanguage)}
        </button>
      )}
    </div>
  );
}
