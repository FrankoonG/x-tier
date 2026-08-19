import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ClipboardEvent as ReactClipboardEvent,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactElement,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { Input } from '../../components/Input/Input';
import { InlineMessage } from '../../components/InlineMessage/InlineMessage';
import { useEventCallback } from '../../hooks/useEventCallback';
import { formatCount } from '../format';
import { formatPortRange, parsePortList } from '../PortRangeInput/portRange';
import {
  AddRowButton,
  ListAnnouncer,
  RemoveIcon,
  ReorderControls,
  looksLikeBlock,
  makeRowId,
  moveItem,
  splitPastedBlock,
  useAnnouncer,
  useFocusRelay,
  useReorder,
  useReorderLabels,
  type ReorderLabels,
} from '../_shared/rowList';
import './RangeList.css';

export type RangeParseResult<T> = { ok: true; start: T; end: T } | { ok: false; message: string };

/**
 * Everything the list needs to know about one kind of range.
 *
 * The bound type is opaque: ports are numbers, time windows are minutes past
 * midnight, IP pools are 32-bit integers. Only `compare` gives them an order,
 * which is why overlap detection here is genuinely generic rather than an
 * integer routine with a different name.
 */
export interface RangeSpec<T> {
  /**
   * Text to bounds. Must NOT silently swap inverted endpoints — the list
   * reports `8100-8000` as an error, because reversing it turns a typo into a
   * rule nobody reviewed.
   */
  parse: (text: string) => RangeParseResult<T>;
  /** Bounds to canonical text. */
  format: (start: T, end: T) => string;
  /** Total ordering on the bound type. Negative, zero, positive. */
  compare: (a: T, b: T) => number;
  /**
   * Units covered inclusively. Omit it and the total reads as UNCOUNTED rather
   * than as zero — a range type with no measure has an unknown span, which is
   * not the same fact as an empty one.
   */
  size?: (start: T, end: T) => number;
  /** `true` when `b` begins immediately after `a` ends. Enables adjacency merge. */
  adjacent?: (aEnd: T, bStart: T) => boolean;
}

export interface RangeEntry {
  id: string;
  /** The range as the operator wrote it. Never rewritten except by Merge. */
  text: string;
  /** Optional name used when this entry is cited as a conflict counterparty. */
  label?: string;
  locked?: boolean;
}

export type RangeIssueCode = 'invalid' | 'inverted' | 'overlap';

export interface RangeIssue {
  code: RangeIssueCode;
  /** `error` blocks; `overlap` is a warning that Merge resolves. */
  severity: 'error' | 'warning';
  entryId: string;
  message: string;
  /** Entry this one conflicts with. Overlaps only. */
  withEntryId?: string;
}

export interface RangeListValidity {
  valid: boolean;
  issues: RangeIssue[];
  /** Distinct units covered by the merged set. `null` when uncountable. */
  totalCovered: number | null;
}

export interface RangeListProps<T> extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  entries: readonly RangeEntry[];
  onChange: (entries: RangeEntry[]) => void;
  spec: RangeSpec<T>;

  reorderable?: boolean;
  showIndex?: boolean;
  showEdgeMoves?: boolean;
  minEntries?: number;
  maxEntries?: number;
  /** `'warning'` (default) lets an overlap through; `'error'` blocks it. */
  overlapSeverity?: 'warning' | 'error';
  /** Offers the merge action when overlaps or adjacencies exist. Default `true`. */
  mergeable?: boolean;
  /** Suppress per-row errors until the row has been blurred. Default `true`. */
  validateOnBlurOnly?: boolean;
  showAllErrors?: boolean;
  allowEmpty?: boolean;
  enterInsertsRow?: boolean;
  backspaceRemovesRow?: boolean;
  size?: 'sm' | 'md' | 'lg';
  label?: string;
  placeholder?: string;
  /**
   * The namespace the overlap comparison is scoped to — "per interface",
   * "per protocol", "per tenant". Two identical ranges may or may not conflict
   * and the operator cannot tell which without being told the scope.
   */
  scopeLabel?: string;
  showTotal?: boolean;

  onValidityChange?: (validity: RangeListValidity) => void;

  labelRow?: (position: number, total: number) => string;
  labelAdd?: string;
  labelRemove?: (position: number, total: number) => string;
  labelInverted?: string;
  labelOverlap?: (other: string, intersection: string) => string;
  labelEmptyRow?: string;
  labelMerge?: string;
  labelMergeHint?: (count: number) => string;
  labelTotal?: (covered: string, ranges: number) => string;
  labelUncounted?: string;
  labelExcluded?: (count: number) => string;
  labelSummary?: (bad: number, total: number) => string;
  labelMaxReached?: (max: number) => string;
  labelMinReached?: (min: number) => string;
  /** Name used for an entry with no `label`, when cited as a counterparty. */
  labelEntryName?: (position: number) => string;
  announceAdded?: (total: number) => string;
  announceRemoved?: (position: number, remaining: number) => string;
  announceMerged?: (before: number, after: number) => string;
  announcePasted?: (added: number) => string;
  reorderLabels?: Partial<ReorderLabels>;
}

