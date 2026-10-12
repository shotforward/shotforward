import { useTranslation } from 'react-i18next';
import { artifactContentURL, type Artifact } from '../api/assets';
import { formatTrimTime, validMediaTrim } from '../mediaTrim';
import { TrimmedMediaPlayer } from './TrimmedMediaPlayer';

export function MediaTrimEditor({
  projectID,
  artifact,
  start,
  end,
  onChange,
}: {
  projectID: string;
  artifact: Artifact;
  start: number;
  end: number;
  onChange: (start: number, end: number) => void;
}) {
  const { t } = useTranslation();
  if (artifact.kind === 'image') return null;
  const original = artifact.duration_ms;
  const valid = validMediaTrim(start, end, original);
  return (
    <section className="media-trim-editor" aria-label={t('mediaTrim.title')}>
      <header>
        <strong>{t('mediaTrim.title')}</strong>
        <button type="button" className="button button--secondary" onClick={() => onChange(0, original)}>
          {t('mediaTrim.reset')}
        </button>
      </header>
      {valid && (
        <TrimmedMediaPlayer
          src={artifactContentURL(projectID, artifact.artifact_id)}
          kind={artifact.kind}
          duration={original}
          trim={{ trim_start_ms: start, trim_end_ms: end }}
        />
      )}
      <div className="media-trim-editor__timeline">
        <div className="media-trim-editor__track" aria-hidden="true">
          <span
            style={{
              left: `${valid ? (start / original) * 100 : 0}%`,
              width: `${valid ? ((end - start) / original) * 100 : 0}%`,
            }}
          />
        </div>
        <input
          type="range"
          aria-label={t('mediaTrim.startHandle')}
          title={t('mediaTrim.startHandle')}
          min={0}
          max={original}
          step={1}
          value={Number.isFinite(start) ? Math.min(original, Math.max(0, start)) : 0}
          onChange={(event) =>
            onChange(
              Math.min(Number(event.target.value), (Number.isFinite(end) ? end : original) - 1),
              Number.isFinite(end) ? end : original,
            )
          }
        />
        <input
          type="range"
          aria-label={t('mediaTrim.endHandle')}
          title={t('mediaTrim.endHandle')}
          min={0}
          max={original}
          step={1}
          value={Number.isFinite(end) ? Math.min(original, Math.max(0, end)) : original}
          onChange={(event) =>
            onChange(
              Number.isFinite(start) ? start : 0,
              Math.max(Number(event.target.value), (Number.isFinite(start) ? start : 0) + 1),
            )
          }
        />
      </div>
      <div className="media-trim-editor__fields">
        <label className="field">
          <span>{t('mediaTrim.start')}</span>
          <input
            type="number"
            min={0}
            max={original / 1000}
            step={0.001}
            value={Number.isFinite(start) ? start / 1000 : ''}
            onChange={(event) =>
              onChange(
                event.target.value === '' ? Number.NaN : Math.round(Number(event.target.value) * 1000),
                end,
              )
            }
          />
        </label>
        <label className="field">
          <span>{t('mediaTrim.end')}</span>
          <input
            type="number"
            min={0}
            max={original / 1000}
            step={0.001}
            value={Number.isFinite(end) ? end / 1000 : ''}
            onChange={(event) =>
              onChange(
                start,
                event.target.value === '' ? Number.NaN : Math.round(Number(event.target.value) * 1000),
              )
            }
          />
        </label>
      </div>
      <p className="media-trim-editor__duration">
        {t('mediaTrim.durations', {
          selected: valid ? formatTrimTime(end - start) : '—',
          original: formatTrimTime(original),
        })}
      </p>
      {!valid && (
        <p className="form-error" role="alert">
          {t('mediaTrim.invalid')}
        </p>
      )}
      <p className="info-note">{t('mediaTrim.hint')}</p>
    </section>
  );
}
