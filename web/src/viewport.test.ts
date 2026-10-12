import { beforeEach, describe, expect, it } from 'vitest';
import { applyViewportPreference } from './viewport';

describe('applyViewportPreference', () => {
  beforeEach(() => {
    window.localStorage.clear();
    window.history.replaceState({}, '', '/');
    document.head.innerHTML = '<meta name="viewport" content="width=device-width, initial-scale=1.0">';
    Object.defineProperty(window.screen, 'width', { configurable: true, value: 360 });
  });

  it('persists and applies a scaled 1440px desktop layout', () => {
    window.history.replaceState({}, '', '/?desktop=1');

    expect(applyViewportPreference()).toBe(true);
    expect(document.querySelector('meta[name="viewport"]')).toHaveAttribute(
      'content',
      'width=1440, initial-scale=0.2500, minimum-scale=0.1, maximum-scale=5, user-scalable=yes',
    );
    expect(window.localStorage.getItem('shotforward.desktop-layout')).toBe('1');

    window.history.replaceState({}, '', '/projects');
    expect(applyViewportPreference()).toBe(true);
  });

  it('restores responsive layout with desktop=0', () => {
    window.localStorage.setItem('shotforward.desktop-layout', '1');
    window.history.replaceState({}, '', '/?desktop=0');

    expect(applyViewportPreference()).toBe(false);
    expect(document.querySelector('meta[name="viewport"]')).toHaveAttribute(
      'content',
      'width=device-width, initial-scale=1.0',
    );
    expect(window.localStorage.getItem('shotforward.desktop-layout')).toBeNull();
  });
});
