import { forwardRef, useId, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { Select } from '../Select/Select';
import './Pagination.css';

export type PaginationSize = 'sm' | 'md';

type PaginationSlot = number | 'start-ellipsis' | 'end-ellipsis';

export interface PaginationSummaryInfo {
  page: number;
  pageCount: number;
  pageSize: number | undefined;
  totalItems: number | undefined;
  /** 1-based index of the first item on the current page, when derivable. */
  firstItem: number | undefined;
  /** 1-based index of the last item on the current page, when derivable. */
  lastItem: number | undefined;
}

export interface PaginationProps extends Omit<HTMLAttributes<HTMLElement>, 'onChange'> {
  /** Total number of pages. Values below 1 render nothing. */
  pageCount: number;
  /** Controlled 1-based current page. */
  page?: number;
  defaultPage?: number;
  onPageChange?: (page: number) => void;
  /** Pages shown either side of the current page. */
  siblingCount?: number;
  /** Pages always shown at each end of the range. */
  boundaryCount?: number;
  /** Adds jump-to-first / jump-to-last controls. */
  showFirstLast?: boolean;
  /** Renders the page-size control. Requires `pageSize`. */
  pageSize?: number;
  pageSizeOptions?: number[];
  onPageSizeChange?: (pageSize: number) => void;
  /** Used only by `renderSummary`. */
  totalItems?: number;
  /**
   * Optional summary line. There is no default: any wording would be an
   * untranslatable hardcoded string, so the framework declines to invent one.
   */
  renderSummary?: (info: PaginationSummaryInfo) => ReactNode;
  size?: PaginationSize;
  /** Blocks every control without removing anything from the tab order. */
  disabled?: boolean;
  /** Render nothing when there is at most one page. */
  hideOnSinglePage?: boolean;

  /* -- Copy. Every string is a prop; nothing user-visible is hardcoded. ----- */
  label?: string;
  labelPrevious?: string;
  labelNext?: string;
  labelFirst?: string;
  labelLast?: string;
  /** Accessible name for a page button. Must contain the visible number. */
  labelPage?: (page: number) => string;
  /** Accessible name for the current page's button. */
  labelCurrentPage?: (page: number) => string;
  /** Announced in place of the visual ellipsis. */
  labelEllipsis?: string;
  labelPageSize?: string;

  iconPrevious?: ReactNode;
  iconNext?: ReactNode;
  iconFirst?: ReactNode;
  iconLast?: ReactNode;
}

const range = (start: number, end: number): number[] =>
  Array.from({ length: Math.max(end - start + 1, 0) }, (_, index) => start + index);

/**
 * Produces the visible slot list: boundary pages, a window around the current
 * page, and ellipses only where they actually replace more than one page —
 * an ellipsis standing in for a single hidden page is strictly worse than the
 * page, so that case renders the page instead.
 */
export function buildPaginationRange(
  page: number,
  pageCount: number,
  siblingCount: number,
  boundaryCount: number,
): PaginationSlot[] {
  const startPages = range(1, Math.min(boundaryCount, pageCount));
  const endPages = range(
    Math.max(pageCount - boundaryCount + 1, boundaryCount + 1),
    pageCount,
  );

  const siblingsStart = Math.max(
    Math.min(page - siblingCount, pageCount - boundaryCount - siblingCount * 2 - 1),
    boundaryCount + 2,
  );
  const firstEndPage = endPages[0];
  const siblingsEnd = Math.min(
    Math.max(page + siblingCount, boundaryCount + siblingCount * 2 + 2),
    firstEndPage !== undefined ? firstEndPage - 2 : pageCount - 1,
  );

  const slots: PaginationSlot[] = [...startPages];

  if (siblingsStart > boundaryCount + 2) {
    slots.push('start-ellipsis');
  } else if (boundaryCount + 1 < pageCount - boundaryCount) {
    slots.push(boundaryCount + 1);
  }

  slots.push(...range(siblingsStart, siblingsEnd));

  if (siblingsEnd < pageCount - boundaryCount - 1) {
    slots.push('end-ellipsis');
  } else if (pageCount - boundaryCount > boundaryCount) {
    slots.push(pageCount - boundaryCount);
  }

  slots.push(...endPages);
  return slots;
}

/* `fill="none"` belongs on the path: the reset layer declares
 * `svg { fill: currentColor }` and author CSS beats a presentational attribute
 * on the <svg> root, which would otherwise flood these stroked glyphs. */
const StepIcon = ({ d }: { d: string }) => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <path
      d={d}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

const IconFirst = () => <StepIcon d="M11.5 4 8 8l3.5 4M4.5 4v8" />;
const IconPrevious = () => <StepIcon d="M10 3.5 5.5 8l4.5 4.5" />;
const IconNext = () => <StepIcon d="M6 3.5 10.5 8 6 12.5" />;
const IconLast = () => <StepIcon d="M4.5 4 8 8l-3.5 4M11.5 4v8" />;

/**
 * Page navigation.
 *
 * WHY THE ARROWS ARE `aria-disabled`, NOT `disabled`
 * --------------------------------------------------
 * Paging forward with the mouse or keyboard means repeatedly activating Next.
 * On the last page a real `disabled` attribute removes that button from the
 * tab order *while it holds focus*, and the browser drops focus to `<body>` —
 * so the keyboard user who just reached the end of the results loses their
 * position in the document entirely. `aria-disabled` keeps the control
 * reachable, announces it as unavailable, and the handler swallows activation.
 *
 * THE PAGE-SIZE CONTROL IS THE FRAMEWORK'S `Select`
 * -------------------------------------------------
 * It used to be a native `<select>`, on the argument that the platform picker
 * is better on touch and costs no dependency on the overlay stack. That
 * argument is real but it loses: an OS-drawn listbox in the middle of a themed
 * panel reads as a different product, and visual coherence across the library
 * is the higher priority. `Select` also brings the library's keyboard model,
 * type-ahead and forced-colours handling with it.
 *
 * It follows the same `aria-disabled` rule as the buttons, so `disabled` is
 * never set on it — the popup still opens and the change handler is what
 * refuses the change. A panel that flips `disabled` during a data reload must
 * not evict the user from the control they are sitting in.
 *
 * `Select` renders a `<button>`, and `<label for>` does not contribute to a
 * button's accessible name, so the visible label is wired with
 * `aria-labelledby` as well — `for` is kept only so clicking the words still
 * opens the picker.
 */
export const Pagination = forwardRef<HTMLElement, PaginationProps>(function Pagination(
  {
    pageCount,
    page: pageProp,
    defaultPage = 1,
    onPageChange,
    siblingCount = 1,
    boundaryCount = 1,
    showFirstLast = false,
    pageSize,
    pageSizeOptions = [10, 25, 50, 100],
    onPageSizeChange,
    totalItems,
    renderSummary,
    size = 'md',
    disabled = false,
    hideOnSinglePage = false,
    label = 'Pagination',
    labelPrevious = 'Previous page',
    labelNext = 'Next page',
    labelFirst = 'First page',
    labelLast = 'Last page',
    labelPage = (value: number) => `Page ${value}`,
    labelCurrentPage = (value: number) => `Page ${value}, current page`,
    labelEllipsis = 'More pages',
    labelPageSize = 'Rows per page',
    iconPrevious,
    iconNext,
    iconFirst,
    iconLast,
    className,
    ...rest
  },
  ref,
) {
  const instanceId = useId();
  const selectId = `stratum-pagination-size-${instanceId}`;
  const selectLabelId = `${selectId}-label`;

  const total = Math.max(0, Math.floor(pageCount));
  const [page, setPage] = useControllableState<number>({
    value: pageProp,
    defaultValue: defaultPage,
    onChange: onPageChange,
  });

  const current = Math.min(Math.max(1, page), Math.max(total, 1));

  const goTo = (next: number) => {
    if (disabled) return;
    const clamped = Math.min(Math.max(1, next), Math.max(total, 1));
    if (clamped === current) return;
    setPage(clamped);
  };

  if (total < 1) return null;
  if (hideOnSinglePage && total <= 1) return null;

  const slots = buildPaginationRange(current, total, siblingCount, boundaryCount);
  const atStart = current <= 1;
  const atEnd = current >= total;

  const summary = renderSummary?.({
    page: current,
    pageCount: total,
    pageSize,
    totalItems,
    firstItem: pageSize === undefined ? undefined : (current - 1) * pageSize + 1,
    lastItem:
      pageSize === undefined
        ? undefined
        : totalItems === undefined
          ? current * pageSize
          : Math.min(current * pageSize, totalItems),
  });

  const step = (
    key: string,
    ariaLabel: string,
    icon: ReactNode,
    target: number,
    inert: boolean,
  ) => (
    <li key={key} className="stratum-pagination__item">
      <button
        type="button"
        data-stratum="pagination-step"
        data-inert={inert || undefined}
        aria-label={ariaLabel}
        aria-disabled={inert || disabled || undefined}
        className="stratum-pagination__button stratum-pagination__button--step"
        onClick={() => {
          if (inert) return;
          goTo(target);
        }}
      >
        {icon}
      </button>
    </li>
  );

  return (
    <nav
      // Before the spread: an explicit attribute written after `...rest` wins
      // in JSX, so a consumer's own `aria-label` would be replaced by the
      // framework default.
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="pagination"
      data-size={size}
      data-disabled={disabled || undefined}
      className={clsx('stratum-pagination', className)}
    >
      {summary != null && summary !== false && (
        <div className="stratum-pagination__summary stratum-numeric">{summary}</div>
      )}

      {pageSize !== undefined && (
        <div className="stratum-pagination__page-size" data-stratum="pagination-page-size">
          <label
            id={selectLabelId}
            className="stratum-pagination__page-size-label"
            htmlFor={selectId}
          >
            {labelPageSize}
          </label>
          <Select
            id={selectId}
            className="stratum-pagination__select"
            // Matches the page buttons beside it; the extra 2px is trimmed back
            // in Pagination.css, which owns the row's height.
            size={size === 'sm' ? 'sm' : 'md'}
            options={pageSizeOptions.map((option) => ({
              value: String(option),
              label: String(option),
            }))}
            value={String(pageSize)}
            // Renders the live page size even when it is absent from
            // `pageSizeOptions`, so `Select`'s English placeholder default can
            // never surface in a localised panel.
            renderValue={(option) => option?.label ?? String(pageSize)}
            // `aria-disabled`, not `disabled`, for the reason in the component
            // doc comment: a real `disabled` attribute on the control that
            // currently holds focus drops the keyboard user to <body>. It does
            // not close the picker, so the handler is what refuses the change.
            aria-disabled={disabled || undefined}
            aria-labelledby={selectLabelId}
            onChange={(next) => {
              if (disabled || next == null) return;
              onPageSizeChange?.(Number(next));
            }}
          />
        </div>
      )}

      <ul className="stratum-pagination__list">
        {showFirstLast &&
          step('first', labelFirst, iconFirst ?? <IconFirst />, 1, atStart)}
        {step('previous', labelPrevious, iconPrevious ?? <IconPrevious />, current - 1, atStart)}

        {slots.map((slot, index) => {
          if (slot === 'start-ellipsis' || slot === 'end-ellipsis') {
            return (
              <li key={`${slot}-${index}`} className="stratum-pagination__item">
                <span className="stratum-pagination__ellipsis">
                  <span aria-hidden="true">…</span>
                  <span className="stratum-visually-hidden">{labelEllipsis}</span>
                </span>
              </li>
            );
          }

          const isCurrent = slot === current;
          return (
            <li key={slot} className="stratum-pagination__item">
              <button
                type="button"
                data-stratum="pagination-page"
                data-current={isCurrent || undefined}
                aria-label={isCurrent ? labelCurrentPage(slot) : labelPage(slot)}
                aria-current={isCurrent ? 'page' : undefined}
                aria-disabled={disabled || undefined}
                className="stratum-pagination__button stratum-numeric"
                onClick={() => goTo(slot)}
              >
                {slot}
              </button>
            </li>
          );
        })}

        {step('next', labelNext, iconNext ?? <IconNext />, current + 1, atEnd)}
        {showFirstLast && step('last', labelLast, iconLast ?? <IconLast />, total, atEnd)}
      </ul>
    </nav>
  );
});
