import {
  forwardRef,
  useCallback,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useCopyAction } from '../_shared/useCopyAction';
import { findMatchRanges, splitByRanges, type MatchRange } from '../_shared/matches';
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  CopyIcon,
  CrossIcon,
} from '../_shared/icons';
import { SearchInput } from '../../components/SearchInput/SearchInput';
import {
  buildTree,
  scanForMatches,
  stringifyValue,
  type JsonNode,
  type JsonValueKind,
} from './tree';
import './JsonViewer.css';

// `onSelect` shadows the DOM text-selection event deliberately: selecting a
// NODE is what this component is for, and a caller needing the raw DOM event
// can attach it to a wrapper.
export interface JsonViewerProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onCopy' | 'children' | 'onSelect'> {
  /** Any value. Cycles, Maps, Sets, Dates and accessors are all handled. */
  data: unknown;

  /** Depth expanded on first render. Default 1. */
  initialDepth?: number;
  /** Children rendered per container before a "show more" row. Default 100. */
  windowSize?: number;
  /** Ceiling on nodes built from `data`. Default 20000. */
  maxNodes?: number;
  /** Depth past which containers stop expanding. Default 64. */
  maxDepth?: number;
  /** Longest rendered string. Copy is unaffected. Default 400. */
  maxStringLength?: number;
  /** Label for the root node and the prefix of every copied path. Default `'$'`. */
  rootKey?: string;

  expandedPaths?: readonly string[];
  defaultExpandedPaths?: readonly string[];
  onExpandedPathsChange?: (paths: string[]) => void;

  search?: string;
  defaultSearch?: string;
  onSearchChange?: (value: string) => void;
  caseSensitive?: boolean;
  /** Open the ancestors of every match while searching. Default true. */
  expandMatches?: boolean;

  toolbar?: boolean;
  /** Caps the body height and scrolls past it. */
  maxHeight?: number | string;

  onSelect?: (node: JsonNode) => void;
  onCopy?: (text: string, scope: 'path' | 'value') => void;

  /* -- Copy --------------------------------------------------------------- */
  label?: string;
  labelSearch?: string;
  searchPlaceholder?: string;
  /** Accessible name for the search field's clear button. */
  labelClearSearch?: string;
  labelCopyPath?: string;
  labelCopyValue?: string;
  labelCopied?: string;
  labelCopyFailed?: string;
  labelExpandAll?: string;
  labelCollapseAll?: string;
  /** Receives the number of remaining children. */
  labelShowMore?: (remaining: number) => string;
  labelTruncated?: string;
  /** Receives the number of matches. */
  labelMatchCount?: (count: number) => string;
}

interface FlatRow {
  node: JsonNode;
  level: number;
  posInSet: number;
  setSize: number;
  expandable: boolean;
  expanded: boolean;
  /** A "show more" affordance rather than a value. */
  more?: { parentPath: string; remaining: number; shown: number };
}

/** Depth ceiling for expand-all, so one click cannot mount 20k rows. */
const EXPAND_ALL_DEPTH = 8;

function isExpandable(node: JsonNode): boolean {
  return node.children !== null && node.children.length > 0;
}

/**
 * Identity for a rendered row.
 *
 * Deliberately NOT used as the DOM `id`: a path may legitimately contain
 * spaces and quotes (`$["my key"]`), and an IDREF containing a space silently
 * fails to resolve — which would break `aria-activedescendant` for exactly the
 * documents most in need of inspection. The DOM id is index-derived; this key
 * is the stable handle kept in state.
 */
function rowKey(row: FlatRow): string {
  // The trailing marker cannot collide with a real path: a property literally
  // named `more` produces `$.more`, never `$ more`.
  return row.more ? row.more.parentPath + ' more' : row.node.path;
}

