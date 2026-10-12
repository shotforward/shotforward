import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';
import '../i18n';
import { ProjectSectionTabs } from './ProjectSectionTabs';

describe('ProjectSectionTabs', () => {
  it('keeps trash inside project settings instead of exposing a fourth top-level section', () => {
    render(
      <MemoryRouter initialEntries={['/projects/prj_1/trash']}>
        <ProjectSectionTabs projectID="prj_1" activeSection="settings" />
      </MemoryRouter>,
    );

    const navigation = screen.getByRole('navigation', { name: /项目管理|Project management/i });
    expect(within(navigation).getAllByRole('link')).toHaveLength(3);
    expect(within(navigation).getByRole('link', { name: /项目设置|Project settings/i })).toHaveClass(
      'active',
    );
    expect(within(navigation).queryByRole('link', { name: /回收站|Trash/i })).not.toBeInTheDocument();
  });
});
