import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import '../i18n';
import { PairingPage } from './PairingPage';

describe('PairingPage', () => {
  it('normalizes input to six uppercase letters or digits', () => {
    render(<PairingPage onPaired={vi.fn()} />);
    const input = screen.getByLabelText(/配对码|Pairing code/i);

    fireEvent.change(input, { target: { value: 'a-b 12_cd345' } });

    expect(input).toHaveValue('AB12CD');
    expect(input).toHaveAttribute('maxlength', '6');
    expect(input).toHaveAttribute('pattern', '[A-Z0-9]{6}');
  });
});
