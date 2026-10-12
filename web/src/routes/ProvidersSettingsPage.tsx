import { FormEvent, useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { APIError } from '../api/client';
import {
  checkProvider,
  listProviders,
  validateAndConfigureProvider,
  type ConfigureProviderInput,
  type ProviderProfile,
} from '../api/providers';
import '../styles/providers-settings.css';

interface ProviderDefinition {
  id: string;
  kind: 'image' | 'video';
  name: string;
  source: string;
  endpoint: string;
  models: string[];
  readOnly?: boolean;
}

const wanGenerationPath = '/api/v1/services/aigc/video-generation/video-synthesis';
const maximumProviderConcurrency = 10;
const wanEndpointPresets = [
  ['alibaba_cn_beijing', 'cn-beijing.maas.aliyuncs.com'],
  ['alibaba_ap_southeast_1', 'ap-southeast-1.maas.aliyuncs.com'],
  ['alibaba_ap_northeast_1', 'ap-northeast-1.maas.aliyuncs.com'],
  ['alibaba_eu_central_1', 'eu-central-1.maas.aliyuncs.com'],
  ['alibaba_us_east_1', 'us-east-1.maas.aliyuncs.com'],
] as const;

export function parseWanEndpoint(value: string): {
  endpointPreset: string;
  workspaceID: string;
} | null {
  let parsed: URL;
  try {
    parsed = new URL(value.trim());
  } catch {
    return null;
  }
  const path = parsed.pathname.replace(/\/$/, '') || '/';
  if (
    parsed.protocol !== 'https:' ||
    parsed.username ||
    parsed.password ||
    parsed.port ||
    parsed.search ||
    parsed.hash ||
    (path !== '/' && path !== wanGenerationPath)
  ) {
    return null;
  }
  for (const [endpointPreset, suffix] of wanEndpointPresets) {
    const ending = `.${suffix}`;
    if (!parsed.hostname.endsWith(ending)) continue;
    const workspaceID = parsed.hostname.slice(0, -ending.length);
    if (!/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,126}[A-Za-z0-9])?$/.test(workspaceID) || workspaceID.includes('.')) {
      return null;
    }
    return { endpointPreset, workspaceID };
  }
  return null;
}

export function wanEndpointForProfile(profile: ProviderProfile | undefined): string {
  if (!profile?.workspace_id) return '';
  const route = wanEndpointPresets.find(([preset]) => preset === profile.endpoint_preset);
  return route ? `https://${profile.workspace_id}.${route[1]}${wanGenerationPath}` : '';
}

const definitions: ProviderDefinition[] = [
  {
    id: 'image.codex',
    kind: 'image',
    name: 'Codex Image',
    source: 'ChatGPT subscription',
    endpoint: 'codex_subscription',
    models: [],
    readOnly: true,
  },
  {
    id: 'image.shotforward',
    kind: 'image',
    name: 'ShotForward Image',
    source: 'ShotForward Cloud',
    endpoint: 'shotforward_cloud',
    models: [],
    readOnly: true,
  },
  {
    id: 'video.h3.shotforward',
    kind: 'video',
    name: 'shotforward/MiniMax-H3',
    source: 'ShotForward Cloud · LoRA',
    endpoint: 'shotforward_cloud',
    models: [],
    readOnly: true,
  },
  {
    id: 'video.seedance-2.5.volcengine-cn',
    kind: 'video',
    name: 'Seedance 2.5',
    source: 'Volcengine China',
    endpoint: 'volcengine_cn_beijing',
    models: [
      'doubao-seedance-2-5-260628',
      'doubao-seedance-2-0-260128',
      'doubao-seedance-2-0-fast-260128',
      'doubao-seedance-2-0-mini-260615',
    ],
  },
  {
    id: 'video.seedance-2.5.byteplus-global',
    kind: 'video',
    name: 'Seedance 2.5',
    source: 'BytePlus Global',
    endpoint: 'byteplus_ap_southeast_1',
    models: [
      'dreamina-seedance-2-5-260628',
      'dreamina-seedance-2-0-260128',
      'dreamina-seedance-2-0-fast-260128',
      'dreamina-seedance-2-0-mini-260615',
    ],
  },
  {
    id: 'video.h3.minimax-cn',
    kind: 'video',
    name: 'MiniMax H3',
    source: 'MiniMax China',
    endpoint: 'minimax_cn',
    models: ['MiniMax-H3', 'MiniMax-H3-Max'],
  },
  {
    id: 'video.h3.official',
    kind: 'video',
    name: 'MiniMax H3',
    source: 'MiniMax Global',
    endpoint: 'minimax_global',
    models: ['MiniMax-H3', 'MiniMax-H3-Max'],
  },
  {
    id: 'video.wan-3.0.official',
    kind: 'video',
    name: 'Wan 3.0',
    source: 'Alibaba Cloud',
    endpoint: 'alibaba_ap_southeast_1',
    models: ['wan3.0-video', 'wan3.0-video-prime'],
  },
];

