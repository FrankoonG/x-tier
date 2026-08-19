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
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { InlineMessage } from '../../components/InlineMessage/InlineMessage';
import { Switch } from '../../components/Switch/Switch';
import { Tag } from '../../components/Tag/Tag';
import { useEventCallback } from '../../hooks/useEventCallback';
import { AddressInput } from '../AddressInput/AddressInput';
import {
  ALL_ADDRESS_KINDS,
  parseAddress,
  type AddressKind,
  type AddressOptions,
} from '../AddressInput/address';
import { StatusDot, type StatusDotStatus } from '../StatusDot/StatusDot';
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
import './AddressList.css';

export interface AddressEntry {
  id: string;
  value: string;
  /** Default `true`. A disabled address stays configured but is not used. */
  enabled?: boolean;
  /**
   * Reachability of this specific address. Defaults to `unknown`, which means
   * NOT PROBED — never draw an unmeasured address as a healthy one.
   */
  status?: StatusDotStatus;
  /** Accessible name for the status dot, e.g. "reachable, 24 ms". */
  statusLabel?: string;
  /** Extra fact shown beside the address — latency, last seen, interface. */
  detail?: ReactNode;
  /** Cannot be moved or removed. */
  locked?: boolean;
}

export interface AddressListValidity {
  valid: boolean;
  invalidIds: string[];
  emptyIds: string[];
  duplicateIds: string[];
}

export interface AddressListProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'onPaste'>,
    AddressOptions {
  entries: readonly AddressEntry[];
  onChange: (entries: AddressEntry[]) => void;
  /** Shapes each address may take. Default: all six. */
  accept?: readonly AddressKind[];

  /**
   * Id of the preferred address, or `null` for none. Omit the prop entirely to
   * hide the primary control — a set of equal addresses has no primary, and a
   * marker that always picks row 0 invents a meaning the data never had.
   */
  primaryId?: string | null;
  onPrimaryChange?: (id: string) => void;

  reorderable?: boolean;
  showIndex?: boolean;
  showEdgeMoves?: boolean;
  minEntries?: number;
  maxEntries?: number;
  /** Flag repeated addresses. Default `'warn'`. Never silently de-duplicates. */
  dedupe?: 'warn' | 'off';
  /** Suppress per-row errors until the row has been blurred. Default `true`. */
  validateOnBlurOnly?: boolean;
  /** Reveal every error regardless of blur — set this after a failed submit. */
  showAllErrors?: boolean;
  /** Rewrite each address to its canonical form on blur. Default `false`. */
  normalizeOnBlur?: boolean;
  /** Treat a blank row as acceptable. Default `false`. */
  allowEmpty?: boolean;
  /** Enter in a row inserts a sibling below it. Default `true`. */
  enterInsertsRow?: boolean;
  /** Backspace in an empty row removes it. Default `true`. */
  backspaceRemovesRow?: boolean;
  size?: 'sm' | 'md' | 'lg';
  /** Accessible name for the list. */
  label?: string;
  placeholder?: string;

  onValidityChange?: (validity: AddressListValidity) => void;
  /** Fires after a multi-value paste expanded into rows. */
  onBulkPaste?: (result: { added: number; values: string[] }) => void;

  labelRow?: (position: number, total: number) => string;
  labelAdd?: string;
  labelRemove?: (position: number, total: number) => string;
  labelEnabled?: (position: number, total: number) => string;
  labelPrimary?: string;
  labelMakePrimary?: (position: number, total: number) => string;
  labelDuplicate?: (position: number) => string;
  labelSummary?: (bad: number, total: number) => string;
  labelDuplicateSummary?: (count: number) => string;
  labelMaxReached?: (max: number) => string;
  labelMinReached?: (min: number) => string;
  labelUnknownStatus?: string;
  announceAdded?: (total: number) => string;
  announceRemoved?: (position: number, remaining: number) => string;
  announcePasted?: (added: number, invalid: number) => string;
  announcePrimary?: (position: number) => string;
  reorderLabels?: Partial<ReorderLabels>;
}

