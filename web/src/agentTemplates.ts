import type { SupportedLanguage } from './i18n';

export const agentTemplateKeys = [
  'script.create',
  'script.extract_assets',
  'asset.add',
  'asset.edit_prompt',
  'storyboard.plan',
  'storyboard.add_clip',
  'prompt.first_frame',
  'prompt.last_frame',
  'prompt.video',
  'prompt.video.adapt.h3',
  'prompt.video.adapt.seedance',
  'prompt.video.adapt.wan3',
] as const;

export type AgentTemplateKey = (typeof agentTemplateKeys)[number];

export interface AgentTemplateDefinition {
  key: AgentTemplateKey;
  group: 'script' | 'assets' | 'storyboard' | 'prompts';
  variables: string[];
}

export const agentTemplateDefinitions: AgentTemplateDefinition[] = [
  { key: 'script.create', group: 'script', variables: [] },
  { key: 'script.extract_assets', group: 'script', variables: [] },
  { key: 'asset.add', group: 'assets', variables: [] },
  { key: 'asset.edit_prompt', group: 'assets', variables: ['name', 'asset_id'] },
  { key: 'storyboard.plan', group: 'storyboard', variables: [] },
  { key: 'storyboard.add_clip', group: 'storyboard', variables: ['scene_title', 'scene_id'] },
  { key: 'prompt.first_frame', group: 'prompts', variables: ['clip_title', 'clip_id'] },
  { key: 'prompt.last_frame', group: 'prompts', variables: ['clip_title', 'clip_id'] },
  {
    key: 'prompt.video',
    group: 'prompts',
    variables: ['clip_title', 'clip_id', 'generation_mode'],
  },
  {
    key: 'prompt.video.adapt.h3',
    group: 'prompts',
    variables: [
      'clip_title',
      'clip_id',
      'generation_mode_id',
      'generator_id',
      'provider_model_id',
      'prompt_profile',
    ],
  },
  {
    key: 'prompt.video.adapt.seedance',
    group: 'prompts',
    variables: [
      'clip_title',
      'clip_id',
      'generation_mode_id',
      'generator_id',
      'provider_model_id',
      'prompt_profile',
    ],
  },
  {
    key: 'prompt.video.adapt.wan3',
    group: 'prompts',
    variables: [
      'clip_title',
      'clip_id',
      'generation_mode_id',
      'generator_id',
      'provider_model_id',
      'prompt_profile',
    ],
  },
];

const assetReuseTemplateGuidance = {
  'zh-CN':
    '资产连续性：读完创作内容和当前视频的全部资产后，用 list_prior_asset_candidates 查找历史候选。同一身份的历史资产先用 get_prior_asset_candidate 读取详情，再通过 source_asset_id 为当前视频创建沿用版本；不要只因同名而跳过，也不要另建同名身份。当前视频已有的资产保持不变；没有匹配时才新建，身份不确定时先确认。沿用版本的后续提示词和图片可独立调整，不修改历史视频资产。每次应用后重新读取当前资产列表。\n\n',
  en: 'Asset continuity: after reading the complete creative content and current asset list, use list_prior_asset_candidates to find historical matches. Read each matching identity with get_prior_asset_candidate, then use source_asset_id to create its current-video variant; do not skip a requested asset merely because its name already exists or create a duplicate identity. Leave assets already present in this video unchanged. Create a new identity only when no match exists, and ask when identity matching is uncertain. Later prompt and image changes to the variant are independent and must not edit historical video assets. Re-read the current asset list after each applied change.\n\n',
};

const h3PromptAuthoringGuidance = {
  'zh-CN':
    'H3 创作语言：描述性正文使用用户当前创作语言（例如中文），不要在创作、优化、重写、拆分、合并或适配时提前翻译成英文。结构字段名、<Subject N>、@素材名、[Shot N] At 00:SS.mmm 等格式和时间标签保持固定。对白逐字保留在 <d>[Language]对白原文</d> 中并填写实际语言（例如 Chinese）；歌词逐字保留在 <lyrics>歌词原文</lyrics> 中；画面内文字逐字保留在 <on_screen_text>文字原文</on_screen_text> 中。这些显式标签界定提交翻译时的原文保护边界，普通引号不代表保护边界。提交生成时系统只为本次任务准备英文描述，不改已保存的创作正文或保护区原文。',
  en: 'H3 authoring language: write descriptive prose in the user’s current authoring language, such as Chinese. Do not translate it into English while creating, improving, rewriting, splitting, merging, or adapting. Keep section keys, <Subject N>, @media names, and format and time labels such as [Shot N] At 00:SS.mmm fixed. Preserve dialogue verbatim inside <d>[Language]original dialogue</d>, using its actual language such as Chinese; preserve lyrics verbatim inside <lyrics>original lyrics</lyrics>; preserve on-screen text verbatim inside <on_screen_text>original text</on_screen_text>. These explicit tags define the literal boundaries protected during submission translation; ordinary quotation marks do not define protected boundaries. Submission prepares English descriptions only for the current generation task, without changing the saved authoring prose or protected literals.',
};

