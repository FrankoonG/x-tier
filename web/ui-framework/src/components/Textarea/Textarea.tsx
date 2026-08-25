import {
  forwardRef,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  type TextareaHTMLAttributes,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { isAriaInvalid, useMergedRefs } from '../Input/Input';
import { useFieldControl } from '../Field/Field';
import './Textarea.css';

export type TextareaSize = 'sm' | 'md' | 'lg';
export type TextareaVariant = 'default' | 'subtle';

export interface TextareaProps
  extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'defaultValue'> {
  size?: TextareaSize;
  variant?: TextareaVariant;
  invalid?: boolean;
  /** Renders in the monospace face with tabular figures. */
  monospace?: boolean;
  /** Grows with its content between `minRows` and `maxRows`. */
  autoResize?: boolean;
  /** Floor for `autoResize`, and the `rows` attribute when it is off. */
  minRows?: number;
  /** Ceiling for `autoResize`. Past it the textarea scrolls. */
  maxRows?: number;
  fullWidth?: boolean;
  value?: string | number;
  defaultValue?: string | number;
  onValueChange?: (value: string) => void;
}

/** `useLayoutEffect` warns during SSR; there is no layout to read there. */
const useIsomorphicLayoutEffect = typeof document === 'undefined' ? useEffect : useLayoutEffect;

/**
 * Multi-line text field.
 *
 * AUTO-RESIZE
 * -----------
 * Height is measured, not guessed. The element is collapsed to `height: auto`,
 * its `scrollHeight` read, and the result clamped to the row bounds computed
 * from the *resolved* line-height and padding — which is why the CSS sets an
 * explicit `line-height` token rather than leaving it `normal`, where
 * `getComputedStyle` returns a string no arithmetic can use.
 *
 * The work happens in a layout effect so the browser never paints the
 * intermediate `auto` height, and a `ResizeObserver` re-runs it when the
 * element's *width* changes — width only. Reacting to its own height change
 * would be a feedback loop, which is the classic way an auto-growing textarea
 * ends up pinned at max height or oscillating.
 *
 * `field-sizing: content` will eventually make this unnecessary, but it is
 * Chromium-only today, and a textarea that grows in one engine and not another
 * is worse than one that grows everywhere.
 */
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  {
    size: sizeProp,
    variant = 'default',
    invalid: invalidProp,
    monospace = false,
    autoResize = false,
    minRows = 3,
    maxRows = 12,
    fullWidth = false,
    value: valueProp,
    defaultValue,
    onValueChange,
    onChange,
    className,
    disabled = false,
    readOnly = false,
    rows,
    required: requiredProp,
    id: idProp,
    'aria-invalid': ariaInvalidProp,
    'aria-required': ariaRequiredProp,
    'aria-describedby': ariaDescribedByProp,
    ...rest
  },
  ref,
) {
  const field = useFieldControl();
  const size = sizeProp ?? field.size;
  const invalid = invalidProp ?? field.invalid;
  const id = idProp ?? field.id;
  const describedBy = clsx(field.describedBy, ariaDescribedByProp) || undefined;
  const ariaRequired = ariaRequiredProp ?? (requiredProp || field.required || undefined);
  const innerRef = useRef<HTMLTextAreaElement | null>(null);
  const mergedRef = useMergedRefs<HTMLTextAreaElement>(innerRef, ref);
  const lastWidthRef = useRef<number | null>(null);

  const [value, setValue] = useControllableState<string>({
    value: valueProp === undefined ? undefined : String(valueProp),
    defaultValue: defaultValue === undefined ? '' : String(defaultValue),
    onChange: onValueChange,
  });

  // An explicit `aria-invalid` wins over the `invalid` prop in BOTH channels,
  // so the painted danger state can never disagree with the emitted attribute.
  const isInvalid = ariaInvalidProp !== undefined ? isAriaInvalid(ariaInvalidProp) : invalid;
  const lowRows = Math.max(1, minRows);
  const highRows = Math.max(lowRows, maxRows);

  const resize = useCallback(() => {
    const element = innerRef.current;
    if (!element) return;

    if (!autoResize) {
      element.style.height = '';
      element.style.overflowY = '';
      return;
    }

    const styles = window.getComputedStyle(element);

    let lineHeight = Number.parseFloat(styles.lineHeight);
    if (!Number.isFinite(lineHeight)) {
      const fontSize = Number.parseFloat(styles.fontSize);
      lineHeight = Number.isFinite(fontSize) ? fontSize * 1.5 : 20;
    }

    const paddingBlock =
      (Number.parseFloat(styles.paddingTop) || 0) + (Number.parseFloat(styles.paddingBottom) || 0);
    const borderBlock =
      (Number.parseFloat(styles.borderTopWidth) || 0) +
      (Number.parseFloat(styles.borderBottomWidth) || 0);

    // scrollHeight includes padding but never border, so a border-box height
    // has to add the border back on.
    element.style.height = 'auto';
    const contentHeight = element.scrollHeight;

    const min = lowRows * lineHeight + paddingBlock;
    const max = highRows * lineHeight + paddingBlock;
    const clamped = Math.min(Math.max(contentHeight, min), max);
    const isBorderBox = styles.boxSizing === 'border-box';

    element.style.height = `${Math.round(isBorderBox ? clamped + borderBlock : clamped - paddingBlock)}px`;
    element.style.overflowY = contentHeight > max ? 'auto' : 'hidden';
  }, [autoResize, lowRows, highRows]);

  // `size` and `monospace` are dependencies even though `resize` does not close
  // over them: both change the *resolved* line-height, padding or font face
  // that the measurement reads, and neither changes the element's inline size,
  // so the ResizeObserver below cannot pick them up.
  useIsomorphicLayoutEffect(() => {
    resize();
  }, [resize, value, size, monospace]);

  useEffect(() => {
    const element = innerRef.current;
    if (!autoResize || !element || typeof ResizeObserver === 'undefined') return;

    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      const box = entry.borderBoxSize?.[0];
      const width = box ? box.inlineSize : entry.contentRect.width;
      // Width only. Observing height here would re-trigger on the very change
      // this callback just made.
      if (lastWidthRef.current === width) return;
      lastWidthRef.current = width;
      resize();
    });

    observer.observe(element);
    return () => observer.disconnect();
  }, [autoResize, resize]);

  return (
    <textarea
      {...rest}
      ref={mergedRef}
      id={id}
      data-stratum="textarea"
      data-size={size}
      data-variant={variant}
      data-invalid={isInvalid || undefined}
      data-monospace={monospace || undefined}
      data-auto-resize={autoResize || undefined}
      data-readonly={readOnly || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-textarea', className)}
      value={value}
      rows={rows ?? lowRows}
      disabled={disabled}
      readOnly={readOnly}
      required={requiredProp}
      aria-invalid={ariaInvalidProp ?? (invalid || undefined)}
      aria-required={ariaRequired}
      aria-describedby={describedBy}
      onChange={(event) => {
        setValue(event.target.value);
        onChange?.(event);
      }}
    />
  );
});
