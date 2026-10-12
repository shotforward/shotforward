import type { TFunction } from 'i18next';

export function businessErrorCode(error: unknown): string | undefined {
  if (error && typeof error === 'object' && 'code' in error && typeof error.code === 'string')
    return error.code;
}

export function isBalanceError(code?: string): boolean {
  return code === 'shotforward_negative_balance' || code === 'shotforward_insufficient_points';
}

export function businessErrorMessage(code: string | undefined, t: TFunction): string | undefined {
  if (isBalanceError(code)) return t('businessErrors.balance');
  if (code === 'shotforward_wallet_not_found') return t('businessErrors.wallet');
}
