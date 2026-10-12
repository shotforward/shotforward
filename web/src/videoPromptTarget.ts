import {
  availableProviderModels,
  isProviderAvailable,
  type ProviderModel,
  type ProviderProfile,
} from './api/providers';
import type { AgentVideoPromptTarget } from './api/agent';
import { preferredVideoModel } from './preferences';

export function videoModelSource(generatorID: string) {
  const sources: Record<string, string> = {
    'video.h3.shotforward': 'ShotForward Cloud',
    'video.seedance-2.5.volcengine-cn': 'Volcengine China',
    'video.seedance-2.5.byteplus-global': 'BytePlus Global',
    'video.h3.minimax-cn': 'MiniMax China',
    'video.h3.official': 'MiniMax Global',
    'video.wan-3.0.official': 'Alibaba Cloud',
  };
  return sources[generatorID] ?? generatorID;
}

function videoModelDisplayName(generatorID: string, modelID: string, displayName = '') {
  if (displayName) return displayName;
  if (generatorID === 'video.h3.shotforward') return 'MiniMax-H3';
  if (modelID.includes('seedance-2-5')) return 'Seedance 2.5';
  if (modelID.includes('seedance-2-0-fast')) return 'Seedance 2.0 Fast';
  if (modelID.includes('seedance-2-0-mini')) return 'Seedance 2.0 Mini';
  if (modelID.includes('seedance-2-0')) return 'Seedance 2.0';
  if (modelID === 'wan3.0-video-prime') return 'Wan 3.0 Prime';
  if (modelID === 'wan3.0-video') return 'Wan 3.0';
  return modelID;
}

export function videoModelChoices(providers: ProviderProfile[], mode: string) {
  return providers
    .filter((provider) => provider.generator_id !== 'video.h3.shotforward' || isProviderAvailable(provider))
    .flatMap((provider) => {
      const available = availableProviderModels(provider);
      const models: ProviderModel[] = available.length
        ? available
        : [
            {
              model_id: provider.model_id,
              is_default: true,
              capability_version: provider.capability_version,
              health: provider.health,
              failure_code: provider.failure_code,
            },
          ];
      return models
        .filter((model) => {
          if (model.deprecated) return false;
          if (model.capabilities) return model.capabilities.generation_modes?.includes(mode) ?? false;
          if (provider.generator_id !== 'video.h3.shotforward') return true;
          return model.model_id === (mode === 'reference' ? 'h3-ref2va' : 'h3-fl2va');
        })
        .map((model) => {
          const modelID = model.model_id || provider.model_id || provider.generator_id;
          const label = videoModelDisplayName(provider.generator_id, modelID, model.display_name);
          const workflow =
            provider.generator_id === 'video.h3.shotforward'
              ? model.capabilities?.workflow?.toUpperCase() || (modelID === 'h3-ref2va' ? 'Ref2VA' : 'FL2VA')
              : '';
          return {
            value: `${provider.generator_id}\u001f${modelID}`,
            generatorID: provider.generator_id,
            modelID,
            label,
            description: [
              videoModelSource(provider.generator_id),
              workflow || (label === modelID ? '' : modelID),
            ]
              .filter(Boolean)
              .join(' · '),
            sortOrder: model.sort_order ?? 0,
            capabilities: model.capabilities,
          };
        });
    })
    .sort(
      (a, b) =>
        Number(b.generatorID === 'video.h3.shotforward') - Number(a.generatorID === 'video.h3.shotforward') ||
        a.sortOrder - b.sortOrder ||
        a.modelID.localeCompare(b.modelID),
    );
}

export type VideoModelChoice = ReturnType<typeof videoModelChoices>[number];

export function preferredVideoModelChoice(
  options: VideoModelChoice[],
  savedVideoModel = preferredVideoModel(),
) {
  return (
    options.find((item) => item.value === savedVideoModel) ??
    options.find((item) => savedVideoModel && item.generatorID === savedVideoModel.split('\u001f')[0]) ??
    options.find((item) => item.generatorID === 'video.h3.shotforward') ??
    options[0]
  );
}

export function resolveVideoPromptTarget(
  providers: ProviderProfile[],
  savedVideoModel = preferredVideoModel(),
): AgentVideoPromptTarget | undefined {
  const available = providers.filter(
    (provider) => provider.generator_id.startsWith('video.') && isProviderAvailable(provider),
  );
  const referenceOptions = videoModelChoices(available, 'reference');
  const [savedGenerator, savedModel] = savedVideoModel.split('\u001f');
  const reference =
    referenceOptions.find((model) => model.value === savedVideoModel) ??
    referenceOptions.find(
      (model) =>
        model.generatorID === savedGenerator &&
        savedModel &&
        model.modelID.toLowerCase() === savedModel.replace(/fl2va/i, 'ref2va').toLowerCase(),
    ) ??
    preferredVideoModelChoice(referenceOptions, savedVideoModel);
  if (!reference) return undefined;
  const lastFrameChoices = videoModelChoices(available, 'first_last_image');
  const keyframes = videoModelChoices(available, 'first_image').filter((model) =>
    lastFrameChoices.some((choice) => choice.value === model.value),
  );
  const keyframe =
    keyframes.find(
      (model) => model.generatorID === reference.generatorID && model.value === savedVideoModel,
    ) ??
    keyframes.find(
      (model) => model.generatorID === reference.generatorID && model.modelID === reference.modelID,
    ) ??
    keyframes.find(
      (model) =>
        model.generatorID === reference.generatorID &&
        model.modelID.toLowerCase() === reference.modelID.replace(/ref2va/i, 'fl2va').toLowerCase(),
    ) ??
    keyframes.find((model) => model.generatorID === reference.generatorID);
  return {
    generator_id: reference.generatorID,
    provider_model_id: reference.modelID,
    ...(keyframe ? { keyframe_provider_model_id: keyframe.modelID } : {}),
  };
}
