import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { agentRuntimeOptions, changeAgentRuntime, refreshAgentRuntime } from '../agentRuntime';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { APIError } from '../api/client';
import { getCloudAccount, type CloudAccount } from '../api/cloud';
import { type AgentRuntimeKind, type RuntimeModel, type RuntimeSnapshot } from '../api/runtime';

export function AgentServiceSettingsPage() {
  const { i18n } = useTranslation();
  const copy = i18n.resolvedLanguage?.startsWith('zh') ? serviceCopy.zh : serviceCopy.en;
  const queryClient = useQueryClient();
  const cloudRuntime = useQuery(agentRuntimeOptions(queryClient, 'shotforward'));
  const localRuntime = useQuery(agentRuntimeOptions(queryClient, 'user_codex'));
  const account = useQuery({ queryKey: ['cloud-account'], queryFn: getCloudAccount, retry: false });
  const selection = useMutation({
    mutationFn: (kind: AgentRuntimeKind) => changeAgentRuntime(queryClient, kind),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['agent-sessions'] });
    },
  });
  const refresh = useMutation({
    mutationFn: async () => {
      const accountOwner = queryClient.getQueryCache().find({ queryKey: ['cloud-account'], exact: true });
      const results = await Promise.allSettled([
        refreshAgentRuntime(queryClient, 'shotforward'),
        refreshAgentRuntime(queryClient, 'user_codex'),
        getCloudAccount(),
      ]);
      const [, , accountResult] = results;
      if (
        accountResult.status === 'fulfilled' &&
        accountOwner === queryClient.getQueryCache().find({ queryKey: ['cloud-account'], exact: true })
      )
        queryClient.setQueryData<CloudAccount>(['cloud-account'], accountResult.value);
      const failed = results.find((result) => result.status === 'rejected');
      if (failed?.status === 'rejected') throw failed.reason;
    },
  });
  const active =
    selection.data?.active_runtime_kind ??
    cloudRuntime.data?.active_runtime_kind ??
    localRuntime.data?.active_runtime_kind ??
    'user_codex';
  const local = localRuntime.data?.runtimes?.user_codex;
  const cloud = cloudRuntime.data?.runtimes?.shotforward;
  const cloudModels = cloudRuntime.data?.runtime_models?.shotforward ?? [];
  const localModels = localRuntime.data?.runtime_models?.user_codex ?? [];
  const cloudReady = account.data?.signed_in === true && cloud?.health === 'ready' && cloudModels.length > 0;
  const localReady = local?.health === 'ready' && localModels.length > 0;
  const switchBlocked =
    selection.error instanceof APIError && selection.error.code === 'runtime_switch_blocked';

  const choose = (kind: AgentRuntimeKind) => {
    if (kind !== active && !selection.isPending) selection.mutate(kind);
  };
  const loading =
    cloudRuntime.isFetching || localRuntime.isFetching || account.isFetching || refresh.isPending;

  return (
    <div className="settings-section agent-service-settings">
      <header className="settings-section__header">
        <div>
          <span className="agent-service-settings__eyebrow">Agent</span>
          <h2>{copy.title}</h2>
          <p>{copy.description}</p>
        </div>
        <button className="button" type="button" disabled={loading} onClick={() => refresh.mutate()}>
          {loading ? copy.refreshing : copy.refresh}
        </button>
      </header>

      {refresh.isError && (
        <div className="message-panel is-error">
          <div>
            <strong>{copy.refreshFailed}</strong>
            <p>{copy.refreshFailedHint}</p>
          </div>
          <button className="button" type="button" onClick={() => refresh.reset()}>
            {copy.dismiss}
          </button>
        </div>
      )}

      <section className="agent-service-picker" role="radiogroup" aria-label={copy.title}>
        <ServiceCard
          kind="shotforward"
          title="ShotForward Agent"
          description={copy.shotForwardDescription}
          active={active === 'shotforward'}
          available={cloudReady}
          snapshot={cloud}
          models={cloudModels}
          modelsLabel={copy.cloudModels}
          noModelsLabel={
            cloudRuntime.isPending || account.isPending
              ? copy.loadingService
              : cloudRuntime.isError || account.isError
                ? copy.loadFailed
                : copy.noCompatibleCloudModels
          }
          status={
            cloudRuntime.isPending || account.isPending
              ? copy.loadingService
              : cloudRuntime.isError || account.isError
                ? copy.serviceUnavailable
                : account.data?.signed_in
                  ? cloudReady
                    ? copy.available
                    : copy.serviceUnavailable
                  : copy.accountRequired
          }
          recommended={copy.recommended}
          configureLabel={copy.manageAccount}
          configureTo="/settings/account"
          busy={selection.isPending}
          onChoose={choose}
        />
        <ServiceCard
          kind="user_codex"
          title={copy.localTitle}
          description={copy.localDescription}
          active={active === 'user_codex'}
          available={localReady}
          snapshot={local}
          models={localModels}
          modelsLabel={copy.localModels}
          noModelsLabel={
            localRuntime.isPending
              ? copy.loadingService
              : localRuntime.isError
                ? copy.loadFailed
                : copy.noModels
          }
          status={
            localRuntime.isPending
              ? copy.loadingService
              : localRuntime.isError
                ? copy.loadFailed
                : localReady
                  ? copy.available
                  : copy.setupRequired
          }
          recommended={undefined}
          configureLabel={copy.configureLocal}
          configureTo="/settings/runtime"
          busy={selection.isPending}
          onChoose={choose}
        />
      </section>

      <div className="agent-service-note">
        <span aria-hidden="true">i</span>
        <div>
          <strong>{copy.singleEndpoint}</strong>
          <p>{copy.historyHint}</p>
        </div>
      </div>

      {selection.isError && (
        <div className="message-panel is-error agent-service-error" role="alert">
          <div>
            <strong>{switchBlocked ? copy.switchBlocked : copy.switchFailed}</strong>
            <p>{switchBlocked ? copy.switchBlockedHint : copy.switchFailedHint}</p>
          </div>
          <button className="button" type="button" onClick={() => selection.reset()}>
            {copy.dismiss}
          </button>
        </div>
      )}
    </div>
  );
}

