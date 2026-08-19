import { useCallback, useSyncExternalStore } from 'react';

/**
 * Subscribes to a CSS media query and re-renders when it changes.
 *
 * Built on `useSyncExternalStore` rather than `useState` + `useEffect` so the
 * first render already has the correct value — no flash of the wrong layout —
 * and so it stays correct under concurrent rendering.
 *
 * @param query A media query string, e.g. `'(min-width: 768px)'`.
 * @param defaultValue Value to report where `matchMedia` is unavailable
 *   (server rendering, very old engines). Defaults to `false`.
 */
export function useMediaQuery(query: string, defaultValue = false): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window === 'undefined' || !window.matchMedia) return () => {};
      const mql = window.matchMedia(query);
      // `addEventListener` on MediaQueryList is the standard API; Safari
      // only gained it in 14. addListener is deprecated but harmless as a
      // fallback and costs one feature test.
      if (mql.addEventListener) {
        mql.addEventListener('change', onChange);
        return () => mql.removeEventListener('change', onChange);
      }
      mql.addListener(onChange);
      return () => mql.removeListener(onChange);
    },
    [query],
  );

  const getSnapshot = useCallback(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return defaultValue;
    return window.matchMedia(query).matches;
  }, [query, defaultValue]);

  const getServerSnapshot = useCallback(() => defaultValue, [defaultValue]);

  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
