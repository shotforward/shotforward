import { FormEvent, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { APIError, pair } from '../api/client';
import { LanguageSwitch } from '../components/LanguageSwitch';
import { Logo } from '../components/Logo';

export function PairingPage({ onPaired }: { onPaired: () => void }) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [failed, setFailed] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!code.trim() || submitting) return;
    setSubmitting(true);
    setFailed(false);
    try {
      await pair(code);
      onPaired();
    } catch (error) {
      setFailed(error instanceof APIError || error instanceof TypeError);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="pairing-page">
      <div className="pairing-page__toolbar">
        <LanguageSwitch />
      </div>
      <section className="pairing-card">
        <Logo />
        <div className="pairing-card__copy">
          <h1>{t('auth.title')}</h1>
          <p>{t('auth.description')}</p>
        </div>
        <form onSubmit={submit} className="pairing-form">
          <label htmlFor="pairing-code">{t('auth.pairingCode')}</label>
          <input
            id="pairing-code"
            value={code}
            onChange={(event) =>
              setCode(
                event.target.value
                  .toUpperCase()
                  .replace(/[^A-Z0-9]/g, '')
                  .slice(0, 6),
              )
            }
            placeholder={t('auth.placeholder')}
            autoComplete="one-time-code"
            autoCapitalize="characters"
            maxLength={6}
            pattern="[A-Z0-9]{6}"
            autoFocus
            aria-invalid={failed}
          />
          {failed && (
            <p className="form-error" role="alert">
              {t('auth.invalid')}
            </p>
          )}
          <button className="button button--primary" disabled={!code.trim() || submitting}>
            {submitting ? t('auth.connecting') : t('auth.connect')}
          </button>
        </form>
      </section>
    </main>
  );
}
