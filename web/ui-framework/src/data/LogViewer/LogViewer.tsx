import {
  forwardRef,
  useCallback,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type UIEvent as ReactUIEvent,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { formatRelativeTime, formatTimestamp } from '../../network/format';
import { useCopyAction } from '../_shared/useCopyAction';
import {
  compileFilter,
  findLiteralRanges,
  findRegexRanges,
  splitByRanges,
  type MatchRange,
} from '../_shared/matches';
import {
  ArrowToBottomIcon,
  CheckIcon,
  CopyIcon,
  CrossIcon,
  FollowIcon,
  WrapIcon,
} from '../_shared/icons';
import { SearchInput } from '../../components/SearchInput/SearchInput';
import { needsAnsiParse, parseAnsi, stripAnsi, type AnsiSpan } from './ansi';
import './LogViewer.css';

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal';

export interface LogLine {
  /** Stable, unique identity. Used as the React key; never displayed. */
  id: string | number;
  /** Raw text. May contain ANSI SGR escape sequences. */
  text: string;
  /**
   * Severity. `undefined` means NOT OBSERVED, not "info" — and a line whose
   * level was never parsed is never removed by the level filter. Hiding what
   * you failed to classify is how an operator loses the one line that mattered.
   */
  level?: LogLevel;
  /** `null`/`undefined` renders as unobserved, never as the epoch. */
  timestamp?: Date | number | string | null;
  /** Optional emitter tag — subsystem, worker, container. */
  source?: string;
  /** Exact clipboard payload. Display text and diagnostic columns stay visible. */
  copyText?: string;
}

export interface LogViewerSummary {
  total: number;
  shown: number;
  following: boolean;
}

export interface LogViewerColumnLabels {
  /** Header for the leading per-row action column. */
  actions?: string;
  timestamp?: string;
  level?: string;
  message?: string;
}

export interface LogViewerProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onCopy' | 'children'> {
  lines: readonly LogLine[];

  /** Height of the scroll viewport. Default `'24rem'`. */
  height?: number | string;
  density?: 'compact' | 'comfortable';
  /**
   * Parse ANSI SGR sequences. When false they are stripped, never displayed.
   *
   * Colours chosen by a remote process cannot be held to a contrast guarantee —
   * a producer is free to emit a mid-grey on a light surface. The palette this
   * maps to is placed to clear AA on its own (see `tokens.semantic.css`), but a
   * 256-colour or truecolor escape is used verbatim. Turn this off where the
   * contrast of arbitrary log output has to be guaranteed.
   */
  ansi?: boolean;
  /** Renders the filter / level / wrap / follow / copy toolbar. Default true. */
  toolbar?: boolean;

  showTimestamp?: boolean;
  timestampFormat?: 'time' | 'datetime' | 'relative';
  locale?: string;
  timeZone?: string;

  showLevel?: boolean;
  /** Levels offered by the filter, in display order. */
  levelOptions?: readonly LogLevel[];
  enabledLevels?: readonly LogLevel[];
  defaultEnabledLevels?: readonly LogLevel[];
  onEnabledLevelsChange?: (levels: LogLevel[]) => void;

  filter?: string;
  defaultFilter?: string;
  onFilterChange?: (value: string) => void;
  filterMode?: 'text' | 'regex';
  caseSensitive?: boolean;

  wrap?: boolean;
  defaultWrap?: boolean;
  onWrapChange?: (wrap: boolean) => void;

  /** Pin to the newest line. Detaches automatically when the user scrolls up. */
  follow?: boolean;
  defaultFollow?: boolean;
  onFollowChange?: (follow: boolean) => void;

  /** Rows rendered beyond the viewport on each side. Default 8. */
  overscan?: number;

  onLineActivate?: (line: LogLine, index: number) => void;
  onCopy?: (text: string, scope: 'line' | 'all') => void;

  /**
   * Announce the status summary through a live region. Off by default: a log
   * that appends continuously would make a polite live region unusable. The
   * summary is always present in the DOM and reachable in browse mode.
   */
  announceSummary?: boolean;

  /* -- Copy. Every user-visible string is a prop with an English default. -- */
  label?: string;
  labelFilter?: string;
  filterPlaceholder?: string;
  /** Accessible name for the filter field's clear button. */
  labelClearFilter?: string;
  labelCopyLine?: string;
  labelCopyAll?: string;
  labelCopied?: string;
  labelCopyFailed?: string;
  labelWrap?: string;
  labelFollow?: string;
  labelJumpToLatest?: string;
  /** Receives the number of lines appended since the view detached. */
  labelNewLines?: (count: number) => string;
  labelLevelFilter?: string;
  labelInvalidPattern?: string;
  labelEmpty?: string;
  labelNoMatches?: string;
  labelUnobservedTime?: string;
  labelUnobservedLevel?: string;
  columnLabels?: LogViewerColumnLabels;
  levelLabels?: Partial<Record<LogLevel, string>>;
  summaryLabel?: (summary: LogViewerSummary) => string;
}

const ALL_LEVELS: readonly LogLevel[] = ['trace', 'debug', 'info', 'warn', 'error', 'fatal'];

const DEFAULT_LEVEL_LABELS: Record<LogLevel, string> = {
  trace: 'Trace',
  debug: 'Debug',
  info: 'Info',
  warn: 'Warn',
  error: 'Error',
  fatal: 'Fatal',
};

