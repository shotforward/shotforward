import { describe, expect, it } from 'vitest';
import type { Artifact, EpisodeAsset, MediaSlotDetails } from '../api/assets';
import { updateVideoPromptFamily, type Clip } from '../api/storyboard';
import {
  adaptVideoPrompt,
  assetMentionAt,
  buildGenerationTarget,
  completeAssetMention,
  generatorDurationIssue,
  MissingAssetReferenceMediaError,
  videoModelChoices,
  referenceRoleForFile,
  uploadedReferenceFromArtifact,
  splitPromptAssetMentions,
  type AssetReferenceMedia,
} from './StoryboardWorkspacePage';

function media(slotID: string, artifactID: string): MediaSlotDetails {
  return {
    slot: { media_slot_id: slotID, current_media_version_id: `version_${slotID}`, revision: 2 },
    versions: [{ media_version_id: `version_${slotID}`, artifact_id: artifactID }],
    artifacts: [{ artifact_id: artifactID }],
  } as MediaSlotDetails;
}

function asset(assetID: string, name: string): EpisodeAsset {
  return {
    asset_id: assetID,
    identity: { name },
    prompt: `${name}的视觉设定`,
    primary_image_slot_id: `slot_${assetID}`,
  } as EpisodeAsset;
}

const baseClip = {
  first_image_slot_id: 'slot_first',
  last_image_slot_id: 'slot_last',
  video_slot_id: 'slot_video',
  reference_audio_slot_id: 'slot_reference_audio',
  first_image_prompt: '首帧',
  last_image_prompt: '尾帧',
  video_prompt: '以@林黛玉为主体，她缓缓抬头，场景位于@荣国府。',
  video_prompts: {
    first_image: '首帧模式下人物缓缓抬头。',
    first_last_image: '人物从首帧移动到尾帧。',
    reference: '以@林黛玉为主体，她缓缓抬头，场景位于@荣国府。',
  },
  planned_duration_ms: 5_000,
  generation_mode: 'reference',
} as Clip;

const assetMedia: AssetReferenceMedia[] = [
  { asset: asset('asset_1', '林黛玉'), media: media('slot_asset_1', 'artifact_asset_1') },
  { asset: asset('asset_2', '荣国府'), media: media('slot_asset_2', 'artifact_asset_2') },
];

