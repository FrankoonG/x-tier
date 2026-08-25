import '../../ui-framework/src/styles/index.css';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ThemeProvider, ToastProvider, TooltipProvider } from '@stratum/ui';
import { App } from './App';
import './app.css';

const root = document.getElementById('root');
if (!root) throw new Error('#root is missing from index.html');

createRoot(root).render(
  <StrictMode>
    {/* `system` by default: an operator's OS preference is a real preference,
      * and a panel that ignores it at 3am is a panel they resent. */}
    <ThemeProvider defaultPreference="system" storageKey="xtier.theme">
      <TooltipProvider>
        {/*
         * Bottom-right, and deliberately short-lived.
         *
         * A change in this panel is confirmed by the configuration itself — the
         * revision in the topbar moves and the affected screen re-reads. The
         * toast exists for the case where the operator's eye is somewhere else
         * on the page when that happens, so it says what changed and gets out
         * of the way. Anything needing a decision belongs in the dialog that is
         * already open, not in a corner that vanishes.
         */}
        <ToastProvider placement="bottom-right" duration={4000} limit={3}>
          <App />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
