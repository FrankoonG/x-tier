import {
  forwardRef,
  useCallback,
  useId,
  useRef,
  type ButtonHTMLAttributes,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import './Switch.css';

export type SwitchSize = 'sm' | 'md';

export interface SwitchProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    'onChange' | 'type' | 'value' | 'name' | 'defaultChecked' | 'defaultValue'
  > {
  /** Controlled on/off state. */
  checked?: boolean;
  /** Initial state for the uncontrolled case. */
  defaultChecked?: boolean;
  /** Fires in both controlled and uncontrolled modes. */
  onCheckedChange?: (checked: boolean) => void;
  size?: SwitchSize;
  invalid?: boolean;
  /** Visible label. */
  children?: ReactNode;
  /** Secondary line under the label, wired up as `aria-describedby`. */
  description?: ReactNode;
  /** Places the label before the control. */
  labelPlacement?: 'start' | 'end';
  /**
   * When set, a hidden checkbox mirrors the state so the switch participates in
   * native form submission and `FormData`.
   */
  name?: string;
  /** Submitted value while checked. Defaults to `'on'`, matching a checkbox. */
  value?: string;
}

function assignRef<T>(ref: Ref<T> | undefined, node: T | null): void {
  if (typeof ref === 'function') ref(node);
  else if (ref) (ref as { current: T | null }).current = node;
}

/**
 * Immediate-effect on/off control.
 *
 * WHY A BUTTON AND NOT A CHECKBOX
 * -------------------------------
 * A switch and a checkbox differ in meaning, not just in paint: a checkbox is a
 * value that will be submitted later, a switch takes effect the moment it is
 * flipped. `role="switch"` on a real `<button>` gets that across, and a button
 * already handles both Space and Enter — the ARIA switch pattern only mandates
 * Space, but operators reach for Enter constantly and a `<div role="switch">`
 * would have to reimplement both.
 *
 * Form participation is opt-in through `name`: a hidden checkbox mirrors the
 * state so `FormData` sees it, without pretending the visible control is a
 * checkbox to assistive tech.
 *
 * The thumb's travel is deliberately NOT multiplied by
 * `--stratum-motion-distance`. That multiplier collapses decorative movement to
 * zero under reduced motion; here the position *is* the state, so collapsing it
 * would leave the switch permanently reading as off. The transition duration
 * still halves, which is what the preference is actually asking for.
 */
export const Switch = forwardRef<HTMLButtonElement, SwitchProps>(function Switch(
  {
    checked,
    defaultChecked = false,
    onCheckedChange,
    size = 'md',
    invalid = false,
    disabled = false,
    children,
    description,
    labelPlacement = 'end',
    name,
    value = 'on',
    className,
    style,
    onClick,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const labelId = `${generatedId}-label`;
  const descriptionId = `${generatedId}-description`;

  const [isChecked, setChecked] = useControllableState<boolean>({
    value: checked,
    defaultValue: defaultChecked,
    onChange: onCheckedChange,
  });

  const controlRef = useRef<HTMLButtonElement | null>(null);
  const setRefs = useCallback(
    (node: HTMLButtonElement | null) => {
      controlRef.current = node;
      assignRef(ref, node);
    },
    [ref],
  );

  if (
    import.meta.env?.DEV &&
    children == null &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error('[stratum] <Switch> without children requires `aria-label` or `aria-labelledby`.');
  }

  const hasText = children != null || description != null;

  const handleClick = (event: ReactMouseEvent<HTMLButtonElement>) => {
    onClick?.(event);
    if (event.defaultPrevented || disabled) return;
    setChecked(!isChecked);
  };

  // Pointer convenience only: the text is not focusable and never becomes the
  // control, so the keyboard model is unchanged. It routes through the
  // control's own click rather than toggling directly, so `handleClick` stays
  // the single activation path — a consumer who vetoes activation with
  // `preventDefault()` in `onClick` is honoured from the label too.
  const handleTextClick = () => {
    if (disabled) return;
    controlRef.current?.focus();
    controlRef.current?.click();
  };

  return (
    <span
      data-stratum="switch"
      data-size={size}
      data-state={isChecked ? 'checked' : 'unchecked'}
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
      data-label-placement={labelPlacement}
      data-block={description != null || undefined}
      className={clsx('stratum-switch', className)}
      style={style}
    >
      <button
        {...rest}
        ref={setRefs}
        type="button"
        role="switch"
        className="stratum-switch__control"
        disabled={disabled}
        aria-checked={isChecked}
        // Merged, not assigned: written after `{...rest}`, where a bare
        // `undefined` would delete a consumer's own `aria-invalid`.
        aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
        aria-labelledby={clsx(rest['aria-labelledby'], children != null && labelId) || undefined}
        aria-describedby={
          clsx(rest['aria-describedby'], description != null && descriptionId) || undefined
        }
        onClick={handleClick}
      >
        <span className="stratum-switch__track" aria-hidden="true">
          <span className="stratum-switch__thumb" />
        </span>
      </button>

      {/* `hidden` keeps the mirror out of layout and out of the accessibility
          tree while still submitting it — only `disabled` removes a control
          from form data. */}
      {name != null && (
        <input
          type="checkbox"
          hidden
          name={name}
          value={value}
          checked={isChecked}
          disabled={disabled}
          readOnly
        />
      )}

      {hasText && (
        <span className="stratum-switch__text" onClick={handleTextClick}>
          {children != null && (
            <span className="stratum-switch__label" id={labelId}>
              {children}
            </span>
          )}
          {description != null && (
            <span className="stratum-switch__description" id={descriptionId}>
              {description}
            </span>
          )}
        </span>
      )}
    </span>
  );
});
