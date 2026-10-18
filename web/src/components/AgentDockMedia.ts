export const maximumAgentAttachments = 5;
export const maximumAgentMediaFileBytes = 25 * 1024 * 1024;

const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'webp']);
const imageMimeExtensions: Record<string, string> = {
  'image/png': 'png',
  'image/jpeg': 'jpg',
  'image/webp': 'webp',
};

function fileExtension(name: string): string {
  return /\.([^.]+)$/.exec(name)?.[1]?.toLowerCase() ?? '';
}

export function prepareAgentImageFiles(files: readonly File[]): {
  files: File[];
  errorCode?: 'unsupported_media_type' | 'agent_media_too_large' | 'invalid_media';
} {
  const prepared: File[] = [];
  for (const file of files) {
    if (file.size > maximumAgentMediaFileBytes) return { files: [], errorCode: 'agent_media_too_large' };
    if (file.size === 0) return { files: [], errorCode: 'invalid_media' };
    const extension = fileExtension(file.name);
    if (imageExtensions.has(extension) || (extension && imageMimeExtensions[file.type])) {
      // Neither the suffix nor MIME is a security check. WebViews can supply
      // either incorrectly; the server validates the actual image bytes.
      prepared.push(file);
    } else if (!extension && imageMimeExtensions[file.type]) {
      // Some clipboard sources name their image only "image" or "blob".
      prepared.push(
        new File([file], `${file.name || 'pasted-image'}.${imageMimeExtensions[file.type]}`, {
          type: file.type,
          lastModified: file.lastModified,
        }),
      );
    } else {
      return { files: [], errorCode: 'unsupported_media_type' };
    }
  }
  return { files: prepared };
}

export function clipboardImageFiles(data: Pick<DataTransfer, 'items' | 'files'>): File[] {
  const isImage = (file: File) =>
    file.type.startsWith('image/') || imageExtensions.has(fileExtension(file.name));
  const files = Array.from(data.items ?? []).flatMap((item) => {
    if (item.kind !== 'file') return [];
    const file = item.getAsFile();
    return file && isImage(file) ? [file] : [];
  });
  // Use one representation, not both: items and files commonly contain the
  // same clipboard image. Never fetch URLs or parse HTML to obtain an image.
  return files.length ? files : Array.from(data.files ?? []).filter(isImage);
}

type AgentMediaErrorCopyKey =
  | 'tooLarge'
  | 'unsupportedType'
  | 'invalidMedia'
  | 'unreadableMedia'
  | 'importUnavailable'
  | 'sessionExpired'
  | 'uploadFailed';

export function agentMediaErrorMessageKey(code = '', status?: number): AgentMediaErrorCopyKey {
  if (code === 'agent_media_too_large' || status === 413) return 'tooLarge';
  if (code === 'unsupported_media_type') return 'unsupportedType';
  if (code === 'invalid_media' || code === 'invalid_agent_media') return 'invalidMedia';
  if (code === 'unreadable_media') return 'unreadableMedia';
  if (code === 'media_import_unavailable') return 'importUnavailable';
  if (code === 'authentication_required' || code === 'csrf_rejected') return 'sessionExpired';
  return 'uploadFailed';
}
