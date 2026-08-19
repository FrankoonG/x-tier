import {
  createContext,
  forwardRef,
  useContext,
  useId,
  useMemo,
  type CSSProperties,
  type ElementType,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import './Field.css';

export type FieldSize = 'sm' | 'md' | 'lg';
export type FieldOrientation = 'vertical' | 'horizontal';
export type FieldAnnounce = 'off' | 'polite' | 'assertive';

/**
 * What a control needs in order to be correctly labelled and described by the
 * Field wrapping it.
 *
 * THE CONTRACT
 * ------------
 * `id`, `describedBy`, `invalid` and `required` are the four values every
 * control is expected to consume. The rest are conveniences.
 *
 * Outside a Field this is a stable, inert object rather than `null`, so a
 * control can destructure it unconditionally and still work standalone:
 *
 * ```tsx
 * const field = useFieldControl();
 * const autoId = useId();
 * const id = idProp ?? field.id ?? autoId;
 * <input
 *   id={id}
 *   aria-describedby={clsx(field.describedBy, ariaDescribedByProp) || undefined}
 *   aria-invalid={field.invalid || undefined}
 *   aria-required={field.required || undefined}
 * />
 * ```
 */
export interface FieldControl {
  /**
   * The id the control MUST adopt, because the Field's `<label for>` already
   * points at it. `undefined` outside a Field, and also in `group` mode where
   * no single control owns the label.
   */
  id: string | undefined;
  /**
   * Space-separated id list for `aria-describedby`, covering the description,
   * the hint and the error. Merge it with any `aria-describedby` the consumer
   * passed rather than overwriting.
   */
  describedBy: string | undefined;
  /** Set `aria-invalid` and paint the error affordance. */
  invalid: boolean;
  /** Set `aria-required`. The visible marker is the Field's job. */
  required: boolean;
  /** The Field is disabled as a whole. */
  disabled: boolean;
  /** Id of the label element, for controls that need `aria-labelledby`. */
  labelId: string | undefined;
  /** Density the Field was rendered at, so a control can match it. */
  size: FieldSize;
}

const STANDALONE: FieldControl = {
  id: undefined,
  describedBy: undefined,
  invalid: false,
  required: false,
  disabled: false,
  labelId: undefined,
  size: 'md',
};

const FieldControlContext = createContext<FieldControl | null>(null);

/**
 * Reads the wiring published by the nearest `<Field>`.
 *
 * Never throws and never returns `null`: controls are used both inside and
 * outside a Field, and a hook that throws would make the standalone case the
 * awkward one.
 */
export function useFieldControl(): FieldControl {
  return useContext(FieldControlContext) ?? STANDALONE;
}

export interface FieldProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  /** Visible label. Omit only when the control is labelled some other way. */
  label?: ReactNode;
  /** Persistent help text, rendered between the label and the control. */
  description?: ReactNode;
  /** Validation message. Its presence is what makes the field invalid. */
  error?: ReactNode;
  /** Short qualifier shown at the end of the label row, e.g. "max 64 chars". */
  hint?: ReactNode;
  /** Shows the marker and publishes `aria-required` to the control. */
  required?: boolean;
  /** Shows the optional label. Ignored when `required` is set. */
  optional?: boolean;
  /** Glyph for the required marker. */
  requiredMarker?: ReactNode;
  /** Text for the optional qualifier. */
  optionalLabel?: ReactNode;
  size?: FieldSize;
  orientation?: FieldOrientation;
  /**
   * Width of the label column in horizontal orientation. Accepts any CSS
   * length; a number is treated as pixels. Defaults to the label's own width.
   *
   * Set `--stratum-field-label-width` on a common ancestor instead to align a
   * whole group of fields on one column without repeating the prop.
   */
  labelWidth?: string | number;
  /**
   * Narrowest the control may become before a horizontal field stacks its
   * label above the control. Any CSS length; a number is treated as pixels.
   * Ignored in vertical orientation.
   */
  controlMinWidth?: string | number;
  /** Dims the field. Disabling the control itself remains the control's job. */
  disabled?: boolean;
  /**
   * The field wraps several controls (a radio group, a set of checkboxes)
   * rather than one. Renders `role="group"` with the label as its accessible
   * name, since a `<label for>` may only point at a single form control.
   */
  group?: boolean;
  /**
   * Politeness of the live region wrapping the error. `polite` waits for a
   * pause, `assertive` interrupts, `off` disables announcement for fields
   * validated on every keystroke, where re-announcing is worse than silence.
   */
  announce?: FieldAnnounce;
  /** Explicit control id, when the consumer wires ids by hand. */
  id?: string;
  /**
   * Either normal children — controls read the wiring from context — or a
   * render function, for controls that take props instead of context.
   */
  children?: ReactNode | ((control: FieldControl) => ReactNode);
}

function has(node: ReactNode): boolean {
  return node != null && node !== false && node !== '';
}

const ErrorIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" fill="none" focusable="false" aria-hidden="true">
    <path
      d="M8 1.8 15 14H1L8 1.8Z"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinejoin="round"
    />
    <path d="M8 6.2v3.2" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    <circle cx="8" cy="11.6" r="0.9" fill="currentColor" />
  </svg>
);

