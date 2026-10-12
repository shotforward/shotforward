export interface ShotForwardDesktopBridge {
  selectProjectParent(): Promise<string | null>;
  selectScriptFile?(): Promise<string | null>;
  openExternal(url: string): Promise<void>;
  saveDownload?(url: string, suggestedName: string): Promise<boolean>;
  saveTextFile?(suggestedName: string, contents: string): Promise<boolean>;
}

declare global {
  interface Window {
    __SHOTFORWARD_DESKTOP__?: ShotForwardDesktopBridge;
  }
}

export function hasDesktopBridge(): boolean {
  return Boolean(window.__SHOTFORWARD_DESKTOP__);
}

export async function selectProjectParent(): Promise<string | null> {
  const bridge = window.__SHOTFORWARD_DESKTOP__;
  if (!bridge) return null;
  return bridge.selectProjectParent();
}

export async function selectScriptFile(): Promise<string | null> {
  const bridge = window.__SHOTFORWARD_DESKTOP__;
  if (!bridge?.selectScriptFile) throw new Error('The native script file picker is unavailable');
  return bridge.selectScriptFile();
}

export async function openExternal(url: string): Promise<void> {
  const bridge = window.__SHOTFORWARD_DESKTOP__;
  if (bridge) {
    await bridge.openExternal(url);
    return;
  }
  const opened = window.open(url, '_blank', 'noopener,noreferrer');
  if (!opened) throw new Error('The browser blocked the new window');
}

export async function saveDownload(url: string, suggestedName: string): Promise<boolean> {
  const fileName = safeDownloadName(suggestedName);
  const bridge = window.__SHOTFORWARD_DESKTOP__;
  if (bridge?.saveDownload) return bridge.saveDownload(url, fileName);
  browserDownload(url, fileName);
  return true;
}

function browserDownload(url: string, fileName: string): void {
  const link = document.createElement('a');
  link.href = url;
  link.download = fileName;
  link.hidden = true;
  document.body.append(link);
  link.click();
  link.remove();
}

export async function saveTextFile(suggestedName: string, contents: string): Promise<boolean> {
  const fileName = safeDownloadName(suggestedName);
  const bridge = window.__SHOTFORWARD_DESKTOP__;
  if (bridge?.saveTextFile) return bridge.saveTextFile(fileName, contents);
  const url = URL.createObjectURL(new Blob([contents], { type: 'application/json' }));
  try {
    browserDownload(url, fileName);
    return true;
  } finally {
    URL.revokeObjectURL(url);
  }
}

export function safeDownloadName(value: string): string {
  const cleaned = value
    .trim()
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, '-')
    .replace(/[. ]+$/g, '')
    .trim();
  return cleaned || 'shotforward-download';
}

export function projectPathInParent(parent: string, projectName: string): string {
  const folder = projectName
    .trim()
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, '-')
    .replace(/[. ]+$/g, '')
    .trim();
  if (!folder) return '';
  const separator =
    /^[A-Za-z]:[\\/]/.test(parent) || (parent.includes('\\') && !parent.includes('/')) ? '\\' : '/';
  const base = parent.replace(/[\\/]+$/g, '');
  return `${base}${separator}${folder}`;
}
