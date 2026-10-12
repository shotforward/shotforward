import type { SupportedLanguage } from '../i18n';
import type { AgentTemplateKey } from '../agentTemplates';
import { requestJSON } from './client';

export interface AgentTemplateSettings {
  locale: SupportedLanguage;
  overrides: Partial<Record<AgentTemplateKey, string>>;
  revision: number;
}

export function getAgentTemplateSettings(initialLocale: SupportedLanguage) {
  return requestJSON<AgentTemplateSettings>(
    `/api/v1/agent-templates?initial_locale=${encodeURIComponent(initialLocale)}`,
  );
}

export function saveAgentTemplateSettings(
  overrides: Partial<Record<AgentTemplateKey, string>>,
  expectedRevision: number,
) {
  return requestJSON<AgentTemplateSettings>('/api/v1/agent-templates', {
    method: 'PUT',
    body: JSON.stringify({ overrides, expected_revision: expectedRevision }),
  });
}
