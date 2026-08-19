import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { Spinner } from '../Spinner/Spinner';
import './Button.css';

export type ButtonVariant =
  | 'primary'
  | 'default'
  | 'subtle'
  | 'ghost'
  | 'danger'
  | 'danger-subtle'
  | 'link';

export type ButtonSize = 'xs' | 'sm' | 'md' | 'lg';

export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'prefix'> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Leading adornment. Hidden from assistive tech; the label carries meaning. */
  icon?: ReactNode;
  /** Trailing adornment, e.g. a chevron or count. */
  iconEnd?: ReactNode;
  /**
   * Shows a spinner in place of `icon` and blocks activation. The button stays
   * focusable and is marked `aria-busy` rather than `disabled`, so a screen
   * reader user is told what changed instead of losing the element.
   */
  loading?: boolean;
  /** Accessible status announced while `loading`. */
  loadingLabel?: string;
  fullWidth?: boolean;
  /** Renders at icon-only proportions. `aria-label` becomes mandatory. */
  iconOnly?: boolean;
}

/**
 * The framework's primary action control.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - `loading` intentionally does NOT set `disabled`. A disabled element is
 *   removed from the tab order, so a keyboard user who activates a button and
 *   then has focus silently destroyed under them loses their place entirely.
 *   Instead the button stays focusable, reports `aria-disabled` and
 *   `aria-busy`, and swallows activation in the handler.
 * - `iconOnly` throws in development without an accessible name. An icon-only
 *   button with no label is the most common serious violation in operator
 *   tooling, and a `title` attribute does not reliably fix it: several screen
 *   readers ignore `title` when other naming is absent, and it never appears
 *   for touch users at all.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'default',
    size = 'md',
    icon,
    iconEnd,
    loading = false,
    loadingLabel,
    fullWidth = false,
    iconOnly = false,
    disabled = false,
    className,
    children,
    onClick,
    type = 'button',
    ...rest
  },
  ref,
) {
  if (import.meta.env?.DEV && iconOnly && !rest['aria-label'] && !rest['aria-labelledby']) {
    console.error(
      '[stratum] <Button iconOnly> requires `aria-label` or `aria-labelledby`. ' +
        'A `title` attribute is not a reliable substitute.',
    );
  }

  const inert = disabled || loading;

  return (
    <button
      {...rest}
      ref={ref}
      type={type}
      data-stratum="button"
      data-variant={variant}
      data-size={size}
      data-loading={loading || undefined}
      data-icon-only={iconOnly || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-button', className)}
      // `disabled` only when genuinely disabled — see the note above.
      disabled={disabled}
      aria-disabled={loading || undefined}
      aria-busy={loading || undefined}
      onClick={(event) => {
        if (inert) {
          event.preventDefault();
          return;
        }
        onClick?.(event);
      }}
    >
      {loading ? (
        <Spinner
          size={size === 'lg' ? 'md' : 'sm'}
          className="stratum-button__spinner"
          label={loadingLabel}
        />
      ) : (
        icon && (
          <span className="stratum-button__icon" aria-hidden="true">
            {icon}
          </span>
        )
      )}

      {children != null && children !== false && (
        <span className="stratum-button__label">{children}</span>
      )}

      {iconEnd && !loading && (
        <span className="stratum-button__icon" aria-hidden="true">
          {iconEnd}
        </span>
      )}
    </button>
  );
});
