import { FormEvent, MouseEvent, useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { configureRuntimeNetwork, getRuntimeNetwork } from '../api/runtime';
import { agentRuntimeOptions } from '../agentRuntime';
import { hasDesktopBridge, openExternal } from '../desktop';

const codexUpdateURL = 'https://developers.openai.com/codex/cli';
const codexUpdateCommand = 'curl -fsSL https://chatgpt.com/codex/install.sh | sh';

export function RuntimeSettingsPage() {
  const { t, i18n } = useTranslation();
  const networkCopy = i18n.resolvedLanguage?.startsWith('zh') ? runtimeNetworkCopy.zh : runtimeNetworkCopy.en;
  const upgradeCopy = i18n.resolvedLanguage?.startsWith('zh') ? runtimeUpgradeCopy.zh : runtimeUpgradeCopy.en;
  const safeExecutionLabel = i18n.resolvedLanguage?.startsWith('zh') ? '安全代码执行' : 'Safe code execution';
  const queryClient = useQueryClient();
  const runtime = useQuery(agentRuntimeOptions(queryClient, 'user_codex'));
  const network = useQuery({
    queryKey: ['agent-runtime-network'],
    queryFn: getRuntimeNetwork,
    retry: false,
  });
  const [proxyURL, setProxyURL] = useState('');
  const [networkSaved, setNetworkSaved] = useState(false);
  const [upgradeOpenFailed, setUpgradeOpenFailed] = useState(false);
  useEffect(() => {
    if (network.data?.network) setProxyURL(network.data.network.proxy_url || '');
  }, [network.data?.network?.proxy_url]);
  const configureNetwork = useMutation({
    mutationFn: () => configureRuntimeNetwork(proxyURL),
    onSuccess: async (result) => {
      setProxyURL(result.network.proxy_url);
      setNetworkSaved(true);
      queryClient.setQueryData(['agent-runtime-network'], result);
      await queryClient.invalidateQueries({ queryKey: ['agent-runtime'] });
    },
  });
  const localRuntime = runtime.data?.runtimes?.user_codex ?? runtime.data?.runtime;
  const health = localRuntime?.health;
  const upgradeRequired = localRuntime?.failure_code === 'codex_upgrade_required';
  const localModels =
    runtime.data?.runtime_models?.user_codex ??
    runtime.data?.models.filter((model) => model.runtime_kind !== 'shotforward') ??
    [];
  const proxyValid = isLoopbackProxyURL(proxyURL);
  const submitProxy = (event: FormEvent) => {
    event.preventDefault();
    setNetworkSaved(false);
    if (proxyValid) configureNetwork.mutate();
  };
  const openUpgradeGuide = async (event: MouseEvent<HTMLAnchorElement>) => {
    if (!hasDesktopBridge()) return;
    event.preventDefault();
    setUpgradeOpenFailed(false);
    try {
      await openExternal(codexUpdateURL);
    } catch {
      setUpgradeOpenFailed(true);
    }
  };
  return (
    <div className="settings-section">
      <header className="settings-section__header">
        <div>
          <h2>{t('settings.runtime')}</h2>
          <p>{t('runtime.description')}</p>
        </div>
        <button className="button" onClick={() => void runtime.refetch()} disabled={runtime.isFetching}>
          {t('runtime.recheck')}
        </button>
      </header>
      {runtime.isPending && <div className="settings-loading skeleton" />}
      {runtime.isError && (
        <div className="message-panel is-error">
          <strong>{t('runtime.loadFailed')}</strong>
          <button className="button" onClick={() => void runtime.refetch()}>
            {t('common.retry')}
          </button>
        </div>
      )}
      <section className="settings-block runtime-network-settings">
        <h3>{networkCopy.title}</h3>
        <p className="muted-copy">{networkCopy.hint}</p>
        {network.isPending && <div className="settings-loading skeleton" />}
        {network.isError && <p className="form-error">{networkCopy.loadFailed}</p>}
        {network.isSuccess && (
          <form className="form-stack" onSubmit={submitProxy}>
            <label className="field">
              {networkCopy.proxyURL}
              <input
                value={proxyURL}
                onChange={(event) => {
                  setProxyURL(event.target.value);
                  setNetworkSaved(false);
                  configureNetwork.reset();
                }}
                placeholder="http://127.0.0.1:7890"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
              />
              <small>{networkCopy.proxyHint}</small>
            </label>
            {!proxyValid && <p className="form-error">{networkCopy.invalid}</p>}
            {configureNetwork.isError && <p className="form-error">{networkCopy.saveFailed}</p>}
            {networkSaved && <p className="form-success">{networkCopy.saved}</p>}
            <div>
              <button className="button button--primary" disabled={!proxyValid || configureNetwork.isPending}>
                {configureNetwork.isPending ? networkCopy.restarting : networkCopy.saveAndRestart}
              </button>
            </div>
          </form>
        )}
      </section>
      {runtime.isSuccess && (
        <>
          <div className={`runtime-status status-${health}`}>
            <span className="status-dot" />
            <div>
              <strong>{t(`runtime.health.${health}`)}</strong>
              <small>
                {upgradeRequired
                  ? upgradeCopy.summary(localRuntime?.version ?? '', localRuntime?.minimum_version ?? '')
                  : localRuntime?.failure_summary || t('runtime.subscription')}
              </small>
            </div>
          </div>
          {upgradeRequired && (
            <section className="settings-block runtime-upgrade">
              <h3>{upgradeCopy.title}</h3>
              <p className="muted-copy">{upgradeCopy.hint}</p>
              <code className="runtime-upgrade__command">{codexUpdateCommand}</code>
              <a
                className="button button--primary"
                href={codexUpdateURL}
                target="_blank"
                rel="noreferrer"
                onClick={(event) => void openUpgradeGuide(event)}
              >
                {upgradeCopy.openGuide}
              </a>
              {upgradeOpenFailed && (
                <p className="form-error">
                  {upgradeCopy.openFailed} {codexUpdateURL}
                </p>
              )}
            </section>
          )}
          <section className="settings-block">
            <h3>{t('runtime.information')}</h3>
            <dl className="definition-list">
              <div>
                <dt>{t('runtime.connection')}</dt>
                <dd>{t('runtime.localCodex')}</dd>
              </div>
              <div>
                <dt>{t('runtime.authentication')}</dt>
                <dd>{t('runtime.userSubscription')}</dd>
              </div>
              <div>
                <dt>{t('runtime.version')}</dt>
                <dd>{localRuntime?.version || '—'}</dd>
              </div>
              <div>
                <dt>{safeExecutionLabel}</dt>
                <dd>
                  {localRuntime?.safe_execution_available
                    ? t('providers.health.ready')
                    : localRuntime?.safe_execution_failure_code
                      ? t('providers.health.unavailable')
                      : t('providers.health.unchecked')}
                </dd>
              </div>
            </dl>
          </section>
          <section className="settings-block">
            <h3>{t('runtime.models')}</h3>
            {localModels.length ? (
              <div className="model-list">
                {localModels.map((model) => (
                  <div key={model.id}>
                    <strong>{model.display_name}</strong>
                    <code>{model.id}</code>
                  </div>
                ))}
              </div>
            ) : (
              <p className="muted-copy">{t('runtime.noModels')}</p>
            )}
          </section>
          <p className="security-note">{t('runtime.tokenNote')}</p>
        </>
      )}
    </div>
  );
}

const runtimeNetworkCopy = {
  zh: {
    title: '网络与代理',
    hint: 'Codex 或 ShotForward 账号与云服务无法联网时，可填写本机 VPN 提供的 HTTP 或 SOCKS5 代理。只接受本机回环地址；保存后云请求立即生效，并会重启 Codex Runtime。',
    proxyURL: '本机代理地址（可选）',
    proxyHint: '例如 http://127.0.0.1:7890 或 socks5://127.0.0.1:7890；留空表示使用系统网络。',
    invalid: '请输入带端口的 localhost、127.0.0.1 或 ::1 本机代理地址。',
    loadFailed: '无法读取本机代理设置，请重启桌面应用后重试。',
    saveFailed: '代理设置保存失败，网络设置未更改。请检查地址后重试。',
    saved: '代理设置已保存，ShotForward 云请求已更新，Codex Runtime 已重启。请重新检查连接。',
    saveAndRestart: '保存并应用',
    restarting: '正在重启…',
  },
  en: {
    title: 'Network and proxy',
    hint: 'If Codex, your ShotForward account, or cloud services cannot reach the network, enter the HTTP or SOCKS5 proxy exposed by your local VPN. Only loopback addresses are accepted. Cloud requests update immediately and Codex Runtime restarts.',
    proxyURL: 'Local proxy address (optional)',
    proxyHint:
      'For example, http://127.0.0.1:7890 or socks5://127.0.0.1:7890. Leave blank to use the system network.',
    invalid: 'Enter a localhost, 127.0.0.1, or ::1 proxy address with a port.',
    loadFailed: 'The local proxy setting could not be loaded. Restart the desktop app and retry.',
    saveFailed: 'The proxy setting could not be saved. Network settings were not changed.',
    saved: 'Proxy saved. ShotForward cloud requests were updated and Codex Runtime restarted.',
    saveAndRestart: 'Save and apply',
    restarting: 'Restarting…',
  },
} as const;

const runtimeUpgradeCopy = {
  zh: {
    title: '请升级本机 Codex',
    summary: (current: string, minimum: string) =>
      `当前版本 ${current || '未知'}，ShotForward 最低要求 ${minimum || '0.155.1'}。`,
    hint: '升级完成后，请完全退出并重新打开 ShotForward，然后重新检查连接。',
    openGuide: '查看官方升级说明',
    openFailed: '无法打开系统浏览器，请手动访问：',
  },
  en: {
    title: 'Update local Codex',
    summary: (current: string, minimum: string) =>
      `Current version: ${current || 'unknown'}. ShotForward requires ${minimum || '0.155.1'} or newer.`,
    hint: 'After updating, fully quit and reopen ShotForward, then check the connection again.',
    openGuide: 'View official update guide',
    openFailed: 'The system browser could not be opened. Visit:',
  },
} as const;

export function isLoopbackProxyURL(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  try {
    const parsed = new URL(trimmed);
    if (!['http:', 'socks5:', 'socks5h:'].includes(parsed.protocol)) return false;
    if (parsed.username || parsed.password || !parsed.port || parsed.search || parsed.hash) return false;
    if (parsed.pathname !== '' && parsed.pathname !== '/') return false;
    const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, '');
    return host === 'localhost' || host === '::1' || /^127(?:\.\d{1,3}){3}$/.test(host);
  } catch {
    return false;
  }
}