function ProviderForm({
  definition,
  profile,
}: {
  definition: ProviderDefinition;
  profile: ProviderProfile | undefined;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const isWan = definition.id === 'video.wan-3.0.official';
  const [apiKey, setAPIKey] = useState('');
  const configuredModels =
    profile?.models?.map((item) => item.model_id) ?? ([profile?.model_id].filter(Boolean) as string[]);
  const [models, setModels] = useState<string[]>(
    configuredModels.length ? configuredModels : [definition.models[0]!],
  );
  const [endpoint, setEndpoint] = useState(
    isWan ? wanEndpointForProfile(profile) : profile?.endpoint_preset || definition.endpoint,
  );
  const [endpointError, setEndpointError] = useState(false);
  const [enabled, setEnabled] = useState(profile?.enabled ?? true);
  const [concurrency, setConcurrency] = useState(profile?.concurrency_limit ?? 1);

  useEffect(() => {
    setAPIKey('');
    const savedModels =
      profile?.models?.map((item) => item.model_id) ?? ([profile?.model_id].filter(Boolean) as string[]);
    setModels(savedModels.length ? savedModels : [definition.models[0]!]);
    setEndpoint(isWan ? wanEndpointForProfile(profile) : profile?.endpoint_preset || definition.endpoint);
    setEndpointError(false);
    setEnabled(profile?.enabled ?? true);
    setConcurrency(profile?.concurrency_limit ?? 1);
  }, [definition, profile]);

  const validation = useMutation({
    mutationFn: (input: ConfigureProviderInput) => validateAndConfigureProvider(definition.id, input),
    onSuccess: async () => {
      setAPIKey('');
      await queryClient.invalidateQueries({ queryKey: ['generation-providers'] });
    },
  });
  function configurationInput(): ConfigureProviderInput | null {
    const wanRoute = isWan ? parseWanEndpoint(endpoint) : null;
    if (isWan && !wanRoute) {
      setEndpointError(true);
      return null;
    }
    setEndpointError(false);
    return {
      endpoint_preset: wanRoute?.endpointPreset ?? endpoint,
      model_id: models[0]!,
      models: models.map((modelID, index) => ({ model_id: modelID, is_default: index === 0 })),
      resource_id: '',
      workspace_id: wanRoute?.workspaceID ?? '',
      api_key: apiKey.trim(),
      enabled,
      concurrency_limit: concurrency,
      expected_revision: profile?.revision ?? 0,
    };
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const input = configurationInput();
    if (input) validation.mutate(input);
  }

  if (definition.readOnly) {
    const shotForwardManaged = definition.endpoint === 'shotforward_cloud';
    return (
      <div className="provider-readonly">
        <section className="provider-managed-overview">
          <span className="provider-managed-overview__mark" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <circle cx="8" cy="12" r="3.5" />
              <circle cx="16" cy="12" r="3.5" />
              <path d="M11.5 12h1" />
            </svg>
          </span>
          <div>
            <strong>{shotForwardManaged ? 'ShotForward Cloud' : t('providers.runtimeManagedTitle')}</strong>
            <p>
              {shotForwardManaged
                ? 'Models and credentials are managed by your ShotForward account. Sign in under Codex Runtime settings.'
                : t('providers.codexInherited')}
            </p>
            <div className="provider-managed-overview__facts">
              <span>
                <i aria-hidden="true">✓</i>
                {t('providers.subscriptionManaged')}
              </span>
              <span>
                <i aria-hidden="true">✓</i>
                {t('providers.noAPIKey')}
              </span>
            </div>
          </div>
        </section>
      </div>
    );
  }

  return (
    <form className="provider-form" onSubmit={submit}>
      <div className="provider-form__toolbar">
        <span>{t('providers.configuration')}</span>
        <label className="enabled-control">
          <input
            className="switch"
            type="checkbox"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          {t('providers.enabled')}
        </label>
      </div>
      <div className="form-grid">
        <div className={`field field--wide${isWan ? ' field--endpoint' : ''}`}>
          <span>{t(isWan ? 'providers.apiEndpoint' : 'providers.endpoint')}</span>
          {isWan ? (
            <>
              <input
                type="url"
                value={endpoint}
                onChange={(event) => {
                  setEndpoint(event.target.value);
                  setEndpointError(false);
                }}
                required
                spellCheck={false}
                autoCapitalize="none"
                placeholder="https://llm-….cn-beijing.maas.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis"
                aria-invalid={endpointError}
              />
              <small>{t('providers.wanEndpointHint')}</small>
            </>
          ) : (
            <input value={endpoint} readOnly />
          )}
        </div>
        <label className="field field--wide">
          <span>API Key</span>
          <input
            type="password"
            value={apiKey}
            onChange={(event) => setAPIKey(event.target.value)}
            required={!profile?.credential_configured}
            autoComplete="new-password"
            placeholder={
              profile?.credential_configured
                ? t('providers.credentialSaved')
                : t('providers.apiKeyPlaceholder')
            }
          />
          <small>
            {profile?.credential_configured ? t('providers.leaveBlank') : t('providers.secureStorage')}
          </small>
        </label>
        <div className="field provider-model-field">
          <div className="provider-model-field__heading">
            <span>{t('providers.availableModels')}</span>
            <small>{t('providers.chooseModelsHint')}</small>
          </div>
          <div className="provider-model-list">
            {definition.models.map((modelID) => {
              const checked = models.includes(modelID);
              const status =
                profile?.models?.find((item) => item.model_id === modelID)?.health ?? 'unchecked';
              return (
                <div key={modelID} className={checked ? 'is-enabled' : ''}>
                  <label className="provider-model-toggle">
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={(event) => {
                        if (event.target.checked) {
                          setModels((current) => [...current, modelID]);
                        } else {
                          const next = models.filter((value) => value !== modelID);
                          if (next.length === 0) return;
                          setModels(next);
                        }
                      }}
                    />
                    <code>{modelID}</code>
                  </label>
                  <span className="provider-model-health" title={t(`providers.health.${status}`)}>
                    <i className={`health-dot health-${status}`} />
                    {t(`providers.health.${status}`)}
                  </span>
                </div>
              );
            })}
          </div>
        </div>
        <div className="field provider-concurrency-field">
          <span>{t('providers.concurrency')}</span>
          <div className="provider-number-stepper">
            <button
              type="button"
              aria-label={t('providers.decreaseConcurrency')}
              disabled={concurrency <= 1}
              onClick={() => setConcurrency((current) => Math.max(1, current - 1))}
            >
              −
            </button>
            <input
              type="number"
              aria-label={t('providers.concurrency')}
              min={1}
              max={maximumProviderConcurrency}
              step={1}
              inputMode="numeric"
              value={concurrency}
              onChange={(event) => {
                const value = event.currentTarget.valueAsNumber;
                if (Number.isInteger(value)) setConcurrency(value);
              }}
            />
            <button
              type="button"
              aria-label={t('providers.increaseConcurrency')}
              disabled={concurrency >= maximumProviderConcurrency}
              onClick={() => setConcurrency((current) => Math.min(maximumProviderConcurrency, current + 1))}
            >
              +
            </button>
          </div>
          <small>{t('providers.concurrencyRange')}</small>
        </div>
      </div>
      {endpointError && (
        <p className="form-error" role="alert">
          {t('providers.wanEndpointInvalid')}
        </p>
      )}
      {validation.isError && (
        <p className="form-error" role="alert">
          {validation.error instanceof APIError && validation.error.code === 'credential_store_unavailable'
            ? t('providers.credentialStoreUnavailable')
            : validation.error instanceof APIError && validation.error.code === 'provider_validation_failed'
              ? t('providers.validationFailed')
              : t('providers.operationFailed')}
        </p>
      )}
      <footer className="provider-actions">
        <p>{t('providers.validateAndSaveHint')}</p>
        <button className="button button--primary" disabled={validation.isPending}>
          {validation.isPending ? t('providers.checking') : t('providers.validateAndSave')}
        </button>
      </footer>
    </form>
  );
}

export function ProvidersSettingsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const providers = useQuery({
    queryKey: ['generation-providers'],
    queryFn: listProviders,
    retry: 1,
    refetchInterval: (query) =>
      query.state.data?.providers.some(
        (profile) => profile.generator_id === 'image.codex' && profile.health === 'unchecked',
      )
        ? 1_000
        : false,
  });
  const [selectedID, setSelectedID] = useState(definitions[0]!.id);
  const [search, setSearch] = useState('');
  const selected = definitions.find((definition) => definition.id === selectedID) ?? definitions[0]!;
  const profileByID = useMemo(
    () => new Map(providers.data?.providers.map((profile) => [profile.generator_id, profile])),
    [providers.data],
  );
  const visible = definitions.filter((definition) =>
    `${definition.name} ${definition.source}`.toLowerCase().includes(search.toLowerCase()),
  );
  const check = useMutation({
    mutationFn: (generatorID: string) => checkProvider(generatorID),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['generation-providers'] });
    },
  });
  const selectedProfile = profileByID.get(selected.id);
  const selectedHealth = selectedProfile?.health ?? 'unchecked';

  return (
    <div className="providers-section">
      <header>
        <h2>{t('settings.providers')}</h2>
        <p>{t('providers.description')}</p>
      </header>
      {providers.isPending ? (
        <div className="settings-loading skeleton" />
      ) : providers.isError ? (
        <div className="message-panel is-error">
          <strong>{t('providers.loadFailed')}</strong>
          <button className="button" onClick={() => void providers.refetch()}>
            {t('common.retry')}
          </button>
        </div>
      ) : (
        <div className="provider-workspace">
          <aside className="provider-directory">
            <input
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('providers.search')}
            />
            {(['image', 'video'] as const).map((kind) => (
              <section key={kind}>
                <h3>{t(`providers.${kind}`)}</h3>
                {visible
                  .filter((item) => item.kind === kind)
                  .map((definition) => {
                    const profile = profileByID.get(definition.id);
                    return (
                      <button
                        key={definition.id}
                        className={selectedID === definition.id ? 'is-selected' : ''}
                        onClick={() => setSelectedID(definition.id)}
                      >
                        <span>
                          <strong>{definition.name}</strong>
                          <small>{definition.source}</small>
                        </span>
                        <i className={`health-dot health-${profile?.health ?? 'unchecked'}`} />
                      </button>
                    );
                  })}
              </section>
            ))}
          </aside>
          <section className="provider-detail">
            <header className="provider-detail__header">
              <div className="provider-detail__identity">
                <span>
                  {t(selected.readOnly ? 'providers.managedService' : `providers.${selected.kind}`)}
                </span>
                <h3>{selected.name}</h3>
                <p>{selected.source}</p>
                {selected.id === 'video.h3.shotforward' && <p>{t('storyboard.shotforwardH3Description')}</p>}
              </div>
              <div className="provider-detail__actions">
                <span className="provider-status-chip">
                  <i className={`health-dot health-${selectedHealth}`} />
                  {t(`providers.health.${selectedHealth}`)}
                </span>
                {selected.readOnly && (
                  <button
                    type="button"
                    className="button provider-detail__check"
                    onClick={() => check.mutate(selected.id)}
                    disabled={check.isPending}
                  >
                    <svg viewBox="0 0 20 20" aria-hidden="true">
                      <path d="M15.4 7.2A6 6 0 1 0 16 11" />
                      <path d="m15.4 3.8.1 3.6-3.6-.1" />
                    </svg>
                    {check.isPending ? t('providers.checking') : t('providers.check')}
                  </button>
                )}
              </div>
            </header>
            <div className="provider-detail__content">
              <div className={`provider-editor${selected.readOnly ? ' provider-editor--readonly' : ''}`}>
                <ProviderForm key={selected.id} definition={selected} profile={selectedProfile} />
              </div>
              <aside className="provider-summary">
                <header className="provider-summary__header">
                  <h3>{t('providers.connectionDetails')}</h3>
                </header>
                <dl>
                  <div>
                    <dt>{t('providers.credential')}</dt>
                    <dd>
                      {selected.readOnly
                        ? t('providers.inherited')
                        : selectedProfile?.credential_configured
                          ? t('providers.configured')
                          : t('providers.notConfigured')}
                    </dd>
                  </div>
                  <div>
                    <dt>{t('providers.lastChecked')}</dt>
                    <dd>
                      {selectedProfile?.last_checked_at
                        ? new Intl.DateTimeFormat(undefined, {
                            dateStyle: 'medium',
                            timeStyle: 'short',
                          }).format(new Date(selectedProfile.last_checked_at))
                        : '—'}
                    </dd>
                  </div>
                </dl>
                <p>{t('providers.checkHint')}</p>
              </aside>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
