/* ===========================================================================
 * PANEL STATE
 *
 * Small on purpose. The panel has one piece of genuinely shared state — the
 * config REVISION — and everything else is screen-local.
 *
 * The revision matters globally because it is a compare-and-swap token: every
 * mutation states the revision it was composed against and is rejected if the
 * store has moved on. That makes it the one value a screen cannot own, since a
 * write on one screen invalidates a form open on another.
 *
 * Runtime and revision state are refreshed while the page is visible. The
 * timestamp remains explicit so the UI never presents polling as an event
 * stream or a stronger freshness guarantee than it is.
 * ======================================================================== */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import {
  CommandFailure,
  TransportFailure,
  executeMutation,
  getDaemonStatus,
  getLocalStatus,
  type DomainMutation,
} from '../api/control';
import type { DaemonStatus, LocalStatus } from '../api/types';
import { createRefreshCoordinator } from './refreshCoordinator';

/** What the panel knows about the daemon, and how sure it is. */
export interface ControlState {
  /** Config-plane snapshot. Present whenever the config plane is readable. */
  local: LocalStatus | null;
  /**
   * Runtime-plane snapshot, or the reason there isn't one. `null` here means
   * NOT OBSERVED — the daemon was unreachable — and must never render as
   * "stopped".
   */
  daemon: DaemonStatus | null;
  daemonError: string | null;
  localError: string | null;
  loading: boolean;
  /** When the panel last actually looked. Null until the first read. */
  fetchedAt: Date | null;
}

interface ControlApi extends ControlState {
  /** Current CAS token, or 0 before the first successful read. */
  revision: number;
  /**
   * Whether `revision` is a READING or a placeholder.
   *
   * `revision` falls back to 0 before the first read and when both planes
   * fail — and 0 is also a perfectly real revision on a fresh node, so the
   * number alone cannot be told apart from the absence of one. Anything that
   * DISPLAYS the revision must gate on this; the wire path must not, because
   * 0 is the correct CAS token to send against a node that has never been
   * written.
   */
  revisionRead: boolean;
  /**
   * Bumped on every refresh, including one that returns identical data.
   *
   * Screen-local reads depend on this so that ONE "Re-read" control means "go
   * and look again at everything on this screen". Keying them on `revision`
   * alone is not enough: a re-read that finds nothing changed leaves the
   * revision where it was, so a screen watching only the revision would sit on
   * data it never actually re-fetched while the operator watched a button
   * spin.
   *
   * The alternative — a Re-read button per card — puts two identical controls
   * on screen with different scopes and no way to tell them apart.
   */
  epoch: number;
  refresh: () => Promise<void>;
  /**
   * Run a typed domain mutation with the current revision attached.
   *
   * Conflicts are NOT retried. A conflict means another writer changed the
   * config between this form being opened and submitted; retrying would
   * overwrite an intent this panel never saw. The caller is handed the failure
   * so it can re-read and let the operator decide.
   */
  mutate: <T>(
    operation: DomainMutation,
    opts?: {
      dryRun?: boolean;
      /**
       * Reuse an id to retry one intent after a lost response. The live daemon
       * may return its cached outcome; the original revision keeps a replay
       * fail-closed after process restart or cache expiry.
       */
      requestId?: string;
      /**
       * Send this revision instead of the current one.
       *
       * Only for an idempotent resend. The daemon's cache key is the request
       * ID but its equality check is the WHOLE request, so a resend carrying
       * the same id with a moved-on revision is a different request and is
       * rejected 409. Replaying the original revision is what makes the
       * resend actually replay.
       */
      revision?: number;
    },
  ) => Promise<T>;
}

const Ctx = createContext<ControlApi | null>(null);