/** Characters in the width probe. More characters, better average advance. */
const PROBE_CHARS = 64;
const PROBE_TEXT = '0'.repeat(PROBE_CHARS);
/** Used before the first measurement lands, and where there is no layout. */
const FALLBACK_LINE_HEIGHT = 18;
const FALLBACK_CHAR_WIDTH = 7.2;
/** Distance from the bottom still counted as "at the bottom", in px. */
const TAIL_EPSILON = 4;
/** Trailing slack on the computed no-wrap content width, in px. */
const CONTENT_WIDTH_SLACK = 32;
/**
 * Bounds on the level column, in `ch` of content — the gutter padding is added
 * on top by the CSS. The floor is the stylesheet's own default, so the ordinary
 * syslog vocabulary keeps exactly the geometry it was cut for. The ceiling is
 * the point past which a level label would start eating the message column;
 * beyond it the cell ellipsises rather than truncating silently.
 */
const MIN_LEVEL_GUTTER_CH = 5.5;
const MAX_LEVEL_GUTTER_CH = 20;

interface PreparedLine {
  line: LogLine;
  /** Index in the source array; stable across filtering. */
  index: number;
  /** Text with ANSI sequences and control characters removed. */
  plain: string;
  /** Lowercased `plain`, computed once per line object. */
  lower: string;
}

/**
 * Per-line derived text, keyed on the caller's line object.
 *
 * A tailing log hands us a new array on every append. Recomputing `stripAnsi` +
 * `toLowerCase` across all 100k lines each time is the difference between a
 * smooth tail and a locked-up tab, so the work is cached against the line object
 * itself. A WeakMap means a line dropped from the buffer becomes collectable
 * immediately, with no eviction policy to get wrong.
 */
const derivedCache = new WeakMap<LogLine, { plain: string; lower: string }>();

interface Metrics {
  charWidth: number;
  lineHeight: number;
  /** Width available to the message column, in px. */
  textWidth: number;
  /** Width consumed by the timestamp and level columns, in px. */
  gutterWidth: number;
  viewportHeight: number;
}

const INITIAL_METRICS: Metrics = {
  charWidth: 0,
  lineHeight: 0,
  textWidth: 0,
  gutterWidth: 0,
  viewportHeight: 0,
};