/**
 * A collapsible tree over any JavaScript value.
 *
 * WHY IT DOES NOT HANG
 * --------------------
 * The tree is built once, ahead of rendering, with cycle detection based on the
 * ANCESTOR chain, a node budget and a depth ceiling — see `tree.ts`. Pointing
 * this at a live object graph with `parent` back-references is the normal case
 * in a debug pane, not an edge case, and it must not be able to lock the tab.
 * Accessors are shown as accessors and never invoked, because reading a getter
 * to render it can change the value you are inspecting.
 *
 * LARGE CONTAINERS
 * ----------------
 * Containers render `windowSize` children at a time behind a "show N more" row.
 * Chunked rather than virtualized: the depth of a tree row varies, chunking
 * keeps Ctrl+F working, and it makes the cost of looking at a 200k-element
 * array explicit rather than hidden behind a scrollbar.
 *
 * ACCESSIBILITY
 * -------------
 * A flat `role="tree"` with `aria-level` / `aria-posinset` / `aria-setsize` per
 * item, which is the shape that survives windowing — a nested `role="group"`
 * would have to lie about set size as soon as children are chunked. One tab
 * stop; arrow keys move, Right expands or descends, Left collapses or ascends,
 * Enter toggles, `p` copies the path and `c` copies the value. The active row is
 * published through `aria-activedescendant`.
 */
