import { describe, expect, it } from 'vitest';
import { taskFailureMessage } from './taskFailure';
import i18n from './i18n';

describe('taskFailureMessage', () => {
  it('localizes business codes ahead of server messages and distinguishes missing wallets', async () => {
    const task = { failure_code: 'shotforward_insufficient_points', failure_message: 'raw upstream error' };
    await i18n.changeLanguage('zh-CN');
    expect(taskFailureMessage(task, i18n.t)).toContain('余额不足');
    expect(taskFailureMessage({ failure_code: 'shotforward_wallet_not_found' }, i18n.t)).toContain('钱包');
    await i18n.changeLanguage('en');
    expect(taskFailureMessage(task, i18n.t)).toContain('insufficient');
  });
  it('uses a localized safe category and never exposes an unknown translation key', async () => {
    await i18n.changeLanguage('zh-CN');
    expect(
      taskFailureMessage({ failure_code: 'reference_delivery_login_required' }, i18n.t, 'zh-CN'),
    ).toContain('ShotForward');
    expect(taskFailureMessage({ failure_code: 'unknown_internal_failure' }, i18n.t, 'zh-CN')).toBe(
      '任务执行失败，请检查生成服务和输入内容。',
    );
  });

  it('prefers a sanitized official Provider message', () => {
    expect(
      taskFailureMessage(
        { failure_code: 'provider_response_error', failure_message: 'Seedance: rejected reference image' },
        i18n.t,
      ),
    ).toBe('Seedance: rejected reference image');
  });
});
