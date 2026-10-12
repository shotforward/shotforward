import { afterEach, describe, expect, it } from 'vitest';
import i18n from './index';

describe('runtime language switching', () => {
  afterEach(async () => {
    await i18n.changeLanguage('zh-CN');
  });

  it('switches between complete Chinese and English UI catalogs and persists the choice', async () => {
    await i18n.changeLanguage('en');
    expect(i18n.t('nav.projects')).toBe('Projects');
    expect(document.documentElement.lang).toBe('en');
    expect(window.localStorage.getItem('shotforward.language')).toBe('en');

    await i18n.changeLanguage('zh-CN');
    expect(i18n.t('nav.projects')).toBe('项目');
    expect(document.documentElement.lang).toBe('zh-CN');
    expect(window.localStorage.getItem('shotforward.language')).toBe('zh-CN');
  });
});
