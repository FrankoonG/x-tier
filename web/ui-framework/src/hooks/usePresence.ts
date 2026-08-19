import { useCallback, useEffect, useRef, useState } from 'react';

export type PresenceState = 'entering' | 'entered' | 'exiting' | 'exited';

export interface UsePresenceResult {
  /** Whether the element should be rendered at all. */
  isPresent: boolean;
  /**
   * Attach to the animated element. The hook reads running animations off
   * this node to decide when an exit has finished.
   */
  ref: (node: HTMLElement | null) => void;
  /** Publish as `data-state` so CSS can target each phase. */
  state: PresenceState;
}

/**
 * Keeps an element mounted until its exit animation has actually finished.
 *
 * WHY NOT AnimatePresence
 * -----------------------
 * framer-motion's AnimatePresence snapshots the React tree at exit time, so
 * prop updates never reach an exiting subtree. hy2scale had to work around
 * that twice in one file: a 500ms `setTimeout` guessing the animation length,
 * plus direct DOM mutation to kill pointer events on the way out
 * (`ui-framework/src/components/Modal.tsx:38-48`). It also silently does
 * nothing when its direct child is not the animating element — which is why
 * hy2scale's TreeTable row exit animations never ran despite being written
 * (`TreeTable.tsx:114` wraps `<tbody>`, not the rows).
 *
 * HOW THIS WORKS INSTEAD
 * ----------------------
 * Entering is pure CSS: render the element with `data-state="entering"`, let
 * `@starting-style` supply the from-values, and the browser interpolates.
 * No JS is involved and no duration is duplicated in two places.
 *
 * Exiting flips `data-state` to `"exiting"`, then asks the DOM what is
 * actually running via `element.getAnimations({ subtree: true })` and waits on
 * those `finished` promises. That means:
 *   - the duration lives only in CSS; changing it needs no JS change,
 *   - nested/child animations are awaited too,
 *   - an element with no animation unmounts on the next frame rather than
 *     after an arbitrary timeout,
 *   - reduced-motion, which shortens the CSS durations, is honoured for free.
 *
 * A generous safety timeout guards against an animation that never settles
 * (an interrupted transition, a background tab throttling rAF), because a
 * stuck element is worse than a slightly early unmount.
 */
export function usePresence(present: boolean, exitTimeoutMs = 1200): UsePresenceResult {
  const [isPresent, setIsPresent] = useState(present);
  const [state, setState] = useState<PresenceState>(present ? 'entered' : 'exited');
  const nodeRef = useRef<HTMLElement | null>(null);
  // Guards against a stale exit resolving after a re-open has already begun.
  const exitToken = useRef(0);
  // True until the element has been absent at least once. Something that is
  // already present on the very first commit has no entry to animate, and
  // driving it through `entering` would run the enter transition backwards on
  // content the consumer asked to be open from the start — a visible
  // flash-collapse for `<Disclosure defaultOpen unmountOnClose>`. Keyed on
  // "has been absent" rather than "first effect run" so StrictMode's
  // mount/unmount/remount cycle cannot flip it.
  const neverAbsent = useRef(present);

  const ref = useCallback((node: HTMLElement | null) => {
    nodeRef.current = node;
  }, []);

  useEffect(() => {
    if (present) {
      exitToken.current += 1;
      setIsPresent(true);
      // Present since the first commit: settle straight into `entered`. CSS
      // that attaches `@starting-style` to the bare selector (Toast, Banner)
      // still gets its entry transition, because that depends on the element
      // being newly inserted rather than on `data-state`.
      if (neverAbsent.current) {
        setState('entered');
        return;
      }
      setState('entering');
      // One frame later the element exists and `@starting-style` has been
      // applied, so promoting to `entered` triggers the transition.
      const raf = requestAnimationFrame(() => {
        requestAnimationFrame(() => setState('entered'));
      });
      return () => cancelAnimationFrame(raf);
    }

    neverAbsent.current = false;

    if (!isPresent) return;

    const token = ++exitToken.current;
    setState('exiting');

    let timer: ReturnType<typeof setTimeout>;
    let cancelled = false;

    // Wait a frame so `data-state="exiting"` has been committed and the exit
    // animations have been created before we enumerate them.
    const raf = requestAnimationFrame(() => {
      const node = nodeRef.current;
      const finish = () => {
        if (cancelled || token !== exitToken.current) return;
        setIsPresent(false);
        setState('exited');
      };

      if (!node || typeof node.getAnimations !== 'function') {
        finish();
        return;
      }

      const running = node
        .getAnimations({ subtree: true })
        .filter((a) => a.playState === 'running' || a.playState === 'paused');

      if (running.length === 0) {
        finish();
        return;
      }

      // allSettled, not all: a cancelled animation rejects, and a cancelled
      // exit still means the element is done animating out.
      void Promise.allSettled(running.map((a) => a.finished)).then(finish);
      timer = setTimeout(finish, exitTimeoutMs);
    });

    return () => {
      cancelled = true;
      cancelAnimationFrame(raf);
      clearTimeout(timer);
    };
  }, [present, isPresent, exitTimeoutMs]);

  return { isPresent, ref, state };
}
