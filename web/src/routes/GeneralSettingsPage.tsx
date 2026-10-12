import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { SupportedLanguage } from '../i18n';
import {
  defaultThemePreference,
  setTaskNotifications,
  setThemePreference,
  useTaskNotificationPreference,
  useThemePreference,
} from '../preferences';
import { Select } from '../components/Select';

function useLocalChoice(key: string, fallback: string) {
  const [value, setValue] = useState(() => window.localStorage.getItem(key) ?? fallback);
  const update = (next: string) => {
    window.localStorage.setItem(key, next);
    setValue(next);
  };
  return [value, update] as const;
}

export function GeneralSettingsPage() {
  const { t, i18n } = useTranslation();
  const language: SupportedLanguage = i18n.language === 'en' ? 'en' : 'zh-CN';
  const taskNotifications = useTaskNotificationPreference();
  const theme = useThemePreference();
  const [startPage, setStartPage] = useLocalChoice('shotforward.start-page', 'projects');
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  return (
    <div className="settings-section">
      <header>
        <h2>{t('settings.general')}</h2>
        <p>{t('settings.generalDescription')}</p>
      </header>
      <SettingGroup title={t('settings.languageAppearance')}>
        <div className="setting-row">
          <span>
            <strong>{t('settings.interfaceLanguage')}</strong>
            <small>{t('settings.interfaceLanguageHint')}</small>
          </span>
          <Select
            ariaLabel={t('settings.interfaceLanguage')}
            value={language}
            options={[
              { value: 'zh-CN', label: '简体中文' },
              { value: 'en', label: 'English' },
            ]}
            onChange={(next) => void i18n.changeLanguage(next)}
          />
        </div>
        <div className="setting-row">
          <span>
            <strong>{t('settings.appearance')}</strong>
            <small>{t('settings.appearanceHint')}</small>
          </span>
          <Select
            ariaLabel={t('settings.appearance')}
            className="theme-select"
            value={theme}
            options={[
              {
                value: 'brand',
                label: t('settings.themeBrand'),
              },
              {
                value: 'mist',
                label: t('settings.themeMist'),
              },
              {
                value: 'sand',
                label: t('settings.themeSand'),
              },
            ]}
            onChange={setThemePreference}
          />
        </div>
      </SettingGroup>
      <SettingGroup title={t('settings.startup')}>
        <div className="setting-row">
          <span>
            <strong>{t('settings.openAtLaunch')}</strong>
            <small>{t('settings.openAtLaunchHint')}</small>
          </span>
          <Select
            ariaLabel={t('settings.openAtLaunch')}
            value={startPage}
            options={[
              { value: 'projects', label: t('nav.projects') },
              { value: 'last', label: t('settings.lastWorkspace') },
            ]}
            onChange={setStartPage}
          />
        </div>
        <label className="setting-row">
          <span>
            <strong>{t('settings.rememberWindow')}</strong>
            <small>{t('settings.rememberWindowHint')}</small>
          </span>
          <input className="switch" type="checkbox" checked disabled />
        </label>
      </SettingGroup>
      <SettingGroup title={t('settings.notifications')}>
        <label className="setting-row">
          <span>
            <strong>{t('settings.taskNotifications')}</strong>
            <small>{t('settings.taskNotificationsHint')}</small>
          </span>
          <input
            className="switch"
            type="checkbox"
            checked={taskNotifications}
            onChange={(event) => void setTaskNotifications(event.target.checked)}
          />
        </label>
        <label className="setting-row">
          <span>
            <strong>{t('settings.notificationSound')}</strong>
            <small>{t('settings.notificationSoundHint')}</small>
          </span>
          <input className="switch" type="checkbox" checked={false} disabled />
        </label>
      </SettingGroup>
      <SettingGroup title={t('settings.keyboard')}>
        <div className="setting-row">
          <span>
            <strong>{t('settings.shortcuts')}</strong>
            <small>{t('settings.shortcutsHint')}</small>
          </span>
          <button className="button" onClick={() => setShortcutsOpen((open) => !open)}>
            {t('settings.viewShortcuts')}
          </button>
        </div>
        {shortcutsOpen && (
          <div className="shortcut-panel">
            <kbd>⌘ / Ctrl</kbd>
            <span>+</span>
            <kbd>S</kbd>
            <strong>{t('settings.saveShortcut')}</strong>
            <kbd>Esc</kbd>
            <strong>{t('settings.closeShortcut')}</strong>
          </div>
        )}
      </SettingGroup>
      <footer className="settings-reset">
        <p>{t('settings.resetHint')}</p>
        <button
          className="button"
          onClick={() => {
            window.localStorage.removeItem('shotforward.start-page');
            setThemePreference(defaultThemePreference);
            setStartPage('projects');
          }}
        >
          {t('settings.reset')}
        </button>
      </footer>
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