/** Largest row index whose top offset is at or above `y`. */
function findOffsetIndex(offsets: Float64Array, y: number): number {
  let lo = 0;
  let hi = Math.max(0, offsets.length - 2);
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if ((offsets[mid] ?? 0) <= y) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

/**
 * A virtualized log pane.
 *
 * VIRTUALIZATION
 * --------------
 * Bespoke, deliberately: this component must work in a project that installed
 * nothing but React. Two height models:
 *
 *   wrap off  every row is exactly one text line, so offsets are arithmetic and
 *             exact. Horizontal scroll width comes from the longest line in the
 *             data, not from whichever rows happen to be mounted — that detail
 *             is what stops the horizontal scrollbar jittering as you scroll
 *             vertically, which is the classic tell of a naive virtualizer.
 *
 *   wrap on   height is estimated as `ceil(chars / charsPerLine) * lineHeight`
 *             from a measured average character advance. Exact for the
 *             monospace face this ships with; an approximation for text with a
 *             non-uniform advance (CJK, emoji, combining marks). The estimate is
 *             *stable* — it never changes as you scroll — so the scrollbar stays
 *             still even where a row's real height differs by a line. Prefix
 *             sums are rebuilt only when the data, the font metrics or the
 *             viewport width change; never per frame.
 *
 * Nothing outside the rendered window is ever touched: no ANSI parse, no DOM.
 * Per-keystroke filtering is one `indexOf` against precomputed lowercase text.
 * That is what keeps 100k lines interactive.
 *
 * FOLLOW-TAIL
 * -----------
 * Following pins the viewport to the bottom in a layout effect, so appends never
 * flash. Scrolling away detaches; scrolling back to the bottom re-attaches. A
 * detached view NEVER jumps — an operator reading line 40,000 must not be yanked
 * to the end because a heartbeat logged. The floating affordance re-attaches
 * explicitly and reports how many lines arrived while detached.
 *
 * ACCESSIBILITY
 * -------------
 * The viewport is a `role="grid"` with `aria-rowcount` set to the filtered line
 * count plus its header row, and every rendered row carries its true
 * `aria-rowindex`. This is the one honest way to expose virtualization: the DOM
 * holds ~60 rows while assistive technology is told the real total and the real
 * position — instead of the tree claiming 100,000 rows exist, which stalls every
 * screen reader, or claiming only 60 do, which is a lie.
 *
 * Keyboard: the grid is a single tab stop. Arrow keys, Page Up/Down and Home/End
 * move an active row published through `aria-activedescendant`; `c` copies it;
 * `Enter` activates it; `Escape` clears the filter. The active row is forced
 * into the rendered window, so the `aria-activedescendant` target always exists.
 *
 * Text selection is native and works across the rendered window: rows stay in
 * document order and are block-level, so a selection copies with its line breaks
 * intact. Selection cannot extend past the mounted window — an inherent property
 * of virtualization, and the reason `copy all` exists.
 */
export const LogViewer = forwardRef<HTMLDivElement, LogViewerProps>(function LogViewer(
  {
    lines,
    height = '24rem',
    density = 'compact',
    ansi = true,
    toolbar = true,
    showTimestamp = true,
    timestampFormat = 'time',
    locale,
    timeZone,
    showLevel = true,
    levelOptions = ALL_LEVELS,
    enabledLevels,
    defaultEnabledLevels,
    onEnabledLevelsChange,
    filter,
    defaultFilter = '',
    onFilterChange,
    filterMode = 'text',
    caseSensitive = false,
    wrap,
    defaultWrap = false,
    onWrapChange,
    follow,
    defaultFollow = true,
    onFollowChange,
    overscan = 8,
    onLineActivate,
    onCopy,
    announceSummary = false,
    label = 'Log',
    labelFilter = 'Filter lines',
    filterPlaceholder = 'Filter…',
    labelClearFilter = 'Clear filter',
    labelCopyLine = 'Copy line',
    labelCopyAll = 'Copy all',
    labelCopied = 'Copied',
    labelCopyFailed = 'Copy failed',
    labelWrap = 'Wrap lines',
    labelFollow = 'Follow new lines',
    labelJumpToLatest = 'Jump to latest',
    labelNewLines = (n) => `${n} new line${n === 1 ? '' : 's'}`,
    labelLevelFilter = 'Levels',
    labelInvalidPattern = 'Incomplete pattern',
    labelEmpty = 'No log lines.',
    labelNoMatches = 'No lines match the current filter.',
    labelUnobservedTime = 'time not observed',
    labelUnobservedLevel = 'level not observed',
    columnLabels,
    levelLabels,
    summaryLabel = ({ total, shown, following }) =>
      `${shown} of ${total} lines shown, ${following ? 'following new lines' : 'scrolling detached'}.`,
    className,
    style,
    ...rest
  },
  ref,
) {
  const baseId = useId();

  const [filterValue, setFilterValue] = useControllableState<string>({
    value: filter,
    defaultValue: defaultFilter,
    onChange: onFilterChange,
  });
  const [wrapValue, setWrapValue] = useControllableState<boolean>({
    value: wrap,
    defaultValue: defaultWrap,
    onChange: onWrapChange,
  });
  const [followValue, setFollowValue] = useControllableState<boolean>({
    value: follow,
    defaultValue: defaultFollow,
    onChange: onFollowChange,
  });
  const [levelsValue, setLevelsValue] = useControllableState<readonly LogLevel[]>({
    value: enabledLevels,
    defaultValue: defaultEnabledLevels ?? levelOptions,
    onChange: onEnabledLevelsChange ? (next) => onEnabledLevelsChange([...next]) : undefined,
  });

  const viewportRef = useRef<HTMLDivElement | null>(null);
  const probeRef = useRef<HTMLSpanElement | null>(null);
  const metricsTextRef = useRef<HTMLSpanElement | null>(null);

  const [metrics, setMetrics] = useState<Metrics>(INITIAL_METRICS);
  const [scrollTop, setScrollTop] = useState(0);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [detachedAt, setDetachedAt] = useState<number | null>(null);

  const lineCopy = useCopyAction();
  const allCopy = useCopyAction();

  /* -- Derived text ------------------------------------------------------- */

  const prepared = useMemo(() => {
    const rows: PreparedLine[] = [];
    // Which levels actually occur, for the gutter measurement below. Collected
    // here rather than in its own pass because this is the one loop that already
    // walks every line; a level is one interned string, so the `add` costs
    // nothing against the work already being done per row.
    const levels = new Set<LogLevel>();
    let longest = 0;
    for (let i = 0; i < lines.length; i += 1) {
      const line = lines[i];
      if (!line) continue;
      if (line.level) levels.add(line.level);
      let entry = derivedCache.get(line);
      if (!entry) {
        const plain = needsAnsiParse(line.text) ? stripAnsi(line.text) : line.text;
        entry = { plain, lower: plain.toLowerCase() };
        derivedCache.set(line, entry);
      }
      // Width is measured in characters INCLUDING the source tag, which is
      // rendered inside the message cell. Leaving it out lets a sourced row
      // overflow the spacer, and an overflowing row is exactly what makes the
      // horizontal scrollbar twitch as you scroll vertically — the defect the
      // content-width calculation exists to prevent.
      const width = entry.plain.length + (line.source ? line.source.length + 2 : 0);
      if (width > longest) longest = width;
      rows.push({ line, index: i, plain: entry.plain, lower: entry.lower });
    }
    return { rows, longest, levels };
  }, [lines]);

  const enabledSet = useMemo(() => new Set(levelsValue), [levelsValue]);
  const levelFilterActive = useMemo(
    () => levelOptions.some((option) => !enabledSet.has(option)),
    [levelOptions, enabledSet],
  );

  const query = filterValue.trim();
  const { regex: filterRegex, invalid: filterInvalid } = useMemo(
    () => compileFilter(query, filterMode, caseSensitive),
    [query, filterMode, caseSensitive],
  );

  /** Regex mode with a not-yet-valid pattern matches everything, rather than
   *  blanking the pane on every intermediate keystroke of `[a-`. */
  const textFilterActive = query.length > 0 && (filterMode === 'text' || filterRegex !== null);

  const visible = useMemo(() => {
    if (!textFilterActive && !levelFilterActive) return prepared.rows;

    const needle = caseSensitive ? query : query.toLowerCase();
    const out: PreparedLine[] = [];

    for (let i = 0; i < prepared.rows.length; i += 1) {
      const row = prepared.rows[i];
      if (!row) continue;

      // An unobserved level is never filtered out. See `LogLine.level`.
      const level = row.line.level;
      if (levelFilterActive && level !== undefined && !enabledSet.has(level)) continue;

      if (textFilterActive) {
        if (filterRegex) {
          filterRegex.lastIndex = 0;
          if (!filterRegex.test(row.plain)) continue;
        } else if ((caseSensitive ? row.plain : row.lower).indexOf(needle) === -1) {
          continue;
        }
      }

      out.push(row);
    }
    return out;
  }, [
    prepared.rows,
    query,
    textFilterActive,
    levelFilterActive,
    enabledSet,
    caseSensitive,
    filterRegex,
  ]);

  /* -- Measurement --------------------------------------------------------- */

  /**
   * Width of the level column, published as an inline `--_gutter-level`.
   *
   * A log viewer is handed level vocabularies it does not control. The
   * stylesheet's 5.5ch was cut for the syslog set — TRACE through FATAL, five
   * characters — but `levelLabels` lets a caller relabel those levels as
   * outcomes ("Succeeded", "Unreachable", "Outcome unknown"), which is 7-15
   * characters. A fixed column has no reflow to make that collision obvious: it
   * clipped the label straight onto the message. So the column is sized from the
   * words it was actually given rather than from an assumed set.
   *
   * The vocabulary is the union of the levels present in the data and the levels
   * the filter offers. Data alone would be enough to avoid clipping, but the
   * column would then jump the first time an unseen level arrived mid-tail,
   * reflowing the pane under someone who is reading it; folding in the declared
   * options settles the width on the first paint. `+ 0.5` is the same
   * letter-spacing slack the other gutters carry. With nothing measurable the
   * property is left unset and CSS supplies its own default.
   */
  const levelGutter = useMemo(() => {
    if (!showLevel) return undefined;
    let longest = 0;
    const measure = (level: LogLevel) => {
      const text = levelLabels?.[level] ?? DEFAULT_LEVEL_LABELS[level];
      if (text.length > longest) longest = text.length;
    };
    prepared.levels.forEach(measure);
    levelOptions.forEach(measure);
    if (longest === 0) return undefined;
    const ch = Math.min(Math.max(longest + 0.5, MIN_LEVEL_GUTTER_CH), MAX_LEVEL_GUTTER_CH);
    return `${ch}ch`;
  }, [showLevel, prepared.levels, levelOptions, levelLabels]);

  const lineHeight = metrics.lineHeight || FALLBACK_LINE_HEIGHT;
  const charWidth = metrics.charWidth || FALLBACK_CHAR_WIDTH;

  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    const probe = probeRef.current;
    const textCell = metricsTextRef.current;
    if (!viewport || !probe || !textCell) return;

    const read = () => {
      const probeRect = probe.getBoundingClientRect();
      const cellRect = textCell.getBoundingClientRect();
      const next: Metrics = {
        charWidth: probeRect.width > 0 ? probeRect.width / PROBE_CHARS : 0,
        lineHeight: probeRect.height,
        textWidth: cellRect.width,
        gutterWidth: Math.max(0, viewport.clientWidth - cellRect.width),
        viewportHeight: viewport.clientHeight,
      };
      setMetrics((prev) =>
        prev.charWidth === next.charWidth &&
        prev.lineHeight === next.lineHeight &&
        prev.textWidth === next.textWidth &&
        prev.gutterWidth === next.gutterWidth &&
        prev.viewportHeight === next.viewportHeight
          ? prev
          : next,
      );
    };

    read();
    if (typeof ResizeObserver === 'undefined') return;

    // The probe is observed as well as the viewport, so a monospace face that
    // loads late re-measures instead of leaving every offset permanently wrong.
    const observer = new ResizeObserver(read);
    observer.observe(viewport);
    observer.observe(probe);
    observer.observe(textCell);
    return () => observer.disconnect();
    // Every dependency here is something that moves the gutter/message split.
    // `levelGutter` is listed for the path with no ResizeObserver, where the
    // message cell shrinking under a wider level column would otherwise leave
    // `textWidth` — and so every wrapped row's height — measured against the
    // previous layout.
  }, [density, showTimestamp, showLevel, timestampFormat, levelGutter]);

  /* -- Layout -------------------------------------------------------------- */

  const layout = useMemo(() => {
    const count = visible.length;
    if (!wrapValue) {
      return { total: count * lineHeight, offsets: null as Float64Array | null };
    }
    const perLine = Math.max(1, Math.floor((metrics.textWidth || 320) / charWidth));
    const offsets = new Float64Array(count + 1);
    let acc = 0;
    for (let i = 0; i < count; i += 1) {
      offsets[i] = acc;
      const length = visible[i]?.plain.length ?? 0;
      acc += Math.max(1, Math.ceil(length / perLine)) * lineHeight;
    }
    offsets[count] = acc;
    return { total: acc, offsets };
  }, [visible, wrapValue, lineHeight, charWidth, metrics.textWidth]);

  const contentWidth = wrapValue
    ? null
    : Math.max(
        metrics.textWidth + metrics.gutterWidth,
        // Slack absorbs the message cell's trailing padding and the 2px rule a
        // warn/error/fatal row draws down its left edge. Overshooting only
        // lengthens the scroll range harmlessly; undershooting reintroduces the
        // jitter, so this errs generous.
        prepared.longest * charWidth + metrics.gutterWidth + CONTENT_WIDTH_SLACK,
      );

  const viewportHeight = metrics.viewportHeight;

  const renderWindow = useMemo(() => {
    const count = visible.length;
    if (count === 0) return { start: 0, end: 0 };

    let start: number;
    let end: number;
    if (layout.offsets) {
      start = findOffsetIndex(layout.offsets, scrollTop) - overscan;
      end = findOffsetIndex(layout.offsets, scrollTop + viewportHeight) + 1 + overscan;
    } else {
      start = Math.floor(scrollTop / lineHeight) - overscan;
      end = Math.ceil((scrollTop + viewportHeight) / lineHeight) + overscan;
    }

    start = Math.max(0, Math.min(start, count - 1));
    end = Math.max(start + 1, Math.min(end, count));

    // The active row must exist in the DOM for `aria-activedescendant` to
    // resolve. Keyboard movement scrolls it into view in the same commit, so
    // this only ever widens the window by a row or two.
    if (activeIndex !== null && activeIndex >= 0 && activeIndex < count) {
      if (activeIndex < start) start = activeIndex;
      if (activeIndex >= end) end = activeIndex + 1;
    }
    return { start, end };
  }, [visible.length, layout.offsets, scrollTop, viewportHeight, lineHeight, overscan, activeIndex]);

  const offsetOf = useCallback(
    (index: number) => (layout.offsets ? (layout.offsets[index] ?? 0) : index * lineHeight),
    [layout.offsets, lineHeight],
  );

  const heightOf = useCallback(
    (index: number) =>
      layout.offsets ? (layout.offsets[index + 1] ?? 0) - (layout.offsets[index] ?? 0) : lineHeight,
    [layout.offsets, lineHeight],
  );

  /* -- Follow tail --------------------------------------------------------- */

  const pinToBottom = useCallback(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const next = Math.max(0, viewport.scrollHeight - viewport.clientHeight);
    viewport.scrollTop = next;
    setScrollTop(next);
  }, []);

  useLayoutEffect(() => {
    if (!followValue) return;
    pinToBottom();
    // Keyed on `layout.total` rather than the line count: a wrapped line that
    // grows taller also needs the pin re-applied.
  }, [followValue, layout.total, pinToBottom]);

  const handleScroll = useCallback(
    (event: ReactUIEvent<HTMLDivElement>) => {
      const el = event.currentTarget;
      setScrollTop(el.scrollTop);
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= TAIL_EPSILON;

      if (followValue && !atBottom) {
        setFollowValue(false);
        setDetachedAt(visible.length);
      } else if (!followValue && atBottom) {
        setFollowValue(true);
        setDetachedAt(null);
      }
    },
    [followValue, setFollowValue, visible.length],
  );

  const jumpToLatest = useCallback(() => {
    setFollowValue(true);
    setDetachedAt(null);
    pinToBottom();
  }, [setFollowValue, pinToBottom]);

  const newSinceDetach = detachedAt === null ? 0 : Math.max(0, visible.length - detachedAt);

  /* -- Copy ---------------------------------------------------------------- */

  const composeLine = useCallback(
    (row: PreparedLine) => {
      if (row.line.copyText !== undefined) return row.line.copyText;
      const parts: string[] = [];
      if (showTimestamp && row.line.timestamp != null) {
        parts.push(formatTimestamp(row.line.timestamp, { precision: 'millisecond' }));
      }
      if (showLevel && row.line.level) parts.push(row.line.level.toUpperCase());
      if (row.line.source) parts.push(row.line.source);
      parts.push(row.plain);
      return parts.join(' ');
    },
    [showTimestamp, showLevel],
  );

  const copyLine = useCallback(
    (row: PreparedLine) => {
      const text = composeLine(row);
      lineCopy.copy(text);
      onCopy?.(text, 'line');
    },
    [composeLine, lineCopy, onCopy],
  );

  const copyAll = useCallback(() => {
    const parts: string[] = [];
    for (let i = 0; i < visible.length; i += 1) {
      const row = visible[i];
      if (row) parts.push(composeLine(row));
    }
    const text = parts.join('\n');
    allCopy.copy(text);
    onCopy?.(text, 'all');
  }, [visible, composeLine, allCopy, onCopy]);

  /* -- Keyboard ------------------------------------------------------------ */

  const scrollIndexIntoView = useCallback(
    (index: number) => {
      const viewport = viewportRef.current;
      if (!viewport) return;
      const top = offsetOf(index);
      const bottom = top + heightOf(index);
      const current = viewport.scrollTop;

      let next = current;
      if (top < current) next = top;
      else if (bottom > current + viewport.clientHeight) next = bottom - viewport.clientHeight;

      if (next !== current) {
        viewport.scrollTop = next;
        // Mirrored into state so the render window is correct in this same
        // commit rather than one scroll event later.
        setScrollTop(next);
      }
    },
    [offsetOf, heightOf],
  );

  const moveActive = useCallback(
    (next: number) => {
      const count = visible.length;
      if (count === 0) return;
      const clamped = Math.max(0, Math.min(next, count - 1));
      setActiveIndex(clamped);
      scrollIndexIntoView(clamped);
      // Navigating into history is an explicit statement that the operator is
      // reading, so it detaches the tail.
      if (followValue && clamped < count - 1) {
        setFollowValue(false);
        setDetachedAt(count);
      }
    },
    [visible.length, scrollIndexIntoView, followValue, setFollowValue],
  );

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      const count = visible.length;
      if (count === 0) return;
      const current = activeIndex ?? -1;
      const page = Math.max(1, Math.floor(viewportHeight / lineHeight) - 1);
      const modified = event.ctrlKey || event.metaKey || event.altKey;

      switch (event.key) {
        case 'ArrowDown':
          event.preventDefault();
          moveActive(current + 1);
          break;
        case 'ArrowUp':
          event.preventDefault();
          moveActive(current <= 0 ? 0 : current - 1);
          break;
        case 'PageDown':
          event.preventDefault();
          moveActive(current + page);
          break;
        case 'PageUp':
          event.preventDefault();
          moveActive(Math.max(0, current - page));
          break;
        case 'Home':
          event.preventDefault();
          moveActive(0);
          break;
        case 'End':
          event.preventDefault();
          moveActive(count - 1);
          break;
        case 'Enter': {
          const row = current >= 0 ? visible[current] : undefined;
          if (!row) break;
          event.preventDefault();
          onLineActivate?.(row.line, row.index);
          break;
        }
        case 'c':
        case 'C': {
          // Bare `c` only. Ctrl/Cmd+C must stay with the native selection copy,
          // which is the whole point of keeping selection working here.
          if (modified) break;
          const row = current >= 0 ? visible[current] : undefined;
          if (!row) break;
          event.preventDefault();
          copyLine(row);
          break;
        }
        case 'Escape':
          if (filterValue) {
            event.preventDefault();
            setFilterValue('');
          }
          break;
        default:
          break;
      }
    },
    [
      visible,
      activeIndex,
      viewportHeight,
      lineHeight,
      moveActive,
      copyLine,
      onLineActivate,
      filterValue,
      setFilterValue,
    ],
  );

  /* -- Timestamps ---------------------------------------------------------- */

  const timeFormatter = useMemo(() => {
    if (timestampFormat !== 'time') return null;
    try {
      return new Intl.DateTimeFormat(locale, {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        fractionalSecondDigits: 3,
        hour12: false,
        ...(timeZone ? { timeZone } : {}),
      });
    } catch {
      return null;
    }
  }, [timestampFormat, locale, timeZone]);

  const renderTimestamp = useCallback(
    (value: LogLine['timestamp']): { text: string; observed: boolean } => {
      if (value == null) return { text: '—', observed: false };
      if (timestampFormat === 'relative') {
        return { text: formatRelativeTime(value, Date.now(), locale), observed: true };
      }
      if (timestampFormat === 'time' && timeFormatter) {
        const date = value instanceof Date ? value : new Date(value);
        if (Number.isNaN(date.getTime())) return { text: '—', observed: false };
        return { text: timeFormatter.format(date), observed: true };
      }
      const text = formatTimestamp(value, {
        ...(locale !== undefined ? { locale } : {}),
        ...(timeZone !== undefined ? { timeZone } : {}),
        precision: 'millisecond',
      });
      return { text, observed: text !== '—' };
    },
    [timestampFormat, timeFormatter, locale, timeZone],
  );

  /* -- Line content -------------------------------------------------------- */

  const matchNeedle = caseSensitive ? query : query.toLowerCase();

  const renderText = useCallback(
    (row: PreparedLine): ReactNode => {
      let ranges: MatchRange[] = [];
      if (textFilterActive) {
        ranges = filterRegex
          ? findRegexRanges(row.plain, filterRegex)
          : findLiteralRanges(caseSensitive ? row.plain : row.lower, matchNeedle, {
              sourceLength: row.plain.length,
            });
      }

      const spans: AnsiSpan[] =
        ansi && needsAnsiParse(row.line.text)
          ? parseAnsi(row.line.text)
          : [{ text: row.plain, style: {} }];

      // Fast path: unstyled and unmatched means one text node for the whole row,
      // which is most of what makes scrolling 100k lines cheap.
      if (spans.length === 1 && ranges.length === 0) {
        const only = spans[0];
        if (only && Object.keys(only.style).length === 0) return only.text;
      }

      const nodes: ReactNode[] = [];
      let offset = 0;
      let key = 0;

      for (let s = 0; s < spans.length; s += 1) {
        const span = spans[s];
        if (!span || span.text.length === 0) continue;
        const spanStart = offset;
        const spanEnd = offset + span.text.length;
        offset = spanEnd;

        const spanStyle = span.style;
        const inline: CSSProperties = {};
        if (spanStyle.inverse) {
          inline.color = spanStyle.bg ?? 'var(--stratum-code-bg)';
          inline.backgroundColor = spanStyle.fg ?? 'currentColor';
        } else {
          if (spanStyle.fg) inline.color = spanStyle.fg;
          if (spanStyle.bg) inline.backgroundColor = spanStyle.bg;
        }

        const pieces = splitByRanges(spanStart, spanEnd, ranges);
        for (let p = 0; p < pieces.length; p += 1) {
          const piece = pieces[p];
          if (!piece) continue;
          const text = span.text.slice(piece.start - spanStart, piece.end - spanStart);
          if (!text) continue;

          // A matched piece drops the ANSI colours entirely and takes the
          // highlight's own pair from CSS. The highlight background is OUR
          // surface, and an author-chosen foreground on it is frequently
          // unreadable — a search hit you cannot read is worse than one that
          // has lost its colour for the length of the match. The rest of the
          // same coloured run is unaffected.
          //
          // `key` is passed directly rather than through this object: React
          // treats a spread `key` as an error and would put it on props instead
          // of using it for reconciliation.
          const attrs = {
            style: piece.match ? undefined : inline,
            'data-bold': spanStyle.bold || undefined,
            'data-dim': spanStyle.dim || undefined,
            'data-italic': spanStyle.italic || undefined,
            'data-underline': spanStyle.underline || undefined,
            'data-strike': spanStyle.strike || undefined,
            'data-conceal': spanStyle.conceal || undefined,
          };
          const pieceKey = key++;

          nodes.push(
            piece.match ? (
              <mark key={pieceKey} {...attrs} className="stratum-log__span stratum-log__match">
                {text}
              </mark>
            ) : (
              <span key={pieceKey} {...attrs} className="stratum-log__span">
                {text}
              </span>
            ),
          );
        }
      }

      return nodes;
    },
    [textFilterActive, filterRegex, caseSensitive, matchNeedle, ansi],
  );

  /* -- Render -------------------------------------------------------------- */

  // Leading action column + message column, plus the optional time and level.
  const columnCount = 2 + (showTimestamp ? 1 : 0) + (showLevel ? 1 : 0);
  const activeDescendant =
    activeIndex !== null &&
    activeIndex >= renderWindow.start &&
    activeIndex < renderWindow.end
      ? `${baseId}-r${activeIndex}`
      : undefined;

  const rows: ReactNode[] = [];
  for (let i = renderWindow.start; i < renderWindow.end; i += 1) {
    const row = visible[i];
    if (!row) continue;
    const stamp = showTimestamp ? renderTimestamp(row.line.timestamp) : null;
    const level = row.line.level;
    const levelText = level ? (levelLabels?.[level] ?? DEFAULT_LEVEL_LABELS[level]) : null;
    const isActive = activeIndex === i;

    rows.push(
      <div
        key={row.line.id}
        id={`${baseId}-r${i}`}
        role="row"
        // Header row occupies index 1, so data rows start at 2.
        aria-rowindex={i + 2}
        data-level={level ?? 'unobserved'}
        data-active={isActive || undefined}
        className="stratum-log__row"
        style={{ top: `${offsetOf(i)}px` }}
        onClick={() => {
          setActiveIndex(i);
          onLineActivate?.(row.line, row.index);
        }}
      >
        <span role="gridcell" className="stratum-log__cell stratum-log__actions-cell">
          <button
            type="button"
            className="stratum-log__row-copy"
            aria-label={labelCopyLine}
            tabIndex={-1}
            onClick={(event) => {
              event.stopPropagation();
              setActiveIndex(i);
              copyLine(row);
            }}
          >
            <CopyIcon />
          </button>
        </span>

        {showTimestamp && stamp && (
          <span
            role="gridcell"
            className="stratum-log__cell stratum-log__time"
            data-unobserved={!stamp.observed || undefined}
          >
            <span aria-hidden="true">{stamp.text}</span>
            <span className="stratum-visually-hidden">
              {stamp.observed ? stamp.text : labelUnobservedTime}
            </span>
          </span>
        )}

        {showLevel && (
          <span
            role="gridcell"
            className="stratum-log__cell stratum-log__level"
            data-unobserved={levelText === null || undefined}
          >
            <span aria-hidden="true">{levelText ?? '—'}</span>
            <span className="stratum-visually-hidden">{levelText ?? labelUnobservedLevel}</span>
          </span>
        )}

        <span role="gridcell" className="stratum-log__cell stratum-log__text">
          {row.line.source && <span className="stratum-log__source">{row.line.source}</span>}
          {renderText(row)}
        </span>
      </div>,
    );
  }

  const summary = summaryLabel({
    total: prepared.rows.length,
    shown: visible.length,
    following: followValue,
  });

  const copyAllIcon =
    allCopy.status === 'copied' ? (
      <CheckIcon />
    ) : allCopy.status === 'error' ? (
      <CrossIcon />
    ) : (
      <CopyIcon />
    );

  const rootStyle = {
    ...style,
    '--_viewport-h': typeof height === 'number' ? `${height}px` : height,
    ...(levelGutter ? { '--_gutter-level': levelGutter } : {}),
  } as CSSProperties;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="log-viewer"
      data-density={density}
      data-ts-format={showTimestamp ? timestampFormat : undefined}
      data-wrap={wrapValue || undefined}
      data-following={followValue || undefined}
      className={clsx('stratum-log', className)}
      style={rootStyle}
    >
      {toolbar && (
        <div className="stratum-log__toolbar">
          {/* The framework's own search field rather than a bare
              `<input type="search">`. A raw one leaks two pieces of browser
              chrome into the toolbar: WebKit's `searchfield` appearance, which
              repaints the border and ignores the height, and the engine's grey
              `::-webkit-search-cancel-button`, which is invisible on a dark
              surface and matches nothing else here. SearchInput suppresses both
              and supplies the system's own clear control, focus ring, disabled
              and forced-colors treatments.
              `onSearch` is deliberately not used — the filter is applied from
              `onChange` on every keystroke, as before, so no debounce is
              introduced. */}
          <SearchInput
            className="stratum-log__search"
            size="sm"
            variant="subtle"
            fullWidth
            value={filterValue}
            onChange={(event) => setFilterValue(event.target.value)}
            aria-label={labelFilter}
            placeholder={filterPlaceholder}
            invalid={filterInvalid}
            aria-describedby={filterInvalid ? `${baseId}-filter-error` : undefined}
            clearLabel={labelClearFilter}
          />

          {filterInvalid && (
            <span id={`${baseId}-filter-error`} className="stratum-log__filter-error">
              {labelInvalidPattern}
            </span>
          )}

          {showLevel && levelOptions.length > 0 && (
            <div className="stratum-log__levels" role="group" aria-label={labelLevelFilter}>
              {levelOptions.map((option) => {
                const on = enabledSet.has(option);
                return (
                  <button
                    key={option}
                    type="button"
                    className="stratum-log__level-chip"
                    data-level={option}
                    data-on={on || undefined}
                    aria-pressed={on}
                    onClick={() =>
                      setLevelsValue(
                        on
                          ? levelsValue.filter((item) => item !== option)
                          : [...levelsValue, option],
                      )
                    }
                  >
                    {levelLabels?.[option] ?? DEFAULT_LEVEL_LABELS[option]}
                  </button>
                );
              })}
            </div>
          )}

          <div className="stratum-log__actions">
            <button
              type="button"
              className="stratum-log__action"
              aria-pressed={wrapValue}
              aria-label={labelWrap}
              title={labelWrap}
              onClick={() => setWrapValue(!wrapValue)}
            >
              <WrapIcon />
            </button>
            <button
              type="button"
              className="stratum-log__action"
              aria-pressed={followValue}
              aria-label={labelFollow}
              title={labelFollow}
              onClick={() => {
                if (followValue) {
                  setFollowValue(false);
                  setDetachedAt(visible.length);
                } else {
                  jumpToLatest();
                }
              }}
            >
              <FollowIcon />
            </button>
            <button
              type="button"
              className="stratum-log__action"
              data-copy-state={allCopy.status}
              aria-label={
                allCopy.status === 'copied'
                  ? labelCopied
                  : allCopy.status === 'error'
                    ? labelCopyFailed
                    : labelCopyAll
              }
              title={labelCopyAll}
              onClick={copyAll}
            >
              {copyAllIcon}
            </button>
          </div>
        </div>
      )}

      <div className="stratum-log__frame">
        <div
          ref={viewportRef}
          className="stratum-log__viewport stratum-focus-inset"
          role="grid"
          aria-label={label}
          aria-rowcount={visible.length + 1}
          aria-colcount={columnCount}
          aria-activedescendant={activeDescendant}
          tabIndex={0}
          onScroll={handleScroll}
          onKeyDown={handleKeyDown}
        >
          {/* Hidden metrics row. Structurally identical to a real row, so the
              message column's available width and the font's advance are
              measured from the actual layout rather than assumed. */}
          <div className="stratum-log__metrics" aria-hidden="true">
            <span className="stratum-log__cell stratum-log__actions-cell" />
            {showTimestamp && (
              <span className="stratum-log__cell stratum-log__time">00:00:00.000</span>
            )}
            {showLevel && <span className="stratum-log__cell stratum-log__level">Trace</span>}
            <span ref={metricsTextRef} className="stratum-log__cell stratum-log__text">
              <span ref={probeRef} className="stratum-log__probe">
                {PROBE_TEXT}
              </span>
            </span>
          </div>

          <div role="rowgroup" className="stratum-visually-hidden">
            <div role="row" aria-rowindex={1}>
              <span role="columnheader">{columnLabels?.actions ?? 'Actions'}</span>
              {showTimestamp && <span role="columnheader">{columnLabels?.timestamp ?? 'Time'}</span>}
              {showLevel && <span role="columnheader">{columnLabels?.level ?? 'Level'}</span>}
              <span role="columnheader">{columnLabels?.message ?? 'Message'}</span>
            </div>
          </div>

          <div
            role="rowgroup"
            className="stratum-log__spacer"
            style={{
              height: `${layout.total}px`,
              ...(contentWidth !== null ? { width: `${contentWidth}px` } : {}),
            }}
          >
            {rows}
          </div>
        </div>

        {visible.length === 0 && (
          <p className="stratum-log__empty">
            {prepared.rows.length === 0 ? labelEmpty : labelNoMatches}
          </p>
        )}

        {!followValue && visible.length > 0 && (
          <button
            type="button"
            className="stratum-log__jump"
            data-new={newSinceDetach > 0 || undefined}
            onClick={jumpToLatest}
          >
            <ArrowToBottomIcon />
            <span>{newSinceDetach > 0 ? labelNewLines(newSinceDetach) : labelJumpToLatest}</span>
          </button>
        )}
      </div>

      <p
        className="stratum-log__summary"
        {...(announceSummary ? ({ role: 'status', 'aria-live': 'polite' } as const) : {})}
      >
        <span>{summary}</span>
        {lineCopy.status !== 'idle' && (
          <span className="stratum-log__copy-state" data-copy-state={lineCopy.status}>
            {lineCopy.status === 'copied' ? labelCopied : labelCopyFailed}
          </span>
        )}
      </p>
    </div>
  );
});
