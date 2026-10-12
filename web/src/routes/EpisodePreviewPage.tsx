import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { getMediaSlot, updateVideoTrim } from '../api/assets';
import { getEpisode } from '../api/episodes';
import { getExportPreview, submitExport, type PreviewClip } from '../api/export';
import { getTaskSnapshot, useTaskEvents } from '../api/tasks';
import { EpisodeWorkspaceLoading, ProjectTabs } from '../components/ProjectTabs';
import { saveDownload } from '../desktop';
import '../styles/preview-workspace.css';

export function EpisodePreviewPage() {
  const { projectId = '', episodeId = '' } = useParams();
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const videos = useRef(new Map<string, HTMLVideoElement>());
  const videoCanvas = useRef<HTMLDivElement>(null);
  const playIntent = useRef(false);
  const switchingClip = useRef(false);
  const preparedClipVersion = useRef('');
  const downloadedExportJob = useRef('');
  const episode = useQuery({
    queryKey: ['episode', projectId, episodeId],
    queryFn: () => getEpisode(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const preview = useQuery({
    queryKey: ['preview', projectId, episodeId],
    queryFn: () => getExportPreview(projectId, episodeId),
    enabled: Boolean(projectId && episodeId),
  });
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: getTaskSnapshot,
    refetchInterval: 30_000,
  });
  useTaskEvents(tasks.data?.cursor);
  const [selected, setSelected] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [playheadMilliseconds, setPlayheadMilliseconds] = useState(0);
  const [muted, setMuted] = useState(false);
  const [panel, setPanel] = useState<'clip' | 'export'>('clip');
  const [autoDownloadJobID, setAutoDownloadJobID] = useState('');
  const [downloadPending, setDownloadPending] = useState(false);
  const [downloadError, setDownloadError] = useState<DownloadErrorKind | ''>('');
  const [trimPreview, setTrimPreview] = useState<{
    mediaVersionID: string;
    trimIn: number;
    trimOut: number;
    unsaved: boolean;
  }>();
  const updateTrimPreview = useCallback(
    (mediaVersionID: string, trimIn: number, trimOut: number, unsaved: boolean) =>
      setTrimPreview((current) =>
        current?.mediaVersionID === mediaVersionID &&
        current.trimIn === trimIn &&
        current.trimOut === trimOut &&
        current.unsaved === unsaved
          ? current
          : { mediaVersionID, trimIn, trimOut, unsaved },
      ),
    [],
  );
  const refreshPreviewAfterTrim = useCallback(async () => {
    await preview.refetch();
  }, [preview.refetch]);
  const triggerDownload = useCallback(
    async (exportJobID: string) => {
      setDownloadError('');
      setDownloadPending(true);
      try {
        return await saveDownload(
          exportContentURL(projectId, exportJobID),
          `${episode.data?.episode.title || 'shotforward-export'}.mp4`,
        );
      } catch (error) {
        setDownloadError(classifyDownloadError(error));
        return false;
      } finally {
        setDownloadPending(false);
      }
    },
    [episode.data?.episode.title, projectId],
  );
  const exporter = useMutation({
    mutationFn: () => submitExport(projectId, episodeId, preview.data!.preview.storyboard_revision),
    onSuccess: async (result) => {
      setPanel('export');
      if (result.job.state === 'succeeded' && result.job.output_relative_path) {
        downloadedExportJob.current = result.job.export_job_id;
        setAutoDownloadJobID('');
        void triggerDownload(result.job.export_job_id);
      } else {
        setAutoDownloadJobID(result.job.export_job_id);
      }
      await queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
  });
  const data = preview.data?.preview;
  const persistedClip = data?.clips[selected];
  const updateSelectedTrimPreview = useCallback(
    (trimIn: number, trimOut: number, unsaved: boolean) => {
      if (persistedClip) updateTrimPreview(persistedClip.media_version_id, trimIn, trimOut, unsaved);
    },
    [persistedClip?.media_version_id, updateTrimPreview],
  );
  const clip = useMemo(() => {
    if (!persistedClip || trimPreview?.mediaVersionID !== persistedClip.media_version_id)
      return persistedClip;
    return {
      ...persistedClip,
      trim_in_ms: trimPreview.trimIn,
      trim_out_ms: trimPreview.trimOut,
      duration_ms: trimPreview.trimOut - trimPreview.trimIn,
    };
  }, [persistedClip, trimPreview]);
  const nextClip = data?.clips[selected + 1];
  const latestExport = tasks.data?.tasks
    .filter(
      (task) =>
        task.task_type === 'export_job' && task.project_id === projectId && task.episode_id === episodeId,
    )
    .sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0];
  const autoDownloadExport = tasks.data?.tasks.find(
    (task) => task.task_type === 'export_job' && task.task_id === autoDownloadJobID,
  );
  const exportActive = Boolean(
    latestExport && ['queued', 'running', 'cancel_requested'].includes(latestExport.state),
  );
  const activePanel = panel === 'clip' && !clip ? 'export' : panel;
  const effectiveExportDuration =
    (data?.export_duration_ms ?? 0) +
    (clip && persistedClip ? clip.duration_ms - persistedClip.duration_ms : 0);
  const timelineItems = data
    ? [
        ...data.clips.map((item, clipIndex) => ({
          ...(clipIndex === selected && clip ? clip : item),
          clipIndex,
          available: true as const,
        })),
        ...data.skipped_clips.map((item) => ({ ...item, available: false as const })),
      ].sort((left, right) => left.position - right.position)
    : [];
  useEffect(() => {
    if (selected >= (data?.clips.length ?? 0)) setSelected(0);
  }, [data?.clips.length, selected]);
  useEffect(() => {
    setTrimPreview(undefined);
  }, [persistedClip?.media_version_id]);
  useEffect(() => {
    setPlayheadMilliseconds(0);
  }, [clip?.media_version_id, clip?.trim_in_ms, clip?.trim_out_ms]);
  useEffect(() => {
    if (
      !autoDownloadExport ||
      autoDownloadExport.state !== 'succeeded' ||
      !autoDownloadExport.output_relative_path ||
      downloadedExportJob.current === autoDownloadExport.task_id
    )
      return;
    downloadedExportJob.current = autoDownloadExport.task_id;
    void triggerDownload(autoDownloadExport.task_id);
    setAutoDownloadJobID('');
  }, [autoDownloadExport, triggerDownload]);
  useEffect(() => {
    const target = clip ? videos.current.get(clip.media_version_id) : undefined;
    if (!target || !clip) return;
    const prepare = () => {
      const currentMilliseconds = target.currentTime * 1000;
      const changedClip = preparedClipVersion.current !== clip.media_version_id;
      preparedClipVersion.current = clip.media_version_id;
      if (changedClip || currentMilliseconds < clip.trim_in_ms - 50 || currentMilliseconds > clip.trim_out_ms)
        target.currentTime = clip.trim_in_ms / 1000;
      setPlayheadMilliseconds(
        Math.min(clip.duration_ms, Math.max(0, target.currentTime * 1000 - clip.trim_in_ms)),
      );
      if (playIntent.current) {
        void target.play().catch(() => {
          playIntent.current = false;
          setPlaying(false);
        });
      }
      switchingClip.current = false;
    };
    if (target.readyState >= HTMLMediaElement.HAVE_METADATA) prepare();
    else {
      target.addEventListener('loadedmetadata', prepare, { once: true });
      return () => target.removeEventListener('loadedmetadata', prepare);
    }
  }, [clip]);

  const advance = useCallback(() => {
    if (!data) return;
    if (switchingClip.current) return;
    if (selected < data.clips.length - 1) {
      switchingClip.current = true;
      setSelected((current) => current + 1);
    } else {
      playIntent.current = false;
      setPlaying(false);
    }
  }, [data, selected]);
  const selectClip = (index: number) => {
    if (!data || index === selected) return;
    switchingClip.current = true;
    setSelected(Math.min(data.clips.length - 1, Math.max(0, index)));
  };
  useEffect(() => {
    if (!playing || !clip) return;
    let animationFrame = 0;
    const monitorTrimOut = () => {
      const target = videos.current.get(clip.media_version_id);
      if (target && target.currentTime * 1000 >= clip.trim_out_ms - 16) {
        advance();
        return;
      }
      animationFrame = requestAnimationFrame(monitorTrimOut);
    };
    animationFrame = requestAnimationFrame(monitorTrimOut);
    return () => cancelAnimationFrame(animationFrame);
  }, [advance, clip, playing]);
  if (episode.isPending || preview.isPending)
    return (
      <EpisodeWorkspaceLoading
        projectID={projectId}
        episodeID={episodeId}
        projectName={episode.data?.project.name}
        episodeTitle={episode.data?.episode.title}
      />
    );
  if (episode.isError || preview.isError)
    return (
      <div className="page message-state">
        <h2>{t('preview.loadFailed')}</h2>
        <p>{t('preview.loadFailedHint')}</p>
        <button
          className="button"
          onClick={() => {
            void episode.refetch();
            void preview.refetch();
          }}
        >
          {t('common.retry')}
        </button>
      </div>
    );
  if (!data) return null;
  return (
    <div className="creative-page preview-page">
      <header className="creative-heading">
        <div>
          <span className="creative-heading__project">{episode.data.project.name}</span>
          <strong>{episode.data.episode.title}</strong>
        </div>
      </header>
      <ProjectTabs projectID={projectId} episodeID={episodeId} />
      <section className="preview-statusbar">
        <div>
          <strong>{t('preview.continuousPreview')}</strong>
          <span>{t('preview.timelineHint')}</span>
        </div>
        <div className="preview-statusbar__actions">
          <span className="preview-statusbar__metric preview-statusbar__ready">
            {t('preview.readyCount', { count: data.included_clip_count })}
          </span>
          <span className={`preview-statusbar__metric${data.skipped_clip_count ? ' has-warning' : ''}`}>
            {t('preview.attentionCount', { count: data.skipped_clip_count })}
          </span>
          <span className="preview-statusbar__metric preview-statusbar__duration">
            {t('preview.duration', { value: formatDuration(effectiveExportDuration) })}
          </span>
          <button
            type="button"
            className="button preview-export-action"
            disabled={
              !data.clips.length ||
              exporter.isPending ||
              exportActive ||
              downloadPending ||
              trimPreview?.unsaved
            }
            title={
              !data.clips.length
                ? t('preview.exportUnavailable')
                : trimPreview?.unsaved
                  ? t('preview.exportWaitingForTrim')
                  : undefined
            }
            onClick={() => exporter.mutate()}
          >
            <DownloadIcon />
            {trimPreview?.unsaved
              ? t('preview.savingTrim')
              : exporter.isPending || exportActive
                ? t('preview.exporting')
                : downloadPending
                  ? t('preview.savingDownload')
                  : t('preview.downloadExport')}
          </button>
        </div>
      </section>
      {downloadError && (
        <div className="message-panel is-error" role="alert">
          <strong>{t('preview.downloadFailed')}</strong>
          <p>{t(`preview.${downloadError}`)}</p>
        </div>
      )}
      <div className="preview-layout">
        <main className="preview-stage">
          <div className="preview-player">
            <div className="preview-player__viewport">
              <div
                ref={videoCanvas}
                className="video-canvas"
                style={{ aspectRatio: `${data.output_width} / ${data.output_height}` }}
              >
                {clip ? (
                  [clip, nextClip]
                    .filter((item): item is PreviewClip => Boolean(item))
                    .map((item) => {
                      const active = item.media_version_id === clip.media_version_id;
                      return (
                        <video
                          key={item.media_version_id}
                          ref={(node) => {
                            if (node) videos.current.set(item.media_version_id, node);
                            else videos.current.delete(item.media_version_id);
                          }}
                          className={active ? 'is-active' : 'is-buffering'}
                          src={item.media_url}
                          muted={!active || muted}
                          preload="auto"
                          aria-hidden={!active}
                          onLoadedMetadata={(event) => {
                            if (!active) event.currentTarget.currentTime = item.trim_in_ms / 1000;
                          }}
                          onPlay={
                            active
                              ? () => {
                                  playIntent.current = true;
                                  setPlaying(true);
                                }
                              : undefined
                          }
                          onPause={
                            active
                              ? () => {
                                  if (switchingClip.current) return;
                                  playIntent.current = false;
                                  setPlaying(false);
                                }
                              : undefined
                          }
                          onTimeUpdate={
                            active
                              ? (event) => {
                                  setPlayheadMilliseconds(
                                    Math.min(
                                      item.duration_ms,
                                      Math.max(0, event.currentTarget.currentTime * 1000 - item.trim_in_ms),
                                    ),
                                  );
                                  if (event.currentTarget.currentTime * 1000 >= item.trim_out_ms - 30)
                                    advance();
                                }
                              : undefined
                          }
                          onSeeking={
                            active
                              ? (event) => {
                                  const milliseconds = event.currentTarget.currentTime * 1000;
                                  if (milliseconds < item.trim_in_ms)
                                    event.currentTarget.currentTime = item.trim_in_ms / 1000;
                                  else if (milliseconds > item.trim_out_ms)
                                    event.currentTarget.currentTime =
                                      Math.max(item.trim_in_ms, item.trim_out_ms - trimStepMilliseconds) /
                                      1000;
                                }
                              : undefined
                          }
                          onEnded={active ? advance : undefined}
                        />
                      );
                    })
                ) : (
                  <div className="preview-empty">
                    <span>▶</span>
                    <h2>{t('preview.emptyTitle')}</h2>
                  </div>
                )}
                {clip && (
                  <div className="clip-player-controls">
                    <button
                      type="button"
                      aria-label={playing ? t('preview.pause') : t('preview.play')}
                      onClick={() => {
                        const target = videos.current.get(clip.media_version_id);
                        if (!target) return;
                        if (target.paused) {
                          playIntent.current = true;
                          void target.play();
                        } else {
                          playIntent.current = false;
                          target.pause();
                        }
                      }}
                    >
                      {playing ? 'Ⅱ' : '▶'}
                    </button>
                    <span className="clip-player-controls__time">
                      {formatDuration(playheadMilliseconds)} / {formatDuration(clip.duration_ms)}
                    </span>
                    <input
                      type="range"
                      min={0}
                      max={Math.max(1, clip.duration_ms)}
                      step={trimStepMilliseconds}
                      value={Math.min(playheadMilliseconds, clip.duration_ms)}
                      aria-label={t('preview.seek')}
                      onChange={(event) => {
                        const relativeMilliseconds = Math.min(
                          clip.duration_ms,
                          Math.max(0, Number(event.target.value)),
                        );
                        const target = videos.current.get(clip.media_version_id);
                        if (target)
                          target.currentTime =
                            Math.min(
                              clip.trim_out_ms - trimStepMilliseconds,
                              clip.trim_in_ms + relativeMilliseconds,
                            ) / 1000;
                        setPlayheadMilliseconds(relativeMilliseconds);
                      }}
                    />
                    <button
                      type="button"
                      aria-label={muted ? t('preview.unmute') : t('preview.mute')}
                      onClick={() => setMuted((current) => !current)}
                    >
                      <VolumeIcon muted={muted} />
                    </button>
                    <button
                      type="button"
                      aria-label={t('preview.fullscreen')}
                      onClick={() => void videoCanvas.current?.requestFullscreen?.()}
                    >
                      ⛶
                    </button>
                  </div>
                )}
              </div>
            </div>
            {data.clips.length > 0 && (
              <div className="transport">
                <button disabled={!clip || selected === 0} onClick={() => selectClip(selected - 1)}>
                  ‹
                </button>
                <button
                  className="play-button"
                  disabled={!clip}
                  onClick={() => {
                    const target = clip ? videos.current.get(clip.media_version_id) : undefined;
                    if (!target) return;
                    if (target.paused) {
                      playIntent.current = true;
                      void target.play();
                    } else {
                      playIntent.current = false;
                      target.pause();
                    }
                  }}
                >
                  {playing ? 'Ⅱ' : '▶'}
                </button>
                <button
                  disabled={!clip || selected === data.clips.length - 1}
                  onClick={() => selectClip(selected + 1)}
                >
                  ›
                </button>
                <span>{`${selected + 1} / ${data.clips.length}`}</span>
              </div>
            )}
          </div>
          <div className="preview-timeline">
            {timelineItems.map((item) => (
              <button
                key={item.clip_id}
                className={`${item.available && item.clipIndex === selected ? 'active' : ''} ${item.available ? '' : 'is-skipped'}`}
                disabled={!item.available}
                aria-pressed={item.available && item.clipIndex === selected}
                onClick={() => item.available && selectClip(item.clipIndex)}
              >
                <span
                  className="timeline-thumb"
                  style={{ aspectRatio: `${data.output_width} / ${data.output_height}` }}
                >
                  {item.available ? <video src={item.media_url} muted preload="metadata" /> : <b>!</b>}
                </span>
                <span>{String(item.position).padStart(2, '0')}</span>
                <small>{item.available ? formatDuration(item.duration_ms) : t('preview.willSkip')}</small>
              </button>
            ))}
            {!timelineItems.length && <div className="timeline-empty">{t('preview.noClipSelected')}</div>}
          </div>
        </main>
        <aside className="export-panel">
          <nav className="preview-panel-tabs">
            <button
              className={activePanel === 'clip' ? 'active' : ''}
              disabled={!clip}
              aria-pressed={activePanel === 'clip'}
              onClick={() => setPanel('clip')}
            >
              {t('preview.clipPanel')}
            </button>
            <button
              className={activePanel === 'export' ? 'active' : ''}
              aria-pressed={activePanel === 'export'}
              onClick={() => setPanel('export')}
            >
              {t('preview.exportPanel')}
            </button>
          </nav>
          {activePanel === 'clip' ? (
            clip ? (
              <div className="clip-preview-panel">
                <div
                  className="clip-preview-panel__summary"
                  aria-label={t('preview.clip', { count: clip.position })}
                >
                  <div className="clip-preview-panel__media">
                    <video
                      src={clip.media_url}
                      muted
                      preload="metadata"
                      style={{ aspectRatio: `${data.output_width} / ${data.output_height}` }}
                    />
                  </div>
                </div>
                <ClipTrimEditor
                  key={persistedClip!.media_version_id}
                  projectID={projectId}
                  clip={persistedClip!}
                  onPreview={(milliseconds) => {
                    const target = videos.current.get(clip.media_version_id);
                    if (target) target.currentTime = milliseconds / 1000;
                  }}
                  onRangeChange={updateSelectedTrimPreview}
                  onSaved={refreshPreviewAfterTrim}
                />
              </div>
            ) : (
              <div className="preview-panel-empty">{t('preview.noClipSelected')}</div>
            )
          ) : null}
          {activePanel === 'export' && (
            <>
              <header>
                <span>{t('preview.exportSettings')}</span>
                <h2>MP4</h2>
              </header>
              <dl>
                <div>
                  <dt>{t('preview.resolution')}</dt>
                  <dd>
                    {data.output_width} × {data.output_height}
                  </dd>
                </div>
                <div>
                  <dt>{t('preview.durationLabel')}</dt>
                  <dd>{formatDuration(effectiveExportDuration)}</dd>
                </div>
                <div>
                  <dt>{t('preview.included')}</dt>
                  <dd>{data.included_clip_count}</dd>
                </div>
                <div>
                  <dt>{t('preview.skipped')}</dt>
                  <dd className={data.skipped_clip_count ? 'is-warning' : ''}>{data.skipped_clip_count}</dd>
                </div>
              </dl>
              <p className="info-note">{t('preview.frozenHint')}</p>
              {data.skipped_clips.length > 0 && (
                <section className="skipped-clips">
                  <h3>{t('preview.needsAttention')}</h3>
                  {data.skipped_clips.map((item) => (
                    <Link
                      key={item.clip_id}
                      to={`/projects/${projectId}/episodes/${episodeId}/storyboard/clips/${item.clip_id}`}
                    >
                      <strong>{t('preview.clip', { count: item.position })}</strong>
                      <small>{t(`preview.reasons.${item.reason}`)}</small>
                    </Link>
                  ))}
                </section>
              )}
              {latestExport && (
                <div className={`export-status export-status--${latestExport.state}`} role="status">
                  <strong>{t(`tasks.states.${latestExport.state}`)}</strong>
                  <p>
                    {latestExport.state === 'succeeded'
                      ? t('preview.exportSucceededHint')
                      : latestExport.state === 'failed'
                        ? t('preview.exportTaskFailed', { code: latestExport.failure_code || 'unknown' })
                        : latestExport.progress_message || t('preview.submittedHint')}
                  </p>
                </div>
              )}
              {!latestExport && exporter.isSuccess && (
                <div className="export-submitted">
                  <strong>{t('preview.submitted')}</strong>
                  <p>{t('preview.submittedHint')}</p>
                </div>
              )}
              {exporter.isError && <p className="form-error">{t('preview.exportFailed')}</p>}
            </>
          )}
        </aside>
      </div>
    </div>
  );
}

const trimStepMilliseconds = 100;

function exportContentURL(projectID: string, exportJobID: string) {
  return `/api/v1/projects/${encodeURIComponent(projectID)}/exports/${encodeURIComponent(exportJobID)}/content`;
}

function VolumeIcon({ muted }: { muted: boolean }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M4 10v4h4l5 4V6L8 10H4Z" />
      {muted ? (
        <>
          <path d="m17 10 4 4" />
          <path d="m21 10-4 4" />
        </>
      ) : (
        <>
          <path d="M16 9.5a4 4 0 0 1 0 5" />
          <path d="M18.5 7a7.5 7.5 0 0 1 0 10" />
        </>
      )}
    </svg>
  );
}

function DownloadIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M12 4v11" />
      <path d="m8 11 4 4 4-4" />
      <path d="M5 19h14" />
    </svg>
  );
}

function ClipTrimEditor({
  projectID,
  clip,
  onPreview,
  onRangeChange,
  onSaved,
}: {
  projectID: string;
  clip: PreviewClip;
  onPreview: (milliseconds: number) => void;
  onRangeChange: (trimIn: number, trimOut: number, unsaved: boolean) => void;
  onSaved: () => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const media = useQuery({
    queryKey: ['media-slot', projectID, clip.media_slot_id],
    queryFn: () => getMediaSlot(projectID, clip.media_slot_id),
  });
  const [trimIn, setTrimIn] = useState(clip.trim_in_ms);
  const [trimOut, setTrimOut] = useState(clip.trim_out_ms);
  useEffect(() => {
    setTrimIn(clip.trim_in_ms);
    setTrimOut(clip.trim_out_ms);
  }, [clip.media_version_id, clip.trim_in_ms, clip.trim_out_ms]);
  const version = media.data?.versions.find((item) => item.media_version_id === clip.media_version_id);
  const artifact = media.data?.artifacts.find((item) => item.artifact_id === version?.artifact_id);
  const sourceDuration = artifact?.duration_ms ?? Math.max(clip.trim_out_ms, trimOut);
  const maximumIn = Math.max(0, trimOut - trimStepMilliseconds);
  const minimumOut = Math.min(sourceDuration, trimIn + trimStepMilliseconds);
  const persistedTrimIn = version?.trim_in_ms ?? clip.trim_in_ms;
  const persistedTrimOut = version?.trim_out_ms ?? clip.trim_out_ms;
  const dirty = trimIn !== persistedTrimIn || trimOut !== persistedTrimOut;
  const save = useMutation({
    mutationFn: (range: { trimIn: number; trimOut: number; revision: number }) =>
      updateVideoTrim(
        projectID,
        clip.media_slot_id,
        clip.media_version_id,
        range.trimIn,
        range.trimOut,
        range.revision,
      ),
    onSuccess: async (details) => {
      queryClient.setQueryData(['media-slot', projectID, clip.media_slot_id], details);
      await onSaved();
    },
  });
  useEffect(() => {
    onRangeChange(trimIn, trimOut, dirty || save.isPending);
    if (!dirty || media.isPending || save.isPending || save.isError) return;
    const timeout = window.setTimeout(
      () =>
        save.mutate({
          trimIn,
          trimOut,
          revision: version?.revision ?? clip.media_version_revision,
        }),
      500,
    );
    return () => window.clearTimeout(timeout);
  }, [
    clip.media_version_revision,
    dirty,
    media.isPending,
    onRangeChange,
    save.isError,
    save.isPending,
    trimIn,
    trimOut,
    version?.revision,
  ]);
  const updateIn = (milliseconds: number) => {
    const value = Math.min(maximumIn, Math.max(0, milliseconds));
    if (save.isError) save.reset();
    setTrimIn(value);
    onPreview(value);
  };
  const updateOut = (milliseconds: number) => {
    const value = Math.max(minimumOut, Math.min(sourceDuration, milliseconds));
    if (save.isError) save.reset();
    setTrimOut(value);
    onPreview(Math.max(trimIn, value - trimStepMilliseconds));
  };
  const startPercent = sourceDuration ? (trimIn / sourceDuration) * 100 : 0;
  const endPercent = sourceDuration ? (trimOut / sourceDuration) * 100 : 100;

  return (
    <section className="clip-trim-editor">
      <h3>{t('preview.trimRange')}</h3>
      <dl>
        <div>
          <dt>{t('preview.sourceDuration')}</dt>
          <dd>{formatDuration(sourceDuration)}</dd>
        </div>
        <div>
          <dt>{t('preview.durationLabel')}</dt>
          <dd>{formatPreciseDuration(trimOut - trimIn)}</dd>
        </div>
      </dl>
      <div
        className="trim-range"
        style={{ '--trim-start': `${startPercent}%`, '--trim-end': `${endPercent}%` } as CSSProperties}
      >
        <span />
        <input
          type="range"
          min={0}
          max={sourceDuration}
          step={trimStepMilliseconds}
          value={trimIn}
          aria-label={t('preview.trimIn')}
          disabled={media.isPending}
          onChange={(event) => updateIn(Number(event.target.value))}
        />
        <input
          type="range"
          min={0}
          max={sourceDuration}
          step={trimStepMilliseconds}
          value={trimOut}
          aria-label={t('preview.trimOut')}
          disabled={media.isPending}
          onChange={(event) => updateOut(Number(event.target.value))}
        />
      </div>
      <div className="trim-time-fields">
        <label>
          <span>In</span>
          <input
            type="number"
            min={0}
            max={maximumIn / 1000}
            step={0.1}
            value={(trimIn / 1000).toFixed(1)}
            disabled={media.isPending}
            onChange={(event) => updateIn(Math.round(Number(event.target.value) * 1000))}
          />
          <em>s</em>
        </label>
        <label>
          <span>Out</span>
          <input
            type="number"
            min={minimumOut / 1000}
            max={sourceDuration / 1000}
            step={0.1}
            value={(trimOut / 1000).toFixed(1)}
            disabled={media.isPending}
            onChange={(event) => updateOut(Math.round(Number(event.target.value) * 1000))}
          />
          <em>s</em>
        </label>
      </div>
      <footer>
        <button
          type="button"
          className="button button--secondary"
          disabled={media.isPending || (trimIn === 0 && trimOut === sourceDuration)}
          onClick={() => {
            setTrimIn(0);
            setTrimOut(sourceDuration);
            onPreview(0);
          }}
        >
          {t('preview.resetTrim')}
        </button>
        <span className={`trim-save-state${save.isError ? ' is-error' : ''}`} role="status">
          {save.isError
            ? t('preview.trimSaveFailed')
            : dirty || save.isPending
              ? t('preview.savingTrim')
              : t('preview.trimSaved')}
        </span>
      </footer>
      {media.isError && <p className="form-error">{t('preview.trimLoadFailed')}</p>}
    </section>
  );
}

function formatDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

type DownloadErrorKind = 'downloadSessionFailedHint' | 'downloadDestinationFailedHint' | 'downloadFailedHint';

export function classifyDownloadError(error: unknown): DownloadErrorKind {
  const message = error instanceof Error ? error.message : String(error ?? '');
  if (message.includes('download_session_unavailable')) return 'downloadSessionFailedHint';
  if (
    message.includes('download_destination_') ||
    message.includes('download_write_failed') ||
    message.includes('download_finalize_failed')
  )
    return 'downloadDestinationFailedHint';
  return 'downloadFailedHint';
}

function formatPreciseDuration(milliseconds: number) {
  const tenths = Math.max(0, Math.floor(milliseconds / 100));
  const seconds = Math.floor(tenths / 10);
  return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}.${tenths % 10}`;
}
