import { forwardRef, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import './Code.css';

export type CodeVariant = 'default' | 'subtle' | 'plain';

export interface CodeProps extends HTMLAttributes<HTMLElement> {
  variant?: CodeVariant;
  /**
   * Allows breaks inside a long unbroken token. On by default because the
   * strings this component holds — identifiers, keys, hostnames — are exactly
   * the ones that blow a table cell open.
   */
  breakAll?: boolean;
}

/**
 * Inline monospace text.
 *
 * Sized in `em` rather than in a type-scale token so it tracks whatever it is
 * embedded in: a monospace face at the same nominal size as the surrounding
 * sans reads noticeably larger, and a fixed step would make it correct in body
 * text and wrong in a table cell.
 */
export const Code = forwardRef<HTMLElement, CodeProps>(function Code(
  { variant = 'default', breakAll = true, className, children, ...rest },
  ref,
) {
  return (
    <code
      {...rest}
      ref={ref}
      data-stratum="code"
      data-variant={variant}
      data-break-all={breakAll || undefined}
      className={clsx('stratum-code', className)}
    >
      {children}
    </code>
  );
});