const zhCN: Record<AgentTemplateKey, string> = {
  'script.create':
    '请帮助我为当前视频准备创作内容。先读取项目说明、视频名称、目标时长和现有内容，结合我的要求判断适合编写完整剧本、广告或口播文案、内容提纲，还是视觉创意说明；如果题材、受众、风格或其他关键信息不明确，请一次集中向我确认。信息明确后，使用结构清楚的 Markdown 编写内容，并创建、应用内容 ChangeSet。短剧等叙事内容可用二级标题（##）组织章节、三级标题（###）组织场景；其他类型采用适合其用途的结构。不要只在对话中输出长篇内容，也不要开始识别资产、规划故事板或生成媒体。\n\n附加要求：',
  'script.extract_assets':
    assetReuseTemplateGuidance['zh-CN'] +
    '请以专业美术指导和视觉设计师的标准，阅读当前视频的全部创作内容、已有资产及项目内其他视频可复用的资产，识别真正需要新增或复用的人物、地点和物品。默认不要创建独立的视觉风格资产；应把整体风格的色彩、光影、镜头质感、材质语言和美术基调融入每个人物、地点、物品各自的视觉提示词，使这些资产可独立生成且风格一致。只有用户在本次消息中明确要求建立独立风格资产时，才创建 style 类资产。以现有内容为依据，避免重复资产，并把推测明确标为建议。每个资产的视觉提示词应具体、可生成、便于跨镜头保持一致，覆盖形态、材质、色彩、时代、状态、光线和识别特征。人物默认采用纯白背景、正面全身、自然站姿、从头到脚完整入镜；只有内容或身份确实需要时才携带物品。地点说明空间关系、建筑与陈设、时间和光线；物品说明完整外形、尺度、材质和使用痕迹。用户在本次消息中提出的明确要求优先于这些默认规则。完成后创建并应用资产 ChangeSet，不要生成图片。\n\n附加要求：',
  'asset.add':
    assetReuseTemplateGuidance['zh-CN'] +
    '请以专业美术指导的标准，读取当前视频可用的创作内容、视频名称、目标时长、项目说明和已有资产，帮我添加所需的新资产。创作内容可以为空；此时根据其他上下文和我的本次要求继续。先判断人物、地点、物品或视觉风格的类别与用途；如果关键信息不足，请一次集中向我确认。避免创建重复资产，并把上下文未明确的信息标为建议。视觉提示词需具体、可生成并服务于跨镜头一致性。人物默认纯白背景、正面全身、自然站姿、从头到脚完整入镜；物品仅在内容或身份需要时出现。用户在本次消息中提出的明确要求优先。信息明确后创建并应用资产 ChangeSet，不要生成图片。\n\n附加要求：',
  'asset.edit_prompt':
    '请读取资产「{{name}}」（asset_id: {{asset_id}}）当前视觉提示词、当前视频可用的创作内容（可以为空）和相关资产，以专业美术指导与提示词设计师的标准进行优化。保持资产身份和名称不变，只更新该资产的 prompt。描述应明确、可生成、无自相矛盾，并强化跨镜头连续性。若为人物，默认纯白背景、正面全身、自然站姿、头脚完整；只有内容需要时才携带物品。用户在本次消息中提出的明确要求优先，例如三视图、特定构图或背景。完成后创建并应用 ChangeSet，不要生成图片。\n\n附加要求：',
  'storyboard.plan':
    '请以导演、摄影指导和分镜师的专业标准，读取当前视频所有可用信息，包括创作内容、项目说明、视频名称、目标时长、现有资产与当前 Storyboard，规划完整故事板。创作内容可以为空；此时请使用其他上下文和我的本次要求继续。若创作方向仍不足，请一次集中确认题材、内容、风格与声音等必要信息。按内容或叙事节拍拆分场景和视频单元，兼顾表演或主体运动、景别、构图、摄影机运动、连续性、光线、节奏、对白、旁白、音乐、环境声和计划时长（没有的元素不要臆造）。默认采用参考素材模式；视频生成提示词是每个视频单元的核心，其他字段用于补充结构信息。绑定所有实际使用的资产，并按附加的当前视频模型规则直接编写最终提示词。避免空单元、重复镜头和冲突指令。完成后创建并应用 Storyboard ChangeSet，不要生成图片或视频。用户在本次消息中提出的明确要求优先。\n\n附加要求：',
  'storyboard.add_clip':
    '请以导演、摄影指导和分镜师的专业标准，在场景「{{scene_title}}」（scene_id: {{scene_id}}）新增一个完整、可执行的视频单元。先阅读当前视频可用的创作内容（可以为空）、该场景现有视频单元及相关资产，并以现有场景和我的本次要求为主要依据。确保内容、轴线、主体位置、动作和光线连续。默认使用参考素材模式；重点编写专业的视频生成提示词，明确主体、动作路径、景别、构图、镜头运动、节奏、光线、物理变化与已有的对白或声音。绑定所有实际使用的资产，并按附加的当前视频模型规则直接编写最终提示词；其他字段作为补充。完成后创建并应用 ChangeSet，不要创建新场景或生成媒体。用户在本次消息中提出的明确要求优先。\n\n附加要求：',
  'prompt.first_frame':
    '请以导演和摄影指导的专业标准，为视频单元「{{clip_title}}」（clip_id: {{clip_id}}）编写首帧图片提示词。先读取该单元、场景上下文、可用创作内容（可以为空）和关联资产；描述一个可直接拍摄的确定瞬间，明确主体、表演状态、景别、机位、镜头感、构图、前中后景、光线、色彩、环境和关键连续性细节。保持人物与资产身份一致，避免同时描述多个时间点或与镜头目标无关的信息。只更新 first_image_prompt，并创建、应用 ChangeSet；不要生成图片。用户在本次消息中提出的明确要求优先。\n\n附加要求：',
  'prompt.last_frame':
    '请以导演和摄影指导的专业标准，为视频单元「{{clip_title}}」（clip_id: {{clip_id}}）编写尾帧图片提示词。先读取该单元、首帧、场景上下文、可用创作内容（可以为空）和关联资产；描述动作完成后的确定瞬间，明确主体最终位置与状态、景别、机位、镜头感、构图、光线、环境变化和连续性，使其既与首帧形成可实现的运动路径，又能自然衔接下一镜。只更新 last_image_prompt，并创建、应用 ChangeSet；不要生成图片。用户在本次消息中提出的明确要求优先。\n\n附加要求：',
  'prompt.video':
    '请以导演、摄影指导和视频生成提示词设计师的专业标准，为视频单元「{{clip_title}}」（clip_id: {{clip_id}}）编写“{{generation_mode}}”模式的视频提示词。先读取该单元、场景上下文、可用创作内容（可以为空）、首尾帧及关联资产。提示词应以可见、可听、可执行的时间过程为核心，明确主体表演、动作路径与节奏、景别和构图、摄影机运动、光线变化、环境与物理反馈、已有对白、音效和连续性；避免互相冲突的机位、动作或风格指令。第一镜不写时间戳。参考素材模式下，在资产真正参与画面的语义位置按使用顺序写出 @资产名，首次出现顺序即提交时模型引用编号的映射顺序。只更新当前模式的视频提示词，并创建、应用 ChangeSet；不要生成视频。用户在本次消息中提出的明确要求优先。\n\n附加要求：',
  'prompt.video.adapt.h3':
    '请把视频单元「{{clip_title}}」（clip_id: {{clip_id}}）当前 {{generation_mode_id}} 模式的视频提示词适配为 H3，目标 generator_id={{generator_id}}，provider_model_id={{provider_model_id}}，profile={{prompt_profile}}。先调用 get_video_prompt_adaptation_context 读取并确认目标、来源版本、单元上下文，以及所有 Asset 和上传图片/视频/音频。切换模式后目标模式可能为空；此时上下文会从其他模式返回来源，调用 append_video_prompt_adaptation 时必须原样传回 source_generation_mode。默认保留当前素材绑定；若用户本次明确 @ 引用未绑定素材或明确要求增加、移除、替换素材，调用 list_reference_media，并通过 reference_artifact_ids 提交完整的目标绑定顺序。不得仅因用户没有提到某个现有素材就移除它。若某个上传素材的用途不能从名称、描述、来源提示词和镜头上下文中可靠判断，请在当前对话一次性询问用户并停止，不要创建 ChangeSet；用户回复后重新读取上下文。信息充分时，通过 append_video_prompt_adaptation 创建、应用 ChangeSet，并提交完整 references 清单，每项填写 purposes，并用 note 记录“参考什么/不要参考什么”。只改当前模式的 H3 家族版本，不改模型、生成模式、时长或其他家族版本。若 profile 是 h3_base：正文使用用户当前创作语言并严格包含 integrated_multimodal_description、overall_soundscape、non_diegetic_music；首镜不写时间戳，后续镜头使用 [Shot N] At 00:SS.mmm；对白保留原语言并用 <d>[Chinese] ...</d> 等语言标签，必要时用 (S1)。若 profile 是 h3_ref2va：六个结构段的正文同样使用用户当前创作语言，严格包含 subject_definitions、summary、retention_analysis、detailed_description、overall_soundscape、non_diegetic_music，并保留 <Subject N>；所有图片、视频和音频引用都在语义位置使用精确 @素材名，不要自行写 <Picture N>/<Video N>/<Audio N>，提交生成时系统会按首次出现顺序转换为 H3 官方标签。两种 profile 的对白、歌词和画面内文字均保留原语言。保持故事、表演、对白和声音意图，不编造新素材。\n\n' +
    h3PromptAuthoringGuidance['zh-CN'] +
    '\n\n附加要求：',
  'prompt.video.adapt.seedance':
    '请把视频单元「{{clip_title}}」（clip_id: {{clip_id}}）当前 {{generation_mode_id}} 模式的视频提示词适配为 Seedance，目标 generator_id={{generator_id}}，provider_model_id={{provider_model_id}}，profile={{prompt_profile}}。先调用 get_video_prompt_adaptation_context 读取所有 Asset 和上传图片/视频/音频。默认保留当前素材绑定；若用户本次明确 @ 引用未绑定素材或明确要求增加、移除、替换素材，调用 list_reference_media，并通过 reference_artifact_ids 提交完整的目标绑定顺序。不得仅因用户没有提到某个现有素材就移除它。若素材用途不能可靠判断，请在当前对话一次性询问用户并停止，不要创建 ChangeSet；用户回复后重新读取上下文。信息充分时，通过 append_video_prompt_adaptation 创建、应用 ChangeSet，并提交完整 references 清单，为每项填写 purposes 和必要的 note。只改当前模式的 Seedance 家族版本。使用清晰的导演语言组织总体画面、主体表演、动作过程、运镜、光线、物理反馈、对白、环境声、音效和音乐；多阶段内容使用“镜头 N（约 X–Y 秒）”，镜头序号为兼容 Seedance 2.0/2.5 的主要结构，整数秒时间段用于节奏控制。保留每一个目标 @参考素材名并明确它用于身份、场景、动作、风格还是声音；不要编造素材，也不要修改模型或时长。\n\n附加要求：',
  'prompt.video.adapt.wan3':
    '请把视频单元「{{clip_title}}」（clip_id: {{clip_id}}）当前 {{generation_mode_id}} 模式的视频提示词适配为 Wan 3.0，目标 generator_id={{generator_id}}，provider_model_id={{provider_model_id}}，profile={{prompt_profile}}。先调用 get_video_prompt_adaptation_context 读取所有 Asset 和上传图片/视频/音频。默认保留当前素材绑定；若用户本次明确 @ 引用未绑定素材或明确要求增加、移除、替换素材，调用 list_reference_media，并通过 reference_artifact_ids 提交完整的目标绑定顺序。不得仅因用户没有提到某个现有素材就移除它。若素材用途不能可靠判断，请在当前对话一次性询问用户并停止，不要创建 ChangeSet；用户回复后重新读取上下文。信息充分时，通过 append_video_prompt_adaptation 创建、应用 ChangeSet，并提交完整 references 清单，为每项填写 purposes 和必要的 note。只改当前模式的 Wan 3.0 家族版本。按“总体描述、参考素材及用途、连续时间轴/分镜、对白、音效与 BGM、风格情绪、必要的负向要求”组织；时间段连续且动作密度可实现。保留每一个目标 @参考素材名，中文提示词按素材顺序对应图1/视频1/音频1；英文提示词对应 Image 1/Video 1/Audio 1。无台词或无音乐时可明确说明，不编造素材，也不修改模型或时长。\n\n附加要求：',
};

