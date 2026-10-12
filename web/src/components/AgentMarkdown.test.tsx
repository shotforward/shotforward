import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { AgentMarkdown } from './AgentMarkdown';

describe('AgentMarkdown', () => {
  it('renders the supported Markdown structure', () => {
    const { container } = render(
      <AgentMarkdown
        text={
          '# 方案\n\n- **重点**\n- [x] 已完成\n\n| 场次 | 状态 |\n| --- | --- |\n| 第一场 | 完成 |\n\n```json\n{"ok":true}\n```\n\n[文档](https://example.com)\n\n![远程图](https://example.com/tracker.png)'
        }
      />,
    );

    expect(screen.getByRole('heading', { level: 1, name: '方案' })).toBeInTheDocument();
    expect(screen.getByText('重点').tagName).toBe('STRONG');
    expect(screen.getByRole('checkbox')).toBeChecked();
    expect(screen.getByRole('table')).toBeInTheDocument();
    expect(screen.getByText('{"ok":true}').closest('code')).toHaveClass('language-json');
    expect(screen.getByRole('link', { name: '文档' })).toHaveAttribute('target', '_blank');
    expect(container.querySelector('img')).not.toBeInTheDocument();
    expect(screen.getByText('[图片：远程图]')).toBeInTheDocument();
  });

  it('never interprets raw HTML or unsafe link protocols', () => {
    const { container } = render(
      <AgentMarkdown text={'<script>alert(1)</script>\n\n[unsafe](javascript:alert(1))'} />,
    );

    expect(container.querySelector('script')).not.toBeInTheDocument();
    expect(screen.getByText('<script>alert(1)</script>')).toBeInTheDocument();
    expect(screen.getByText('unsafe').closest('a')).toBeNull();
  });
});
