import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  APIError,
  clearCSRFToken,
  createOperationKey,
  onSessionExpired,
  requestJSON,
  setCSRFToken,
} from './client';

describe('requestJSON', () => {
  beforeEach(() => {
    clearCSRFToken();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('uses same-origin credentials and sends CSRF only for mutations', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })),
      );
    vi.stubGlobal('fetch', fetchMock);
    setCSRFToken('csrf-value');

    await requestJSON('/api/read');
    await requestJSON('/api/write', { method: 'PATCH', body: JSON.stringify({ value: 1 }) });

    expect(fetchMock.mock.calls[0]?.[1]?.credentials).toBe('same-origin');
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).has('X-CSRF-Token')).toBe(false);
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('X-CSRF-Token')).toBe('csrf-value');
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('Content-Type')).toBe('application/json');
  });

  it('normalizes structured API errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({ error: { code: 'revision_conflict', message: 'Changed', retryable: true } }),
            { status: 409, headers: { 'Content-Type': 'application/json' } },
          ),
        ),
    );
    await expect(requestJSON('/api/write', { method: 'POST' })).rejects.toMatchObject({
      status: 409,
      code: 'revision_conflict',
      retryable: true,
      message: 'Changed',
    } satisfies Partial<APIError>);
  });

  it('preserves the HTTP status and request ID for non-JSON proxy errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('<html>gateway failure</html>', {
          status: 500,
          headers: { 'Content-Type': 'text/html', 'X-Request-ID': 'req_proxy_1' },
        }),
      ),
    );

    await expect(requestJSON('/api/upload', { method: 'POST' })).rejects.toMatchObject({
      status: 500,
      code: 'http_500',
      requestID: 'req_proxy_1',
      message: 'Request failed with status 500',
    } satisfies Partial<APIError>);
  });

  it('lets the browser set the multipart boundary for file uploads', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
      );
    vi.stubGlobal('fetch', fetchMock);
    setCSRFToken('csrf-value');
    const body = new FormData();
    body.set('file', new File(['image'], 'reference.png', { type: 'image/png' }));

    await requestJSON('/api/upload', { method: 'POST', body });

    const headers = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
    expect(headers.has('Content-Type')).toBe(false);
    expect(headers.get('X-CSRF-Token')).toBe('csrf-value');
  });

  it('notifies the application when an authenticated session expires', async () => {
    const expired = vi.fn();
    const stopListening = onSessionExpired(expired);
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { code: 'authentication_required' } }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    await expect(requestJSON('/api/v1/projects')).rejects.toMatchObject({ status: 401 });
    expect(expired).toHaveBeenCalledOnce();
    stopListening();
  });

  it('does not expire the local daemon session for an unrelated 401', async () => {
    const expired = vi.fn();
    const stopListening = onSessionExpired(expired);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: 'invalid_desktop_code' } }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
      );
    vi.stubGlobal('fetch', fetchMock);
    setCSRFToken('csrf-value');

    await expect(requestJSON('/api/v1/cloud/auth/exchanges', { method: 'POST' })).rejects.toMatchObject({
      status: 401,
      code: 'invalid_desktop_code',
    });
    await requestJSON('/api/write', { method: 'POST' });

    expect(expired).not.toHaveBeenCalled();
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('X-CSRF-Token')).toBe('csrf-value');
    stopListening();
  });

  it('creates a UUID operation key when randomUUID is unavailable on HTTP', () => {
    let next = 0;
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.forEach((_, index) => {
          bytes[index] = next++;
        });
        return bytes;
      },
    });

    expect(createOperationKey()).toBe('00010203-0405-4607-8809-0a0b0c0d0e0f');
  });
});
