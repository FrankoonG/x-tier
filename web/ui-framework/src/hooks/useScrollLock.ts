import { useEffect, type RefObject } from 'react';

/**
 * Blocks scrolling everywhere except inside one subtree, while a modal layer is
 * open.
 *
 * WHY THIS EXISTS
 * ---------------
 * Floating UI's `<FloatingOverlay lockScroll>` sets `document.body.style
 * .overflow = 'hidden'` and nothing else. That is correct for a page that
 * scrolls the document — and useless for an application shell, where the
 * scroll container is an inner element (`AppShell`'s `<main>`, a `ScrollArea`,
 * a table viewport). In that layout the body never scrolls in the first place,
 * so the lock is a no-op and the content behind an open dialog scrolls freely
 * under the scrim. Since an app shell is exactly what this library is for, the
 * default lock covers approximately none of its real usage.
 *
 * HOW IT WORKS
 * ------------
 * Rather than trying to find and freeze every scrollable ancestor — which
 * cannot work, because the modal is in a portal and shares no ancestor with the
 * scroller — this intercepts the scroll *intent* at the document, in the
 * capture phase, and cancels it unless it originated inside the permitted
 * subtree. That is container-agnostic: it holds for the document, for an inner
 * div, for a shadow-DOM scroller, and for containers that did not exist when
 * the lock was installed.
 *
 * Listeners must be non-passive (`passive: false`) to be allowed to call
 * `preventDefault`; Chrome treats document-level `wheel`/`touchmove` as passive
 * by default precisely because cancelling them is expensive, so this is opt-in.
 *
 * Nested layers reference-count through a module-level stack, and only the
 * topmost lock's subtree is scrollable — a select inside a dialog inside a
 * drawer behaves the way a user expects.
 *
 * WHAT IT DELIBERATELY DOES NOT DO
 * --------------------------------
 * It does not hide the background scrollbar. Removing a classic (non-overlay)
 * scrollbar reclaims its width and shifts the layout underneath the scrim,
 * which is a more noticeable artefact than an inert scrollbar. It stays
 * visible, dimmed by the scrim, and does nothing when dragged.
 *
 * Keyboard scrolling is not intercepted either, and does not need to be: a
 * modal traps focus, so PageDown can only reach the modal's own scroller.
 */

interface Lock {
  getElement: () => HTMLElement | null;
}

const stack: Lock[] = [];
let detach: (() => void) | null = null;

function topElement(): HTMLElement | null {
  const top = stack[stack.length - 1];
  return top ? top.getElement() : null;
}

function shouldBlock(event: Event): boolean {
  const allowed = topElement();
  if (!allowed) return false;
  const target = event.target;
  if (!(target instanceof Node)) return true;
  // `contains` covers the element itself and works across portals, since the
  // check is against the modal's own subtree rather than a shared ancestor.
  return !allowed.contains(target);
}

function onScrollIntent(event: Event) {
  if (!event.cancelable) return;
  if (shouldBlock(event)) event.preventDefault();
}

function attach() {
  if (detach) return;
  const opts: AddEventListenerOptions = { capture: true, passive: false };
  document.addEventListener('wheel', onScrollIntent, opts);
  document.addEventListener('touchmove', onScrollIntent, opts);
  detach = () => {
    document.removeEventListener('wheel', onScrollIntent, opts);
    document.removeEventListener('touchmove', onScrollIntent, opts);
    detach = null;
  };
}

/**
 * @param active Whether the lock is engaged.
 * @param allowWithin Subtree that remains scrollable — normally the modal's
 *   overlay or panel element.
 */
export function useScrollLock(
  active: boolean,
  allowWithin: RefObject<HTMLElement | null>,
): void {
  useEffect(() => {
    if (!active) return undefined;

    // The ref is read lazily on each event rather than captured now, because a
    // panel mounted by a presence hook may not exist yet when the lock engages.
    const lock: Lock = { getElement: () => allowWithin.current };
    stack.push(lock);
    attach();

    return () => {
      const i = stack.lastIndexOf(lock);
      if (i !== -1) stack.splice(i, 1);
      if (stack.length === 0) detach?.();
    };
  }, [active, allowWithin]);
}
