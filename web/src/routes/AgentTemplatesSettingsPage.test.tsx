import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import i18n from '../i18n';
import { AgentTemplatesSettingsPage } from './AgentTemplatesSettingsPage';

describe('AgentTemplatesSettingsPage', () => {
  beforeEach(async () => {
    vi.restoreAllMocks();
    await i18n.changeLanguage('zh-CN');
  });

  it('loads the first-use language and saves only customized templates', async () => {
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        requests.push(init ? { url, init } : { url });
        if (init?.method === 'PUT') {
          const body = JSON.parse(String(init.body)) as {
            overrides: Record<string, string>;
            expected_revision: number;
          };
          return json({ locale: 'zh-CN', overrides: body.overrides, revision: 2 });
        }
        return json({ locale: 'zh-CN', overrides: {}, revision: 1 });
      }),
    );

    renderPage();
    const editor = await screen.findByRole('textbox', { name: '编写视频提示词' });
    fireEvent.change(editor, { target: { value: '我的视频模板 {{clip_id}}' } });
    fireEvent.click(screen.getByRole('button', { name: '保存模板' }));

    await waitFor(() => expect(screen.getByText('已保存')).toBeInTheDocument());
    expect(requests[0]?.url).toContain('initial_locale=zh-CN');
    const saveRequest = requests.find((request) => request.init?.method === 'PUT');
    expect(JSON.parse(String(saveRequest?.init?.body))).toEqual({
      overrides: { 'prompt.video': '我的视频模板 {{clip_id}}' },
      expected_revision: 1,
    });
  });
});

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AgentTemplatesSettingsPage />
    </QueryClientProvider>,
  );
}

function json(value: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(value), {
      status,
      headers: { 'Content-Type': 'application/json' },
    }),
  );
}
