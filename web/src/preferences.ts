import { useSyncExternalStore } from 'react';

const notificationKey = 'shotforward.task-notifications';
const agentWebSearchKey = 'shotforward.agent-web-search';
const agentDockWidthKey = 'shotforward.agent-dock-width';
const agentModelKey = 'shotforward.agent-model';
const agentModelKeys = {
  user_codex: 'shotforward.agent-model.user-codex',
  // v2 resets the former single-model ShotForward preference. Astra was the
  // only available choice before Luna and Sol were published, so retaining
  // that value would prevent the new cloud default from taking effect.
  shotforward: 'shotforward.agent-model.shotforward.v2',
} as const;
const themeKey = 'shotforward.theme';
const imageGeneratorKey = 'shotforward.image-generator';
const videoModelKey = 'shotforward.video-model';
const preferenceEvent = 'shotforward:preferences-changed';

export const defaultAgentModel = 'gpt-5.6-luna';
export const defaultShotForwardAgentModel = 'gpt-6-luna';

export type ThemePreference = 'brand' | 'mist' | 'sand';
export const defaultThemePreference: ThemePreference = 'brand';
export type ImageGeneratorPreference = 'image.codex' | 'image.shotforward';

export const defaultAgentDockWidth = 320;
export const minimumAgentDockWidth = 300;
export const maximumAgentDockWidth = 640;

function canNotify() {
  return typeof Notification !== 'undefined';
}

export function taskNotificationsEnabled() {
  return (
    canNotify() &&
    Notification.permission === 'granted' &&
    window.localStorage.getItem(notificationKey) === 'true'
  );
}

export async function setTaskNotifications(enabled: boolean) {
  let next = enabled;
  if (enabled) {
    if (!canNotify()) next = false;
    else if (Notification.permission !== 'granted') {
      next = (await Notification.requestPermission()) === 'granted';
    }
  }
  window.localStorage.setItem(notificationKey, String(next));
  window.dispatchEvent(new Event(preferenceEvent));
  return next;
}

function subscribe(listener: () => void) {
  window.addEventListener(preferenceEvent, listener);
  window.addEventListener('storage', listener);
  return () => {
    window.removeEventListener(preferenceEvent, listener);
    window.removeEventListener('storage', listener);
  };
}

export function themePreference(): ThemePreference {
  const stored = window.localStorage.getItem(themeKey);
  return stored === 'mist' || stored === 'sand' || stored === 'brand' ? stored : defaultThemePreference;
}

export function applyThemePreference(theme = themePreference()) {
  document.documentElement.dataset.theme = theme;
  return theme;
}

export function setThemePreference(theme: ThemePreference) {
  window.localStorage.setItem(themeKey, theme);
  applyThemePreference(theme);
  window.dispatchEvent(new Event(preferenceEvent));
}

export function useThemePreference() {
  return useSyncExternalStore(subscribe, themePreference, () => defaultThemePreference);
}

export function useTaskNotificationPreference() {
  return useSyncExternalStore(subscribe, taskNotificationsEnabled, () => false);
}

export function imageGeneratorPreference(): ImageGeneratorPreference {
  return window.localStorage.getItem(imageGeneratorKey) === 'image.shotforward'
    ? 'image.shotforward'
    : 'image.codex';
}
export function setImageGeneratorPreference(value: ImageGeneratorPreference) {
  window.localStorage.setItem(imageGeneratorKey, value);
  window.dispatchEvent(new Event(preferenceEvent));
}
export function useImageGeneratorPreference() {
  return useSyncExternalStore(subscribe, imageGeneratorPreference, () => 'image.codex' as const);
}

export function preferredVideoModel() {
  return window.localStorage.getItem(videoModelKey) ?? '';
}

export function setPreferredVideoModel(value: string) {
  if (value) window.localStorage.setItem(videoModelKey, value);
  else window.localStorage.removeItem(videoModelKey);
  window.dispatchEvent(new Event(preferenceEvent));
}

export function agentWebSearchEnabled() {
  return window.localStorage.getItem(agentWebSearchKey) !== 'false';
}

export function setAgentWebSearch(enabled: boolean) {
  window.localStorage.setItem(agentWebSearchKey, String(enabled));
  window.dispatchEvent(new Event(preferenceEvent));
}

export function preferredAgentModel() {
  return preferredAgentModelForRuntime('user_codex');
}

export function setPreferredAgentModel(modelID: string) {
  setPreferredAgentModelForRuntime('user_codex', modelID);
}

export type AgentRuntimePreference = keyof typeof agentModelKeys;

export function preferredAgentModelForRuntime(runtimeKind: AgentRuntimePreference) {
  const selected = window.localStorage.getItem(agentModelKeys[runtimeKind]);
  if (selected) return selected;
  if (runtimeKind === 'user_codex') {
    return window.localStorage.getItem(agentModelKey) ?? defaultAgentModel;
  }
  return defaultShotForwardAgentModel;
}

export function setPreferredAgentModelForRuntime(runtimeKind: AgentRuntimePreference, modelID: string) {
  if (modelID) window.localStorage.setItem(agentModelKeys[runtimeKind], modelID);
  else window.localStorage.removeItem(agentModelKeys[runtimeKind]);
  window.dispatchEvent(new Event(preferenceEvent));
}

export function useAgentWebSearchPreference() {
  return useSyncExternalStore(subscribe, agentWebSearchEnabled, () => true);
}

export function clampAgentDockWidth(value: number, viewportWidth = window.innerWidth) {
  const viewportMaximum = Math.floor(viewportWidth * 0.45);
  return Math.min(
    Math.max(value, minimumAgentDockWidth),
    Math.max(minimumAgentDockWidth, Math.min(maximumAgentDockWidth, viewportMaximum)),
  );
}

export function agentDockWidth() {
  const stored = Number(window.localStorage.getItem(agentDockWidthKey));
  return clampAgentDockWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultAgentDockWidth);
}

export function setAgentDockWidth(width: number) {
  const next = clampAgentDockWidth(width);
  window.localStorage.setItem(agentDockWidthKey, String(next));
  return next;
}
