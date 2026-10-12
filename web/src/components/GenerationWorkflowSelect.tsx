import { useTranslation } from 'react-i18next';
import type { Clip } from '../api/storyboard';

export default function GenerationWorkflowSelect({
  value,
  keyframeMode,
  onChange,
}: {
  value: Clip['generation_mode'];
  keyframeMode: Exclude<Clip['generation_mode'], 'reference'>;
  onChange: (value: Clip['generation_mode']) => void;
}) {
  const { t, i18n } = useTranslation();
  const referenceMode = value === 'reference';
  const firstFrameOnly = i18n.resolvedLanguage?.startsWith('zh') ? '仅首帧' : 'First frame only';
  const firstAndLastFrames = i18n.resolvedLanguage?.startsWith('zh') ? '首帧 + 尾帧' : 'First + last frames';
  return (
    <div className="generation-workflow-select">
      <h3 className="generation-workflow-select__heading storyboard-heading storyboard-heading--secondary">
        {t('storyboard.mode')}
      </h3>
      <div className="generation-workflow-select__controls">
        <div
          className="generation-workflow-select__primary"
          role="radiogroup"
          aria-label={t('storyboard.mode')}
        >
          <button
            type="button"
            role="radio"
            aria-checked={referenceMode}
            className={referenceMode ? 'is-active' : ''}
            onClick={() => onChange('reference')}
          >
            <span className="generation-workflow-select__indicator is-reference" aria-hidden="true" />
            <strong>{t('storyboard.referenceWorkflow')}</strong>
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={!referenceMode}
            className={!referenceMode ? 'is-active' : ''}
            onClick={() => onChange(keyframeMode)}
          >
            <span className="generation-workflow-select__indicator is-keyframe" aria-hidden="true" />
            <strong>{t('storyboard.keyframeWorkflow')}</strong>
          </button>
        </div>
        {!referenceMode && (
          <div
            className="generation-workflow-select__keyframes"
            role="radiogroup"
            aria-label={t('storyboard.keyframeWorkflow')}
          >
            <button
              type="button"
              role="radio"
              aria-checked={value === 'first_image'}
              className={value === 'first_image' ? 'is-active' : ''}
              onClick={() => onChange('first_image')}
            >
              {firstFrameOnly}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={value === 'first_last_image'}
              className={value === 'first_last_image' ? 'is-active' : ''}
              onClick={() => onChange('first_last_image')}
            >
              {firstAndLastFrames}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
