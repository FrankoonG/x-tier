import { forwardRef, type ElementType, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { Slot } from '../../primitives/Slot';
import './FormActions.css';

export type FormActionsAlign = 'start' | 'center' | 'end' | 'between';
export type FormActionsStack = 'auto' | 'never' | 'always';

export interface FormActionsProps extends HTMLAttributes<HTMLDivElement> {
  /** Where the buttons sit when they are not in explicit groups. */
  align?: FormActionsAlign;
  /**
   * `auto` stacks the row to full-width buttons once the container is too
   * narrow for them side by side, measured against this row rather than the
   * viewport. `always` and `never` pin the choice.
   */
  stack?: FormActionsStack;
  /** Rule along the top, for a footer that follows scrolling content. */
  divider?: boolean;
  /**
   * Pins the row to the bottom of its scroll container, so the primary action
   * of a long form stays reachable without scrolling to the end.
   */
  sticky?: boolean;
  asChild?: boolean;
  children?: ReactNode;
}

/**
 * The footer row of a form.
 *
 * ORDER IS NOT REVERSED WHEN STACKED
 * ----------------------------------
 * Stacked layouts often promote the primary button to the top with CSS
 * `order`. That detaches the visual sequence from the DOM sequence, so a
 * keyboard user tabs in one order while seeing another — WCAG 2.4.3. The
 * groups keep their authored order at every width; a consumer who wants the
 * primary action first at narrow widths should author it first.
 */
export const FormActions = forwardRef<HTMLDivElement, FormActionsProps>(function FormActions(
  {
    align = 'end',
    stack = 'auto',
    divider = false,
    sticky = false,
    asChild = false,
    className,
    children,
    ...rest
  },
  ref,
) {
  const Comp: ElementType = asChild ? Slot : 'div';

  return (
    <Comp
      {...rest}
      ref={ref}
      data-stratum="form-actions"
      data-align={align}
      data-stack={stack}
      data-divider={divider || undefined}
      data-sticky={sticky || undefined}
      className={clsx('stratum-form-actions', className)}
    >
      {children}
    </Comp>
  );
});

export interface FormActionsGroupProps extends HTMLAttributes<HTMLDivElement> {
  /**
   * `end` pushes the group to the far edge of the row. Two groups — a
   * secondary one at the start, the primary one at the end — produce the
   * usual split footer, and a single `end` group still sits correctly on its
   * own, which `justify-content: space-between` on the parent would not.
   */
  align?: 'start' | 'end';
  asChild?: boolean;
  children?: ReactNode;
}

/**
 * A cluster of related buttons inside `FormActions`.
 *
 * Optional: bare buttons are laid out by the row's own `align`. Reach for
 * groups when destructive or tertiary actions belong at the opposite edge
 * from the primary one.
 */
export const FormActionsGroup = forwardRef<HTMLDivElement, FormActionsGroupProps>(
  function FormActionsGroup({ align = 'start', asChild = false, className, children, ...rest }, ref) {
    const Comp: ElementType = asChild ? Slot : 'div';

    return (
      <Comp
        {...rest}
        ref={ref}
        data-stratum="form-actions-group"
        data-align={align}
        className={clsx('stratum-form-actions__group', className)}
      >
        {children}
      </Comp>
    );
  },
);
