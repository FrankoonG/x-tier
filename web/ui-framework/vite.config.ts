import { resolve } from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import dts from 'vite-plugin-dts';

export default defineConfig({
  plugins: [
    react(),
    dts({ include: ['src'], rollupTypes: false, insertTypesEntry: true }),
  ],
  resolve: {
    alias: { '@': resolve(import.meta.dirname, 'src') },
  },
  build: {
    lib: {
      // Two entries. `data-table` is separate because it is the only component
      // with dependencies of its own — see src/data-table.ts for why a subpath
      // is the only thing that keeps those peers genuinely optional.
      entry: {
        index: resolve(import.meta.dirname, 'src/index.ts'),
        'data-table': resolve(import.meta.dirname, 'src/data-table.ts'),
      },
      formats: ['es'],
    },
    // One extracted stylesheet rather than per-component files. Consumers
    // import it once (`@stratum/ui/styles.css`); splitting it would make the
    // cascade-layer order depend on import order at the call site.
    cssCodeSplit: false,
    sourcemap: true,
    rollupOptions: {
      // Everything the consumer already has, or opts into, stays external so
      // the library never ships a second copy of React, of Floating UI, or of
      // a data-grid engine. Declared `dependencies` are installed by npm
      // automatically, so externalising them costs the consumer nothing and
      // lets the bundler dedupe against anything else that uses them.
      external: [
        'react',
        'react-dom',
        'react/jsx-runtime',
        'clsx',
        '@tanstack/react-table',
        '@tanstack/react-virtual',
        /^@floating-ui\//,
      ],
      output: {
        // Mirror the source tree instead of emitting one bundled file.
        //
        // A single `dist/index.js` defeats consumer tree-shaking: every
        // component is a module-scope `forwardRef(...)` call, which a bundler
        // cannot prove side-effect-free, so importing one Button dragged in the
        // whole library — measured at 621 kB against 204 kB for the same app
        // built from source. Preserved modules restore per-file shaking and let
        // the `sideEffects` field in package.json do its job.
        preserveModules: true,
        preserveModulesRoot: 'src',
        entryFileNames: '[name].js',
        assetFileNames: (info) =>
          info.names?.[0]?.endsWith('.css') ? 'stratum-ui.css' : '[name][extname]',
      },
    },
  },
});
