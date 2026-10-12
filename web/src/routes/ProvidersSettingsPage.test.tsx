import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { APIError } from '../api/client';
import { parseWanEndpoint, ProvidersSettingsPage, wanEndpointForProfile } from './ProvidersSettingsPage';

const mocks = vi.hoisted(() => ({
  checkProvider: vi.fn(),
  listProviders: vi.fn(),
  validateAndConfigureProvider: vi.fn(),
}));

vi.mock('../api/providers', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/providers')>();
  return {
    ...original,
    checkProvider: mocks.checkProvider,
    listProviders: mocks.listProviders,
    validateAndConfigureProvider: mocks.validateAndConfigureProvider,
  };
});

const codexProfile = {
  generator_id: 'image.codex',
  endpoint_preset: 'codex_subscription',
  model_id: '',
  resource_id: '',
  workspace_id: '',
  enabled: true,
  concurrency_limit: 1,
  credential_configured: true,
  capability_version: '1',
  health: 'ready' as const,
  failure_code: '',
  last_checked_at: '2026-09-09T01:03:00Z',
  revision: 1,
  created_at: '2026-09-09T01:00:00Z',
  updated_at: '2026-09-09T01:03:00Z',
};

const wanProfile = {
  ...codexProfile,
  generator_id: 'video.wan-3.0.official',
  endpoint_preset: 'alibaba_cn_beijing',
  model_id: 'wan3.0-video',
  workspace_id: 'llm-existing-workspace',
  health: 'unchecked' as const,
  models: [
    {
      model_id: 'wan3.0-video',
      is_default: true,
      capability_version: '',
      health: 'unchecked' as const,
      failure_code: '',
    },
  ],
};

