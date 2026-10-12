import { FormEvent, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { createProject, type CreateProjectInput } from '../api/projects';
import { Dialog } from '../components/Dialog';
import { hasDesktopBridge, projectPathInParent, selectProjectParent } from '../desktop';
import './CreateProjectDialog.css';

type Aspect = 'portrait' | 'landscape' | 'square';
type Resolution = 480 | 720 | 1080;

interface CompatibleVideoModel {
  name: string;
  nativeResolution: '480P' | '720P' | '768P' | '1080P' | '2K';
  adapted?: boolean;
}

const COMPATIBLE_VIDEO_MODELS: Record<Resolution, readonly CompatibleVideoModel[]> = {
  480: [
    { name: 'Seedance 2.5', nativeResolution: '480P' },
    { name: 'Wan 3.0 Standard / Prime', nativeResolution: '480P' },
    { name: 'MiniMax H3', nativeResolution: '768P', adapted: true },
  ],
  720: [
    { name: 'Seedance 2.5', nativeResolution: '720P' },
    { name: 'Wan 3.0 Standard / Prime', nativeResolution: '720P' },
    { name: 'MiniMax H3', nativeResolution: '768P', adapted: true },
  ],
  1080: [
    { name: 'Wan 3.0 Standard / Prime', nativeResolution: '1080P' },
    { name: 'MiniMax H3', nativeResolution: '2K', adapted: true },
  ],
};

function dimensions(
  aspect: Aspect,
  resolution: Resolution,
): Pick<CreateProjectInput, 'output_width' | 'output_height'> {
  if (aspect === 'square') return { output_width: resolution, output_height: resolution };
  const shortSide = resolution;
  const longSide = resolution === 480 ? 854 : resolution === 720 ? 1280 : 1920;
  return aspect === 'portrait'
    ? { output_width: shortSide, output_height: longSide }
    : { output_width: longSide, output_height: shortSide };
}

export function CreateProjectDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const locationCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? projectLocationCopy.zh
    : projectLocationCopy.en;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [step, setStep] = useState<1 | 2>(1);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [rootPath, setRootPath] = useState('');
  const [selectedParent, setSelectedParent] = useState('');
  const [pickingLocation, setPickingLocation] = useState(false);
  const [locationPickerFailed, setLocationPickerFailed] = useState(false);
  const [aspect, setAspect] = useState<Aspect>('portrait');
  const [resolution, setResolution] = useState<Resolution>(720);
  const creation = useMutation({
    mutationFn: createProject,
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] });
      queryClient.setQueryData(['project', result.project.project_id], result);
      onClose();
      void navigate(`/projects/${result.project.project_id}`);
    },
  });

  function close() {
    if (creation.isPending) return;
    setStep(1);
    creation.reset();
    onClose();
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (step === 1) {
      if (name.trim() && rootPath.trim()) setStep(2);
      return;
    }
    creation.mutate({
      root_path: rootPath.trim(),
      name: name.trim(),
      description: description.trim(),
      ...dimensions(aspect, resolution),
    });
  }

  async function chooseLocation() {
    setPickingLocation(true);
    setLocationPickerFailed(false);
    try {
      const parent = await selectProjectParent();
      if (!parent) return;
      setSelectedParent(parent);
      setRootPath(projectPathInParent(parent, name));
    } catch {
      setLocationPickerFailed(true);
    } finally {
      setPickingLocation(false);
    }
  }

  return (
    <Dialog open={open} label={t('projects.createTitle')} onClose={close}>
      <form className="dialog-panel create-project" onSubmit={submit}>
        <header className="dialog-header">
          <div>
            <p>{step} / 2</p>
            <h2>{t(step === 1 ? 'projects.createBasics' : 'projects.createOutput')}</h2>
          </div>
          <button type="button" className="icon-button" onClick={close} aria-label={t('common.cancel')}>
            ×
          </button>
        </header>
        {step === 1 ? (
          <div className="form-stack">
            <label className="field">
              <span>{t('projects.name')}</span>
              <input
                value={name}
                onChange={(event) => {
                  const value = event.target.value;
                  setName(value);
                  if (selectedParent) setRootPath(projectPathInParent(selectedParent, value));
                }}
                placeholder={t('projects.namePlaceholder')}
                maxLength={120}
                autoFocus
                required
              />
            </label>
            <label className="field">
              <span>{t('projects.description')}</span>
              <textarea
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder={t('projects.descriptionPlaceholder')}
                rows={3}
                maxLength={600}
              />
            </label>
            <label className="field">
              <span>{t('projects.location')}</span>
              <div className="project-location-control">
                <input
                  value={rootPath}
                  onChange={(event) => {
                    setSelectedParent('');
                    setRootPath(event.target.value);
                  }}
                  placeholder={t('projects.locationPlaceholder')}
                  required
                />
                {hasDesktopBridge() && (
                  <button
                    type="button"
                    className="button button--secondary"
                    disabled={pickingLocation}
                    onClick={() => void chooseLocation()}
                  >
                    {pickingLocation ? locationCopy.choosing : locationCopy.choose}
                  </button>
                )}
              </div>
              <small>{locationCopy.hint}</small>
              {locationPickerFailed && <small className="form-error">{locationCopy.failed}</small>}
            </label>
          </div>
        ) : (
          <div className="form-stack">
            <fieldset className="choice-group">
              <legend>{t('projects.aspectRatio')}</legend>
              <div className="choice-grid choice-grid--three">
                {(['portrait', 'landscape', 'square'] as const).map((value) => (
                  <label className={`choice-card${aspect === value ? ' is-selected' : ''}`} key={value}>
                    <input
                      type="radio"
                      name="aspect"
                      value={value}
                      checked={aspect === value}
                      onChange={() => setAspect(value)}
                    />
                    <span className={`aspect-icon aspect-icon--${value}`} />
                    <strong>{t(`projects.${value}`)}</strong>
                  </label>
                ))}
              </div>
            </fieldset>
            <fieldset className="choice-group">
              <legend>{t('projects.resolution')}</legend>
              <div className="choice-grid choice-grid--three">
                {([480, 720, 1080] as const).map((value) => (
                  <label
                    className={`choice-card choice-card--compact${resolution === value ? ' is-selected' : ''}`}
                    key={value}
                  >
                    <input
                      type="radio"
                      name="resolution"
                      value={value}
                      checked={resolution === value}
                      onChange={() => setResolution(value)}
                    />
                    <strong>{value}p</strong>
                  </label>
                ))}
              </div>
            </fieldset>
            <section className="compatible-models" aria-live="polite">
              <div className="compatible-models__heading">
                <strong>{t('projects.compatibleModels')}</strong>
                <span>{t('projects.compatibleModelsResolution', { resolution })}</span>
              </div>
              <ul className="compatible-models__list">
                {COMPATIBLE_VIDEO_MODELS[resolution].map((model) => (
                  <li key={model.name}>
                    <strong>{model.name}</strong>
                    <span className={model.adapted ? 'is-adapted' : undefined}>
                      {model.adapted
                        ? t('projects.adaptedResolution', {
                            source: model.nativeResolution,
                            target: `${resolution}P`,
                          })
                        : t('projects.nativeResolution', { resolution: model.nativeResolution })}
                    </span>
                  </li>
                ))}
              </ul>
              <p>{t('projects.minimaxResolutionHint')}</p>
            </section>
            <p className="info-note">{t('projects.fixedHint')}</p>
          </div>
        )}
        {creation.isError && (
          <p className="form-error" role="alert">
            {t('projects.createFailed')}
          </p>
        )}
        <footer className="dialog-footer">
          <button
            type="button"
            className="button"
            onClick={step === 1 ? close : () => setStep(1)}
            disabled={creation.isPending}
          >
            {t(step === 1 ? 'common.cancel' : 'projects.back')}
          </button>
          <button
            className="button button--primary"
            disabled={creation.isPending || (step === 1 && (!name.trim() || !rootPath.trim()))}
          >
            {creation.isPending
              ? t('projects.creating')
              : t(step === 1 ? 'projects.next' : 'projects.create')}
          </button>
        </footer>
      </form>
    </Dialog>
  );
}

const projectLocationCopy = {
  zh: {
    choose: '选择文件夹',
    choosing: '正在打开…',
    failed: '无法打开系统文件夹选择器，请直接输入绝对路径。',
    hint: '选择父文件夹后，将自动使用项目名称创建专用项目目录。浏览器模式也可直接输入绝对路径。',
  },
  en: {
    choose: 'Choose folder',
    choosing: 'Opening…',
    failed: 'The system folder picker could not be opened. Enter an absolute path instead.',
    hint: 'Choose a parent folder and ShotForward will create a project folder using the project name.',
  },
} as const;
