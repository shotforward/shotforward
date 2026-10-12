import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RechargeAction } from './RechargeAction';
import i18n from '../i18n';

afterEach(() => {
  cleanup();
  delete window.__SHOTFORWARD_DESKTOP__;
});

it('opens recharge in the system browser and follows the selected language', async () => {
  const openExternal = vi.fn().mockResolvedValue(undefined);
  window.__SHOTFORWARD_DESKTOP__ = {
    openExternal,
    selectProjectParent: async () => null,
  };
  await i18n.changeLanguage('zh-CN');
  const view = render(<RechargeAction code="shotforward_negative_balance" />);
  fireEvent.click(screen.getByRole('button', { name: '去充值' }));
  await waitFor(() => expect(openExternal).toHaveBeenCalledWith('https://shotforward.com/app/recharge'));
  await i18n.changeLanguage('en');
  expect(await screen.findByRole('button', { name: 'Recharge' })).toBeInTheDocument();
  view.rerender(<RechargeAction code="shotforward_wallet_not_found" />);
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
  view.rerender(<RechargeAction code="insufficient_points" />);
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
});
