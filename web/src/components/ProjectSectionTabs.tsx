import { NavLink } from 'react-router';
import { useTranslation } from 'react-i18next';

export type ProjectSection = 'episodes' | 'assets' | 'settings';

export function ProjectSectionTabs({
  projectID,
  activeSection,
}: {
  projectID: string;
  activeSection?: ProjectSection | undefined;
}) {
  const { t } = useTranslation();
  return (
    <nav className="project-tabs project-section-tabs" aria-label={t('projectSections.navigation')}>
      <NavLink
        to={`/projects/${projectID}`}
        end
        className={({ isActive }) => (isActive || activeSection === 'episodes' ? 'active' : '')}
      >
        {t('projectSections.episodes')}
      </NavLink>
      <NavLink
        to={`/projects/${projectID}/assets`}
        className={({ isActive }) => (isActive || activeSection === 'assets' ? 'active' : '')}
      >
        {t('projectSections.crossAssets')}
      </NavLink>
      <NavLink
        to={`/projects/${projectID}/settings`}
        className={({ isActive }) => (isActive || activeSection === 'settings' ? 'active' : '')}
      >
        {t('projectSections.settings')}
      </NavLink>
    </nav>
  );
}