export function ControlProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<ControlState>({
    local: null,
    daemon: null,
    daemonError: null,
    localError: null,
    loading: false,
    fetchedAt: null,
  });

  const [epoch, setEpoch] = useState(0);

  /**
   * The revision, readable synchronously.
   *
   * `mutate` MUST NOT close over the rendered value. A caller that does
   * `await refresh(); await mutate(...)` — which is exactly what the mutation
   * dialog's "Re-read and check again" does — is still executing inside the
   * render that raised the conflict, so the rendered `revision` it captured is
   * the stale one. The re-check then re-sends the revision that just lost,
   * conflicts again, and the button can never succeed no matter how many times
   * it is pressed.
   *
   * The ref is written in the same statement that sets state, so anything
   * awaiting `refresh()` sees the new value immediately rather than one render
   * later.
   */
  const revisionRef = useRef(0);

  const performRefresh = useCallback(async () => {
    setState((s) => ({ ...s, loading: true }));

    // Deliberately independent. The config plane is readable from disk while
    // the daemon is stopped, so one failing must not blank the other.
    const [localResult, daemonResult] = await Promise.allSettled([
      getLocalStatus(),
      getDaemonStatus(),
    ]);

    const describe = (err: unknown): string => {
      if (err instanceof CommandFailure) return err.code;
      if (err instanceof TransportFailure) return 'control.unreachable';
      return 'control.unknown_error';
    };

    const local = localResult.status === 'fulfilled' ? localResult.value : null;
    const daemon = daemonResult.status === 'fulfilled' ? daemonResult.value : null;

    // Written before the state update, so a caller awaiting `refresh()` reads
    // the new revision on the very next line rather than after a render.
    revisionRef.current = local?.revision ?? daemon?.revision ?? 0;

    setState({
      local,
      localError: localResult.status === 'rejected' ? describe(localResult.reason) : null,
      daemon,
      daemonError: daemonResult.status === 'rejected' ? describe(daemonResult.reason) : null,
      loading: false,
      fetchedAt: new Date(),
    });
    setEpoch((e) => e + 1);
  }, []);

  const coordinatorRef = useRef<ReturnType<typeof createRefreshCoordinator> | null>(null);
  coordinatorRef.current ??= createRefreshCoordinator(performRefresh);
  const refresh = useCallback(() => coordinatorRef.current!.refresh(), []);
  const refreshFresh = useCallback(() => coordinatorRef.current!.refreshFresh(), []);

  useEffect(() => {
    let timer: number | null = null;
    let active = true;

    const stop = () => {
      if (timer === null) return;
      window.clearTimeout(timer);
      timer = null;
    };
    const schedule = () => {
      if (!active || timer !== null || document.visibilityState !== 'visible') return;
      timer = window.setTimeout(() => {
        timer = null;
        void refresh().then(schedule, schedule);
      }, 5_000);
    };
    const onVisibilityChange = () => {
      if (document.visibilityState !== 'visible') {
        stop();
        return;
      }
      void refresh().then(schedule, schedule);
    };

    document.addEventListener('visibilitychange', onVisibilityChange);
    if (document.visibilityState === 'visible') {
      void refresh().then(schedule, schedule);
    }

    return () => {
      active = false;
      document.removeEventListener('visibilitychange', onVisibilityChange);
      stop();
    };
  }, [refresh]);

  const revision = state.local?.revision ?? state.daemon?.revision ?? 0;
  // Either plane carries the revision, so one answer is enough to make the
  // number above a reading rather than the fallback.
  const revisionRead = state.local !== null || state.daemon !== null;

  const mutate = useCallback(
    async <T,>(
      operation: DomainMutation,
      opts?: { dryRun?: boolean; requestId?: string; revision?: number },
    ): Promise<T> => {
      try {
        const result = await executeMutation<T>(operation, {
          // From the ref, never from the render — see the note on
          // `revisionRef`. An explicit override exists only for an idempotent
          // resend, which must reproduce the original request exactly.
          revision: opts?.revision ?? revisionRef.current,
          dryRun: opts?.dryRun,
          requestId: opts?.requestId,
        });
        // A dry run changes nothing, so re-reading would only cost a round trip
        // and make the preview feel slower than it is.
        if (!opts?.dryRun) await refreshFresh();
        return result;
      } catch (err) {
        // The one error whose write landed. Re-read so the panel shows the new
        // truth, then re-throw so the caller can report it honestly rather
        // than claiming a clean success.
        if (err instanceof CommandFailure && err.isAppliedDespiteError) await refreshFresh();
        throw err;
      }
    },
    [refreshFresh],
  );

  const value = useMemo<ControlApi>(
    () => ({ ...state, revision, revisionRead, epoch, refresh, mutate }),
    [state, revision, revisionRead, epoch, refresh, mutate],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useControl(): ControlApi {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useControl must be used inside <ControlProvider>');
  return ctx;
}
