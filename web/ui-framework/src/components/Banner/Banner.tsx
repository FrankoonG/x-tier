import {
  forwardRef,
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { usePresence } from '../../hooks/usePresence';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { statusGlyph, type StatusTone } from '../_shared/statusIcons';
import { bannerGridColumns, bannerNarrowActionColumn } from './bannerLayout';
import './Banner.css';

export type BannerVariant = 'info' | 'success' | 'warning' | 'danger' | 'neutral' | 'accent';

/** `subtle` tints the surface; `solid` fills it for a page-wide outage strip. */
export type BannerEmphasis = 'subtle' | 'solid';

export type BannerStorage = 'local' | 'session';

export interface BannerProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'children' | 'role'> {
  variant?: BannerVariant;
  emphasis?: BannerEmphasis;
  size?: 'sm' | 'md';
  title?: ReactNode;
  /** Body copy. */
  children?: ReactNode;
  /** `null` suppresses the default variant glyph. */
  icon?: ReactNode | null;
  /** Trailing controls, typically one or two `<Button>`s. */
  action?: ReactNode;
  /** Renders the close button. */
  dismissible?: boolean;
  labelDismiss?: string;
  onDismiss?: () => void;
  /**
   * Persists dismissal under this key, so the banner does not return on the
   * next page load. Omit for a banner that should reappear every session.
   */
  storageKey?: string;
  /** Where `storageKey` is written. Default `'local'`. */
  storage?: BannerStorage;
  /** Controlled visibility. */
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * ARIA role. Defaults to `'alert'` for `danger`, `'status'` for `warning`,
   * and none otherwise — a decorative info strip should not interrupt.
   */
  role?: 'alert' | 'status' | 'region' | 'none';
  /** Full-bleed page banner: no radius, no side borders. */
  fullBleed?: boolean;
}

const TONE_BY_VARIANT: Record<BannerVariant, StatusTone> = {
  info: 'info',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  neutral: 'neutral',
  accent: 'accent',
};

function storageFor(kind: BannerStorage): Storage | null {
  try {
    if (typeof window === 'undefined') return null;
    return kind === 'session' ? window.sessionStorage : window.localStorage;
  } catch {
    // Storage access throws under a restrictive policy or in private mode.
    // A dismissed banner is never worth breaking the page for.
    return null;
  }
}

function readDismissed(key: string | undefined, kind: BannerStorage): boolean {
  if (!key) return false;
  try {
    return storageFor(kind)?.getItem(key) === 'dismissed';
  } catch {
    return false;
  }
}

/**
 * A persistent page- or section-level message.
 *
 * WHY THIS IS NOT A TOAST
 * -----------------------
 * A banner states a condition that is still true — a degraded upstream, a
 * pending migration, an expiring credential. It does not time out, it is not
 * stacked, and it participates in document flow so it can never cover the
 * content it is talking about.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - `role` is derived from the variant rather than always set. A `danger`
 *   banner is genuinely an alert; an `info` banner rendered at page load is
 *   ordinary content, and marking it `alert` would make every navigation
 *   interrupt the user.
 * - The dismiss button is a real labelled button, never an icon with a
 *   `title`.
 * - `storageKey` persistence is read lazily on first render, so the banner
 *   never flashes in before being hidden. An uncontrolled banner re-reads it
 *   when the key itself changes; a controlled one owns its own visibility and
 *   is never overridden.
 */
export const Banner = forwardRef<HTMLDivElement, BannerProps>(function Banner(
  {
    variant = 'info',
    emphasis = 'subtle',
    size = 'md',
    title,
    children,
    icon,
    action,
    dismissible = false,
    labelDismiss = 'Dismiss',
    onDismiss,
    storageKey,
    storage = 'local',
    open,
    defaultOpen = true,
    onOpenChange,
    role,
    fullBleed = false,
    className,
    style,
    ...rest
  },
  ref,
) {
  const [persistedDismissed] = useState(() => readDismissed(storageKey, storage));

  const [isOpen, setOpen] = useControllableState<boolean>({
    value: open,
    defaultValue: defaultOpen && !persistedDismissed,
    onChange: onOpenChange,
  });

  const { isPresent, ref: presenceRef, state } = usePresence(isOpen);

  const emitDismiss = useEventCallback(onDismiss);

  const handleDismiss = useCallback(() => {
    setOpen(false);
    if (storageKey) {
      try {
        storageFor(storage)?.setItem(storageKey, 'dismissed');
      } catch {
        /* see storageFor */
      }
    }
    emitDismiss();
  }, [setOpen, storageKey, storage, emitDismiss]);

  // The initial read happens once, so an uncontrolled banner whose key moves
  // under it (`storageKey={`incident-${id}`}`) would otherwise keep applying
  // the previous key's verdict. Re-read on a real change of key or storage.
  const isControlled = open !== undefined;
  const appliedKeyRef = useRef(`${storage}\0${storageKey ?? ''}`);
  useEffect(() => {
    const identity = `${storage}\0${storageKey ?? ''}`;
    if (appliedKeyRef.current === identity) return;
    appliedKeyRef.current = identity;
    // A controlled banner's visibility belongs to its owner; forcing it here
    // would fight the prop it is being driven by.
    if (isControlled) return;
    setOpen(defaultOpen && !readDismissed(storageKey, storage));
  }, [storageKey, storage, isControlled, defaultOpen, setOpen]);

  // A controlled banner reopened by its owner must clear a stale persisted
  // dismissal, otherwise the next load hides a banner the app just re-showed.
  //
  // ONLY a real closed -> open transition clears it. Clearing whenever `isOpen`
  // is true would wipe the key on every mount of a banner that mounts visible,
  // and on a `storageKey` change it would delete the NEW key's dismissal — the
  // one the user had just made.
  const wasOpenRef = useRef(isOpen);
  useEffect(() => {
    const wasOpen = wasOpenRef.current;
    wasOpenRef.current = isOpen;
    if (!isOpen || wasOpen || !storageKey) return;
    try {
      storageFor(storage)?.removeItem(storageKey);
    } catch {
      /* see storageFor */
    }
  }, [isOpen, storageKey, storage]);

  if (!isPresent) return null;

  const resolvedRole =
    role ?? (variant === 'danger' ? 'alert' : variant === 'warning' ? 'status' : undefined);

  const glyph = icon === null ? null : (icon ?? statusGlyph(TONE_BY_VARIANT[variant]));
  const hasIcon = glyph != null;
  const layoutStyle = {
    ...style,
    '--_grid-columns': bannerGridColumns(hasIcon),
    '--_action-column': bannerNarrowActionColumn(hasIcon),
  } as CSSProperties;

  const setRefs = (node: HTMLDivElement | null) => {
    presenceRef(node);
    if (typeof ref === 'function') ref(node);
    else if (ref) ref.current = node;
  };

  return (
    <div
      {...rest}
      ref={setRefs}
      role={resolvedRole === 'none' ? undefined : resolvedRole}
      data-stratum="banner"
      data-variant={variant}
      data-emphasis={emphasis}
      data-size={size}
      data-state={state}
      data-full-bleed={fullBleed || undefined}
      className={clsx('stratum-banner', className)}
      style={layoutStyle}
    >
      {glyph != null && (
        <span className="stratum-banner__icon" aria-hidden="true">
          {glyph}
        </span>
      )}

      <div className="stratum-banner__body">
        {title != null && title !== false && (
          <div className="stratum-banner__title">{title}</div>
        )}
        {children != null && children !== false && (
          <div className="stratum-banner__text">{children}</div>
        )}
      </div>

      {action && <div className="stratum-banner__action">{action}</div>}

      {dismissible && (
        <button
          type="button"
          className="stratum-banner__dismiss"
          aria-label={labelDismiss}
          onClick={handleDismiss}
        >
          <svg
            viewBox="0 0 16 16"
            width="1em"
            height="1em"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            focusable="false"
            aria-hidden="true"
          >
            <path d="m4.5 4.5 7 7M11.5 4.5l-7 7" />
          </svg>
        </button>
      )}
    </div>
  );
});
