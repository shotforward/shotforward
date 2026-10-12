import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { AgentPromptActionButton } from './AgentPromptActionButton';

describe('AgentPromptActionButton', () => {
  it('provides one shared accessible button structure for edit and adaptation actions', () => {
    const onClick = vi.fn();
    render(<AgentPromptActionButton onClick={onClick}>Agent 修改</AgentPromptActionButton>);

    const button = screen.getByRole('button', { name: 'Agent 修改' });
    expect(button).toHaveClass('agent-prompt-action');
    expect(button).toHaveAttribute('type', 'button');
    expect(button.querySelector('svg')).toBeInTheDocument();

    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledOnce();
  });
});
