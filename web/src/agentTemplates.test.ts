import { describe, expect, it } from 'vitest';
import {
  appendAgentTemplateGuidance,
  defaultAgentTemplates,
  renderAgentTemplate,
  renderStoryboardVideoModelGuidance,
} from './agentTemplates';

describe('Agent templates', () => {
  it('leaves the user supplement heading last in every built-in template', () => {
    for (const locale of ['zh-CN', 'en'] as const) {
      const heading = locale === 'zh-CN' ? '附加要求：' : 'Additional requirements:';
      for (const template of Object.values(defaultAgentTemplates[locale])) {
        expect(template.endsWith(`\n\n${heading}`)).toBe(true);
      }
      const h3 = defaultAgentTemplates[locale]['prompt.video.adapt.h3'];
      const languageRules = locale === 'zh-CN' ? 'H3 创作语言：' : 'H3 authoring language:';
      expect(h3.indexOf(languageRules)).toBeGreaterThan(0);
      expect(h3.indexOf(languageRules)).toBeLessThan(h3.lastIndexOf(heading));
      expect(h3.split(heading)).toHaveLength(2);
    }
  });

  it('inserts model guidance before the supplement for planning and adding Clips in both languages', () => {
    for (const locale of ['zh-CN', 'en'] as const) {
      const heading = locale === 'zh-CN' ? '附加要求：' : 'Additional requirements:';
      for (const generatorID of [
        'video.h3.shotforward',
        'video.seedance-2.5.volcengine-cn',
        'video.wan-3.0.official',
      ]) {
        const guidance = renderStoryboardVideoModelGuidance(locale, {
          generatorID,
          providerModelID: 'selected-model',
        });
        for (const key of ['storyboard.plan', 'storyboard.add_clip'] as const) {
          const template = defaultAgentTemplates[locale][key];
          const request = appendAgentTemplateGuidance(template, guidance);
          expect(request).toBe(
            `${template.slice(0, template.lastIndexOf(heading)).trimEnd()}\n\n${guidance}\n\n${heading}`,
          );
          expect(request).toContain(`generator_id=${generatorID}`);
          expect(request.endsWith(heading)).toBe(true);
        }
      }
    }
  });

  it('preserves existing custom supplement text and puts placement and model rules before it', () => {
    for (const heading of ['附加要求：', 'Additional requirements:']) {
      const supplement = `${heading}保持 @布布 和 @一二 的对白。\n<d>[Chinese]你好！</d>\n`;
      const template = `自定义任务\n\n${supplement}`;
      expect(appendAgentTemplateGuidance(template, '插入到开场前\n\n当前模型规则')).toBe(
        `自定义任务\n\n插入到开场前\n\n当前模型规则\n\n${supplement}`,
      );
    }
  });

  it('does not mistake inline prose for a supplement heading or invent a heading for custom templates', () => {
    const template = '保留正文里的“附加要求：”和 Additional requirements: 字样。';
    expect(appendAgentTemplateGuidance(template, '模型规则')).toBe(`${template}\n\n模型规则`);
    expect(appendAgentTemplateGuidance('', '模型规则')).toBe('模型规则');
  });

  it('uses the last supplement heading without changing earlier sections', () => {
    const template = '示例\n附加要求：示例文字\n\n任务正文\n\n附加要求：用户要求';
    expect(appendAgentTemplateGuidance(template, '模型规则')).toBe(
      '示例\n附加要求：示例文字\n\n任务正文\n\n模型规则\n\n附加要求：用户要求',
    );
  });

  it('leaves a custom template unchanged when there is no extra guidance', () => {
    const template = '自定义任务\n\n附加要求：我的要求\n';
    expect(appendAgentTemplateGuidance(template, '')).toBe(template);
    expect(appendAgentTemplateGuidance(template, ' \n ')).toBe(template);
  });

  it('guides asset creation and extraction through historical identity reuse in both languages', () => {
    for (const locale of ['zh-CN', 'en'] as const) {
      for (const key of ['script.extract_assets', 'asset.add'] as const) {
        const template = defaultAgentTemplates[locale][key];
        const listIndex = template.indexOf('list_prior_asset_candidates');
        const readIndex = template.indexOf('get_prior_asset_candidate');
        const sourceIndex = template.indexOf('source_asset_id');
        expect(listIndex).toBeGreaterThan(-1);
        expect(readIndex).toBeGreaterThan(listIndex);
        expect(sourceIndex).toBeGreaterThan(readIndex);
      }
    }
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain('不要只因同名而跳过');
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain('身份不确定时先确认');
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain('不修改历史视频资产');
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain('do not skip a requested asset');
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain(
      'ask when identity matching is uncertain',
    );
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain(
      'must not edit historical video assets',
    );
  });

  it('interpolates known variables and leaves unknown variables visible', () => {
    expect(renderAgentTemplate('Shot {{clip_title}} / {{missing}}', { clip_title: 'Arrival' })).toBe(
      'Shot Arrival / {{missing}}',
    );
  });

  it('keeps professional reference-order guidance in both built-in languages', () => {
    expect(defaultAgentTemplates['zh-CN']['prompt.video']).toContain('模型引用编号');
    expect(defaultAgentTemplates.en['prompt.video']).toContain('model reference-number');
    expect(defaultAgentTemplates['zh-CN']['prompt.video.adapt.h3']).toContain('subject_definitions');
    expect(defaultAgentTemplates['zh-CN']['prompt.video.adapt.seedance']).toContain('镜头 N');
    expect(defaultAgentTemplates['zh-CN']['prompt.video.adapt.wan3']).toContain('图1');
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain('纯白背景');
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain('pure white background');
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain(
      '默认不要创建独立的视觉风格资产',
    );
    expect(defaultAgentTemplates['zh-CN']['script.extract_assets']).toContain(
      '融入每个人物、地点、物品各自的视觉提示词',
    );
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain(
      'Do not create a standalone visual-style asset by default',
    );
    expect(defaultAgentTemplates.en['script.extract_assets']).toContain('only when the user explicitly asks');
    expect(defaultAgentTemplates['zh-CN']['prompt.video.adapt.h3']).toContain(
      '六个结构段的正文同样使用用户当前创作语言',
    );
    expect(defaultAgentTemplates.en['prompt.video.adapt.h3']).toContain(
      'write all six structured sections in the user’s current authoring language',
    );
    for (const key of [
      'prompt.video.adapt.h3',
      'prompt.video.adapt.seedance',
      'prompt.video.adapt.wan3',
    ] as const) {
      expect(defaultAgentTemplates['zh-CN'][key]).toContain('reference_artifact_ids');
      expect(defaultAgentTemplates['zh-CN'][key]).toContain('默认保留当前素材绑定');
      expect(defaultAgentTemplates.en[key]).toContain('reference_artifact_ids');
      expect(defaultAgentTemplates.en[key]).toContain('Preserve current media bindings by default');
    }
  });

  it('shares H3 authoring language and explicit literal boundaries across adaptation and all storyboard edits', () => {
    for (const locale of ['zh-CN', 'en'] as const) {
      const guidance = renderStoryboardVideoModelGuidance(locale, {
        generatorID: 'video.h3.shotforward',
        providerModelID: 'h3-ref2va',
        keyframeProviderModelID: 'h3-fl2va',
      });
      for (const text of [guidance, defaultAgentTemplates[locale]['prompt.video.adapt.h3']]) {
        expect(text).toContain('<d>[Language]');
        expect(text).toContain('<lyrics>');
        expect(text).toContain('</lyrics>');
        expect(text).toContain('<on_screen_text>');
        expect(text).toContain('</on_screen_text>');
        expect(text).toContain('[Shot N] At 00:SS.mmm');
        expect(text).toContain('subject_definitions');
        expect(text).toContain('integrated_multimodal_description');
        expect(text).not.toMatch(
          /正文(?:同样使用|使用|用)英文|bodies in English|body in English|sections in English/,
        );
      }
      if (locale === 'zh-CN') {
        expect(guidance).toContain('规划、新增、拆分、合并、优化和适配');
        expect(guidance).toContain('普通引号不代表保护边界');
      } else {
        expect(guidance).toContain('planning, adding, splitting, merging, improving, and adapting');
        expect(guidance).toContain('ordinary quotation marks do not define protected boundaries');
      }
    }
  });

  it('includes the selected model and mode-specific H3 profiles in model guidance', () => {
    const guidance = renderStoryboardVideoModelGuidance('zh-CN', {
      generatorID: 'video.h3.shotforward',
      providerModelID: 'MiniMax-H3-Turbo-Ref2VA',
      keyframeProviderModelID: 'MiniMax-H3-Turbo-FL2VA',
    });
    expect(guidance).toContain('generator_id=video.h3.shotforward');
    expect(guidance).toContain('provider_model_id=MiniMax-H3-Turbo-Ref2VA');
    expect(guidance).toContain('provider_model_id=MiniMax-H3-Turbo-FL2VA');
    expect(guidance).toContain('h3_ref2va@1');
    expect(guidance).toContain('h3_base@1');
    expect(guidance).toContain('@素材名');
    expect(guidance).toContain('不要自行编写 <Picture N>');
  });

  it('keeps video rules scoped and uses family rules without inventing an endpoint model', () => {
    for (const locale of ['zh-CN', 'en'] as const) {
      const seedance = renderStoryboardVideoModelGuidance(locale, {
        generatorID: 'video.seedance-2.5.volcengine-cn',
        providerModelID: 'seedance-2-5-pro',
      });
      const wan = renderStoryboardVideoModelGuidance(locale, {
        generatorID: 'video.wan-3.0.official',
        providerModelID: 'wan3.0-video',
      });
      expect(seedance).toContain('Seedance');
      expect(seedance).not.toContain('subject_definitions');
      expect(wan).toContain('Wan 3.0');
      expect(wan).toContain('BGM');
      expect(wan).not.toContain('integrated_multimodal_description');
      expect(seedance).not.toContain('first_last_image 模式填写 provider_model_id=seedance-2-5-pro');
    }
    const h3 = renderStoryboardVideoModelGuidance('en', {
      generatorID: 'video.h3.shotforward',
      providerModelID: 'h3-ref2va',
    });
    expect(h3).toContain('Do not apply video syntax to asset or first/last-frame image prompts');
    expect(h3).toContain('No compatible endpoint model is configured');
    expect(h3).not.toContain('provider_model_id=h3-ref2va for first_image');
  });
});
