import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { VisuallyHidden } from '../../primitives/VisuallyHidden';
import { SearchInput } from '../SearchInput/SearchInput';
import './TransferList.css';

/* -------------------------------------------------------------------------- */
/* Types                                                                       */
/* -------------------------------------------------------------------------- */

export interface TransferListOption {
  value: string;
  label: string;
  /** Secondary line. Also searched by the default filter. */
  description?: string;
  /** Cannot be transferred in either direction. */
  disabled?: boolean;
}

export type TransferListSize = 'sm' | 'md';

export interface TransferListProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue' | 'children'> {
  options: readonly TransferListOption[];
  /** Values in the selected pane, in pane order. */
  value?: string[];
  defaultValue?: string[];
  onChange?: (values: string[]) => void;

  searchable?: boolean;
  filter?: (option: TransferListOption, query: string) => boolean;

  /**
   * Adds move up / move down to the selected pane.
   *
   * Only turn this on when order in the selected pane MEANS something — a
   * fallback chain, an evaluation order. Otherwise it is a control that
   * invites the user to fiddle with something that has no effect.
   */
  reorderable?: boolean;

  size?: TransferListSize;
  disabled?: boolean;
  /** Height of each list body. A number is treated as pixels. */
  listHeight?: string | number;

  /* -- Copy --------------------------------------------------------------- */
  availableLabel?: string;
  selectedLabel?: string;
  searchAvailablePlaceholder?: string;
  searchSelectedPlaceholder?: string;
  labelSearchAvailable?: string;
  labelSearchSelected?: string;
  labelMoveRight?: string;
  labelMoveLeft?: string;
  labelMoveAllRight?: string;
  labelMoveAllLeft?: string;
  labelSelectAll?: string;
  labelClearMarks?: string;
  labelMoveUp?: string;
  labelMoveDown?: string;
  labelCount?: (shown: number, total: number) => string;
  emptyAvailableLabel?: string;
  emptySelectedLabel?: string;
  labelTransferred?: (count: number, destination: string) => string;
  labelReordered?: (label: string, position: number, total: number) => string;
  labelReorderFiltered?: string;
  /** Announced when a transfer control is activated with nothing marked. */
  labelNothingMarked?: string;
  labelHelp?: string;
}

/* -------------------------------------------------------------------------- */
/* Helpers                                                                     */
/* -------------------------------------------------------------------------- */

function defaultFilter(option: TransferListOption, query: string): boolean {
  if (query === '') return true;
  const needle = query.toLowerCase();
  return (
    option.label.toLowerCase().includes(needle) ||
    option.value.toLowerCase().includes(needle) ||
    (option.description?.toLowerCase().includes(needle) ?? false)
  );
}

function toggleIn(list: readonly string[], value: string): string[] {
  return list.includes(value) ? list.filter((item) => item !== value) : [...list, value];
}

const ChevronRight = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m6.5 4 4 4-4 4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const ChevronLeft = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m9.5 4-4 4 4 4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const ChevronsRight = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m3.5 4 4 4-4 4M9 4l4 4-4 4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const ChevronsLeft = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m12.5 4-4 4 4 4M7 4 3 8l4 4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const ArrowUp = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="M8 12.5v-9m0 0L4.5 7M8 3.5 11.5 7" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const ArrowDown = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="M8 3.5v9m0 0L4.5 9M8 12.5 11.5 9" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

/* -------------------------------------------------------------------------- */
/* Pane                                                                        */
/* -------------------------------------------------------------------------- */

interface PaneProps {
  paneId: string;
  title: string;
  shown: TransferListOption[];
  total: number;
  marked: readonly string[];
  activeValue: string | null;
  query: string;
  searchable: boolean;
  searchPlaceholder: string | undefined;
  searchLabel: string;
  disabled: boolean;
  emptyLabel: string;
  labelSelectAll: string;
  labelClearMarks: string;
  labelCount: (shown: number, total: number) => string;
  /** Arrow key that sends marked options to the other pane. */
  transferKey: 'ArrowRight' | 'ArrowLeft';
  describedBy: string;
  onQueryChange: (query: string) => void;
  onMarkedChange: (values: string[]) => void;
  onActiveChange: (value: string | null) => void;
  onTransfer: (values: string[]) => void;
  onReorder?: ((delta: -1 | 1) => void) | undefined;
  reorderEnabled?: boolean;
}

