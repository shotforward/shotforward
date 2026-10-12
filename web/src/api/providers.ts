import { requestJSON } from './client';

export type ProviderHealth = 'unchecked' | 'ready' | 'unavailable';

export interface ProviderModelDuration {
  minimum_seconds: number;
  maximum_seconds: number;
  step_seconds: number;
  default_seconds: number;
}

export interface ProviderModelOutput {
  default_size: string;
  sizes: string[];
}

export interface ProviderModelInput {
  role: 'first_image' | 'last_image' | 'reference_image' | 'reference_video' | 'reference_audio';
  generation_modes: string[];
  required: boolean;
  minimum_count: number;
  maximum_count: number;
  maximum_bytes: number;
  byte_limit_source: string;
  minimum_seconds?: number;
  maximum_seconds?: number;
  maximum_total_seconds?: number;
  accepted_mime_types: string[];
  requires_any_of?: string[];
}

export interface ProviderModelCapabilities {
  workflow: string;
  generation_modes: string[];
  duration: ProviderModelDuration;
  output: ProviderModelOutput;
  // Older image catalogs omit these limits. Missing is not the same as an
  // explicit empty list, which declares that no reference input is supported.
  inputs?: ProviderModelInput[];
  minimum_total_inputs: number;
  maximum_total_inputs: number;
  supports_stop: boolean;
  stop_policy: string;
  supports_recovery: boolean;
  recovery_policy: string;
}

export interface ProviderModel {
  model_id: string;
  display_name?: string;
  availability?: string;
  family?: string;
  variant?: string;
  sort_order?: number;
  deprecated?: boolean;
  replacement_model_id?: string;
  is_default: boolean;
  capability_version: string;
  capabilities?: ProviderModelCapabilities;
  health: ProviderHealth;
  failure_code: string;
  last_checked_at?: string;
}

export interface ProviderProfile {
  generator_id: string;
  endpoint_preset: string;
  model_id: string;
  resource_id: string;
  workspace_id: string;
  enabled: boolean;
  concurrency_limit: number;
  credential_configured: boolean;
  capability_version: string;
  health: ProviderHealth;
  failure_code: string;
  last_checked_at?: string;
  revision: number;
  created_at: string;
  updated_at: string;
  models?: ProviderModel[];
}

export function isProviderAvailable(provider: ProviderProfile): boolean {
  if (!provider.enabled || provider.health !== 'ready') return false;
  return !provider.models?.length || provider.models.some((model) => model.health === 'ready');
}

export function availableProviderModels(provider: ProviderProfile): ProviderModel[] {
  return provider.models?.filter((model) => model.health === 'ready') ?? [];
}

export function providerReferenceInputsReady(
  capabilities: ProviderModelCapabilities | undefined,
  counts: Record<string, number>,
): boolean {
  if (!capabilities) return Object.values(counts).some((count) => count > 0);
  const roleMap: Record<string, string> = {
    reference_image: 'image_reference',
    reference_video: 'video_reference',
    reference_audio: 'audio_reference',
  };
  const total = Object.values(counts).reduce((sum, count) => sum + count, 0);
  if (
    total < capabilities.minimum_total_inputs ||
    (capabilities.maximum_total_inputs > 0 && total > capabilities.maximum_total_inputs)
  )
    return false;
  if (!capabilities.inputs) return total > 0;
  return capabilities.inputs
    .filter((input) => input.generation_modes?.includes('reference'))
    .every((input) => {
      const role = roleMap[input.role] ?? input.role;
      const count = counts[role] ?? 0;
      if (count < input.minimum_count || count > input.maximum_count) return false;
      if (!count || !input.requires_any_of?.length) return true;
      return input.requires_any_of.some((required) => (counts[roleMap[required] ?? required] ?? 0) > 0);
    });
}

export interface ConfigureProviderInput {
  endpoint_preset: string;
  model_id: string;
  models: Array<{ model_id: string; is_default: boolean }>;
  resource_id: string;
  workspace_id: string;
  api_key: string;
  enabled: boolean;
  concurrency_limit: number;
  expected_revision: number;
}

function normalizeProvider(provider: ProviderProfile): ProviderProfile {
  // Go's nil slices can arrive as JSON null. Normalize the catalog once at the
  // API boundary; retain unknown input limits so legacy image catalogs still
  // use the backend's supported reference limit rather than disabling images.
  return {
    ...provider,
    models: (provider.models ?? []).map((model) => {
      const { capabilities, ...fields } = model;
      if (!capabilities) return fields;
      const { inputs, ...capabilityFields } = capabilities;
      return {
        ...fields,
        capabilities: {
          ...capabilityFields,
          generation_modes: capabilities.generation_modes ?? [],
          output: {
            ...capabilities.output,
            default_size: capabilities.output?.default_size ?? '',
            sizes: capabilities.output?.sizes ?? [],
          },
          ...(inputs
            ? {
                inputs: inputs.map((input) => ({
                  ...input,
                  generation_modes: input.generation_modes ?? [],
                  accepted_mime_types: input.accepted_mime_types ?? [],
                  requires_any_of: input.requires_any_of ?? [],
                })),
              }
            : {}),
        },
      };
    }),
  };
}

async function requestProvider(path: string, init: RequestInit): Promise<{ provider: ProviderProfile }> {
  const result = await requestJSON<{ provider: ProviderProfile }>(path, init);
  return { ...result, provider: normalizeProvider(result.provider) };
}

export async function listProviders(): Promise<{ providers: ProviderProfile[] }> {
  const result = await requestJSON<{ providers: ProviderProfile[] }>('/api/v1/generation-providers');
  return { ...result, providers: (result.providers ?? []).map(normalizeProvider) };
}

export function configureProvider(
  generatorID: string,
  input: ConfigureProviderInput,
): Promise<{ provider: ProviderProfile }> {
  return requestProvider(`/api/v1/generation-providers/${encodeURIComponent(generatorID)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  });
}

export function validateAndConfigureProvider(
  generatorID: string,
  input: ConfigureProviderInput,
): Promise<{ provider: ProviderProfile }> {
  return requestProvider(
    `/api/v1/generation-providers/${encodeURIComponent(generatorID)}:validate-and-configure`,
    {
      method: 'POST',
      body: JSON.stringify(input),
    },
  );
}

export function checkProvider(generatorID: string): Promise<{ provider: ProviderProfile }> {
  return requestProvider(`/api/v1/generation-providers/${encodeURIComponent(generatorID)}:check`, {
    method: 'POST',
    body: '{}',
  });
}
