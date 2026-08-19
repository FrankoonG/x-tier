import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { statusGlyph, type StatusTone } from '../_shared/statusIcons';
import './InlineMessage.css';

export type InlineMessageVariant =
  | 'info'
  | 'success'
  | 'warning'
  | 'danger'
  | 'neutral'
  | 'accent';

export interface InlineMessageProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'role' | 'children'> {
  variant?: InlineMessageVariant;
  size?: 'xs' | 'sm';
  /** `null` suppresses the glyph, e.g. in a dense column of hints. */
  icon?: ReactNode | null;
  children?: ReactNode;
  /**
   * ARIA role. Defaults to `'alert'` for `danger` and `'status'` for
   * `warning` — a field error that appears after submit must be announced.
   * Pass `'none'` for a static hint that is already referenced by
   * `aria-describedby`, so it is not announced twice.
   */
  role?: 'alert' | 'status' | 'none';
}

const TONE_BY_VARIANT: Record<InlineMessageVariant, StatusTone> = {
  info: 'info',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  neutral: 'neutral',
  accent: 'accent',
};

/**
 * A compact message attached to a field, a row or a small section.
 *
 * USAGE
 * -----
 * Give it an `id` and point the field's `aria-describedby` at it. For a
 * validation error also set `aria-invalid` on the field itself — a message
 * next to an input is not, on its own, an association a screen reader can
 * follow.
 *
 * ```tsx
 * <input aria-invalid aria-describedby="port-error" />
 * <InlineMessage id="port-error" variant="danger">Port must be 1-65535.</InlineMessage>
 * ```
 *
 * ACCESSIBILITY NOTE
 * ------------------
 * The default `role` is variant-derived rather than always `'alert'`. When
 * the message is ALSO referenced by `aria-describedby`, an `alert` role makes
 * the text announce twice — once as a live region and once as the field's
 * description. That is why `role="none"` is an explicit, documented opt-out
 * rather than something a consumer has to discover.
 */
export const InlineMessage = forwardRef<HTMLDivElement, InlineMessageProps>(
  function InlineMessage(
    { variant = 'neutral', size = 'sm', icon, children, role, className, ...rest },
    ref,
  ) {
    const resolvedRole =
      role ?? (variant === 'danger' ? 'alert' : variant === 'warning' ? 'status' : undefined);

    const glyph = icon === null ? null : (icon ?? statusGlyph(TONE_BY_VARIANT[variant]));

    return (
      <div
        {...rest}
        ref={ref}
        role={resolvedRole === 'none' ? undefined : resolvedRole}
        data-stratum="inline-message"
        data-variant={variant}
        data-size={size}
        className={clsx('stratum-inline-message', className)}
      >
        {glyph != null && (
          <span className="stratum-inline-message__icon" aria-hidden="true">
            {glyph}
          </span>
        )}
        <span className="stratum-inline-message__text">{children}</span>
      </div>
    );
  },
);
