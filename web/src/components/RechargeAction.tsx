import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { openExternal } from '../desktop';
import { isBalanceError } from '../businessErrors';

export function RechargeAction({ code }: { code?: string | undefined }) {
  const { t } = useTranslation();
  const [failed, setFailed] = useState(false);
  if (!isBalanceError(code)) return null;
  return (
    <span>
      <button
        type="button"
        className="button button--secondary"
        onClick={() => {
          setFailed(false);
          void openExternal('https://shotforward.com/app/recharge').catch(() => setFailed(true));
        }}
      >
        {t('businessErrors.recharge')}
      </button>
      {failed && <small role="alert">{t('businessErrors.browserFailed')}</small>}
    </span>
  );
}
