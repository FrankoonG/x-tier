import { forwardRef, useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';
import { Button, type ButtonProps } from '../Button/Button';
import { useEventCallback } from '../../hooks/useEventCallback';
import './CopyButton.css';

export type CopyStatus = 'idle' | 'copied' | 'error';

/**
 * Writes text to the clipboard, with a fallback for the many contexts where
 * the async Clipboard API is not available.
 *
 * `navigator.clipboard` is undefined on plain HTTP, inside a cross-origin
 * iframe without `clipboard-write`, and in older WebViews — all of which are
 * routine for an operations panel served from an appliance on a LAN. The
 * fallback is the deprecated `document.execCommand('copy')` path, which still
 * works everywhere the modern API does not.
 *
 * Ordering matters: `execCommand` is only permitted inside the task started by
 * a user gesture. So the modern API is used only when it is actually present,
 * and the legacy path is reached synchronously when it is not. If the modern
 * API is present but *rejects* — a denied permission, for instance — the
 * legacy retry happens after the gesture has expired and will usually fail
 * too, which is why the result is reported rather than assumed.
 *
 * @returns whether the text reached the clipboard.
 */
export function copyTextToClipboard(text: string): Promise<boolean> {
  const canUseAsync =
    typeof navigator !== 'undefined' &&
    typeof navigator.clipboard?.writeText === 'function' &&
    typeof window !== 'undefined' &&
    window.isSecureContext;

  if (canUseAsync) {
    return navigator.clipboard.writeText(text).then(
      () => true,
      () => legacyCopy(text),
    );
  }

  return Promise.resolve(legacyCopy(text));
}

function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;

  const previouslyFocused = document.activeElement;
  const selection = document.getSelection();
  const previousRange =
    selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;

  const field = document.createElement('textarea');
  field.value = text;
  field.setAttribute('readonly', '');
  // Positioned rather than hidden: a `display: none` or `visibility: hidden`
  // field cannot be selected, and scrolling to a field at the document origin
  // would jump the page. `position: fixed` keeps it in the viewport with no
  // scroll side effect.
  field.style.position = 'fixed';
  field.style.insetBlockStart = '0';
  field.style.insetInlineStart = '0';
  field.style.opacity = '0';
  field.style.pointerEvents = 'none';
  field.style.inlineSize = '1px';
  field.style.blockSize = '1px';
  field.setAttribute('aria-hidden', 'true');
  field.tabIndex = -1;

  let succeeded = false;
  try {
    document.body.appendChild(field);
    field.select();
    field.setSelectionRange(0, field.value.length);
    succeeded = document.execCommand('copy');
  } catch {
    succeeded = false;
  } finally {
    field.remove();
    // Selecting the field stole both the selection and the focus. Both are
    // restored, or a keyboard user is dumped back at the top of the document.
    if (selection) {
      selection.removeAllRanges();
      if (previousRange) selection.addRange(previousRange);
    }
    if (previouslyFocused instanceof HTMLElement) previouslyFocused.focus();
  }

  return succeeded;
}

export interface CopyButtonProps
  extends Omit<ButtonProps, 'onCopy' | 'children' | 'value' | 'loading' | 'loadingLabel'> {
  /** Text placed on the clipboard. */
  value: string;
  /** Accessible name and, with `showLabel`, the visible text. Default `'Copy'`. */
  label?: string;
  /** Announced after a successful copy. Default `'Copied'`. */
  copiedLabel?: string;
  /** Announced after a failed copy. Default `'Copy failed'`. */
  errorLabel?: string;
  /** Milliseconds before returning to the resting state. Default `2000`. */
  resetAfterMs?: number;
  /** Fires with the real outcome, including failure. */
  onCopy?: (succeeded: boolean) => void;
  /** Renders the label next to the icon instead of icon-only. */
  showLabel?: boolean;
  /** Replaces the default tick. Use a different SHAPE, not a recoloured copy. */
  copiedIcon?: ReactNode;
  /** Replaces the default alert glyph. */
  errorIcon?: ReactNode;
}

