import { FormEvent, MouseEvent, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { APIError } from '../api/client';
import { exchangeCloudLoginCode, getCloudAccount, logoutCloud, resetCloudSession } from '../api/cloud';
import { hasDesktopBridge, openExternal } from '../desktop';

export function ShotForwardAccountSettingsPage() {
  const { t, i18n } = useTranslation();
  const copy = i18n.resolvedLanguage?.startsWith('zh') ? accountCopy.zh : accountCopy.en;
  const queryClient = useQueryClient();
  const [loginCode, setLoginCode] = useState('');
  const [portalOpenFailed, setPortalOpenFailed] = useState(false);
  const [portalCopied, setPortalCopied] = useState(false);
  const account = useQuery({ queryKey: ['cloud-account'], queryFn: getCloudAccount, retry: false });
  const refreshCloudCapabilities = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['cloud-account'] }),
      queryClient.invalidateQueries({ queryKey: ['generation-providers'] }),
      queryClient.invalidateQueries({ queryKey: ['agent-runtime'] }),
    ]);
  };
  const signIn = useMutation({
    mutationFn: () => exchangeCloudLoginCode(loginCode.trim()),
    onSuccess: async () => {
      setLoginCode('');
      await refreshCloudCapabilities();
    },
  });
  const signOut = useMutation({ mutationFn: logoutCloud, onSuccess: refreshCloudCapabilities });
  const resetSession = useMutation({ mutationFn: resetCloudSession, onSuccess: refreshCloudCapabilities });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (loginCode.trim()) signIn.mutate();
  };
  const signedIn = account.data?.signed_in;
  const points = new Intl.NumberFormat(i18n.resolvedLanguage, { maximumFractionDigits: 9 });
  const signInFailure =
    signIn.error instanceof APIError && signIn.error.code === 'invalid_desktop_code'
      ? copy.invalidLoginCode
      : copy.signInFailed;
  const openAccountPortal = async (event: MouseEvent<HTMLAnchorElement>, url: string) => {
    if (!hasDesktopBridge()) return;
    event.preventDefault();
    setPortalOpenFailed(false);
    setPortalCopied(false);
    try {
      await openExternal(url);
    } catch {
      setPortalOpenFailed(true);
    }
  };
  const copyPortalURL = async (url: string) => {
    try {
      await navigator.clipboard.writeText(url);
      setPortalCopied(true);
    } catch {
      setPortalCopied(false);
    }
  };

  return (
    <div className="settings-section cloud-account-settings">
      <header className="settings-section__header">
        <div>
          <h2>{copy.title}</h2>
          <p>{copy.description}</p>
        </div>
        {signedIn && (
          <button
            className="button button--secondary"
            type="button"
            disabled={signOut.isPending}
            onClick={() => signOut.mutate()}
          >
            {signOut.isPending ? copy.signingOut : copy.signOut}
          </button>
        )}
      </header>

      <section className="cloud-capabilities" aria-label={copy.services}>
        {(['agent', 'image', 'video'] as const).map((capability) => (
          <article key={capability} data-capability={capability}>
            <span className="cloud-capabilities__icon" aria-hidden="true">
              {capability === 'agent' ? '✦' : capability === 'image' ? '◇' : '▶'}
            </span>
            <div>
              <strong>{copy.capabilities[capability].title}</strong>
              <small>{copy.capabilities[capability].hint}</small>
            </div>
          </article>
        ))}
      </section>

      {account.isPending && <div className="settings-loading skeleton" />}
      {account.isError && !account.data && (
        <div className="message-panel is-error">
          <strong>{copy.loadFailed}</strong>
          <p>{copy.loadFailedHint}</p>
          <div className="button-row">
            <button className="button" type="button" onClick={() => void account.refetch()}>
              {t('common.retry')}
            </button>
            <button
              className="button button--secondary"
              type="button"
              disabled={resetSession.isPending}
              onClick={() => resetSession.mutate()}
            >
              {resetSession.isPending ? copy.resetting : copy.reconnect}
            </button>
          </div>
          {resetSession.isError && <p className="form-error">{copy.resetFailed}</p>}
        </div>
      )}
      {account.data &&
        (signedIn ? (
          <>
            <div className="runtime-status status-ready cloud-account-status">
              <span className="status-dot" />
              <div>
                <strong>{account.data.user?.email || copy.connectedAccount}</strong>
                <small>{copy.connected}</small>
              </div>
            </div>
            <section className="settings-block">
              <h3>{copy.accountInformation}</h3>
              <dl className="definition-list">
                <div>
                  <dt>{copy.email}</dt>
                  <dd>{account.data.user?.email || '—'}</dd>
                </div>
                <div>
                  <dt>{copy.availablePoints}</dt>
                  <dd>{points.format(account.data.wallet?.available_points ?? 0)}</dd>
                </div>
                <div>
                  <dt>{copy.reservedPoints}</dt>
                  <dd>{points.format(account.data.wallet?.reserved_points ?? 0)}</dd>
                </div>
              </dl>
            </section>
            {signOut.isError && <p className="form-error">{copy.signOutFailed}</p>}
          </>
        ) : (
          <section className="settings-block cloud-sign-in">
            <h3>{copy.connect}</h3>
            <form className="form-stack" onSubmit={submit}>
              <div className="cloud-sign-in__guide">
                <p>{copy.codeHint}</p>
                {account.data.account_portal_url && (
                  <a
                    className="button button--secondary cloud-account-link"
                    href={account.data.account_portal_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    onClick={(event) => void openAccountPortal(event, account.data!.account_portal_url!)}
                  >
                    {copy.getCode}
                    <span aria-hidden="true">↗</span>
                  </a>
                )}
              </div>
              {portalOpenFailed && account.data.account_portal_url && (
                <div className="message-panel is-error cloud-account-link-fallback" role="alert">
                  <strong>{copy.portalOpenFailed}</strong>
                  <p>{copy.portalOpenHint}</p>
                  <code>{account.data.account_portal_url}</code>
                  <button
                    className="button button--secondary"
                    type="button"
                    onClick={() => void copyPortalURL(account.data!.account_portal_url!)}
                  >
                    {portalCopied ? copy.linkCopied : copy.copyLink}
                  </button>
                </div>
              )}
              <label className="field">
                {copy.code}
                <input
                  value={loginCode}
                  autoComplete="one-time-code"
                  maxLength={20}
                  onChange={(event) => setLoginCode(event.target.value.toUpperCase())}
                  placeholder="XXXXXX-XXXXXXX"
                />
              </label>
              <button
                className="button button--primary cloud-sign-in__submit"
                disabled={!loginCode.trim() || signIn.isPending}
              >
                {signIn.isPending ? copy.signingIn : copy.signIn}
                <span aria-hidden="true">→</span>
              </button>
              {signIn.isError && <p className="form-error">{signInFailure}</p>}
            </form>
          </section>
        ))}
    </div>
  );
}