interface Analysed<T> {
  entry: RangeEntry;
  empty: boolean;
  ok: boolean;
  /** Parsed but with the bounds the wrong way round. Never auto-corrected. */
  inverted: boolean;
  start: T | null;
  end: T | null;
  /** Parser wording. `null` for an inverted range, whose wording is ours. */
  message: string | null;
  /** Index of the earlier entry this one intersects, or -1. */
  overlapWith: number;
  /** Formatted intersection with that entry. */
  intersection: string | null;
}

/**
 * A list of ranges that must not overlap.
 *
 * Port ranges, address pools, schedule windows. Generic over the bound type via
 * `spec`, so the overlap arithmetic is written once and the domain supplies
 * `parse` / `format` / `compare`.
 *
 * OVERLAP IS NAMED, NOT ASSERTED
 * ------------------------------
 * Every real product this pattern was surveyed against reports overlap as a
 * bare sentence — "the specified range overlaps an existing range" — which
 * leaves the operator to find the counterparty themselves. Here the message
 * names the OTHER entry and the exact intersecting sub-range, because those two
 * facts are the entire content of the finding.
 *
 * ERROR AND WARNING ARE DIFFERENT
 * -------------------------------
 * An inverted range is an error: there is no defensible reading of `8100-8000`,
 * and swapping it silently would turn a typo into configuration nobody reviewed.
 * An overlap is a warning by default: the union is unambiguous, it arises
 * naturally from pasting two lists together, and it is sometimes deliberate. A
 * warning that blocks is worse than an honest error, so `overlapSeverity` is a
 * prop rather than a hardcoded decision.
 *
 * MERGE IS AN ACTION, NEVER AN EFFECT
 * -----------------------------------
 * Coalescing is offered as a button that says how many entries it will collapse.
 * It runs only over entries that PARSE — anything invalid is left exactly where
 * it is, because a merge that quietly discards the row the operator was still
 * fixing is the worst possible response to a typo.
 *
 * THE TOTAL DISTINGUISHES ZERO FROM UNKNOWN
 * -----------------------------------------
 * With no `size` in the spec the covered count reads as uncounted rather than as
 * `0`, and a set containing unparsed rows says how many it left out.
 */
