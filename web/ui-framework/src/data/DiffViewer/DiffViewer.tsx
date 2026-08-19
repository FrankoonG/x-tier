import {
  forwardRef,
  useCallback,
  useId,
  useMemo,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { ExpandIcon, SplitIcon, UnifiedIcon, WrapIcon } from '../_shared/icons';
import {
  buildDiff,
  toSplitRows,
  toUnifiedRows,
  type DiffLine,
  type DiffResult,
  type DiffSplitRow,
  type DiffUnifiedRow,
  type WordSegment,
} from './diff';
import './DiffViewer.css';

export type DiffView = 'unified' | 'split';

export interface DiffViewerProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  before: string;
  after: string;

  view?: DiffView;
  defaultView?: DiffView;
  onViewChange?: (view: DiffView) => void;

  /** Unchanged lines kept either side of a change. Default 3. */
  context?: number;
  /** Collapse long unchanged runs. Default true. */
  collapseUnchanged?: boolean;
  /** Minimum hidden lines before a run is worth collapsing. Default 2. */
  collapseThreshold?: number;

  /** Indices of equal blocks the reader has expanded. */
  expandedRegions?: readonly number[];
  defaultExpandedRegions?: readonly number[];
  onExpandedRegionsChange?: (regions: number[]) => void;

  /** Word-level highlighting inside paired changed lines. Default true. */
  wordDiff?: boolean;
  /** Token overlap below which two paired lines are treated as unrelated. */
  similarityThreshold?: number;
  /** Bound on the Myers search depth. See `diff.ts`. */
  maxEditDistance?: number;

  wrap?: boolean;
  defaultWrap?: boolean;
  onWrapChange?: (wrap: boolean) => void;

  showLineNumbers?: boolean;
  header?: boolean;
  /** Caps the body height and scrolls past it. */
  maxHeight?: number | string;

  /* -- Copy --------------------------------------------------------------- */
  label?: string;
  labelBefore?: string;
  labelAfter?: string;
  labelUnified?: string;
  labelSplit?: string;
  labelWrap?: string;
  labelExpandAll?: string;
  labelCollapseAll?: string;
  /** Receives the number of hidden lines. */
  labelExpand?: (hidden: number) => string;
  labelAdditions?: (count: number) => string;
  labelDeletions?: (count: number) => string;
  labelIdentical?: string;
  labelTruncated?: string;
  /** Announced prefixes; never rendered visually. */
  rowLabels?: { added?: string; removed?: string; unchanged?: string; absent?: string };
  columnLabels?: { beforeLine?: string; afterLine?: string; marker?: string; content?: string };
}

const DEFAULT_ROW_LABELS = {
  added: 'added',
  removed: 'removed',
  unchanged: 'unchanged',
  absent: 'no line',
} as const;

/** Renders a changed line's word segments, or its plain text. */
function renderLineContent(line: DiffLine, side: 'before' | 'after'): ReactNode {
  // An empty line is real content, not an absence of it. The row keeps its
  // height from CSS rather than from a placeholder glyph, so nothing that is
  // not in the file ever reaches the clipboard.
  if (!line.segments) return line.text;
  const nodes: ReactNode[] = [];
  for (let i = 0; i < line.segments.length; i += 1) {
    const segment: WordSegment | undefined = line.segments[i];
    if (!segment) continue;
    if (!segment.changed) {
      nodes.push(segment.text);
      continue;
    }
    nodes.push(
      <mark key={i} className="stratum-diff__word" data-side={side}>
        {segment.text}
      </mark>,
    );
  }
  return nodes;
}

/**
 * A unified or split diff of two texts.
 *
 * COLOUR IS NOT THE CHANNEL
 * -------------------------
 * Roughly one man in twelve cannot reliably separate the red and green a diff
 * conventionally uses, and under `forced-colors` the fills are discarded for
 * everyone. So the `+` and `-` gutter markers are not decoration — they are the
 * primary channel, present in every view, and each row additionally carries a
 * spoken "added"/"removed"/"unchanged" for assistive technology. The tints are
 * the redundant channel, not the other way round.
 *
 * ABSENT IS NOT EMPTY
 * -------------------
 * In split view, a change that deletes three lines and adds one leaves two
 * cells on the right with NO line in them. Those render as explicit filler —
 * hatched, and announced as "no line" — never as an empty line, because an
 * empty line is a real thing a file can contain and conflating the two makes
 * the line numbering look wrong.
 *
 * NOT VIRTUALIZED, DELIBERATELY
 * -----------------------------
 * Unchanged regions collapse to a `context` window, which bounds the DOM to
 * roughly the size of the change rather than the size of the file — a 20,000
 * line config with four edits renders about 30 rows. Virtualizing on top of
 * that would break Ctrl+F, which is the thing people actually do in a diff.
 * A diff whose changes genuinely number in the thousands is a file replacement,
 * and is better served by `collapseUnchanged` plus a smaller `context`.
 */
