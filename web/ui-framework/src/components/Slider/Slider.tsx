import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import './Slider.css';

export type SliderSize = 'sm' | 'md';
export type SliderTooltipMode = 'auto' | 'always' | 'never';

/** A single value, or `[start, end]` for a two-thumb range. */
export type SliderValue = number | [number, number];

export interface SliderMark {
  value: number;
  /** Optional caption rendered under the track. */
  label?: ReactNode;
}

export interface SliderProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue' | 'defaultChecked'> {
  /** Controlled value. An array turns the slider into a two-thumb range. */
  value?: SliderValue;
  /** Initial value for the uncontrolled case. An array selects range mode. */
  defaultValue?: SliderValue;
  /** Fires on every change, in both controlled and uncontrolled modes. */
  onValueChange?: (value: SliderValue) => void;
  /** Fires once the interaction settles — pointer release or key up. */
  onValueCommit?: (value: SliderValue) => void;
  min?: number;
  max?: number;
  step?: number;
  /** Distance for PageUp/PageDown and Shift+Arrow. Defaults to a tenth of the range. */
  largeStep?: number;
  /** Minimum gap between range thumbs, counted in steps. */
  minStepsBetweenThumbs?: number;
  disabled?: boolean;
  invalid?: boolean;
  size?: SliderSize;
  /** Tick marks drawn on the track, with optional captions. */
  marks?: SliderMark[];
  /** `auto` shows the bubble while dragging, hovering or focused. */
  tooltip?: SliderTooltipMode;
  /** Formats the bubble and `aria-valuetext`. Raw numbers are used otherwise. */
  formatValue?: (value: number) => string;
  /** Accessible name for the slider. */
  label?: string;
  /** Accessible name of the lower thumb in range mode. */
  labelMinimum?: string;
  /** Accessible name of the upper thumb in range mode. */
  labelMaximum?: string;
}

function cssVars(vars: Record<string, string | number>): CSSProperties {
  return vars as CSSProperties;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(Math.max(value, low), high);
}

/** Decimal places implied by the step, so 0.1 + 0.2 never leaks into a label. */
function decimalsOf(step: number): number {
  if (!Number.isFinite(step) || Number.isInteger(step)) return 0;
  const text = String(step);
  const exponent = text.indexOf('e-');
  if (exponent !== -1) return Math.min(Number(text.slice(exponent + 2)) || 0, 10);
  const dot = text.indexOf('.');
  return dot === -1 ? 0 : Math.min(text.length - dot - 1, 10);
}

function snap(raw: number, min: number, max: number, step: number): number {
  if (!(step > 0)) return clamp(raw, min, max);
  const steps = Math.round((raw - min) / step);
  const snapped = min + steps * step;
  return clamp(Number(snapped.toFixed(decimalsOf(step))), min, max);
}

function toArray(value: SliderValue): number[] {
  return Array.isArray(value) ? [value[0], value[1]] : [value];
}

/** Keys whose release ends an interaction and fires `onValueCommit`. */
const COMMIT_KEYS = ['PageUp', 'PageDown', 'Home', 'End'];

/**
 * Single-value and two-thumb range slider.
 *
 * WHY NOT `<input type="range">`
 * ------------------------------
 * The native control cannot express two thumbs, and stacking two of them means
 * one is always unreachable by pointer over half the track. So the thumbs are
 * `role="slider"` elements and every keyboard interaction the native control
 * would have given us is implemented explicitly: arrows, Shift+arrows,
 * PageUp/PageDown, Home/End, and per-thumb `aria-valuemin`/`aria-valuemax` that
 * narrow to the neighbouring thumb so a screen reader reads the real bounds.
 *
 * MEASUREMENT
 * -----------
 * Position is a CSS percentage of the track (`--_at`), so a container resize
 * repositions everything with no JS. The only measurement is the track rect,
 * read once per pointer event to convert a client coordinate into a value —
 * there is no way to avoid that, and it is not layout-dependent state.
 *
 * The value bubble is anchored with plain CSS rather than a floating-ui
 * positioner on purpose: it is a non-dismissable, non-portalled child of the
 * thumb, and a JS positioner would have to be re-run on every pointer move,
 * lagging one frame behind the thumb it is supposed to be attached to.
 */
