import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { videoModelSource, type VideoModelChoice } from '../videoPromptTarget';

type Translate = ReturnType<typeof useTranslation>['t'];

function description(model: VideoModelChoice, t: Translate) {
  if (model.generatorID === 'video.h3.shotforward') {
    return t('storyboard.videoModelDescriptions.shotforwardH3', {
      minimum: model.capabilities?.duration?.minimum_seconds ?? 4,
      maximum: model.capabilities?.duration?.maximum_seconds ?? 15,
      sizes: model.capabilities?.output?.sizes?.join(' / ') || '720p',
    });
  }
  if (model.generatorID.includes('seedance-2.5')) {
    return t('storyboard.videoModelDescriptions.seedance', {
      maximum: model.modelID.includes('-2-5-') ? 30 : 15,
    });
  }
  if (model.generatorID.startsWith('video.h3.')) {
    return t('storyboard.videoModelDescriptions.minimaxH3', {
      minimum: model.modelID === 'MiniMax-H3-Max' ? 5 : 4,
    });
  }
  if (model.generatorID === 'video.wan-3.0.official') {
    return t('storyboard.videoModelDescriptions.wan3');
  }
  return t('storyboard.videoModelDescriptions.fallback');
}

export default function VideoModelHelp({ selected }: { selected: VideoModelChoice | undefined }) {
  const { t } = useTranslation();
  const tooltipID = useId();
  if (!selected) return null;
  return (
    <span className="video-model-help">
      <button type="button" aria-label={t('storyboard.videoProvider')} aria-describedby={tooltipID}>
        <span aria-hidden="true">?</span>
      </button>
      <span className="video-model-help__popover" id={tooltipID} role="tooltip">
        {`${selected.label} · ${videoModelSource(selected.generatorID)}`}：{description(selected, t)}
      </span>
    </span>
  );
}
