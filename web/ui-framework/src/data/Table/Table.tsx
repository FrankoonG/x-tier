import { forwardRef, useMemo, useRef } from 'react';
import type {
  CSSProperties,
  FocusEvent,
  ForwardedRef,
  HTMLAttributes,
  KeyboardEvent,
  MouseEvent,
  ReactElement,
  ReactNode,
  RefAttributes,
} from 'react';
import clsx from 'clsx';
import { isInteractiveDescendant, type SelectionModel } from '../selection';
import { closestRow, rowIndexOf, useRowNavigation } from '../rowNavigation';
import {
  SELECT_COLUMN_WIDTH,
  SelectionCheckbox,
  SkeletonRows,
  resolvePxWidth,
  stopRowPropagation,
} from './parts';
import './Table.css';

export type TableDensity = 'compact' | 'default' | 'comfortable';
export type TableAlign = 'start' | 'center' | 'end';
export type TableStickySide = 'start' | 'end';

/** Style object that also carries CSS custom properties. */
export type TableCSSVars = CSSProperties & Record<`--${string}`, string | number>;

export interface TableColumn<T> {
  /** Stable identity. Also the React key for every cell in the column. */
  key: string;
  /** Header content. Rendered inside a real `<th scope="col">`. */
  header: ReactNode;
  /** Cell renderer. Receives the row and its index in `data`. */
  cell: (row: T, index: number) => ReactNode;
  /**
   * Plain-text header name, needed when `header` is a node rather than a
   * string. Used as the column's accessible name.
   */
  headerLabel?: string;
  /**
   * Column width. Numbers are px. Sticky columns need a px width — see
   * {@link TableProps.layout}.
   */
  width?: number | string;
  align?: TableAlign;
  /** Pins the column. `true` means `'start'`. */
  sticky?: boolean | TableStickySide;
  className?: string;
  headerClassName?: string;
}

/**
 * The slice of {@link SelectionModel} a table needs. Pass the whole object
 * returned by `useSelection`.
 */
export type TableSelection = Pick<
  SelectionModel,
  'selected' | 'toggle' | 'toggleAll' | 'selectOnly' | 'isAllSelected' | 'isSomeSelected'
>;

