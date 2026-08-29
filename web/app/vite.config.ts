import { resolve } from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { proxyAuthority, rewriteProxyAuthority } from './viteProxyAuthority.ts';

const controlTarget = 'http://127.0.0.1:19091';
const controlAuthority = proxyAuthority(controlTarget);

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
    // The control plane is same-origin in production. During development the
    // app reaches the real local web bridge through this proxy as well.
    proxy: {
      '/v1': {
        target: controlTarget,
        changeOrigin: true,
        configure(proxy) {
          proxy.on('proxyReq', (proxyRequest) => {
            rewriteProxyAuthority(proxyRequest, controlAuthority);
          });
        },
      },
    },
  },
});
