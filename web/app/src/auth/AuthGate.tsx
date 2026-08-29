/* ===========================================================================
 * THE GATE
 *
 * Decides whether the application is mounted at all.
 *
 * It has to sit ABOVE ControlProvider, not inside it. That provider polls the
 * node's status the moment it mounts, and every one of those routes is
 * session-protected: mounting it unauthenticated would fire a burst of requests
 * that can only 401, and would put the application's own chrome on screen for
 * however long that burst takes. Rendering nothing until the session is known
 * is what "no flash" actually requires — a gate one level lower cannot deliver
 * it, however carefully it hides things.
 *
 * The same placement is what makes sign-out clean: unmounting the provider
 * disposes every poll, every timer and every cached read with it, so the next
 * operator to sign in starts from an empty panel rather than the previous
 * operator's data.
 * ======================================================================== */

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react';
import { Button, Card, Mark } from '@stratum/ui';
import { clearJournal } from '../api/control.ts';
import type { FailureView } from '../api/errors.ts';
import {
  destroySession,
  clearSessionLogoutPending,
  isSessionFailure,
  isSessionSuperseded,
  onControlSessionLost,
  readSession,
  sessionLogoutPending,
} from '../api/session.ts';
import { FailureNotice } from '../components/FailureNotice.tsx';
import { LoginScreen } from './LoginScreen.tsx';
import { describeSessionFailure } from './sessionFailure.ts';
import './login.css';

/**
 * How long the probe may run before the screen admits to being busy.
 *
 * On a local daemon the session check resolves in a few milliseconds. Painting
 * a spinner for those milliseconds is itself the flash the requirement is
 * about, so below this threshold the screen shows nothing at all and the
 * application simply appears.
 */
const SPINNER_DELAY_MS = 180;
const SIGN_OUT_TIMEOUT_MS = 10_000;

type Phase =
  | { name: 'checking' }
  | { name: 'signing_out' }
  | { name: 'logout_failed'; notice: FailureView }
  | { name: 'anonymous'; notice: FailureView | null; retryProbe: boolean }
  | { name: 'authenticated' };

export interface AuthGateProps {
  /** The application. Rendered only while a session is held. */
  children: (signOut: () => void) => ReactNode;
}

export function AuthGate({ children }: AuthGateProps) {
  const [phase, setPhase] = useState<Phase>({ name: 'checking' });
  const [probeToken, setProbeToken] = useState(0);

  const checking = phase.name === 'checking';

  /* -- The initial probe --------------------------------------------------
   * Aborted on unmount so a slow answer cannot set state into a torn-down
   * tree, and re-run whenever `probeToken` changes — which is what the login
   * screen's Retry does. */
  useEffect(() => {
    if (!checking) return;
    const controller = new AbortController();
    let live = true;

    void (async () => {
      if (sessionLogoutPending()) {
        const outcome = await destroySession(controller.signal);
        if (!live) return;

        if (outcome.kind === 'ended') {
          clearJournal();
          setPhase({ name: 'anonymous', notice: null, retryProbe: false });
          return;
        }
        if (isSessionSuperseded(outcome)) {
          setProbeToken((token) => token + 1);
          return;
        }
        const failure = isSessionFailure(outcome) ? outcome : SESSION_CHANGED;
        setPhase({ name: 'logout_failed', notice: describeSessionFailure(failure) });
        return;
      }

      const probe = await readSession(controller.signal);
      if (!live) return;

      if (probe.kind === 'authenticated') {
        setPhase({ name: 'authenticated' });
        return;
      }
      if (isSessionSuperseded(probe)) {
        setProbeToken((token) => token + 1);
        return;
      }
      setPhase({
        name: 'anonymous',
        notice: isSessionFailure(probe) ? describeSessionFailure(probe) : null,
        retryProbe: isSessionFailure(probe),
      });
    })();

    return () => {
      live = false;
      controller.abort();
    };
  }, [checking, probeToken]);

  /* -- Sessions that end without being asked to ---------------------------
   * The transport raises this when the bridge stops accepting our cookie. It
   * can fire several times over — one per request in flight when the session
   * died — so the transition has to be idempotent, and it must not overwrite a
   * notice the operator is already reading. */
  useEffect(() => {
    return onControlSessionLost(() => {
      setPhase((current) => {
        if (current.name === 'signing_out' || current.name === 'logout_failed') {
          clearSessionLogoutPending();
          clearJournal();
          return { name: 'anonymous', notice: null, retryProbe: false };
        }
        if (current.name !== 'authenticated') return current;
        clearJournal();
        return { name: 'anonymous', notice: describeSessionFailure(EXPIRED), retryProbe: false };
      });
    });
  }, []);

  const signOut = useSignOut(setPhase);

  const retryProbe = useCallback(() => {
    setPhase({ name: 'checking' });
    setProbeToken((token) => token + 1);
  }, []);

  const authenticate = useCallback(() => {
    setPhase({ name: 'authenticated' });
  }, []);

  if (checking || phase.name === 'signing_out') {
    return <CheckingScreen label={checking ? 'Checking session' : 'Signing out'} />;
  }

  if (phase.name === 'anonymous') {
    return (
      <LoginScreen
        onAuthenticated={authenticate}
        entryNotice={phase.notice}
        onRetryEntry={phase.notice && phase.retryProbe ? retryProbe : undefined}
      />
    );
  }

  if (phase.name === 'logout_failed') {
    return <LogoutFailedScreen notice={phase.notice} onRetry={signOut} />;
  }

  return <>{children(signOut)}</>;
}

