import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { Switch } from '../../components/Switch/Switch';
import { Tag } from '../../components/Tag/Tag';
import { InlineMessage } from '../../components/InlineMessage/InlineMessage';
import { useEventCallback } from '../../hooks/useEventCallback';
import { UNOBSERVED, formatCount } from '../format';
import {
  AddRowButton,
  ListAnnouncer,
  RemoveIcon,
  ReorderControls,
  moveItem,
  useAnnouncer,
  useFocusRelay,
  useReorder,
  useReorderLabels,
  type ReorderLabels,
} from '../_shared/rowList';
import './PriorityList.css';

/**
 * What the engine reported about one entry.
 *
 * `unknown` is the default and means NOT MEASURED. It is never inferred from
 * position, from a zero hit count, or from the absence of a report — a rule the
 * panel has no telemetry for must not be shown as one that was evaluated and
 * did not match, because those two facts lead an operator to opposite
 * conclusions when a rule appears not to be working.
 */
export type PriorityMatchState = 'matched' | 'evaluated' | 'unreachable' | 'unknown';

/**
 * Whether the entry is live.
 *
 * A list of routing rules is almost never applied the instant it is edited:
 * `dirty` is changed locally, `pending` is submitted but not yet applied by the
 * engine, and only `clean` describes what is actually running. Collapsing the
 * three is how a panel ends up showing an operator a rule set that is not the
 * one in force.
 */
export type PriorityEntryState = 'clean' | 'dirty' | 'pending';

export interface PriorityEntry {
  id: string;
  /** Primary row content. */
  label: ReactNode;
  /**
   * Plain-text name used in accessible names and announcements. Required when
   * `label` is not a string, otherwise the row is announced by position alone.
   */
  name?: string;
  description?: ReactNode;
  /** Extra row content — a `PathChain`, a `Tag` row, a metric. */
  detail?: ReactNode;
  /** Default `true`. A disabled entry is skipped by the engine. */
  enabled?: boolean;
  /** Engine-reported match state. Default `'unknown'`. */
  match?: PriorityMatchState;
  /**
   * Times traffic hit this entry. `null` means the counter exists and reads
   * zero-but-unobserved; `undefined` means there is no counter at all. They are
   * rendered differently and neither is drawn as `0`.
   */
  hitCount?: number | null;
  /**
   * Ids of the earlier entries that make this one unreachable.
   *
   * A WARNING, never an error: a shadowed rule is legal, is sometimes left in
   * place deliberately during a migration, and blocking on it would be wrong.
   */
  shadowedBy?: readonly string[];
  state?: PriorityEntryState;
  /** Cannot be moved or removed. */
  locked?: boolean;
}

export interface PriorityListProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  entries: readonly PriorityEntry[];
  /**
   * Receives the reordered array as well as the indices, so the common case
   * needs no array surgery in the consumer.
   */
  onReorder?: (entries: PriorityEntry[], from: number, to: number) => void;
  onToggleEnabled?: (id: string, enabled: boolean) => void;
  onRemove?: (id: string) => void;
  /** Renders the dashed add plate when supplied. */
  onAdd?: () => void;

  /**
   * How the engine walks the list. Rendered as a caption above it and wired to
   * the list through `aria-describedby`, because position only means something
   * once this is stated.
   */
  semantics?: 'first-match' | 'last-match' | 'all';
  reorderable?: boolean;
  /** Shows the 1-based evaluation index. Default `true`. */
  showIndex?: boolean;
  /** Adds move-to-top / move-to-bottom buttons to every row. Default `false`. */
  showEdgeMoves?: boolean;
  /**
   * Forces the hit-count column on or off. Default: on when any entry carries a
   * `hitCount` key.
   */
  showHits?: boolean;
  /**
   * Marks the match/hit column as no longer trustworthy.
   *
   * Left undefined the component sets this ITSELF after any reorder it emits,
   * and clears it when fresh counters arrive. Attribution decays the moment the
   * list is reordered — engines that link recorded traffic to rules by position
   * will point the old numbers at the new occupants — so the column has to say
   * so rather than quietly mislead.
   */
  attributionStale?: boolean;
  size?: 'sm' | 'md';
  /** Accessible name for the list. */
  label?: string;
  /** Maximum entries. The add plate explains itself at the ceiling. */
  maxEntries?: number;

  labelFirstMatch?: string;
  labelLastMatch?: string;
  labelAllMatch?: string;
  labelMatched?: string;
  labelEvaluated?: string;
  labelUnreachable?: string;
  labelUnknownMatch?: string;
  labelHits?: (count: number) => string;
  labelNoHits?: string;
  labelEnabled?: (name: string) => string;
  labelRemove?: (name: string) => string;
  labelAdd?: string;
  labelMaxReached?: (max: number) => string;
  labelDirty?: string;
  labelPending?: string;
  labelShadowed?: (by: string[]) => string;
  labelShadowSummary?: (count: number) => string;
  labelStaleAttribution?: string;
  /** Announced after a removal. */
  announceRemoved?: (name: string, remaining: number) => string;
  announceToggled?: (name: string, enabled: boolean) => string;
  announceAdded?: (total: number) => string;
  /** Row name used when an entry has neither `name` nor a string `label`. */
  labelRow?: (position: number, total: number, name: string | undefined) => string;
  reorderLabels?: Partial<ReorderLabels>;
}

