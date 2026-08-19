import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { Button } from '../Button/Button';
import { statusGlyph } from '../_shared/statusIcons';
import type { HeadingLevel } from '../EmptyState/EmptyState';
import './ErrorState.css';

export type ErrorStateSize = 'sm' | 'md' | 'lg';

export type ErrorStateVariant = 'danger' | 'warning' | 'neutral';

export interface ErrorStateProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'role'> {
  /** Overrides the default variant glyph. `null` removes it. */
  icon?: ReactNode | null;
  title: ReactNode;
  description?: ReactNode;
  /**
   * The raw technical string — a stack, a response body, an error code.
   * Hidden behind a disclosure and rendered monospace, because it is for
   * someone filing a bug, not for the operator reading the summary.
   */
  details?: string;
  /** Open the details disclosure on first render. */
  defaultDetailsOpen?: boolean;
  labelDetails?: string;
  /** Shown while the details are open. Defaults to `labelDetails`. */
  labelDetailsOpen?: string;
  onRetry?: () => void;
  labelRetry?: string;
  /**
   * Announced from the retry button's busy live region while `retrying`.
   * Deliberately NOT the same string as `labelRetry`: the hidden text
   * contributes to name-from-content, so reusing it would make the button's
   * accessible name "Try again Try again".
   */
  labelRetrying?: string;
  /** Puts the retry button in its loading state. */
  retrying?: boolean;
  /** Additional controls rendered beside the retry button. */
  actions?: ReactNode;
  /** Adds a copy button to the details block. Default `true` when `details` is set. */
  copyable?: boolean;
  labelCopy?: string;
  labelCopied?: string;
  size?: ErrorStateSize;
  variant?: ErrorStateVariant;
  align?: 'center' | 'start';
  bordered?: boolean;
  headingLevel?: HeadingLevel;
  /**
   * Announce the failure when it appears. Default `true`, which sets
   * `role="alert"`. Turn it off when the error is already announced by
   * something else, e.g. a toast fired by the same failure.
   */
  announce?: boolean;
}

/** How long the "Copied" acknowledgement stays up. */
const COPIED_MS = 1600;

/**
 * The "this failed" state.
 *
 * WHY DETAILS ARE A DISCLOSURE
 * ----------------------------
 * A stack trace pasted into the page teaches the operator that the product is
 * broken and teaches them nothing about what to do. But removing it entirely
 * makes the bug unreportable. A native `<details>` gets both: the summary is
 * a real, keyboard-operable, correctly-announced control in every browser
 * with no JavaScript and no ARIA of our own, and the technical string is one
 * key away when it is actually wanted.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - `role="alert"` by default, so a failure that replaces content mid-session
 *   is announced rather than silently swapped in. Disable with
 *   `announce={false}` when something else already says it.
 * - The copy acknowledgement is a `status` live region, not a change of the
 *   button's own label, so a screen reader hears "Copied" without the button
 *   it is focused on being renamed underneath it.
 * - `headingLevel` is explicit for the same reason as in `EmptyState`.
 */
