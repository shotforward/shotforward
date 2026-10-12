import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { lazy, Suspense, useEffect, useState } from 'react';
import { createBrowserRouter, Navigate, RouterProvider } from 'react-router';
import { APIError, clearCSRFToken, onSessionExpired, pair, restoreSession } from '../api/client';
import { AppShell } from './AppShell';

// Desktop bootstrap normally establishes a session without the manual pairing
// form. Load its controls only when a pairing code is actually needed.
const PairingPage = lazy(async () => ({ default: (await import('../routes/PairingPage')).PairingPage }));

function AppLoading() {
  return (
    <div className="app-loading">
      <div className="spinner" />
    </div>
  );
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, refetchOnWindowFocus: false, retry: false },
    mutations: { retry: false },
  },
});

function StartupRedirect() {
  const lastPath = window.localStorage.getItem('shotforward.last-path');
  const useLast = window.localStorage.getItem('shotforward.start-page') === 'last';
  const target = useLast && lastPath?.startsWith('/') ? lastPath : '/projects';
  return <Navigate to={target === '/' ? '/projects' : target} replace />;
}

const router = createBrowserRouter([
  {
    element: <AppShell />,
    children: [
      { index: true, element: <StartupRedirect /> },
      {
        path: '/projects',
        lazy: async () => ({ Component: (await import('../routes/ProjectsPage')).ProjectsPage }),
      },
      {
        path: '/projects/:projectId',
        lazy: async () => ({
          Component: (await import('../routes/ProjectOverviewPage')).ProjectOverviewPage,
        }),
      },
      {
        path: '/projects/:projectId/assets',
        lazy: async () => ({
          Component: (await import('../routes/CrossEpisodeAssetsPage')).CrossEpisodeAssetsPage,
        }),
      },
      {
        path: '/projects/:projectId/settings',
        lazy: async () => ({
          Component: (await import('../routes/ProjectSettingsPage')).ProjectSettingsPage,
        }),
      },
      {
        path: '/projects/:projectId/trash',
        lazy: async () => ({ Component: (await import('../routes/ProjectTrashPage')).ProjectTrashPage }),
      },
      {
        path: '/projects/:projectId/episodes/:episodeId/script',
        lazy: async () => ({
          Component: (await import('../routes/ScriptWorkspacePage')).ScriptWorkspacePage,
        }),
      },
      {
        path: '/projects/:projectId/episodes/:episodeId/assets',
        lazy: async () => ({ Component: (await import('../routes/EpisodeAssetsPage')).EpisodeAssetsPage }),
      },
      {
        path: '/projects/:projectId/episodes/:episodeId/storyboard',
        lazy: async () => ({
          Component: (await import('../routes/StoryboardWorkspacePage')).StoryboardWorkspacePage,
        }),
      },
      {
        path: '/projects/:projectId/episodes/:episodeId/storyboard/clips/:clipId',
        lazy: async () => ({
          Component: (await import('../routes/StoryboardWorkspacePage')).StoryboardWorkspacePage,
        }),
      },
      {
        path: '/projects/:projectId/episodes/:episodeId/preview',
        lazy: async () => ({ Component: (await import('../routes/EpisodePreviewPage')).EpisodePreviewPage }),
      },
      {
        path: '/change-sets/:changeSetId',
        lazy: async () => ({
          Component: (await import('../routes/ChangeSetReviewPage')).ChangeSetReviewPage,
        }),
      },
      {
        path: '/tasks',
        lazy: async () => ({ Component: (await import('../routes/TaskCenterPage')).TaskCenterPage }),
      },
      {
        path: '/settings',
        lazy: async () => ({ Component: (await import('../routes/SettingsLayout')).SettingsLayout }),
        children: [
          {
            index: true,
            lazy: async () => ({
              Component: (await import('../routes/GeneralSettingsPage')).GeneralSettingsPage,
            }),
          },
          {
            path: 'account',
            lazy: () => import('../routes/ShotForwardAccountSettingsPage'),
          },
          {
            path: 'agent',
            lazy: () => import('../routes/AgentServiceSettingsPage'),
          },
          {
            path: 'runtime',
            lazy: async () => ({
              Component: (await import('../routes/RuntimeSettingsPage')).RuntimeSettingsPage,
            }),
          },
          {
            path: 'agent-templates',
            lazy: async () => ({
              Component: (await import('../routes/AgentTemplatesSettingsPage')).AgentTemplatesSettingsPage,
            }),
          },
          {
            path: 'providers',
            lazy: async () => ({
              Component: (await import('../routes/ProvidersSettingsPage')).ProvidersSettingsPage,
            }),
          },
          {
            path: 'privacy',
            lazy: async () => ({
              Component: (await import('../routes/PrivacySettingsPage')).PrivacySettingsPage,
            }),
          },
        ],
      },
      { path: '*', element: <Navigate to="/projects" replace /> },
    ],
  },
]);

