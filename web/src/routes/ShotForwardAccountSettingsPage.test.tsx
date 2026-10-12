import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import i18n from '../i18n';
import { RuntimeSettingsPage } from './RuntimeSettingsPage';
import { ShotForwardAccountSettingsPage } from './ShotForwardAccountSettingsPage';

describe('ShotForward account settings', () => {
  beforeEach(async () => {
    vi.restoreAllMocks();
    delete window.__SHOTFORWARD_DESKTOP__;
    await i18n.changeLanguage('zh-CN');
  });

  it('opens the account center in the system browser and offers a copy fallback', async () => {
    const openExternal = vi.fn().mockRejectedValue(new Error('browser unavailable'));
    const writeText = vi.fn().mockResolvedValue(undefined);
    window.__SHOTFORWARD_DESKTOP__ = { selectProjectParent: vi.fn(), openExternal };
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        if (String(input).includes('/api/v1/cloud/account')) return json(account(false));
        return Promise.reject(new Error(`Unexpected request: ${String(input)}`));
      }),
    );

    renderPage(<ShotForwardAccountSettingsPage />);
    const link = await screen.findByRole('link', { name: '获取一次性登录码' });
    fireEvent.click(link);

    expect(openExternal).toHaveBeenCalledWith('https://cloud.example/app/account');
    expect(await screen.findByText('无法打开系统浏览器')).toBeInTheDocument();
    expect(screen.getByText('https://cloud.example/app/account')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '复制网址' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://cloud.example/app/account'));
    expect(screen.getByRole('button', { name: '已复制' })).toBeInTheDocument();
  });

  it('owns cloud sign-in for Agent, image, and video services', async () => {
    let signedIn = false;
    let exchangeBody: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/cloud/auth/exchanges')) {
          exchangeBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
          signedIn = true;
          return json(account(true));
        }
        if (url.includes('/api/v1/cloud/account')) return json(account(signedIn));
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<ShotForwardAccountSettingsPage />);

    expect(await screen.findByRole('heading', { name: 'ShotForward 账号' })).toBeInTheDocument();
    expect(screen.getByText('Agent')).toBeInTheDocument();
    expect(screen.getByText('图像生成')).toBeInTheDocument();
    expect(screen.getByText('视频生成')).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: '获取一次性登录码' })).toHaveAttribute(
      'href',
      'https://cloud.example/app/account',
    );

    const code = await screen.findByLabelText('一次性登录码');
    const signInButton = screen.getByRole('button', { name: '登录 ShotForward' });
    expect(signInButton).toHaveClass('cloud-sign-in__submit');
    expect(signInButton).toBeDisabled();
    fireEvent.change(code, { target: { value: 'abc123-cloud' } });
    expect(code).toHaveValue('ABC123-CLOUD');
    expect(signInButton).toBeEnabled();
    fireEvent.click(signInButton);

    await waitFor(() => expect(exchangeBody).toEqual({ code: 'ABC123-CLOUD' }));
    expect(await screen.findAllByText('creator@shotforward.com')).toHaveLength(2);
    expect(screen.getByText('120.125')).toBeInTheDocument();
    expect(screen.getByText('8.5')).toBeInTheDocument();
  });

  it('keeps an invalid cloud login code on the account page with a specific error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/cloud/auth/exchanges')) {
          return json(
            {
              error: {
                code: 'invalid_desktop_code',
                message: 'ShotForward desktop login code is invalid or expired',
              },
            },
            401,
          );
        }
        if (url.includes('/api/v1/cloud/account')) return json(account(false));
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<ShotForwardAccountSettingsPage />);
    fireEvent.change(await screen.findByLabelText('一次性登录码'), { target: { value: 'INVALID-CODE' } });
    fireEvent.click(screen.getByRole('button', { name: '登录 ShotForward' }));

    expect(await screen.findByText('登录码无效、已过期或已使用，请重新生成后重试。')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'ShotForward 账号' })).toBeInTheDocument();
    expect(screen.queryByLabelText('配对码')).not.toBeInTheDocument();
  });

  it('lets the user clear an unreadable cloud session and reconnect', async () => {
    let reset = false;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/cloud/auth/reset')) {
          reset = true;
          return Promise.resolve(new Response(null, { status: 204 }));
        }
        if (url.includes('/api/v1/cloud/account')) {
          if (reset) return json(account(false));
          return json({ error: { code: 'cloud_unavailable', message: 'refresh failed' } }, 502);
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<ShotForwardAccountSettingsPage />);

    expect(await screen.findByText('ShotForward 账号状态暂时无法加载。')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '重新绑定账号' }));
    expect(await screen.findByRole('link', { name: '获取一次性登录码' })).toBeInTheDocument();
  });

  it('keeps ShotForward cloud models out of the local Codex Runtime page', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/agent/runtime/network')) {
        return json({ network: { proxy_url: '' } });
      }
      if (url.includes('/api/v1/agent/runtime')) {
        return json({
          runtime: {
            health: 'ready',
            version: '1.2.3',
            failure_summary: '',
            safe_execution_available: true,
            safe_execution_backend: 'codex_linux_sandbox',
          },
          models: [
            { id: 'gpt-local', display_name: 'Local model', runtime_kind: 'user_codex' },
            { id: 'gpt-cloud', display_name: 'Cloud model', runtime_kind: 'shotforward' },
          ],
        });
      }
      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);

    renderPage(<RuntimeSettingsPage />);

    expect(await screen.findByText('Local model')).toBeInTheDocument();
    expect(screen.queryByText('Cloud model')).not.toBeInTheDocument();
    expect(screen.getByText('安全代码执行').nextElementSibling).toHaveTextContent('可用');
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/cloud/account'))).toBe(false);
  });

  it('prompts users to update an incompatible local Codex version', async () => {
    const openExternal = vi.fn().mockResolvedValue(undefined);
    window.__SHOTFORWARD_DESKTOP__ = { selectProjectParent: vi.fn(), openExternal };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime/network')) return json({ network: { proxy_url: '' } });
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: {
              health: 'incompatible',
              version: '0.146.0',
              minimum_version: '0.155.1',
              failure_code: 'codex_upgrade_required',
              failure_summary: 'Update local Codex',
            },
            models: [],
          });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<RuntimeSettingsPage />);

    expect(await screen.findByText('请升级本机 Codex')).toBeInTheDocument();
    expect(screen.getByText(/当前版本 0\.146\.0.*最低要求 0\.155\.1/)).toBeInTheDocument();
    expect(screen.getByText('curl -fsSL https://chatgpt.com/codex/install.sh | sh')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: '查看官方升级说明' }));
    await waitFor(() => expect(openExternal).toHaveBeenCalledWith('https://developers.openai.com/codex/cli'));
  });

  it('validates and saves a loopback proxy before restarting Codex', async () => {
    let networkBody: Record<string, unknown> | undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime/network')) {
          if (init?.method === 'PUT') {
            networkBody = JSON.parse(String(init.body)) as Record<string, unknown>;
            return json({ network: networkBody });
          }
          return json({ network: { proxy_url: '' } });
        }
        if (url.includes('/api/v1/agent/runtime')) {
          return json({
            runtime: { health: 'ready', version: '1.2.3', failure_summary: '' },
            models: [],
            network: { proxy_url: '' },
          });
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<RuntimeSettingsPage />);
    const proxy = await screen.findByLabelText(/本机代理地址/);
    const save = screen.getByRole('button', { name: '保存并应用' });
    fireEvent.change(proxy, { target: { value: 'http://proxy.example.com:7890' } });
    expect(screen.getByText(/localhost、127\.0\.0\.1/)).toBeInTheDocument();
    expect(save).toBeDisabled();

    fireEvent.change(proxy, { target: { value: 'http://127.0.0.1:7890' } });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(networkBody).toEqual({ proxy_url: 'http://127.0.0.1:7890' }));
    expect(await screen.findByText(/代理设置已保存/)).toBeInTheDocument();
  });

  it('keeps proxy recovery available when Codex cannot start', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/v1/agent/runtime/network')) {
          return json({ network: { proxy_url: 'socks5://127.0.0.1:7890' } });
        }
        if (url.includes('/api/v1/agent/runtime')) {
          return Promise.resolve(
            new Response(JSON.stringify({ error: { code: 'runtime_unavailable' } }), {
              status: 503,
              headers: { 'Content-Type': 'application/json' },
            }),
          );
        }
        return Promise.reject(new Error(`Unexpected request: ${url}`));
      }),
    );

    renderPage(<RuntimeSettingsPage />);

    expect(await screen.findByText('无法连接 Codex Runtime。')).toBeInTheDocument();
    expect(await screen.findByLabelText(/本机代理地址/)).toHaveValue('socks5://127.0.0.1:7890');
    expect(screen.getByRole('button', { name: '保存并应用' })).toBeEnabled();
  });
});

function renderPage(page: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{page}</QueryClientProvider>);
}

function account(signedIn: boolean) {
  return signedIn
    ? {
        signed_in: true,
        user: {
          id: 'usr_1',
          email: 'creator@shotforward.com',
          status: 'active',
          email_verified: true,
        },
        wallet: { available_points: 120.125, reserved_points: 8.5 },
      }
    : { signed_in: false, account_portal_url: 'https://cloud.example/app/account' };
}

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
  );
}
