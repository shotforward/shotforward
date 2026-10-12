# ShotForward Studio Web UI

This is the single React interface served by `shotforwardd`. Browser and future
desktop-shell builds use the same static output and the same local HTTP/SSE API.

## Commands

```sh
npm ci
npm run typecheck
npm test
npm run build
```

`npm run build` writes the production assets to
`../daemon/internal/webui/dist` and then enforces gzip bundle budgets. The UI
uses native `fetch`, `EventSource`, `AbortController`, browser notifications,
and `Intl`; it intentionally avoids a second state store, HTTP client, form
framework, component kit, or utility library.

For local development, start the daemon and then run `npm run dev`. Set
`SHOTFORWARD_DAEMON_URL` only when the daemon is not using the default proxy
target.

## Architecture rules

- TanStack Query owns remote snapshots; SSE only invalidates or refreshes them.
- Local component state is limited to drafts, selection, filters, and dialogs.
- Every route is lazy-loaded. Shared navigation and the task-event bridge stay
  in the application shell.
- User-visible strings live in both `src/i18n/locales/zh-CN.ts` and `en.ts`.
  A contract test prevents either catalog from silently losing keys.
- Product screenshots under `docs/images` are documentation assets. Domain
  rules and API responses remain authoritative during implementation.
- Provider keys, database access, filesystem mutation, generation scheduling,
  and ChangeSet application never move into frontend code.

The desktop shell is intentionally a separate delivery work package. It must
start or discover the daemon, complete the established one-time bootstrap
without putting credentials in URLs or logs, and add native pickers/lifecycle
handling without creating another business layer.