function ServiceCard({
  kind,
  title,
  description,
  active,
  available,
  snapshot,
  models,
  modelsLabel,
  noModelsLabel,
  status,
  recommended,
  configureLabel,
  configureTo,
  busy,
  onChoose,
}: {
  kind: AgentRuntimeKind;
  title: string;
  description: string;
  active: boolean;
  available: boolean;
  snapshot: RuntimeSnapshot | undefined;
  models: RuntimeModel[];
  modelsLabel: string;
  noModelsLabel: string;
  status: string;
  recommended: string | undefined;
  configureLabel: string;
  configureTo: string;
  busy: boolean;
  onChoose: (kind: AgentRuntimeKind) => void;
}) {
  return (
    <article className={`agent-service-card${active ? ' is-selected' : ''}`}>
      <button
        type="button"
        role="radio"
        aria-checked={active}
        disabled={busy || (!available && !active)}
        onClick={() => onChoose(kind)}
      >
        <span className={`agent-service-card__mark agent-service-card__mark--${kind}`} aria-hidden="true">
          {kind === 'shotforward' ? '✦' : '>_'}
        </span>
        <span className="agent-service-card__body">
          <span className="agent-service-card__heading">
            <strong>{title}</strong>
            {recommended && <em>{recommended}</em>}
          </span>
          <small>{description}</small>
          <span
            className={`agent-service-card__status status-${available ? 'ready' : snapshot?.health || 'unavailable'}`}
          >
            <i className="status-dot" />
            {status}
            {snapshot?.version && kind === 'user_codex' ? ` · ${snapshot.version}` : ''}
          </span>
        </span>
        <span className="agent-service-card__radio" aria-hidden="true" />
      </button>
      <section className="agent-service-card__models" aria-label={modelsLabel}>
        <strong>{modelsLabel}</strong>
        {models.length ? (
          <div>
            {models.map((model) => (
              <code key={model.id}>{model.display_name}</code>
            ))}
          </div>
        ) : (
          <small>{noModelsLabel}</small>
        )}
      </section>
      <footer>
        <span>{active ? '✓' : ''}</span>
        <Link to={configureTo}>{configureLabel} →</Link>
      </footer>
    </article>
  );
}

