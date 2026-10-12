import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { defaultAgentTemplates, renderAgentTemplate, type AgentTemplateKey } from './agentTemplates';
import { getAgentTemplateSettings } from './api/agentTemplates';
import type { SupportedLanguage } from './i18n';

export function useAgentTemplates() {
  const { i18n } = useTranslation();
  const initialLocale: SupportedLanguage = i18n.language === 'en' ? 'en' : 'zh-CN';
  const query = useQuery({
    queryKey: ['agent-templates'],
    queryFn: () => getAgentTemplateSettings(initialLocale),
    staleTime: Number.POSITIVE_INFINITY,
  });
  const locale = query.data?.locale ?? initialLocale;

  return {
    query,
    locale,
    render(key: AgentTemplateKey, variables: Record<string, string | number> = {}) {
      const template = query.data?.overrides[key] ?? defaultAgentTemplates[locale][key];
      return renderAgentTemplate(template, variables);
    },
  };
}