export const JsonViewer = forwardRef<HTMLDivElement, JsonViewerProps>(function JsonViewer(
  {
    data,
    initialDepth = 1,
    windowSize = 100,
    maxNodes,
    maxDepth,
    maxStringLength,
    rootKey = '$',
    expandedPaths,
    defaultExpandedPaths,
    onExpandedPathsChange,
    search,
    defaultSearch = '',
    onSearchChange,
    caseSensitive = false,
    expandMatches = true,
    toolbar = true,
    maxHeight = '24rem',
    onSelect,
    onCopy,
    label = 'Data',
    labelSearch = 'Search keys and values',
    searchPlaceholder = 'Search…',
    labelClearSearch = 'Clear search',
    labelCopyPath = 'Copy path',
    labelCopyValue = 'Copy value',
    labelCopied = 'Copied',
    labelCopyFailed = 'Copy failed',
    labelExpandAll = 'Expand',
    labelCollapseAll = 'Collapse',
    labelShowMore = (remaining) => `Show ${remaining} more`,
    labelTruncated = 'truncated',
    labelMatchCount = (count) => `${count} match${count === 1 ? '' : 'es'}`,
    className,
    style,
    ...rest
  },
  ref,
) {
  const baseId = useId();
  const treeRef = useRef<HTMLDivElement | null>(null);

  const [searchValue, setSearchValue] = useControllableState<string>({
    value: search,
    defaultValue: defaultSearch,
    onChange: onSearchChange,
  });

  const root = useMemo(
    () =>
      buildTree(data, {
        rootKey,
        ...(maxDepth !== undefined ? { maxDepth } : {}),
        ...(maxNodes !== undefined ? { maxNodes } : {}),
        ...(maxStringLength !== undefined ? { maxStringLength } : {}),
      }),
    [data, rootKey, maxDepth, maxNodes, maxStringLength],
  );

  /** Paths opened by default, computed once per tree. */
  const initialExpanded = useMemo(() => {
    const out: string[] = [];
    const walk = (node: JsonNode) => {
      if (node.depth >= initialDepth || !node.children) return;
      out.push(node.path);
      for (let i = 0; i < node.children.length; i += 1) {
        const child = node.children[i];
        if (child) walk(child);
      }
    };
    walk(root);
    return out;
  }, [root, initialDepth]);

  const [expandedValue, setExpandedValue] = useControllableState<readonly string[]>({
    value: expandedPaths,
    defaultValue: defaultExpandedPaths ?? initialExpanded,
    onChange: onExpandedPathsChange ? (next) => onExpandedPathsChange([...next]) : undefined,
  });

  const [shownCounts, setShownCounts] = useState<Record<string, number>>({});
  const [activePath, setActivePath] = useState<string | null>(null);

  const pathCopy = useCopyAction();
  const valueCopy = useCopyAction();

  /* -- Search -------------------------------------------------------------- */

  const query = searchValue.trim();
  const needle = caseSensitive ? query : query.toLowerCase();

  const scan = useMemo(() => {
    if (!query) return null;
    return scanForMatches(root, (node) => {
      const key = node.key ?? '';
      const haystackKey = caseSensitive ? key : key.toLowerCase();
      if (haystackKey.includes(needle)) return true;
      const text = caseSensitive ? node.text : node.text.toLowerCase();
      return text.length > 0 && text.includes(needle);
    });
  }, [root, query, needle, caseSensitive]);

  const expandedSet = useMemo(() => {
    const set = new Set(expandedValue);
    if (scan && expandMatches) for (const path of scan.expand) set.add(path);
    return set;
  }, [expandedValue, scan, expandMatches]);

  /* -- Flattening ----------------------------------------------------------- */

  const rows = useMemo(() => {
    const out: FlatRow[] = [];

    const walk = (node: JsonNode, level: number, posInSet: number, setSize: number) => {
      const expandable = isExpandable(node);
      const expanded = expandable && expandedSet.has(node.path);
      out.push({ node, level, posInSet, setSize, expandable, expanded });

      if (!expanded || !node.children) return;

      const total = node.children.length;
      const shown = Math.min(total, shownCounts[node.path] ?? windowSize);
      for (let i = 0; i < shown; i += 1) {
        const child = node.children[i];
        if (child) walk(child, level + 1, i + 1, total);
      }
      if (shown < total) {
        out.push({
          node,
          level: level + 1,
          posInSet: shown + 1,
          setSize: total,
          expandable: false,
          expanded: false,
          more: { parentPath: node.path, remaining: total - shown, shown },
        });
      }
    };

    walk(root, 1, 1, 1);
    return out;
  }, [root, expandedSet, shownCounts, windowSize]);

  const indexByKey = useMemo(() => {
    const map = new Map<string, number>();
    for (let i = 0; i < rows.length; i += 1) {
      const row = rows[i];
      if (row) map.set(rowKey(row), i);
    }
    return map;
  }, [rows]);

  const activeIndex = activePath !== null ? (indexByKey.get(activePath) ?? -1) : -1;

  /* -- Mutations ------------------------------------------------------------ */

  const setExpanded = useCallback(
    (path: string, next: boolean) => {
      const has = expandedValue.includes(path);
      if (next === has) return;
      setExpandedValue(next ? [...expandedValue, path] : expandedValue.filter((p) => p !== path));
    },
    [expandedValue, setExpandedValue],
  );

  const expandAll = useCallback(() => {
    const out: string[] = [];
    const walk = (node: JsonNode) => {
      if (!node.children || node.children.length === 0) return;
      if (node.depth > EXPAND_ALL_DEPTH) return;
      out.push(node.path);
      for (let i = 0; i < node.children.length; i += 1) {
        const child = node.children[i];
        if (child) walk(child);
      }
    };
    walk(root);
    setExpandedValue(out);
  }, [root, setExpandedValue]);

  const showMore = useCallback(
    (parentPath: string, shown: number) => {
      setShownCounts((prev) => ({ ...prev, [parentPath]: shown + windowSize }));
    },
    [windowSize],
  );

  const copyPath = useCallback(
    (node: JsonNode) => {
      pathCopy.copy(node.path);
      onCopy?.(node.path, 'path');
    },
    [pathCopy, onCopy],
  );

  const copyValue = useCallback(
    (node: JsonNode) => {
      const text =
        node.children === null && node.kind === 'string'
          ? String(node.value)
          : stringifyValue(node.value);
      valueCopy.copy(text);
      onCopy?.(text, 'value');
    },
    [valueCopy, onCopy],
  );

  /* -- Keyboard ------------------------------------------------------------- */

  const focusRow = useCallback(
    (index: number) => {
      const clamped = Math.max(0, Math.min(index, rows.length - 1));
      const row = rows[clamped];
      if (!row) return;
      setActivePath(rowKey(row));
      const element = treeRef.current?.querySelector<HTMLElement>(
        `[data-row-index="${clamped}"]`,
      );
      element?.scrollIntoView({ block: 'nearest' });
    },
    [rows],
  );

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (rows.length === 0) return;
      const current = activeIndex >= 0 ? activeIndex : -1;
      const row = current >= 0 ? rows[current] : undefined;
      const modified = event.ctrlKey || event.metaKey || event.altKey;

      switch (event.key) {
        case 'ArrowDown':
          event.preventDefault();
          focusRow(current + 1);
          break;
        case 'ArrowUp':
          event.preventDefault();
          focusRow(current <= 0 ? 0 : current - 1);
          break;
        case 'Home':
          event.preventDefault();
          focusRow(0);
          break;
        case 'End':
          event.preventDefault();
          focusRow(rows.length - 1);
          break;
        case 'ArrowRight':
          if (!row) break;
          event.preventDefault();
          if (row.expandable && !row.expanded) setExpanded(row.node.path, true);
          else if (row.expandable) focusRow(current + 1);
          break;
        case 'ArrowLeft': {
          if (!row) break;
          event.preventDefault();
          if (row.expandable && row.expanded) {
            setExpanded(row.node.path, false);
            break;
          }
          // Walk back to the first shallower row — the parent.
          for (let i = current - 1; i >= 0; i -= 1) {
            const candidate = rows[i];
            if (candidate && candidate.level < row.level) {
              focusRow(i);
              break;
            }
          }
          break;
        }
        case 'Enter':
        case ' ': {
          if (!row) break;
          event.preventDefault();
          if (row.more) showMore(row.more.parentPath, row.more.shown);
          else if (row.expandable) setExpanded(row.node.path, !row.expanded);
          if (!row.more) onSelect?.(row.node);
          break;
        }
        case 'c':
        case 'C':
          if (modified || !row || row.more) break;
          event.preventDefault();
          copyValue(row.node);
          break;
        case 'p':
        case 'P':
          if (modified || !row || row.more) break;
          event.preventDefault();
          copyPath(row.node);
          break;
        case 'Escape':
          if (searchValue) {
            event.preventDefault();
            setSearchValue('');
          }
          break;
        default:
          break;
      }
    },
    [
      rows,
      activeIndex,
      focusRow,
      setExpanded,
      showMore,
      onSelect,
      copyValue,
      copyPath,
      searchValue,
      setSearchValue,
    ],
  );

  /* -- Highlighting --------------------------------------------------------- */

  const highlight = useCallback(
    (text: string): ReactNode => {
      if (!query || !text) return text;
      const ranges: MatchRange[] = findMatchRanges(text, query, caseSensitive);
      if (ranges.length === 0) return text;
      const pieces = splitByRanges(0, text.length, ranges);
      return pieces.map((piece, i) =>
        piece.match ? (
          <mark key={i} className="stratum-json__match">
            {text.slice(piece.start, piece.end)}
          </mark>
        ) : (
          text.slice(piece.start, piece.end)
        ),
      );
    },
    [query, caseSensitive],
  );

  /* -- Render --------------------------------------------------------------- */

  const bodyStyle = {
    maxHeight: typeof maxHeight === 'number' ? `${maxHeight}px` : maxHeight,
  } as CSSProperties;

  const renderRow = (row: FlatRow, index: number): ReactNode => {
    const rowStyle = { '--_level': row.level } as CSSProperties;

    const key = rowKey(row);
    const id = `${baseId}-r${index}`;
    const isActive = activePath === key;

    if (row.more) {
      const more = row.more;
      return (
        <div
          key={key}
          id={id}
          data-row-index={index}
          role="treeitem"
          aria-level={row.level}
          className="stratum-json__row stratum-json__row--more"
          data-active={isActive || undefined}
          style={rowStyle}
          onClick={() => {
            setActivePath(key);
            showMore(more.parentPath, more.shown);
          }}
        >
          <span className="stratum-json__more">{labelShowMore(more.remaining)}</span>
        </div>
      );
    }

    const node = row.node;
    const isMatch = scan?.matches.has(node.path) ?? false;
    const isLeaf = node.children === null;

    return (
      <div
        key={key}
        id={id}
        data-row-index={index}
        role="treeitem"
        aria-level={row.level}
        aria-posinset={row.posInSet}
        aria-setsize={row.setSize}
        aria-expanded={row.expandable ? row.expanded : undefined}
        aria-selected={isActive || undefined}
        className="stratum-json__row"
        data-kind={node.kind}
        data-active={isActive || undefined}
        data-match={isMatch || undefined}
        style={rowStyle}
        onClick={() => {
          setActivePath(node.path);
          if (row.expandable) setExpanded(node.path, !row.expanded);
          onSelect?.(node);
        }}
      >
        <span className="stratum-json__twisty" aria-hidden="true">
          {row.expandable ? row.expanded ? <ChevronDownIcon /> : <ChevronRightIcon /> : null}
        </span>

        {node.key !== null && (
          <>
            <span className="stratum-json__key" data-key-kind={node.keyKind}>
              {highlight(node.key)}
            </span>
            <span className="stratum-json__punct" aria-hidden="true">
              :
            </span>
          </>
        )}

        {isLeaf ? (
          <span className="stratum-json__value" data-kind={node.kind}>
            {highlight(node.text)}
          </span>
        ) : (
          <span className="stratum-json__summary" data-kind={node.kind}>
            {node.summary}
            {node.truncated && <span className="stratum-json__truncated"> {labelTruncated}</span>}
          </span>
        )}

        <span className="stratum-json__row-actions">
          <button
            type="button"
            className="stratum-json__row-action"
            aria-label={labelCopyPath}
            tabIndex={-1}
            onClick={(event) => {
              event.stopPropagation();
              setActivePath(node.path);
              copyPath(node);
            }}
          >
            <span aria-hidden="true">{rootKey}</span>
          </button>
          <button
            type="button"
            className="stratum-json__row-action"
            aria-label={labelCopyValue}
            tabIndex={-1}
            onClick={(event) => {
              event.stopPropagation();
              setActivePath(node.path);
              copyValue(node);
            }}
          >
            <CopyIcon />
          </button>
        </span>
      </div>
    );
  };

  const copyStatus = valueCopy.status !== 'idle' ? valueCopy.status : pathCopy.status;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="json-viewer"
      className={clsx('stratum-json', className)}
      style={style}
    >
      {toolbar && (
        <div className="stratum-json__toolbar">
          {/* The framework's own search field rather than a bare
              `<input type="search">`, which leaks WebKit's `searchfield`
              appearance and the engine's grey `::-webkit-search-cancel-button`
              into the toolbar. SearchInput suppresses both and supplies the
              system's clear control, focus ring and forced-colors treatment.
              `onSearch` is deliberately unused so the scan still runs from
              `onChange` on every keystroke — no debounce is introduced. */}
          <SearchInput
            className="stratum-json__search"
            size="sm"
            variant="subtle"
            fullWidth
            value={searchValue}
            onChange={(event) => setSearchValue(event.target.value)}
            aria-label={labelSearch}
            placeholder={searchPlaceholder}
            clearLabel={labelClearSearch}
          />

          {scan && (
            <span className="stratum-json__count" role="status">
              {labelMatchCount(scan.matches.size)}
            </span>
          )}

          <div className="stratum-json__toolbar-actions">
            <button type="button" className="stratum-json__control" onClick={expandAll}>
              {labelExpandAll}
            </button>
            <button
              type="button"
              className="stratum-json__control"
              onClick={() => setExpandedValue([])}
            >
              {labelCollapseAll}
            </button>
            {copyStatus !== 'idle' && (
              <span className="stratum-json__copy-state" data-copy-state={copyStatus}>
                {copyStatus === 'copied' ? <CheckIcon /> : <CrossIcon />}
                <span>{copyStatus === 'copied' ? labelCopied : labelCopyFailed}</span>
              </span>
            )}
          </div>
        </div>
      )}

      <div className="stratum-json__body" style={bodyStyle}>
        <div
          ref={treeRef}
          role="tree"
          aria-label={label}
          aria-activedescendant={activeIndex >= 0 ? `${baseId}-r${activeIndex}` : undefined}
          tabIndex={0}
          className="stratum-json__tree stratum-focus-inset"
          onKeyDown={handleKeyDown}
        >
          {rows.map(renderRow)}
        </div>
      </div>
    </div>
  );
});

export type { JsonNode, JsonValueKind };