function RangeListInner<T>(
  {
    entries,
    onChange,
    spec,
    reorderable = true,
    showIndex = true,
    showEdgeMoves = false,
    minEntries = 1,
    maxEntries,
    overlapSeverity = 'warning',
    mergeable = true,
    validateOnBlurOnly = true,
    showAllErrors = false,
    allowEmpty = false,
    enterInsertsRow = true,
    backspaceRemovesRow = true,
    size = 'md',
    label,
    placeholder,
    scopeLabel,
    showTotal = true,
    onValidityChange,
    labelRow = (position, total) => `Range ${position} of ${total}`,
    labelAdd = 'Add range',
    labelRemove = (position, total) => `Remove range ${position} of ${total}`,
    labelInverted = 'This range runs backwards. Write the lower bound first.',
    labelOverlap = (other, intersection) => `Overlaps ${other} at ${intersection}.`,
    labelEmptyRow = 'Enter a range.',
    labelMerge = 'Merge overlapping ranges',
    labelMergeHint = (count) =>
      count === 1 ? '1 range can be merged.' : `${count} ranges can be merged.`,
    labelTotal = (covered, ranges) =>
      `${covered} covered across ${ranges} ${ranges === 1 ? 'range' : 'ranges'}.`,
    labelUncounted = 'not counted',
    labelExcluded = (count) =>
      count === 1 ? '1 entry could not be read and is excluded.' : `${count} entries could not be read and are excluded.`,
    labelSummary = (bad, total) => `${bad} of ${total} ranges need attention.`,
    labelMaxReached = (max) => `Limit of ${max} ranges reached.`,
    labelMinReached = (min) => `At least ${min} ${min === 1 ? 'range' : 'ranges'} required.`,
    labelEntryName = (position) => `range ${position}`,
    announceAdded = (total) => `Range added. ${total} ranges.`,
    announceRemoved = (position, remaining) =>
      `Range ${position} removed. ${remaining} ${remaining === 1 ? 'range' : 'ranges'} remain.`,
    announceMerged = (before, after) => `${before} ranges merged into ${after}.`,
    announcePasted = (added) => `${added} ranges added.`,
    reorderLabels,
    className,
    ...rest
  }: RangeListProps<T>,
  ref: Ref<HTMLDivElement>,
) {
  const uid = useId();
  const total = entries.length;
  const { message, announce } = useAnnouncer();
  const { register, requestFocus } = useFocusRelay();
  const labels = useReorderLabels(reorderLabels);
  const [touched, setTouched] = useState<ReadonlySet<string>>(() => new Set());

  const nameOf = useCallback(
    (index: number) => entries[index]?.label ?? labelEntryName(index + 1),
    [entries, labelEntryName],
  );

  /* -- One analysis pass --------------------------------------------------- */
  const analysis = useMemo<Analysed<T>[]>(() => {
    const out: Analysed<T>[] = [];

    for (let i = 0; i < entries.length; i += 1) {
      const entry = entries[i]!;
      const text = entry.text.trim();

      if (text === '') {
        out.push({
          entry,
          empty: true,
          ok: false,
          inverted: false,
          start: null,
          end: null,
          message: null,
          overlapWith: -1,
          intersection: null,
        });
        continue;
      }

      const parsed = spec.parse(text);
      if (!parsed.ok) {
        out.push({
          entry,
          empty: false,
          ok: false,
          inverted: false,
          start: null,
          end: null,
          message: parsed.message,
          overlapWith: -1,
          intersection: null,
        });
        continue;
      }
      if (spec.compare(parsed.start, parsed.end) > 0) {
        // Kept as an ERROR rather than swapped. There is no defensible reading
        // of a backwards range, and reversing it silently turns a typo into a
        // rule nobody reviewed.
        out.push({
          entry,
          empty: false,
          ok: false,
          inverted: true,
          start: parsed.start,
          end: parsed.end,
          message: null,
          overlapWith: -1,
          intersection: null,
        });
        continue;
      }

      // Pairwise against everything already accepted. These lists are short by
      // construction — a hundred ranges is a very large firewall rule.
      let overlapWith = -1;
      let intersection: string | null = null;
      for (let j = 0; j < out.length; j += 1) {
        const other = out[j];
        if (!other || !other.ok || other.start === null || other.end === null) continue;
        if (
          spec.compare(parsed.start, other.end) <= 0 &&
          spec.compare(other.start, parsed.end) <= 0
        ) {
          overlapWith = j;
          const lo = spec.compare(parsed.start, other.start) >= 0 ? parsed.start : other.start;
          const hi = spec.compare(parsed.end, other.end) <= 0 ? parsed.end : other.end;
          intersection = spec.format(lo, hi);
          break;
        }
      }

      out.push({
        entry,
        empty: false,
        ok: true,
        inverted: false,
        start: parsed.start,
        end: parsed.end,
        message: null,
        overlapWith,
        intersection,
      });
    }
    return out;
  }, [entries, spec]);

  /** Merged view of everything that parsed. Drives the total and Merge. */
  const merged = useMemo(() => {
    const usable = analysis.filter(
      (a): a is Analysed<T> & { start: T; end: T } => a.ok && a.start !== null && a.end !== null,
    );
    const sorted = [...usable].sort((a, b) => spec.compare(a.start, b.start));
    const out: { start: T; end: T }[] = [];
    for (const item of sorted) {
      const last = out[out.length - 1];
      const touching =
        last !== undefined &&
        (spec.compare(item.start, last.end) <= 0 ||
          (spec.adjacent ? spec.adjacent(last.end, item.start) : false));
      if (last && touching) {
        if (spec.compare(item.end, last.end) > 0) last.end = item.end;
        continue;
      }
      out.push({ start: item.start, end: item.end });
    }
    return { ranges: out, usableCount: usable.length };
  }, [analysis, spec]);

  const totalCovered = useMemo(() => {
    const measure = spec.size;
    if (!measure) return null;
    return merged.ranges.reduce((sum, r) => sum + measure(r.start, r.end), 0);
  }, [merged, spec]);

  const issues = useMemo<RangeIssue[]>(() => {
    const list: RangeIssue[] = [];
    analysis.forEach((a) => {
      if (a.empty) {
        if (!allowEmpty) {
          list.push({
            code: 'invalid',
            severity: 'error',
            entryId: a.entry.id,
            message: labelEmptyRow,
          });
        }
        return;
      }
      if (!a.ok) {
        list.push({
          code: a.inverted ? 'inverted' : 'invalid',
          severity: 'error',
          entryId: a.entry.id,
          message: a.message ?? labelInverted,
        });
        return;
      }
      if (a.overlapWith >= 0) {
        const other = analysis[a.overlapWith];
        const issue: RangeIssue = {
          code: 'overlap',
          severity: overlapSeverity === 'error' ? 'error' : 'warning',
          entryId: a.entry.id,
          message: labelOverlap(nameOf(a.overlapWith), a.intersection ?? ''),
        };
        if (other) issue.withEntryId = other.entry.id;
        list.push(issue);
      }
    });
    return list;
  }, [analysis, allowEmpty, overlapSeverity, labelEmptyRow, labelInverted, labelOverlap, nameOf]);

  const validity = useMemo<RangeListValidity>(
    () => ({
      valid: !issues.some((issue) => issue.severity === 'error'),
      issues,
      totalCovered,
    }),
    [issues, totalCovered],
  );

  const emitValidity = useEventCallback(onValidityChange);
  const lastSignature = useRef<string | null>(null);
  useEffect(() => {
    const signature = `${validity.valid}|${validity.totalCovered ?? ''}|${issues
      .map((i) => `${i.entryId}:${i.code}`)
      .join(',')}`;
    if (lastSignature.current === signature) return;
    lastSignature.current = signature;
    emitValidity(validity);
  }, [validity, issues, emitValidity]);

  /* -- Mutations ----------------------------------------------------------- */

  const setText = useCallback(
    (index: number, text: string) => {
      const next = entries.slice();
      const entry = next[index];
      if (!entry) return;
      next[index] = { ...entry, text };
      onChange(next);
    },
    [entries, onChange],
  );

  const insertAt = useCallback(
    (index: number, texts: string[] = ['']) => {
      const created = texts.map((text) => ({ id: makeRowId('range'), text }));
      const next = entries.slice();
      next.splice(index, 0, ...created);
      onChange(next);
      const last = created[created.length - 1];
      if (last) requestFocus(`field:${last.id}`);
      return created;
    },
    [entries, onChange, requestFocus],
  );

  const removeAt = useCallback(
    (index: number) => {
      if (total <= minEntries) return;
      const next = entries.slice();
      next.splice(index, 1);
      const target = next[index] ?? next[index - 1];
      if (target) requestFocus(`field:${target.id}`);
      else requestFocus('add');
      onChange(next);
      announce(announceRemoved(index + 1, next.length));
    },
    [entries, total, minEntries, onChange, requestFocus, announce, announceRemoved],
  );

  const handleMove = useCallback(
    (from: number, to: number) => onChange(moveItem(entries, from, to)),
    [entries, onChange],
  );

  const reorderEnabled = reorderable && total > 1;
  const reorder = useReorder({
    count: total,
    onMove: handleMove,
    announce,
    labels,
    disabled: !reorderEnabled,
  });

  const collapsible = merged.usableCount - merged.ranges.length;

  const applyMerge = useCallback(() => {
    // Only the entries that parsed are replaced. Anything invalid keeps its
    // text and its position at the end, so a merge can never eat the row
    // somebody is still fixing.
    const survivors = entries.filter((_, index) => {
      const a = analysis[index];
      return !a || !a.ok;
    });
    const mergedEntries = merged.ranges.map((range) => ({
      id: makeRowId('range'),
      text: spec.format(range.start, range.end),
    }));
    const before = merged.usableCount;
    onChange([...mergedEntries, ...survivors]);
    announce(announceMerged(before, mergedEntries.length));
  }, [entries, analysis, merged, spec, onChange, announce, announceMerged]);

  /* -- Paste and keyboard --------------------------------------------------- */

  const handlePaste = (index: number, event: ReactClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData('text');
    if (!text || !looksLikeBlock(text)) return;
    event.preventDefault();
    const parts = splitPastedBlock(text);
    const [first, ...remainder] = parts;
    const next = entries.slice();
    const current = next[index];
    if (!current) return;
    next[index] = { ...current, text: first ?? '' };
    next.splice(index + 1, 0, ...remainder.map((value) => ({ id: makeRowId('range'), text: value })));
    const capped = maxEntries !== undefined ? next.slice(0, maxEntries) : next;
    onChange(capped);
    setTouched((prev) => {
      const set = new Set(prev);
      for (const row of capped.slice(index)) set.add(row.id);
      return set;
    });
    const placed = Math.min(parts.length, capped.length - index);
    const last = capped[Math.min(index + remainder.length, capped.length - 1)];
    if (last) requestFocus(`field:${last.id}`);
    announce(announcePasted(placed));
  };

  const handleKeyDown = (index: number, event: ReactKeyboardEvent<HTMLInputElement>) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const entry = entries[index];
    if (!entry) return;

    if (enterInsertsRow && event.key === 'Enter') {
      if (maxEntries !== undefined && total >= maxEntries) return;
      event.preventDefault();
      insertAt(index + 1);
      announce(announceAdded(total + 1));
      return;
    }

    if (backspaceRemovesRow && event.key === 'Backspace' && entry.text === '' && total > minEntries) {
      event.preventDefault();
      const prev = entries[index - 1] ?? entries[index + 1];
      const next = entries.slice();
      next.splice(index, 1);
      onChange(next);
      if (prev) requestFocus(`field:${prev.id}`);
      else requestFocus('add');
      announce(announceRemoved(index + 1, next.length));
    }
  };

  /* -- Render -------------------------------------------------------------- */

  const revealed = (id: string) => showAllErrors || !validateOnBlurOnly || touched.has(id);
  const problemCount = analysis.filter(
    (a) => revealed(a.entry.id) && !a.ok && (!a.empty || !allowEmpty),
  ).length;
  const excludedCount = analysis.filter((a) => !a.ok).length;

  const atCeiling = maxEntries !== undefined && total >= maxEntries;
  const atFloor = total <= minEntries;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="range-list"
      data-size={size}
      data-dragging={reorder.dragging || undefined}
      className={clsx('stratum-range-list', className)}
    >
      {problemCount > 0 && (
        <InlineMessage variant="danger" size="xs" role="status">
          {labelSummary(problemCount, total)}
        </InlineMessage>
      )}

      <span id={reorder.instructionsId} className="stratum-visually-hidden">
        {reorder.instructions}
      </span>

      <ol
        role="list"
        className="stratum-row-list stratum-range-list__list"
        aria-label={label}
        data-dragging={reorder.dragging || undefined}
      >
        {entries.map((entry, index) => {
          const rowProps = reorder.getRowProps(index);
          const a = analysis[index];
          const noteId = `${uid}note${index}`;
          const show = revealed(entry.id);

          const problem =
            a && !a.ok && show
              ? a.empty
                ? allowEmpty
                  ? null
                  : labelEmptyRow
                : a.inverted
                  ? labelInverted
                  : a.message
              : null;
          const overlap =
            a && a.ok && a.overlapWith >= 0
              ? labelOverlap(nameOf(a.overlapWith), a.intersection ?? '')
              : null;
          const note = problem ?? overlap;

          return (
            <li
              {...rowProps}
              key={entry.id}
              className="stratum-row-list__row stratum-range-list__row"
              aria-label={labelRow(index + 1, total)}
              data-invalid={problem ? true : undefined}
              data-overlap={overlap ? true : undefined}
            >
              {showIndex && (
                <span className="stratum-row-list__index" aria-hidden="true">
                  {index + 1}
                </span>
              )}

              {reorderEnabled && (
                <ReorderControls
                  index={index}
                  total={total}
                  api={reorder}
                  labels={labels}
                  showEdgeMoves={showEdgeMoves}
                  locked={entry.locked}
                />
              )}

              <div className="stratum-row-list__main">
                <Input
                  ref={register(`field:${entry.id}`)}
                  size={size}
                  fullWidth
                  value={entry.text}
                  invalid={Boolean(problem)}
                  aria-label={labelRow(index + 1, total)}
                  aria-describedby={note ? noteId : undefined}
                  placeholder={placeholder}
                  autoComplete="off"
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                  inputClassName="stratum-range-list__input"
                  onValueChange={(value) => setText(index, value)}
                  onKeyDown={(event) => handleKeyDown(index, event)}
                  onPaste={(event) => handlePaste(index, event)}
                  onBlur={() =>
                    setTouched((prev) => {
                      if (prev.has(entry.id)) return prev;
                      const set = new Set(prev);
                      set.add(entry.id);
                      return set;
                    })
                  }
                />
                {note && (
                  <p
                    className="stratum-row-list__note"
                    data-tone={problem ? 'danger' : overlapSeverity === 'error' ? 'danger' : 'warning'}
                    id={noteId}
                  >
                    {note}
                  </p>
                )}
              </div>

              <div className="stratum-row-list__actions">
                <button
                  type="button"
                  className="stratum-row-list__action"
                  data-tone="danger"
                  aria-label={labelRemove(index + 1, total)}
                  disabled={atFloor || entry.locked}
                  aria-describedby={atFloor ? `${uid}floor` : undefined}
                  onClick={() => removeAt(index)}
                >
                  {RemoveIcon}
                </button>
              </div>
            </li>
          );
        })}
      </ol>

      {atFloor && minEntries > 0 && (
        <span id={`${uid}floor`} className="stratum-row-list__ceiling">
          {labelMinReached(minEntries)}
        </span>
      )}

      <div className="stratum-range-list__footer">
        {showTotal && (
          <p className="stratum-range-list__total">
            <span
              className="stratum-range-list__total-value"
              data-unobserved={totalCovered === null || undefined}
            >
              {totalCovered === null
                ? labelUncounted
                : labelTotal(formatCount(totalCovered), merged.ranges.length)}
            </span>
            {scopeLabel && <span className="stratum-range-list__scope">{scopeLabel}</span>}
            {excludedCount > 0 && (
              <span className="stratum-range-list__excluded">{labelExcluded(excludedCount)}</span>
            )}
          </p>
        )}

        {mergeable && collapsible > 0 && (
          <div className="stratum-range-list__merge">
            <span className="stratum-range-list__merge-hint">{labelMergeHint(collapsible)}</span>
            <button type="button" className="stratum-range-list__merge-button" onClick={applyMerge}>
              {labelMerge}
            </button>
          </div>
        )}
      </div>

      <AddRowButton
        label={labelAdd}
        buttonRef={register('add')}
        disabled={atCeiling}
        disabledReason={maxEntries !== undefined ? labelMaxReached(maxEntries) : undefined}
        onClick={() => {
          insertAt(total);
          announce(announceAdded(total + 1));
        }}
      />

      <ListAnnouncer message={message} />
    </div>
  );
}

