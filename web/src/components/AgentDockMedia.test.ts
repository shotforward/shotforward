import { describe, expect, it, vi } from 'vitest';
import {
  agentMediaErrorMessageKey,
  clipboardImageFiles,
  maximumAgentMediaFileBytes,
  prepareAgentImageFiles,
} from './AgentDockMedia';

describe('Agent image inputs', () => {
  it.each(['', 'application/octet-stream', 'image/jpeg'])('accepts a PNG suffix despite MIME %s', (type) => {
    const file = new File(['image'], 'Screenshot.PNG', { type });
    expect(prepareAgentImageFiles([file])).toEqual({ files: [file] });
  });

  it.each(['png', 'jpg', 'jpeg', 'webp'])('accepts the supported .%s suffix', (extension) => {
    const file = new File(['image'], `reference.${extension}`);
    expect(prepareAgentImageFiles([file])).toEqual({ files: [file] });
  });

  it.each(['bin', 'gif', 'unknown'])(
    'accepts a supported MIME with the .%s suffix unchanged',
    (extension) => {
      const file = new File(['image'], `reference.${extension}`, { type: 'image/png' });
      expect(prepareAgentImageFiles([file])).toEqual({ files: [file] });
    },
  );

  it('gives extensionless clipboard images a suitable filename without changing their bytes', async () => {
    const file = new File(['clipboard image'], 'image', { type: 'image/png', lastModified: 123 });
    const result = prepareAgentImageFiles([file]);
    expect(result.errorCode).toBeUndefined();
    expect(result.files[0]).toMatchObject({
      name: 'image.png',
      type: 'image/png',
      size: file.size,
      lastModified: 123,
    });
    expect(result.files[0]?.slice().size).toBe(file.size);
  });

  it.each(['gif', 'heic', 'tiff', 'svg', 'pdf'])(
    'rejects unsupported .%s files before uploading',
    (extension) => {
      expect(
        prepareAgentImageFiles([
          new File(['image'], `reference.${extension}`, { type: 'application/octet-stream' }),
        ]),
      ).toEqual({ files: [], errorCode: 'unsupported_media_type' });
    },
  );

  it('accepts exactly 25 MiB, rejects the next byte, and rejects empty files', () => {
    const file = new File(['image'], 'boundary.png');
    Object.defineProperty(file, 'size', { value: maximumAgentMediaFileBytes, configurable: true });
    expect(prepareAgentImageFiles([file])).toEqual({ files: [file] });
    Object.defineProperty(file, 'size', { value: maximumAgentMediaFileBytes + 1 });
    expect(prepareAgentImageFiles([file])).toEqual({ files: [], errorCode: 'agent_media_too_large' });
    expect(prepareAgentImageFiles([new File([], 'empty.png')])).toEqual({
      files: [],
      errorCode: 'invalid_media',
    });
  });

  it('validates a whole batch before returning any uploadable files', () => {
    expect(
      prepareAgentImageFiles([new File(['image'], 'first.png'), new File(['bad'], 'second.gif')]),
    ).toEqual({ files: [], errorCode: 'unsupported_media_type' });
  });

  it('uses clipboard file items once instead of duplicating them from files', () => {
    const image = new File(['image'], 'image.png');
    const data = {
      items: [{ kind: 'file', getAsFile: () => image }],
      files: [image],
    } as unknown as DataTransfer;
    expect(clipboardImageFiles(data)).toEqual([image]);
  });

  it('falls back to clipboard files and exposes unsupported image formats for a useful error', () => {
    const gif = new File(['image'], 'image.gif', { type: 'image/gif' });
    expect(clipboardImageFiles({ items: [], files: [gif] } as unknown as DataTransfer)).toEqual([gif]);
  });

  it('leaves plain text, HTML URLs, Finder file URLs, and null file items alone', () => {
    const getData = vi.fn();
    const data = {
      items: [
        { kind: 'string', getAsFile: vi.fn() },
        { kind: 'file', getAsFile: () => null },
      ],
      files: [],
      getData,
    } as unknown as DataTransfer;
    expect(clipboardImageFiles(data)).toEqual([]);
    expect(getData).not.toHaveBeenCalled();
  });

  it.each([
    ['unsupported_media_type', 'unsupportedType'],
    ['agent_media_too_large', 'tooLarge'],
    ['invalid_media', 'invalidMedia'],
    ['invalid_agent_media', 'invalidMedia'],
    ['unreadable_media', 'unreadableMedia'],
    ['media_import_unavailable', 'importUnavailable'],
    ['authentication_required', 'sessionExpired'],
    ['csrf_rejected', 'sessionExpired'],
    ['external_failure', 'uploadFailed'],
    ['future_error', 'uploadFailed'],
  ])('maps %s to localized copy instead of raw backend text', (code, key) => {
    expect(agentMediaErrorMessageKey(code)).toBe(key);
  });

  it('recognizes a non-JSON upstream 413 too', () => {
    expect(agentMediaErrorMessageKey('http_413', 413)).toBe('tooLarge');
  });
});
