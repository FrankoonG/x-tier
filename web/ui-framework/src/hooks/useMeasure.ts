import { useCallback, useLayoutEffect, useRef, useState } from 'react';

export interface Measurement {
  width: number;
  height: number;
}

export interface UseMeasureResult<T extends HTMLElement> {
  ref: (node: T | null) => void;
  /** Latest observed size. `null` until the first observation lands. */
  size: Measurement | null;
  /**
   * `false` for the first observation, `true` afterwards. Bind this to a
   * `data-animate` attribute so a container can size itself instantly on
   * first paint and animate only on subsequent changes.
   */
  hasSettled: boolean;
}

/**
 * Observes an element's size with `ResizeObserver`.
 *
 * The `hasSettled` flag exists to solve a specific problem: a container that
 * animates its height must NOT animate the first time, or every dialog would
 * play a height transition from 0 as it opens. hy2scale solved this by
 * incrementing a ref during render (`Modal.tsx:132`, `count.current++`), which
 * is a render-phase side effect — under React's concurrent double-invoke it
 * counts twice and skips a real animation. Here the flag is advanced inside
 * the observer callback, which is a genuine effect and runs exactly once per
 * observation regardless of how many times the component renders.
 *
 * Sizes are read from `borderBoxSize` where available so the value includes
 * padding and border and therefore matches what a CSS `height` should be set
 * to; `contentRect` is the fallback and is content-box only.
 */
export function useMeasure<T extends HTMLElement = HTMLElement>(): UseMeasureResult<T> {
  const [size, setSize] = useState<Measurement | null>(null);
  const [hasSettled, setHasSettled] = useState(false);

  const observerRef = useRef<ResizeObserver | null>(null);
  const seenFirstRef = useRef(false);
  const settledRef = useRef(false);

  const ref = useCallback((node: T | null) => {
    observerRef.current?.disconnect();
    observerRef.current = null;
    if (!node || typeof ResizeObserver === 'undefined') return;

    const ro = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;

      const box = entry.borderBoxSize?.[0];
      const next: Measurement = box
        ? { width: box.inlineSize, height: box.blockSize }
        : { width: entry.contentRect.width, height: entry.contentRect.height };

      setSize((prev) =>
        prev && prev.width === next.width && prev.height === next.height ? prev : next,
      );

      if (!seenFirstRef.current) {
        // First observation establishes the baseline; no animation.
        seenFirstRef.current = true;
      } else if (!settledRef.current) {
        settledRef.current = true;
        setHasSettled(true);
      }
    });

    ro.observe(node);
    observerRef.current = ro;
  }, []);

  useLayoutEffect(
    () => () => {
      observerRef.current?.disconnect();
      observerRef.current = null;
    },
    [],
  );

  return { ref, size, hasSettled };
}
