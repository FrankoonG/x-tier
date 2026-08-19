import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Badge.css';

export type BadgeVariant =
  | 'neutral'
  | 'accent'
  | 'success'
  | 'warning'
  | 'danger'
  | 'info'
  | 'unknown';

export type BadgeSize = 'xs' | 'sm' | 'md';

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  variant?: BadgeVariant;
  size?: BadgeSize;
  /** Draws a filled dot before the label, matching the variant's colour. */
  dot?: boolean;
  /** Border and text only, no fill. Reads lighter in a dense table. */
  outline?: boolean;
  /** Fully rounded ends. */
  pill?: boolean;
  /** Leading adornment. Hidden from assistive tech; the label carries meaning. */
  icon?: ReactNode;
}

/**
 * A static status label.
 *
 * NOT INTERACTIVE. A badge that needs a click target is a `Button`; a badge
 * that needs to be removed or toggled is a `Tag`. Keeping this one inert means
 * it can be dropped into a table cell without stealing a tab stop from the row.
 *
 * The variant colour is a reinforcement, never the message: every badge
 * renders text, so the state survives a monochrome display, a dichromatic
 * reader and forced-colors mode.
 */
export const Badge = forwardRef<HTMLSpanElement, BadgeProps>(function Badge(
  {
    variant = 'neutral',
    size = 'sm',
    dot = false,
    outline = false,
    pill = false,
    icon,
    className,
    children,
    ...rest
  },
  ref,
) {
  // The docblock above asserts that every badge renders text. Nothing enforced
  // it: `<Badge variant="danger" dot />` is an aria-hidden dot and nothing else,
  // so the state is carried by hue alone for sighted users (WCAG 1.4.1) and is
  // silent to assistive tech — and under forced colours every variant collapses
  // to the same CanvasText dot.
  if (
    import.meta.env?.DEV &&
    (children == null || children === false || children === '') &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      '[stratum] <Badge> with no children requires `aria-label` or `aria-labelledby`. ' +
        'The variant colour is a reinforcement, never the message.',
    );
  }

  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="badge"
      data-variant={variant}
      data-size={size}
      data-outline={outline || undefined}
      data-pill={pill || undefined}
      className={clsx('stratum-badge', className)}
    >
      {dot && <span className="stratum-badge__dot" aria-hidden="true" />}
      {icon && (
        <span className="stratum-badge__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      {children != null && children !== false && (
        <span className="stratum-badge__label">{children}</span>
      )}
    </span>
  );
});