const serviceCopy = {
  zh: {
    title: 'Agent 服务',
    description: '选择所有项目默认使用的 Agent 服务。两个账号可以同时保持连接，但对话仅使用当前选中的一个。',
    shotForwardDescription: '使用 ShotForward 账号提供的云端 Agent，无需管理本地运行环境。',
    localTitle: '本地 Codex',
    localDescription: '使用这台电脑上已登录的 Codex CLI 与 ChatGPT 订阅。',
    recommended: '推荐',
    available: '已连接，可使用',
    accountRequired: '需要连接 ShotForward 账号',
    serviceUnavailable: '账号已连接，Agent 服务暂不可用',
    setupRequired: '需要配置本地 Codex',
    manageAccount: '管理 ShotForward 账号',
    configureLocal: '配置本地 Codex',
    singleEndpoint: '同一时间只使用一个 Agent 服务',
    historyHint:
      '切换后仅显示所选服务的对话记录；另一服务的历史会完整保留，切换回来即可继续查看。图像和视频生成不受影响。',
    loadFailed: 'Agent 服务状态暂时无法加载',
    loadFailedHint: '请确认客户端后台服务正在运行，然后重试。',
    retry: '重试',
    refresh: '刷新模型',
    refreshing: '刷新中…',
    loadingService: '正在加载此服务…',
    refreshFailed: '部分 Agent 服务刷新失败',
    refreshFailedHint: '已保留成功加载的服务状态；请检查网络或 Codex Runtime 后重试。',
    cloudModels: '云端当前模型',
    localModels: '本机当前模型',
    noModels: '当前没有可用模型',
    noCompatibleCloudModels: '云端模型与当前客户端目录暂无兼容项',
    switchBlocked: '当前有 Agent 任务正在执行',
    switchBlockedHint: '请等待任务完成，或先在对话框中停止当前任务，再切换服务。',
    switchFailed: '无法切换 Agent 服务',
    switchFailedHint: '当前设置未改变，请检查服务状态后重试。',
    dismiss: '知道了',
  },
  en: {
    title: 'Agent service',
    description:
      'Choose the default Agent service for every project. Both accounts can stay connected, while conversations use only the selected service.',
    shotForwardDescription:
      'Use the cloud Agent included with your ShotForward account without managing a local runtime.',
    localTitle: 'Local Codex',
    localDescription: 'Use the Codex CLI and ChatGPT subscription signed in on this computer.',
    recommended: 'Recommended',
    available: 'Connected and ready',
    accountRequired: 'Connect a ShotForward account',
    serviceUnavailable: 'Account connected; Agent is temporarily unavailable',
    setupRequired: 'Set up local Codex',
    manageAccount: 'Manage ShotForward account',
    configureLocal: 'Configure local Codex',
    singleEndpoint: 'Only one Agent service is used at a time',
    historyHint:
      'After switching, only conversations from the selected service are shown. Other history remains intact and returns when you switch back. Image and video generation are unaffected.',
    loadFailed: 'Agent service status could not be loaded',
    loadFailedHint: 'Make sure the desktop background service is running, then retry.',
    retry: 'Retry',
    refresh: 'Refresh models',
    refreshing: 'Refreshing…',
    loadingService: 'Loading this service…',
    refreshFailed: 'Some Agent services could not be refreshed',
    refreshFailedHint:
      'Successfully loaded services were kept. Check the network or Codex Runtime and try again.',
    cloudModels: 'Current cloud models',
    localModels: 'Current local models',
    noModels: 'No models are currently available',
    noCompatibleCloudModels: 'No cloud model is compatible with this client catalog',
    switchBlocked: 'An Agent task is still running',
    switchBlockedHint:
      'Wait for it to finish or stop it in the conversation panel before switching services.',
    switchFailed: 'Could not switch Agent service',
    switchFailedHint: 'The current setting was not changed. Check service status and retry.',
    dismiss: 'Dismiss',
  },
} as const;

export { AgentServiceSettingsPage as Component };