const MATCH_VARIANT: Record<PriorityMatchState, 'success' | 'neutral' | 'warning' | 'unknown'> = {
  matched: 'success',
  evaluated: 'neutral',
  unreachable: 'warning',
  unknown: 'unknown',
};

function entryName(entry: PriorityEntry): string | undefined {
  if (entry.name !== undefined) return entry.name;
  return typeof entry.label === 'string' ? entry.label : undefined;
}

/**
 * An ordered list in which POSITION IS SEMANTICS.
 *
 * Routing rules, ACLs, fallback chains: the engine walks the list in order and
 * the first (or last) match wins, so moving row 4 above row 2 is a behavioural
 * change, not a cosmetic one. Three things follow from that, and they are the
 * whole design.
 *
 * ORDER IS STATED, NOT IMPLIED
 * ----------------------------
 * The evaluation index is rendered, the walk direction is captioned, and the
 * caption is wired to the list via `aria-describedby`. A reader who cannot see
 * the visual stacking still learns that order decides the outcome.
 *
 * REORDER HAS THREE INPUT PATHS
 * -----------------------------
 * Pointer drag, keyboard lift/drop, and explicit move buttons — see
 * `_shared/rowList.tsx`. Keyboard support alone does not satisfy WCAG 2.2 SC
 * 2.5.7, which is assessed independently of 2.1.1 because a touchscreen user may
 * have no keyboard.
 *
 * ATTRIBUTION IS A FIRST-CLASS COLUMN, AND IT EXPIRES
 * ---------------------------------------------------
 * "Which rule actually fired" is the most valuable thing on this screen and is
 * always bolted on afterwards. It is modelled here as four honest states rather
 * than a number: `matched`, `evaluated`, `unreachable`, and `unknown` for no
 * telemetry at all. The counters are also invalidated the moment the list is
 * reordered, because the edit this component exists to make is precisely the one
 * that breaks position-based attribution in the engine underneath.
 *
 * SHADOWING IS A WARNING
 * ----------------------
 * An entry that can never be reached because an earlier one already matches is
 * flagged with the entries responsible, and never as an error: a shadowed rule
 * is valid configuration, and refusing it would be wrong.
 */
