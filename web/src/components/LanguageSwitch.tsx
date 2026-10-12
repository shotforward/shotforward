import { useTranslation } from 'react-i18next';
import type { SupportedLanguage } from '../i18n';
import { Select } from './Select';

export function LanguageSwitch() {
  const { i18n, t } = useTranslation();
  const language: SupportedLanguage = i18n.language === 'en' ? 'en' : 'zh-CN';

  return (
    <div className="language-switch">
      <span className="sr-only">{t('common.language')}</span>
      <Select
        value={language}
        options={[
          { value: 'zh-CN', label: '中文' },
          { value: 'en', label: 'English' },
        ]}
        compact
        ariaLabel={t('common.language')}
        onChange={(next) => void i18n.changeLanguage(next)}
      />
    </div>
  );
}
