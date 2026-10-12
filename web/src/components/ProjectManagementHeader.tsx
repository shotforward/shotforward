import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { ProjectSectionTabs, type ProjectSection } from './ProjectSectionTabs';
import '../styles/project-management.css';

export function ProjectManagementHeader({
  projectID,
  projectName,
  activeSection,
}: {
  projectID: string;
  projectName: string;
  activeSection?: ProjectSection;
}) {
  const { t } = useTranslation();
  return (
    <>
      <header className="project-heading project-heading--context">
        <div className="project-heading__copy">
          <Link to="/projects">← {t('projects.backToLibrary')}</Link>
          <div className="project-heading__title-row">
            <h1>{projectName}</h1>
            <Link
              className="project-heading__edit-link"
              to={`/projects/${encodeURIComponent(projectID)}/settings#project-name`}
              aria-label={t('projectSettings.editBasicInfo')}
              title={t('projectSettings.editBasicInfo')}
            >
              ✎
            </Link>
          </div>
        </div>
      </header>
      <ProjectSectionTabs projectID={projectID} activeSection={activeSection} />
    </>
  );
}
