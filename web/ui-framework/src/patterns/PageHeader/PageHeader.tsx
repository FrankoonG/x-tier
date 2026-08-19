import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './PageHeader.css';

export interface PageHeaderProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title: ReactNode;
  /** One short line. Anything longer belongs in the page body. */
  description?: ReactNode;
  /** Status chips, counts, live indicators — sits inline after the title. */
  meta?: ReactNode;
  /** Primary and secondary actions, aligned to the end. */
  actions?: ReactNode;
  /** Breadcrumb or back affordance, above the title. */
  above?: ReactNode;
  /** Tabs or a filter bar, below the header, sharing its border. */
  below?: ReactNode;
  /** Heading level. Defaults to `h1`; use `h2` for a section header. */
  level?: 1 | 2 | 3;
  /** Pins the header while the content scrolls beneath it. */
  sticky?: boolean;
  size?: 'sm' | 'md';
}

/**
 * The header row of a page or a major section.
 *
 * Deliberately not a hero: one line of title, an optional single line of
 * description, and actions on the same row. Vertical space at the top of an
 * operator page is the most expensive space in the product — every pixel spent
 * here is a table row that does not fit.
 */
export const PageHeader = forwardRef<HTMLElement, PageHeaderProps>(function PageHeader(
  {
    title,
    description,
    meta,
    actions,
    above,
    below,
    level = 1,
    sticky = false,
    size = 'md',
    className,
    ...rest
  },
  ref,
) {
  const Heading = `h${level}` as 'h1' | 'h2' | 'h3';

  return (
    <header
      {...rest}
      ref={ref}
      data-stratum="page-header"
      data-size={size}
      data-sticky={sticky || undefined}
      className={clsx('stratum-page-header', className)}
    >
      {above && <div className="stratum-page-header__above">{above}</div>}

      <div className="stratum-page-header__row">
        <div className="stratum-page-header__titles">
          <div className="stratum-page-header__title-line">
            <Heading className="stratum-page-header__title">{title}</Heading>
            {meta && <div className="stratum-page-header__meta">{meta}</div>}
          </div>
          {description && <p className="stratum-page-header__description">{description}</p>}
        </div>

        {actions && <div className="stratum-page-header__actions">{actions}</div>}
      </div>

      {below && <div className="stratum-page-header__below">{below}</div>}
    </header>
  );
});