export interface TableProps<T> extends Omit<HTMLAttributes<HTMLTableElement>, 'children'> {
  data: readonly T[];
  columns: readonly TableColumn<T>[];
  /** Stable identity per row. Also the React key. */
  rowKey: (row: T, index: number) => string;
  rowClassName?: (row: T, index: number) => string | undefined;
  /** Shown when `data` is empty and `loading` is false. */
  emptyState?: ReactNode;
  /** Replaces the body with skeleton rows. The header keeps its widths. */
  loading?: boolean;
  /** Number of skeleton rows. Default 5. */
  loadingRows?: number;
  /** Status announced while loading. */
  loadingLabel?: string;
  density?: TableDensity;
  /**
   * `'fixed'` makes column widths authoritative, which is what pinned columns
   * and a live-updating body both want — an auto-layout table re-measures on
   * every data change and the columns twitch. Pinned columns REQUIRE it, along
   * with a px `width`, because their offsets are summed in JS.
   *
   * Under `'fixed'` the trailing column absorbs whatever width the container
   * has spare, so every column before it keeps exactly the width it was given.
   * Default `'auto'`.
   */
  layout?: 'auto' | 'fixed';
  /**
   * Pins the header. Sticks to the nearest scrolling ancestor, which this
   * component deliberately does NOT create: the consumer owns the scroll
   * container, so the same table works in a panel, a drawer or the page.
   */
  stickyHeader?: boolean;
  zebra?: boolean;
  bordered?: boolean;
  onRowClick?: (
    row: T,
    index: number,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => void;
  selection?: TableSelection;
  /** Accessible name for the select-all checkbox. */
  labelSelectAll?: string;
  /** Accessible name for a row checkbox. */
  labelSelectRow?: (row: T, index: number) => string;
  caption?: ReactNode;
}

/**
 * A dense, presentational table with no external dependency.
 *
 * SELECTION IS TWO GESTURES, NOT ONE
 * ----------------------------------
 * The leading checkbox adds to the selection; the row body replaces it, and
 * replaces it with nothing when the row was already the only one selected. The
 * checkbox cell swallows its own clicks, and clicks that land on a control
 * inside a row never reach the row. The reasoning is in `selection.ts`, and
 * the keyboard mirrors it exactly: Space is the checkbox, Enter is the body.
 *
 * KEYBOARD
 * --------
 * When the table is interactive it becomes a `grid` with a roving tabindex
 * over ROWS — arrow keys move, Home/End jump, Tab leaves. Putting every row in
 * the tab order instead would make a 10 000-row table impossible to escape.
 * A table with neither `onRowClick` nor `selection` stays a plain `table` with
 * no invented interactivity.
 *
 * NOTHING ANIMATES
 * ----------------
 * No row entry, exit or layout animation, and no static `will-change`
 * anywhere. Rows are the one place in a UI where animation reliably costs more
 * than it communicates: they arrive in bulk, they arrive during a scroll, and
 * a transition on a row is a frame budget spent on something nobody asked to
 * watch.
 */
const TableRoot = forwardRef(function Table<T>(
  {
    data,
    columns,
    rowKey,
    rowClassName,
    emptyState = 'No rows',
    loading = false,
    loadingRows = 5,
    loadingLabel = 'Loading rows',
    density = 'default',
    layout = 'auto',
    stickyHeader = false,
    zebra = false,
    bordered = false,
    onRowClick,
    selection,
    labelSelectAll = 'Select all rows',
    labelSelectRow = () => 'Select row',
    caption,
    className,
    style,
    ...rest
  }: TableProps<T>,
  ref: ForwardedRef<HTMLTableElement>,
) {
  const bodyRef = useRef<HTMLTableSectionElement>(null);
  const interactive = Boolean(onRowClick || selection);
  const hasSelection = Boolean(selection);

  const rowIds = useMemo(() => data.map((row, index) => rowKey(row, index)), [data, rowKey]);

  const columnLayout = useMemo(
    () => computeColumnLayout(columns, hasSelection),
    [columns, hasSelection],
  );

  const nav = useRowNavigation({
    enabled: interactive,
    count: data.length,
    containerRef: bodyRef,
  });

  const activateRow = (
    index: number,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => {
    if (index < 0 || index >= data.length) return;
    const row = data[index] as T;
    const id = rowIds[index];
    if (selection && id !== undefined) selection.selectOnly(id);
    onRowClick?.(row, index, event);
  };

  // Delegated from <tbody>: one handler for the whole body rather than a
  // closure per row, which is what keeps a 10 000-row render cheap.
  const handleBodyClick = (event: MouseEvent<HTMLTableSectionElement>) => {
    const row = closestRow(event.target, bodyRef.current);
    if (!row) return;
    // A click on an inline control is that control's click, not the row's.
    if (isInteractiveDescendant(event.target, row)) return;
    activateRow(rowIndexOf(row), event);
  };

  const handleBodyFocus = (event: FocusEvent<HTMLTableSectionElement>) => {
    const row = closestRow(event.target, bodyRef.current);
    if (row) nav.setActiveIndex(rowIndexOf(row));
  };

  const handleBodyKeyDown = (event: KeyboardEvent<HTMLTableSectionElement>) => {
    // Only when the ROW itself holds focus. A Space pressed inside a cell's
    // text field must stay a space.
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;
    const index = rowIndexOf(target);
    if (index < 0 || target.dataset['rowIndex'] === undefined) return;

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        nav.focusRow(index + 1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        nav.focusRow(index - 1);
        break;
      case 'Home':
        event.preventDefault();
        nav.focusRow(0);
        break;
      case 'End':
        event.preventDefault();
        nav.focusRow(data.length - 1);
        break;
      case ' ': {
        // Space is the checkbox: additive.
        if (!selection) break;
        event.preventDefault();
        const id = rowIds[index];
        if (id !== undefined) selection.toggle(id);
        break;
      }
      case 'Enter':
        // Enter is the row body: exclusive.
        event.preventDefault();
        activateRow(index, event);
        break;
      default:
        break;
    }
  };

  const totalColumns = columns.length + (hasSelection ? 1 : 0);
  const showEmpty = !loading && data.length === 0;

  const rootStyle: TableCSSVars = {
    '--stratum-table-select-w': `${SELECT_COLUMN_WIDTH}px`,
    ...style,
  };

  return (
    <table
      // Placed before the spread. After it, the `undefined` branches would win
      // the object literal and delete a consumer's own `role`, `aria-busy` or
      // `aria-multiselectable` rather than defaulting them.
      role={interactive ? 'grid' : undefined}
      aria-multiselectable={hasSelection ? true : undefined}
      aria-busy={loading || undefined}
      {...rest}
      ref={ref}
      data-stratum="table"
      data-density={density}
      data-layout={layout}
      data-zebra={zebra || undefined}
      data-bordered={bordered || undefined}
      data-sticky-header={stickyHeader || undefined}
      data-interactive={interactive || undefined}
      className={clsx('stratum-table', className)}
      style={rootStyle}
    >
      {(caption || loading) && (
        <caption className="stratum-table__caption">
          {caption}
          {loading && <span className="stratum-visually-hidden">{loadingLabel}</span>}
        </caption>
      )}

      {/* THE LAST COLUMN TAKES THE SLACK.
       *
       * Under `table-layout: fixed` a table wider than the sum of its columns
       * shares the surplus across ALL of them — including the pinned ones,
       * whose rendered width then no longer matches the width the sticky
       * offsets were summed from, and pinned columns overlap by exactly that
       * error. Leaving the trailing column's width unset makes it the only
       * candidate for the surplus, so every column before it keeps the width
       * it was given. */}
      <colgroup>
        {hasSelection && <col style={{ width: SELECT_COLUMN_WIDTH }} />}
        {columns.map((column, index) => {
          const absorbsSlack = layout === 'fixed' && index === columns.length - 1;
          return (
            <col
              key={column.key}
              style={
                absorbsSlack || column.width === undefined ? undefined : { width: column.width }
              }
            />
          );
        })}
      </colgroup>

      <thead className="stratum-table__head">
        <tr className="stratum-table__row stratum-table__row--head">
          {selection && (
            <th
              scope="col"
              className="stratum-table__header-cell stratum-table__select-cell"
              data-sticky={columnLayout.selectSticky ? 'start' : undefined}
            >
              <label className="stratum-table__select-hit">
                <SelectionCheckbox
                  checked={selection.isAllSelected}
                  indeterminate={selection.isSomeSelected}
                  label={labelSelectAll}
                  onChange={() => selection.toggleAll()}
                />
              </label>
            </th>
          )}

          {columns.map((column, index) => {
            const cell = columnLayout.cells[index];
            return (
              <th
                key={column.key}
                scope="col"
                className={clsx('stratum-table__header-cell', column.headerClassName)}
                data-align={column.align}
                data-sticky={cell?.sticky}
                data-sticky-edge={cell?.edge || undefined}
                style={cell?.style}
                aria-label={column.headerLabel}
              >
                <span className="stratum-table__header-label">{column.header}</span>
              </th>
            );
          })}
        </tr>
      </thead>

      <tbody
        ref={bodyRef}
        className="stratum-table__body"
        onClick={interactive ? handleBodyClick : undefined}
        onKeyDown={interactive ? handleBodyKeyDown : undefined}
        onFocus={interactive ? handleBodyFocus : undefined}
      >
        {loading && <SkeletonRows rows={loadingRows} columns={totalColumns} />}

        {showEmpty && (
          <tr className="stratum-table__row stratum-table__row--empty">
            <td className="stratum-table__cell stratum-table__empty" colSpan={totalColumns}>
              {emptyState}
            </td>
          </tr>
        )}

        {!loading &&
          data.map((row, index) => {
            const id = rowIds[index] ?? String(index);
            const isSelected = selection?.selected.has(id) ?? false;

            return (
              <tr
                key={id}
                data-row-index={index}
                data-row-id={id}
                data-selected={isSelected || undefined}
                aria-selected={hasSelection ? isSelected : undefined}
                tabIndex={interactive ? (index === Math.max(nav.activeIndex, 0) ? 0 : -1) : undefined}
                className={clsx('stratum-table__row', rowClassName?.(row, index))}
              >
                {selection && (
                  <td
                    className="stratum-table__cell stratum-table__select-cell"
                    data-sticky={columnLayout.selectSticky ? 'start' : undefined}
                    onClick={stopRowPropagation}
                    onMouseDown={stopRowPropagation}
                  >
                    {/* The label fills the cell, so the padding is part of the
                        hit target and a near-miss still toggles the box. */}
                    <label className="stratum-table__select-hit">
                      <SelectionCheckbox
                        checked={isSelected}
                        label={labelSelectRow(row, index)}
                        onChange={() => selection.toggle(id)}
                      />
                    </label>
                  </td>
                )}

                {columns.map((column, columnIndex) => {
                  const cell = columnLayout.cells[columnIndex];
                  return (
                    <td
                      key={column.key}
                      className={clsx('stratum-table__cell', column.className)}
                      data-align={column.align}
                      data-sticky={cell?.sticky}
                      data-sticky-edge={cell?.edge || undefined}
                      style={cell?.style}
                    >
                      {column.cell(row, index)}
                    </td>
                  );
                })}
              </tr>
            );
          })}
      </tbody>
    </table>
  );
});

export const Table = TableRoot as <T>(
  props: TableProps<T> & RefAttributes<HTMLTableElement>,
) => ReactElement;

/* -------------------------------------------------------------------------- */

interface CellLayout {
  sticky: TableStickySide | undefined;
  /** Last column of the leading pinned run / first of the trailing run. */
  edge: boolean;
  style: TableCSSVars | undefined;
}

interface ColumnLayoutResult {
  cells: CellLayout[];
  /**
   * The leading checkbox column pins whenever any data column pins, because a
   * pinned first column sliding under an unpinned checkbox looks like a bug.
   */
  selectSticky: boolean;
}

/**
 * Resolves pinned-column offsets.
 *
 * Offsets have to be summed in JS because CSS has no way to ask "how wide is
 * every column before me". That makes a px width mandatory on pinned columns;
 * a percentage cannot be added up. Rather than silently un-pin the column —
 * which looks like the prop was ignored — the offset falls back to 0 and
 * development logs the column that is missing a width, so the overlap is both
 * visible and explained.
 */
export function computeColumnLayout<T>(
  columns: readonly TableColumn<T>[],
  hasSelection: boolean,
): ColumnLayoutResult {
  const sides = columns.map<TableStickySide | undefined>((column) => {
    if (column.sticky === true || column.sticky === 'start') return 'start';
    if (column.sticky === 'end') return 'end';
    return undefined;
  });

  const selectSticky = hasSelection && sides.includes('start');

  const offsets = new Array<number>(columns.length).fill(0);
  let lastStart = -1;
  let firstEnd = -1;

  let run = selectSticky ? SELECT_COLUMN_WIDTH : 0;
  for (const [index, column] of columns.entries()) {
    if (sides[index] !== 'start') continue;
    offsets[index] = run;
    lastStart = index;
    const width = resolvePxWidth(column.width);
    if (width === null && import.meta.env?.DEV) {
      console.error(
        `[stratum] <Table> column "${column.key}" is pinned but has no pixel width. ` +
          'Pinned columns need `width` as a number (or a `px` string) so the ' +
          'offsets of the columns after them can be summed.',
      );
    }
    run += width ?? 0;
  }

  let endRun = 0;
  for (let index = columns.length - 1; index >= 0; index -= 1) {
    if (sides[index] !== 'end') continue;
    const column = columns[index];
    offsets[index] = endRun;
    firstEnd = index;
    const width = resolvePxWidth(column?.width);
    if (width === null && import.meta.env?.DEV) {
      console.error(
        `[stratum] <Table> column "${column?.key ?? index}" is pinned to the end but has no pixel width.`,
      );
    }
    endRun += width ?? 0;
  }

  const cells = columns.map<CellLayout>((_column, index) => {
    const side = sides[index];
    return {
      sticky: side,
      edge: (side === 'start' && index === lastStart) || (side === 'end' && index === firstEnd),
      style: side ? { '--_sticky-offset': `${offsets[index] ?? 0}px` } : undefined,
    };
  });

  return { cells, selectSticky };
}
