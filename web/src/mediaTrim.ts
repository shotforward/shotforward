import type { Artifact } from './api/assets';

export interface MediaTrim {
  trim_start_ms?: number;
  trim_end_ms?: number;
}

/** Keep physical Artifact metadata immutable. The range belongs to an Episode's library entry. */
export function mediaTrimRange(artifact: Pick<Artifact, 'duration_ms'>, trim?: MediaTrim) {
  const original = Math.max(0, Number.isFinite(artifact.duration_ms) ? artifact.duration_ms : 0);
  const requestedStart = trim?.trim_start_ms ?? 0;
  const requestedEnd = trim?.trim_end_ms ?? 0;
  const full = requestedStart === 0 && requestedEnd === 0;
  const start = full ? 0 : Math.min(original, Math.max(0, requestedStart));
  const end = full ? original : Math.min(original, Math.max(start, requestedEnd));
  return { start, end, duration: end - start, original };
}

export function validMediaTrim(start: number, end: number, original: number) {
  return (
    Number.isSafeInteger(start) && Number.isSafeInteger(end) && start >= 0 && start < end && end <= original
  );
}

export function savedMediaTrim(start: number, end: number, original: number) {
  return start === 0 && end === original
    ? { trim_start_ms: 0, trim_end_ms: 0 }
    : { trim_start_ms: start, trim_end_ms: end };
}

export function formatTrimTime(milliseconds: number) {
  const safe = Math.max(0, Math.round(milliseconds));
  const seconds = Math.floor(safe / 1000);
  const fraction = safe % 1000;
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}${fraction ? `.${String(fraction).padStart(3, '0').replace(/0+$/, '')}` : ''}`;
}