export const PriorityList = forwardRef<HTMLDivElement, PriorityListProps>(function PriorityList(
  {
    entries,
    onReorder,
    onToggleEnabled,
    onRemove,
    onAdd,
    semantics = 'first-match',
    reorderable = true,
    showIndex = true,
    showEdgeMoves = false,
    showHits,
    attributionStale,
    size = 'md',
    label,
    maxEntries,
    labelFirstMatch = 'First match wins — evaluated top to bottom.',
    labelLastMatch = 'Last match wins — evaluated top to bottom, the final match applies.',
    labelAllMatch = 'Every matching entry applies, in order.',
    labelMatched = 'matched',
    labelEvaluated = 'evaluated, no match',
    labelUnreachable = 'never reached',
    labelUnknownMatch = 'not measured',
    labelHits = (count) => `${formatCount(count)} hits`,
    labelNoHits = 'no hit counter',
    labelEnabled = (name) => `Enable ${name}`,
    labelRemove = (name) => `Remove ${name}`,
    labelAdd = 'Add entry',
    labelMaxReached = (max) => `Limit of ${max} entries reached.`,
    labelDirty = 'edited',
    labelPending = 'not yet applied',
    labelShadowed = (by) => `Never reached: already matched by ${by.join(', ')}.`,
    labelShadowSummary = (count) =>
      count === 1 ? '1 entry can never be reached.' : `${count} entries can never be reached.`,
    labelStaleAttribution =
      'Match counts are from the previous order and no longer identify these entries.',
    announceRemoved = (name, remaining) =>
      `${name} removed. ${remaining} ${remaining === 1 ? 'entry' : 'entries'} remain.`,
    announceToggled = (name, enabled) => `${name} ${enabled ? 'enabled' : 'disabled'}.`,
    announceAdded = (total) => `Entry added. ${total} entries.`,
    labelRow = (position, total, name) =>
      name ? `${name}, position ${position} of ${total}` : `Entry ${position} of ${total}`,
    reorderLabels,
    className,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const semanticsId = `${uid}semantics`;
  const total = entries.length;

  const { message, announce } = useAnnouncer();
  const { register, requestFocus } = useFocusRelay();
  const labels = useReorderLabels(reorderLabels);

  const emitReorder = useEventCallback(onReorder);

  /* -- Attribution staleness ---------------------------------------------
   * Signed over ids and counts SORTED BY ID, so a pure reorder leaves the
   * signature untouched and only genuinely new telemetry clears the flag.
   */
  const hitSignature = useMemo(
    () =>
      entries
        .map((e) => `${e.id}=${e.hitCount ?? ''}:${e.match ?? ''}`)
        .sort()
        .join('|'),
    [entries],
  );
  const [selfStale, setSelfStale] = useState(false);
  const firstSignature = useRef(hitSignature);
  useEffect(() => {
    if (firstSignature.current === hitSignature) return;
    firstSignature.current = hitSignature;
    setSelfStale(false);
  }, [hitSignature]);

  const handleMove = useCallback(
    (from: number, to: number) => {
      setSelfStale(true);
      emitReorder(moveItem(entries, from, to), from, to);
    },
    [entries, emitReorder],
  );

  const reorderEnabled = reorderable && typeof onReorder === 'function' && total > 1;
  const reorder = useReorder({
    count: total,
    onMove: handleMove,
    announce,
    labels,
    disabled: !reorderEnabled,
  });

  const hasHitKey = entries.some((e) => e.hitCount !== undefined);
  const showHitColumn = showHits ?? hasHitKey;
  const stale = attributionStale ?? selfStale;

  const shadowedCount = entries.filter((e) => (e.shadowedBy?.length ?? 0) > 0).length;

  const nameOf = useCallback(
    (entry: PriorityEntry, index: number) =>
      entryName(entry) ?? labelRow(index + 1, total, undefined),
    [labelRow, total],
  );

  const handleRemove = (entry: PriorityEntry, index: number) => {
    const name = nameOf(entry, index);
    // Focus goes to the same control in the next row, else the previous row,
    // else the add plate. Never to <body>.
    const next = entries[index + 1];
    const prev = entries[index - 1];
    if (next) requestFocus(`remove:${next.id}`);
    else if (prev) requestFocus(`remove:${prev.id}`);
    else requestFocus('add');
    onRemove?.(entry.id);
    announce(announceRemoved(name, total - 1));
  };

  const semanticsText =
    semantics === 'first-match' ? labelFirstMatch : semantics === 'last-match' ? labelLastMatch : labelAllMatch;

  const atCeiling = maxEntries !== undefined && total >= maxEntries;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="priority-list"
      data-size={size}
      data-dragging={reorder.dragging || undefined}
      className={clsx('stratum-priority-list', className)}
    >
      <p className="stratum-priority-list__semantics" id={semanticsId}>
        {semanticsText}
      </p>

      {stale && showHitColumn && (
        <InlineMessage variant="warning" size="xs" role="status">
          {labelStaleAttribution}
        </InlineMessage>
      )}

      {shadowedCount > 0 && (
        <InlineMessage variant="warning" size="xs" role="none">
          {labelShadowSummary(shadowedCount)}
        </InlineMessage>
      )}

      <span id={reorder.instructionsId} className="stratum-visually-hidden">
        {reorder.instructions}
      </span>

      <ol
        // Safari removes list semantics from a list with `list-style: none`,
        // which is exactly the list an operator most needs counted.
        role="list"
        className="stratum-row-list stratum-priority-list__list"
        aria-label={label}
        aria-describedby={semanticsId}
        data-dragging={reorder.dragging || undefined}
      >
        {entries.map((entry, index) => {
          const rowProps = reorder.getRowProps(index);
          const enabled = entry.enabled ?? true;
          const match = entry.match ?? 'unknown';
          const shadowed = entry.shadowedBy ?? [];
          const name = nameOf(entry, index);
          const state = entry.state ?? 'clean';

          return (
            <li
              {...rowProps}
              key={entry.id}
              className="stratum-row-list__row stratum-priority-list__row"
              aria-label={labelRow(index + 1, total, entryName(entry))}
              data-disabled={!enabled || undefined}
              data-shadowed={shadowed.length > 0 || undefined}
              data-state={state}
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
                <div className="stratum-priority-list__label">{entry.label}</div>
                {entry.description != null && entry.description !== false && (
                  <div className="stratum-priority-list__description">{entry.description}</div>
                )}
                {entry.detail}
                {shadowed.length > 0 && (
                  <p className="stratum-row-list__note" data-tone="warning">
                    {labelShadowed([...shadowed])}
                  </p>
                )}
              </div>

              <div className="stratum-priority-list__meta">
                {state !== 'clean' && (
                  <Tag size="sm" variant={state === 'pending' ? 'info' : 'warning'} outline>
                    {state === 'pending' ? labelPending : labelDirty}
                  </Tag>
                )}

                <Tag
                  size="sm"
                  variant={MATCH_VARIANT[match]}
                  dot={match !== 'unknown'}
                  outline={match === 'unknown'}
                >
                  {match === 'matched'
                    ? labelMatched
                    : match === 'evaluated'
                      ? labelEvaluated
                      : match === 'unreachable'
                        ? labelUnreachable
                        : labelUnknownMatch}
                </Tag>

                {showHitColumn && (
                  <span
                    className="stratum-priority-list__hits"
                    data-unobserved={entry.hitCount == null || undefined}
                    data-stale={stale || undefined}
                    title={entry.hitCount == null ? labelNoHits : undefined}
                  >
                    {entry.hitCount == null ? UNOBSERVED : labelHits(entry.hitCount)}
                  </span>
                )}
              </div>

              <div className="stratum-row-list__actions">
                {onToggleEnabled && (
                  <Switch
                    size="sm"
                    checked={enabled}
                    aria-label={labelEnabled(name)}
                    onCheckedChange={(next) => {
                      onToggleEnabled(entry.id, next);
                      announce(announceToggled(name, next));
                    }}
                  />
                )}
                {onRemove && (
                  <button
                    type="button"
                    ref={register(`remove:${entry.id}`)}
                    className="stratum-row-list__action"
                    data-tone="danger"
                    aria-label={labelRemove(name)}
                    disabled={entry.locked}
                    onClick={() => handleRemove(entry, index)}
                  >
                    {RemoveIcon}
                  </button>
                )}
              </div>
            </li>
          );
        })}
      </ol>

      {onAdd && (
        <AddRowButton
          label={labelAdd}
          buttonRef={register('add')}
          disabled={atCeiling}
          disabledReason={maxEntries !== undefined ? labelMaxReached(maxEntries) : undefined}
          onClick={() => {
            onAdd();
            announce(announceAdded(total + 1));
          }}
        />
      )}

      <ListAnnouncer message={message} />
    </div>
  );
});
