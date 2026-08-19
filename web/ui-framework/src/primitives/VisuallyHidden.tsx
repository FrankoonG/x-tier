import { forwardRef, type ElementType, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import { Slot } from './Slot';
import './VisuallyHidden.css';

export interface VisuallyHiddenProps extends HTMLAttributes<HTMLElement> {
  /**
   * Merge onto the single child element instead of rendering a `<span>`.
   * Use when the hidden content must be a specific element — a heading that
   * belongs in the document outline, or a `<label>` bound to a control.
   */
  asChild?: boolean;
  /**
   * Reveal the content while focus is inside it. This is the skip-link
   * pattern: hidden until a keyboard user reaches it, then fully visible.
   * Content that can receive focus must never be permanently hidden, because
   * a sighted keyboard user would lose track of where focus went.
   */
  focusable?: boolean;
}

/**
 * Removes content from view while leaving it in the accessibility tree.
 *
 * The clip technique is used rather than `display: none`, `visibility: hidden`
 * or `width: 0`: all three remove the element from the accessibility tree, and
 * `text-indent: -9999px` breaks in right-to-left writing modes and forces the
 * browser to maintain a huge scrollable area.
 */
export const VisuallyHidden = forwardRef<HTMLElement, VisuallyHiddenProps>(
  function VisuallyHidden({ asChild = false, focusable = false, className, ...rest }, ref) {
    const Comp: ElementType = asChild ? Slot : 'span';

    return (
      <Comp
        {...rest}
        ref={ref}
        data-stratum="visually-hidden"
        data-focusable={focusable || undefined}
        className={clsx('stratum-visually-hidden', className)}
      />
    );
  },
);
