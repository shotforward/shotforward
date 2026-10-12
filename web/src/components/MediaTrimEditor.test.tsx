import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import '../i18n';
import type { Artifact } from '../api/assets';
import { MediaTrimEditor } from './MediaTrimEditor';
import { mediaTrimRange, savedMediaTrim, validMediaTrim } from '../mediaTrim';

function Editor({ kind = 'audio' }: { kind?: 'audio' | 'video' }) {
  const [range, setRange] = useState([3000, 11000]);
  return (
    <MediaTrimEditor
      projectID="project"
      artifact={{ artifact_id: 'artifact', kind, duration_ms: 15_000 } as Artifact}
      start={range[0]!}
      end={range[1]!}
      onChange={(start, end) => setRange([start, end])}
    />
  );
}

describe('non-destructive Episode media trim controls', () => {
  it.each(['audio', 'video'] as const)(
    'edits %s using millisecond numeric values and restores the complete original',
    (kind) => {
      render(<Editor kind={kind} />);
      expect(screen.getByText(/Selected 0:08 · Original 0:15|选区 0:08 · 原始 0:15/)).toBeInTheDocument();
      const start = screen.getByRole('spinbutton', { name: /Start \(seconds\)|开始时间/ });
      const end = screen.getByRole('spinbutton', { name: /End \(seconds\)|结束时间/ });
      fireEvent.change(start, { target: { value: '4.125' } });
      expect(screen.getByRole('slider', { name: /Selection start handle|选区开始位置/ })).toHaveValue('4125');
      fireEvent.change(end, { target: { value: '9.375' } });
      expect(
        screen.getByText(/Selected 0:05.25 · Original 0:15|选区 0:05.25 · 原始 0:15/),
      ).toBeInTheDocument();
      fireEvent.change(screen.getByRole('slider', { name: /Selection start handle|选区开始位置/ }), {
        target: { value: '7000' },
      });
      expect(start).toHaveValue(7);
      fireEvent.click(screen.getByRole('button', { name: /Restore full original|恢复完整原始素材/ }));
      expect(start).toHaveValue(0);
      expect(end).toHaveValue(15);
      expect(screen.getByText(/Selected 0:15 · Original 0:15|选区 0:15 · 原始 0:15/)).toBeInTheDocument();
    },
  );

  it('rejects reversed/outside/empty ranges and does not play the full original as fallback', () => {
    const { container } = render(<Editor />);
    const start = screen.getByRole('spinbutton', { name: /Start \(seconds\)|开始时间/ });
    const end = screen.getByRole('spinbutton', { name: /End \(seconds\)|结束时间/ });
    fireEvent.change(start, { target: { value: '12' } });
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(container.querySelector('audio')).not.toBeInTheDocument();
    fireEvent.change(end, { target: { value: '18' } });
    expect(screen.getByRole('alert')).toBeInTheDocument();
    fireEvent.change(start, { target: { value: '' } });
    expect(start).toHaveValue(null);
    expect(container.querySelector('audio')).not.toBeInTheDocument();
  });

  it('resolves legacy full ranges without mutating Artifact physical metadata', () => {
    const original = { duration_ms: 15000 };
    expect(mediaTrimRange(original)).toEqual({ start: 0, end: 15000, duration: 15000, original: 15000 });
    expect(mediaTrimRange(original, { trim_start_ms: 3000, trim_end_ms: 11000 }).duration).toBe(8000);
    expect(original.duration_ms).toBe(15000);
    expect(savedMediaTrim(0, 15000, 15000)).toEqual({ trim_start_ms: 0, trim_end_ms: 0 });
    expect(savedMediaTrim(3000, 11000, 15000)).toEqual({ trim_start_ms: 3000, trim_end_ms: 11000 });
    for (const [start, end] of [
      [0, 0],
      [5000, 3000],
      [-1, 9000],
      [0, 15001],
      [0.5, 9000],
      [NaN, 9000],
    ])
      expect(validMediaTrim(start!, end!, 15000)).toBe(false);
  });
});