/**
 * An ordered, paste-aware, per-row-validated list of network addresses.
 *
 * VALIDATION IS THE EXISTING PARSER, NOT NEW REGEXES
 * --------------------------------------------------
 * Every row is checked with `parseAddress` from `AddressInput/address.ts` —
 * the same code path, the same option set, the same error codes. Address
 * validation written twice in one product always diverges, and the weaker copy
 * is invariably the one guarding the more dangerous field.
 *
 * A PASTED BLOCK BECOMES ROWS
 * ---------------------------
 * Operator data arrives as a block: a column out of a spreadsheet, a comma list
 * out of a config file. Pasting one into a single-value field and watching it
 * turn red is the most common complaint about editors of this shape. Here a
 * paste containing more than one value fills the current row with the first and
 * inserts rows for the rest. Fragments that do not parse still become rows —
 * marked invalid, editable in place — because dropping what someone pasted is
 * worse than showing them what failed.
 *
 * NOTHING IS SILENTLY DROPPED OR MERGED
 * -------------------------------------
 * Blank rows are reported rather than filtered away at save time, duplicates are
 * flagged rather than removed (a repeated address is occasionally deliberate),
 * and `onValidityChange` reports the true state on every transition so the form
 * can block submission. Validation the operator can ignore is worse than none,
 * because it teaches them that red means nothing.
 *
 * KEYBOARD
 * --------
 * Enter inserts a row below and focuses it; Backspace in an empty row removes it
 * and puts focus in the previous row; Alt+ArrowUp/Down reorders; the grip
 * supports lift/drop. Removing a row NEVER drops focus to `<body>` — it goes to
 * the same field in the next row, else the previous, else the add plate.
 */
