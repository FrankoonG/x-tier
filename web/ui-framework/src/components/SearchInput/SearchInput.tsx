import {
  forwardRef,
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useEventCallback } from '../../hooks/useEventCallback';
import { Input, setNativeInputValue, useMergedRefs, type InputProps } from '../Input/Input';
import './SearchInput.css';

const IconSearch = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.6"
    strokeLinecap="round"
    aria-hidden="true"
    focusable="false"
  >
    <circle cx="7.2" cy="7.2" r="4.3" />
    <path d="m10.4 10.4 3.1 3.1" />
  </svg>
);

export interface SearchInputProps extends Omit<InputProps, 'type'> {
  /**
   * Debounced query callback. Fires immediately — bypassing the debounce — on
   * Enter, on clear and on Escape, because those are explicit commits rather
   * than typing.
   */
  onSearch?: (value: string) => void;
  /** Debounce window in milliseconds. `0` disables debouncing. */
  debounceMs?: number;
  /** Escape empties the field while it has a value. */
  clearOnEscape?: boolean;
  /** Replaces the default magnifier. Pass `null` for no icon. */
  prefix?: ReactNode;
}

/**
 * Search field: a magnifier, a clear button, Escape to empty, and a debounced
 * query callback.
 *
 * ESCAPE
 * ------
 * Escape only clears while the field has a value; on an empty field the event
 * is left to propagate. That distinction matters because a search box usually
 * lives inside something dismissible — a dialog, a popover, a command palette —
 * and swallowing Escape unconditionally leaves a keyboard user with no way out
 * of the container. Clearing also fires `onSearch('')` immediately rather than
 * on the debounce, so the results list empties the moment the field does.
 *
 * `type="search"` gives the field its `searchbox` role and, on iOS, a keyboard
 * with a Search key. The engine's own clear button is suppressed in CSS: two
 * clear affordances in one field is a bug report waiting to be filed.
 */
export const SearchInput = forwardRef<HTMLInputElement, SearchInputProps>(function SearchInput(
  {
    onSearch,
    debounceMs = 250,
    clearOnEscape = true,
    clearable = true,
    prefix = <IconSearch />,
    className,
    onChange,
    onKeyDown,
    onClear,
    disabled = false,
    readOnly = false,
    ...rest
  },
  ref,
) {
  const innerRef = useRef<HTMLInputElement | null>(null);
  const mergedRef = useMergedRefs<HTMLInputElement>(innerRef, ref);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // Stable identity, latest closure: the debounce timer never needs to be torn
  // down and rebuilt because the handler changed.
  const emit = useEventCallback(onSearch);

  const cancel = () => {
    if (timerRef.current !== undefined) {
      clearTimeout(timerRef.current);
      timerRef.current = undefined;
    }
  };

  const schedule = (value: string, immediate: boolean) => {
    cancel();
    if (immediate || debounceMs <= 0) {
      emit(value);
      return;
    }
    timerRef.current = setTimeout(() => {
      timerRef.current = undefined;
      emit(value);
    }, debounceMs);
  };

  useEffect(() => cancel, []);

  /**
   * The one clearing path. Escape and the clear button produce the same
   * user-visible outcome, so they must produce the same callbacks — a consumer
   * closing a suggestions list from `onClear` cannot be told about one and not
   * the other.
   */
  const clearNow = (element: HTMLInputElement) => {
    // Dispatching the edit lets the consumer's own onChange and any form
    // library see the clear; the immediate schedule then overrides the
    // debounced call that edit just queued.
    setNativeInputValue(element, '');
    schedule('', true);
    onClear?.();
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;

    if (event.key === 'Enter') {
      // Commit now; a form submit still gets its default behaviour.
      schedule(event.currentTarget.value, true);
      return;
    }

    if (event.key !== 'Escape' || !clearOnEscape || disabled || readOnly) return;

    const element = innerRef.current;
    if (!element || element.value === '') {
      // Nothing to clear — let the dialog, popover or palette above us handle it.
      return;
    }

    event.preventDefault();
    event.stopPropagation();
    clearNow(element);
  };

  return (
    <Input
      {...rest}
      ref={mergedRef}
      data-stratum="search-input"
      className={clsx('stratum-search-input', className)}
      type="search"
      // No explicit role: `type="search"` already maps to `searchbox`, and
      // hardcoding it would stop a consumer from making this a `combobox` for
      // a suggestions list.
      autoComplete="off"
      autoCorrect="off"
      spellCheck={false}
      prefix={prefix}
      clearable={clearable}
      disabled={disabled}
      readOnly={readOnly}
      onKeyDown={handleKeyDown}
      onChange={(event) => {
        schedule(event.target.value, false);
        onChange?.(event);
      }}
      onClear={() => {
        schedule('', true);
        onClear?.();
      }}
    />
  );
});
