import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  projectPathInParent,
  safeDownloadName,
  saveDownload,
  saveTextFile,
  selectScriptFile,
} from './desktop';

describe('desktop bridge helpers', () => {
  afterEach(() => {
    delete window.__SHOTFORWARD_DESKTOP__;
    vi.restoreAllMocks();
  });

  it('builds portable project paths below the selected parent', () => {
    expect(projectPathInParent('/Users/creator/Videos', '山川:序章')).toBe('/Users/creator/Videos/山川-序章');
    expect(projectPathInParent('C:\\Users\\creator\\Videos', 'Mountain / Pilot')).toBe(
      'C:\\Users\\creator\\Videos\\Mountain - Pilot',
    );
  });

  it('sanitizes suggested file names and delegates downloads to the desktop save dialog', async () => {
    const saveDownloadBridge = vi.fn().mockResolvedValue(true);
    const saveTextFileBridge = vi.fn().mockResolvedValue(true);
    window.__SHOTFORWARD_DESKTOP__ = {
      selectProjectParent: vi.fn(),
      openExternal: vi.fn(),
      saveDownload: saveDownloadBridge,
      saveTextFile: saveTextFileBridge,
    };

    await expect(saveDownload('/api/v1/projects/p/artifacts/a/content', 'scene/01.mp4')).resolves.toBe(true);
    expect(saveDownloadBridge).toHaveBeenCalledWith('/api/v1/projects/p/artifacts/a/content', 'scene-01.mp4');

    await expect(saveTextFile('diagnostics?.json', '{"ok":true}')).resolves.toBe(true);
    expect(saveTextFileBridge).toHaveBeenCalledWith('diagnostics-.json', '{"ok":true}');
    expect(safeDownloadName(' .. ')).toBe('shotforward-download');
  });

  it('delegates script selection to the native desktop file picker', async () => {
    const picker = vi.fn().mockResolvedValue('/Users/creator/Documents/script.md');
    window.__SHOTFORWARD_DESKTOP__ = {
      selectProjectParent: vi.fn(),
      selectScriptFile: picker,
      openExternal: vi.fn(),
    };

    await expect(selectScriptFile()).resolves.toBe('/Users/creator/Documents/script.md');
    expect(picker).toHaveBeenCalledOnce();
  });
});
