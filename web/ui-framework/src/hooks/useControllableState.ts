import { useCallback, useRef, useState } from 'react';

export interface UseControllableStateOptions<T> {
  /** Controlled value. When not `undefined`, the component is controlled. */
  value?: T | undefined;
  /** Initial value for the uncontrolled case. */
  defaultValue: T;
  /** Called on every change, in both controlled and uncontrolled modes. */
  onChange?: ((value: T) => void) | undefined;
}

/**
 * Supports controlled and uncontrolled usage from one implementation.
 *
 * Every stateful component in the framework uses this, so `<Select value>` and
 * `<Select defaultValue>` behave identically from the component's point of
 * view and `onChange` always fires — including when the parent controls the
 * value, which is the case consumers most often find broken elsewhere.
 *
 * Mode is decided per render by whether `value` is `undefined`, and a
 * development warning fires if a component flips between the two, because
 * that silently strands state and is otherwise very hard to diagnose.
 */
export function useControllableState<T>({
  value,
  defaultValue,
  onChange,
}: UseControllableStateOptions<T>): [T, (next: T | ((prev: T) => T)) => void] {
  const [uncontrolled, setUncontrolled] = useState<T>(defaultValue);
  const isControlled = value !== undefined;
  const current = isControlled ? (value as T) : uncontrolled;

  // Keep the latest values in refs so the returned setter is stable across
  // renders — an unstable setter defeats memoisation in every consumer.
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const currentRef = useRef(current);
  currentRef.current = current;
  const isControlledRef = useRef(isControlled);

  if (import.meta.env?.DEV && isControlledRef.current !== isControlled) {
    console.warn(
      `[stratum] A component changed from ${
        isControlledRef.current ? 'controlled to uncontrolled' : 'uncontrolled to controlled'
      }. Decide once and keep \`value\` either always defined or always undefined.`,
    );
  }
  isControlledRef.current = isControlled;

  const setValue = useCallback((next: T | ((prev: T) => T)) => {
    const resolved =
      typeof next === 'function' ? (next as (prev: T) => T)(currentRef.current) : next;
    if (Object.is(resolved, currentRef.current)) return;
    if (!isControlledRef.current) setUncontrolled(resolved);
    onChangeRef.current?.(resolved);
  }, []);

  return [current, setValue];
}
