/* ===========================================================================
 * SIGN IN
 *
 * One field and one button, which is the whole point. Everything that could go
 * wrong is reported in the panel's existing vocabulary — the same FailureNotice
 * and the same error codes the operational screens use — so nothing here has to
 * be learned separately.
 *
 * WHERE THE SECRET LIVES
 *
 * In the input element, and nowhere else.
 *
 * The field is uncontrolled on purpose. A controlled input would put the
 * credential into React state, and from there into every render, every devtools
 * inspection of the component tree, and every error boundary that serializes
 * props on the way past. Reading `ref.current.value` at submit keeps it in the
 * one place it has to exist anyway — the DOM node the operator typed into —
 * for as long as the submission is in flight, and no longer.
 *
 * No state on this screen is derived from the credential, not even whether it
 * is empty.
 * ======================================================================== */

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Button, Card, Field, Mark, PasswordInput, setNativeInputValue } from '@stratum/ui';
import { FailureNotice } from '../components/FailureNotice.tsx';
import { describeFailure, type FailureView } from '../api/errors.ts';
import { createSession, type LoginOutcome } from '../api/session.ts';
import { describeSessionFailure } from './sessionFailure.ts';
import './login.css';

const SIGN_IN_TIMEOUT_MS = 15_000;

export interface LoginScreenProps {
  /** Called once the bridge has accepted a credential. */
  onAuthenticated: () => void;
  /**
   * Why the operator is looking at this screen — an expired session, a failed
   * sign-out, a session probe that could not complete. Distinct from anything
   * this screen produces itself, and cleared as soon as they act.
   */
  entryNotice?: FailureView | null;
  /** Re-runs whatever produced `entryNotice`. */
  onRetryEntry?: () => void;
}

/** What the last submission produced. Never holds anything secret. */
type Attempt =
  | { status: 'idle' }
  | { status: 'pending' }
  /** A verdict on the value: belongs on the field, not in a banner. */
  | { status: 'rejected'; message: string }
  /** A verdict on the request: belongs in a banner, the value is still good. */
  | { status: 'failed'; failure: FailureView; retryAfterSeconds: number | null };

