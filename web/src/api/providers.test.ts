import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  checkProvider,
  configureProvider,
  listProviders,
  providerReferenceInputsReady,
  validateAndConfigureProvider,
  type ConfigureProviderInput,
  type ProviderModelCapabilities,
} from './providers';
import { imageReferenceMaximum } from '../assetImageReferences';

function respond(value: unknown) {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } }),
        ),
      ),
  );
}

const profile = {
  generator_id: 'image.shotforward',
  model_id: 'cloud-image',
  enabled: true,
  health: 'ready',
  models: [
    {
      model_id: 'cloud-image',
      health: 'ready',
      is_default: true,
      capabilities: { inputs: null, generation_modes: null, output: { sizes: null } },
    },
  ],
};

afterEach(() => vi.unstubAllGlobals());

describe('provider catalog response normalization', () => {
  it('keeps absent input limits unknown while normalizing other null collection fields', async () => {
    respond({ providers: [profile] });
    const { providers } = await listProviders();
    const capabilities = providers[0]!.models![0]!.capabilities!;
    expect(capabilities.inputs).toBeUndefined();
    expect(capabilities.generation_modes).toEqual([]);
    expect(capabilities.output.sizes).toEqual([]);
    expect(imageReferenceMaximum(providers[0])).toBe(5);

    respond({ providers: [{ ...profile, models: null }] });
    expect((await listProviders()).providers[0]!.models).toEqual([]);
    respond({ providers: null });
    expect((await listProviders()).providers).toEqual([]);
  });

  it('preserves an explicit no-reference capability rather than treating it as unknown', async () => {
    respond({
      providers: [{ ...profile, models: [{ ...profile.models[0], capabilities: { inputs: [] } }] }],
    });
    expect(imageReferenceMaximum((await listProviders()).providers[0])).toBe(0);
  });

  it('normalizes nested input arrays without changing role/count restrictions', async () => {
    respond({
      providers: [
        {
          ...profile,
          models: [
            {
              ...profile.models[0],
              capabilities: {
                generation_modes: ['reference'],
                inputs: [
                  {
                    role: 'reference_image',
                    generation_modes: null,
                    accepted_mime_types: null,
                    requires_any_of: null,
                    maximum_count: 2,
                  },
                ],
                maximum_total_inputs: 1,
              },
            },
          ],
        },
      ],
    });
    const provider = (await listProviders()).providers[0]!;
    expect(provider.models![0]!.capabilities!.inputs![0]).toMatchObject({
      role: 'reference_image',
      maximum_count: 2,
      generation_modes: [],
      accepted_mime_types: [],
      requires_any_of: [],
    });
    expect(imageReferenceMaximum(provider)).toBe(1);
  });

  it.each(['configure', 'validate-and-configure', 'check'])(
    'normalizes the %s response using the same catalog boundary',
    async (operation) => {
      respond({ provider: profile });
      const input = {} as ConfigureProviderInput;
      const result = await (operation === 'configure'
        ? configureProvider('image.shotforward', input)
        : operation === 'validate-and-configure'
          ? validateAndConfigureProvider('image.shotforward', input)
          : checkProvider('image.shotforward'));
      expect(imageReferenceMaximum(result.provider)).toBe(5);
      expect(result.provider.models![0]!.capabilities!.output.sizes).toEqual([]);
    },
  );
});

describe('reference readiness for legacy catalogs', () => {
  it('does not crash on missing/null input arrays or input generation_modes', () => {
    expect(providerReferenceInputsReady({ inputs: null } as unknown as ProviderModelCapabilities, {})).toBe(
      false,
    );
    expect(
      providerReferenceInputsReady(
        {
          inputs: null,
          minimum_total_inputs: 1,
          maximum_total_inputs: 2,
        } as unknown as ProviderModelCapabilities,
        { image_reference: 1 },
      ),
    ).toBe(true);
    expect(
      providerReferenceInputsReady(
        {
          inputs: null,
          minimum_total_inputs: 1,
          maximum_total_inputs: 2,
        } as unknown as ProviderModelCapabilities,
        {},
      ),
    ).toBe(false);
    expect(
      providerReferenceInputsReady(
        {
          inputs: [{ role: 'reference_image', generation_modes: null }],
        } as unknown as ProviderModelCapabilities,
        { image_reference: 1 },
      ),
    ).toBe(true);
  });

  it('still enforces published role, total count and companion input requirements', () => {
    const capabilities = {
      minimum_total_inputs: 1,
      maximum_total_inputs: 3,
      inputs: [
        {
          role: 'reference_image',
          generation_modes: ['reference'],
          minimum_count: 1,
          maximum_count: 2,
        },
        {
          role: 'reference_audio',
          generation_modes: ['reference'],
          minimum_count: 0,
          maximum_count: 1,
          requires_any_of: ['reference_video'],
        },
      ],
    } as ProviderModelCapabilities;
    expect(providerReferenceInputsReady(capabilities, { image_reference: 1 })).toBe(true);
    expect(providerReferenceInputsReady(capabilities, { image_reference: 3 })).toBe(false);
    expect(providerReferenceInputsReady(capabilities, { image_reference: 1, audio_reference: 1 })).toBe(
      false,
    );
    expect(
      providerReferenceInputsReady(capabilities, {
        image_reference: 1,
        audio_reference: 1,
        video_reference: 1,
      }),
    ).toBe(true);
  });
});
