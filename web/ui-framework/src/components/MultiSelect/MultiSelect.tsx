import {
  Fragment,
  forwardRef,
  useId,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type HTMLProps,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';
import {
  FloatingFocusManager,
  FloatingList,
  FloatingPortal,
  autoUpdate,
  flip,
  offset,
  shift,
  size,
  useClick,
  useDismiss,
  useFloating,
  useInteractions,
  useListItem,
  useListNavigation,
  useMergeRefs,
  useRole,
  type Placement,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { Spinner } from '../Spinner/Spinner';
import { useIsomorphicLayoutEffect } from '../Popover/overlayFocus';
import type { SelectOption, SelectSize } from '../Select/Select';
import { usePresence } from '../../hooks/usePresence';
import './MultiSelect.css';

/* Positioning geometry — see the note in Menu.tsx. */
const POPUP_GAP = 4;
const VIEWPORT_PADDING = 8;

export type MultiSelectOption = SelectOption;
export type MultiSelectSize = SelectSize;

export interface MultiSelectProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'value' | 'defaultValue' | 'onChange' | 'size' | 'type' | 'list'
  > {
  options: readonly MultiSelectOption[];
  value?: string[];
  defaultValue?: string[];
  onChange?: (values: string[]) => void;
  inputValue?: string;
  defaultInputValue?: string;
  onInputValueChange?: (inputValue: string) => void;
  placeholder?: string;
  size?: MultiSelectSize;
  invalid?: boolean;
  fullWidth?: boolean;
  loading?: boolean;
  labelLoading?: string;
  emptyLabel?: string;
  /** Chips rendered before the rest collapse into a `+N` summary. */
  maxVisible?: number;
  /** Hard cap on selections. Unselected options go `aria-disabled` at the cap. */
  maxSelected?: number;
  /** Accessible name of a chip's remove control. */
  labelRemove?: (label: string) => string;
  /** Visible text of the overflow chip. */
  overflowLabel?: (count: number) => string;
  /** Accessible name of the overflow chip. */
  labelOverflow?: (count: number) => string;
  /** Polite announcement of the filtered option count. */
  labelResultCount?: (count: number) => string;
  /** Polite announcement of how many things are selected. */
  labelSelectedCount?: (count: number) => string;
  highlightMatches?: boolean;
  shouldFilter?: boolean;
  filter?: (option: MultiSelectOption, query: string) => boolean;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  placement?: Placement;
  /** Emits one hidden input per selected value. */
  name?: string;
  inputClassName?: string;
}

interface MultiSelectSection {
  key: string;
  label: string | null;
  options: MultiSelectOption[];
}

type PendingFocus = { kind: 'chip'; index: number } | { kind: 'input' };

function defaultFilter(option: MultiSelectOption, query: string): boolean {
  const needle = query.toLowerCase();
  return (
    option.label.toLowerCase().includes(needle) ||
    (option.description?.toLowerCase().includes(needle) ?? false)
  );
}

function buildSections(options: readonly MultiSelectOption[]): {
  sections: MultiSelectSection[];
  flat: MultiSelectOption[];
} {
  const ungrouped: MultiSelectOption[] = [];
  const grouped = new Map<string, MultiSelectOption[]>();

  for (const option of options) {
    if (option.group == null || option.group === '') {
      ungrouped.push(option);
      continue;
    }
    const bucket = grouped.get(option.group);
    if (bucket) bucket.push(option);
    else grouped.set(option.group, [option]);
  }

  const sections: MultiSelectSection[] = [];
  if (ungrouped.length > 0) sections.push({ key: '', label: null, options: ungrouped });
  for (const [label, groupOptions] of grouped) {
    sections.push({ key: label, label, options: groupOptions });
  }

  return { sections, flat: sections.flatMap((section) => section.options) };
}

function highlightMatch(label: string, query: string): ReactNode {
  if (query === '') return label;
  const index = label.toLowerCase().indexOf(query.toLowerCase());
  if (index < 0) return label;
  return (
    <>
      {label.slice(0, index)}
      <span className="stratum-multiselect__match">{label.slice(index, index + query.length)}</span>
      {label.slice(index + query.length)}
    </>
  );
}

function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false" aria-hidden="true">
      <path
        d="m4.5 4.5 7 7m0-7-7 7"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
    </svg>
  );
}

function ChevronDownIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false" aria-hidden="true">
      <path
        d="m4 6.5 4 4 4-4"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false" aria-hidden="true">
      <path
        d="m3.5 8.5 3 3 6-7"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/**
 * A filterable multi-select with chips.
 *
 * FOCUS MODEL
 * -----------
 * Like `Combobox`, the option list is navigated virtually via
 * `aria-activedescendant` so the field keeps receiving characters. The chips
 * are a *second* composite: they form a roving-`tabindex` group that is
 * entered with `ArrowLeft` from an empty field and left with `ArrowRight` or
 * `Escape`.
 *
 * Chips are deliberately not individual tab stops. Twenty selections would
 * otherwise mean twenty presses of `Tab` to get past one control, which is the
 * single most common complaint about token inputs — so removal is reachable
 * three ways instead: `Backspace` on an empty field takes the last one,
 * `ArrowLeft` walks the group, and each chip keeps a real clickable button
 * with its own `aria-label`.
 *
 * ANNOUNCEMENTS
 * -------------
 * Selection count and result count go through polite live regions. Nothing
 * else reports them: toggling an option changes neither DOM focus nor the
 * field's value, so without this a screen-reader user gets silence.
 */
export const MultiSelect = forwardRef<HTMLInputElement, MultiSelectProps>(function MultiSelect(
  {
    options,
    value: valueProp,
    defaultValue = [],
    onChange,
    inputValue: inputValueProp,
    defaultInputValue = '',
    onInputValueChange,
    placeholder,
    size: sizeProp = 'md',
    invalid = false,
    fullWidth = false,
    loading = false,
    labelLoading = 'Loading options',
    emptyLabel = 'No matches',
    maxVisible = 3,
    maxSelected,
    labelRemove = (label) => `Remove ${label}`,
    overflowLabel = (count) => `+${count}`,
    labelOverflow = (count) => `${count} more selected`,
    labelResultCount = (count) => `${count} ${count === 1 ? 'result' : 'results'} available`,
    labelSelectedCount = (count) => `${count} selected`,
    highlightMatches = true,
    shouldFilter = true,
    filter,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    placement = 'bottom-start',
    name,
    className,
    inputClassName,
    disabled = false,
    onKeyDown,
    ...rest
  },
  forwardedRef,
) {
  // The field is a bare <input>: a placeholder is not a label, and nothing else
  // in the markup names it. `<Field>` supplies `aria-labelledby` through `rest`,
  // so this only fires for a genuinely unnamed control. Same guard as Button's.
  if (
    import.meta.env?.DEV &&
    !rest['aria-label'] &&
    !rest['aria-labelledby'] &&
    !rest.id
  ) {
    console.error(
      '[stratum] <MultiSelect> requires an accessible name: `aria-label`, ' +
        '`aria-labelledby`, or a wrapping <Field>. A placeholder is not a label.',
    );
  }

  const [values, setValues] = useControllableState<string[]>({
    value: valueProp,
    defaultValue,
    onChange,
  });
  const [inputValue, setInputValue] = useControllableState<string>({
    value: inputValueProp,
    defaultValue: defaultInputValue,
    onChange: onInputValueChange,
  });
  const [isOpen, setIsOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });

  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [focusedChip, setFocusedChip] = useState<number | null>(null);
  const [pendingFocus, setPendingFocus] = useState<PendingFocus | null>(null);

  const elementsRef = useRef<Array<HTMLElement | null>>([]);
  const labelsRef = useRef<Array<string | null>>([]);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const chipRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const listId = useId();
  const resultStatusId = `${listId}-results`;
  const selectionStatusId = `${listId}-selection`;

  const query = inputValue.trim();
  const atCapacity = maxSelected != null && values.length >= maxSelected;

  const visibleOptions = useMemo(() => {
    if (!shouldFilter || query === '') return options;
    const predicate = filter ?? defaultFilter;
    return options.filter((option) => predicate(option, query));
  }, [options, query, shouldFilter, filter]);

  const { sections, flat } = useMemo(() => buildSections(visibleOptions), [visibleOptions]);

  const selectedOptions = useMemo(
    () =>
      values.map<MultiSelectOption>(
        (value) => options.find((option) => option.value === value) ?? { value, label: value },
      ),
    [values, options],
  );

  const shownChips = selectedOptions.slice(0, Math.max(0, maxVisible));
  const hiddenCount = selectedOptions.length - shownChips.length;

  // Focus moves are queued rather than performed inline: the element that
  // should receive focus after a removal does not exist until React has
  // committed the new chip list.
  //
  // A layout effect, not a passive one: removing the focused chip unmounts the
  // button that had focus, so the browser drops focus to <body> during that
  // commit. A passive effect runs after paint, which leaves a frame with focus
  // on the document — announced as a context change and visible as a flicker.
  useIsomorphicLayoutEffect(() => {
    if (!pendingFocus) return;
    if (pendingFocus.kind === 'input') inputRef.current?.focus();
    else chipRefs.current[pendingFocus.index]?.focus();
    setPendingFocus(null);
  }, [pendingFocus]);

  const { refs, floatingStyles, context } = useFloating<HTMLInputElement>({
    open: isOpen,
    onOpenChange: setIsOpen,
    placement,
    strategy: 'fixed',
    // Position with top/left, not a transform. Floating UI's default writes
    // `transform: translate(x, y)` inline, which collides with this surface's
    // entrance animation twice over: the inline value overrides the CSS
    // `transform`, and `transition: transform` then animates the very first
    // positioning step from `translate(0, 0)` — so the panel visibly flew in
    // from the top-left corner on its first open and only looked right on
    // subsequent ones, once a previous value existed to interpolate from.
    transform: false,
    whileElementsMounted: autoUpdate,
    middleware: [
      offset(POPUP_GAP),
      flip({ padding: VIEWPORT_PADDING }),
      shift({ padding: VIEWPORT_PADDING }),
      size({
        padding: VIEWPORT_PADDING,
        apply({ availableHeight, rects, elements }) {
          elements.floating.style.setProperty('--_available-height', `${availableHeight}px`);
          elements.floating.style.setProperty('--_trigger-width', `${rects.reference.width}px`);
        },
      }),
    ],
  });

  const click = useClick(context, { enabled: !disabled, keyboardHandlers: false, toggle: false });
  const role = useRole(context, { role: 'combobox' });
  const dismiss = useDismiss(context);
  const listNavigation = useListNavigation(context, {
    listRef: elementsRef,
    activeIndex,
    onNavigate: setActiveIndex,
    virtual: true,
    loop: true,
    // See the note in Combobox: 'auto' highlights on an arrow-key open only.
    focusItemOnOpen: 'auto',
  });

  const { getReferenceProps, getFloatingProps, getItemProps } = useInteractions([
    click,
    role,
    dismiss,
    listNavigation,
  ]);

  function toggleValue(optionValue: string) {
    const next = values.includes(optionValue)
      ? values.filter((existing) => existing !== optionValue)
      : atCapacity
        ? values
        : [...values, optionValue];
    if (next !== values) setValues(next);
  }

  function toggleAt(index: number) {
    const option = flat[index];
    if (!option || option.disabled) return;
    if (!values.includes(option.value) && atCapacity) return;
    toggleValue(option.value);
    // Clearing the query is what makes "type, pick, type, pick" work without
    // reaching for Backspace between every selection. The highlight is only
    // dropped when that actually re-expands the list — otherwise the row you
    // just toggled would stop being the one Enter acts on, and toggling a
    // checkbox off would take two arrow presses.
    if (inputValue !== '') {
      setInputValue('');
      setActiveIndex(null);
    }
  }

  function removeAt(index: number) {
    const target = values[index];
    if (target === undefined) return;
    setValues(values.filter((_, position) => position !== index));

    const remaining = values.length - 1;
    if (remaining === 0) {
      setPendingFocus({ kind: 'input' });
      setFocusedChip(null);
      return;
    }
    // Prefer the chip that slides into the removed slot; fall back to the one
    // before it when the last chip was removed.
    const nextIndex = Math.min(index, remaining - 1);
    setPendingFocus({ kind: 'chip', index: nextIndex });
    setFocusedChip(nextIndex);
  }

  function handleInputChange(event: ChangeEvent<HTMLInputElement>) {
    setInputValue(event.target.value);
    setActiveIndex(null);
    if (!isOpen) setIsOpen(true);
  }

  function handleInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    // See the matching note in Combobox: Floating UI has already prevented the
    // default for navigation keys by the time this runs, so only a *new*
    // prevention by the consumer's handler should stop us.
    const preventedUpstream = event.defaultPrevented;
    onKeyDown?.(event);
    if (event.defaultPrevented && !preventedUpstream) return;

    if (event.key === 'Enter' && isOpen && activeIndex != null) {
      event.preventDefault();
      toggleAt(activeIndex);
      return;
    }

    if (inputValue !== '' || values.length === 0) return;

    if (event.key === 'Backspace' || event.key === 'Delete') {
      event.preventDefault();
      removeAt(values.length - 1);
      // Focus stays in the field: this is a typing gesture, not navigation.
      setPendingFocus(null);
      setFocusedChip(null);
      return;
    }

    if (event.key === 'ArrowLeft') {
      event.preventDefault();
      const lastVisible = shownChips.length - 1;
      if (lastVisible < 0) return;
      setFocusedChip(lastVisible);
      setPendingFocus({ kind: 'chip', index: lastVisible });
    }
  }

  function handleChipKeyDown(event: ReactKeyboardEvent<HTMLButtonElement>, index: number) {
    const lastVisible = shownChips.length - 1;

    switch (event.key) {
      case 'ArrowLeft': {
        event.preventDefault();
        const next = Math.max(0, index - 1);
        setFocusedChip(next);
        setPendingFocus({ kind: 'chip', index: next });
        break;
      }
      case 'ArrowRight': {
        event.preventDefault();
        if (index >= lastVisible) {
          setFocusedChip(null);
          setPendingFocus({ kind: 'input' });
        } else {
          setFocusedChip(index + 1);
          setPendingFocus({ kind: 'chip', index: index + 1 });
        }
        break;
      }
      case 'Home': {
        event.preventDefault();
        setFocusedChip(0);
        setPendingFocus({ kind: 'chip', index: 0 });
        break;
      }
      case 'End': {
        event.preventDefault();
        setFocusedChip(lastVisible);
        setPendingFocus({ kind: 'chip', index: lastVisible });
        break;
      }
      case 'Backspace':
      case 'Delete': {
        event.preventDefault();
        removeAt(index);
        break;
      }
      case 'Escape': {
        event.preventDefault();
        setFocusedChip(null);
        setPendingFocus({ kind: 'input' });
        break;
      }
      default:
        break;
    }
  }

  function handleShellPointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (disabled) return;
    const target = event.target;
    if (!(target instanceof Element)) return;
    // Let the chip buttons and the field itself handle their own presses.
    if (target.closest('[data-stratum="multiselect-chip-remove"]')) return;
    if (target === inputRef.current) return;
    // Anywhere else in the shell behaves as if the field were clicked, without
    // the caret being dropped somewhere unexpected.
    event.preventDefault();
    inputRef.current?.focus();
    setIsOpen(true);
  }

  const mergedInputRef = useMergeRefs([refs.setReference, inputRef, forwardedRef]);
  // Keeps the popup mounted for the length of its exit transition — see the
  // note on the exit rules in MultiSelect.css.
  const presence = usePresence(isOpen);
  const popupRef = useMergeRefs([refs.setFloating, presence.ref]);
  const [side = 'bottom', align = 'start'] = context.placement.split('-');
  const describedBy =
    clsx(resultStatusId, selectionStatusId, rest['aria-describedby']) || undefined;

  return (
    <div
      data-stratum="multiselect"
      data-size={sizeProp}
      data-open={isOpen || undefined}
      data-disabled={disabled || undefined}
      data-invalid={invalid || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-multiselect', className)}
      onPointerDown={handleShellPointerDown}
    >
      <div className="stratum-multiselect__chips">
        {shownChips.map((option, index) => (
          <span
            key={option.value}
            data-stratum="multiselect-chip"
            data-focused={focusedChip === index || undefined}
            className="stratum-multiselect__chip"
          >
            <span className="stratum-multiselect__chip-label">{option.label}</span>
            <button
              ref={(node) => {
                chipRefs.current[index] = node;
              }}
              type="button"
              data-stratum="multiselect-chip-remove"
              tabIndex={focusedChip === index ? 0 : -1}
              disabled={disabled}
              aria-label={labelRemove(option.label)}
              className="stratum-multiselect__chip-remove stratum-focus-inset"
              onFocus={() => setFocusedChip(index)}
              onBlur={() => setFocusedChip((current) => (current === index ? null : current))}
              onKeyDown={(event) => handleChipKeyDown(event, index)}
              onClick={(event: ReactMouseEvent<HTMLButtonElement>) => {
                event.stopPropagation();
                removeAt(index);
              }}
            >
              <CloseIcon />
            </button>
          </span>
        ))}

        {hiddenCount > 0 && (
          <span
            data-stratum="multiselect-overflow"
            className="stratum-multiselect__chip stratum-multiselect__chip--overflow"
          >
            <span aria-hidden="true">{overflowLabel(hiddenCount)}</span>
            <span className="stratum-visually-hidden">{labelOverflow(hiddenCount)}</span>
          </span>
        )}

        {/* `rest` goes THROUGH the getter, not alongside it: `useClick` and
            `useListNavigation` return onClick/onKeyDown/onKeyUp/onFocus/
            onPointerDown/onMouseDown/onPointerEnter, so a sibling `{...rest}`
            spread is overwritten key for key and the consumer's handlers never
            fire. Floating UI chains the handlers it is given as user props.
            The field's own attributes stay after the spread, so the merged
            `aria-describedby` wins over a raw one in `rest`. */}
        <input
          ref={mergedInputRef}
          type="text"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          {...getReferenceProps({
            ...(rest as HTMLProps<HTMLInputElement>),
            onChange: handleInputChange,
            onKeyDown: handleInputKeyDown,
          })}
          value={inputValue}
          placeholder={selectedOptions.length === 0 ? placeholder : undefined}
          disabled={disabled}
          // Falls back to the consumer's value rather than clobbering it: this
          // sits after the spread, so a bare `invalid || undefined` would strip
          // an `aria-invalid` passed through `rest`.
          aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
          aria-describedby={describedBy}
          className={clsx('stratum-multiselect__input', inputClassName)}
        />
      </div>

      <span className="stratum-multiselect__adornment" aria-hidden="true">
        {loading ? <Spinner size="sm" /> : <ChevronDownIcon />}
      </span>

      {name != null &&
        values.map((selected) => (
          <input key={selected} type="hidden" name={name} value={selected} />
        ))}

      <span id={resultStatusId} role="status" className="stratum-visually-hidden">
        {isOpen && !loading ? labelResultCount(flat.length) : ''}
      </span>
      <span id={selectionStatusId} role="status" className="stratum-visually-hidden">
        {labelSelectedCount(values.length)}
      </span>

      {presence.isPresent && (
        /* `preserveTabOrder` (the default) renders two `tabindex="0"`,
         * `aria-hidden="true"` guard spans at this position in the tree — a
         * hidden tab stop inside the field, and an `aria-hidden-focus` failure.
         * It exists to let Tab walk into a portalled popup, which this popup
         * never needs: the list is `virtual`, so focus stays on the input and
         * the options are reached with the arrow keys. Same call Menu makes,
         * for the same reason. */
        <FloatingPortal preserveTabOrder={false}>
          <FloatingFocusManager
            context={context}
            modal={false}
            initialFocus={-1}
            returnFocus={false}
            /* Suppresses Floating UI's `markOthers` pass.
             *
             * The reference is a typeable `role="combobox"` and `initialFocus`
             * is -1, which Floating UI reads as an "untrapped typeable
             * combobox" and answers by setting `aria-hidden="true"` on every
             * element outside the popup — WITHOUT `inert`, because `guards` are
             * used instead. The result is a document full of tabbable content
             * that is invisible to assistive tech: axe reports it as
             * `aria-hidden-focus`, and it is a real SC 4.1.2 failure, not a
             * false positive.
             *
             * Hiding the page is not warranted here in the first place. The
             * list is `virtual`, so focus never leaves the field, and the popup
             * is not modal. Returning the body as an "inside" element makes
             * `markOthers` stop at the body and mark nothing, which is the
             * behaviour a non-modal listbox should have had. Everything else
             * FloatingFocusManager does — focus guards, close-on-focus-out —
             * is unaffected.
             *
             * `getInsideElements` is wrapped in `useEffectEvent` upstream, so
             * an inline callback does not re-run the effect. */
            getInsideElements={() =>
              [inputRef.current?.ownerDocument.body ?? null].filter(
                (node): node is HTMLElement => node != null,
              )
            }
          >
            <div
              ref={popupRef}
              style={floatingStyles}
              data-stratum="multiselect-popup"
              data-state={presence.state}
              data-side={side}
              data-align={align}
              className="stratum-multiselect__popup"
              {...getFloatingProps({
                'aria-multiselectable': true,
                'aria-busy': loading || undefined,
                onMouseDown(event: ReactMouseEvent<HTMLDivElement>) {
                  // Toggling must not blur the field, or the list closes
                  // between the first and second selection.
                  event.preventDefault();
                },
              })}
            >
              <FloatingList elementsRef={elementsRef} labelsRef={labelsRef}>
                {loading ? (
                  <div className="stratum-multiselect__message" role="presentation">
                    <Spinner size="sm" />
                    <span>{labelLoading}</span>
                  </div>
                ) : flat.length === 0 ? (
                  <div className="stratum-multiselect__message" role="presentation">
                    <span>{emptyLabel}</span>
                  </div>
                ) : (
                  sections.map((section) => {
                    const body = section.options.map((option) => {
                      const selected = values.includes(option.value);
                      return (
                        <MultiSelectOptionRow
                          key={option.value}
                          option={option}
                          activeIndex={activeIndex}
                          selected={selected}
                          blocked={!selected && atCapacity}
                          query={highlightMatches ? query : ''}
                          getItemProps={getItemProps}
                          onToggle={toggleAt}
                        />
                      );
                    });

                    if (section.label == null) {
                      return <Fragment key={section.key || '__ungrouped'}>{body}</Fragment>;
                    }
                    const groupLabelId = `${listId}-group-${section.key}`;
                    return (
                      <div
                        key={section.key}
                        role="group"
                        aria-labelledby={groupLabelId}
                        className="stratum-multiselect__group"
                      >
                        <div
                          id={groupLabelId}
                          role="presentation"
                          className="stratum-multiselect__group-label"
                        >
                          {section.label}
                        </div>
                        {body}
                      </div>
                    );
                  })
                )}
              </FloatingList>
            </div>
          </FloatingFocusManager>
        </FloatingPortal>
      )}
    </div>
  );
});