export function LoginScreen({ onAuthenticated, entryNotice, onRetryEntry }: LoginScreenProps) {
  const inputRef = useRef<HTMLInputElement | null>(null);
  const submittingRef = useRef(false);
  const [attempt, setAttempt] = useState<Attempt>({ status: 'idle' });
  const [revealed, setRevealed] = useState(false);
  const [dismissedEntry, setDismissedEntry] = useState(false);
  const [blockedUntil, setBlockedUntil] = useState<number | null>(null);
  const [clock, setClock] = useState(() => Date.now());

  const pending = attempt.status === 'pending';
  const retryAfterSeconds = blockedUntil === null
    ? null
    : Math.max(0, Math.ceil((blockedUntil - clock) / 1000));
  const blocked = retryAfterSeconds !== null && retryAfterSeconds > 0;

  useEffect(() => {
    if (blockedUntil === null) return undefined;
    const tick = () => {
      const now = Date.now();
      setClock(now);
      if (now >= blockedUntil) setBlockedUntil(null);
    };
    tick();
    const timer = window.setInterval(tick, 250);
    return () => window.clearInterval(timer);
  }, [blockedUntil]);

  /**
   * Empties the field.
   *
   * `setNativeInputValue` rather than `input.value = ''` because the assignment
   * has to go through the native setter to produce an `input` event; without
   * one, a password manager that is watching the field keeps the old value and
   * the reveal toggle can be left showing a string that is no longer there.
   */
  function clearCredential() {
    setRevealed(false);
    const input = inputRef.current;
    if (input) setNativeInputValue(input, '');
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // Enter submits natively and does not pass through the button's own guard,
    // so re-entry is refused here rather than there.
    if (submittingRef.current || pending || blocked) return;

    const input = inputRef.current;
    if (!input) return;

    let credential = input.value;
    if (!credential) {
      setRevealed(false);
      setAttempt({ status: 'rejected', message: 'Enter the panel credential.' });
      input.focus();
      return;
    }

    submittingRef.current = true;
    setDismissedEntry(true);
    setAttempt({ status: 'pending' });

    let outcome: LoginOutcome;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), SIGN_IN_TIMEOUT_MS);
    try {
      outcome = await createSession(credential, controller.signal);
    } catch (error) {
      // createSession does not throw by contract; this is the belt on the
      // braces, and it must not leave the button spinning forever. Handed to
      // the panel's own describer rather than the session one: a bug in this
      // file is not a session failure, and `panel.exception` is the view that
      // says so.
      setAttempt({ status: 'failed', failure: describeFailure(error), retryAfterSeconds: null });
      input.focus();
      return;
    } finally {
      window.clearTimeout(timeout);
      submittingRef.current = false;
      credential = '';
      clearCredential();
    }

    if (outcome.kind === 'authenticated') {
      // Cleared before the transition, not left to unmounting. The value should
      // be gone from the DOM whether or not this component survives the frame.
      setBlockedUntil(null);
      setAttempt({ status: 'idle' });
      onAuthenticated();
      return;
    }

    if (outcome.kind === 'rejected') {
      // The server ruled on this exact string. Retrying it verbatim cannot
      // work, so it goes.
      setAttempt({ status: 'rejected', message: outcome.detail });
      input.focus();
      return;
    }

    /*
     * Everything below is a failure of the request, not of the credential.
     * The value still leaves the DOM because retaining a panel credential is a
     * worse failure mode than asking the operator to type it again.
     */
    if (outcome.kind === 'rate_limited') {
      setAttempt({
        status: 'failed',
        failure: describeSessionFailure({
          kind: 'refused',
          code: outcome.code,
          detail: outcome.detail,
        }),
        retryAfterSeconds: outcome.retryAfterSeconds,
      });
      setClock(Date.now());
      setBlockedUntil(outcome.retryAfterSeconds === null
        ? null
        : Date.now() + outcome.retryAfterSeconds * 1000);
      input.focus();
      return;
    }

    setAttempt({
      status: 'failed',
      failure: describeSessionFailure(outcome.kind === 'superseded' ? SESSION_CHANGED : outcome),
      retryAfterSeconds: null,
    });
    input.focus();
  }

  const notice =
    attempt.status === 'failed'
      ? {
          ...attempt.failure,
          detail: withWait(attempt.failure.detail, retryAfterSeconds),
        }
      : !dismissedEntry && entryNotice
        ? entryNotice
        : null;

  const showEntryRetry = notice !== null && notice === entryNotice && onRetryEntry !== undefined;

  return (
    <main className="xtier-login">
      <div className="xtier-login__column">
        {/* Outside the card, because it names the product rather than the task.
         * The card holds the one thing there is to do. */}
        <div className="xtier-login__brand">
          <Mark size={26} />
          <span className="xtier-login__wordmark">X-Tier</span>
        </div>

        {/* The panel's own surface, on the panel's own backdrop. Every other
         * screen puts its content on a Card; a login that floated bare on the
         * background would be the one screen that did not look like the
         * product it opens. */}
        <Card className="xtier-login__card" padding="lg">
          <Card.Body className="xtier-login__body">
            <h1 className="xtier-login__title">Sign in</h1>

            {notice ? (
              <FailureNotice
                failure={notice}
                actions={
                  showEntryRetry ? (
                    /* The same button the operational screens put on a failed
                     * read, down to the wording: a retry is a retry wherever it
                     * appears. */
                    <Button size="sm" variant="default" onClick={onRetryEntry}>
                      Try again
                    </Button>
                  ) : undefined
                }
              />
            ) : null}

            <form className="xtier-login__form" onSubmit={submit} noValidate>
              <Field
                label="Panel credential"
                error={attempt.status === 'rejected' ? attempt.message : undefined}
              >
                <PasswordInput
                  ref={inputRef}
                  name="credential"
                  autoComplete="current-password"
                  autoFocus
                  size="md"
                  fullWidth
                  revealed={revealed}
                  onRevealedChange={setRevealed}
                />
              </Field>

              <Button
                type="submit"
                variant="primary"
                size="md"
                fullWidth
                loading={pending}
                disabled={blocked}
                loadingLabel="Signing in"
              >
                Sign in
              </Button>
            </form>
          </Card.Body>
        </Card>
      </div>
    </main>
  );
}

const SESSION_CHANGED = {
  kind: 'refused',
  code: 'control.session_changed',
  detail: 'Browser session authority changed while sign-in was in flight.',
} as const;

/** Appends the bridge's own wait to its message, when it gave one. */
function withWait(detail: string, seconds: number | null): string {
  if (seconds === null) return detail;
  const wait = seconds < 60
    ? `${seconds} second${seconds === 1 ? '' : 's'}`
    : `${Math.ceil(seconds / 60)} minute${Math.ceil(seconds / 60) === 1 ? '' : 's'}`;
  return `${detail} Try again in ${wait}.`;
}
