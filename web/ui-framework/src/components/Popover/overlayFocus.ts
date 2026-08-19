import { useEffect, useLayoutEffect } from 'react';

/**
 * `useLayoutEffect` in the browser, `useEffect` on the server, so importing an
 * overlay into an SSR render does not warn.
 */
export const useIsomorphicLayoutEffect = typeof document !== 'undefined' ? useLayoutEffect : useEffect;

/**
 * How long to keep waiting for the pointer to come up before giving up on the
 * deferred focus restore. Long enough for a deliberate press-and-hold, short
 * enough that an unrelated click much later never triggers it.
 */
const POINTER_SETTLE_MS = 500;

/**
 * The element focus should return to when an overlay closes.
 *
 * Dialogs and drawers have no reference element for Floating UI to infer this
 * from — they open by flipping a prop — so it is captured explicitly at open
 * time. `anchor` wins when given; otherwise whatever had focus at that moment
 * is almost always the control the user activated.
 */
export function resolveReturnFocusTarget(
  anchor: { current: HTMLElement | null } | undefined,
): HTMLElement | null {
  const explicit = anchor?.current;
  if (explicit && explicit.isConnected) return explicit;
  const active = document.activeElement;
  return active instanceof HTMLElement && active !== document.body ? active : null;
}

export interface ScheduleReturnFocusOptions {
  /** Element that should end up focused. */
  target: HTMLElement | null;
  /** The closing panel. Focus still sitting inside it counts as lost. */
  panel: HTMLElement | null;
}

/**
 * Returns focus to `target` after an overlay closes, and keeps it there.
 *
 * WHY THIS IS NOT JUST `element.focus()`
 * --------------------------------------
 * Two things get in the way, and both are silent:
 *
 * 1. The overlay usually closes *during* a pointer press — the user clicked the
 *    scrim. Chrome re-runs focus assignment when the button is RELEASED, and
 *    since the release lands on a non-focusable scrim it clears the document's
 *    focus again. A restore performed between press and release is therefore
 *    thrown away, and the user is left on `<body>`: the next Tab starts from
 *    the top of the document. So the restore is attempted immediately (which is
 *    what a keyboard or programmatic close needs) and then once more on the
 *    next pointer release.
 *
 * 2. Both attempts are guarded. Focus is only moved if it is currently nowhere
 *    — on `<body>`, on the document element, or still inside the panel that is
 *    animating out. If the user's click landed on some other focusable control,
 *    that control keeps focus and nothing is stolen from it.
 *
 * `preventScroll` is used throughout: the return target may have been scrolled
 * out of view behind the overlay, and yanking the page back to it is worse than
 * leaving the viewport where the user put it.
 */
export function scheduleReturnFocus({ target, panel }: ScheduleReturnFocusOptions): () => void {
  if (!target) return () => {};

  const run = () => {
    if (!target.isConnected) return;
    const active = document.activeElement;
    const insidePanel = panel != null && active != null && panel.contains(active);
    const lost =
      active == null ||
      active === document.body ||
      active === document.documentElement ||
      insidePanel;
    if (!lost) return;
    target.focus({ preventScroll: true });
  };

  queueMicrotask(run);

  const onPointerUp = () => {
    queueMicrotask(run);
  };
  window.addEventListener('pointerup', onPointerUp, { once: true, capture: true });
  const timer = window.setTimeout(() => {
    window.removeEventListener('pointerup', onPointerUp, true);
  }, POINTER_SETTLE_MS);

  return () => {
    window.clearTimeout(timer);
    window.removeEventListener('pointerup', onPointerUp, true);
  };
}
