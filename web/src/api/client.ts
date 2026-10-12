export interface APIErrorBody {
  error?: {
    code?: string;
    message?: string;
    retryable?: boolean;
    request_id?: string;
    suggested_action?: string;
    details?: Record<string, unknown>;
  };
}

export class APIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly requestID: string | undefined;
  readonly details: Record<string, unknown>;

  constructor(status: number, body: APIErrorBody) {
    const failure = body.error;
    super(failure?.message || `Request failed with status ${status}`);
    this.name = 'APIError';
    this.status = status;
    this.code = failure?.code ?? 'unknown_error';
    this.retryable = failure?.retryable ?? false;
    this.requestID = failure?.request_id;
    this.details = failure?.details ?? {};
  }
}

let csrfToken = '';
const sessionExpiredListeners = new Set<() => void>();

export function setCSRFToken(token: string): void {
  csrfToken = token;
}

export function clearCSRFToken(): void {
  csrfToken = '';
}

export function onSessionExpired(listener: () => void): () => void {
  sessionExpiredListeners.add(listener);
  return () => sessionExpiredListeners.delete(listener);
}

export function createOperationKey(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID();
  }
  if (typeof globalThis.crypto?.getRandomValues !== 'function') {
    throw new Error('Secure random number generation is unavailable');
  }
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const value = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`;
}

export async function requestJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase();
  const headers = new Headers(init.headers);
  const multipartBody = typeof FormData !== 'undefined' && init.body instanceof FormData;
  if (init.body !== undefined && !headers.has('Content-Type') && !multipartBody) {
    headers.set('Content-Type', 'application/json');
  }
  if (!['GET', 'HEAD'].includes(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken);
  }

  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  const contentType = response.headers.get('Content-Type') ?? '';
  const body = contentType.includes('application/json')
    ? ((await response.json()) as T | APIErrorBody)
    : undefined;

  if (!response.ok) {
    const requestID = response.headers.get('X-Request-ID');
    const errorBody =
      body ??
      ({
        error: {
          code: `http_${response.status}`,
          message: `Request failed with status ${response.status}`,
          ...(requestID ? { request_id: requestID } : {}),
        },
      } satisfies APIErrorBody);
    const error = new APIError(response.status, errorBody as APIErrorBody);
    if (error.code === 'authentication_required' && path !== '/api/v1/bootstrap') {
      clearCSRFToken();
      sessionExpiredListeners.forEach((listener) => listener());
    }
    throw error;
  }
  return body as T;
}

export interface SessionResponse {
  csrf_token: string;
  instance_id: string;
}

export async function restoreSession(): Promise<SessionResponse> {
  const session = await requestJSON<SessionResponse>('/api/v1/session');
  setCSRFToken(session.csrf_token);
  return session;
}

export async function pair(pairingCode: string): Promise<void> {
  const result = await requestJSON<{ csrf_token: string }>('/api/v1/bootstrap', {
    method: 'POST',
    body: JSON.stringify({ pairing_code: pairingCode.trim() }),
  });
  setCSRFToken(result.csrf_token);
}