interface MultiSelectOptionRowProps {
  option: MultiSelectOption;
  activeIndex: number | null;
  selected: boolean;
  /** Unselected while the selection cap is reached. */
  blocked: boolean;
  query: string;
  getItemProps: ReturnType<typeof useInteractions>['getItemProps'];
  onToggle: (index: number) => void;
}

function MultiSelectOptionRow({
  option,
  activeIndex,
  selected,
  blocked,
  query,
  getItemProps,
  onToggle,
}: MultiSelectOptionRowProps) {
  const unavailable = option.disabled === true || blocked;
  const item = useListItem({ label: unavailable ? null : option.label });
  const isActive = item.index === activeIndex && item.index !== -1;

  return (
    <div
      ref={item.ref}
      {...getItemProps({
        active: isActive,
        selected,
        onClick() {
          if (!unavailable) onToggle(item.index);
        },
      })}
      // Asserted here, after the spread, so it cannot be clobbered and does not
      // depend on `useRole` continuing to emit it for combobox items. Selection
      // is the whole point of a multi-select and nothing else conveys it: the
      // tick is aria-hidden and `data-selected` carries no ARIA semantics.
      aria-selected={selected}
      aria-disabled={unavailable || undefined}
      data-stratum="multiselect-option"
      data-active={isActive || undefined}
      data-selected={selected || undefined}
      className="stratum-multiselect__option"
    >
      <span className="stratum-multiselect__option-check" aria-hidden="true">
        {selected && <CheckIcon />}
      </span>
      {option.icon && (
        <span className="stratum-multiselect__option-icon" aria-hidden="true">
          {option.icon}
        </span>
      )}
      <span className="stratum-multiselect__option-text">
        <span className="stratum-multiselect__option-label">
          {highlightMatch(option.label, query)}
        </span>
        {option.description && (
          <span className="stratum-multiselect__option-description">{option.description}</span>
        )}
      </span>
    </div>
  );
}
