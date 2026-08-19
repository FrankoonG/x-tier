import { forwardRef, useMemo, useRef } from 'react';
import type {
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
import { useControllableState } from '../../hooks/useControllableState';
import { isInteractiveDescendant } from '../selection';
import { closestRow, rowIndexOf, useRowNavigation } from '../rowNavigation';
import {
  DisclosureGlyph,
  SELECT_COLUMN_WIDTH,
  SelectionCheckbox,
  SkeletonRows,
  stopRowPropagation,
} from '../Table/parts';
import {
  computeColumnLayout,
  type TableCSSVars,
  type TableColumn,
  type TableDensity,
  type TableSelection,
} from '../Table/Table';
import '../Table/Table.css';
import './TreeTable.css';

/**
 * Characters used to draw the tree. Overridable because the right glyphs
 * depend on the font a panel ships with: box-drawing characters are perfect in
 * a monospace stack and can render as tofu in a constrained embedded one, so a
 * consumer can drop to `|`, `+` and `` ` `` without forking the component.
 */
export interface TreeGuideGlyphs {
  /** Continues an ancestor's sibling line. Default `'│'`. */
  vertical: string;
  /** A child with siblings after it. Default `'├─'`. */
  branch: string;
  /** The last child in its group. Default `'└─'`. */
  lastBranch: string;
  /** An ancestor with nothing following it. Default `' '`. */
  blank: string;
}

export type TreeGuideMode = 'lines' | 'ascii' | 'none';

const DEFAULT_GLYPHS: TreeGuideGlyphs = {
  vertical: '│',
  branch: '├─',
  lastBranch: '└─',
  blank: ' ',
};

/** Position of one row in the tree, handed to `rowClassName`. */
export interface TreeRowInfo {
  id: string;
  /** 0-based depth. Roots are 0. */
  level: number;
  hasChildren: boolean;
  expanded: boolean;
  isLast: boolean;
  parentId: string | null;
}

interface FlatRow<T> extends TreeRowInfo {
  row: T;
  posInSet: number;
  setSize: number;
  /** For each ancestor, whether it has a following sibling. Length === level. */
  rails: boolean[];
}

export interface TreeTableProps<T> extends Omit<HTMLAttributes<HTMLTableElement>, 'children'> {
  /** Root rows. Children come from `getChildren`. */
  data: readonly T[];
  /** `cell` receives the row and its index among the currently VISIBLE rows. */
  columns: readonly TableColumn<T>[];
  /** Stable identity. Must be unique across the whole tree, not per level. */
  rowKey: (row: T) => string;
  /** Children of a row, or `undefined`/`[]` for a leaf. */
  getChildren: (row: T) => readonly T[] | undefined;
  /** Controlled set of expanded row ids. */
  expanded?: Set<string>;
  defaultExpanded?: Set<string>;
  onExpandedChange?: (expanded: Set<string>) => void;
  /** Column that carries the guides and the twisty. Defaults to the first. */
  treeColumnKey?: string;
  /**
   * How the hierarchy is drawn. Indentation is kept in every mode.
   *
   * `'lines'`  hairlines that meet across rows, with the ASCII characters
   *            still present in the DOM so a copied selection is valid tree
   *            art. This is the default because a column of `│` glyphs in a
   *            padded table row renders as a dashed line, not a connector.
   * `'ascii'`  the characters themselves, terminal-authentic gaps and all.
   * `'none'`   indentation only.
   */
  guides?: TreeGuideMode;
  guideGlyphs?: Partial<TreeGuideGlyphs>;
  /** Width of one indent step, in px. Default 20. */
  indentSize?: number;
  rowClassName?: (row: T, info: TreeRowInfo) => string | undefined;
  emptyState?: ReactNode;
  loading?: boolean;
  loadingRows?: number;
  loadingLabel?: string;
  density?: TableDensity;
  layout?: 'auto' | 'fixed';
  stickyHeader?: boolean;
  zebra?: boolean;
  bordered?: boolean;
  onRowClick?: (
    row: T,
    info: TreeRowInfo,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => void;
  selection?: TableSelection;
  labelSelectAll?: string;
  labelSelectRow?: (row: T, index: number) => string;
  /** Tooltip on a collapsed twisty. */
  labelExpand?: string;
  /** Tooltip on an expanded twisty. */
  labelCollapse?: string;
  caption?: ReactNode;
}

/**
 * A hierarchical table with ASCII guide lines.
 *
 * WHY CONNECTORS AND NOT INDENTATION ALONE
 * ----------------------------------------
 * Indentation alone stops working at exactly the moment a tree gets
 * interesting: once a subtree is taller than the viewport, a row 40 lines
 * below its parent is just a row with some space in front of it, and the
 * reader has to scroll up to find out whose child it is. The vertical
 * connectors carry that relationship down the whole subtree, which is why
 * every terminal tree tool draws them.
 *
 * The default `guides="lines"` paints those connectors as hairlines while
 * keeping the `│ ├ └` characters in the DOM — so a copied selection is still
 * valid tree art, but the rendering does not break into dashes the way a
 * column of glyphs does inside a padded table row. `guides="ascii"` shows the
 * characters themselves for a terminal-faithful look.
 *
 * Either way the guides are decorative: `role="treegrid"` with `aria-level`,
 * `aria-expanded`, `aria-posinset` and `aria-setsize` carries the same
 * structure to assistive technology, so the glyphs are `aria-hidden` and a
 * screen reader never has to hear "vertical line".
 *
 * KEYBOARD (APG treegrid, row focus)
 * ----------------------------------
 *   ArrowDown / ArrowUp   previous / next VISIBLE row
 *   ArrowRight            expand; if already expanded, move to the first child
 *   ArrowLeft             collapse; if already collapsed, move to the parent
 *   Home / End            first / last visible row
 *   Space                 toggle selection (the checkbox gesture)
 *   Enter                 activate the row (the row-body gesture)
 *
 * SELECTION DOES NOT CASCADE. Selecting a parent does not select its
 * descendants: whether an action on a group implies an action on its members
 * is a policy this library has no business guessing, and guessing wrong means
 * an operator acts on rows they never saw.
 */
const TreeTableRoot = forwardRef(function TreeTable<T>(
  {
    data,
    columns,
    rowKey,
    getChildren,
    expanded,
    defaultExpanded,
    onExpandedChange,
    treeColumnKey,
    guides = 'lines',
    guideGlyphs,
    indentSize = 20,
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
    labelExpand = 'Expand',
    labelCollapse = 'Collapse',
    caption,
    className,
    style,
    ...rest
  }: TreeTableProps<T>,
  ref: ForwardedRef<HTMLTableElement>,
) {
  const bodyRef = useRef<HTMLTableSectionElement>(null);
  const hasSelection = Boolean(selection);

  const [expandedSet, setExpandedSet] = useControllableState<Set<string>>({
    value: expanded,
    defaultValue: defaultExpanded ?? EMPTY_EXPANDED,
    onChange: onExpandedChange,
  });

  const glyphs: TreeGuideGlyphs = useMemo(
    () => ({ ...DEFAULT_GLYPHS, ...guideGlyphs }),
    [guideGlyphs],
  );

  const { rows, indexById } = useMemo(() => {
    const out: FlatRow<T>[] = [];
    const index = new Map<string, number>();

    const walk = (
      nodes: readonly T[],
      level: number,
      parentId: string | null,
      rails: boolean[],
    ) => {
      nodes.forEach((row, position) => {
        const id = rowKey(row);
        const children = getChildren(row);
        const hasChildren = children !== undefined && children.length > 0;
        const isLast = position === nodes.length - 1;
        const isExpanded = hasChildren && expandedSet.has(id);

        index.set(id, out.length);
        out.push({
          row,
          id,
          level,
          posInSet: position + 1,
          setSize: nodes.length,
          hasChildren,
          expanded: isExpanded,
          isLast,
          parentId,
          rails,
        });

        if (isExpanded && children) {
          // A descendant draws a continuation line at this level only when
          // this node still has siblings below it.
          walk(children, level + 1, id, [...rails, !isLast]);
        }
      });
    };

    walk(data, 0, null, []);
    return { rows: out, indexById: index };
  }, [data, getChildren, rowKey, expandedSet]);

  const columnLayout = useMemo(
    () => computeColumnLayout(columns, hasSelection),
    [columns, hasSelection],
  );

  const treeKey = treeColumnKey ?? columns[0]?.key;

  const nav = useRowNavigation({
    enabled: true,
    count: rows.length,
    containerRef: bodyRef,
  });

  const setExpanded = (id: string, next: boolean) => {
    setExpandedSet((prev) => {
      if (prev.has(id) === next) return prev;
      const updated = new Set(prev);
      if (next) updated.add(id);
      else updated.delete(id);
      return updated;
    });
  };

  const activateRow = (
    index: number,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => {
    const node = rows[index];
    if (!node) return;
    selection?.selectOnly(node.id);
    onRowClick?.(node.row, node, event);
  };

  const handleBodyClick = (event: MouseEvent<HTMLTableSectionElement>) => {
    const rowElement = closestRow(event.target, bodyRef.current);
    if (!rowElement) return;
    if (isInteractiveDescendant(event.target, rowElement)) return;
    activateRow(rowIndexOf(rowElement), event);
  };

  const handleBodyFocus = (event: FocusEvent<HTMLTableSectionElement>) => {
    const rowElement = closestRow(event.target, bodyRef.current);
    if (rowElement) nav.setActiveIndex(rowIndexOf(rowElement));
  };

  const handleBodyKeyDown = (event: KeyboardEvent<HTMLTableSectionElement>) => {
    const target = event.target;
    if (!(target instanceof HTMLElement) || target.dataset['rowIndex'] === undefined) return;
    const index = rowIndexOf(target);
    const node = rows[index];
    if (!node) return;

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        nav.focusRow(index + 1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        nav.focusRow(index - 1);
        break;
      case 'ArrowRight':
        event.preventDefault();
        if (!node.hasChildren) break;
        // Open, then — on a second press — descend. Two presses, two effects,
        // which is what the APG treegrid pattern specifies.
        if (!node.expanded) setExpanded(node.id, true);
        else nav.focusRow(index + 1);
        break;
      case 'ArrowLeft':
        event.preventDefault();
        if (node.expanded) {
          setExpanded(node.id, false);
        } else if (node.parentId !== null) {
          const parent = indexById.get(node.parentId);
          if (parent !== undefined) nav.focusRow(parent);
        }
        break;
      case 'Home':
        event.preventDefault();
        nav.focusRow(0);
        break;
      case 'End':
        event.preventDefault();
        nav.focusRow(rows.length - 1);
        break;
      case ' ':
        if (!selection) break;
        event.preventDefault();
        selection.toggle(node.id);
        break;
      case 'Enter':
        event.preventDefault();
        activateRow(index, event);
        break;
      default:
        break;
    }
  };

  const totalColumns = columns.length + (hasSelection ? 1 : 0);
  const showEmpty = !loading && rows.length === 0;

  const rootStyle: TableCSSVars = {
    '--stratum-table-select-w': `${SELECT_COLUMN_WIDTH}px`,
    '--stratum-tree-indent': `${indentSize}px`,
    ...style,
  };

  return (
    <table
      // Placed before the spread: after it, the `undefined` branches would win
      // the object literal and delete a consumer's own attribute instead of
      // defaulting it. `role` stays below — `treegrid` is what the row
      // `aria-level`/`aria-expanded` contract and the keyboard model require.
      aria-multiselectable={hasSelection ? true : undefined}
      aria-busy={loading || undefined}
      {...rest}
      ref={ref}
      role="treegrid"
      data-stratum="tree-table"
      data-density={density}
      data-layout={layout}
      data-zebra={zebra || undefined}
      data-bordered={bordered || undefined}
      data-sticky-header={stickyHeader || undefined}
      data-interactive
      data-guides={guides}
      className={clsx('stratum-table', 'stratum-tree-table', className)}
      style={rootStyle}
    >
      {(caption || loading) && (
        <caption className="stratum-table__caption">
          {caption}
          {loading && <span className="stratum-visually-hidden">{loadingLabel}</span>}
        </caption>
      )}

      <colgroup>
        {hasSelection && <col style={{ width: SELECT_COLUMN_WIDTH }} />}
        {columns.map((column) => (
          <col
            key={column.key}
            style={column.width === undefined ? undefined : { width: column.width }}
          />
        ))}
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
          {columns.map((column, columnIndex) => {
            const cell = columnLayout.cells[columnIndex];
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
        onClick={handleBodyClick}
        onKeyDown={handleBodyKeyDown}
        onFocus={handleBodyFocus}
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
          rows.map((node, index) => {
            const isSelected = selection?.selected.has(node.id) ?? false;

            return (
              <tr
                key={node.id}
                data-row-index={index}
                data-row-id={node.id}
                data-level={node.level}
                data-selected={isSelected || undefined}
                aria-level={node.level + 1}
                aria-posinset={node.posInSet}
                aria-setsize={node.setSize}
                aria-expanded={node.hasChildren ? node.expanded : undefined}
                aria-selected={hasSelection ? isSelected : undefined}
                tabIndex={index === Math.max(nav.activeIndex, 0) ? 0 : -1}
                className={clsx('stratum-table__row', rowClassName?.(node.row, node))}
              >
                {selection && (
                  <td
                    className="stratum-table__cell stratum-table__select-cell"
                    data-sticky={columnLayout.selectSticky ? 'start' : undefined}
                    onClick={stopRowPropagation}
                    onMouseDown={stopRowPropagation}
                  >
                    <label className="stratum-table__select-hit">
                      <SelectionCheckbox
                        checked={isSelected}
                        label={labelSelectRow(node.row, index)}
                        onChange={() => selection.toggle(node.id)}
                      />
                    </label>
                  </td>
                )}

                {columns.map((column, columnIndex) => {
                  const cell = columnLayout.cells[columnIndex];
                  const isTreeColumn = column.key === treeKey;

                  return (
                    <td
                      key={column.key}
                      className={clsx('stratum-table__cell', column.className, {
                        'stratum-tree-table__cell': isTreeColumn,
                      })}
                      data-align={column.align}
                      data-sticky={cell?.sticky}
                      data-sticky-edge={cell?.edge || undefined}
                      style={cell?.style}
                    >
                      {isTreeColumn ? (
                        <span className="stratum-tree-table__tree-cell">
                          <TreeGuides node={node} glyphs={glyphs} mode={guides} />
                          <button
                            type="button"
                            className="stratum-table__disclosure stratum-tree-table__twisty"
                            data-hidden={node.hasChildren ? undefined : true}
                            // The ROW owns `aria-expanded`, so exposing the
                            // twisty as a second control would announce the
                            // same state twice and add a tab stop that the
                            // treegrid pattern deliberately does not have.
                            aria-hidden="true"
                            tabIndex={-1}
                            title={node.expanded ? labelCollapse : labelExpand}
                            onClick={() => setExpanded(node.id, !node.expanded)}
                          >
                            <DisclosureGlyph expanded={node.expanded} />
                          </button>
                          <span className="stratum-tree-table__label">
                            {column.cell(node.row, index)}
                          </span>
                        </span>
                      ) : (
                        column.cell(node.row, index)
                      )}
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

export const TreeTable = TreeTableRoot as <T>(
  props: TreeTableProps<T> & RefAttributes<HTMLTableElement>,
) => ReactElement;

/* -------------------------------------------------------------------------- */

const EMPTY_EXPANDED: Set<string> = new Set();

interface TreeGuidesProps<T> {
  node: FlatRow<T>;
  glyphs: TreeGuideGlyphs;
  mode: TreeGuideMode;
}

/**
 * One fixed-width slot per level. The last slot is this row's branch; the ones
 * before it continue an ancestor's sibling line, or are blank where that
 * ancestor was the last of its group.
 *
 * The slots are rendered in every mode, because they are also what produces
 * the indentation — dropping them would make the tree jump sideways when the
 * guides are turned off.
 */
function TreeGuides<T>({ node, glyphs, mode }: TreeGuidesProps<T>) {
  if (node.level === 0) return null;

  return (
    <span className="stratum-tree-table__guides" aria-hidden="true">
      {Array.from({ length: node.level }, (_unused, slot) => {
        const isBranch = slot === node.level - 1;
        const isRail = !isBranch && node.rails[slot] === true;
        const glyph = isBranch
          ? node.isLast
            ? glyphs.lastBranch
            : glyphs.branch
          : isRail
            ? glyphs.vertical
            : glyphs.blank;

        return (
          <span
            key={slot}
            className="stratum-tree-table__guide"
            data-branch={isBranch || undefined}
            data-continues={isBranch && !node.isLast ? true : undefined}
            data-rail={isRail || undefined}
          >
            {mode === 'none' ? '' : glyph}
          </span>
        );
      })}
    </span>
  );
}