describe('storyboard generation inputs', () => {
  it('uses uploaded Episode selections without truncating videos and keeps omitted ranges daemon-resolvable', () => {
    const original = {
      artifact_id: 'uploaded_video',
      kind: 'video',
      duration_ms: 60000,
      original_filename: 'original.mp4',
    } as Artifact;
    expect(uploadedReferenceFromArtifact(original, '长视频').reference).toMatchObject({
      trim_start_ms: 0,
      trim_end_ms: 0,
    });
    const selected = uploadedReferenceFromArtifact(original, '长视频', {
      trim_start_ms: 22000,
      trim_end_ms: 42000,
    });
    expect(selected.reference).toMatchObject({ trim_start_ms: 22000, trim_end_ms: 42000, name: '长视频' });
    const target = buildGenerationTarget(
      'video',
      { ...baseClip, video_prompts: { ...baseClip.video_prompts, reference: '参考@长视频' } },
      'video.seedance-2.5.volcengine-cn',
      undefined,
      undefined,
      media('slot_video', 'current_video'),
      [],
      [selected.reference],
    );
    expect(target.references).toEqual([
      { role: 'video_reference', artifact_id: 'uploaded_video', trim_start_ms: 22000, trim_end_ms: 42000 },
    ]);
    expect(original.duration_ms).toBe(60000);
  });
  it('selects the current model family and compiles H3 @assets in first-mention order', () => {
    const h3Prompt =
      'subject_definitions:\n<Subject 1>: @荣国府\nsummary:\nTest\nretention_analysis:\n@荣国府: environment\ndetailed_description:\n@林黛玉 enters @荣国府.\noverall_soundscape:\nNone\nnon_diegetic_music:\nNone';
    let stored = updateVideoPromptFamily('', 'h3', h3Prompt, {
      profile: 'h3_ref2va@1',
      generator_id: 'video.h3.shotforward',
      provider_model_id: 'h3-ref2va',
      asset_ids: ['asset_2', 'asset_1'],
      references: [
        { key: 'asset:asset_2', purposes: ['scene'] },
        { key: 'asset:asset_1', purposes: ['identity'] },
        { key: 'upload:artifact_upload', purposes: ['composition'] },
      ],
    });
    stored = updateVideoPromptFamily(stored, 'wan3', '@林黛玉走进@荣国府。', {
      profile: 'wan3@1',
      generator_id: 'video.wan-3.0.official',
      provider_model_id: 'wan3.0-video-prime',
      asset_ids: ['asset_1', 'asset_2'],
    });
    const clip = { ...baseClip, video_prompts: { ...baseClip.video_prompts, reference: stored } };

    const h3 = buildGenerationTarget(
      'video',
      clip,
      'video.h3.shotforward',
      undefined,
      undefined,
      media('slot_video', 'artifact_video'),
      assetMedia,
      [],
      'h3-ref2va',
    );
    expect(h3.prompt).toContain('<Subject 1>: <Picture 1>');
    expect(h3.prompt).toContain('<Picture 2> enters <Picture 1>');
    expect(h3.prompt).not.toContain('@荣国府');
    expect(h3.references?.map((reference) => reference.artifact_id)).toEqual([
      'artifact_asset_2',
      'artifact_asset_1',
    ]);

    const h3WithUpload = buildGenerationTarget(
      'video',
      clip,
      'video.h3.shotforward',
      undefined,
      undefined,
      media('slot_video', 'artifact_video'),
      assetMedia,
      [{ role: 'image_reference', artifact_id: 'artifact_upload', name: '上传图' }],
      'h3-ref2va',
    );
    expect(h3WithUpload.references?.map((reference) => reference.artifact_id)).toEqual([
      'artifact_asset_2',
      'artifact_asset_1',
      'artifact_upload',
    ]);

    const wan = buildGenerationTarget(
      'video',
      clip,
      'video.wan-3.0.official',
      undefined,
      undefined,
      media('slot_video', 'artifact_video'),
      assetMedia,
      [],
      'wan3.0-video-prime',
    );
    expect(wan.prompt).toContain('图1走进图2');
  });

  it('uses the selected video model duration boundary', () => {
    expect(generatorDurationIssue(5, 'video.h3.shotforward', 'h3-ref2va')).toBe(false);
    for (const model of ['h3-ref2va', 'h3-fl2va']) {
      for (let seconds = 4; seconds <= 15; seconds++) {
        expect(generatorDurationIssue(seconds, 'video.h3.shotforward', model)).toBe(false);
      }
      for (const seconds of [3, 16, 7.5]) {
        expect(generatorDurationIssue(seconds, 'video.h3.shotforward', model)).toBe(true);
      }
    }
    expect(generatorDurationIssue(16, 'video.h3.official')).toBe(true);
    expect(generatorDurationIssue(15, 'video.h3.official')).toBe(false);
    expect(generatorDurationIssue(4, 'video.h3.minimax-cn', 'MiniMax-H3-Max')).toBe(true);
    expect(generatorDurationIssue(5, 'video.h3.minimax-cn', 'MiniMax-H3-Max')).toBe(false);
    expect(generatorDurationIssue(30, 'video.wan-3.0.official')).toBe(false);
    expect(generatorDurationIssue(30, 'video.seedance-2.5.byteplus-global')).toBe(false);
    const capabilities = {
      duration: { minimum_seconds: 5, maximum_seconds: 15, step_seconds: 2, default_seconds: 5 },
    } as Parameters<typeof generatorDurationIssue>[3];
    expect(generatorDurationIssue(7, 'video.h3.shotforward', 'dynamic', capabilities)).toBe(false);
    expect(generatorDurationIssue(6, 'video.h3.shotforward', 'dynamic', capabilities)).toBe(true);
  });

  it('filters and sorts ShotForward models from published capabilities', () => {
    const capability = (workflow: string, generationModes: string[]) => ({
      workflow,
      generation_modes: generationModes,
      duration: { minimum_seconds: 4, maximum_seconds: 15, step_seconds: 1, default_seconds: 5 },
      output: { default_size: '720x1280', sizes: ['720x1280', '1280x720', '1024x1024'] },
      inputs: [],
      minimum_total_inputs: 1,
      maximum_total_inputs: 12,
      supports_stop: true,
      stop_policy: 'queued_only',
      supports_recovery: true,
      recovery_policy: 'query_existing_task',
    });
    const providers = [
      {
        generator_id: 'video.h3.shotforward',
        model_id: 'MiniMax-H3-Turbo-FL2VA',
        enabled: true,
        health: 'ready',
        models: [
          {
            model_id: 'MiniMax-H3-Ref2VA',
            display_name: 'MiniMax-H3 · 参考生视频',
            sort_order: 20,
            health: 'ready',
            capabilities: capability('ref2va', ['reference']),
          },
          {
            model_id: 'MiniMax-H3-Turbo-Ref2VA',
            display_name: 'MiniMax-H3-Turbo · 参考生视频',
            sort_order: 10,
            health: 'ready',
            capabilities: capability('ref2va', ['reference']),
          },
          {
            model_id: 'MiniMax-H3-Turbo-FL2VA',
            display_name: 'MiniMax-H3-Turbo · 首尾帧生视频',
            sort_order: 5,
            health: 'ready',
            capabilities: capability('fl2va', ['first_image', 'first_last_image']),
          },
        ],
      },
    ] as unknown as Parameters<typeof videoModelChoices>[0];
    expect(videoModelChoices(providers, 'reference').map((option) => option.modelID)).toEqual([
      'MiniMax-H3-Turbo-Ref2VA',
      'MiniMax-H3-Ref2VA',
    ]);
    expect(videoModelChoices(providers, 'first_last_image')[0]).toMatchObject({
      modelID: 'MiniMax-H3-Turbo-FL2VA',
      label: 'MiniMax-H3-Turbo · 首尾帧生视频',
    });
  });

  it('prioritizes signed-in ShotForward and routes each video mode without exposing the mock', () => {
    const providers = [
      { generator_id: 'video.h3.official', model_id: 'MiniMax-H3', enabled: true, health: 'ready' },
      {
        generator_id: 'video.h3.shotforward',
        model_id: 'h3-fl2va',
        enabled: true,
        health: 'ready',
        models: [
          { model_id: 'h3-video', health: 'ready' },
          { model_id: 'h3-fl2va', health: 'ready' },
          { model_id: 'h3-ref2va', health: 'ready' },
        ],
      },
    ] as Parameters<typeof videoModelChoices>[0];
    expect(videoModelChoices(providers, 'reference').map((option) => option.modelID)).toEqual([
      'h3-ref2va',
      'MiniMax-H3',
    ]);
    expect(videoModelChoices(providers, 'first_image')[0]).toMatchObject({
      modelID: 'h3-fl2va',
      label: 'MiniMax-H3',
    });
    expect(videoModelChoices(providers, 'first_last_image')[0]?.modelID).toBe('h3-fl2va');
    providers[1]!.health = 'unavailable';
    expect(videoModelChoices(providers, 'reference').map((option) => option.modelID)).toEqual(['MiniMax-H3']);
  });

  it('matches an asset mention prefix and completes it without losing surrounding text', () => {
    expect(assetMentionAt('人物参考@林黛', 7)).toEqual({ start: 4, end: 7, query: '林黛' });
    expect(completeAssetMention('人物参考@林黛', 4, 7, '林黛玉')).toBe('人物参考@林黛玉 ');
    expect(assetMentionAt('人物参考@林黛玉 走入房间', 15)).toBeNull();
  });

  it('splits bound Asset mentions into clickable prompt-preview parts', () => {
    expect(
      splitPromptAssetMentions('以@林黛玉为主体，走入@荣国府。', [
        { asset_id: 'asset_1', name: '林黛玉' },
        { asset_id: 'asset_2', name: '荣国府' },
      ]),
    ).toEqual([
      { text: '以' },
      { text: '@林黛玉', assetID: 'asset_1' },
      { text: '为主体，走入' },
      { text: '@荣国府', assetID: 'asset_2' },
      { text: '。' },
    ]);
  });

  it('recognizes bound H3 Asset names without changing the structured prompt text', () => {
    expect(
      splitPromptAssetMentions('<Subject 1>: Tang Sanzang follows @White Tiger Ridge.', [
        { asset_id: 'asset_tang', name: 'Tang Sanzang', plain: true },
        { asset_id: 'asset_ridge', name: 'White Tiger Ridge' },
      ]),
    ).toEqual([
      { text: '<Subject 1>: ' },
      { text: 'Tang Sanzang', assetID: 'asset_tang', implicitAssetMention: true },
      { text: ' follows ' },
      { text: '@White Tiger Ridge', assetID: 'asset_ridge' },
      { text: '.' },
    ]);
    expect(
      splitPromptAssetMentions('A Tangerine robe.', [{ asset_id: 'asset_tang', name: 'Tang', plain: true }]),
    ).toEqual([{ text: 'A Tangerine robe.' }]);
  });

  it('maps bound asset images to reference inputs instead of first-frame inputs', () => {
    const target = buildGenerationTarget(
      'video',
      baseClip,
      'video.wan-3.0.official',
      media('slot_first', 'artifact_first'),
      media('slot_last', 'artifact_last'),
      media('slot_video', 'artifact_video'),
      assetMedia,
    );

    expect(target.references).toEqual([
      { role: 'image_reference', artifact_id: 'artifact_asset_1' },
      { role: 'image_reference', artifact_id: 'artifact_asset_2' },
    ]);
    expect(target.prompt).toContain('图1 是“林黛玉”');
    expect(target.prompt).toContain('图1为主体');
  });

  it('numbers ready asset references by their first mention in the video prompt', () => {
    const target = buildGenerationTarget(
      'video',
      {
        ...baseClip,
        video_prompts: {
          ...baseClip.video_prompts,
          reference: '@荣国府内，@林黛玉缓缓抬头，再看向@荣国府深处。',
        },
      },
      'video.wan-3.0.official',
      undefined,
      undefined,
      undefined,
      assetMedia,
    );

    expect(target.references).toEqual([
      { role: 'image_reference', artifact_id: 'artifact_asset_2' },
      { role: 'image_reference', artifact_id: 'artifact_asset_1' },
    ]);
    expect(target.prompt).toContain('图1 是“荣国府”');
    expect(target.prompt).toContain('图2 是“林黛玉”');
    expect(target.prompt).toContain('图1内，图2缓缓抬头，再看向图1深处');
  });

  it('includes user-uploaded image, audio, and video references in the current generation', () => {
    const uploaded = [
      { role: 'image_reference', artifact_id: 'artifact_upload_image' },
      {
        role: 'audio_reference',
        artifact_id: 'artifact_upload_audio',
        trim_start_ms: 0,
        trim_end_ms: 8_000,
      },
      {
        role: 'video_reference',
        artifact_id: 'artifact_upload_video',
        trim_start_ms: 0,
        trim_end_ms: 6_000,
      },
    ] as const;
    const target = buildGenerationTarget(
      'video',
      baseClip,
      'video.seedance-2.5.byteplus-global',
      media('slot_first', 'artifact_first'),
      media('slot_last', 'artifact_last'),
      media('slot_video', 'artifact_video'),
      assetMedia,
      [...uploaded],
    );

    expect(target.references).toEqual([
      { role: 'image_reference', artifact_id: 'artifact_asset_1' },
      { role: 'image_reference', artifact_id: 'artifact_asset_2' },
      ...uploaded,
    ]);
  });

  it('continues from the previous video and extracts only its final 15 seconds when needed', () => {
    const target = buildGenerationTarget(
      'video',
      { ...baseClip, continue_from_previous: true },
      'video.h3.official',
      undefined,
      undefined,
      media('slot_video', 'artifact_video'),
      assetMedia,
      [],
      'MiniMax-Hailuo-3',
      {
        clip: { clip_id: 'clip_previous', position: 1, title: '上一镜' },
        artifact: {
          artifact_id: 'artifact_previous_video',
          kind: 'video',
          duration_ms: 22_000,
        } as Artifact,
      },
    );

    expect(target.references?.[0]).toEqual({
      role: 'video_reference',
      artifact_id: 'artifact_previous_video',
      trim_start_ms: 7_000,
      trim_end_ms: 22_000,
    });
    expect(target.prompt).toContain('[video continuation]');
    expect(target.prompt).toContain('end of <Video 1>');
  });

  it('uses Seedance continuation syntax without rewriting the stored prompt', () => {
    const original = '@林黛玉继续向前走。';
    const compiled = adaptVideoPrompt(
      original,
      'reference',
      'video.seedance-2.5.byteplus-global',
      assetMedia.slice(0, 1).map(({ asset, media }) => ({ asset, artifact: media!.artifacts[0]! })),
      assetMedia.slice(0, 1),
      [],
      true,
    );

    expect(compiled).toContain('Continue forward from the ending of @Video 1');
    expect(compiled).toContain('@Image 1继续向前走');
    expect(original).toBe('@林黛玉继续向前走。');
  });

  it('uses localized Seedance labels for the China endpoint', () => {
    const compiled = adaptVideoPrompt(
      '@林黛玉继续向前走。',
      'reference',
      'video.seedance-2.5.volcengine-cn',
      assetMedia.slice(0, 1).map(({ asset, media }) => ({ asset, artifact: media!.artifacts[0]! })),
      assetMedia.slice(0, 1),
      [],
      true,
    );

    expect(compiled).toContain('生成 @视频1 之后的内容');
    expect(compiled).toContain('@图片1继续向前走');
  });

  it('recognizes supported reference uploads by MIME type or extension', () => {
    expect(referenceRoleForFile({ name: 'portrait.png', type: 'image/png' })).toBe('image_reference');
    expect(referenceRoleForFile({ name: 'voice.wav', type: '' })).toBe('audio_reference');
    expect(referenceRoleForFile({ name: 'motion.mov', type: 'video/quicktime' })).toBe('video_reference');
    expect(() => referenceRoleForFile({ name: 'notes.txt', type: 'text/plain' })).toThrow();
  });

  it('sends the Chinese prompt directly to MiniMax H3 with converted reference labels', () => {
    const prompt = adaptVideoPrompt(
      '@林黛玉缓慢走入@荣国府。',
      'reference',
      'video.h3.official',
      assetMedia.map(({ asset, media }) => ({ asset, artifact: media!.artifacts[0]! })),
    );

    expect(prompt).toBe('<Picture 1>缓慢走入<Picture 2>。');
    expect(prompt).not.toContain('subject_definitions');
    expect(prompt).not.toContain('detailed_description');
  });

  it('numbers uploaded references before generated asset images in provider prompts', () => {
    const references = assetMedia.map(({ asset, media }) => ({ asset, artifact: media!.artifacts[0]! }));
    const uploaded = [
      { role: 'image_reference' as const, artifact_id: 'artifact_upload_image', name: '构图参考' },
      {
        role: 'video_reference' as const,
        artifact_id: 'artifact_upload_video',
        trim_start_ms: 0,
        trim_end_ms: 5_000,
        name: '动作参考',
      },
    ];
    const prompt = adaptVideoPrompt(
      '@构图参考中@林黛玉参考@动作参考走入@荣国府。',
      'reference',
      'video.h3.official',
      references,
      assetMedia,
      uploaded,
    );

    expect(prompt).toContain('<Picture 1>中<Picture 2>参考<Video 1>走入<Picture 3>');
    expect(prompt).not.toContain('参考素材对应关系');
  });

  it('compiles uploaded audio mentions to each provider native label', () => {
    const uploaded = [{ role: 'audio_reference' as const, artifact_id: 'artifact_voice', name: '布布配音' }];
    const compile = (generatorID: string, prompt = '@布布配音 作为人物对白音色参考。') =>
      adaptVideoPrompt(prompt, 'reference', generatorID, [], [], uploaded);

    expect(compile('video.h3.official')).toContain('<Audio 1>');
    expect(compile('video.seedance-2.5.volcengine-cn')).toContain('@音频1');
    expect(compile('video.seedance-2.5.byteplus-global')).toContain('@Audio 1');
    expect(compile('video.wan-3.0.official')).toContain('音频1');
    expect(
      adaptVideoPrompt(
        '@Bubu voice is the dialogue reference.',
        'reference',
        'video.wan-3.0.official',
        [],
        [],
        [{ role: 'audio_reference', artifact_id: 'artifact_voice', name: 'Bubu voice' }],
      ),
    ).toContain('Audio 1');
  });

  it('orders uploaded and generated image references together by first mention', () => {
    const target = buildGenerationTarget(
      'video',
      {
        ...baseClip,
        video_prompts: {
          ...baseClip.video_prompts,
          reference: '@林黛玉看向@构图参考，随后再次靠近@林黛玉。',
        },
      },
      'video.h3.official',
      undefined,
      undefined,
      undefined,
      [assetMedia[0]!],
      [{ role: 'image_reference', artifact_id: 'artifact_upload_image', name: '构图参考' }],
    );

    expect(target.references).toEqual([
      { role: 'image_reference', artifact_id: 'artifact_asset_1' },
      { role: 'image_reference', artifact_id: 'artifact_upload_image' },
    ]);
    expect(target.prompt).toContain('<Picture 1>看向<Picture 2>，随后再次靠近<Picture 1>');
    expect(target.prompt).not.toContain('参考素材对应关系');
  });

  it('blocks video submission when an explicitly referenced Asset has no image', () => {
    expect(() =>
      buildGenerationTarget('video', baseClip, 'video.h3.official', undefined, undefined, undefined, [
        assetMedia[0]!,
        { asset: assetMedia[1]!.asset, media: undefined },
      ]),
    ).toThrow(MissingAssetReferenceMediaError);
    expect(() =>
      buildGenerationTarget('video', baseClip, 'video.h3.official', undefined, undefined, undefined, [
        assetMedia[0]!,
        { asset: assetMedia[1]!.asset, media: undefined },
      ]),
    ).toThrow('荣国府');
  });

  it('does not require an image for a bound Asset that is not referenced by the prompt', () => {
    const target = buildGenerationTarget(
      'video',
      { ...baseClip, video_prompts: { ...baseClip.video_prompts, reference: '@林黛玉缓缓抬头。' } },
      'video.h3.official',
      undefined,
      undefined,
      undefined,
      [assetMedia[0]!, { asset: assetMedia[1]!.asset, media: undefined }],
    );
    expect(target.prompt).toBe('<Picture 1>缓缓抬头。');
  });

  it('converts named asset mentions to provider-native reference labels', () => {
    const references = assetMedia.map(({ asset, media }) => ({ asset, artifact: media!.artifacts[0]! }));
    const seedance = adaptVideoPrompt(
      '@林黛玉走进@荣国府，保持人物面容。',
      'reference',
      'video.seedance-2.5.byteplus-global',
      references,
    );
    const wan = adaptVideoPrompt(
      '@林黛玉走进@荣国府，保持人物面容。',
      'reference',
      'video.wan-3.0.official',
      references,
    );

    expect(seedance).toContain('@Image 1走进@Image 2');
    expect(wan).toContain('图1走进图2');
  });

  it('uses prepared endpoints only for first/last-frame mode', () => {
    const target = buildGenerationTarget(
      'video',
      { ...baseClip, generation_mode: 'first_last_image' },
      'video.seedance-2.5.byteplus-global',
      media('slot_first', 'artifact_first'),
      media('slot_last', 'artifact_last'),
      media('slot_video', 'artifact_video'),
      assetMedia,
    );

    expect(target.references).toEqual([
      { role: 'first_image', artifact_id: 'artifact_first' },
      { role: 'last_image', artifact_id: 'artifact_last' },
    ]);
    expect(target.prompt).toBe('人物从首帧移动到尾帧。');
  });

  it('uses the prepared first frame as the visual reference when generating a last frame', () => {
    const target = buildGenerationTarget(
      'last_image',
      { ...baseClip, generation_mode: 'first_last_image' },
      'image.codex',
      media('slot_first', 'artifact_first'),
      media('slot_last', 'artifact_last'),
      media('slot_video', 'artifact_video'),
      assetMedia,
    );

    expect(target.references).toEqual([{ role: 'image_reference', artifact_id: 'artifact_first' }]);
    expect(target.media_slot_id).toBe('slot_last');
    expect(target.prompt).toBe('尾帧');
  });

  it('submits prompt-selected reference images when generating a frame', () => {
    const target = buildGenerationTarget(
      'first_image',
      { ...baseClip, first_image_prompt: '@构图参考中，@林黛玉站在窗前。' },
      'image.codex',
      media('slot_first', 'artifact_first'),
      media('slot_last', 'artifact_last'),
      media('slot_video', 'artifact_video'),
      assetMedia,
      [{ role: 'image_reference', artifact_id: 'artifact_upload_image', name: '构图参考' }],
    );

    expect(target.references).toEqual([
      { role: 'image_reference', artifact_id: 'artifact_upload_image' },
      { role: 'image_reference', artifact_id: 'artifact_asset_1' },
    ]);
  });
});