export const AddressList = forwardRef<HTMLDivElement, AddressListProps>(function AddressList(
  {
    entries,
    onChange,
    accept = ALL_ADDRESS_KINDS,
    primaryId,
    onPrimaryChange,
    reorderable = true,
    showIndex = false,
    showEdgeMoves = false,
    minEntries = 1,
    maxEntries,
    dedupe = 'warn',
    validateOnBlurOnly = true,
    showAllErrors = false,
    normalizeOnBlur = false,
    allowEmpty = false,
    enterInsertsRow = true,
    backspaceRemovesRow = true,
    size = 'md',
    label,
    placeholder,
    onValidityChange,
    onBulkPaste,
    labelRow = (position, total) => `Address ${position} of ${total}`,
    labelAdd = 'Add address',
    labelRemove = (position, total) => `Remove address ${position} of ${total}`,
    labelEnabled = (position, total) => `Use address ${position} of ${total}`,
    labelPrimary = 'Primary',
    labelMakePrimary = (position, total) => `Make address ${position} of ${total} the primary`,
    labelDuplicate = (position) => `Same address as row ${position}.`,
    labelSummary = (bad, total) => `${bad} of ${total} addresses need attention.`,
    labelDuplicateSummary = (count) =>
      count === 1 ? '1 address is repeated.' : `${count} addresses are repeated.`,
    labelMaxReached = (max) => `Limit of ${max} addresses reached.`,
    labelMinReached = (min) => `At least ${min} ${min === 1 ? 'address' : 'addresses'} required.`,
    labelUnknownStatus = 'not probed',
    announceAdded = (total) => `Address added. ${total} addresses.`,
    announceRemoved = (position, remaining) =>
      `Address ${position} removed. ${remaining} ${remaining === 1 ? 'address' : 'addresses'} remain.`,
    announcePasted = (added, invalid) =>
      invalid > 0
        ? `${added} addresses added, ${invalid} need attention.`
        : `${added} addresses added.`,
    announcePrimary = (position) => `Address ${position} is now the primary.`,
    reorderLabels,
    className,
    allowUnderscore,
    allowTrailingDot,
    requireMultiLabel,
    allowNumericTld,
    allowZone,
    allowPortZero,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const total = entries.length;
  const { message, announce } = useAnnouncer();
  const { register, requestFocus } = useFocusRelay();
  const labels = useReorderLabels(reorderLabels);
  const [touched, setTouched] = useState<ReadonlySet<string>>(() => new Set());

  const parseOptions = useMemo<AddressOptions>(
    () => ({
      accept,
      allowUnderscore,
      allowTrailingDot,
      requireMultiLabel,
      allowNumericTld,
      allowZone,
      allowPortZero,
    }),
    [
      accept,
      allowUnderscore,
      allowTrailingDot,
      requireMultiLabel,
      allowNumericTld,
      allowZone,
      allowPortZero,
    ],
  );

  /* -- One parse pass, shared by the rows, the summary and the report ------ */
  const analysis = useMemo(() => {
    const seen = new Map<string, number>();
    return entries.map((entry, index) => {
      const trimmed = entry.value.trim();
      if (trimmed === '') {
        return { empty: true, ok: false, key: '', duplicateOf: -1 };
      }
      const result = parseAddress(trimmed, parseOptions);
      // Compare on the CANONICAL form, so `2001:DB8::1` and `2001:db8:0::1`
      // are recognised as the same host rather than as two.
      const key = result.ok ? result.normalized : trimmed.toLowerCase();
      const first = seen.get(key);
      if (first === undefined) seen.set(key, index);
      return {
        empty: false,
        ok: result.ok,
        key,
        duplicateOf: first === undefined ? -1 : first,
      };
    });
  }, [entries, parseOptions]);

  const validity = useMemo<AddressListValidity>(() => {
    const invalidIds: string[] = [];
    const emptyIds: string[] = [];
    const duplicateIds: string[] = [];
    entries.forEach((entry, index) => {
      const a = analysis[index];
      if (!a) return;
      if (a.empty) emptyIds.push(entry.id);
      else if (!a.ok) invalidIds.push(entry.id);
      if (dedupe === 'warn' && a.duplicateOf >= 0) duplicateIds.push(entry.id);
    });
    return {
      valid: invalidIds.length === 0 && (allowEmpty || emptyIds.length === 0),
      invalidIds,
      emptyIds,
      duplicateIds,
    };
  }, [entries, analysis, dedupe, allowEmpty]);

  // Transitions only. A callback firing on every keystroke turns a form with
  // several of these into a re-render storm.
  const emitValidity = useEventCallback(onValidityChange);
  const lastSignature = useRef<string | null>(null);
  useEffect(() => {
    const signature = `${validity.valid}|${validity.invalidIds.join(',')}|${validity.emptyIds.join(',')}|${validity.duplicateIds.join(',')}`;
    if (lastSignature.current === signature) return;
    lastSignature.current = signature;
    emitValidity(validity);
  }, [validity, emitValidity]);

  /* -- Mutations ----------------------------------------------------------- */

  const commit = useCallback((next: AddressEntry[]) => onChange(next), [onChange]);

  const setValue = useCallback(
    (index: number, value: string) => {
      const next = entries.slice();
      const entry = next[index];
      if (!entry) return;
      next[index] = { ...entry, value };
      commit(next);
    },
    [entries, commit],
  );

  const insertAt = useCallback(
    (index: number, values: string[] = ['']) => {
      const created = values.map((value) => ({ id: makeRowId(`addr`), value }));
      const next = entries.slice();
      next.splice(index, 0, ...created);
      commit(next);
      const last = created[created.length - 1];
      if (last) requestFocus(`field:${last.id}`);
      return created;
    },
    [entries, commit, requestFocus],
  );

  const removeAt = useCallback(
    (index: number) => {
      if (total <= minEntries) return;
      const next = entries.slice();
      const [removed] = next.splice(index, 1);
      // Same field, next row; else previous row; else the add plate.
      const target = next[index] ?? next[index - 1];
      if (target) requestFocus(`field:${target.id}`);
      else requestFocus('add');
      commit(next);
      announce(announceRemoved(index + 1, next.length));
      return removed;
    },
    [entries, total, minEntries, commit, requestFocus, announce, announceRemoved],
  );

  const handleMove = useCallback(
    (from: number, to: number) => commit(moveItem(entries, from, to)),
    [entries, commit],
  );

  const reorderEnabled = reorderable && total > 1;
  const reorder = useReorder({
    count: total,
    onMove: handleMove,
    announce,
    labels,
    disabled: !reorderEnabled,
  });

  /* -- Paste --------------------------------------------------------------- */

  const handlePaste = (index: number, event: ReactClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData('text');
    if (!text || !looksLikeBlock(text)) return;
    event.preventDefault();

    const parts = splitPastedBlock(text);
    const [first, ...remainder] = parts;
    const created = remainder.map((value) => ({ id: makeRowId('addr'), value }));

    const next = entries.slice();
    const current = next[index];
    if (!current) return;
    next[index] = { ...current, value: first ?? '' };
    next.splice(index + 1, 0, ...created);

    const capped = maxEntries !== undefined ? next.slice(0, maxEntries) : next;
    commit(capped);

    // Everything pasted is now visible, so every row is answerable — which is
    // why the whole block is marked touched rather than waiting for a blur that
    // will never come on rows the operator did not type into.
    setTouched((prev) => {
      const set = new Set(prev);
      for (const row of capped.slice(index)) set.add(row.id);
      return set;
    });

    // What actually landed, which is fewer than what was pasted once the
    // ceiling truncates the block. Announcing the optimistic number would be a
    // small lie at exactly the moment rows went missing.
    const placed = Math.min(parts.length, capped.length - index);
    const kept = parts.slice(0, placed);
    const invalid = kept.filter((value) => !parseAddress(value, parseOptions).ok).length;
    const last = capped[Math.min(index + created.length, capped.length - 1)];
    if (last) requestFocus(`field:${last.id}`);
    announce(announcePasted(placed, invalid));
    onBulkPaste?.({ added: placed, values: kept });
  };

  /* -- Row keyboard -------------------------------------------------------- */

  const handleKeyDown = (index: number, event: ReactKeyboardEvent<HTMLInputElement>) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const entry = entries[index];
    if (!entry) return;

    if (enterInsertsRow && event.key === 'Enter') {
      if (maxEntries !== undefined && total >= maxEntries) return;
      // Deliberately swallows the implicit form submit: in a list this size,
      // Enter meaning "next row" is what every operator reaches for, and a
      // submit fired halfway through entering twelve addresses is destructive.
      event.preventDefault();
      insertAt(index + 1);
      announce(announceAdded(total + 1));
      return;
    }

    if (
      backspaceRemovesRow &&
      event.key === 'Backspace' &&
      entry.value === '' &&
      total > minEntries
    ) {
      event.preventDefault();
      // Focus lands in the PREVIOUS row here rather than the next one: the
      // operator is deleting backwards, so backwards is where they are looking.
      const prev = entries[index - 1] ?? entries[index + 1];
      const next = entries.slice();
      next.splice(index, 1);
      commit(next);
      if (prev) requestFocus(`field:${prev.id}`);
      else requestFocus('add');
      announce(announceRemoved(index + 1, next.length));
    }
  };

  /* -- Summaries ----------------------------------------------------------- */

  const revealed = (id: string) => showAllErrors || !validateOnBlurOnly || touched.has(id);
  const problemCount = entries.filter((entry, index) => {
    const a = analysis[index];
    if (!a || !revealed(entry.id)) return false;
    return !a.ok && (!a.empty || !allowEmpty);
  }).length;
  const duplicateCount = validity.duplicateIds.length;

  const showStatusColumn = entries.some((entry) => entry.status !== undefined);
  const showPrimary = primaryId !== undefined && typeof onPrimaryChange === 'function';
  const atCeiling = maxEntries !== undefined && total >= maxEntries;
  const atFloor = total <= minEntries;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="address-list"
      data-size={size}
      data-dragging={reorder.dragging || undefined}
      className={clsx('stratum-address-list', className)}
    >
      {problemCount > 0 && (
        <InlineMessage variant="danger" size="xs" role="status">
          {labelSummary(problemCount, total)}
        </InlineMessage>
      )}
      {duplicateCount > 0 && (
        <InlineMessage variant="warning" size="xs" role="none">
          {labelDuplicateSummary(duplicateCount)}
        </InlineMessage>
      )}

      <span id={reorder.instructionsId} className="stratum-visually-hidden">
        {reorder.instructions}
      </span>

      <ol
        role="list"
        className="stratum-row-list stratum-address-list__list"
        aria-label={label}
        data-dragging={reorder.dragging || undefined}
      >
        {entries.map((entry, index) => {
          const rowProps = reorder.getRowProps(index);
          const a = analysis[index];
          const enabled = entry.enabled ?? true;
          const isPrimary = showPrimary && primaryId === entry.id;
          const duplicateOf = dedupe === 'warn' ? (a?.duplicateOf ?? -1) : -1;
          const dupId = `${uid}dup${index}`;

          return (
            <li
              {...rowProps}
              key={entry.id}
              className="stratum-row-list__row stratum-address-list__row"
              aria-label={labelRow(index + 1, total)}
              data-disabled={!enabled || undefined}
              data-primary={isPrimary || undefined}
              data-duplicate={duplicateOf >= 0 || undefined}
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
                <AddressInput
                  ref={register(`field:${entry.id}`)}
                  size={size}
                  accept={accept}
                  value={entry.value}
                  onChange={(value) => setValue(index, value)}
                  aria-label={labelRow(index + 1, total)}
                  aria-describedby={duplicateOf >= 0 ? dupId : undefined}
                  placeholder={placeholder}
                  normalizeOnBlur={normalizeOnBlur}
                  validateOnBlurOnly={validateOnBlurOnly && !showAllErrors}
                  allowUnderscore={allowUnderscore}
                  allowTrailingDot={allowTrailingDot}
                  requireMultiLabel={requireMultiLabel}
                  allowNumericTld={allowNumericTld}
                  allowZone={allowZone}
                  allowPortZero={allowPortZero}
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
                {duplicateOf >= 0 && (
                  <p className="stratum-row-list__note" data-tone="warning" id={dupId}>
                    {labelDuplicate(duplicateOf + 1)}
                  </p>
                )}
              </div>

              {showStatusColumn && (
                <span className="stratum-address-list__status">
                  <StatusDot
                    size="sm"
                    status={entry.status ?? 'unknown'}
                    label={entry.statusLabel ?? labelUnknownStatus}
                  />
                  {entry.detail != null && entry.detail !== false && (
                    <span className="stratum-address-list__detail">{entry.detail}</span>
                  )}
                </span>
              )}

              <div className="stratum-row-list__actions">
                {showPrimary && (
                  <button
                    type="button"
                    className="stratum-address-list__primary"
                    aria-pressed={isPrimary}
                    aria-label={labelMakePrimary(index + 1, total)}
                    onClick={() => {
                      if (isPrimary) return;
                      onPrimaryChange?.(entry.id);
                      announce(announcePrimary(index + 1));
                    }}
                  >
                    <Tag size="sm" variant={isPrimary ? 'accent' : 'neutral'} outline={!isPrimary}>
                      {labelPrimary}
                    </Tag>
                  </button>
                )}

                <Switch
                  size="sm"
                  checked={enabled}
                  aria-label={labelEnabled(index + 1, total)}
                  onCheckedChange={(next) => {
                    const list = entries.slice();
                    const target = list[index];
                    if (!target) return;
                    list[index] = { ...target, enabled: next };
                    commit(list);
                  }}
                />

                <button
                  type="button"
                  className="stratum-row-list__action"
                  data-tone="danger"
                  aria-label={labelRemove(index + 1, total)}
                  // Disabled rather than hidden at the floor: a control that
                  // disappears mid-interaction teaches nothing about the limit.
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
});