function TransferPane({
  paneId,
  title,
  shown,
  total,
  marked,
  activeValue,
  query,
  searchable,
  searchPlaceholder,
  searchLabel,
  disabled,
  emptyLabel,
  labelSelectAll,
  labelClearMarks,
  labelCount,
  transferKey,
  describedBy,
  onQueryChange,
  onMarkedChange,
  onActiveChange,
  onTransfer,
  onReorder,
  reorderEnabled = false,
}: PaneProps) {
  const listRef = useRef<HTMLUListElement | null>(null);
  const anchorRef = useRef<string | null>(null);
  const titleId = `${paneId}title`;

  const activeIndex = useMemo(() => {
    const index = shown.findIndex((option) => option.value === activeValue);
    if (index >= 0) return index;
    return shown.length > 0 ? 0 : -1;
  }, [shown, activeValue]);

  useEffect(() => {
    if (activeIndex < 0) return;
    const nodes = listRef.current?.querySelectorAll<HTMLElement>('[role="option"]');
    nodes?.[activeIndex]?.scrollIntoView({ block: 'nearest' });
  }, [activeIndex, shown]);

  const enabledValues = useMemo(
    () => shown.filter((option) => !option.disabled).map((option) => option.value),
    [shown],
  );

  const markRange = useCallback(
    (to: string) => {
      const from = anchorRef.current ?? to;
      const a = shown.findIndex((option) => option.value === from);
      const b = shown.findIndex((option) => option.value === to);
      if (a < 0 || b < 0) {
        onMarkedChange([to]);
        return;
      }
      const [start, end] = a <= b ? [a, b] : [b, a];
      const range = shown
        .slice(start, end + 1)
        .filter((option) => !option.disabled)
        .map((option) => option.value);
      onMarkedChange(range);
    },
    [shown, onMarkedChange],
  );

  const moveActive = (index: number, extend: boolean) => {
    const clamped = Math.max(0, Math.min(index, shown.length - 1));
    const option = shown[clamped];
    if (!option) return;
    onActiveChange(option.value);
    if (extend) markRange(option.value);
    else anchorRef.current = option.value;
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLUListElement>) => {
    if (disabled || shown.length === 0) return;

    if (reorderEnabled && onReorder && event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      event.preventDefault();
      onReorder(event.key === 'ArrowUp' ? -1 : 1);
      return;
    }

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveActive(activeIndex + 1, event.shiftKey);
        return;
      case 'ArrowUp':
        event.preventDefault();
        moveActive(activeIndex - 1, event.shiftKey);
        return;
      case 'Home':
        event.preventDefault();
        moveActive(0, event.shiftKey);
        return;
      case 'End':
        event.preventDefault();
        moveActive(shown.length - 1, event.shiftKey);
        return;
      case ' ':
      case 'Spacebar': {
        event.preventDefault();
        const option = shown[activeIndex];
        if (!option || option.disabled) return;
        anchorRef.current = option.value;
        onMarkedChange(toggleIn(marked, option.value));
        return;
      }
      case 'Enter':
      case transferKey: {
        event.preventDefault();
        if (marked.length > 0) {
          onTransfer([...marked]);
          return;
        }
        const option = shown[activeIndex];
        if (option && !option.disabled) onTransfer([option.value]);
        return;
      }
      case 'a':
      case 'A':
        if (!event.ctrlKey && !event.metaKey) return;
        event.preventDefault();
        onMarkedChange(enabledValues);
        return;
      case 'Escape':
        if (marked.length === 0) return;
        event.preventDefault();
        event.stopPropagation();
        onMarkedChange([]);
        return;
      default:
        return;
    }
  };

  const handleOptionClick = (option: TransferListOption) => (event: ReactMouseEvent<HTMLLIElement>) => {
    if (disabled || option.disabled) return;
    onActiveChange(option.value);
    if (event.shiftKey) {
      markRange(option.value);
      return;
    }
    anchorRef.current = option.value;
    if (event.ctrlKey || event.metaKey) {
      onMarkedChange(toggleIn(marked, option.value));
      return;
    }
    onMarkedChange(marked.length === 1 && marked[0] === option.value ? [] : [option.value]);
  };

  return (
    <section className="stratum-transfer-list__pane" aria-labelledby={titleId}>
      <header className="stratum-transfer-list__pane-header">
        <h3 className="stratum-transfer-list__pane-title" id={titleId}>
          {title}
        </h3>
        <span className="stratum-transfer-list__pane-count">{labelCount(shown.length, total)}</span>
      </header>

      {searchable && (
        <SearchInput
          size="sm"
          fullWidth
          value={query}
          onValueChange={onQueryChange}
          disabled={disabled}
          aria-label={searchLabel}
          {...(searchPlaceholder === undefined ? {} : { placeholder: searchPlaceholder })}
        />
      )}

      <ul
        ref={listRef}
        role="listbox"
        aria-multiselectable="true"
        aria-labelledby={titleId}
        aria-describedby={describedBy}
        aria-activedescendant={
          activeIndex >= 0 && shown.length > 0 ? `${paneId}opt${activeIndex}` : undefined
        }
        aria-disabled={disabled || undefined}
        tabIndex={disabled ? -1 : 0}
        className="stratum-transfer-list__options stratum-focus-inset"
        onKeyDown={handleKeyDown}
      >
        {shown.length === 0 ? (
          <li className="stratum-transfer-list__empty" role="presentation">
            {emptyLabel}
          </li>
        ) : (
          shown.map((option, index) => (
            <li
              key={option.value}
              id={`${paneId}opt${index}`}
              role="option"
              aria-selected={marked.includes(option.value)}
              aria-disabled={option.disabled || undefined}
              data-active={index === activeIndex || undefined}
              className="stratum-transfer-list__option"
              onClick={handleOptionClick(option)}
              onDoubleClick={() => {
                if (!disabled && !option.disabled) onTransfer([option.value]);
              }}
            >
              <span className="stratum-transfer-list__option-label">{option.label}</span>
              {option.description != null && option.description !== '' && (
                <span className="stratum-transfer-list__option-description">
                  {option.description}
                </span>
              )}
            </li>
          ))
        )}
      </ul>

      <footer className="stratum-transfer-list__pane-footer">
        <button
          type="button"
          className="stratum-transfer-list__link"
          disabled={disabled || enabledValues.length === 0}
          onClick={() => onMarkedChange(enabledValues)}
        >
          {labelSelectAll}
        </button>
        <button
          type="button"
          className="stratum-transfer-list__link"
          disabled={disabled || marked.length === 0}
          onClick={() => onMarkedChange([])}
        >
          {labelClearMarks}
        </button>
      </footer>
    </section>
  );
}