/**
 * `forwardRef` erases generics, so the cast restores the call signature. This is
 * the standard workaround for a generic component with a forwarded ref and is
 * confined to this one line.
 */
export const RangeList = forwardRef(RangeListInner) as <T>(
  props: RangeListProps<T> & { ref?: Ref<HTMLDivElement> },
) => ReactElement;

/* ========================================================================== */
/* A ready-made spec for the commonest case                                   */
/* ========================================================================== */

/**
 * Port ranges, built on the existing `portRange.ts` grammar.
 *
 * Deliberately rejects a multi-item string: a row here holds ONE range, and a
 * control that quietly accepts `80,443,8000-8100` in a box labelled "range"
 * hides a whole grammar behind an error message.
 */
export const PORT_RANGE_SPEC: RangeSpec<number> = {
  parse(text) {
    const result = parsePortList(text);
    const first = result.ranges[0];
    if (!result.ok || result.ranges.length !== 1 || !first) {
      const issue = result.issues.find((i) => i.severity === 'error');
      return {
        ok: false,
        message: issue?.message ?? 'Enter one port or one range, for example 8000-8100.',
      };
    }
    return { ok: true, start: first.start, end: first.end };
  },
  format: (start, end) => formatPortRange({ start, end }),
  compare: (a, b) => a - b,
  size: (start, end) => end - start + 1,
  adjacent: (aEnd, bStart) => bStart === aEnd + 1,
};
