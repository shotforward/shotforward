import type { Artifact } from './api/assets';
import type { ProviderProfile } from './api/providers';

export interface AssetReferenceSelection {
  key: string;
  artifact: Artifact;
  name: string;
  episodeID: string;
  episodeTitle: string;
  source: 'version' | 'asset' | 'upload';
}

// Both image backends currently accept at most five references. Catalog limits
// can narrow that limit, but must not exceed the backend's supported maximum.
export function imageReferenceMaximum(provider: ProviderProfile | undefined, modelID?: string): number {
  const model =
    provider?.models?.find((item) => item.model_id === (modelID || provider.model_id)) ??
    provider?.models?.find((item) => item.is_default);
  const capabilities = model?.capabilities;
  const totalMaximum =
    capabilities && capabilities.maximum_total_inputs > 0 ? capabilities.maximum_total_inputs : 5;
  if (!capabilities?.inputs) return Math.min(5, totalMaximum);
  const input = capabilities.inputs.find((item) => item.role === 'reference_image');
  if (!input) return 0;
  return Math.max(0, Math.min(5, input.maximum_count, totalMaximum));
}

export function uniqueImageReferences(values: AssetReferenceSelection[]): AssetReferenceSelection[] {
  const seen = new Set<string>();
  return values.filter(({ artifact }) => {
    if (seen.has(artifact.artifact_id)) return false;
    seen.add(artifact.artifact_id);
    return true;
  });
}

export function imageReferenceLimitMessage(language: string | undefined, maximum: number): string {
  return language?.startsWith('zh')
    ? `当前服务最多支持 ${maximum} 张参考图，请减少选择后重试。`
    : `This service supports at most ${maximum} reference images. Remove extra images and try again.`;
}
