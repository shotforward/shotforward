import { describe, expect, it } from 'vitest';
import type { ProviderModel, ProviderProfile } from './api/providers';
import { resolveVideoPromptTarget, videoModelChoices } from './videoPromptTarget';

function provider(generatorID: string, models: Array<[string, string[]]>): ProviderProfile {
  return {
    generator_id: generatorID,
    enabled: true,
    health: 'ready',
    model_id: models[0]?.[0] ?? '',
    models: models.map(
      ([id, modes]) =>
        ({ model_id: id, health: 'ready', capabilities: { generation_modes: modes } }) as ProviderModel,
    ),
  } as ProviderProfile;
}

describe('Agent video prompt targets', () => {
  const h3 = provider('video.h3.shotforward', [
    ['MiniMax-H3-Turbo-Ref2VA', ['reference']],
    ['MiniMax-H3-Ref2VA', ['reference']],
    ['MiniMax-H3-Turbo-FL2VA', ['first_image', 'first_last_image']],
    ['MiniMax-H3-FL2VA', ['first_image', 'first_last_image']],
  ]);

  it('routes the saved H3 variant to compatible reference and endpoint models', () => {
    for (const model of ['MiniMax-H3-Ref2VA', 'MiniMax-H3-FL2VA']) {
      expect(resolveVideoPromptTarget([h3], `video.h3.shotforward\u001f${model}`)).toEqual({
        generator_id: 'video.h3.shotforward',
        provider_model_id: 'MiniMax-H3-Ref2VA',
        keyframe_provider_model_id: 'MiniMax-H3-FL2VA',
      });
    }
  });

  it('uses the selected Seedance or Wan default instead of a ready H3 provider', () => {
    for (const [generator, model] of [
      ['video.seedance-2.5.volcengine-cn', 'seedance-2-5-pro'],
      ['video.wan-3.0.official', 'wan3.0-video'],
    ]) {
      const selected = provider(generator!, [[model!, ['reference', 'first_image', 'first_last_image']]]);
      expect(resolveVideoPromptTarget([h3, selected], `${generator}\u001f${model}`)).toEqual({
        generator_id: generator,
        provider_model_id: model,
        keyframe_provider_model_id: model,
      });
    }
  });

  it('does not invent an endpoint target from Ref2VA or another provider', () => {
    const referenceOnly = provider('video.h3.shotforward', [['h3-ref2va', ['reference']]]);
    const endpoints = provider('video.wan-3.0.official', [
      ['wan3.0-video', ['first_image', 'first_last_image']],
    ]);
    expect(
      resolveVideoPromptTarget([referenceOnly, endpoints], 'video.h3.shotforward\u001fh3-ref2va'),
    ).toEqual({
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'h3-ref2va',
    });
    expect(resolveVideoPromptTarget([], '')).toBeUndefined();
  });

  it('does not advertise a first-frame-only model for both endpoint modes', () => {
    const firstFrameOnly = provider('video.h3.shotforward', [
      ['h3-ref2va', ['reference']],
      ['h3-fl2va', ['first_image']],
    ]);
    expect(resolveVideoPromptTarget([firstFrameOnly], 'video.h3.shotforward\u001fh3-ref2va')).toEqual({
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'h3-ref2va',
    });
  });

  it('does not crash or advertise undeclared generation modes from a null catalog array', () => {
    const value = provider('video.h3.shotforward', [['h3-ref2va', ['reference']]]);
    value.models![0]!.capabilities!.generation_modes = null as never;
    expect(videoModelChoices([value], 'reference')).toEqual([]);
    expect(resolveVideoPromptTarget([value], '')).toBeUndefined();
  });
});