export const ErrorState = forwardRef<HTMLDivElement, ErrorStateProps>(function ErrorState(
  {
    icon,
    title,
    description,
    details,
    defaultDetailsOpen = false,
    labelDetails = 'Technical details',
    labelDetailsOpen,
    onRetry,
    labelRetry = 'Try again',
    labelRetrying = 'Retrying',
    retrying = false,
    actions,
    copyable = true,
    labelCopy = 'Copy',
    labelCopied = 'Copied',
    size = 'md',
    variant = 'danger',
    align = 'center',
    bordered = false,
    headingLevel = 3,
    announce = true,
    className,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const titleId = `${reactId}-title`;
  const Heading = `h${headingLevel}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';

  const [copied, setCopied] = useState(false);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // The disclosure is uncontrolled — `open` is only the initial attribute — so
  // the open state has to be observed through the native `toggle` event. Without
  // it `labelDetailsOpen` would be rendered in both states, which is the same as
  // not having the prop.
  const [detailsOpen, setDetailsOpen] = useState(defaultDetailsOpen);

  useEffect(
    () => () => {
      if (copyTimer.current !== undefined) clearTimeout(copyTimer.current);
    },
    [],
  );

  const handleCopy = useCallback(() => {
    if (!details) return;
    const done = () => {
      setCopied(true);
      if (copyTimer.current !== undefined) clearTimeout(copyTimer.current);
      copyTimer.current = setTimeout(() => setCopied(false), COPIED_MS);
    };
    // `navigator.clipboard` is unavailable on insecure origins, which is a
    // realistic deployment for an on-premises panel, so failure is silent
    // rather than throwing into the user's face.
    const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard;
    if (!clipboard?.writeText) return;
    void clipboard.writeText(details).then(done, () => {});
  }, [details]);

  const glyph = icon === null ? null : (icon ?? statusGlyph(variant === 'neutral' ? 'neutral' : variant));
  const summaryLabel = detailsOpen ? (labelDetailsOpen ?? labelDetails) : labelDetails;

  return (
    <div
      // A bare <div> maps to `role="generic"`, which does not support an
      // accessible name — `aria-labelledby` on it is discarded outright. A
      // nameable structure role is what makes the association real. `group`
      // rather than the `region` landmark, because the message inside is
      // already a live region and one landmark per failure would flood the
      // landmark list. `aria-labelledby` sits BEFORE the spread so a
      // consumer-supplied name wins; after it, ours would delete theirs.
      role="group"
      aria-labelledby={titleId}
      {...rest}
      ref={ref}
      data-stratum="error-state"
      data-size={size}
      data-variant={variant}
      data-align={align}
      data-bordered={bordered || undefined}
      className={clsx('stratum-error-state', className)}
    >
      {glyph != null && (
        <div className="stratum-error-state__icon" aria-hidden="true">
          {glyph}
        </div>
      )}

      {/* The live region wraps the message ONLY. On the root it would also
        * contain the disclosure and the copy acknowledgement, and since
        * `alert` is `aria-live="assertive"`, opening "Technical details" would
        * insert the whole stack trace into a live region and interrupt the
        * reader to recite it — then again on every copy. */}
      <div
        className="stratum-error-state__message"
        data-stratum="error-state-message"
        role={announce ? 'alert' : undefined}
      >
        <Heading className="stratum-error-state__title" id={titleId}>
          {title}
        </Heading>

        {description != null && description !== false && (
          <p className="stratum-error-state__description">{description}</p>
        )}
      </div>

      {(onRetry || actions) && (
        <div className="stratum-error-state__actions">
          {onRetry && (
            <Button
              variant="default"
              onClick={onRetry}
              loading={retrying}
              loadingLabel={labelRetrying}
            >
              {labelRetry}
            </Button>
          )}
          {actions}
        </div>
      )}

      {details && (
        <details
          className="stratum-error-state__details"
          open={defaultDetailsOpen}
          data-stratum="error-state-details"
          onToggle={(event) => setDetailsOpen(event.currentTarget.open)}
        >
          <summary className="stratum-error-state__summary">
            <span className="stratum-error-state__summary-text">{summaryLabel}</span>
            <svg
              className="stratum-error-state__chevron"
              viewBox="0 0 16 16"
              width="1em"
              height="1em"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.6"
              strokeLinecap="round"
              strokeLinejoin="round"
              focusable="false"
              aria-hidden="true"
            >
              <path d="m6 4 4 4-4 4" />
            </svg>
          </summary>

          <div className="stratum-error-state__details-body">
            <pre className="stratum-error-state__code stratum-mono">{details}</pre>
            {copyable && (
              <div className="stratum-error-state__copy">
                <Button size="xs" variant="ghost" onClick={handleCopy}>
                  {labelCopy}
                </Button>
                {/* A separate live region: renaming the focused button to
                  * "Copied" would change the accessible name of the control
                  * the user is standing on. */}
                <span className="stratum-visually-hidden" role="status">
                  {copied ? labelCopied : ''}
                </span>
                <span className="stratum-error-state__copied" aria-hidden="true" data-visible={copied || undefined}>
                  {labelCopied}
                </span>
              </div>
            )}
          </div>
        </details>
      )}
    </div>
  );
});
