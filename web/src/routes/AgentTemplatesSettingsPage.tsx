import { useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { agentTemplateDefinitions, defaultAgentTemplates, type AgentTemplateKey } from '../agentTemplates';
import { saveAgentTemplateSettings } from '../api/agentTemplates';
import { useAgentTemplates } from '../useAgentTemplates';
import '../styles/agent-templates-settings.css';

type TemplateDrafts = Record<AgentTemplateKey, string>;

export function AgentTemplatesSettingsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { query, locale } = useAgentTemplates();
  const [drafts, setDrafts] = useState<TemplateDrafts>(() => ({ ...defaultAgentTemplates[locale] }));
  const [saved, setSaved] = useState(false);
  const loadedRevision = useRef(0);

  useEffect(() => {
    if (!query.data || loadedRevision.current !== 0) return;
    loadedRevision.current = query.data.revision;
    setDrafts(resolveDrafts(query.data.locale, query.data.overrides));
  }, [query.data]);

  const save = useMutation({
    mutationFn: (next: TemplateDrafts) =>
      saveAgentTemplateSettings(toOverrides(next, locale), query.data?.revision ?? 0),
    onSuccess: (result) => {
      loadedRevision.current = result.revision;
      queryClient.setQueryData(['agent-templates'], result);
      setSaved(true);
      window.setTimeout(() => setSaved(false), 1800);
    },
  });
  const reset = useMutation({
    mutationFn: (key?: AgentTemplateKey) => {
      const overrides = { ...(query.data?.overrides ?? {}) };
      if (key) delete overrides[key];
      else Object.keys(overrides).forEach((item) => delete overrides[item as AgentTemplateKey]);
      return saveAgentTemplateSettings(overrides, query.data?.revision ?? 0);
    },
    onSuccess: (result, key) => {
      loadedRevision.current = result.revision;
      queryClient.setQueryData(['agent-templates'], result);
      setDrafts((current) => {
        if (!key) return { ...defaultAgentTemplates[result.locale] };
        return { ...current, [key]: defaultAgentTemplates[result.locale][key] };
      });
    },
  });

  const groups = useMemo(() => ['script', 'assets', 'storyboard', 'prompts'] as const, []);
  const dirty = query.data ? !sameOverrides(toOverrides(drafts, locale), query.data.overrides) : false;

  return (
    <div className="settings-section agent-template-settings">
      <header className="settings-section__header">
        <div>
          <h2>{t('agentTemplates.title')}</h2>
          <p>{t('agentTemplates.description')}</p>
        </div>
        <button
          type="button"
          className="button"
          disabled={!query.data || reset.isPending || save.isPending}
          onClick={() => reset.mutate(undefined)}
        >
          {t('agentTemplates.restoreAll')}
        </button>
      </header>

      <div className="agent-template-locale">
        <span>{t('agentTemplates.templateLanguage')}</span>
        <strong>{locale === 'zh-CN' ? '简体中文' : 'English'}</strong>
        <small>{t('agentTemplates.languageHint')}</small>
      </div>

      {query.isPending ? (
        <div className="settings-loading skeleton" />
      ) : query.isError ? (
        <div className="message-panel is-error">
          <strong>{t('agentTemplates.loadFailed')}</strong>
          <button className="button" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </button>
        </div>
      ) : (
        <div className="agent-template-groups">
          {groups.map((group) => (
            <section className="agent-template-group" key={group}>
              <h3>{t(`agentTemplates.groups.${group}`)}</h3>
              {agentTemplateDefinitions
                .filter((definition) => definition.group === group)
                .map((definition) => {
                  const customized = Boolean(query.data?.overrides[definition.key]);
                  return (
                    <article className="agent-template-card" key={definition.key}>
                      <header>
                        <div>
                          <strong>
                            {t(`agentTemplates.items.${templateTranslationKey(definition.key)}.title`)}
                          </strong>
                          <small>
                            {t(`agentTemplates.items.${templateTranslationKey(definition.key)}.hint`)}
                          </small>
                        </div>
                        <span className={customized ? 'is-customized' : ''}>
                          {t(customized ? 'agentTemplates.customized' : 'agentTemplates.default')}
                        </span>
                      </header>
                      {definition.variables.length > 0 && (
                        <div className="agent-template-variables" aria-label={t('agentTemplates.variables')}>
                          <span>{t('agentTemplates.variables')}</span>
                          {definition.variables.map((variable) => (
                            <code key={variable}>{`{{${variable}}}`}</code>
                          ))}
                        </div>
                      )}
                      <textarea
                        value={drafts[definition.key]}
                        aria-label={t(`agentTemplates.items.${templateTranslationKey(definition.key)}.title`)}
                        onChange={(event) => {
                          setSaved(false);
                          setDrafts((current) => ({ ...current, [definition.key]: event.target.value }));
                        }}
                      />
                      <footer>
                        <small>
                          {drafts[definition.key].length.toLocaleString()} {t('agentTemplates.characters')}
                        </small>
                        <button
                          type="button"
                          className="button button--text"
                          disabled={reset.isPending || save.isPending}
                          onClick={() => reset.mutate(definition.key)}
                        >
                          {t('agentTemplates.restore')}
                        </button>
                      </footer>
                    </article>
                  );
                })}
            </section>
          ))}
        </div>
      )}

      {!query.isPending && !query.isError && (
        <footer className="agent-template-savebar">
          <span>
            {save.isError || reset.isError
              ? t('agentTemplates.saveFailed')
              : saved
                ? t('agentTemplates.saved')
                : dirty
                  ? t('agentTemplates.unsaved')
                  : t('agentTemplates.noChanges')}
          </span>
          <button
            type="button"
            className="button button--primary"
            disabled={
              !dirty ||
              save.isPending ||
              reset.isPending ||
              Object.values(drafts).some((value) => !value.trim())
            }
            onClick={() => save.mutate(drafts)}
          >
            {save.isPending ? t('agentTemplates.saving') : t('agentTemplates.save')}
          </button>
        </footer>
      )}
    </div>
  );
}

function resolveDrafts(
  locale: 'zh-CN' | 'en',
  overrides: Partial<Record<AgentTemplateKey, string>>,
): TemplateDrafts {
  return Object.fromEntries(
    agentTemplateDefinitions.map(({ key }) => [key, overrides[key] ?? defaultAgentTemplates[locale][key]]),
  ) as TemplateDrafts;
}

function toOverrides(drafts: TemplateDrafts, locale: 'zh-CN' | 'en') {
  return Object.fromEntries(
    agentTemplateDefinitions.flatMap(({ key }) =>
      drafts[key] === defaultAgentTemplates[locale][key] ? [] : [[key, drafts[key]]],
    ),
  ) as Partial<Record<AgentTemplateKey, string>>;
}

function sameOverrides(
  left: Partial<Record<AgentTemplateKey, string>>,
  right: Partial<Record<AgentTemplateKey, string>>,
) {
  return agentTemplateDefinitions.every(({ key }) => left[key] === right[key]);
}

function templateTranslationKey(key: AgentTemplateKey) {
  return key.replaceAll('.', '_');
}
