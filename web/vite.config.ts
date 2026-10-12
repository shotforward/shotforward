import react from '@vitejs/plugin-react';
import { defineConfig, loadEnv } from 'vite';

export default defineConfig(({ mode }) => {
  const environment = loadEnv(mode, process.cwd(), 'SHOTFORWARD_');
  const daemonURL = environment.SHOTFORWARD_DAEMON_URL ?? 'http://127.0.0.1:32145';

  return {
    plugins: [react()],
    build: {
      outDir: '../daemon/internal/webui/dist',
      emptyOutDir: true,
      sourcemap: false,
      target: 'es2022',
      reportCompressedSize: true,
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (
              id.endsWith('/src/routes/useStoryboardOperations.ts') ||
              id.endsWith('/src/routes/storyboardVideoTask.ts')
            )
              return 'storyboard-operations';
            if (id.endsWith('/src/storyboardPromptReferences.ts')) return 'storyboard-prompt-references';
            if (id.includes('/src/i18n/locales/')) return 'locales';
            if (id.includes('/node_modules/@tanstack/react-query/')) return 'data';
            if (id.includes('/node_modules/i18next/') || id.includes('/node_modules/react-i18next/'))
              return 'i18n';
            if (
              id.includes('/node_modules/react/') ||
              id.includes('/node_modules/react-dom/') ||
              id.includes('/node_modules/react-router/')
            )
              return 'react';
          },
        },
      },
    },
    server: {
      host: '127.0.0.1',
      port: 5173,
      strictPort: true,
      proxy: {
        '/api': {
          target: daemonURL,
          changeOrigin: true,
          configure(proxy) {
            proxy.on('proxyReq', (request) => {
              request.setHeader('Origin', daemonURL);
            });
          },
        },
        '/healthz': {
          target: daemonURL,
          changeOrigin: true,
        },
      },
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      css: true,
      // Bound DOM-test contention on CI runners with limited CPU quotas.
      maxWorkers: 2,
      // Multi-step editor tests need more than Vitest's default five seconds.
      testTimeout: 30_000,
    },
  };
});