type SessionState = 'checking' | 'authenticated' | 'pairing';

interface DesktopBootstrap {
  pairingCode: string;
  instanceID: string;
}

declare global {
  interface Window {
    __SHOTFORWARD_DESKTOP_BOOTSTRAP__?: DesktopBootstrap;
  }
}

function takeDesktopBootstrap(): DesktopBootstrap | undefined {
  const bootstrap = window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__;
  delete window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__;
  if (
    !bootstrap ||
    !/^[A-Z0-9]{6}$/.test(bootstrap.pairingCode) ||
    typeof bootstrap.instanceID !== 'string' ||
    bootstrap.instanceID.length === 0
  ) {
    return undefined;
  }
  return bootstrap;
}

let pendingDesktopBootstrap: DesktopBootstrap | undefined;
let desktopBootstrapPromise: Promise<void> | undefined;

function captureDesktopBootstrap(): void {
  const bootstrap = takeDesktopBootstrap();
  if (!pendingDesktopBootstrap && bootstrap) pendingDesktopBootstrap = bootstrap;
}

function discardDesktopBootstrap(): void {
  pendingDesktopBootstrap = undefined;
  delete window.__SHOTFORWARD_DESKTOP_BOOTSTRAP__;
}

function establishDesktopSession(): Promise<void> | undefined {
  if (desktopBootstrapPromise) return desktopBootstrapPromise;
  const bootstrap = pendingDesktopBootstrap;
  if (!bootstrap) return undefined;
  pendingDesktopBootstrap = undefined;
  const attempt = pair(bootstrap.pairingCode);
  desktopBootstrapPromise = attempt;
  const clearAttempt = () => {
    if (desktopBootstrapPromise === attempt) desktopBootstrapPromise = undefined;
  };
  void attempt.then(clearAttempt, clearAttempt);
  return desktopBootstrapPromise;
}

export function App() {
  captureDesktopBootstrap();
  const [session, setSession] = useState<SessionState>('checking');

  useEffect(() => {
    let active = true;
    let startupComplete = false;
    const stopListening = onSessionExpired(() => {
      if (!active || !startupComplete) return;
      queryClient.clear();
      setSession('pairing');
    });
    void restoreSession()
      .catch(async (error: unknown) => {
        if (!(error instanceof APIError) || error.code !== 'authentication_required') throw error;
        const desktopSession = establishDesktopSession();
        if (!desktopSession) throw error;
        clearCSRFToken();
        await desktopSession;
      })
      .then(() => {
        discardDesktopBootstrap();
        startupComplete = true;
        if (active) setSession('authenticated');
      })
      .catch((error: unknown) => {
        if (!active) return;
        if (error instanceof APIError && error.code === 'authentication_required') clearCSRFToken();
        discardDesktopBootstrap();
        startupComplete = true;
        setSession('pairing');
      });
    return () => {
      active = false;
      stopListening();
    };
  }, []);

  if (session === 'checking') {
    return <AppLoading />;
  }
  if (session === 'pairing') {
    return (
      <Suspense fallback={<AppLoading />}>
        <PairingPage onPaired={() => setSession('authenticated')} />
      </Suspense>
    );
  }
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}
