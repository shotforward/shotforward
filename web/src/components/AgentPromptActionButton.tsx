import type { ComponentPropsWithoutRef } from 'react';
import { SparklesIcon } from './Icons';

export function AgentPromptActionButton({
  children,
  className,
  type = 'button',
  ...props
}: ComponentPropsWithoutRef<'button'>) {
  return (
    <button {...props} type={type} className={['agent-prompt-action', className].filter(Boolean).join(' ')}>
      <SparklesIcon />
      <span>{children}</span>
    </button>
  );
}
