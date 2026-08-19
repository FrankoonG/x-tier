import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useRef,
  type ChangeEvent,
  type InputHTMLAttributes,
  type ReactNode,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import './Checkbox.css';

export type CheckboxSize = 'sm' | 'md' | 'lg';

export interface CheckboxProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'size' | 'type' | 'checked' | 'defaultChecked'
  > {
  /** Controlled checked state. */
  checked?: boolean;
  /** Initial checked state for the uncontrolled case. */
  defaultChecked?: boolean;
  /** Fires in both controlled and uncontrolled modes. */
  onCheckedChange?: (checked: boolean) => void;
  /**
   * Renders the mixed state and reports `aria-checked="mixed"`.
   *
   * `indeterminate` is a DOM *property* with no HTML attribute, so React cannot
   * set it declaratively — it is applied through a ref effect below.
   */
  indeterminate?: boolean;
  size?: CheckboxSize;
  /** Marks the field invalid. Paired with a thicker border, never colour alone. */
  invalid?: boolean;
  /** Secondary line under the label. Wired up as `aria-describedby`. */
  description?: ReactNode;
  /** Visible label. */
  children?: ReactNode;
}

function assignRef<T>(ref: Ref<T> | undefined, node: T | null): void {
  if (typeof ref === 'function') ref(node);
  else if (ref) (ref as { current: T | null }).current = node;
}

/**
 * Tri-state checkbox built on a real `<input type="checkbox">`.
 *
 * WHY A NATIVE INPUT
 * ------------------
 * A `<div role="checkbox">` loses form participation, native `:checked`
 * semantics, autofill, and the `indeterminate` property that assistive tech
 * reads directly. The input is kept at `opacity: 0` stretched over the painted
 * box, so pointer hits, the tab order and the accessibility tree all come from
 * the platform while the visuals are ours.
 *
 * `aria-checked="mixed"` is set only while `indeterminate` is true, so the
 * explicit ARIA value can never disagree with the host language semantics —
 * an override that contradicts the DOM state is worse than no override.
 *
 * PROP ROUTING
 * ------------
 * `className` and `style` land on the root wrapper; `...rest` lands on the
 * input, because the prop type is the input's (`name`, `required`, `form`,
 * `value` must reach the form control, not a decorative wrapper).
 */
export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  {
    checked,
    defaultChecked = false,
    onCheckedChange,
    onChange,
    indeterminate = false,
    size = 'md',
    invalid = false,
    disabled = false,
    description,
    className,
    style,
    children,
    id,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const inputId = id ?? `${generatedId}-input`;
  const descriptionId = `${generatedId}-description`;

  const [isChecked, setChecked] = useControllableState<boolean>({
    value: checked,
    defaultValue: defaultChecked,
    onChange: onCheckedChange,
  });

  const innerRef = useRef<HTMLInputElement | null>(null);
  const setRefs = useCallback(
    (node: HTMLInputElement | null) => {
      innerRef.current = node;
      assignRef(ref, node);
    },
    [ref],
  );

  // Re-applied when the checked state changes too: activating a checkbox clears
  // `indeterminate` in the DOM, and a consumer that keeps the prop true expects
  // the mixed state to survive.
  useEffect(() => {
    const node = innerRef.current;
    if (node) node.indeterminate = indeterminate;
  }, [indeterminate, isChecked]);

  if (
    import.meta.env?.DEV &&
    children == null &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      '[stratum] <Checkbox> without children requires `aria-label` or `aria-labelledby`.',
    );
  }

  const state = indeterminate ? 'mixed' : isChecked ? 'checked' : 'unchecked';
  const hasText = children != null || description != null;

  return (
    <span
      data-stratum="checkbox"
      data-size={size}
      data-state={state}
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
      data-block={description != null || undefined}
      className={clsx('stratum-checkbox', className)}
      style={style}
    >
      <span className="stratum-checkbox__control">
        <input
          {...rest}
          ref={setRefs}
          id={inputId}
          type="checkbox"
          className="stratum-checkbox__input"
          checked={isChecked}
          disabled={disabled}
          aria-checked={indeterminate ? 'mixed' : undefined}
          // Merged, not assigned: written after `{...rest}`, where a bare
          // `undefined` would delete a consumer's own `aria-invalid`.
          aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
          aria-describedby={
            clsx(rest['aria-describedby'], description != null && descriptionId) || undefined
          }
          onChange={(event: ChangeEvent<HTMLInputElement>) => {
            setChecked(event.target.checked);
            onChange?.(event);
          }}
        />
        <span className="stratum-checkbox__box" aria-hidden="true">
          <svg className="stratum-checkbox__check" viewBox="0 0 16 16" fill="none">
            <path
              d="M3.5 8.4 6.4 11.3 12.5 5.2"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
          <svg className="stratum-checkbox__dash" viewBox="0 0 16 16" fill="none">
            <path d="M4 8h8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
          </svg>
        </span>
      </span>

      {hasText && (
        <span className="stratum-checkbox__text">
          {children != null && (
            <label className="stratum-checkbox__label" htmlFor={inputId}>
              {children}
            </label>
          )}
          {description != null && (
            <span className="stratum-checkbox__description" id={descriptionId}>
              {description}
            </span>
          )}
        </span>
      )}
    </span>
  );
});
