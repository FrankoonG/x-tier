import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Separator.css';

export type SeparatorOrientation = 'horizontal' | 'vertical';
export type SeparatorInset = 'none' | 'sm' | 'md' | 'lg';

export interface SeparatorProps extends HTMLAttributes<HTMLDivElement> {
  orientation?: SeparatorOrientation;
  /**
   * Purely visual. Removes the element from the accessibility tree.
   *
   * Default `true`, because most rules in a panel repeat a grouping the layout
   * already conveys, and a screen-reader user does not want "separator"
   * announced between every row. Set it to `false` only when the rule is the
   * sole indication that two groups are distinct.
   */
  decorative?: boolean;
  /** Pulls the rule in from the container edges. */
  inset?: SeparatorInset;
  /** Text set into the middle of the rule, e.g. "OR". */
  label?: ReactNode;
}

/**
 * A rule between two groups.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * `role="separator"` takes its name from the author, never from its contents,
 * so a labelled separator that relies on the visible text alone would be
 * announced as an unnamed separator. When `label` is a plain string it is
 * mirrored into `aria-label`; when it is a node the consumer supplies
 * `aria-label` themselves through `...rest`.
 *
 * A decorative separator uses `role="none"` rather than `aria-hidden`, so it
 * is removed from the accessibility tree without hiding anything that a
 * consumer may have slotted into `label`.
 */
export const Separator = forwardRef<HTMLDivElement, SeparatorProps>(function Separator(
  {
    orientation = 'horizontal',
    decorative = true,
    inset = 'none',
    label,
    className,
    children,
    ...rest
  },
  ref,
) {
  const hasLabel = label != null && label !== false;
  const ariaLabel =
    !decorative && hasLabel && typeof label === 'string' ? label : undefined;

  // A non-string label cannot be mirrored into `aria-label`, and the visible
  // label is `aria-hidden` in this branch — so without a consumer-supplied name
  // the separator is announced as an unnamed one and its content is gone from
  // the tree entirely. Silent degradation, hence the dev-mode error.
  if (
    import.meta.env?.DEV &&
    !decorative &&
    hasLabel &&
    typeof label !== 'string' &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      '[stratum] <Separator decorative={false}> with a non-string `label` requires ' +
        '`aria-label` or `aria-labelledby`. role="separator" takes its name from the ' +
        'author, and the visible label is hidden from assistive tech.',
    );
  }

  return (
    <div
      // All three sit before the spread so a consumer can override any of them.
      // After it, `aria-orientation`'s explicit `undefined` in the decorative
      // branch would delete a value the consumer had passed through `...rest`,
      // and `role` would silently overrule theirs.
      role={decorative ? 'none' : 'separator'}
      aria-orientation={decorative ? undefined : orientation}
      aria-label={ariaLabel}
      {...rest}
      ref={ref}
      data-stratum="separator"
      data-orientation={orientation}
      data-inset={inset}
      data-labelled={hasLabel || undefined}
      className={clsx('stratum-separator', className)}
    >
      {hasLabel && (
        <>
          <span className="stratum-separator__line" aria-hidden="true" />
          <span className="stratum-separator__label" aria-hidden={!decorative || undefined}>
            {label}
          </span>
          <span className="stratum-separator__line" aria-hidden="true" />
        </>
      )}
      {children}
    </div>
  );
});
