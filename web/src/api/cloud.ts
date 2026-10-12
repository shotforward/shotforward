import { requestJSON } from './client';

export interface CloudAccount {
  signed_in: boolean;
  account_portal_url?: string;
  user?: { id: string; email: string | null; status: string; email_verified: boolean };
  wallet?: { available_points: number; reserved_points: number };
}

export function getCloudAccount(): Promise<CloudAccount> {
  return requestJSON('/api/v1/cloud/account');
}

export function exchangeCloudLoginCode(code: string): Promise<CloudAccount> {
  return requestJSON('/api/v1/cloud/auth/exchanges', { method: 'POST', body: JSON.stringify({ code }) });
}

export function logoutCloud(): Promise<void> {
  return requestJSON('/api/v1/cloud/auth/logout', { method: 'POST' });
}

export function resetCloudSession(): Promise<void> {
  return requestJSON('/api/v1/cloud/auth/reset', { method: 'POST' });
}