describe('ProvidersSettingsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listProviders.mockResolvedValue({ providers: [codexProfile] });
    mocks.checkProvider.mockResolvedValue({ provider: codexProfile });
    mocks.validateAndConfigureProvider.mockResolvedValue({ provider: wanProfile });
  });

  it('groups the provider status action in the detail header', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );

    const detailHeader = await waitFor(() => {
      const element = document.querySelector('.provider-detail__header');
      expect(element).not.toBeNull();
      return element!;
    });
    const check = screen.getByRole('button', { name: /检查连接|Check connection/i });
    expect(detailHeader).toContainElement(check);
    expect(detailHeader).toHaveTextContent('Codex Image');
    expect(document.querySelector('.provider-managed-overview')).toBeInTheDocument();

    fireEvent.click(check);
    await waitFor(() => expect(mocks.checkProvider).toHaveBeenCalledWith('image.codex'));
  });

  it('uses the same full-width endpoint and API key layout for every video provider', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );
    await screen.findAllByText('Codex Image');

    for (const source of ['Volcengine China', 'BytePlus Global', 'MiniMax China', 'MiniMax Global', 'Alibaba Cloud']) {
      fireEvent.click(screen.getByText(source).closest('button')!);
      expect(document.querySelectorAll('.provider-form .field--wide')).toHaveLength(2);
    }
  });

  it('offers all regional Seedance models without mixing China and Global IDs', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );
    await screen.findAllByText('Codex Image');

    fireEvent.click(screen.getByText('Volcengine China').closest('button')!);
    for (const model of [
      'doubao-seedance-2-5-260628',
      'doubao-seedance-2-0-260128',
      'doubao-seedance-2-0-fast-260128',
      'doubao-seedance-2-0-mini-260615',
    ]) {
      expect(screen.getByRole('checkbox', { name: model })).toBeInTheDocument();
    }
    expect(screen.queryByRole('checkbox', { name: /dreamina-seedance/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByText('BytePlus Global').closest('button')!);
    expect(screen.getByRole('checkbox', { name: 'dreamina-seedance-2-0-fast-260128' })).toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /doubao-seedance/ })).not.toBeInTheDocument();
  });

  it('offers H3 and H3 Max on separate MiniMax China and Global routes', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );
    await screen.findAllByText('Codex Image');

    fireEvent.click(screen.getByText('MiniMax China').closest('button')!);
    expect(document.querySelector<HTMLInputElement>('.provider-form input[readonly]')).toHaveValue('minimax_cn');
    expect(screen.getByRole('checkbox', { name: 'MiniMax-H3' })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'MiniMax-H3-Max' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('checkbox', { name: 'MiniMax-H3-Max' }));
    fireEvent.change(document.querySelector<HTMLInputElement>('input[type="password"]')!, {
      target: { value: 'china-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: /验证并保存|Validate and save/i }));
    await waitFor(() =>
      expect(mocks.validateAndConfigureProvider).toHaveBeenCalledWith('video.h3.minimax-cn', {
        endpoint_preset: 'minimax_cn',
        model_id: 'MiniMax-H3',
        models: [
          { model_id: 'MiniMax-H3', is_default: true },
          { model_id: 'MiniMax-H3-Max', is_default: false },
        ],
        resource_id: '',
        workspace_id: '',
        api_key: 'china-key',
        enabled: true,
        concurrency_limit: 1,
        expected_revision: 0,
      }),
    );

    fireEvent.click(screen.getByText('MiniMax Global').closest('button')!);
    expect(document.querySelector<HTMLInputElement>('.provider-form input[readonly]')).toHaveValue('minimax_global');
    expect(screen.getByRole('checkbox', { name: 'MiniMax-H3-Max' })).toBeInTheDocument();
  });

  it('configures Wan from a full endpoint without exposing region or workspace fields', async () => {
    mocks.listProviders.mockResolvedValue({ providers: [codexProfile, wanProfile] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole('button', { name: /Wan 3\.0/i }));
    const endpoint = document.querySelector<HTMLInputElement>('input[type="url"]');
    expect(endpoint).toHaveValue(
      'https://llm-existing-workspace.cn-beijing.maas.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis',
    );
    expect(endpoint?.closest('.field')).toHaveClass('field--wide');
    expect(document.querySelector<HTMLInputElement>('input[type="password"]')?.closest('.field')).toHaveClass(
      'field--wide',
    );
    expect(screen.queryByText('Workspace ID')).not.toBeInTheDocument();
    expect(screen.queryByText(/China · Beijing/i)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('checkbox', { name: 'wan3.0-video-prime' }));
    expect(document.querySelectorAll('.provider-model-list input[type="radio"]')).toHaveLength(0);

    fireEvent.change(endpoint!, {
      target: {
        value:
          'https://llm-bfeq24txqjdlxtw1.cn-beijing.maas.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis',
      },
    });
    fireEvent.change(document.querySelector<HTMLInputElement>('input[type="password"]')!, {
      target: { value: 'secret-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: /验证并保存|Validate and save/i }));

    await waitFor(() =>
      expect(mocks.validateAndConfigureProvider).toHaveBeenCalledWith('video.wan-3.0.official', {
        endpoint_preset: 'alibaba_cn_beijing',
        model_id: 'wan3.0-video',
        models: [
          { model_id: 'wan3.0-video', is_default: true },
          { model_id: 'wan3.0-video-prime', is_default: false },
        ],
        resource_id: '',
        workspace_id: 'llm-bfeq24txqjdlxtw1',
        api_key: 'secret-key',
        enabled: true,
        concurrency_limit: 1,
        expected_revision: 1,
      }),
    );
  });

  it('edits provider concurrency with an integer stepper from 1 through 10', async () => {
    mocks.listProviders.mockResolvedValue({ providers: [codexProfile, wanProfile] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole('button', { name: /Wan 3\.0/i }));
    const input = screen.getByRole('spinbutton', { name: /最大并发|Maximum concurrency/i });
    const decrease = screen.getByRole('button', { name: /减少最大并发|Decrease maximum concurrency/i });
    const increase = screen.getByRole('button', { name: /增加最大并发|Increase maximum concurrency/i });

    expect(input).toHaveAttribute('min', '1');
    expect(input).toHaveAttribute('max', '10');
    expect(input).toHaveAttribute('step', '1');
    expect(input).toHaveValue(1);
    expect(decrease).toBeDisabled();

    fireEvent.click(increase);
    expect(input).toHaveValue(2);
    fireEvent.change(input, { target: { value: '10' } });
    expect(input).toHaveValue(10);
    expect(increase).toBeDisabled();

    fireEvent.click(decrease);
    expect(input).toHaveValue(9);
  });

  it('offers one action that validates and saves a first-time configuration', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole('button', { name: /Wan 3\.0/i }));
    fireEvent.change(document.querySelector<HTMLInputElement>('input[type="url"]')!, {
      target: { value: 'https://llm-new.cn-beijing.maas.aliyuncs.com' },
    });
    fireEvent.change(document.querySelector<HTMLInputElement>('input[type="password"]')!, {
      target: { value: 'secret-key' },
    });
    const check = screen.getByRole('button', { name: /验证并保存|Validate and save/i });
    expect(check).toBeEnabled();
    expect(screen.queryByRole('button', { name: /保存配置|Save configuration/i })).not.toBeInTheDocument();
    fireEvent.click(check);

    await waitFor(() => expect(mocks.validateAndConfigureProvider).toHaveBeenCalledOnce());
    expect(mocks.checkProvider).not.toHaveBeenCalled();
  });

  it('keeps validation failure in the single save flow', async () => {
    mocks.listProviders.mockResolvedValue({ providers: [codexProfile, wanProfile] });
    mocks.validateAndConfigureProvider.mockRejectedValue(
      new APIError(422, {
        error: { code: 'provider_validation_failed', message: 'validation failed' },
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ProvidersSettingsPage />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole('button', { name: /Wan 3\.0/i }));
    fireEvent.click(screen.getByRole('button', { name: /验证并保存|Validate and save/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/验证失败|Validation failed/i);
    expect(mocks.validateAndConfigureProvider).toHaveBeenCalledOnce();
    expect(mocks.checkProvider).not.toHaveBeenCalled();
  });

  it('accepts only known Alibaba Cloud Wan endpoints', () => {
    expect(parseWanEndpoint('https://llm-workspace.ap-southeast-1.maas.aliyuncs.com')).toEqual({
      endpointPreset: 'alibaba_ap_southeast_1',
      workspaceID: 'llm-workspace',
    });
    expect(parseWanEndpoint('http://llm-workspace.cn-beijing.maas.aliyuncs.com')).toBeNull();
    expect(parseWanEndpoint('https://llm-workspace.cn-beijing.maas.aliyuncs.com/other')).toBeNull();
    expect(parseWanEndpoint('https://example.com')).toBeNull();
    expect(wanEndpointForProfile(wanProfile)).toContain(
      'llm-existing-workspace.cn-beijing.maas.aliyuncs.com',
    );
  });
});