const en: Record<AgentTemplateKey, string> = {
  'script.create':
    'Help me prepare the creative content for the current video. First read the project description, video name, target length, and existing content, then use my request to decide whether this should be a complete screenplay, advertising or spoken copy, an outline, or a visual creative brief. Ask for all missing essentials together. Once clear, write well-structured Markdown and create and apply a content ChangeSet. Narrative work such as a short drama may use level-two headings (##) for sections and level-three headings (###) for scenes; use a structure appropriate to the purpose for other formats. Do not leave long content only in chat, and do not identify assets, plan a storyboard, or generate media.\n\nAdditional requirements:',
  'script.extract_assets':
    assetReuseTemplateGuidance.en +
    'Act as a professional production designer and visual designer. Read all creative content for the current video, existing assets, and reusable assets from other videos in the project, then identify the people, locations, and objects that truly need to be reused or added. Do not create a standalone visual-style asset by default. Instead, incorporate the overall palette, lighting, lens character, material language, texture, and art direction into each person, location, and object prompt so every asset can be generated independently with a consistent style. Create a style asset only when the user explicitly asks for a standalone style asset in the current message. Ground decisions in the available content, avoid duplicates, and label inferred details as recommendations. Make every visual prompt specific, generatable, and suitable for continuity across shots, covering form, materials, color, period, condition, lighting, and identifying features. By default, present people against a pure white background in a frontal, natural full-body standing pose with the entire head and feet visible; include an object only when the content or identity genuinely calls for one. For locations, define spatial relationships, architecture, dressing, time, and light. For objects, define full form, scale, material, and wear. Explicit requirements in this message override these defaults. Create and apply an asset ChangeSet when complete. Do not generate images.\n\nAdditional requirements:',
  'asset.add':
    assetReuseTemplateGuidance.en +
    'Act as a professional production designer. Read all available context for the current video, including its creative content, name, target length, project description, and existing assets, then help add the required new asset. The creative content may be empty; continue from the other context and my current request. Determine whether it is a person, location, object, or visual style and how it serves the content; ask for all missing essentials together. Avoid duplicates and label unstated details as recommendations. The visual prompt must be specific, generatable, and useful for continuity. By default, show a person against a pure white background, front-facing in a natural full-body standing pose with head and feet fully visible; include an object only when the content or identity requires it. Explicit requirements in this message take priority. Once clear, create and apply an asset ChangeSet. Do not generate images.\n\nAdditional requirements:',
  'asset.edit_prompt':
    'Read the current visual prompt for “{{name}}” (asset_id: {{asset_id}}), the available creative content for the current video (which may be empty), and related assets. Improve it to a professional production-design and prompt-writing standard while preserving the asset identity and name; update only this asset prompt. Make it clear, generatable, internally consistent, and strong enough for continuity across shots. For a person, default to a pure white background, front-facing natural full-body stance, with the complete head and feet visible; include an object only when the content calls for it. Explicit requirements in this message take priority, including requests such as a three-view sheet, a different composition, or another background. Create and apply a ChangeSet when complete. Do not generate images.\n\nAdditional requirements:',
  'storyboard.plan':
    'Act as a professional director, cinematographer, and storyboard artist. Read all available current-video context, including its creative content, project description, name, target length, existing assets, and current storyboard, then plan the complete storyboard. The creative content may be empty; continue from the other context and my current request. If the direction is still insufficient, ask for the essential subject, content, style, and sound choices together. Break the work into scenes and video units by content or dramatic beat while managing subject movement or performance, shot size, composition, camera movement, continuity, lighting, rhythm, dialogue, narration, music, ambience, and planned duration—without inventing elements that are not called for. Use reference mode by default. Treat each video-generation prompt as the core of its unit and use other fields as supporting structure. Bind every asset actually used and write the final prompt directly according to the appended current-video-model rules. Avoid empty units, redundant coverage, and conflicting instructions. Create and apply a Storyboard ChangeSet when complete. Do not generate images or video. Explicit requirements in this message take priority.\n\nAdditional requirements:',
  'storyboard.add_clip':
    'Act as a professional director, cinematographer, and storyboard artist. Add one complete, executable video unit to “{{scene_title}}” (scene_id: {{scene_id}}). First read the available creative content for the video (which may be empty), existing units in this scene, and related assets, using the existing scene and my current request as the primary direction. Keep content, screen direction, subject position, action, and lighting continuous. Use reference mode by default. Prioritize a professional video-generation prompt that defines the subject, action path, shot size, composition, camera movement, rhythm, light, physical change, and any existing dialogue or sound. Bind every asset actually used and write the final prompt directly according to the appended current-video-model rules; other fields provide supporting structure. Create and apply a ChangeSet when complete. Do not create a new scene or generate media. Explicit requirements in this message take priority.\n\nAdditional requirements:',
  'prompt.first_frame':
    'Act as a professional director and cinematographer. Write the first-frame image prompt for “{{clip_title}}” (clip_id: {{clip_id}}) after reading the unit, scene context, available creative content (which may be empty), and related assets. Describe one precise, directly shootable instant: subject, performance state, shot size, camera position, lens character, composition, foreground/midground/background, lighting, color, environment, and essential continuity details. Preserve character and asset identity. Avoid describing multiple moments or irrelevant information. Update only first_image_prompt, then create and apply a ChangeSet. Do not generate an image. Explicit requirements in this message take priority.\n\nAdditional requirements:',
  'prompt.last_frame':
    'Act as a professional director and cinematographer. Write the last-frame image prompt for “{{clip_title}}” (clip_id: {{clip_id}}) after reading the unit, first frame, scene context, available creative content (which may be empty), and related assets. Describe the precise instant after the action: final subject position and state, shot size, camera position, lens character, composition, lighting, environmental change, and continuity. It must form an achievable motion path from the first frame and cut naturally into the next shot. Update only last_image_prompt, then create and apply a ChangeSet. Do not generate an image. Explicit requirements in this message take priority.\n\nAdditional requirements:',
  'prompt.video':
    'Act as a professional director, cinematographer, and video-generation prompt designer. Write the “{{generation_mode}}” video prompt for “{{clip_title}}” (clip_id: {{clip_id}}) after reading the unit, scene context, available creative content (which may be empty), first/last frames, and related assets. Center the prompt on a visible, audible, executable progression through time. Define performance, action path and rhythm, shot size and composition, camera movement, changing light, environment and physical response, existing dialogue, sound, and continuity. Avoid contradictory camera, action, or style directions. Do not timestamp the opening shot. In reference mode, place each @asset name at the semantic point where it enters the shot; first-mention order becomes the model reference-number mapping at submission. Update only the current mode video prompt, then create and apply a ChangeSet. Do not generate video. Explicit requirements in this message take priority.\n\nAdditional requirements:',
  'prompt.video.adapt.h3':
    'Adapt the current {{generation_mode_id}} video prompt for “{{clip_title}}” (clip_id: {{clip_id}}) to H3. Target generator_id={{generator_id}}, provider_model_id={{provider_model_id}}, profile={{prompt_profile}}. First call get_video_prompt_adaptation_context and inspect every bound Asset and uploaded image, video, or audio medium. After a mode switch the target mode may be empty; in that case context can return a source from another mode, and append_video_prompt_adaptation must copy source_generation_mode back exactly. Preserve current media bindings by default. If this request explicitly @mentions unbound media or explicitly adds, removes, or replaces media, call list_reference_media and pass the complete desired binding order through reference_artifact_ids. Never remove an existing binding merely because the user did not mention it. If a medium’s purpose cannot be inferred safely from its name, description, source prompt, and Clip context, ask all essential questions together in this chat and stop without creating a ChangeSet; read the context again after the user replies. When clear, use append_video_prompt_adaptation to create and apply a ChangeSet and submit the complete references list, with purposes for every item and a note stating what to copy or avoid when useful. Change only this mode’s H3-family version; do not change model, mode, duration, or other families. For h3_base, write the body in the user’s current authoring language with exactly integrated_multimodal_description, overall_soundscape, and non_diegetic_music; do not timestamp Shot 1, timestamp later shots as [Shot N] At 00:SS.mmm, preserve dialogue in its original language inside tags such as <d>[Chinese] ...</d>, and use speaker IDs such as (S1) when needed. For h3_ref2va, also write all six structured sections in the user’s current authoring language and use exactly subject_definitions, summary, retention_analysis, detailed_description, overall_soundscape, and non_diegetic_music, retaining <Subject N>. At every semantic media reference, use the exact @media name; do not write <Picture N>, <Video N>, or <Audio N> yourself because submission converts @names to official H3 labels in first-mention order. In both profiles, preserve dialogue, lyrics, and on-screen text in their original language. Preserve story, performance, dialogue, and sound intent. Never invent media.\n\n' +
    h3PromptAuthoringGuidance.en +
    '\n\nAdditional requirements:',
  'prompt.video.adapt.seedance':
    'Adapt the current {{generation_mode_id}} video prompt for “{{clip_title}}” (clip_id: {{clip_id}}) to Seedance. Target generator_id={{generator_id}}, provider_model_id={{provider_model_id}}, profile={{prompt_profile}}. First call get_video_prompt_adaptation_context and inspect every bound Asset and uploaded image, video, or audio medium. Preserve current media bindings by default. If this request explicitly @mentions unbound media or explicitly adds, removes, or replaces media, call list_reference_media and pass the complete desired binding order through reference_artifact_ids. Never remove an existing binding merely because the user did not mention it. If a medium’s purpose cannot be inferred safely, ask all essential questions together in this chat and stop without creating a ChangeSet; read the context again after the user replies. When clear, use append_video_prompt_adaptation to create and apply a ChangeSet and submit the complete references list with purposes and useful notes. Change only this mode’s Seedance-family version. Use clear directing language for the overall image, performance, action, camera, light, physical response, dialogue, ambience, effects, and music. For multi-stage action, use “Shot N (about X–Y seconds)”: shot order is the compatibility structure for Seedance 2.0/2.5 and integer seconds express pacing. Keep every desired @reference name at its semantic use point and state whether it controls identity, scene, motion, style, or sound. Never invent media or change model or duration.\n\nAdditional requirements:',
  'prompt.video.adapt.wan3':
    'Adapt the current {{generation_mode_id}} video prompt for “{{clip_title}}” (clip_id: {{clip_id}}) to Wan 3.0. Target generator_id={{generator_id}}, provider_model_id={{provider_model_id}}, profile={{prompt_profile}}. First call get_video_prompt_adaptation_context and inspect every bound Asset and uploaded image, video, or audio medium. Preserve current media bindings by default. If this request explicitly @mentions unbound media or explicitly adds, removes, or replaces media, call list_reference_media and pass the complete desired binding order through reference_artifact_ids. Never remove an existing binding merely because the user did not mention it. If a medium’s purpose cannot be inferred safely, ask all essential questions together in this chat and stop without creating a ChangeSet; read the context again after the user replies. When clear, use append_video_prompt_adaptation to create and apply a ChangeSet and submit the complete references list with purposes and useful notes. Change only this mode’s Wan 3.0-family version. Organize the result as overall description, references and purposes, continuous timeline/shots, dialogue, sound effects and BGM, style and mood, and only necessary negative constraints. Keep time ranges continuous and action density achievable. Keep every desired @reference name at its semantic use point; Chinese prompts map media to 图1/视频1/音频1 and English prompts to Image 1/Video 1/Audio 1. State no dialogue or no music when appropriate. Never invent media or change model or duration.\n\nAdditional requirements:',
};

