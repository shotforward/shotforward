import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import '../i18n';
import i18n from '../i18n';
import { GeneralSettingsPage } from './GeneralSettingsPage';

describe('GeneralSettingsPage appearance', () => {
  beforeEach(async () => {
    window.localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    await i18n.changeLanguage('zh-CN');
  });

  it('offers persisted Studio palettes and applies the choice immediately', () => {
    render(<GeneralSettingsPage />);

    const appearance = screen.getByRole('combobox', { name: '外观' });
    expect(appearance).toBeEnabled();
    expect(appearance).toHaveTextContent('品牌经典');
    expect(appearance.querySelector('small')).not.toBeInTheDocument();
    expect(document.querySelector('.theme-select .select-control__icon')).not.toBeInTheDocument();

    fireEvent.click(appearance);
    expect(document.querySelector('.theme-select .select-control__icon')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('option', { name: /雾蓝/ }));

    expect(appearance).toHaveTextContent('雾蓝');
    expect(window.localStorage.getItem('shotforward.theme')).toBe('mist');
    expect(document.documentElement).toHaveAttribute('data-theme', 'mist');
  });
});
