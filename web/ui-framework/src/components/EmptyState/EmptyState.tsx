import { forwardRef, useId, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './EmptyState.css';

export type EmptyStateSize = 'sm' | 'md' | 'lg';

export type HeadingLevel = 1 | 2 | 3 | 4 | 5 | 6;

export interface EmptyStateProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  /** Icon or illustration. Decorative — the title carries the meaning. */
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  /** Buttons or links. Rendered in a row that wraps. */
  actions?: ReactNode;
  size?: EmptyStateSize;
  align?: 'center' | 'start';
  /** Draws a dashed container, for an empty slot inside a populated page. */
  bordered?: boolean;
  /**
   * Heading level for the title. Default `3`. Set it to whatever keeps the
   * page outline sequential — an empty state inside an `<h2>` section wants
   * an `h3`, one that replaces the whole page probably wants `h1`.
   */
  headingLevel?: HeadingLevel;
  /** Extra content below the actions — a link to docs, a keyboard hint. */
  footer?: ReactNode;
}

/**
 * The "there is nothing here" state.
 *
 * WHY THE HEADING LEVEL IS A PROP
 * -------------------------------
 * A component cannot know its depth in the document outline, and a hardcoded
 * `<h2>` inside an `<h4>` section produces a skipped level — one of the most
 * common structural failures screen reader users hit when navigating by
 * heading. So the level is explicit, defaults to something sane, and renders
 * a real heading element rather than a styled `<div>`.
 *
 * The root is labelled by that heading via `aria-labelledby`, so a user
 * jumping between landmarks hears what the empty region is about.
 */
export const EmptyState = forwardRef<HTMLDivElement, EmptyStateProps>(function EmptyState(
  {
    icon,
    title,
    description,
    actions,
    size = 'md',
    align = 'center',
    bordered = false,
    headingLevel = 3,
    footer,
    className,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const titleId = `${reactId}-title`;
  const Heading = `h${headingLevel}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';

  return (
    <div
      // A bare <div> maps to `role="generic"`, which does not support an
      // accessible name, so `aria-labelledby` on it is discarded by the
      // accessibility API. `region` is the role the docstring above promises.
      // Both sit BEFORE the spread so a consumer-supplied role or label wins —
      // after it, an explicit value would delete theirs.
      role="region"
      aria-labelledby={titleId}
      {...rest}
      ref={ref}
      data-stratum="empty-state"
      data-size={size}
      data-align={align}
      data-bordered={bordered || undefined}
      className={clsx('stratum-empty-state', className)}
    >
      {icon != null && (
        <div className="stratum-empty-state__icon" aria-hidden="true">
          {icon}
        </div>
      )}

      <Heading className="stratum-empty-state__title" id={titleId}>
        {title}
      </Heading>

      {description != null && description !== false && (
        <p className="stratum-empty-state__description">{description}</p>
      )}

      {actions != null && actions !== false && (
        <div className="stratum-empty-state__actions">{actions}</div>
      )}

      {footer != null && footer !== false && (
        <div className="stratum-empty-state__footer">{footer}</div>
      )}
    </div>
  );
});
