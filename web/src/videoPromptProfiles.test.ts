import { describe, expect, it } from 'vitest';
import {
  allVideoPromptText,
  parseVideoPromptSet,
  updateVideoPromptFamily,
  videoPromptFamily,
  videoPromptProfile,
  videoPromptText,
} from './api/storyboard';

describe('video prompt profiles', () => {
  it('maps providers to the three prompt families', () => {
    expect(videoPromptFamily('video.h3.shotforward')).toBe('h3');
    expect(videoPromptFamily('video.seedance-2.5.volcengine-cn')).toBe('seedance');
    expect(videoPromptFamily('video.wan-3.0.official')).toBe('wan3');
  });

  it('uses separate H3 profiles for base and Ref2VA', () => {
    expect(videoPromptProfile('h3', 'first_last_image')).toBe('h3_base@1');
    expect(videoPromptProfile('h3', 'reference')).toBe('h3_ref2va@1');
  });

  it('keeps legacy text readable and lazily adds family variants', () => {
    const stored = updateVideoPromptFamily('旧提示词', 'wan3', 'Wan 提示词', {
      profile: 'wan3@1',
      asset_ids: ['ast_1'],
      references: [
        { key: 'asset:ast_1', purposes: ['identity'] },
        { key: 'upload:art_1', purposes: ['motion'], note: '只参考动作' },
      ],
    });
    expect(videoPromptText(stored, 'wan3')).toBe('Wan 提示词');
    expect(parseVideoPromptSet(stored).legacy_prompt).toBe('旧提示词');
    expect(allVideoPromptText(stored)).toContain('旧提示词');
    expect(allVideoPromptText(stored)).toContain('Wan 提示词');
    expect(parseVideoPromptSet(stored).families.wan3?.references).toEqual([
      { key: 'asset:ast_1', purposes: ['identity'] },
      { key: 'upload:art_1', purposes: ['motion'], note: '只参考动作' },
    ]);
  });
});
