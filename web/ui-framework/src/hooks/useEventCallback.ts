import { useCallback, useInsertionEffect, useRef } from 'react';

/**
 * Returns a stable function identity that always calls the latest callback.
 *
 * Lets a component put a handler in a `useEffect` dependency array, or hand it
 * to a memoised child, without re-subscribing on every render — while still
 * seeing current props and state when it fires.
 *
 * `useInsertionEffect` rather than `useLayoutEffect` because it runs before
 * any layout effect in the tree, so a child's layout effect that invokes the
 * callback already sees the current version. This is the same approach React
 * uses in its own `useEffectEvent` prototype.
 */
export function useEventCallback<Args extends unknown[], R>(
  fn: ((...args: Args) => R) | undefined,
): (...args: Args) => R | undefined {
  const ref = useRef<typeof fn>(undefined);

  useInsertionEffect(() => {
    ref.current = fn;
  });

  return useCallback((...args: Args) => ref.current?.(...args), []);
}