/**
 * Label, control, help text and validation message as one accessible unit.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - `aria-describedby` is published as description → hint → error, matching
 *   both DOM order and the order a screen reader will read them. It is a
 *   single space-separated list because multiple `aria-describedby`
 *   attributes do not merge — a control that overwrites it silently drops the
 *   error message.
 * - The required marker is `aria-hidden`. An asterisk is not announced as
 *   "required" by any screen reader, so the real signal is `aria-required` on
 *   the control; the marker is the visual half of the same information.
 *   `optional` is the opposite case — it has no ARIA equivalent, so it stays
 *   in the accessibility tree as text.
 * - The error lives inside a live region that is present BEFORE the error is:
 *   a live region inserted at the same moment as its content is frequently
 *   not announced at all, because the assistive technology has nothing to
 *   diff against. The empty container costs no layout — it has no content box
 *   until a message arrives, and spacing is applied with margins on the
 *   message rather than a `gap` on the parent, which would otherwise show up
 *   as a phantom gap while the field is valid.
 */
export const Field = forwardRef<HTMLDivElement, FieldProps>(function Field(
  {
    label,
    description,
    error,
    hint,
    required = false,
    optional = false,
    requiredMarker = '*',
    optionalLabel = '(optional)',
    size = 'md',
    orientation = 'vertical',
    labelWidth,
    controlMinWidth = '16rem',
    disabled = false,
    group = false,
    announce = 'polite',
    id: idProp,
    className,
    style,
    children,
    // Pulled out of `rest` so the derived values below can MERGE with them.
    // Written after the spread they would clobber the consumer's value even
    // when ours is `undefined`, because the later key always wins in JSX.
    role: roleProp,
    'aria-labelledby': ariaLabelledByProp,
    'aria-describedby': ariaDescribedByProp,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const controlId = idProp ?? `${uid}control`;
  const labelId = `${uid}label`;
  const descriptionId = `${uid}description`;
  const hintId = `${uid}hint`;
  const errorId = `${uid}error`;

  const hasLabel = has(label);
  const hasDescription = has(description);
  const hasHint = has(hint);
  const hasError = has(error);

  // clsx doubles as an id-list builder: it drops falsy entries and joins with
  // the single space `aria-describedby` expects.
  const describedBy =
    clsx(
      hasDescription && descriptionId,
      hasHint && hintId,
      hasError && errorId,
    ) || undefined;

  const control = useMemo<FieldControl>(
    () => ({
      // In group mode no single control owns the label or the description —
      // the group container does — so children get neither id.
      id: group ? undefined : controlId,
      describedBy: group ? undefined : describedBy,
      invalid: hasError,
      required,
      disabled,
      labelId: hasLabel ? labelId : undefined,
      size,
    }),
    [group, controlId, describedBy, hasError, required, disabled, hasLabel, labelId, size],
  );

  const content = typeof children === 'function' ? children(control) : children;

  const styleVars: Record<string, string | number> = {
    '--_control-min': typeof controlMinWidth === 'number' ? `${controlMinWidth}px` : controlMinWidth,
  };
  if (labelWidth !== undefined) {
    styleVars['--stratum-field-label-width'] =
      typeof labelWidth === 'number' ? `${labelWidth}px` : labelWidth;
  }

  // A `<label for>` may only reference one form control, so a field wrapping a
  // group of them degrades to a plain span named by `aria-labelledby`.
  const LabelTag: ElementType = group ? 'span' : 'label';

  return (
    <FieldControlContext.Provider value={control}>
      <div
        {...rest}
        ref={ref}
        data-stratum="field"
        data-size={size}
        data-orientation={orientation}
        data-invalid={hasError || undefined}
        data-disabled={disabled || undefined}
        data-required={required || undefined}
        // Exposed because it changes what the label IS, not just how it looks:
        // `<label for>` in the normal case, a plain `<span>` here. The disabled
        // styling has to tell those apart — only the former is the accessible
        // name of an inactive control, which is the one thing WCAG 1.4.3 lets
        // us render below 4.5:1.
        data-group={group || undefined}
        className={clsx('stratum-field', className)}
        style={{ ...(styleVars as CSSProperties), ...style }}
        role={group ? 'group' : roleProp}
        aria-labelledby={clsx(group && hasLabel && labelId, ariaLabelledByProp) || undefined}
        aria-describedby={clsx(group && describedBy, ariaDescribedByProp) || undefined}
      >
        {(hasLabel || hasHint) && (
          <div className="stratum-field__header">
            {hasLabel && (
              <LabelTag
                className="stratum-field__label"
                id={labelId}
                htmlFor={group ? undefined : controlId}
              >
                {label}
                {required ? (
                  <span className="stratum-field__marker" aria-hidden="true">
                    {requiredMarker}
                  </span>
                ) : optional ? (
                  <span className="stratum-field__optional">{optionalLabel}</span>
                ) : null}
              </LabelTag>
            )}
            {hasHint && (
              <span className="stratum-field__hint" id={hintId}>
                {hint}
              </span>
            )}
          </div>
        )}

        <div className="stratum-field__body">
          {hasDescription && (
            <div className="stratum-field__description" id={descriptionId}>
              {description}
            </div>
          )}

          <div className="stratum-field__control">{content}</div>

          <div
            className="stratum-field__messages"
            aria-live={announce === 'off' ? undefined : announce}
            aria-atomic={announce === 'off' ? undefined : true}
          >
            {hasError && (
              <div className="stratum-field__error" id={errorId}>
                <span className="stratum-field__error-icon">
                  <ErrorIcon />
                </span>
                <span className="stratum-field__error-text">{error}</span>
              </div>
            )}
          </div>
        </div>
      </div>
    </FieldControlContext.Provider>
  );
});