export const Slider = forwardRef<HTMLDivElement, SliderProps>(function Slider(
  {
    value,
    defaultValue,
    onValueChange,
    onValueCommit,
    min = 0,
    max = 100,
    step = 1,
    largeStep,
    minStepsBetweenThumbs = 0,
    disabled = false,
    invalid = false,
    size = 'md',
    marks,
    tooltip = 'auto',
    formatValue,
    label,
    labelMinimum = 'Minimum',
    labelMaximum = 'Maximum',
    className,
    style,
    // Pulled out of `rest` so they never land on the root: when `!isRange` the
    // root has no role, so an `aria-label` there is ignored by assistive tech.
    // They are routed to the thumb (the `role="slider"`) instead, and put back
    // on the root only in range mode, where `role="group"` can carry a name.
    'aria-label': ariaLabel,
    'aria-labelledby': ariaLabelledBy,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const isRange = Array.isArray(value ?? defaultValue);
  const span = max - min;

  if (import.meta.env?.DEV && !label && !ariaLabel && !ariaLabelledBy) {
    console.error(
      '[stratum] <Slider> requires `label`, `aria-label` or `aria-labelledby` — the accessible name is set on the thumb.',
    );
  }

  /** Internal `number[]` back to the public single-or-tuple shape. */
  const toPublic = (next: number[]): SliderValue =>
    isRange ? ([next[0] ?? min, next[1] ?? max] as [number, number]) : (next[0] ?? min);

  const [rawValues, setValues] = useControllableState<number[]>({
    value: value === undefined ? undefined : toArray(value),
    defaultValue: toArray(defaultValue ?? (isRange ? [min, max] : min)),
    onChange: (next) => {
      onValueChange?.(toPublic(next));
    },
  });

  /** Snapped, clamped, ordered view of the value. Never trusts the input. */
  const values = useMemo(() => {
    const snapped = rawValues.map((v) => snap(v, min, max, step));
    if (snapped.length === 0) return [min];
    if (snapped.length < 2) return snapped;
    const gap = step * minStepsBetweenThumbs;
    const lower = snapped[0] ?? min;
    const upper = snapped[1] ?? max;
    return lower <= upper - gap
      ? [lower, upper]
      : [clamp(Math.min(lower, upper - gap), min, max), upper];
  }, [rawValues, min, max, step, minStepsBetweenThumbs]);

  const trackRef = useRef<HTMLDivElement | null>(null);
  const thumbRefs = useRef<Array<HTMLDivElement | null>>([]);
  const [draggingIndex, setDraggingIndex] = useState<number | null>(null);

  /**
   * The last value actually applied, readable synchronously.
   *
   * `pointermove` is a continuous-priority event, so its state update is
   * scheduled at DefaultLane and flushed in a later task; `pointerup` is
   * discrete and only flushes sync-lane work first. At the end of a fast drag —
   * or any drag under main-thread load — the two arrive in the same task and
   * the re-render has not happened, so reading `values` from the render closure
   * would commit the second-to-last value after `onValueChange` already
   * reported the newest one.
   */
  const latestValuesRef = useRef<number[]>(values);
  useEffect(() => {
    latestValuesRef.current = values;
  }, [values]);

  const commit = useEventCallback(onValueCommit);

  const ratioOf = (v: number) => (span === 0 ? 0 : clamp((v - min) / span, 0, 1));

  /** Bounds for one thumb, narrowed by its neighbours. */
  const boundsFor = useCallback(
    (index: number): [number, number] => {
      const gap = step * minStepsBetweenThumbs;
      const low = index > 0 ? Math.max(min, (values[index - 1] ?? min) + gap) : min;
      const high = index < values.length - 1 ? Math.min(max, (values[index + 1] ?? max) - gap) : max;
      return [low, Math.max(low, high)];
    },
    [values, min, max, step, minStepsBetweenThumbs],
  );

  /** Applies a raw value to one thumb and returns the resulting tuple. */
  const applyThumb = useCallback(
    (index: number, raw: number): number[] | null => {
      const [low, high] = boundsFor(index);
      const next = clamp(snap(raw, min, max, step), low, high);
      if (values[index] === next) return null;
      const result = [...values];
      result[index] = next;
      return result;
    },
    [boundsFor, values, min, max, step],
  );

  const moveThumb = (index: number, raw: number) => {
    const next = applyThumb(index, raw);
    if (next) {
      latestValuesRef.current = next;
      setValues(next);
    }
  };

  const valueFromPointer = useCallback(
    (clientX: number): number => {
      const track = trackRef.current;
      if (!track) return min;
      const rect = track.getBoundingClientRect();
      if (rect.width === 0) return min;
      let ratio = (clientX - rect.left) / rect.width;
      if (getComputedStyle(track).direction === 'rtl') ratio = 1 - ratio;
      return min + clamp(ratio, 0, 1) * span;
    },
    [min, span],
  );

  /** Nearest thumb, biased toward the one that can actually reach the target. */
  const closestThumb = useCallback(
    (target: number): number => {
      if (values.length < 2) return 0;
      const first = values[0] ?? min;
      const second = values[1] ?? max;
      if (target < first) return 0;
      if (target > second) return 1;
      return target - first <= second - target ? 0 : 1;
    },
    [values, min, max],
  );

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled || event.button !== 0) return;
    const target = valueFromPointer(event.clientX);
    const index = closestThumb(target);
    event.currentTarget.setPointerCapture(event.pointerId);
    // Prevents the browser from starting a text selection or a scroll gesture
    // that would fight the drag.
    event.preventDefault();
    setDraggingIndex(index);
    thumbRefs.current[index]?.focus();
    moveThumb(index, target);
  };

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (draggingIndex === null) return;
    moveThumb(draggingIndex, valueFromPointer(event.clientX));
  };

  const handlePointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (draggingIndex === null) return;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDraggingIndex(null);
    commit(toPublic(latestValuesRef.current));
  };

  const handleThumbKeyDown = (index: number) => (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (disabled) return;
    if (event.altKey || event.ctrlKey || event.metaKey) return;

    const big = largeStep ?? Math.max(step, span / 10);
    const delta = event.shiftKey ? big : step;
    const current = values[index] ?? min;
    const [low, high] = boundsFor(index);
    const rtl =
      event.currentTarget instanceof HTMLElement &&
      getComputedStyle(event.currentTarget).direction === 'rtl';
    let next: number;

    switch (event.key) {
      case 'ArrowRight':
        next = current + (rtl ? -delta : delta);
        break;
      case 'ArrowLeft':
        next = current - (rtl ? -delta : delta);
        break;
      case 'ArrowUp':
        next = current + delta;
        break;
      case 'ArrowDown':
        next = current - delta;
        break;
      case 'PageUp':
        next = current + big;
        break;
      case 'PageDown':
        next = current - big;
        break;
      // Home/End go to the thumb's own bounds, not the track's: on a range,
      // pushing the lower thumb past the upper one is never what was meant.
      case 'Home':
        next = low;
        break;
      case 'End':
        next = high;
        break;
      default:
        return;
    }

    event.preventDefault();
    moveThumb(index, next);
  };

  const handleThumbKeyUp = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (disabled) return;
    if (event.key.startsWith('Arrow') || COMMIT_KEYS.includes(event.key)) {
      commit(toPublic(values));
    }
  };

  const format = (v: number) => (formatValue ? formatValue(v) : String(v));

  const fillFrom = values.length > 1 ? ratioOf(values[0] ?? min) : 0;
  const fillTo = ratioOf(values[values.length - 1] ?? min);

  const thumbLabel = (index: number): string | undefined => {
    // A single thumb takes the whole slider's name. `aria-labelledby` wins over
    // any label text, so no `aria-label` is emitted alongside it.
    if (!isRange) return ariaLabelledBy ? undefined : (label ?? ariaLabel);
    const own = index === 0 ? labelMinimum : labelMaximum;
    return label ? `${label} ${own}` : own;
  };

  const hasMarkLabels = marks?.some((mark) => mark.label != null) ?? false;

  return (
    <div
      {...rest}
      ref={ref}
      // A wrapper role only earns its place when there really are two controls
      // to group. A single-thumb slider carries everything on the thumb, and an
      // extra unnamed group is just one more thing to arrow past. Falls back to
      // the consumer's own `role` rather than deleting it: this is written after
      // `{...rest}`, where a bare `undefined` still wins.
      role={isRange ? 'group' : rest.role}
      data-stratum="slider"
      data-size={size}
      data-range={isRange || undefined}
      data-disabled={disabled || undefined}
      data-invalid={invalid || undefined}
      data-tooltip={tooltip}
      data-dragging={draggingIndex !== null || undefined}
      className={clsx('stratum-slider', className)}
      style={style}
      aria-label={isRange ? (ariaLabel ?? (ariaLabelledBy ? undefined : label)) : undefined}
      aria-labelledby={isRange ? ariaLabelledBy : undefined}
      aria-disabled={rest['aria-disabled'] ?? (disabled || undefined)}
    >
      <div
        ref={trackRef}
        className="stratum-slider__control"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerUp}
      >
        <div className="stratum-slider__rail" />
        <div
          className="stratum-slider__fill"
          style={cssVars({ '--_from': fillFrom, '--_to': fillTo })}
        />

        {marks?.map((mark, index) => {
          const at = ratioOf(mark.value);
          return (
            <span
              key={`mark-${index}`}
              className="stratum-slider__mark"
              data-state={at >= fillFrom && at <= fillTo ? 'filled' : 'empty'}
              style={cssVars({ '--_at': at })}
              aria-hidden="true"
            />
          );
        })}

        {values.map((v, index) => {
          const [low, high] = boundsFor(index);
          return (
            <div
              key={`thumb-${index}`}
              ref={(node) => {
                thumbRefs.current[index] = node;
              }}
              id={`${generatedId}-thumb-${index}`}
              role="slider"
              className="stratum-slider__thumb"
              data-dragging={draggingIndex === index || undefined}
              style={cssVars({ '--_at': ratioOf(v) })}
              tabIndex={disabled ? -1 : 0}
              aria-orientation="horizontal"
              aria-valuemin={low}
              aria-valuemax={high}
              aria-valuenow={v}
              aria-valuetext={formatValue ? formatValue(v) : undefined}
              aria-label={thumbLabel(index)}
              aria-labelledby={isRange ? undefined : ariaLabelledBy}
              aria-disabled={disabled || undefined}
              aria-invalid={invalid || undefined}
              onKeyDown={handleThumbKeyDown(index)}
              onKeyUp={handleThumbKeyUp}
            >
              {tooltip !== 'never' && (
                <span className="stratum-slider__tooltip" aria-hidden="true">
                  {format(v)}
                </span>
              )}
            </div>
          );
        })}
      </div>

      {hasMarkLabels && (
        <div className="stratum-slider__mark-labels" aria-hidden="true">
          {marks?.map((mark, index) =>
            mark.label == null ? null : (
              <span
                key={`mark-label-${index}`}
                className="stratum-slider__mark-label"
                style={cssVars({ '--_at': ratioOf(mark.value) })}
              >
                {mark.label}
              </span>
            ),
          )}
        </div>
      )}
    </div>
  );
});
