import {
  forwardRef,
  useId,
  useRef,
  type CSSProperties,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import './SegmentedControl.css';

export type SegmentedControlSize = 'sm' | 'md' | 'lg';

export interface SegmentedControlItem {
  /** Value reported by `onValueChange`. Must be unique within the control. */
  value: string;
  /** Visible label. Omit for an icon-only segment and supply `label` instead. */
  children?: ReactNode;
  /** Leading adornment, hidden from assistive tech. */
  icon?: ReactNode;
  /** Accessible name. Required when the segment renders icon-only. */
  label?: string;
  disabled?: boolean;
}

export interface SegmentedControlProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue' | 'defaultChecked'> {
  items: SegmentedControlItem[];
  /** Controlled selected value. */
  value?: string | null;
  /** Initial selected value for the uncontrolled case. */
  defaultValue?: string | null;
  /** Fires in both controlled and uncontrolled modes. */
  onValueChange?: (value: string) => void;
  size?: SegmentedControlSize;
  /** Stretches the control to fill its container. Segments stay equal width. */
  fullWidth?: boolean;
  disabled?: boolean;
  /** Accessible name for the whole group. */
  label?: string;
}

function cssVars(vars: Record<string, string | number>): CSSProperties {
  return vars as CSSProperties;
}

/**
 * Horizontal exclusive selector — the compact form of a radio group.
 *
 * THE INDICATOR
 * -------------
 * Segments are laid out as `repeat(n, minmax(0, 1fr))`, so every segment is the
 * same width and the active one is at `index * 100%` of the indicator's own
 * width. That makes the slide a pure `translate` driven by two custom
 * properties (`--_count`, `--_index`) with no measurement, no ResizeObserver
 * and no layout animation — the indicator cannot desync from the segment it is
 * tracking, and a resize repositions it for free because the offset is
 * expressed as a percentage.
 *
 * The offset is deliberately NOT multiplied by `--stratum-motion-distance`:
 * that multiplier exists to cancel decorative movement, and here the position
 * *is* the selected state. Reduced motion halves the transition instead.
 *
 * KEYBOARD MODEL
 * --------------
 * `role="radiogroup"` with `role="radio"` segments: one tab stop, arrow keys
 * move focus and selection together and wrap, `Home`/`End` jump to the ends,
 * and disabled segments are skipped rather than focused-and-ignored.
 */
export const SegmentedControl = forwardRef<HTMLDivElement, SegmentedControlProps>(
  function SegmentedControl(
    {
      items,
      value,
      defaultValue = null,
      onValueChange,
      size = 'md',
      fullWidth = false,
      disabled = false,
      label = 'Options',
      className,
      style,
      onKeyDown,
      ...rest
    },
    ref,
  ) {
    const generatedId = useId();
    const [selected, setSelected] = useControllableState<string | null>({
      value,
      defaultValue,
      onChange: (next) => {
        if (next !== null) onValueChange?.(next);
      },
    });

    const buttonsRef = useRef<Array<HTMLButtonElement | null>>([]);

    const activeIndex = items.findIndex((item) => item.value === selected);
    const enabledIndexes = items
      .map((item, index) => (item.disabled || disabled ? -1 : index))
      .filter((index) => index !== -1);

    // Exactly one segment is reachable with Tab: the selected one, or the first
    // enabled one when nothing is selected yet. The selection must also be
    // enabled — a disabled button holding the only tabIndex={0} is unfocusable,
    // which would strand the whole control outside the tab order.
    const rovingIndex =
      activeIndex !== -1 && enabledIndexes.includes(activeIndex)
        ? activeIndex
        : (enabledIndexes[0] ?? -1);

    const focusAndSelect = (index: number) => {
      const item = items[index];
      if (!item) return;
      buttonsRef.current[index]?.focus();
      setSelected(item.value);
    };

    const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
      onKeyDown?.(event);
      if (event.defaultPrevented || disabled) return;
      if (event.altKey || event.ctrlKey || event.metaKey) return;
      if (enabledIndexes.length === 0) return;

      const current = buttonsRef.current.indexOf(event.target as HTMLButtonElement);
      const position = enabledIndexes.indexOf(current);
      if (position === -1) return;

      const rtl =
        event.currentTarget instanceof HTMLElement &&
        getComputedStyle(event.currentTarget).direction === 'rtl';
      const last = enabledIndexes.length - 1;
      let nextPosition: number;

      switch (event.key) {
        case 'ArrowDown':
          nextPosition = position === last ? 0 : position + 1;
          break;
        case 'ArrowUp':
          nextPosition = position === 0 ? last : position - 1;
          break;
        case 'ArrowRight':
          nextPosition = rtl
            ? position === 0
              ? last
              : position - 1
            : position === last
              ? 0
              : position + 1;
          break;
        case 'ArrowLeft':
          nextPosition = rtl
            ? position === last
              ? 0
              : position + 1
            : position === 0
              ? last
              : position - 1;
          break;
        case 'Home':
          nextPosition = 0;
          break;
        case 'End':
          nextPosition = last;
          break;
        default:
          return;
      }

      const nextIndex = enabledIndexes[nextPosition];
      if (nextIndex === undefined) return;

      event.preventDefault();
      focusAndSelect(nextIndex);
    };

    return (
      <div
        {...rest}
        ref={ref}
        role="radiogroup"
        data-stratum="segmented-control"
        data-size={size}
        data-full-width={fullWidth || undefined}
        data-disabled={disabled || undefined}
        data-empty={activeIndex === -1 || undefined}
        className={clsx('stratum-segmented', className)}
        style={{
          ...cssVars({ '--_count': items.length, '--_index': Math.max(activeIndex, 0) }),
          ...style,
        }}
        aria-label={rest['aria-label'] ?? (rest['aria-labelledby'] ? undefined : label)}
        aria-disabled={rest['aria-disabled'] ?? (disabled || undefined)}
        aria-orientation="horizontal"
        onKeyDown={handleKeyDown}
      >
        <span className="stratum-segmented__indicator" aria-hidden="true" />

        {items.map((item, index) => {
          const isActive = item.value === selected;
          const isDisabled = disabled || item.disabled === true;
          const isIconOnly = item.children == null && item.icon != null;

          if (import.meta.env?.DEV && isIconOnly && !item.label) {
            console.error(
              `[stratum] <SegmentedControl> item "${item.value}" renders icon-only and needs \`label\`.`,
            );
          }

          return (
            <button
              key={item.value}
              ref={(node) => {
                buttonsRef.current[index] = node;
              }}
              id={`${generatedId}-${index}`}
              type="button"
              role="radio"
              data-stratum="segmented-control-item"
              data-state={isActive ? 'checked' : 'unchecked'}
              data-icon-only={isIconOnly || undefined}
              // The ring is drawn inside the segment: an offset ring would be
              // clipped against the container's 2px padding.
              className="stratum-segmented__item stratum-focus-inset"
              aria-checked={isActive}
              aria-label={item.label}
              disabled={isDisabled}
              tabIndex={index === rovingIndex ? 0 : -1}
              onClick={() => {
                if (isDisabled) return;
                setSelected(item.value);
              }}
            >
              {item.icon != null && (
                <span className="stratum-segmented__icon" aria-hidden="true">
                  {item.icon}
                </span>
              )}
              {item.children != null && (
                <span className="stratum-segmented__label">{item.children}</span>
              )}
            </button>
          );
        })}
      </div>
    );
  },
);
