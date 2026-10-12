import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { setAgentWebSearch, useAgentWebSearchPreference } from '../preferences';
import { saveTextFile } from '../desktop';

export function PrivacySettingsPage() {
  const { t } = useTranslation();
  const webSearch = useAgentWebSearchPreference();
  const exportDiagnostics = () => {
    const content = JSON.stringify(
      {
        exported_at: new Date().toISOString(),
        language: document.documentElement.lang,
        agent_web_search_enabled: webSearch,
        anonymous_diagnostics_enabled: false,
      },
      null,
      2,
    );
    void saveTextFile('shotforward-diagnostics.json', content).catch(() => undefined);
  };
  return (
    <div className="settings-section privacy-section">
      <header>
        <h2>{t('settings.privacy')}</h2>
        <p>{t('privacy.description')}</p>
      </header>
      <div className="privacy-assurance">
        <span>✓</span>
        <div>
          <strong>{t('privacy.assuranceTitle')}</strong>
          <p>{t('privacy.assuranceHint')}</p>
        </div>
      </div>
      <SettingGroup title={t('privacy.dataStorage')}>
        <div className="setting-row">
          <span>
            <strong>{t('privacy.localData')}</strong>
            <small>{t('privacy.localDataHint')}</small>
          </span>
          <em className="status-value is-ready">● {t('privacy.local')}</em>
        </div>
        <div className="setting-row">
          <span>
            <strong>{t('privacy.credentials')}</strong>
            <small>{t('privacy.credentialsHint')}</small>
          </span>
          <em className="status-value is-ready">● {t('privacy.systemVault')}</em>
        </div>
      </SettingGroup>
      <SettingGroup title={t('privacy.externalServices')}>
        <div className="privacy-service-head">
          <span>{t('privacy.service')}</span>
          <span>{t('privacy.sentData')}</span>
          <span>{t('privacy.whenSent')}</span>
          <span>{t('privacy.status')}</span>
        </div>
        <div className="privacy-service-row">
          <strong>Codex Runtime</strong>
          <span>{t('privacy.scriptContext')}</span>
          <span>{t('privacy.onRequest')}</span>
          <em>● {t('privacy.available')}</em>
        </div>
        <div className="privacy-service-row">
          <strong>{t('settings.providers')}</strong>
          <span>{t('privacy.generationContext')}</span>
          <span>{t('privacy.perTask')}</span>
          <Link to="/settings/providers">{t('privacy.manage')}</Link>
        </div>
        <label className="setting-row">
          <span>
            <strong>{t('privacy.webSearch')}</strong>
            <small>{t('privacy.webSearchHint')}</small>
          </span>
          <input
            className="switch"
            type="checkbox"
            checked={webSearch}
            onChange={(event) => setAgentWebSearch(event.target.checked)}
          />
        </label>
      </SettingGroup>
      <SettingGroup title={t('privacy.diagnostics')}>
        <label className="setting-row">
          <span>
            <strong>{t('privacy.shareDiagnostics')}</strong>
            <small>{t('privacy.diagnosticsHint')}</small>
          </span>
          <input className="switch" type="checkbox" checked={false} disabled />
        </label>
        <div className="setting-row">
          <span>
            <strong>{t('privacy.diagnosticExport')}</strong>
            <small>{t('privacy.diagnosticExportHint')}</small>
          </span>
          <button className="button" onClick={exportDiagnostics}>
            {t('privacy.export')}
          </button>
        </div>
      </SettingGroup>
    </div>
  );
}

function SettingGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="setting-group">
      <h3>{title}</h3>
      <div className="settings-list">{children}</div>
    </section>
  );
}
