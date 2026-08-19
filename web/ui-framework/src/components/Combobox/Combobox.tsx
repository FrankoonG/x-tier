import {
  Fragment,
  forwardRef,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type FocusEvent as ReactFocusEvent,
  type HTMLProps,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
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
import type { SelectOption, SelectSize } from '../Select/Select';
import { usePresence } from '../../hooks/usePresence';
import './Combobox.css';

/* Positioning geometry — see the note in Menu.tsx. */
const POPUP_GAP = 4;
const VIEWPORT_PADDING = 8;

export type ComboboxOption = SelectOption;
export type ComboboxSize = SelectSize;

export interface ComboboxProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'value' | 'defaultValue' | 'onChange' | 'size' | 'type' | 'list'
  > {
  options: readonly ComboboxOption[];
  /** Committed value. `null` when nothing is chosen. */
  value?: string | null;
  defaultValue?: string | null;
  onChange?: (value: string | null) => void;
  /** Text in the field. Controlled independently of the committed value. */
  inputValue?: string;
  defaultInputValue?: string;
  onInputValueChange?: (inputValue: string) => void;
  /** Commits free text that matches no option, on `Enter` and on blur. */
  allowCustomValue?: boolean;
  /** Replaces the list with a busy state and marks the popup `aria-busy`. */
  loading?: boolean;
  labelLoading?: string;
  emptyLabel?: string;
  placeholder?: string;
  size?: ComboboxSize;
  invalid?: boolean;
  fullWidth?: boolean;
  /** Wraps the matched substring in each label. Default `true`. */
  highlightMatches?: boolean;
  /**
   * Set `false` when the option list already arrives filtered from the server;
   * the component then renders `options` verbatim and only handles navigation.
   */
  shouldFilter?: boolean;
  /** Replaces the default case-insensitive substring test. */
  filter?: (option: ComboboxOption, query: string) => boolean;
  /** Screen-reader announcement of the result count. */
  labelResultCount?: (count: number) => string;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  placement?: Placement;
  name?: string;
  /** Applied to the `<input>` rather than the control shell. */
  inputClassName?: string;
}

interface ComboboxSection {
  key: string;
  label: string | null;
  options: ComboboxOption[];
}

function defaultFilter(option: ComboboxOption, query: string): boolean {
  const needle = query.toLowerCase();
  return (
    option.label.toLowerCase().includes(needle) ||
    (option.description?.toLowerCase().includes(needle) ?? false)
  );
}

function buildSections(options: readonly ComboboxOption[]): {
  sections: ComboboxSection[];
  flat: ComboboxOption[];
} {
  const ungrouped: ComboboxOption[] = [];
  const grouped = new Map<string, ComboboxOption[]>();

  for (const option of options) {
    if (option.group == null || option.group === '') {
      ungrouped.push(option);
      continue;
    }
    const bucket = grouped.get(option.group);
    if (bucket) bucket.push(option);
    else grouped.set(option.group, [option]);
  }

  const sections: ComboboxSection[] = [];
  if (ungrouped.length > 0) sections.push({ key: '', label: null, options: ungrouped });
  for (const [label, groupOptions] of grouped) {
    sections.push({ key: label, label, options: groupOptions });
  }

  return { sections, flat: sections.flatMap((section) => section.options) };
}

/** Wraps the first case-insensitive occurrence of `query` in `label`. */
export function highlightMatch(label: string, query: string): ReactNode {
  if (query === '') return label;
  const index = label.toLowerCase().indexOf(query.toLowerCase());
  if (index < 0) return label;
  return (
    <>
      {label.slice(0, index)}
      <span className="stratum-combobox__match">{label.slice(index, index + query.length)}</span>
      {label.slice(index + query.length)}
    </>
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

/**
 * A filterable single-select combobox.
 *
 * FOCUS MODEL: `aria-activedescendant`.
 * ------------------------------------
 * DOM focus never leaves the `<input>` — it cannot, or typing would stop
 * working — so the highlighted option is published virtually. This is the
 * inverse of `Select`, which uses roving focus because its trigger is a
 * button and nothing needs to keep receiving characters.
 *
 * Two consequences are handled explicitly rather than left to chance:
 *   - `mousedown` inside the popup is prevented, so clicking an option cannot
 *     blur the field. Without it the blur handler fires first and commits (or
 *     reverts) before the click ever lands, and the option appears not to
 *     respond to the mouse at all.
 *   - The result count goes through a polite live region. A virtually focused
 *     list gives a screen reader no navigation event to announce when the
 *     filter changes underneath it.
 */
export const Combobox = forwardRef<HTMLInputElement, ComboboxProps>(function Combobox(
  {
    options,
    value: valueProp,
    defaultValue = null,
    onChange,
    inputValue: inputValueProp,
    defaultInputValue,
    onInputValueChange,
    allowCustomValue = false,
    loading = false,
    labelLoading = 'Loading options',
    emptyLabel = 'No matches',
    placeholder,
    size: sizeProp = 'md',
    invalid = false,
    fullWidth = false,
    highlightMatches = true,
    shouldFilter = true,
    filter,
    labelResultCount = (count) => `${count} ${count === 1 ? 'result' : 'results'} available`,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    placement = 'bottom-start',
    name,
    className,
    inputClassName,
    disabled = false,
    onKeyDown,
    onBlur,
    onClick,
    ...rest
  },
  forwardedRef,
) {
  const [value, setValue] = useControllableState<string | null>({
    value: valueProp,
    defaultValue,
    onChange,
  });

  const initialValue = valueProp !== undefined ? valueProp : defaultValue;
  const [inputValue, setInputValue] = useControllableState<string>({
    value: inputValueProp,
    defaultValue:
      defaultInputValue ?? options.find((option) => option.value === initialValue)?.label ?? '',
    onChange: onInputValueChange,
  });

  const [isOpen, setIsOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  /**
   * Filtering starts only once the user actually types. Re-opening a combobox
   * that already shows its selection must not filter the list down to that one
   * option — the whole point of re-opening is to pick something else.
   */
  const [isQueryActive, setIsQueryActive] = useState(false);

  const elementsRef = useRef<Array<HTMLElement | null>>([]);
  const labelsRef = useRef<Array<string | null>>([]);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const previousValueRef = useRef(value);

  const listId = useId();
  const statusId = `${listId}-status`;

  const query = isQueryActive ? inputValue.trim() : '';

  const visibleOptions = useMemo(() => {
    if (!shouldFilter || query === '') return options;
    const predicate = filter ?? defaultFilter;
    return options.filter((option) => predicate(option, query));
  }, [options, query, shouldFilter, filter]);

  const { sections, flat } = useMemo(() => buildSections(visibleOptions), [visibleOptions]);
  const selectedOption = useMemo(
    () => (value == null ? null : (options.find((option) => option.value === value) ?? null)),
    [options, value],
  );

  // Follow an externally driven value change, but never yank text out from
  // under someone who is mid-edit.
  //
  // A free-text value matches no option by definition, so falling back to `''`
  // would blank the field on every commit-by-blur: the blur has already moved
  // focus, so the mid-edit guard above does not save it. Show the value itself
  // instead.
  useEffect(() => {
    if (previousValueRef.current === value) return;
    previousValueRef.current = value;
    if (document.activeElement !== inputRef.current) {
      const match = options.find((option) => option.value === value);
      setInputValue(match ? match.label : allowCustomValue && value != null ? value : '');
    }
  }, [value, options, allowCustomValue, setInputValue]);

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

  // `keyboardHandlers: false` — Space is a character here, not an activation
  // key. `toggle: false` — clicking a field you are already typing in must not
  // close the list you are reading.
  const click = useClick(context, {
    enabled: !disabled,
    keyboardHandlers: false,
    toggle: false,
  });
  const role = useRole(context, { role: 'combobox' });
  const dismiss = useDismiss(context);
  const listNavigation = useListNavigation(context, {
    listRef: elementsRef,
    activeIndex,
    onNavigate: setActiveIndex,
    virtual: true,
    loop: true,
    // 'auto' is what gives APG's "an arrow key on a closed combobox opens the
    // list *and* moves visual focus onto an option" while leaving a list that
    // was opened by typing or by clicking with nothing highlighted — so Enter
    // still commits what was typed rather than silently choosing a suggestion.
    focusItemOnOpen: 'auto',
    // With free text allowed, arrowing past the ends must be able to land back
    // on what was typed; without it, there is no way to choose your own text
    // once an option has been highlighted.
    allowEscape: allowCustomValue,
  });

  const { getReferenceProps, getFloatingProps, getItemProps } = useInteractions([
    click,
    role,
    dismiss,
    listNavigation,
  ]);

  function commitOption(index: number) {
    const option = flat[index];
    if (!option || option.disabled) return;
    setValue(option.value);
    setInputValue(option.label);
    setIsQueryActive(false);
    setActiveIndex(null);
    setIsOpen(false);
  }

  function revertText() {
    setInputValue(selectedOption?.label ?? '');
    setIsQueryActive(false);
  }

  function handleInputChange(event: ChangeEvent<HTMLInputElement>) {
    const next = event.target.value;
    setInputValue(next);
    setIsQueryActive(true);
    setActiveIndex(null);
    if (!isOpen) setIsOpen(true);
    if (next === '' && !allowCustomValue) setValue(null);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    // Floating UI's own reference handler runs before this one and has already
    // called `preventDefault()` for the navigation keys, so a bare
    // `defaultPrevented` check would swallow everything below. Only a consumer
    // handler that newly prevents the default should stop us.
    const preventedUpstream = event.defaultPrevented;
    onKeyDown?.(event);
    if (event.defaultPrevented && !preventedUpstream) return;

    if (event.key === 'Enter') {
      if (isOpen && activeIndex != null) {
        event.preventDefault();
        commitOption(activeIndex);
        return;
      }
      if (allowCustomValue) {
        event.preventDefault();
        const text = inputValue.trim();
        setValue(text === '' ? null : text);
        setIsQueryActive(false);
        setIsOpen(false);
        return;
      }
      if (isOpen) {
        event.preventDefault();
        setIsOpen(false);
      }
      return;
    }

    // `useDismiss` already closed an open popup by the time this runs, so a
    // second Escape is the one that clears what was typed.
    if (event.key === 'Escape' && !isOpen) {
      revertText();
    }
  }

  function handleBlur(event: ReactFocusEvent<HTMLInputElement>) {
    onBlur?.(event);
    if (allowCustomValue) {
      const text = inputValue.trim();
      if (text !== (selectedOption?.label ?? '')) {
        setValue(text === '' ? null : text);
      }
      setIsQueryActive(false);
      return;
    }
    // Leaving half-typed text in a field that cannot accept it would show a
    // value the component does not hold.
    revertText();
  }

  const mergedInputRef = useMergeRefs([refs.setReference, inputRef, forwardedRef]);
  // Keeps the popup mounted for the length of its exit transition — see the
  // note on the exit rules in Combobox.css.
  const presence = usePresence(isOpen);
  const popupRef = useMergeRefs([refs.setFloating, presence.ref]);
  const [side = 'bottom', align = 'start'] = context.placement.split('-');

  return (
    <div
      data-stratum="combobox"
      data-size={sizeProp}
      data-open={isOpen || undefined}
      data-disabled={disabled || undefined}
      data-invalid={invalid || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-combobox', className)}
    >
      {/* `rest` goes THROUGH the getter, not alongside it: `useClick` and
          `useListNavigation` return onClick/onKeyDown/onKeyUp/onFocus/
          onPointerDown/onMouseDown, so a sibling `{...rest}` spread is
          overwritten key for key and the consumer's handlers never fire.
          Floating UI chains the handlers it is given as user props. The three
          handlers this component owns stay destructured and chained by hand:
          each has ordering logic (see handleKeyDown) that must run in a
          specific position. The field's own attributes stay after the spread,
          so the merged `aria-describedby` wins over a raw one in `rest`. */}
      <input
        {...getReferenceProps({
          ...(rest as HTMLProps<HTMLInputElement>),
          onChange: handleInputChange,
          onKeyDown: handleKeyDown,
          onBlur: handleBlur,
          onClick(event: ReactMouseEvent<HTMLInputElement>) {
            onClick?.(event);
            // Clicking the field shows everything, not the filtered remainder
            // of whatever is currently displayed in it.
            setIsQueryActive(false);
          },
        })}
        ref={mergedInputRef}
        type="text"
        value={inputValue}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        autoCorrect="off"
        spellCheck={false}
        // Falls back to the consumer's value rather than clobbering it: this
        // sits after the spread, so a bare `invalid || undefined` would strip
        // an `aria-invalid` passed through `rest`.
        aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
        aria-describedby={clsx(statusId, rest['aria-describedby']) || undefined}
        className={clsx('stratum-combobox__input', inputClassName)}
      />

      <span className="stratum-combobox__adornment" aria-hidden="true">
        {loading ? <Spinner size="sm" /> : <ChevronDownIcon />}
      </span>

      {name != null && <input type="hidden" name={name} value={value ?? ''} />}

      {/* Virtual focus produces no navigation event when the list is refiltered
        * under the cursor, so the count is announced explicitly. */}
      <span id={statusId} role="status" className="stratum-visually-hidden">
        {isOpen && !loading ? labelResultCount(flat.length) : ''}
      </span>

      {presence.isPresent && (
        <FloatingPortal>
          <FloatingFocusManager
            context={context}
            modal={false}
            initialFocus={-1}
            returnFocus={false}
          >
            <div
              ref={popupRef}
              style={floatingStyles}
              data-stratum="combobox-popup"
              data-state={presence.state}
              data-side={side}
              data-align={align}
              className="stratum-combobox__popup"
              {...getFloatingProps({
                'aria-busy': loading || undefined,
                onMouseDown(event: ReactMouseEvent<HTMLDivElement>) {
                  // Keeps focus — and therefore the caret and the typed text —
                  // in the input while an option is clicked.
                  event.preventDefault();
                },
              })}
            >
              <FloatingList elementsRef={elementsRef} labelsRef={labelsRef}>
                {loading ? (
                  <div className="stratum-combobox__message" role="presentation">
                    <Spinner size="sm" />
                    <span>{labelLoading}</span>
                  </div>
                ) : flat.length === 0 ? (
                  <div className="stratum-combobox__message" role="presentation">
                    <span>{emptyLabel}</span>
                  </div>
                ) : (
                  sections.map((section) => {
                    const body = section.options.map((option) => (
                      <ComboboxOptionRow
                        key={option.value}
                        option={option}
                        activeIndex={activeIndex}
                        selected={option.value === value}
                        query={highlightMatches ? query : ''}
                        getItemProps={getItemProps}
                        onCommit={commitOption}
                      />
                    ));

                    if (section.label == null) {
                      return <Fragment key={section.key || '__ungrouped'}>{body}</Fragment>;
                    }
                    const groupLabelId = `${listId}-group-${section.key}`;
                    return (
                      <div
                        key={section.key}
                        role="group"
                        aria-labelledby={groupLabelId}
                        className="stratum-combobox__group"
                      >
                        <div
                          id={groupLabelId}
                          role="presentation"
                          className="stratum-combobox__group-label"
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

interface ComboboxOptionRowProps {
  option: ComboboxOption;
  activeIndex: number | null;
  selected: boolean;
  query: string;
  getItemProps: ReturnType<typeof useInteractions>['getItemProps'];
  onCommit: (index: number) => void;
}

function ComboboxOptionRow({
  option,
  activeIndex,
  selected,
  query,
  getItemProps,
  onCommit,
}: ComboboxOptionRowProps) {
  const item = useListItem({ label: option.disabled ? null : option.label });
  const isActive = item.index === activeIndex && item.index !== -1;

  return (
    <div
      ref={item.ref}
      data-stratum="combobox-option"
      data-active={isActive || undefined}
      data-selected={selected || undefined}
      aria-disabled={option.disabled || undefined}
      className="stratum-combobox__option"
      {...getItemProps({
        active: isActive,
        selected,
        onClick() {
          if (!option.disabled) onCommit(item.index);
        },
      })}
    >
      {option.icon && (
        <span className="stratum-combobox__option-icon" aria-hidden="true">
          {option.icon}
        </span>
      )}
      <span className="stratum-combobox__option-text">
        <span className="stratum-combobox__option-label">
          {highlightMatch(option.label, query)}
        </span>
        {option.description && (
          <span className="stratum-combobox__option-description">{option.description}</span>
        )}
      </span>
    </div>
  );
}
