import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  agentDockWidth,
  agentWebSearchEnabled,
  applyThemePreference,
  defaultAgentModel,
  defaultShotForwardAgentModel,
  preferredAgentModel,
  preferredAgentModelForRuntime,
  preferredVideoModel,
  setAgentDockWidth,
  setPreferredAgentModel,
  setPreferredAgentModelForRuntime,
  setPreferredVideoModel,
  setAgentWebSearch,
  setTaskNotifications,
  setThemePreference,
  taskNotificationsEnabled,
  themePreference,
} from './preferences';

describe('task notification preference', () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    vi.unstubAllGlobals();
  });

  it('enables notifications only after browser permission is granted', async () => {
    const notification = { permission: 'default', requestPermission: vi.fn().mockResolvedValue('granted') };
    vi.stubGlobal('Notification', notification);
    expect(await setTaskNotifications(true)).toBe(true);
    notification.permission = 'granted';
    expect(taskNotificationsEnabled()).toBe(true);
  });

  it('does not store a misleading enabled state when permission is denied', async () => {
    vi.stubGlobal('Notification', { permission: 'denied', requestPermission: vi.fn() });
    expect(await setTaskNotifications(true)).toBe(false);
    expect(window.localStorage.getItem('shotforward.task-notifications')).toBe('false');
  });

  it('enables Agent web search by default and preserves an explicit opt-out', () => {
    expect(agentWebSearchEnabled()).toBe(true);
    setAgentWebSearch(false);
    expect(agentWebSearchEnabled()).toBe(false);
    setAgentWebSearch(true);
    expect(agentWebSearchEnabled()).toBe(true);
  });

  it('defaults the Agent to Luna while preserving an explicit model choice', () => {
    expect(defaultAgentModel).toBe('gpt-5.6-luna');
    expect(defaultShotForwardAgentModel).toBe('gpt-6-luna');
    expect(preferredAgentModel()).toBe('gpt-5.6-luna');
    expect(preferredAgentModelForRuntime('shotforward')).toBe('gpt-6-luna');

    setPreferredAgentModel('gpt-6-astra');
    expect(preferredAgentModel()).toBe('gpt-6-astra');

    setPreferredAgentModel('');
    expect(preferredAgentModel()).toBe('gpt-5.6-luna');
  });

  it('stores separate model choices for ShotForward and local Codex', () => {
    setPreferredAgentModelForRuntime('shotforward', 'gpt-6-astra');
    setPreferredAgentModelForRuntime('user_codex', 'gpt-5.6-sol');

    expect(preferredAgentModelForRuntime('shotforward')).toBe('gpt-6-astra');
    expect(preferredAgentModelForRuntime('user_codex')).toBe('gpt-5.6-sol');
  });

  it('stores the video model selected by the user', () => {
    expect(preferredVideoModel()).toBe('');

    setPreferredVideoModel('video.wan-3.0.official\u001fwan3.0-video-prime');
    expect(preferredVideoModel()).toBe('video.wan-3.0.official\u001fwan3.0-video-prime');

    setPreferredVideoModel('');
    expect(preferredVideoModel()).toBe('');
  });

  it('does not retain the legacy single-model ShotForward default', () => {
    window.localStorage.setItem('shotforward.agent-model.shotforward', 'gpt-6-astra');
    expect(preferredAgentModelForRuntime('shotforward')).toBe('gpt-6-luna');
  });

  it('clamps and restores the Agent dock layout preference', () => {
    expect(agentDockWidth()).toBe(320);
    expect(setAgentDockWidth(900)).toBe(460);
    expect(agentDockWidth()).toBe(460);
    expect(setAgentDockWidth(100)).toBe(300);
  });

  it('applies and restores a validated Studio color theme', () => {
    expect(applyThemePreference()).toBe('brand');
    expect(document.documentElement).toHaveAttribute('data-theme', 'brand');

    setThemePreference('mist');
    expect(themePreference()).toBe('mist');
    expect(window.localStorage.getItem('shotforward.theme')).toBe('mist');
    expect(document.documentElement).toHaveAttribute('data-theme', 'mist');

    window.localStorage.setItem('shotforward.theme', 'unsupported');
    expect(themePreference()).toBe('brand');
  });
});