export const defaultAgentTemplates: Record<SupportedLanguage, Record<AgentTemplateKey, string>> = {
  'zh-CN': zhCN,
  en,
};

export function renderAgentTemplate(template: string, variables: Record<string, string | number> = {}) {
  return template.replace(/\{\{([a-z_]+)}}/g, (match, key: string) =>
    Object.hasOwn(variables, key) ? String(variables[key]) : match,
  );
}

// Keep the user's supplement at the end, including any existing custom text.
// Only recognize a section heading, not those words embedded in ordinary prose.
export function appendAgentTemplateGuidance(template: string, guidance: string): string {
  if (!guidance.trim()) return template;
  const heading = [...template.matchAll(/^(?:附加要求：|Additional requirements:)/gm)].at(-1);
  if (!heading || heading.index === undefined)
    return [template.trimEnd(), guidance.trim()].filter(Boolean).join('\n\n');
  return [template.slice(0, heading.index).trimEnd(), guidance.trim(), template.slice(heading.index)]
    .filter(Boolean)
    .join('\n\n');
}

export function renderStoryboardVideoModelGuidance(
  language: SupportedLanguage,
  target: { generatorID: string; providerModelID: string; keyframeProviderModelID?: string },
) {
  const isChinese = language === 'zh-CN';
  const generatorID = target.generatorID;
  const keyframeModelID = target.keyframeProviderModelID;
  const modeModels = isChinese
    ? `- reference 模式填写 provider_model_id=${target.providerModelID}。${keyframeModelID ? `first_image 或 first_last_image 模式填写 provider_model_id=${keyframeModelID}。` : '未配置兼容的首帧/首尾帧模型，不要把参考模型作为首尾帧模型。'}`
    : `- Use provider_model_id=${target.providerModelID} for reference mode. ${keyframeModelID ? `Use provider_model_id=${keyframeModelID} for first_image or first_last_image.` : 'No compatible endpoint model is configured; do not use a reference-only model for endpoint modes.'}`;
  const targetLines = isChinese
    ? `系统注入的当前视频模型要求（不属于可编辑模板）：\n- 以下规则仅用于 Agent 创建或修改视频提示词，包括规划、新增、拆分、合并、优化和适配；不用于资产视觉提示词或首尾帧图片提示词，不覆盖用户手工内容。\n- append_storyboard_operations 的每个 create_clip 必须填写 generator_id=${generatorID}。\n${modeModels}\n- 用户本次明确要求优先；生成模式未指定时默认 reference。\n- 直接写入适配当前模型的视频提示词，素材在语义位置使用精确 @素材名；提交时系统按首次出现顺序转换为官方标签。每个 Clip 的时间轴独立从零开始，每个 Clip 的时长应符合当前模式模型限制。`
    : `System-injected current video model requirements (not part of the editable template):\n- Apply these rules only when Agent creates or edits video prompts, including planning, adding, splitting, merging, improving, and adapting. Do not apply video syntax to asset or first/last-frame image prompts, or overwrite manually edited content.\n- Every create_clip passed to append_storyboard_operations must include generator_id=${generatorID}.\n${modeModels}\n- Explicit requirements in this turn take priority; default to reference mode when no mode is requested.\n- Write prompts for the current model, using exact @media names at semantic reference points. Submission converts them to official labels in first-mention order. Each Clip has its own local timeline from zero and must use its mode model's legal duration.`;
  if (generatorID.startsWith('video.seedance-')) {
    return `${targetLines}\n${
      isChinese
        ? '- Seedance：使用清晰的导演语言组织总体画面、主体表演、动作过程、运镜、光线、物理反馈、对白、环境声、音效和音乐；多阶段内容使用“镜头 N（约 X–Y 秒）”，整数秒时间段用于节奏控制。精确 @素材名说明身份、场景、动作、风格或声音用途，不套用 H3 的英文结构字段。'
        : '- Seedance: use clear directing language for overall image, performance, action, camera, light, physical response, dialogue, ambience, effects, and music. For multi-stage action use Shot N (about X–Y seconds), with integer seconds for pacing. Keep exact @media names and their identity, scene, motion, style, or sound purposes; do not use H3-specific English section fields.'
    }`;
  }
  if (generatorID.startsWith('video.wan-')) {
    return `${targetLines}\n${
      isChinese
        ? '- Wan 3.0：按“总体描述、参考素材及用途、连续时间轴/分镜、对白、音效与 BGM、风格情绪、必要的负向要求”组织，时间段连续且动作密度可实现。保留精确 @素材名，中文引用顺序对应图1/视频1/音频1；英文对应 Image 1/Video 1/Audio 1。不套用 H3 字段。'
        : '- Wan 3.0: organize overall description, references and purposes, continuous timeline/shots, dialogue, sound effects and BGM, style and mood, and necessary negative constraints. Keep time ranges continuous and achievable. Keep exact @media names; Chinese reference order maps to 图1/视频1/音频1 and English to Image 1/Video 1/Audio 1. Do not use H3-specific fields.'
    }`;
  }
  if (!generatorID.startsWith('video.h3.')) return targetLines;
  const h3Lines = isChinese
    ? '- H3 reference 使用 h3_ref2va@1：六段依次为 subject_definitions、summary、retention_analysis、detailed_description、overall_soundscape、non_diegetic_music，正文使用用户当前创作语言，保留 <Subject N>；图片、视频和音频在语义位置使用精确 @素材名，不要自行编写 <Picture N>/<Video N>/<Audio N>。首镜不写时间戳，后续镜头使用 [Shot N] At 00:SS.mmm。\n- H3 首帧/首尾帧使用 h3_base@1：三段依次为 integrated_multimodal_description、overall_soundscape、non_diegetic_music，正文使用用户当前创作语言。首镜不写时间戳，后续镜头使用 [Shot N] At 00:SS.mmm。\n- 对白、歌词和画面内文字保留原语言；对白使用 <d>[Chinese] ...</d> 等语言标签。'
    : '- For H3 reference mode use h3_ref2va@1 with these six sections in order: subject_definitions, summary, retention_analysis, detailed_description, overall_soundscape, non_diegetic_music. Write their bodies in the user’s current authoring language and retain <Subject N>. Use exact @media names at semantic image, video, and audio reference points; do not write <Picture N>, <Video N>, or <Audio N> yourself. Do not timestamp Shot 1; use [Shot N] At 00:SS.mmm for later shots.\n- For H3 first-frame/first-last-frame modes use h3_base@1 with integrated_multimodal_description, overall_soundscape, and non_diegetic_music in order and bodies in the user’s current authoring language. Do not timestamp Shot 1; use [Shot N] At 00:SS.mmm for later shots.\n- Preserve dialogue, lyrics, and on-screen text in their original language; tag dialogue with its language, such as <d>[Chinese] ...</d>.';
  return `${targetLines}\n${h3Lines}\n${h3PromptAuthoringGuidance[language]}`;
}