export const DiffViewer = forwardRef<HTMLDivElement, DiffViewerProps>(function DiffViewer(
  {
    before,
    after,
    view,
    defaultView = 'unified',
    onViewChange,
    context = 3,
    collapseUnchanged = true,
    collapseThreshold = 2,
    expandedRegions,
    defaultExpandedRegions,
    onExpandedRegionsChange,
    wordDiff = true,
    similarityThreshold,
    maxEditDistance,
    wrap,
    defaultWrap = false,
    onWrapChange,
    showLineNumbers = true,
    header = true,
    maxHeight = '32rem',
    label = 'Difference',
    labelBefore = 'Before',
    labelAfter = 'After',
    labelUnified = 'Unified view',
    labelSplit = 'Split view',
    labelWrap = 'Wrap lines',
    labelExpandAll = 'Expand all',
    labelCollapseAll = 'Collapse unchanged',
    labelExpand = (hidden) => `Expand ${hidden} unchanged line${hidden === 1 ? '' : 's'}`,
    labelAdditions = (count) => `${count} addition${count === 1 ? '' : 's'}`,
    labelDeletions = (count) => `${count} deletion${count === 1 ? '' : 's'}`,
    labelIdentical = 'The two inputs are identical.',
    labelTruncated =
      'The two inputs differ too extensively to align line by line; the changed region is shown as a whole-block replacement.',
    rowLabels,
    columnLabels,
    className,
    style,
    ...rest
  },
  ref,
) {
  const baseId = useId();

  const [viewValue, setViewValue] = useControllableState<DiffView>({
    value: view,
    defaultValue: defaultView,
    onChange: onViewChange,
  });
  const [wrapValue, setWrapValue] = useControllableState<boolean>({
    value: wrap,
    defaultValue: defaultWrap,
    onChange: onWrapChange,
  });
  const [expandedValue, setExpandedValue] = useControllableState<readonly number[]>({
    value: expandedRegions,
    defaultValue: defaultExpandedRegions ?? [],
    onChange: onExpandedRegionsChange ? (next) => onExpandedRegionsChange([...next]) : undefined,
  });

  const rowText = { ...DEFAULT_ROW_LABELS, ...rowLabels };

  const diff: DiffResult = useMemo(
    () =>
      buildDiff(before, after, {
        wordDiff,
        ...(similarityThreshold !== undefined ? { similarityThreshold } : {}),
        ...(maxEditDistance !== undefined ? { maxEditDistance } : {}),
      }),
    [before, after, wordDiff, similarityThreshold, maxEditDistance],
  );

  const expandedSet = useMemo(() => new Set(expandedValue), [expandedValue]);

  const collapseOptions = useMemo(
    () => ({
      context,
      expanded: expandedSet,
      // `Infinity` disables collapsing without a second code path: no run can
      // ever hide that many lines.
      minCollapse: collapseUnchanged ? Math.max(1, collapseThreshold) : Number.POSITIVE_INFINITY,
    }),
    [context, expandedSet, collapseUnchanged, collapseThreshold],
  );

  const unifiedRows = useMemo(
    () => (viewValue === 'unified' ? toUnifiedRows(diff.blocks, collapseOptions) : []),
    [viewValue, diff.blocks, collapseOptions],
  );
  const splitRows = useMemo(
    () => (viewValue === 'split' ? toSplitRows(diff.blocks, collapseOptions) : []),
    [viewValue, diff.blocks, collapseOptions],
  );

  const collapsibleIndices = useMemo(
    () => diff.blocks.filter((block) => block.kind === 'equal').map((block) => block.index),
    [diff.blocks],
  );

  const allExpanded =
    collapsibleIndices.length > 0 && collapsibleIndices.every((i) => expandedSet.has(i));

  const expandRegion = useCallback(
    (index: number) => {
      if (expandedSet.has(index)) return;
      setExpandedValue([...expandedValue, index]);
    },
    [expandedSet, expandedValue, setExpandedValue],
  );

  const rowCount = viewValue === 'unified' ? unifiedRows.length : splitRows.length;

  /* -- Cells -------------------------------------------------------------- */

  // `rowheader` only where there is actually a number. An added line has no
  // before-number, and a row header with no text is both an axe violation and
  // a genuinely useless stop for a screen-reader user — the absence is already
  // announced by the row's "added"/"removed" marker.
  const lineNumberCell = (value: number | null, preferHeader: boolean) =>
    showLineNumbers ? (
      <span
        role={preferHeader && value !== null ? 'rowheader' : 'cell'}
        className="stratum-diff__num"
        data-absent={value === null || undefined}
      >
        <span aria-hidden="true">{value ?? ''}</span>
        {value !== null && <span className="stratum-visually-hidden">{value}</span>}
      </span>
    ) : null;

  const markerFor = (type: 'add' | 'delete' | 'equal') =>
    type === 'add' ? '+' : type === 'delete' ? '−' : ' ';

  const spokenFor = (type: 'add' | 'delete' | 'equal') =>
    type === 'add' ? rowText.added : type === 'delete' ? rowText.removed : rowText.unchanged;

  const renderCollapsedRow = (
    key: string,
    rowIndex: number,
    hidden: number,
    blockIndex: number,
    span: number,
  ) => (
    <div key={key} role="row" aria-rowindex={rowIndex} className="stratum-diff__row" data-type="collapsed">
      <span role="cell" className="stratum-diff__collapsed" style={{ gridColumn: `1 / span ${span}` }}>
        <button
          type="button"
          className="stratum-diff__expand"
          onClick={() => expandRegion(blockIndex)}
        >
          <ExpandIcon />
          <span>{labelExpand(hidden)}</span>
        </button>
      </span>
    </div>
  );

  /* -- Unified ------------------------------------------------------------ */

  const renderUnified = (row: DiffUnifiedRow, rowIndex: number) => {
    if (row.type === 'collapsed') {
      return renderCollapsedRow(
        row.key,
        rowIndex,
        row.hidden ?? 0,
        row.blockIndex ?? 0,
        (showLineNumbers ? 2 : 0) + 2,
      );
    }
    const line = row.line;
    if (!line) return null;
    const type = row.type === 'add' ? 'add' : row.type === 'delete' ? 'delete' : 'equal';

    return (
      <div
        key={row.key}
        role="row"
        aria-rowindex={rowIndex}
        className="stratum-diff__row"
        data-type={type}
      >
        {lineNumberCell(line.beforeNumber, true)}
        {lineNumberCell(line.afterNumber, false)}
        <span role="cell" className="stratum-diff__marker">
          <span aria-hidden="true">{markerFor(type)}</span>
          <span className="stratum-visually-hidden">{spokenFor(type)}</span>
        </span>
        <span role="cell" className="stratum-diff__content">
          {renderLineContent(line, type === 'add' ? 'after' : 'before')}
        </span>
      </div>
    );
  };

  /* -- Split -------------------------------------------------------------- */

  const renderSplitSide = (line: DiffLine | null, side: 'before' | 'after', changed: boolean) => {
    if (!line) {
      return (
        <>
          {showLineNumbers && (
            <span role="cell" className="stratum-diff__num" data-absent="true">
              <span aria-hidden="true" />
            </span>
          )}
          <span role="cell" className="stratum-diff__content" data-absent="true">
            <span className="stratum-visually-hidden">{rowText.absent}</span>
          </span>
        </>
      );
    }
    const type = changed ? (side === 'before' ? 'delete' : 'add') : 'equal';
    const number = side === 'before' ? line.beforeNumber : line.afterNumber;
    return (
      <>
        {showLineNumbers && lineNumberCell(number, side === 'before')}
        <span role="cell" className="stratum-diff__content" data-type={type}>
          <span className="stratum-diff__marker" aria-hidden="true">
            {markerFor(type)}
          </span>
          <span className="stratum-visually-hidden">{spokenFor(type)} </span>
          {renderLineContent(line, side)}
        </span>
      </>
    );
  };

  const renderSplit = (row: DiffSplitRow, rowIndex: number) => {
    if (row.type === 'collapsed') {
      return renderCollapsedRow(
        row.key,
        rowIndex,
        row.hidden ?? 0,
        row.blockIndex ?? 0,
        showLineNumbers ? 4 : 2,
      );
    }
    const changed = row.type !== 'equal';
    return (
      <div
        key={row.key}
        role="row"
        aria-rowindex={rowIndex}
        className="stratum-diff__row"
        data-type={row.type}
      >
        {renderSplitSide(row.before, 'before', changed)}
        {renderSplitSide(row.after, 'after', changed)}
      </div>
    );
  };

  /* -- Render -------------------------------------------------------------- */

  // Unified is [before no., after no., marker, content]; split is
  // [before no., before content, after no., after content]. Both drop to two
  // columns without line numbers.
  const columnCount = showLineNumbers ? 4 : 2;

  const bodyStyle = {
    maxHeight: typeof maxHeight === 'number' ? `${maxHeight}px` : maxHeight,
  } as CSSProperties;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="diff-viewer"
      data-view={viewValue}
      data-wrap={wrapValue || undefined}
      data-line-numbers={showLineNumbers || undefined}
      className={clsx('stratum-diff', className)}
      style={style}
    >
      {header && (
        <div className="stratum-diff__header">
          <div className="stratum-diff__titles">
            <span className="stratum-diff__title" data-side="before">
              {labelBefore}
            </span>
            <span className="stratum-diff__arrow" aria-hidden="true">
              →
            </span>
            <span className="stratum-diff__title" data-side="after">
              {labelAfter}
            </span>
          </div>

          <p className="stratum-diff__counts">
            <span className="stratum-diff__count" data-kind="add">
              <span aria-hidden="true">+{diff.additions}</span>
              <span className="stratum-visually-hidden">{labelAdditions(diff.additions)}</span>
            </span>
            <span className="stratum-diff__count" data-kind="del">
              <span aria-hidden="true">&minus;{diff.deletions}</span>
              <span className="stratum-visually-hidden">{labelDeletions(diff.deletions)}</span>
            </span>
          </p>

          <div className="stratum-diff__controls">
            <button
              type="button"
              className="stratum-diff__control"
              aria-pressed={viewValue === 'unified'}
              aria-label={labelUnified}
              title={labelUnified}
              onClick={() => setViewValue('unified')}
            >
              <UnifiedIcon />
            </button>
            <button
              type="button"
              className="stratum-diff__control"
              aria-pressed={viewValue === 'split'}
              aria-label={labelSplit}
              title={labelSplit}
              onClick={() => setViewValue('split')}
            >
              <SplitIcon />
            </button>
            <button
              type="button"
              className="stratum-diff__control"
              aria-pressed={wrapValue}
              aria-label={labelWrap}
              title={labelWrap}
              onClick={() => setWrapValue(!wrapValue)}
            >
              <WrapIcon />
            </button>
            {collapseUnchanged && collapsibleIndices.length > 0 && (
              <button
                type="button"
                className="stratum-diff__control stratum-diff__control--text"
                onClick={() => setExpandedValue(allExpanded ? [] : collapsibleIndices)}
              >
                {allExpanded ? labelCollapseAll : labelExpandAll}
              </button>
            )}
          </div>
        </div>
      )}

      {diff.truncated && (
        <p className="stratum-diff__notice" role="note">
          {labelTruncated}
        </p>
      )}

      {diff.identical ? (
        <p className="stratum-diff__identical">{labelIdentical}</p>
      ) : (
        <div className="stratum-diff__body" style={bodyStyle}>
          <div
            role="table"
            aria-label={label}
            aria-rowcount={rowCount + 1}
            aria-colcount={columnCount}
            className="stratum-diff__table"
          >
            <div role="rowgroup" className="stratum-visually-hidden">
              <div role="row" aria-rowindex={1}>
                {showLineNumbers && (
                  <span role="columnheader">{columnLabels?.beforeLine ?? 'Before line'}</span>
                )}
                {showLineNumbers && viewValue === 'unified' && (
                  <span role="columnheader">{columnLabels?.afterLine ?? 'After line'}</span>
                )}
                {viewValue === 'unified' && (
                  <span role="columnheader">{columnLabels?.marker ?? 'Change'}</span>
                )}
                <span role="columnheader">{columnLabels?.content ?? 'Content'}</span>
                {viewValue === 'split' && showLineNumbers && (
                  <span role="columnheader">{columnLabels?.afterLine ?? 'After line'}</span>
                )}
                {viewValue === 'split' && (
                  <span role="columnheader">{columnLabels?.content ?? 'Content'}</span>
                )}
              </div>
            </div>

            <div role="rowgroup" className="stratum-diff__rows" id={`${baseId}-rows`}>
              {viewValue === 'unified'
                ? unifiedRows.map((row, i) => renderUnified(row, i + 2))
                : splitRows.map((row, i) => renderSplit(row, i + 2))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
});
