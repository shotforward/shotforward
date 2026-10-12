import type { SupportedLanguage } from './i18n';

// These instructions are only used in the lazy-loaded Storyboard workspace.
// Keep them out of the global locale bundle needed at application startup.
export function clipSplitAgentRequest(
  language: SupportedLanguage,
  clip: { title: string; clipID: string; duration: number },
) {
  return language === 'zh-CN'
    ? `请将视频单元「${clip.title}」（clip_id: ${clip.clipID}，${clip.duration} 秒）按叙事节拍拆为 2–4 个内容完整、可独立生成的单元。读取所在场景及绑定资产，完整保留对白、动作和顺序，按每个单元的内容与当前视频模型合法时长分别规划，拆分后的总时长可以变化。本次实际涉及模式的每个视频提示词遵守当前目标模型规则。使用 append_clip_split_operation 提交一次可撤销的原子变更，不要以分别新增、删除模拟拆分。`
    : `Split video unit “${clip.title}” (clip_id: ${clip.clipID}, ${clip.duration}s) into 2–4 complete, independently generatable units at narrative beats. Read its scene and bound assets; preserve all dialogue, action, and order. Plan each replacement around its content and the current video model’s legal duration; the combined duration may change. Write each involved mode’s video prompt according to its current target model rules. Submit one undoable atomic change with append_clip_split_operation; do not simulate it with separate create/delete operations.`;
}

export function clipMergeAgentRequest(
  language: SupportedLanguage,
  selection: { sceneTitle: string; sceneID: string; duration: number; clipList: string },
) {
  return language === 'zh-CN'
    ? `请将场景「${selection.sceneTitle}」（scene_id: ${selection.sceneID}）中以下按顺序相邻的视频单元合并为一个完整的视频单元，总时长保持 ${selection.duration} 秒。请先完整读取每个视频单元及其资产，综合标题、画面、对白、创作意图和各模式提示词作为上下文。仅为本次操作实际涉及的模式按目标视频模型规则编写提示词，不改未涉及模式或其他家族的版本，再调用 append_clip_merge_operation 提交并应用可撤销的变更；不要遗漏或重排内容。\n\n${selection.clipList}`
    : `Merge the following adjacent video units in scene “${selection.sceneTitle}” (scene_id: ${selection.sceneID}) into one complete video unit while preserving the total duration of ${selection.duration} seconds. Read every unit and its assets in full, use the title, visuals, dialogue, creative intent, and existing mode prompts as context. Write only the modes involved in this operation according to their target video model rules; preserve uninvolved modes and other model-family versions. Then call append_clip_merge_operation and apply the undoable change. Do not omit or reorder content.\n\n${selection.clipList}`;
}
