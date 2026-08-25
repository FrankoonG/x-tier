import { resolve } from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // Source, not dist, so framework edits hot-reload without a rebuild.
      '@stratum/ui/data-table': resolve(import.meta.dirname, '../ui-framework/src/data-table.ts'),
      '@stratum/ui': resolve(import.meta.dirname, '../ui-framework/src/index.ts'),
      '@': resolve(import.meta.dirname, 'src'),
    },
  },
  server: {
    port: 5190,
    strictPort: false,
    // The control plane is same-origin in production (a local bridge on the
    // same host), so the dev server proxies to the mock daemon rather than
    // teaching the app about two different origins.
    proxy: {
      '/v1': { target: 'http://127.0.0.1:19091', changeOrigin: false },
    },
  },
});