/* -------------------------------------------------------------------------- */
/* Component                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * Two panes, and a set of things moved between them.
 *
 * WHEN TO REACH FOR THIS
 * ----------------------
 * Rarely. A filtered multi-select committing to chips sits on far
 * better-supported primitives and is the right answer for plain membership.
 * A transfer list earns its cost in one case: when the ORDER of the selected
 * pane also carries meaning — a fallback chain, an evaluation order — because
 * that is the thing a chip field cannot express. Hence `reorderable`.
 *
 * INTERACTION
 * -----------
 * Marking and membership are separate. Marking is a staging state inside one
 * pane (`aria-selected` on the option); membership is which pane an option is
 * in. Conflating them is why these widgets so often announce the wrong thing.
 *
 *   - click marks one, Ctrl/Cmd+click toggles, Shift+click marks a range from
 *     the anchor — the gesture whose absence turns "select rows 3 to 40" into
 *     thirty-eight clicks;
 *   - Arrow keys move the active option, Shift+Arrow extends the range,
 *     Space toggles one, Ctrl/Cmd+A marks everything currently shown;
 *   - Enter, or the arrow pointing at the other pane, transfers;
 *   - double-click transfers one;
 *   - Escape clears the marks and stops there, rather than closing whatever
 *     dialog the list happens to be inside.
 *
 * SELECT-ALL MEANS SELECT-ALL-SHOWN
 * ---------------------------------
 * With a filter active, "select all" that reached past the filter would be a
 * trap: what the user can see is what they think they are acting on. Both the
 * footer control and Ctrl+A operate on the filtered list, and the count in
 * the header always names both numbers so the difference is visible.
 *
 * ACCESSIBILITY
 * -------------
 * A listbox's options may not contain interactive children, so the transfer
 * controls sit outside both lists — which means `<label for>` cannot reach
 * them and every control is named by `aria-labelledby` or `aria-label`
 * instead. Each pane is a titled `<section>`, so a screen reader user always
 * knows which side they are on, and every transfer and reorder is reported
 * through one polite live region with the resulting counts.
 */
