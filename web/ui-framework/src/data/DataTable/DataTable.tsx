import { forwardRef, useEffect, useMemo, useRef, useState } from 'react';
import type {
  FocusEvent,
  ForwardedRef,
  HTMLAttributes,
  KeyboardEvent,
  MouseEvent,
  ReactElement,
  ReactNode,
  Ref,
  RefAttributes,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { SearchInput } from '../../components/SearchInput/SearchInput';
import { isInteractiveDescendant } from '../selection';
import { closestRow, rowIndexOf, useRowNavigation } from '../rowNavigation';
import {
  DisclosureGlyph,
  SELECT_COLUMN_WIDTH,
  SelectionCheckbox,
  SkeletonRows,
  SortGlyph,
  stopRowPropagation,
} from '../Table/parts';
import type {
  TableAlign,
  TableCSSVars,
  TableDensity,
  TableSelection,
} from '../Table/Table';
import '../Table/Table.css';
import './DataTable.css';

/* OPTIONAL PEERS — @tanstack/react-table and @tanstack/react-virtual.
 *
 * The specifiers are literal on purpose. A variable specifier stops the
 * bundler resolving them at build time, but it also stops the DEV server
 * pre-bundling them, so the import fails at runtime even when the package is
 * installed — trading a build-time problem for a worse one.
 *
 * What keeps them genuinely optional is the build: `preserveModules` emits
 * this file separately, so a consumer who never imports DataTable never pulls
 * it into their module graph and never sees these imports at all. A consumer
 * who DOES import DataTable is expected to install the peers, which is exactly
 * what `peerDependenciesMeta.optional` means. The `.catch` below turns a
 * missing package into an actionable message rather than a stack trace.
 */


/* ==========================================================================
 * PUBLIC TYPES
 *
 * Deliberately free of any `@tanstack/*` type. The engine is an OPTIONAL peer
 * dependency, so a consumer who never renders a DataTable must not need it
 * installed for their own `tsc` to pass — which is exactly what would happen
 * if a single exported interface referenced it.
 * ========================================================================== */

export interface SortRule {
  /** `key` of the column being sorted. */
  key: string;
  desc: boolean;
}

export interface DataTableColumn<T> {
  key: string;
  header: ReactNode;
  /** Plain-text header name, for when `header` is a node. */
  headerLabel?: string;
  /** Cell renderer. Falls back to the string form of `value`. */
  cell?: (row: T, index: number) => ReactNode;
  /**
   * Sortable / filterable value for the row. Sorting and the global filter both
   * read this; a column without it can render anything but cannot be sorted or
   * searched, and says so in development.
   */
  value?: (row: T) => string | number | boolean | Date | null | undefined;
  /** Initial width in px. Default 150. */
  width?: number;
  minWidth?: number;
  maxWidth?: number;
  align?: TableAlign;
  sortable?: boolean;
  /** Whether the column may be hidden. Default true. */
  hideable?: boolean;
  /** Pins the column to the leading edge. Pin a LEADING RUN of columns. */
  sticky?: boolean;
  /** Include in the global filter. Defaults to true when `value` is present. */
  filterable?: boolean;
  className?: string;
  headerClassName?: string;
}

export interface DataTableProps<T> extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  data: readonly T[];
  columns: readonly DataTableColumn<T>[];
  /** Stable identity. Must be unique across the whole tree when nesting. */
  rowKey: (row: T) => string;

  /* -- Sorting ------------------------------------------------------------ */
  sorting?: SortRule[];
  defaultSorting?: SortRule[];
  onSortingChange?: (sorting: SortRule[]) => void;
  /** Shift-click adds a column to the sort. Default true. */
  multiSort?: boolean;

  /* -- Column visibility -------------------------------------------------- */
  columnVisibility?: Record<string, boolean>;
  defaultColumnVisibility?: Record<string, boolean>;
  onColumnVisibilityChange?: (visibility: Record<string, boolean>) => void;
  /** Renders the visibility toggles in the toolbar. */
  columnToggles?: boolean;

  /* -- Column sizing ------------------------------------------------------ */
  columnSizing?: Record<string, number>;
  defaultColumnSizing?: Record<string, number>;
  onColumnSizingChange?: (sizing: Record<string, number>) => void;
  /** Adds a drag handle — and a keyboard-operable one — to every header. */
  resizable?: boolean;
  /** Px per arrow-key press while resizing. Default 8; Shift multiplies by 4. */
  resizeStep?: number;

  /* -- Global filter ------------------------------------------------------ */
  globalFilter?: string;
  defaultGlobalFilter?: string;
  onGlobalFilterChange?: (value: string) => void;
  /** Renders the search field in the toolbar. */
  filterable?: boolean;

  /* -- Expansion ---------------------------------------------------------- */
  /** Hierarchical children. Expanded children render as ordinary rows. */
  getSubRows?: (row: T) => readonly T[] | undefined;
  /** Detail panel rendered in a full-width row beneath its parent. */
  renderSubRow?: (row: T) => ReactNode;
  /** Whether a row can open a detail panel. Default: all of them. */
  canExpandRow?: (row: T) => boolean;
  expanded?: Set<string>;
  defaultExpanded?: Set<string>;
  onExpandedChange?: (expanded: Set<string>) => void;

  /* -- Virtualisation ----------------------------------------------------- */
  /**
   * Renders only the visible window. Requires `height`, because a virtualiser
   * needs a bounded scroll container to measure against.
   */
  virtualize?: boolean;
  /** Height of the scroll viewport. Numbers are px. */
  height?: number | string;
  /** Starting row-height guess. Defaults to the density's natural height. */
  estimateRowHeight?: number;
  /** Rows rendered beyond the window. Default 8. */
  overscan?: number;
  /**
   * Measures each rendered row instead of trusting `estimateRowHeight`.
   *
   * Default true, because a wrong estimate makes the scrollbar drift and that
   * is a worse failure than a slower scroll. Turn it OFF when every row is the
   * same height — it removes one `ResizeObserver` per rendered row and one
   * forced measurement per row mount, which is the single biggest saving
   * available on a very long list. */
  dynamicRowHeight?: boolean;

  /* -- Presentation ------------------------------------------------------- */
  density?: TableDensity;
  /**
   * Default `'fixed'`: column widths are authoritative, which is what sizing,
   * resizing and pinned columns all need. The trailing column absorbs any
   * width the viewport has spare, so the columns before it keep exactly the
   * width they were given.
   */
  layout?: 'auto' | 'fixed';
  /** Default true — a data table nearly always wants its header pinned. */
  stickyHeader?: boolean;
  zebra?: boolean;
  bordered?: boolean;
  loading?: boolean;
  loadingRows?: number;
  loadingLabel?: string;
  emptyState?: ReactNode;
  caption?: ReactNode;
  /** Applied to the inner `<table>`; `className` goes on the viewport. */
  tableClassName?: string;

  /* -- Interaction -------------------------------------------------------- */
  onRowClick?: (
    row: T,
    index: number,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => void;
  selection?: TableSelection;

  /* -- Text --------------------------------------------------------------- */
  labelSelectAll?: string;
  labelSelectRow?: (row: T, index: number) => string;
  labelFilter?: string;
  filterPlaceholder?: string;
  /** Accessible name for the filter field's clear button. */
  labelClearFilter?: string;
  labelColumns?: string;
  /** Receives the column's header text. */
  labelResizeColumn?: (column: string) => string;
  labelExpandRow?: string;
  labelCollapseRow?: string;
  /**
   * Accessible name for the scroll container.
   *
   * The rows scroll inside their own box, so that box is a keyboard tab stop
   * (WCAG 2.1 SC 2.1.1 — a region nobody can scroll without a mouse). A stop
   * with no name is a stop a screen reader announces as nothing, so name it
   * after what is in it whenever a page carries more than one table.
   */
  labelScrollRegion?: string;
  /** Receives the column's header text and the order the next click applies. */
  labelSortBy?: (column: string, next: 'ascending' | 'descending' | 'none') => string;
}

/* ==========================================================================
 * ENGINE BINDINGS
 *
 * Hand-written structural types for the slice of TanStack Table v9 and
 * TanStack Virtual v3 this component touches. Writing them out is what lets
 * the import stay dynamic AND the emitted `.d.ts` stay free of the optional
 * peers.
 * ========================================================================== */

type EngineSortDirection = 'asc' | 'desc';

interface EngineColumn {
  id: string;
  columnDef: { enableGlobalFilter?: boolean };
  getIsVisible: () => boolean;
  getCanHide: () => boolean;
  getCanSort: () => boolean;
  getIsSorted: () => false | EngineSortDirection;
  getSortIndex: () => number;
  getNextSortingOrder: (multi?: boolean) => EngineSortDirection | false;
  getToggleSortingHandler: () => undefined | ((event: unknown) => void);
  getSize: () => number;
  toggleVisibility: (value?: boolean) => void;
}

interface EngineHeader {
  id: string;
  colSpan: number;
  isPlaceholder: boolean;
  column: EngineColumn;
  getSize: () => number;
  getResizeHandler: (context?: Document) => (event: unknown) => void;
}

interface EngineRow<T> {
  id: string;
  index: number;
  depth: number;
  original: T;
  subRows: EngineRow<T>[];
  getCanExpand: () => boolean;
  getIsExpanded: () => boolean;
  toggleExpanded: (expanded?: boolean) => void;
}

interface EngineTable<T> {
  getHeaderGroups: () => Array<{ id: string; headers: EngineHeader[] }>;
  getRowModel: () => { rows: Array<EngineRow<T>> };
  getVisibleLeafColumns: () => EngineColumn[];
  getAllLeafColumns: () => EngineColumn[];
  getTotalSize: () => number;
}

interface TableEngine {
  useTable: (options: Record<string, unknown>) => EngineTable<unknown>;
  features: unknown;
}

interface VirtualItem {
  index: number;
  key: string | number;
  start: number;
  end: number;
  size: number;
}

interface VirtualizerInstance {
  getVirtualItems: () => VirtualItem[];
  getTotalSize: () => number;
  measureElement: (node: Element | null) => void;
  scrollToIndex: (index: number, options?: { align?: 'start' | 'center' | 'end' | 'auto' }) => void;
}

type UseVirtualizer = (options: {
  count: number;
  getScrollElement: () => HTMLElement | null;
  estimateSize: (index: number) => number;
  overscan?: number;
  getItemKey?: (index: number) => string | number;
}) => VirtualizerInstance;

interface VirtualEngine {
  useVirtualizer: UseVirtualizer;
}

const MISSING_TABLE =
  '[stratum] <DataTable> needs the optional peer dependency "@tanstack/react-table" (v9). ' +
  'Install it with `npm i @tanstack/react-table`, or use <Table> instead, which has no dependencies.';

const MISSING_VIRTUAL =
  '[stratum] <DataTable virtualize> needs the optional peer dependency "@tanstack/react-virtual" (v3). ' +
  'Install it with `npm i @tanstack/react-virtual`, or drop `virtualize`.';

const WRONG_TABLE_VERSION =
  '[stratum] <DataTable> found "@tanstack/react-table" but not its v9 API (`useTable` / `tableFeatures`). ' +
  'Upgrade to v9.';

let enginePromise: Promise<TableEngine> | null = null;
let virtualPromise: Promise<VirtualEngine> | null = null;

/**
 * Loads the table engine once per process and assembles its feature set.
 *
 * v9 is tree-shakeable: every feature, and every row model those features
 * need, has to be listed explicitly. They are all listed here rather than
 * being derived from props, because `tableFeatures` is meant to be called once
 * and statically — deriving it per-prop-combination would rebuild the table's
 * prototypes whenever a flag flipped.
 */
function loadEngine(): Promise<TableEngine> {
  enginePromise ??= import('@tanstack/react-table')
    .catch((cause: unknown) => {
      throw new Error(MISSING_TABLE, { cause });
    })
    .then((imported) => {
      const mod = imported as unknown as Record<string, unknown>;
      const useTable = mod['useTable'];
      const tableFeatures = mod['tableFeatures'];
      if (typeof useTable !== 'function' || typeof tableFeatures !== 'function') {
        throw new Error(WRONG_TABLE_VERSION);
      }

      const create = (name: string): unknown => {
        const factory = mod[name];
        return typeof factory === 'function' ? (factory as () => unknown)() : undefined;
      };

      const features = (tableFeatures as (input: unknown) => unknown)({
        rowSortingFeature: mod['rowSortingFeature'],
        sortedRowModel: create('createSortedRowModel'),
        sortFns: mod['sortFns'],

        columnVisibilityFeature: mod['columnVisibilityFeature'],

        columnSizingFeature: mod['columnSizingFeature'],
        columnResizingFeature: mod['columnResizingFeature'],

        rowExpandingFeature: mod['rowExpandingFeature'],
        expandedRowModel: create('createExpandedRowModel'),

        columnFilteringFeature: mod['columnFilteringFeature'],
        filteredRowModel: create('createFilteredRowModel'),
        filterFns: mod['filterFns'],
        globalFilteringFeature: mod['globalFilteringFeature'],
      });

      return {
        useTable: useTable as TableEngine['useTable'],
        features,
      };
    });

  return enginePromise;
}

function loadVirtual(): Promise<VirtualEngine> {
  virtualPromise ??= import('@tanstack/react-virtual')
    .catch((cause: unknown) => {
      throw new Error(MISSING_VIRTUAL, { cause });
    })
    .then((imported) => {
      const mod = imported as unknown as Record<string, unknown>;
      const useVirtualizer = mod['useVirtualizer'];
      if (typeof useVirtualizer !== 'function') throw new Error(MISSING_VIRTUAL);
      return { useVirtualizer: useVirtualizer as UseVirtualizer };
    });

  return virtualPromise;
}

interface EngineState {
  engine?: TableEngine;
  virtual?: VirtualEngine;
  error?: Error;
}

function useEngines(needVirtual: boolean): EngineState {
  const [state, setState] = useState<EngineState>({});

  useEffect(() => {
    let cancelled = false;
    Promise.all([loadEngine(), needVirtual ? loadVirtual() : Promise.resolve(undefined)])
      .then(([engine, virtual]) => {
        if (cancelled) return;
        setState(virtual ? { engine, virtual } : { engine });
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setState({ error: error instanceof Error ? error : new Error(String(error)) });
      });
    return () => {
      cancelled = true;
    };
  }, [needVirtual]);

  return state;
}

/* ==========================================================================
 * COMPONENT
 * ========================================================================== */

/**
 * The full-power table: sorting, column visibility, column sizing, row
 * virtualisation, expandable sub-rows, a pinned header, pinned leading
 * columns, and a global filter.
 *
 * WHY THE ENGINE IS LOADED, NOT IMPORTED
 * --------------------------------------
 * `@tanstack/react-table` and `@tanstack/react-virtual` are optional peers.
 * Importing them at the top of this file would put ~30 KB into the bundle of
 * every consumer who only ever renders a `<Table>`. They are loaded on first
 * render of a DataTable instead, and a missing package produces a sentence
 * that names the package and the install command rather than
 * `Cannot read properties of undefined`.
 *
 * WHAT IS NOT DELEGATED TO THE ENGINE
 * -----------------------------------
 * Rendering. Cells come from this component's own `cell(row, index)`, never
 * from `flexRender`, which keeps the engine's types out of the public API and
 * keeps a DataTable column definition identical in shape to a Table one.
 *
 * PERFORMANCE
 * -----------
 * Row handlers are delegated from `<tbody>`, so a 10 000-row body allocates no
 * per-row closures for click, focus or keyboard. No row transitions, no row
 * entry animation, no static `will-change`.
 */
const DataTableRoot = forwardRef(function DataTable<T>(
  props: DataTableProps<T>,
  ref: ForwardedRef<HTMLDivElement>,
) {
  const wantsVirtual = Boolean(props.virtualize && props.height != null);
  const { engine, virtual, error } = useEngines(wantsVirtual);

  if (import.meta.env?.DEV && props.virtualize && props.height == null) {
    console.error(
      '[stratum] <DataTable virtualize> also needs `height`. A virtualiser has ' +
        'nothing to measure against without a bounded scroll container, so ' +
        'virtualisation stays off until one is given.',
    );
  }

  // Thrown during render so an error boundary can present it. Returning a
  // broken table silently would be worse: the symptom (no rows, no sorting)
  // looks like a data problem, not a missing dependency.
  if (error) throw error;

  if (!engine) {
    return (
      <div
        ref={ref}
        data-stratum="data-table"
        className={clsx('stratum-data-table', props.className)}
        style={props.height == null ? props.style : { blockSize: props.height, ...props.style }}
        aria-busy="true"
      >
        <span className="stratum-visually-hidden">{props.loadingLabel ?? 'Loading rows'}</span>
      </div>
    );
  }

  return (
    <DataTableInner
      {...props}
      engine={engine}
      virtualEngine={wantsVirtual ? virtual : undefined}
      containerRef={ref}
    />
  );
});

export const DataTable = DataTableRoot as <T>(
  props: DataTableProps<T> & RefAttributes<HTMLDivElement>,
) => ReactElement;

/* -------------------------------------------------------------------------- */

interface DataTableInnerProps<T> extends DataTableProps<T> {
  engine: TableEngine;
  virtualEngine: VirtualEngine | undefined;
  containerRef: Ref<HTMLDivElement>;
}

/**
 * Natural row height per density: 13px text at `leading-snug`, plus the
 * density's vertical padding, plus the 1px row divider. Measured, not guessed,
 * so the scrollbar is close to correct on the first paint and exact once the
 * rows report their real heights.
 */
const DENSITY_ROW_HEIGHT: Record<TableDensity, number> = {
  compact: 23,
  default: 31,
  comfortable: 39,
};

type DisplayRow<T> = { kind: 'row' | 'detail'; row: EngineRow<T> };

function DataTableInner<T>({
  data,
  columns,
  rowKey,
  sorting,
  defaultSorting,
  onSortingChange,
  multiSort = true,
  columnVisibility,
  defaultColumnVisibility,
  onColumnVisibilityChange,
  columnToggles = false,
  columnSizing,
  defaultColumnSizing,
  onColumnSizingChange,
  resizable = false,
  resizeStep = 8,
  globalFilter,
  defaultGlobalFilter,
  onGlobalFilterChange,
  filterable = false,
  getSubRows,
  renderSubRow,
  canExpandRow,
  expanded,
  defaultExpanded,
  onExpandedChange,
  virtualize = false,
  height,
  estimateRowHeight,
  overscan = 8,
  dynamicRowHeight = true,
  density = 'default',
  layout = 'fixed',
  stickyHeader = true,
  zebra = false,
  bordered = false,
  loading = false,
  loadingRows = 8,
  loadingLabel = 'Loading rows',
  emptyState = 'No rows',
  caption,
  tableClassName,
  onRowClick,
  selection,
  labelSelectAll = 'Select all rows',
  labelSelectRow = () => 'Select row',
  labelFilter = 'Filter rows',
  filterPlaceholder = 'Filter…',
  labelClearFilter = 'Clear filter',
  labelColumns = 'Columns',
  labelResizeColumn = (column) => `Resize ${column}`,
  labelExpandRow = 'Expand row',
  labelCollapseRow = 'Collapse row',
  labelScrollRegion = 'Table rows',
  labelSortBy = (column, next) => `Sort by ${column}, ${next}`,
  engine,
  virtualEngine,
  containerRef,
  className,
  style,
  ...rest
}: DataTableInnerProps<T>) {
  /**
   * The scroll container is held in STATE, not a ref.
   *
   * React attaches a host ref during the layout phase working upward from the
   * leaves, so a layout effect inside a descendant — which is exactly what the
   * virtualiser installs — runs BEFORE this ancestor's ref exists. Reading it
   * from a ref therefore yields `null` on the only pass that matters, the
   * virtualiser observes nothing, and the table renders zero rows forever
   * because nothing ever triggers a second attempt. Holding the element in
   * state makes its arrival a render, which is what the virtualiser needs.
   */
  const [scrollElement, setScrollElement] = useState<HTMLDivElement | null>(null);
  const bodyRef = useRef<HTMLTableSectionElement>(null);
  const virtualizerRef = useRef<VirtualizerInstance | null>(null);

  const hasSelection = Boolean(selection);
  const interactive = Boolean(onRowClick || selection || renderSubRow || getSubRows);

  /* -- Controlled / uncontrolled state ------------------------------------ */
  const [sortRules, setSortRules] = useControllableState<SortRule[]>({
    value: sorting,
    defaultValue: defaultSorting ?? EMPTY_SORT,
    onChange: onSortingChange,
  });
  const [visibility, setVisibility] = useControllableState<Record<string, boolean>>({
    value: columnVisibility,
    defaultValue: defaultColumnVisibility ?? EMPTY_RECORD,
    onChange: onColumnVisibilityChange,
  });
  const [sizing, setSizing] = useControllableState<Record<string, number>>({
    value: columnSizing,
    defaultValue: defaultColumnSizing ?? EMPTY_SIZING,
    onChange: onColumnSizingChange,
  });
  const [filterValue, setFilterValue] = useControllableState<string>({
    value: globalFilter,
    defaultValue: defaultGlobalFilter ?? '',
    onChange: onGlobalFilterChange,
  });
  const [expandedSet, setExpandedSet] = useControllableState<Set<string>>({
    value: expanded,
    defaultValue: defaultExpanded ?? EMPTY_EXPANDED,
    onChange: onExpandedChange,
  });

  /* -- Engine wiring ------------------------------------------------------ */
  const columnByKey = useMemo(() => {
    const map = new Map<string, DataTableColumn<T>>();
    for (const column of columns) map.set(column.key, column);
    return map;
  }, [columns]);

  const engineColumns = useMemo(
    () =>
      columns.map((column) => {
        const sortable = column.sortable === true && column.value !== undefined;
        if (import.meta.env?.DEV && column.sortable && column.value === undefined) {
          console.error(
            `[stratum] <DataTable> column "${column.key}" is sortable but has no \`value\`. ` +
              'Sorting compares `value(row)`, so the column stays unsorted.',
          );
        }
        return {
          id: column.key,
          accessorFn: column.value ?? (() => undefined),
          header: column.headerLabel ?? column.key,
          enableSorting: sortable,
          enableHiding: column.hideable !== false,
          enableResizing: resizable,
          enableGlobalFilter: (column.filterable ?? column.value !== undefined) === true,
          size: column.width ?? 150,
          ...(column.minWidth === undefined ? {} : { minSize: column.minWidth }),
          ...(column.maxWidth === undefined ? {} : { maxSize: column.maxWidth }),
        };
      }),
    [columns, resizable],
  );

  const engineSorting = useMemo(
    () => sortRules.map((rule) => ({ id: rule.key, desc: rule.desc })),
    [sortRules],
  );
  const engineExpanded = useMemo(() => {
    const record: Record<string, boolean> = {};
    for (const id of expandedSet) record[id] = true;
    return record;
  }, [expandedSet]);

  const handleSortingChange = useEventCallback((updater: unknown) => {
    const next = applyUpdater<Array<{ id: string; desc: boolean }>>(updater, engineSorting);
    setSortRules(next.map((rule) => ({ key: rule.id, desc: rule.desc })));
  });
  const handleVisibilityChange = useEventCallback((updater: unknown) => {
    setVisibility(applyUpdater<Record<string, boolean>>(updater, visibility));
  });
  const handleSizingChange = useEventCallback((updater: unknown) => {
    setSizing(applyUpdater<Record<string, number>>(updater, sizing));
  });
  const handleFilterChange = useEventCallback((updater: unknown) => {
    setFilterValue(applyUpdater<string>(updater, filterValue));
  });
  const handleExpandedChange = useEventCallback((updater: unknown) => {
    const next = applyUpdater<Record<string, boolean> | true>(updater, engineExpanded);
    // `true` is the engine's "everything expanded" sentinel. Nothing here can
    // produce it, and turning it into a set would need every id in the tree,
    // so it is ignored rather than guessed at.
    if (next === true) return;
    const ids = new Set<string>();
    for (const [id, open] of Object.entries(next)) if (open) ids.add(id);
    setExpandedSet(ids);
  });

  const table = engine.useTable({
    features: engine.features,
    data,
    columns: engineColumns,
    getRowId: (row: unknown) => rowKey(row as T),
    ...(getSubRows ? { getSubRows: (row: unknown) => getSubRows(row as T) } : {}),
    ...(renderSubRow
      ? {
          getRowCanExpand: (row: unknown) => {
            const engineRow = row as EngineRow<T>;
            if (engineRow.subRows.length > 0) return true;
            return canExpandRow ? canExpandRow(engineRow.original) : true;
          },
        }
      : {}),
    state: {
      sorting: engineSorting,
      columnVisibility: visibility,
      columnSizing: sizing,
      globalFilter: filterValue,
      expanded: engineExpanded,
    },
    onSortingChange: handleSortingChange,
    onColumnVisibilityChange: handleVisibilityChange,
    onColumnSizingChange: handleSizingChange,
    onGlobalFilterChange: handleFilterChange,
    onExpandedChange: handleExpandedChange,
    globalFilterFn: 'includesString',
    getColumnCanGlobalFilter: (column: EngineColumn) => column.columnDef.enableGlobalFilter === true,
    enableMultiSort: multiSort,
    enableColumnResizing: resizable,
    columnResizeMode: 'onChange',
  }) as EngineTable<T>;

  const rows = table.getRowModel().rows;

  /* -- Display list -------------------------------------------------------
   * Detail panels are flattened in alongside their parents so that ONE virtual
   * item is always exactly ONE <tr>. Measuring a virtual item that renders two
   * sibling rows is impossible — there is no element that wraps them — and the
   * scroll height silently drifts as panels open. */
  const display = useMemo<Array<DisplayRow<T>>>(() => {
    if (!renderSubRow) return rows.map((row) => ({ kind: 'row' as const, row }));
    const out: Array<DisplayRow<T>> = [];
    for (const row of rows) {
      out.push({ kind: 'row', row });
      if (row.getIsExpanded()) out.push({ kind: 'detail', row });
    }
    return out;
  }, [rows, renderSubRow]);

  /* -- Columns ------------------------------------------------------------ */
  const headerGroups = table.getHeaderGroups();
  const visibleColumns = table.getVisibleLeafColumns();

  const pinned = useMemo(() => {
    const offsets = new Map<string, number>();
    let run = 0;
    let last: string | null = null;
    let anyPinned = false;

    for (const column of visibleColumns) {
      if (columnByKey.get(column.id)?.sticky !== true) break; // leading run only
      anyPinned = true;
      offsets.set(column.id, run);
      run += column.getSize();
      last = column.id;
    }

    // The checkbox column always leads, so it pins whenever anything does; the
    // offsets above are shifted to make room for it.
    if (anyPinned && hasSelection) {
      for (const [id, offset] of offsets) offsets.set(id, offset + SELECT_COLUMN_WIDTH);
    }

    return { offsets, last, selectSticky: anyPinned && hasSelection };
    // `visibleColumns` is rebuilt by the engine on every render, so the sizes
    // it reports are already current; memoising on it is enough.
  }, [visibleColumns, columnByKey, hasSelection]);

  /* -- Navigation --------------------------------------------------------- */
  const nav = useRowNavigation({
    enabled: interactive,
    count: display.length,
    containerRef: bodyRef,
    onBeforeFocus: virtualize
      ? (index) => virtualizerRef.current?.scrollToIndex(index, { align: 'auto' })
      : undefined,
  });

  const nextFocusable = (from: number, step: 1 | -1): number => {
    let index = from + step;
    while (index >= 0 && index < display.length && display[index]?.kind !== 'row') index += step;
    return index;
  };

  const activateRow = (
    index: number,
    event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>,
  ) => {
    const entry = display[index];
    if (!entry || entry.kind !== 'row') return;
    selection?.selectOnly(entry.row.id);
    onRowClick?.(entry.row.original, entry.row.index, event);
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
    const entry = display[index];
    if (!entry) return;

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        nav.focusRow(nextFocusable(index, 1));
        break;
      case 'ArrowUp':
        event.preventDefault();
        nav.focusRow(nextFocusable(index, -1));
        break;
      case 'ArrowRight':
        if (!entry.row.getCanExpand() || entry.row.getIsExpanded()) break;
        event.preventDefault();
        entry.row.toggleExpanded(true);
        break;
      case 'ArrowLeft':
        if (!entry.row.getIsExpanded()) break;
        event.preventDefault();
        entry.row.toggleExpanded(false);
        break;
      case 'Home':
        event.preventDefault();
        nav.focusRow(0);
        break;
      case 'End':
        event.preventDefault();
        nav.focusRow(nextFocusable(display.length, -1));
        break;
      case ' ':
        if (!selection) break;
        event.preventDefault();
        selection.toggle(entry.row.id);
        break;
      case 'Enter':
        event.preventDefault();
        activateRow(index, event);
        break;
      default:
        break;
    }
  };

  /* -- Resizing ----------------------------------------------------------- */
  const adjustSize = (columnId: string, delta: number) => {
    const definition = columnByKey.get(columnId);
    const current = sizing[columnId] ?? definition?.width ?? 150;
    const min = definition?.minWidth ?? 40;
    const max = definition?.maxWidth ?? Number.MAX_SAFE_INTEGER;
    setSizing({ ...sizing, [columnId]: Math.min(Math.max(current + delta, min), max) });
  };

  /* -- Render ------------------------------------------------------------- */
  const totalColumns = visibleColumns.length + (hasSelection ? 1 : 0);
  const showEmpty = !loading && display.length === 0;
  const rowHeight = estimateRowHeight ?? DENSITY_ROW_HEIGHT[density];
  const canVirtualize =
    virtualize && virtualEngine !== undefined && !loading && scrollElement !== null;

  const viewportStyle: TableCSSVars = {
    '--stratum-table-select-w': `${SELECT_COLUMN_WIDTH}px`,
    ...(height == null ? {} : { blockSize: typeof height === 'number' ? `${height}px` : height }),
    ...style,
  };

  const renderRow = (
    entry: DisplayRow<T>,
    index: number,
    measureRef?: (node: HTMLTableRowElement | null) => void,
  ): ReactNode => {
    const engineRow = entry.row;

    if (entry.kind === 'detail') {
      return (
        <tr
          key={`${engineRow.id}::detail`}
          ref={measureRef}
          data-index={index}
          className="stratum-table__row stratum-data-table__detail-row"
          aria-rowindex={canVirtualize ? index + 2 : undefined}
        >
          <td className="stratum-table__cell stratum-data-table__detail-cell" colSpan={totalColumns}>
            {renderSubRow?.(engineRow.original)}
          </td>
        </tr>
      );
    }

    const isSelected = selection?.selected.has(engineRow.id) ?? false;
    const canExpand = engineRow.getCanExpand();
    const isExpanded = engineRow.getIsExpanded();

    return (
      <tr
        key={engineRow.id}
        ref={measureRef}
        data-index={index}
        data-row-index={index}
        data-row-id={engineRow.id}
        data-depth={engineRow.depth || undefined}
        data-selected={isSelected || undefined}
        aria-selected={hasSelection ? isSelected : undefined}
        /* NO `aria-expanded` here. It is only permitted on a row inside a
         * `treegrid`, and this is a `grid` — rows expand into a detail panel
         * just as often as into child rows, which is a disclosure, not a tree.
         * The state lives on the disclosure button below, where the ARIA
         * pattern actually puts it. ArrowLeft/ArrowRight on the row still
         * toggle it; the button is what announces the result. */
        aria-rowindex={canVirtualize ? index + 2 : undefined}
        tabIndex={interactive ? (index === Math.max(nav.activeIndex, 0) ? 0 : -1) : undefined}
        className="stratum-table__row"
      >
        {selection && (
          <td
            className="stratum-table__cell stratum-table__select-cell"
            data-sticky={pinned.selectSticky ? 'start' : undefined}
            onClick={stopRowPropagation}
            onMouseDown={stopRowPropagation}
          >
            <label className="stratum-table__select-hit">
              <SelectionCheckbox
                checked={isSelected}
                label={labelSelectRow(engineRow.original, engineRow.index)}
                onChange={() => selection.toggle(engineRow.id)}
              />
            </label>
          </td>
        )}

        {visibleColumns.map((column, columnIndex) => {
          const definition = columnByKey.get(column.id);
          const offset = pinned.offsets.get(column.id);
          const isFirst = columnIndex === 0;

          return (
            <td
              key={column.id}
              className={clsx('stratum-table__cell', definition?.className)}
              data-align={definition?.align}
              data-sticky={offset === undefined ? undefined : 'start'}
              data-sticky-edge={pinned.last === column.id || undefined}
              style={offset === undefined ? undefined : ({ '--_sticky-offset': `${offset}px` } as TableCSSVars)}
            >
              {isFirst && (canExpand || engineRow.depth > 0) ? (
                <span
                  className="stratum-data-table__tree-cell"
                  style={{ '--_depth': engineRow.depth } as TableCSSVars}
                >
                  <button
                    type="button"
                    className="stratum-table__disclosure"
                    data-hidden={canExpand ? undefined : true}
                    aria-expanded={canExpand ? isExpanded : undefined}
                    aria-label={isExpanded ? labelCollapseRow : labelExpandRow}
                    onClick={() => engineRow.toggleExpanded()}
                  >
                    <DisclosureGlyph expanded={isExpanded} />
                  </button>
                  <span className="stratum-data-table__tree-label">
                    {renderCell(definition, engineRow.original, engineRow.index)}
                  </span>
                </span>
              ) : (
                renderCell(definition, engineRow.original, engineRow.index)
              )}
            </td>
          );
        })}
      </tr>
    );
  };

  const tableElement = (
    <table
      data-stratum="data-table-grid"
      data-density={density}
      data-layout={layout}
      data-zebra={zebra || undefined}
      data-bordered={bordered || undefined}
      data-sticky-header={stickyHeader || undefined}
      data-interactive={interactive || undefined}
      className={clsx('stratum-table', 'stratum-data-table__table', tableClassName)}
      style={{ inlineSize: layout === 'fixed' ? table.getTotalSize() + (hasSelection ? SELECT_COLUMN_WIDTH : 0) : undefined }}
      role="grid"
      aria-multiselectable={hasSelection ? true : undefined}
      aria-busy={loading || undefined}
      aria-rowcount={canVirtualize ? display.length + 1 : undefined}
    >
      {(caption || loading) && (
        <caption className="stratum-table__caption">
          {caption}
          {loading && <span className="stratum-visually-hidden">{loadingLabel}</span>}
        </caption>
      )}

      {/* The trailing column absorbs any width the viewport has spare. Sharing
       * it across every column — what `table-layout: fixed` does by default —
       * silently widens the pinned columns past the widths their sticky
       * offsets were summed from, and they overlap by that difference. */}
      <colgroup>
        {hasSelection && <col style={{ width: SELECT_COLUMN_WIDTH }} />}
        {visibleColumns.map((column, index) => {
          const absorbsSlack = layout === 'fixed' && index === visibleColumns.length - 1;
          return (
            <col key={column.id} style={absorbsSlack ? undefined : { width: column.getSize() }} />
          );
        })}
      </colgroup>

      <thead className="stratum-table__head">
        {headerGroups.map((group) => (
          <tr
            key={group.id}
            className="stratum-table__row stratum-table__row--head"
            aria-rowindex={canVirtualize ? 1 : undefined}
          >
            {hasSelection && selection && (
              <th
                scope="col"
                className="stratum-table__header-cell stratum-table__select-cell"
                data-sticky={pinned.selectSticky ? 'start' : undefined}
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

            {group.headers.map((header) => {
              const definition = columnByKey.get(header.column.id);
              const offset = pinned.offsets.get(header.column.id);
              const sorted = header.column.getIsSorted();
              const canSort = header.column.getCanSort();
              const headerText = definition?.headerLabel ?? textOf(definition?.header) ?? header.column.id;
              const nextOrder = canSort ? header.column.getNextSortingOrder() : false;

              return (
                <th
                  key={header.id}
                  scope="col"
                  colSpan={header.colSpan}
                  className={clsx('stratum-table__header-cell', definition?.headerClassName)}
                  data-align={definition?.align}
                  data-sticky={offset === undefined ? undefined : 'start'}
                  data-sticky-edge={pinned.last === header.column.id || undefined}
                  style={offset === undefined ? undefined : ({ '--_sticky-offset': `${offset}px` } as TableCSSVars)}
                  aria-sort={canSort ? ARIA_SORT[String(sorted)] : undefined}
                >
                  {header.isPlaceholder ? null : canSort ? (
                    <button
                      type="button"
                      className="stratum-table__sort-button"
                      aria-label={labelSortBy(headerText, ARIA_SORT[String(nextOrder)] ?? 'none')}
                      onClick={header.column.getToggleSortingHandler()}
                    >
                      <span className="stratum-table__header-label">{definition?.header}</span>
                      <SortGlyph
                        direction={sorted}
                        order={sorted ? header.column.getSortIndex() + 1 : undefined}
                      />
                    </button>
                  ) : (
                    <span className="stratum-table__header-label">{definition?.header}</span>
                  )}

                  {resizable && !header.isPlaceholder && (
                    <button
                      type="button"
                      className="stratum-data-table__resizer"
                      aria-label={labelResizeColumn(headerText)}
                      onMouseDown={header.getResizeHandler()}
                      onTouchStart={header.getResizeHandler()}
                      onKeyDown={(event) => {
                        const step = event.shiftKey ? resizeStep * 4 : resizeStep;
                        if (event.key === 'ArrowRight') {
                          event.preventDefault();
                          adjustSize(header.column.id, step);
                        } else if (event.key === 'ArrowLeft') {
                          event.preventDefault();
                          adjustSize(header.column.id, -step);
                        }
                      }}
                    />
                  )}
                </th>
              );
            })}
          </tr>
        ))}
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
          (canVirtualize && virtualEngine ? (
            <VirtualRows
              useVirtualizer={virtualEngine.useVirtualizer}
              count={display.length}
              scrollElement={scrollElement}
              estimateSize={rowHeight}
              overscan={overscan}
              columnCount={totalColumns}
              publish={virtualizerRef}
            >
              {(items, measure) =>
                items.map((item) => {
                  const entry = display[item.index];
                  if (!entry) return null;
                  return renderRow(entry, item.index, dynamicRowHeight ? measure : undefined);
                })
              }
            </VirtualRows>
          ) : (
            display.map((entry, index) => renderRow(entry, index))
          ))}
      </tbody>
    </table>
  );

  return (
    <div
      {...rest}
      ref={containerRef}
      data-stratum="data-table"
      data-virtualized={canVirtualize || undefined}
      className={clsx('stratum-data-table', className)}
      style={viewportStyle}
    >
      {(filterable || columnToggles) && (
        <div className="stratum-data-table__toolbar">
          {/* The framework's own search field rather than a bare
              `<input type="search">`. The raw element leaked two pieces of
              browser chrome into the toolbar: WebKit's `searchfield`
              appearance, which repaints the border and ignores the declared
              height, and the engine's grey `::-webkit-search-cancel-button`,
              which is invisible against a dark surface and matches nothing else
              in the table. It also had no magnifier and no clear control at
              all. SearchInput supplies all of that, plus the focus ring,
              disabled and forced-colors treatments.
              `onSearch` is deliberately unused — filtering still happens from
              `onChange` on every keystroke, so no debounce is introduced. */}
          {filterable && (
            <SearchInput
              className="stratum-data-table__filter"
              size="sm"
              variant="subtle"
              value={filterValue}
              aria-label={labelFilter}
              placeholder={filterPlaceholder}
              clearLabel={labelClearFilter}
              onChange={(event) => setFilterValue(event.target.value)}
            />
          )}

          {columnToggles && (
            <div className="stratum-data-table__columns" role="group" aria-label={labelColumns}>
              {table.getAllLeafColumns().map((column) => {
                const definition = columnByKey.get(column.id);
                if (!column.getCanHide()) return null;
                return (
                  <button
                    key={column.id}
                    type="button"
                    className="stratum-data-table__column-toggle"
                    aria-pressed={column.getIsVisible()}
                    onClick={() => column.toggleVisibility()}
                  >
                    {definition?.headerLabel ?? textOf(definition?.header) ?? column.id}
                  </button>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* The SCROLL container, not the root, is what the virtualiser measures:
          a toolbar above it must not scroll away with the rows.

          It is also a named tab stop. Rows are only focusable when the table is
          `interactive`, so a plain virtualised table was a box a keyboard user
          could reach the end of and never scroll (SC 2.1.1). `role="group"`
          rather than `role="region"` on purpose: `region` is a LANDMARK, and
          two tables on one page would then publish two identically named
          landmarks — a navigation list full of "Table rows" is worse than no
          landmark at all. `group` still carries the name. */}
      <div
        ref={setScrollElement}
        className="stratum-data-table__scroll"
        tabIndex={0}
        role="group"
        aria-label={labelScrollRegion}
      >
        {tableElement}
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */

interface VirtualRowsProps {
  useVirtualizer: UseVirtualizer;
  count: number;
  scrollElement: HTMLElement | null;
  estimateSize: number;
  overscan: number;
  columnCount: number;
  publish: { current: VirtualizerInstance | null };
  children: (
    items: VirtualItem[],
    measure: (node: HTMLTableRowElement | null) => void,
  ) => ReactNode;
}

/**
 * The virtual window, isolated so `useVirtualizer` is only ever called when
 * the package is actually loaded — a hook cannot be called conditionally, so
 * the condition has to be a component boundary.
 *
 * Spacer rows rather than absolute positioning: absolutely positioned `<tr>`
 * elements break table layout entirely, taking column alignment, `colspan` and
 * pinned columns with them. Two zero-content rows carrying the height above
 * and below the window keep the markup a real table.
 */
function VirtualRows({
  useVirtualizer,
  count,
  scrollElement,
  estimateSize,
  overscan,
  columnCount,
  publish,
  children,
}: VirtualRowsProps) {
  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scrollElement,
    estimateSize: () => estimateSize,
    overscan,
  });

  useEffect(() => {
    publish.current = virtualizer;
    return () => {
      publish.current = null;
    };
  }, [publish, virtualizer]);

  const items = virtualizer.getVirtualItems();
  const total = virtualizer.getTotalSize();
  const first = items[0];
  const last = items[items.length - 1];
  const padTop = first ? first.start : 0;
  const padBottom = last ? Math.max(total - last.end, 0) : 0;

  return (
    <>
      {padTop > 0 && (
        <tr aria-hidden="true" className="stratum-data-table__spacer">
          <td className="stratum-data-table__spacer-cell" colSpan={columnCount} style={{ blockSize: padTop }} />
        </tr>
      )}
      {children(items, virtualizer.measureElement)}
      {padBottom > 0 && (
        <tr aria-hidden="true" className="stratum-data-table__spacer">
          <td className="stratum-data-table__spacer-cell" colSpan={columnCount} style={{ blockSize: padBottom }} />
        </tr>
      )}
    </>
  );
}

/* -------------------------------------------------------------------------- */

const EMPTY_SORT: SortRule[] = [];
const EMPTY_RECORD: Record<string, boolean> = {};
const EMPTY_SIZING: Record<string, number> = {};
const EMPTY_EXPANDED: Set<string> = new Set();

const ARIA_SORT: Record<string, 'ascending' | 'descending' | 'none'> = {
  asc: 'ascending',
  desc: 'descending',
  false: 'none',
};

/** TanStack hands changes over as either a value or a `(prev) => next`. */
function applyUpdater<V>(updater: unknown, current: V): V {
  return typeof updater === 'function' ? (updater as (previous: V) => V)(current) : (updater as V);
}

function renderCell<T>(
  definition: DataTableColumn<T> | undefined,
  row: T,
  index: number,
): ReactNode {
  if (!definition) return null;
  if (definition.cell) return definition.cell(row, index);
  const value = definition.value?.(row);
  // NULL MEANS UNOBSERVED. An unmeasured value renders as nothing rather than
  // as "null" or "0"; what "not observed" should look like is the caller's
  // decision, and a fallback that invents a zero is the exact mistake this
  // library exists to avoid.
  if (value === null || value === undefined) return null;
  return value instanceof Date ? value.toISOString() : String(value);
}

/** Cheap plain-text extraction for accessible names built from a header node. */
function textOf(node: ReactNode): string | undefined {
  return typeof node === 'string' ? node : typeof node === 'number' ? String(node) : undefined;
}