/**
 * Copies a string to the clipboard and reports what happened.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - The outcome is announced through a `role="status"` live region, and the
 *   button's label — visible and accessible — never changes. Swapping it to
 *   "Copied" costs three things at once: several screen readers re-announce a
 *   focused control whose name changed, the name is then wrong for the two
 *   seconds before it resets, and the button physically resizes under the
 *   pointer that just clicked it, shifting everything beside it in a toolbar.
 *   The visible state change is carried by the icon SHAPE and colour instead,
 *   which shifts nothing and works without audio.
 * - The live region is rendered from the first paint with empty contents.
 *   A live region inserted at the same moment as its text is frequently missed
 *   entirely, because the announcement is triggered by a change to an
 *   already-observed subtree. For the same reason the region is never keyed or
 *   remounted to force a repeat announcement — that would recreate exactly the
 *   condition this paragraph exists to avoid. It is blanked and refilled one
 *   frame later instead, so a second copy inside the reset window is a real
 *   mutation of a region that has been observed since mount.
 * - Failure is a real, announced state. A copy button that silently does
 *   nothing on plain HTTP is the single most common defect in this component.
 */
export const CopyButton = forwardRef<HTMLButtonElement, CopyButtonProps>(function CopyButton(
  {
    value,
    label = 'Copy',
    copiedLabel = 'Copied',
    errorLabel = 'Copy failed',
    resetAfterMs = 2000,
    onCopy,
    showLabel = false,
    icon,
    copiedIcon,
    errorIcon,
    variant = 'ghost',
    size = 'sm',
    className,
    onClick,
    ...rest
  },
  ref,
) {
  const [status, setStatus] = useState<CopyStatus>('idle');
  // Held separately from `status` because the live region has to be able to
  // change its text even when the status does not — two copies in a row are
  // both `'copied'`, and an unchanged subtree announces nothing.
  const [announcement, setAnnouncement] = useState('');
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const frameRef = useRef<number | undefined>(undefined);
  const notify = useEventCallback(onCopy);
  const handleClickProp = useEventCallback(onClick);

  /** Blanks the region, then fills it on the next frame so the change is real. */
  const announce = useCallback((text: string) => {
    if (frameRef.current !== undefined) cancelAnimationFrame(frameRef.current);
    setAnnouncement('');
    frameRef.current = requestAnimationFrame(() => {
      frameRef.current = undefined;
      setAnnouncement(text);
    });
  }, []);

  useEffect(
    () => () => {
      clearTimeout(timerRef.current);
      if (frameRef.current !== undefined) cancelAnimationFrame(frameRef.current);
    },
    [],
  );

  // A new value invalidates the previous outcome.
  useEffect(() => {
    clearTimeout(timerRef.current);
    setStatus('idle');
    setAnnouncement('');
  }, [value]);

  const currentIcon =
    status === 'copied'
      ? (copiedIcon ?? <CheckIcon />)
      : status === 'error'
        ? (errorIcon ?? <AlertIcon />)
        : (icon ?? <ClipboardIcon />);

  // A caller who names the button for its target — "Copy node ID" — must keep
  // that name. Written after the spread, `aria-label` overwrote theirs with the
  // generic default, and deleted it outright under `showLabel`.
  const resolvedLabel = (rest['aria-label'] as string | undefined) ?? label;

  return (
    <>
      <Button
        aria-label={showLabel ? undefined : resolvedLabel}
        {...rest}
        ref={ref}
        variant={variant}
        size={size}
        icon={currentIcon}
        iconOnly={!showLabel}
        data-status={status}
        className={clsx('stratum-copy-button', className)}
        onClick={(event) => {
          handleClickProp(event);
          if (event.defaultPrevented) return;
          void copyTextToClipboard(value).then((succeeded) => {
            setStatus(succeeded ? 'copied' : 'error');
            announce(succeeded ? copiedLabel : errorLabel);
            notify(succeeded);
            clearTimeout(timerRef.current);
            timerRef.current = setTimeout(() => {
              setStatus('idle');
              setAnnouncement('');
            }, resetAfterMs);
          });
        }}
      >
        {showLabel ? label : null}
      </Button>

      <span role="status" aria-live="polite" className="stratum-visually-hidden">
        {announcement}
      </span>
    </>
  );
});

function ClipboardIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false">
      <rect
        x="5.85"
        y="5.85"
        width="8.3"
        height="8.3"
        rx="2"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <path
        d="M10.15 3.6v-.35a2 2 0 0 0-2-2h-5a2 2 0 0 0-2 2v5a2 2 0 0 0 2 2h.35"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
      />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false">
      <path
        d="m3 8.6 3.3 3.3L13 4.9"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function AlertIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false">
      <circle cx="8" cy="8" r="6.3" stroke="currentColor" strokeWidth="1.4" />
      <path
        d="M8 4.85v3.9"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
      <circle cx="8" cy="11.15" r="0.85" fill="currentColor" />
    </svg>
  );
}
