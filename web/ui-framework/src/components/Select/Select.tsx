import {
  Fragment,
  forwardRef,
  useId,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type HTMLProps,
  type KeyboardEvent as ReactKeyboardEvent,
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
  useTypeahead,
  type Placement,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { usePresence } from '../../hooks/usePresence';
import './Select.css';

/* Positioning geometry — see the note in Menu.tsx. */
const POPUP_GAP = 4;
const VIEWPORT_PADDING = 8;

export type SelectSize = 'sm' | 'md' | 'lg';

export interface SelectOption {
  value: string;
  label: string;
  /** Secondary line under the label. Never the only place meaning lives. */
  description?: string;
  /** Leading adornment. Decorative — `label` carries the accessible name. */
  icon?: ReactNode;
  disabled?: boolean;
  /** Options sharing a `group` render together under that heading. */
  group?: string;
}

export interface SelectProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    'value' | 'defaultValue' | 'onChange' | 'type' | 'children'
  > {
  options: readonly SelectOption[];
  value?: string | null;
  defaultValue?: string | null;
  onChange?: (value: string | null) => void;
  /** Shown when nothing is selected. */
  placeholder?: string;
  size?: SelectSize;
  invalid?: boolean;
  fullWidth?: boolean;
  /** Shown in place of the list when `options` is empty. */
  emptyLabel?: string;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  placement?: Placement;
  /** Emits a hidden input so the value participates in native form submission. */
  name?: string;
  /** Replaces the trigger's text. Receives `null` when nothing is selected. */
  renderValue?: (option: SelectOption | null) => ReactNode;
}

interface SelectSection {
  key: string;
  label: string | null;
  options: SelectOption[];
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

/** Groups in first-appearance order; ungrouped options keep a leading block. */
function buildSections(options: readonly SelectOption[]): {
  sections: SelectSection[];
  flat: SelectOption[];
} {
  const ungrouped: SelectOption[] = [];
  const grouped = new Map<string, SelectOption[]>();

  for (const option of options) {
    if (option.group == null || option.group === '') {
      ungrouped.push(option);
      continue;
    }
    const bucket = grouped.get(option.group);
    if (bucket) bucket.push(option);
    else grouped.set(option.group, [option]);
  }

  const sections: SelectSection[] = [];
  if (ungrouped.length > 0) sections.push({ key: '', label: null, options: ungrouped });
  for (const [label, groupOptions] of grouped) {
    sections.push({ key: label, label, options: groupOptions });
  }

  return { sections, flat: sections.flatMap((section) => section.options) };
}

/**
 * A single-select listbox.
 *
 * FOCUS MODEL: roving focus, not `aria-activedescendant`.
 * ------------------------------------------------------
 * When the popup opens, DOM focus moves onto the option itself and the roving
 * `tabindex` follows the arrow keys. The trigger is a real `<button>` and the
 * options are real focus targets, which is what makes screen-reader "focus
 * mode", speech control and switch access all land on the same element the
 * sighted user is looking at. `aria-activedescendant` is used by `Combobox`
 * and `MultiSelect` instead, because those must keep focus in a text field —
 * that is the one situation where virtual focus is the only correct answer.
 *
 * KEYBOARD
 * --------
 * `ArrowDown`/`ArrowUp` open and move with wrap, `Home`/`End` jump to the
 * ends, printable characters type-ahead (and select directly while closed,
 * like a native `<select>`), `Enter`/`Space` commit, `Tab` commits and leaves,
 * `Escape` closes and leaves the value untouched.
 */
export const Select = forwardRef<HTMLButtonElement, SelectProps>(function Select(
  {
    options,
    value: valueProp,
    defaultValue = null,
    onChange,
    placeholder = 'Select an option',
    size: sizeProp = 'md',
    invalid = false,
    fullWidth = false,
    emptyLabel = 'No options',
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    placement = 'bottom-start',
    name,
    renderValue,
    disabled = false,
    className,
    ...rest
  },
  forwardedRef,
) {
  const [value, setValue] = useControllableState<string | null>({
    value: valueProp,
    defaultValue,
    onChange,
  });
  const [isOpen, setIsOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });
  const [activeIndex, setActiveIndex] = useState<number | null>(null);

  const elementsRef = useRef<Array<HTMLElement | null>>([]);
  const labelsRef = useRef<Array<string | null>>([]);
  const isTypingRef = useRef(false);

  const listboxLabelId = useId();

  const { sections, flat } = useMemo(() => buildSections(options), [options]);
  const selectedIndex = useMemo(
    () => (value == null ? -1 : flat.findIndex((option) => option.value === value)),
    [flat, value],
  );
  const selectedOption = selectedIndex >= 0 ? (flat[selectedIndex] ?? null) : null;

  function handleOpenChange(nextOpen: boolean) {
    if (nextOpen) setActiveIndex(selectedIndex >= 0 ? selectedIndex : null);
    setIsOpen(nextOpen);
  }

  const { refs, floatingStyles, context } = useFloating<HTMLButtonElement>({
    open: isOpen,
    onOpenChange: handleOpenChange,
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

  const click = useClick(context, { enabled: !disabled });
  const role = useRole(context, { role: 'select' });
  const dismiss = useDismiss(context);
  const listNavigation = useListNavigation(context, {
    listRef: elementsRef,
    activeIndex,
    selectedIndex: selectedIndex >= 0 ? selectedIndex : null,
    onNavigate: setActiveIndex,
    loop: true,
    // Always hand the item real focus on open, whatever opened it. That is
    // what scrolls the current value into view in a long list, and it keeps
    // the highlight and the focus ring on the same row from the first frame.
    focusItemOnOpen: true,
  });
  const typeahead = useTypeahead(context, {
    listRef: labelsRef,
    activeIndex,
    selectedIndex: selectedIndex >= 0 ? selectedIndex : null,
    onMatch(index) {
      if (isOpen) {
        setActiveIndex(index);
        return;
      }
      // Closed: type-ahead commits directly, matching a native <select>.
      const option = flat[index];
      if (option && !option.disabled) setValue(option.value);
    },
    onTypingChange(isTyping) {
      isTypingRef.current = isTyping;
    },
  });

  const { getReferenceProps, getFloatingProps, getItemProps } = useInteractions([
    click,
    role,
    dismiss,
    listNavigation,
    typeahead,
  ]);

  function commit(index: number) {
    const option = flat[index];
    if (!option || option.disabled) return;
    setValue(option.value);
    setIsOpen(false);
  }

  const triggerRef = useMergeRefs([refs.setReference, forwardedRef]);
  // The popup outlives `isOpen` by the length of its exit transition. Without
  // this it was unmounted on the same frame the flag flipped, so the exit rules
  // in Select.css could never match and the popup simply vanished.
  const presence = usePresence(isOpen);
  const popupRef = useMergeRefs([refs.setFloating, presence.ref]);
  const [side = 'bottom', align = 'start'] = context.placement.split('-');
  const ariaLabel = rest['aria-label'];
  const ariaLabelledby = rest['aria-labelledby'];

  // `rest` goes *through* the prop getter rather than being spread alongside
  // it. `useClick`, `useDismiss`, `useListNavigation` and `useTypeahead` all
  // return `onClick`/`onKeyDown`/`onKeyUp`/`onFocus`/`onPointerDown`/
  // `onMouseDown`, so a bare `{...rest}` next to the getter loses every one of
  // those consumer handlers key for key. Floating UI chains the handlers it
  // receives as user props instead.
  const triggerProps = getReferenceProps(rest as HTMLProps<HTMLButtonElement>);

  return (
    <>
      <button
        {...triggerProps}
        ref={triggerRef}
        type="button"
        data-stratum="select"
        data-size={sizeProp}
        data-open={isOpen || undefined}
        data-placeholder={selectedOption ? undefined : ''}
        data-full-width={fullWidth || undefined}
        disabled={disabled}
        // Falls back to the consumer's value rather than clobbering it: this
        // sits after the spread, so a bare `invalid || undefined` would strip
        // an `aria-invalid` passed through `rest`. It also keeps the attribute
        // in step with the danger border, which Select.css keys on it.
        aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
        className={clsx('stratum-select', className)}
      >
        {selectedOption?.icon && (
          <span className="stratum-select__icon" aria-hidden="true">
            {selectedOption.icon}
          </span>
        )}
        <span className="stratum-select__value">
          {renderValue ? renderValue(selectedOption) : (selectedOption?.label ?? placeholder)}
        </span>
        <span className="stratum-select__chevron" aria-hidden="true">
          <ChevronDownIcon />
        </span>
      </button>

      {name != null && <input type="hidden" name={name} value={value ?? ''} />}

      {presence.isPresent && (
        <FloatingPortal>
          <FloatingFocusManager context={context} modal={false} initialFocus={-1}>
            <div
              ref={popupRef}
              style={floatingStyles}
              data-stratum="select-popup"
              data-state={presence.state}
              data-side={side}
              data-align={align}
              className="stratum-select__popup"
              {...getFloatingProps({
                'aria-label': ariaLabelledby ? undefined : ariaLabel,
                'aria-labelledby': ariaLabelledby,
                onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
                  // Space only commits when type-ahead is not mid-word;
                  // otherwise a label containing a space could never be typed.
                  if (event.key === 'Enter' || (event.key === ' ' && !isTypingRef.current)) {
                    event.preventDefault();
                    if (activeIndex != null) commit(activeIndex);
                    else setIsOpen(false);
                    return;
                  }
                  // APG: Tab commits the focused option, then moves on. No
                  // preventDefault — the focus move is the point.
                  if (event.key === 'Tab' && activeIndex != null) {
                    commit(activeIndex);
                  }
                },
              })}
            >
              <FloatingList elementsRef={elementsRef} labelsRef={labelsRef}>
                {flat.length === 0 ? (
                  <div className="stratum-select__empty" role="presentation">
                    {emptyLabel}
                  </div>
                ) : (
                  sections.map((section) => {
                    // Indices are assigned by FloatingList from real document
                    // order, so groups can be reordered without any bookkeeping
                    // here — and a group heading never occupies an index.
                    const body = section.options.map((option) => (
                      <SelectOptionRow
                        key={option.value}
                        option={option}
                        activeIndex={activeIndex}
                        selected={option.value === value}
                        getItemProps={getItemProps}
                        onCommit={commit}
                      />
                    ));

                    if (section.label == null) {
                      return <Fragment key={section.key || '__ungrouped'}>{body}</Fragment>;
                    }
                    const groupLabelId = `${listboxLabelId}-group-${section.key}`;
                    return (
                      <div
                        key={section.key}
                        role="group"
                        aria-labelledby={groupLabelId}
                        className="stratum-select__group"
                      >
                        <div id={groupLabelId} role="presentation" className="stratum-select__group-label">
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
    </>
  );
});

interface SelectOptionRowProps {
  option: SelectOption;
  activeIndex: number | null;
  selected: boolean;
  getItemProps: ReturnType<typeof useInteractions>['getItemProps'];
  onCommit: (index: number) => void;
}

function SelectOptionRow({
  option,
  activeIndex,
  selected,
  getItemProps,
  onCommit,
}: SelectOptionRowProps) {
  // `label: null` removes a disabled option from type-ahead, so typing never
  // lands the highlight on a row that cannot be chosen.
  const item = useListItem({ label: option.disabled ? null : option.label });
  const isActive = item.index === activeIndex && item.index !== -1;

  return (
    <div
      ref={item.ref}
      data-stratum="select-option"
      data-active={isActive || undefined}
      data-selected={selected || undefined}
      aria-disabled={option.disabled || undefined}
      tabIndex={isActive ? 0 : -1}
      className="stratum-select__option stratum-focus-inset"
      {...getItemProps({
        active: isActive,
        selected,
        onClick() {
          if (!option.disabled) onCommit(item.index);
        },
      })}
    >
      <span className="stratum-select__option-check" aria-hidden="true">
        {selected && <CheckIcon />}
      </span>
      {option.icon && (
        <span className="stratum-select__option-icon" aria-hidden="true">
          {option.icon}
        </span>
      )}
      <span className="stratum-select__option-text">
        <span className="stratum-select__option-label">{option.label}</span>
        {option.description && (
          <span className="stratum-select__option-description">{option.description}</span>
        )}
      </span>
    </div>
  );
}