const accountCopy = {
  zh: {
    title: 'ShotForward 账号',
    description: '绑定同一账号，统一使用 ShotForward 提供的 Agent、图像生成和视频生成服务。',
    services: '账号可用服务',
    capabilities: {
      agent: { title: 'Agent', hint: '理解项目上下文，辅助完成创作与编辑。' },
      image: { title: '图像生成', hint: '使用 ShotForward 提供的图像生成模型与额度。' },
      video: { title: '视频生成', hint: '使用 ShotForward 提供的视频模型与云端算力。' },
    },
    connect: '连接账号',
    codeHint: '请在 ShotForward 网站账号中心生成一次性登录码，并在五分钟内输入到这里。',
    getCode: '获取一次性登录码',
    portalOpenFailed: '无法打开系统浏览器',
    portalOpenHint: '请复制下面的网址到浏览器，登录后获取一次性登录码。',
    copyLink: '复制网址',
    linkCopied: '已复制',
    code: '一次性登录码',
    signIn: '登录 ShotForward',
    signingIn: '正在登录…',
    invalidLoginCode: '登录码无效、已过期或已使用，请重新生成后重试。',
    signInFailed: '无法连接 ShotForward 账号，请检查网络后重试。',
    connected: '账号已连接，云端模型与积分状态将自动同步。',
    connectedAccount: 'ShotForward 账号',
    accountInformation: '账号信息',
    email: '邮箱',
    availablePoints: '可用积分',
    reservedPoints: '任务预留积分',
    signOut: '退出账号',
    signingOut: '正在退出…',
    signOutFailed: '退出账号失败，请稍后重试。',
    loadFailed: 'ShotForward 账号状态暂时无法加载。',
    loadFailedHint: '如果登录凭据已经失效，可以清除本机会话并重新绑定账号。',
    reconnect: '重新绑定账号',
    resetting: '正在清除本地会话…',
    resetFailed: '无法清除本地会话，请检查系统钥匙串后重试。',
  },
  en: {
    title: 'ShotForward account',
    description: 'Connect one account for ShotForward Agent, image, and video generation services.',
    services: 'Account services',
    capabilities: {
      agent: { title: 'Agent', hint: 'Understand project context and assist with creative editing.' },
      image: { title: 'Image generation', hint: 'Use ShotForward image models and account credits.' },
      video: { title: 'Video generation', hint: 'Use ShotForward video models and cloud compute.' },
    },
    connect: 'Connect account',
    codeHint:
      'Generate a one-time code in the ShotForward account center and enter it here within five minutes.',
    getCode: 'Get one-time login code',
    portalOpenFailed: 'Could not open the system browser',
    portalOpenHint: 'Copy this address into your browser, sign in, and get a one-time login code.',
    copyLink: 'Copy address',
    linkCopied: 'Copied',
    code: 'One-time login code',
    signIn: 'Sign in to ShotForward',
    signingIn: 'Signing in…',
    invalidLoginCode: 'This login code is invalid, expired, or already used. Generate a new code and try again.',
    signInFailed: 'Could not connect the account. Check your network and try again.',
    connected: 'Account connected. Cloud models and points sync automatically.',
    connectedAccount: 'ShotForward account',
    accountInformation: 'Account information',
    email: 'Email',
    availablePoints: 'Available points',
    reservedPoints: 'Points reserved for tasks',
    signOut: 'Sign out',
    signingOut: 'Signing out…',
    signOutFailed: 'Could not sign out. Try again shortly.',
    loadFailed: 'The ShotForward account status could not be loaded.',
    loadFailedHint: 'If the saved credentials have expired, clear the local session and connect again.',
    reconnect: 'Reconnect account',
    resetting: 'Clearing local session…',
    resetFailed: 'Could not clear the local session. Check the system credential vault and try again.',
  },
} as const;

export { ShotForwardAccountSettingsPage as Component };
