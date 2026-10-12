import { describe, expect, it } from 'vitest';
import type { ProviderProfile } from './api/providers';
import {
  imageReferenceMaximum,
  uniqueImageReferences,
  type AssetReferenceSelection,
} from './assetImageReferences';

function provider(roleMaximum: number, totalMaximum = 0): ProviderProfile {
  return {
    model_id: 'default-image',
    models: [
      {
        model_id: 'default-image',
        is_default: true,
        capabilities: {
          inputs: [{ role: 'reference_image', maximum_count: roleMaximum }],
          maximum_total_inputs: totalMaximum,
        },
      },
    ],
  } as ProviderProfile;
}

describe('asset image references', () => {
  it('uses the current backend maximum when catalog capabilities are unavailable', () => {
    expect(imageReferenceMaximum(undefined)).toBe(5);
    expect(imageReferenceMaximum({ model_id: 'codex', models: [] } as unknown as ProviderProfile)).toBe(5);
  });

  it('treats legacy null or missing inputs as unpublished limits, not unsupported references', () => {
    const nullInputs = provider(5);
    nullInputs.models![0]!.capabilities!.inputs = null as never;
    expect(imageReferenceMaximum(nullInputs)).toBe(5);
    nullInputs.models![0]!.capabilities!.maximum_total_inputs = 2;
    expect(imageReferenceMaximum(nullInputs)).toBe(2);
    const missingInputs = provider(5);
    delete missingInputs.models![0]!.capabilities!.inputs;
    expect(imageReferenceMaximum(missingInputs)).toBe(5);
  });

  it('respects narrower role and total limits without increasing the backend maximum', () => {
    expect(imageReferenceMaximum(provider(3, 2))).toBe(2);
    expect(imageReferenceMaximum(provider(2, 4))).toBe(2);
    expect(imageReferenceMaximum(provider(9, 9))).toBe(5);
    expect(imageReferenceMaximum(provider(0))).toBe(0);
    const noReference = provider(5);
    noReference.models![0]!.capabilities!.inputs = [];
    expect(imageReferenceMaximum(noReference)).toBe(0);
  });

  it('uses the selected model capability instead of a different default model', () => {
    const value = provider(5);
    value.models!.push({
      ...value.models![0]!,
      model_id: 'other-image',
      is_default: false,
      capabilities: { ...value.models![0]!.capabilities!, maximum_total_inputs: 1 },
    });
    expect(imageReferenceMaximum(value, 'other-image')).toBe(1);
  });

  it('deduplicates artifact aliases while retaining first-selection order and metadata', () => {
    const a = {
      key: 'version:a',
      artifact: { artifact_id: 'a' },
      name: 'Version A',
    } as AssetReferenceSelection;
    const b = { key: 'asset:b', artifact: { artifact_id: 'b' } } as AssetReferenceSelection;
    expect(uniqueImageReferences([b, a, { ...a, key: 'upload:a', name: 'Alias A' }])).toEqual([b, a]);
  });
});
