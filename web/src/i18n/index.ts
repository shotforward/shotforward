import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en';
import zhCN from './locales/zh-CN';

export const supportedLanguages = ['zh-CN', 'en'] as const;
export type SupportedLanguage = (typeof supportedLanguages)[number];

function initialLanguage(): SupportedLanguage {
  const saved = window.localStorage.getItem('shotforward.language');
  if (saved === 'en' || saved === 'zh-CN') return saved;
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en';
}

void i18n.use(initReactI18next).init({
  resources: {
    'zh-CN': { translation: zhCN },
    en: { translation: en },
  },
  lng: initialLanguage(),
  fallbackLng: 'en',
  showSupportNotice: false,
  interpolation: { escapeValue: false },
  returnNull: false,
});

i18n.on('languageChanged', (language) => {
  const next: SupportedLanguage = language === 'en' ? 'en' : 'zh-CN';
  document.documentElement.lang = next;
  window.localStorage.setItem('shotforward.language', next);
});

document.documentElement.lang = i18n.language === 'en' ? 'en' : 'zh-CN';

export default i18n;
