import type { TFunction } from 'i18next';
import { businessErrorMessage } from './businessErrors';

interface FailedTask {
  failure_code?: string;
  failure_message?: string;
}

const messages = {
  zh: {
    generator_start_failed: '生成任务启动失败，请检查模型和输入。',
    generator_poll_failed: '无法刷新生成状态，请稍后重试。',
    generator_request_too_large: '生成请求过大，请减少参考素材后重试。',
    provider_unreachable: '无法连接生成服务，请检查网络后重试。',
    reference_delivery_login_required: '承接视频需要连接 ShotForward 账号，请登录后重试。',
    reference_delivery_failed: '承接视频交付失败，请检查 ShotForward 账号和网络。',
  },
  en: {
    generator_start_failed: 'Could not start generation. Check the model and inputs.',
    generator_poll_failed: 'Could not refresh generation status. Retry shortly.',
    generator_request_too_large: 'The request is too large. Remove references and retry.',
    provider_unreachable: 'Could not reach the generation service. Check your network.',
    reference_delivery_login_required: 'Sign in to ShotForward to deliver the continuation video.',
    reference_delivery_failed:
      'Could not deliver the continuation video. Check ShotForward and your network.',
  },
} as const;

export function taskFailureMessage(task: FailedTask, t: TFunction, language?: string): string {
  const businessMessage = businessErrorMessage(task.failure_code, t);
  if (businessMessage) return businessMessage;
  if (task.failure_message) return task.failure_message;
  const localized = language?.startsWith('zh') ? messages.zh : messages.en;
  return localized[task.failure_code as keyof typeof localized] ?? t('tasks.failures.default');
}

export function generationCapabilityMessage(
  details: Record<string, unknown>,
  language?: string,
): string | undefined {
  const { constraint, role, actual, maximum } = details;
  if (
    typeof constraint !== 'string' ||
    typeof actual !== 'number' ||
    typeof maximum !== 'number' ||
    maximum <= 0
  )
    return;
  const zh = language?.startsWith('zh');
  const media =
    role === 'audio_reference'
      ? zh
        ? '音频'
        : 'audio'
      : role === 'video_reference'
        ? zh
          ? '视频'
          : 'video'
        : zh
          ? '素材'
          : 'media';
  const subject = zh
    ? constraint === 'reference_total_duration'
      ? `参考${media}总时长`
      : constraint === 'reference_duration'
        ? `单条参考${media}时长`
        : constraint === 'reference_count'
          ? `参考${media}数量`
          : constraint === 'total_reference_count'
            ? '参考素材数量'
            : constraint === 'total_timed_reference_duration'
              ? '参考音频和视频合计时长'
              : ''
    : constraint === 'reference_total_duration'
      ? `Total reference ${media} duration`
      : constraint === 'reference_duration'
        ? `Single reference ${media} duration`
        : constraint === 'reference_count'
          ? `Reference ${media} count`
          : constraint === 'total_reference_count'
            ? 'Total reference count'
            : constraint === 'total_timed_reference_duration'
              ? 'Combined reference audio and video duration'
              : '';
  if (!subject) return;
  const duration = constraint.includes('duration');
  const display = (value: number) =>
    duration ? (value / 1000).toFixed(3).replace(/\.?0+$/, '') : String(value);
  return zh
    ? `${subject}为 ${display(actual)}${duration ? '秒' : ''}，当前模型上限为 ${display(maximum)}${duration ? '秒' : ''}。请调整输入后重试。`
    : `${subject} is ${display(actual)}${duration ? ' sec' : ''}; this model allows at most ${display(maximum)}${duration ? ' sec' : ''}. Adjust the inputs and try again.`;
}
