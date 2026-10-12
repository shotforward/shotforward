import { describe, expect, it } from 'vitest';
import en from './locales/en';
import zhCN from './locales/zh-CN';

function leafKeys(value: unknown, prefix = ''): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [prefix];
  return Object.entries(value).flatMap(([key, child]) => leafKeys(child, prefix ? `${prefix}.${key}` : key));
}

function leafValues(value: unknown): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [String(value ?? '')];
  return Object.values(value).flatMap(leafValues);
}

describe('locale catalogs', () => {
  it('keep English and Chinese translation contracts in sync', () => {
    const contract = (catalog: unknown) =>
      [...new Set(leafKeys(catalog).map((key) => key.replace(/_(?:one|other)$/, '')))].sort();
    expect(contract(zhCN)).toEqual(contract(en));
  });

  it('keeps the Chinese interface free of mixed Episode terminology', () => {
    expect(leafValues(zhCN).join('\n')).not.toMatch(/\bEpisode\b/);
  });

  it('uses one Agent edit label for ordinary and model-specific video prompts', () => {
    expect(zhCN.storyboard.agentAdaptPrompt).toBe('Agent 修改');
    expect(zhCN.storyboard.agentAdaptPrompt).toBe(zhCN.storyboard.agentEditPrompt);
    expect(en.storyboard.agentAdaptPrompt).toBe(en.storyboard.agentEditPrompt);
  });
});