export const TransferList = forwardRef<HTMLDivElement, TransferListProps>(function TransferList(
  {
    options,
    value: valueProp,
    defaultValue,
    onChange,
    searchable = true,
    filter = defaultFilter,
    reorderable = false,
    size = 'md',
    disabled = false,
    listHeight = '16rem',
    availableLabel = 'Available',
    selectedLabel = 'Selected',
    searchAvailablePlaceholder = 'Search',
    searchSelectedPlaceholder = 'Search',
    labelSearchAvailable = 'Search available items',
    labelSearchSelected = 'Search selected items',
    labelMoveRight = 'Move marked items to selected',
    labelMoveLeft = 'Move marked items to available',
    labelMoveAllRight = 'Move all shown items to selected',
    labelMoveAllLeft = 'Move all shown items to available',
    labelSelectAll = 'Mark all shown',
    labelClearMarks = 'Clear marks',
    labelMoveUp = 'Move marked items up',
    labelMoveDown = 'Move marked items down',
    labelCount,
    emptyAvailableLabel = 'Nothing available.',
    emptySelectedLabel = 'Nothing selected yet.',
    labelTransferred,
    labelReordered,
    labelReorderFiltered = 'Clear the search before reordering.',
    labelNothingMarked = 'Nothing is marked. Use Space to mark an item.',
    labelHelp = 'Use the arrow keys to move, Space to mark, Enter to transfer.',
    className,
    style,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const helpId = `${uid}help`;

  const [selected, setSelected] = useControllableState<string[]>({
    value: valueProp,
    defaultValue: defaultValue ?? [],
    onChange,
  });

  const [availableQuery, setAvailableQuery] = useState('');
  const [selectedQuery, setSelectedQuery] = useState('');
  const [markedAvailable, setMarkedAvailable] = useState<string[]>([]);
  const [markedSelected, setMarkedSelected] = useState<string[]>([]);
  const [activeAvailable, setActiveAvailable] = useState<string | null>(null);
  const [activeSelected, setActiveSelected] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState('');

  const byValue = useMemo(
    () => new Map(options.map((option) => [option.value, option])),
    [options],
  );
  const selectedSet = useMemo(() => new Set(selected), [selected]);

  const availableAll = useMemo(
    () => options.filter((option) => !selectedSet.has(option.value)),
    [options, selectedSet],
  );
  const selectedAll = useMemo(() => {
    const list: TransferListOption[] = [];
    for (const value of selected) {
      const option = byValue.get(value);
      if (option) list.push(option);
    }
    return list;
  }, [selected, byValue]);

  const availableShown = useMemo(
    () => availableAll.filter((option) => filter(option, availableQuery)),
    [availableAll, availableQuery, filter],
  );
  const selectedShown = useMemo(
    () => selectedAll.filter((option) => filter(option, selectedQuery)),
    [selectedAll, selectedQuery, filter],
  );

  // Marks are a staging state, and a mark on something no longer in the pane
  // would make the transfer button act on an invisible item.
  useEffect(() => {
    setMarkedAvailable((current) =>
      current.every((value) => !selectedSet.has(value) && byValue.has(value))
        ? current
        : current.filter((value) => !selectedSet.has(value) && byValue.has(value)),
    );
    setMarkedSelected((current) =>
      current.every((value) => selectedSet.has(value))
        ? current
        : current.filter((value) => selectedSet.has(value)),
    );
  }, [selectedSet, byValue]);

  const count = useCallback(
    (shown: number, total: number) =>
      labelCount ? labelCount(shown, total) : shown === total ? `${total}` : `${shown} of ${total}`,
    [labelCount],
  );

  const announceTransfer = useCallback(
    (moved: number, destination: string) => {
      setAnnouncement(
        labelTransferred
          ? labelTransferred(moved, destination)
          : `${moved} moved to ${destination}. ${availableAll.length - moved} available, ${
              selected.length + moved
            } selected.`,
      );
    },
    [availableAll.length, labelTransferred, selected.length],
  );

  const toSelected = useCallback(
    (values: readonly string[]) => {
      if (disabled) return;
      const additions = values.filter(
        (value) => !selectedSet.has(value) && byValue.get(value)?.disabled !== true,
      );
      if (additions.length === 0) return;
      setSelected([...selected, ...additions]);
      setMarkedAvailable([]);
      announceTransfer(additions.length, selectedLabel);
    },
    [announceTransfer, byValue, disabled, selected, selectedLabel, selectedSet, setSelected],
  );

  const toAvailable = useCallback(
    (values: readonly string[]) => {
      if (disabled) return;
      const removals = values.filter(
        (value) => selectedSet.has(value) && byValue.get(value)?.disabled !== true,
      );
      if (removals.length === 0) return;
      const removalSet = new Set(removals);
      setSelected(selected.filter((value) => !removalSet.has(value)));
      setMarkedSelected([]);
      setAnnouncement(
        labelTransferred
          ? labelTransferred(removals.length, availableLabel)
          : `${removals.length} moved to ${availableLabel}. ${
              availableAll.length + removals.length
            } available, ${selected.length - removals.length} selected.`,
      );
    },
    [
      availableAll.length,
      availableLabel,
      byValue,
      disabled,
      labelTransferred,
      selected,
      selectedSet,
      setSelected,
    ],
  );

  /* -- Reordering the selected pane --------------------------------------- */
  const reorderBlocked = selectedQuery !== '';

  const reorder = useCallback(
    (delta: -1 | 1) => {
      if (!reorderable || disabled) return;
      if (reorderBlocked) {
        // Reordering a filtered view has no honest meaning: the positions the
        // user can see are not the positions being changed.
        setAnnouncement(labelReorderFiltered);
        return;
      }
      // With nothing marked, the pane's active option is the target. It falls
      // back to the first row because that is what the pane renders as active
      // before the user has touched it — the control must act on what is
      // visibly highlighted, not on nothing.
      const fallbackActive = activeSelected ?? selectedShown[0]?.value ?? null;
      const moving =
        markedSelected.length > 0
          ? markedSelected
          : fallbackActive !== null && selectedSet.has(fallbackActive)
            ? [fallbackActive]
            : [];
      if (moving.length === 0) return;

      const next = selected.slice();
      const movingSet = new Set(moving);
      const order = delta === -1 ? [...next.keys()] : [...next.keys()].reverse();
      let changed = false;

      for (const index of order) {
        const current = next[index];
        if (current === undefined || !movingSet.has(current)) continue;
        const target = index + delta;
        if (target < 0 || target >= next.length) continue;
        const neighbour = next[target];
        if (neighbour === undefined || movingSet.has(neighbour)) continue;
        next[index] = neighbour;
        next[target] = current;
        changed = true;
      }

      if (!changed) return;
      setSelected(next);

      const lead = moving[0];
      const position = lead === undefined ? -1 : next.indexOf(lead);
      const label = lead === undefined ? '' : (byValue.get(lead)?.label ?? lead);
      setAnnouncement(
        labelReordered
          ? labelReordered(label, position + 1, next.length)
          : `${label} moved to position ${position + 1} of ${next.length}.`,
      );
    },
    [
      activeSelected,
      byValue,
      disabled,
      labelReorderFiltered,
      labelReordered,
      markedSelected,
      reorderable,
      reorderBlocked,
      selected,
      selectedSet,
      selectedShown,
      setSelected,
    ],
  );

  const heightValue = typeof listHeight === 'number' ? `${listHeight}px` : listHeight;

  /**
   * `aria-disabled`, not `disabled`, when there is simply nothing marked.
   * A real `disabled` drops the control out of the tab order, so a keyboard
   * user exploring the transfer column finds a gap where the button was and
   * no explanation for it. This stays reachable and says why on activation.
   */
  const transferControl = (
    label: string,
    icon: ReactNode,
    onActivate: () => void,
    inert: boolean,
  ) => (
    <button
      type="button"
      className="stratum-transfer-list__move"
      aria-label={label}
      disabled={disabled}
      aria-disabled={inert || undefined}
      onClick={() => {
        if (inert) {
          setAnnouncement(labelNothingMarked);
          return;
        }
        onActivate();
      }}
    >
      {icon}
    </button>
  );

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="transfer-list"
      data-size={size}
      data-disabled={disabled || undefined}
      className={clsx('stratum-transfer-list', className)}
      style={{ ...({ '--_list-height': heightValue } as Record<string, string>), ...style }}
    >
      <div className="stratum-transfer-list__layout">
        <TransferPane
          paneId={`${uid}avail`}
          title={availableLabel}
          shown={availableShown}
          total={availableAll.length}
          marked={markedAvailable}
          activeValue={activeAvailable}
          query={availableQuery}
          searchable={searchable}
          searchPlaceholder={searchAvailablePlaceholder}
          searchLabel={labelSearchAvailable}
          disabled={disabled}
          emptyLabel={emptyAvailableLabel}
          labelSelectAll={labelSelectAll}
          labelClearMarks={labelClearMarks}
          labelCount={count}
          transferKey="ArrowRight"
          describedBy={helpId}
          onQueryChange={setAvailableQuery}
          onMarkedChange={setMarkedAvailable}
          onActiveChange={setActiveAvailable}
          onTransfer={toSelected}
        />

        <div className="stratum-transfer-list__controls">
          {transferControl(labelMoveRight, <ChevronRight />, () => toSelected(markedAvailable), markedAvailable.length === 0)}
          {transferControl(labelMoveLeft, <ChevronLeft />, () => toAvailable(markedSelected), markedSelected.length === 0)}
          {transferControl(
            labelMoveAllRight,
            <ChevronsRight />,
            () => toSelected(availableShown.map((option) => option.value)),
            availableShown.length === 0,
          )}
          {transferControl(
            labelMoveAllLeft,
            <ChevronsLeft />,
            () => toAvailable(selectedShown.map((option) => option.value)),
            selectedShown.length === 0,
          )}

          {reorderable && (
            <>
              <span className="stratum-transfer-list__control-gap" aria-hidden="true" />
              {transferControl(labelMoveUp, <ArrowUp />, () => reorder(-1), false)}
              {transferControl(labelMoveDown, <ArrowDown />, () => reorder(1), false)}
            </>
          )}
        </div>

        <TransferPane
          paneId={`${uid}sel`}
          title={selectedLabel}
          shown={selectedShown}
          total={selectedAll.length}
          marked={markedSelected}
          activeValue={activeSelected}
          query={selectedQuery}
          searchable={searchable}
          searchPlaceholder={searchSelectedPlaceholder}
          searchLabel={labelSearchSelected}
          disabled={disabled}
          emptyLabel={emptySelectedLabel}
          labelSelectAll={labelSelectAll}
          labelClearMarks={labelClearMarks}
          labelCount={count}
          transferKey="ArrowLeft"
          describedBy={helpId}
          onQueryChange={setSelectedQuery}
          onMarkedChange={setMarkedSelected}
          onActiveChange={setActiveSelected}
          onTransfer={toAvailable}
          onReorder={reorder}
          reorderEnabled={reorderable}
        />
      </div>

      <VisuallyHidden id={helpId}>{labelHelp}</VisuallyHidden>
      <VisuallyHidden role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </VisuallyHidden>
    </div>
  );
});