/**
 * Sign-out hides operational state immediately, but remains unconfirmed until
 * the bridge proves the server-side session ended. A timeout can mean either
 * outcome, so it keeps the local proof for an explicit retry instead of
 * claiming the browser is safely signed out.
 *
 * The journal goes with it. It holds a redacted record of what the last person
 * did, and it has no business surviving into the next person's session.
 */
function useSignOut(setPhase: Dispatch<SetStateAction<Phase>>) {
  // A ref, not state: nothing renders differently while the DELETE is in
  // flight, and a second click during it must be dropped rather than queued.
  const running = useRef(false);

  return useCallback(() => {
    if (running.current) return;
    running.current = true;
    clearJournal();
    setPhase({ name: 'signing_out' });

    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), SIGN_OUT_TIMEOUT_MS);

    void (async () => {
      try {
        const outcome = await destroySession(controller.signal);
        if (outcome.kind === 'ended') {
          setPhase({ name: 'anonymous', notice: null, retryProbe: false });
          return;
        }
        const failure = isSessionFailure(outcome) ? outcome : SESSION_CHANGED;
        setPhase((current) => current.name === 'anonymous'
          ? current
          : { name: 'logout_failed', notice: describeSessionFailure(failure) });
      } catch (error) {
        const failure = {
          kind: 'unreachable',
          code: 'control.unreachable',
          detail: error instanceof Error ? error.message : String(error),
        } as const;
        setPhase((current) => current.name === 'anonymous'
          ? current
          : { name: 'logout_failed', notice: describeSessionFailure(failure) });
      } finally {
        window.clearTimeout(timeout);
        running.current = false;
      }
    })();
  }, [setPhase]);
}

function LogoutFailedScreen({ notice, onRetry }: { notice: FailureView; onRetry: () => void }) {
  return (
    <main className="xtier-login">
      <div className="xtier-login__column">
        <div className="xtier-login__brand">
          <Mark size={26} />
          <span className="xtier-login__wordmark">X-Tier</span>
        </div>
        <Card className="xtier-login__card" padding="lg">
          <Card.Body className="xtier-login__body">
            <h1 className="xtier-login__title">Sign-out not confirmed</h1>
            <FailureNotice
              failure={notice}
              actions={(
                <Button size="sm" variant="default" onClick={onRetry}>
                  Retry sign out
                </Button>
              )}
            />
          </Card.Body>
        </Card>
      </div>
    </main>
  );
}

/**
 * The probe's screen.
 *
 * Nothing but the mark, and only after the delay. The mark is what the spinner
 * is built from, so the busy state here is the product's own glyph rather than
 * a generic one bolted on.
 */
function CheckingScreen({ label }: { label: string }) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => setVisible(true), SPINNER_DELAY_MS);
    return () => clearTimeout(timer);
  }, []);

  if (!visible) return null;

  return (
    <div className="xtier-login__checking" role="status" aria-label={label}>
      <Mark size={40} loading="spiral" />
    </div>
  );
}

/**
 * Not a bridge code — the panel observed this, the daemon did not report it.
 * It is catalogued under the bridge's own session code because that is exactly
 * what happened and what the operator should read.
 */
const EXPIRED = {
  kind: 'refused',
  code: 'webbridge.session_invalid',
  detail: 'The session ended. Sign in to continue.',
} as const;

const SESSION_CHANGED = {
  kind: 'refused',
  code: 'control.session_changed',
  detail: 'Browser session authority changed while the request was in flight.',
} as const;
