import { NavLink, Outlet } from 'react-router';
import { useTranslation } from 'react-i18next';
import '../styles/settings-layout.css';
import '../styles/settings-polish.css';

export function SettingsLayout() {
  const { t, i18n } = useTranslation();
  return (
    <div className="settings-page">
      <aside className="settings-nav">
        <header>
          <p className="eyebrow">ShotForward Studio</p>
          <h1>{t('settings.title')}</h1>
        </header>
        <nav aria-label={t('settings.title')}>
          <NavLink to="/settings" end>
            {t('settings.general')}
          </NavLink>
          <NavLink to="/settings/agent">
            {i18n.resolvedLanguage?.startsWith('zh') ? 'Agent 服务' : 'Agent service'}
          </NavLink>
          <NavLink to="/settings/account">
            {i18n.resolvedLanguage?.startsWith('zh') ? 'ShotForward 账号' : 'ShotForward account'}
          </NavLink>
          <NavLink to="/settings/runtime">{t('settings.runtime')}</NavLink>
          <NavLink to="/settings/agent-templates">{t('settings.agentTemplates')}</NavLink>
          <NavLink to="/settings/providers">{t('settings.providers')}</NavLink>
          <NavLink to="/settings/privacy">{t('settings.privacy')}</NavLink>
        </nav>
      </aside>
      <main className="settings-content">
        <Outlet />
      </main>
    </div>
  );
}
