import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { Select } from './Select';

const options = [
  { value: 'updated', label: 'Recently updated' },
  { value: 'name', label: 'Project name' },
  { value: 'created', label: 'Date created' },
] as const;

function Example({ onChange = () => undefined }: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState<(typeof options)[number]['value']>('updated');
  return (
    <Select
      ariaLabel="Sort projects"
      value={value}
      options={options}
      onChange={(next) => {
        setValue(next);
        onChange(next);
      }}
    />
  );
}

describe('Select', () => {
  it('opens a branded listbox and chooses an option', () => {
    const onChange = vi.fn();
    render(<Example onChange={onChange} />);

    const trigger = screen.getByRole('combobox', { name: 'Sort projects' });
    expect(trigger).toHaveTextContent('Recently updated');
    fireEvent.click(trigger);

    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('option', { name: 'Recently updated' })).toHaveAttribute('aria-selected', 'true');
    fireEvent.click(screen.getByRole('option', { name: 'Project name' }));

    expect(onChange).toHaveBeenCalledWith('name');
    expect(trigger).toHaveTextContent('Project name');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
  });

  it('supports arrow, home, end, enter, and escape keys', () => {
    const onChange = vi.fn();
    render(<Example onChange={onChange} />);
    const trigger = screen.getByRole('combobox', { name: 'Sort projects' });

    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    fireEvent.keyDown(trigger, { key: 'End' });
    fireEvent.keyDown(trigger, { key: 'Enter' });
    expect(onChange).toHaveBeenLastCalledWith('created');

    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    fireEvent.keyDown(trigger, { key: 'Home' });
    fireEvent.keyDown(trigger, { key: 'Escape' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
  });

  it('does not open while disabled', () => {
    render(
      <Select
        ariaLabel="Appearance"
        value="light"
        options={[{ value: 'light', label: 'Light' }]}
        disabled
        onChange={() => undefined}
      />,
    );
    const trigger = screen.getByRole('combobox', { name: 'Appearance' });
    expect(trigger).toBeDisabled();
    fireEvent.click(trigger);
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });
});
