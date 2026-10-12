import { useEffect, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { formatTrimTime, mediaTrimRange, type MediaTrim } from '../mediaTrim';
import '../styles/media-trim.css';

/** Native controls expose the entire original. Use a selection-relative timeline instead. */
export function TrimmedMediaPlayer({
  src,
  kind,
  duration,
  trim,
  autoPlay = false,
  videoRef,
}: {
  src: string;
  kind: 'audio' | 'video';
  duration: number;
  trim?: MediaTrim;
  autoPlay?: boolean;
  videoRef?: RefObject<HTMLVideoElement | null>;
}) {
  const { t } = useTranslation();
  const element = useRef<HTMLMediaElement | null>(null);
  const [position, setPosition] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [muted, setMuted] = useState(false);
  const [failed, setFailed] = useState(false);
  const [playbackRate, setPlaybackRate] = useState(1);
  const range = mediaTrimRange({ duration_ms: duration }, trim);
  const { start, end } = range;

  const syncPosition = () => {
    const media = element.current;
    if (!media || !range.duration) return;
    if (media.currentTime * 1000 < start) media.currentTime = start / 1000;
    if (media.currentTime * 1000 >= end) {
      if (!media.paused) media.pause();
      if (Math.abs(media.currentTime * 1000 - end) > 1) media.currentTime = end / 1000;
      setPlaying(false);
    }
    setPosition(Math.min(range.duration, Math.max(0, media.currentTime * 1000 - start)));
  };
  const play = () => {
    const media = element.current;
    if (!media || !range.duration) return;
    if (media.currentTime * 1000 < start || media.currentTime * 1000 >= end) {
      media.currentTime = start / 1000;
      setPosition(0);
    }
    void media.play().catch(() => setPlaying(false));
  };
  const prepare = () => {
    const media = element.current;
    if (!media) return;
    media.currentTime = start / 1000;
    setPosition(0);
    setPlaybackRate(media.playbackRate || 1);
    if (autoPlay) play();
  };

  useEffect(() => {
    const media = element.current;
    setPosition(0);
    setPlaying(false);
    setFailed(false);
    if (media && !media.paused) media.pause();
    if (media && media.readyState >= 1) {
      media.currentTime = start / 1000;
      if (autoPlay) void media.play().catch(() => setPlaying(false));
    }
  }, [src, start, end, autoPlay]);

  // timeupdate can be sparse. Check every animation frame and schedule the end boundary too.
  useEffect(() => {
    const media = element.current;
    if (!media || !playing) return;
    let frame = 0;
    const watch = () => {
      if (media.currentTime * 1000 >= end) {
        media.pause();
        media.currentTime = end / 1000;
        setPosition(range.duration);
        setPlaying(false);
      } else frame = requestAnimationFrame(watch);
    };
    frame = requestAnimationFrame(watch);
    const remaining = Math.max(0, end - media.currentTime * 1000) / playbackRate;
    const timer = window.setTimeout(() => {
      if (media.currentTime * 1000 >= end - 10) {
        media.pause();
        media.currentTime = end / 1000;
        setPosition(range.duration);
        setPlaying(false);
      }
    }, remaining);
    return () => {
      cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, [playing, end, range.duration, position, playbackRate]);

  const mediaProps = {
    src,
    preload: 'metadata' as const,
    muted,
    onLoadedMetadata: prepare,
    onTimeUpdate: syncPosition,
    onSeeking: syncPosition,
    onRateChange: () => setPlaybackRate(element.current?.playbackRate || 1),
    onPlay: () => {
      const media = element.current;
      if (media && (media.currentTime * 1000 < start || media.currentTime * 1000 >= end)) {
        media.currentTime = start / 1000;
        setPosition(0);
      }
      setPlaying(true);
    },
    onPause: () => setPlaying(false),
    onEnded: () => {
      setPlaying(false);
      setPosition(range.duration);
    },
    onError: () => setFailed(true),
  };

  return (
    <div className={`trimmed-media-player is-${kind}`}>
      {kind === 'video' ? (
        <video
          {...mediaProps}
          playsInline
          ref={(value) => {
            element.current = value;
            if (videoRef) videoRef.current = value;
          }}
        />
      ) : (
        <audio
          {...mediaProps}
          ref={(value) => {
            element.current = value;
          }}
        />
      )}
      <div className="trimmed-media-player__controls">
        <button
          type="button"
          className="icon-button"
          aria-label={t(playing ? 'mediaTrim.pause' : 'mediaTrim.play')}
          disabled={!range.duration || failed}
          onClick={() => (playing ? element.current?.pause() : play())}
        >
          {playing ? 'Ⅱ' : '▶'}
        </button>
        <output>
          {formatTrimTime(position)} / {formatTrimTime(range.duration)}
        </output>
        <input
          type="range"
          aria-label={t('mediaTrim.seek')}
          min={0}
          max={range.duration}
          step={1}
          value={position}
          disabled={!range.duration || failed}
          onChange={(event) => {
            const relative = Math.min(range.duration, Math.max(0, Number(event.target.value)));
            if (element.current) element.current.currentTime = (start + relative) / 1000;
            setPosition(relative);
            if (relative === range.duration && element.current && !element.current.paused) {
              element.current.pause();
              setPlaying(false);
            }
          }}
        />
        <button
          type="button"
          className="icon-button"
          aria-label={t(muted ? 'mediaTrim.unmute' : 'mediaTrim.mute')}
          onClick={() => setMuted(!muted)}
        >
          {muted ? '◌' : '♫'}
        </button>
      </div>
      {failed && (
        <p className="form-error" role="alert">
          {t('mediaTrim.playbackFailed')}
        </p>
      )}
    </div>
  );
}
