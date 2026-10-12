import { describe, expect, it } from 'vitest';
import { truncateUnicode } from './text';

describe('truncateUnicode', () => {
  it('counts emoji as one character and never splits it', () => {
    expect(truncateUnicode('甲乙🎬尾', 3)).toBe('甲乙🎬');
  });

  it('returns shorter text unchanged', () => {
    expect(truncateUnicode('short', 20)).toBe('short');
  });
});
