import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { Checkbox } from '../../components/Checkbox/Checkbox';
import { useFieldControl } from '../../components/Field/Field';
import { Select, type SelectOption } from '../../components/Select/Select';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { AddressInput, type AddressKind } from '../AddressInput/AddressInput';
import { PortRangeInput } from '../PortRangeInput/PortRangeInput';
import { CredentialField } from '../CredentialField/CredentialField';
import { DurationInput, type DurationUnit } from '../DurationInput/DurationInput';
import { ByteSizeInput, type ByteUnit } from '../ByteSizeInput/ByteSizeInput';
import { joinIds, type NetControlSize } from '../_shared/netField';
import { statusGlyph } from '../../components/_shared/statusIcons';
import './ProtocolPicker.css';

/** Value carried by one declared field. `null` means not set — never zero. */
export type FieldValue = string | number | boolean | null;

export type FieldValues = Record<string, FieldValue>;

/**
 * The control a field is rendered with.
 *
 * `address`, `ports`, `duration`, `bytes` and `secret` delegate to the other
 * members of this input family, so a registry-declared field gets the same
 * validation and the same unobserved-versus-zero discipline as a hand-written
 * one.
 */
export type FieldKind =
  | 'text'
  | 'number'
  | 'secret'
  | 'select'
  | 'boolean'
  | 'address'
  | 'ports'
  | 'duration'
  | 'bytes'
  | 'custom';

export interface FieldOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface FieldRenderContext {
  /** Id to place on the control, already linked to the label. */
  id: string;
  value: FieldValue;
  setValue: (next: FieldValue) => void;
  /** Ids of the description elements this control should reference. */
  describedBy: string | undefined;
  /** Report validity back to the picker so it can aggregate. */
  setValid: (valid: boolean) => void;
  disabled: boolean;
  size: NetControlSize;
}

export interface FieldSpec {
  /** Key within the values object. Must be unique inside one protocol. */
  name: string;
  /** Visible label. Required — an unlabelled field is not usable. */
  label: string;
  /** Default `'text'`. */
  kind?: FieldKind;
  /** Longer help text, rendered under the control and linked with `aria-describedby`. */
  description?: string;
  placeholder?: string;
  required?: boolean;
  /** Applied when the protocol is selected and the field has no value yet. */
  defaultValue?: FieldValue;

  /* -- Per-kind configuration ------------------------------------------- */
  /** `select` only. */
  options?: FieldOption[];
  /** `address` only. */
  accept?: readonly AddressKind[];
  /** `duration` only. */
  durationUnits?: readonly DurationUnit[];
  /** `bytes` only. */
  byteUnits?: readonly ByteUnit[];
  /** `bytes` only. Default `'binary'`. */
  base?: 'binary' | 'decimal';
  /** `number`, `duration` (ms) and `bytes` (bytes). */
  min?: number;
  max?: number;
  /** `secret` only — keep the value out of the DOM while masked. */
  redactWhenHidden?: boolean;
  /** `secret` only. */
  revealTimeout?: number;

  /** `custom` only. The escape hatch for anything the registry cannot describe. */
  render?: (context: FieldRenderContext) => ReactNode;
}

export interface ProtocolDescriptor {
  id: string;
  label: string;
  description?: string;
  /** Decorative; the label carries the meaning. */
  icon?: ReactNode;
  fields?: FieldSpec[];
  disabled?: boolean;
}

export interface ProtocolValidation {
  /** The selected protocol id, or `null`. */
  protocol: string | null;
  /** `true` when a protocol is selected and every declared field is satisfied. */
  valid: boolean;
  /** Per-field validity, keyed by field name. */
  fields: Record<string, boolean>;
  /** Names of required fields that are still empty. */
  missing: string[];
}

export interface ProtocolPickerProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue'> {
  /** The registry. The component knows nothing about any entry in it. */
  protocols: readonly ProtocolDescriptor[];

  /** Selected protocol id. `null` for none. */
  value?: string | null;
  defaultValue?: string | null;
  onChange?: (protocolId: string | null) => void;

  /** Values for the selected protocol's declared fields. */
  values?: FieldValues;
  defaultValues?: FieldValues;
  onValuesChange?: (values: FieldValues) => void;

  onValidChange?: (validation: ProtocolValidation) => void;

  size?: NetControlSize;
  disabled?: boolean;
  required?: boolean;

  /**
   * Carry values whose field NAME appears in both the old and new protocol
   * across a protocol change. Off by default: two registries can use `host` for
   * different things, and silently reusing a value across that boundary is a
   * harder bug to see than retyping it.
   */
  keepSharedValues?: boolean;

  /** Render the declared fields. Default `true`. */
  showFields?: boolean;
  /** Render the selected protocol's description. Default `true`. */
  showDescription?: boolean;
  /**
   * Show "required" errors on empty declared fields. Default `false`.
   *
   * Off by default because the picker cannot know when the operator is done:
   * selecting a protocol would otherwise paint every one of its required fields
   * red before a single one has been touched. The form owner knows about submit,
   * so it flips this after a failed attempt; until then `onValidChange` reports
   * `missing` silently. Fields with their OWN rules — an address, a port list —
   * still report those on blur through their own controls.
   */
  showRequiredErrors?: boolean;

  /* -- Copy ---------------------------------------------------------------- */
  /** Accessible name for the protocol selector. Default `'Protocol'`. */
  protocolLabel?: string;
  /** Option shown when nothing is selected. Default `'Select a protocol…'`. */
  placeholderLabel?: string;
  /** Accessible name for the declared-field group. Receives the protocol label. */
  fieldsLabel?: (protocolLabel: string) => string;
  /** Suffix marking a required field. Default `'(required)'`, visually hidden. */
  requiredLabel?: string;
  /** Message for an empty required field. Receives the field label. */
  requiredMessage?: (fieldLabel: string) => string;
  /** Message when no protocol is selected and one is required. */
  protocolRequiredMessage?: string;

  wrapperClassName?: string;
}

const DEFAULT_FIELDS_LABEL = (protocolLabel: string) => `${protocolLabel} settings`;
const DEFAULT_REQUIRED_MESSAGE = (fieldLabel: string) => `${fieldLabel} is required.`;

function isEmptyValue(value: FieldValue): boolean {
  return value === null || value === undefined || value === '';
}

function defaultsFor(protocol: ProtocolDescriptor | undefined): FieldValues {
  const out: FieldValues = {};
  for (const field of protocol?.fields ?? []) {
    out[field.name] = field.defaultValue ?? (field.kind === 'boolean' ? false : null);
  }
  return out;
}

/**
 * A transport selector driven entirely by a registry the consumer supplies.
 *
 * WHY A REGISTRY AND NOT A UNION OF PROTOCOL NAMES
 * ------------------------------------------------
 * The obvious version of this component hardcodes the protocols it knows and
 * grows a `switch` for each one's settings. That design fails twice: the
 * library gains domain knowledge it has no business holding, and every new
 * transport becomes a library release rather than a data change in the app.
 *
 * Here the consumer passes `protocols`, each entry declaring its own fields,
 * and the component renders them. It cannot name a single protocol, and adding
 * one requires no change to this file. That is the whole point.
 *
 * WHAT THE DECLARED FIELDS GET FOR FREE
 * -------------------------------------
 * `kind: 'address'` renders the family's `AddressInput`, with its real IPv6
 * parser; `'ports'` gets range merging and a live port total; `'duration'` and
 * `'bytes'` get unit selectors that emit milliseconds and bytes and never turn
 * a blank field into a zero; `'secret'` gets masking, a reveal timeout and
 * DOM redaction. A registry entry therefore inherits the whole family's
 * validation discipline without the consumer wiring any of it.
 *
 * PROTOCOL CHANGE RESETS THE FIELDS
 * ---------------------------------
 * Selecting a different protocol replaces the values with the new protocol's
 * declared defaults. Field NAMES are not a stable identity across a registry —
 * two protocols can both declare `host` and mean different things — so
 * carrying values over is opt-in (`keepSharedValues`) rather than automatic.
 */
export const ProtocolPicker = forwardRef<HTMLDivElement, ProtocolPickerProps>(
  function ProtocolPicker(
    {
      protocols,
      value,
      defaultValue = null,
      onChange,
      values,
      defaultValues,
      onValuesChange,
      onValidChange,

      size: sizeProp,
      disabled = false,
      required: requiredProp,
      keepSharedValues = false,

      showFields = true,
      showDescription = true,
      showRequiredErrors = false,

      protocolLabel = 'Protocol',
      placeholderLabel = 'Select a protocol…',
      fieldsLabel = DEFAULT_FIELDS_LABEL,
      requiredLabel = '(required)',
      requiredMessage = DEFAULT_REQUIRED_MESSAGE,
      protocolRequiredMessage = 'Select a protocol.',

      className,
      wrapperClassName,
      id: idProp,
      // Pulled out of `rest` on purpose: a Field wrapper aims these at the
      // CONTROL, and spreading them onto the outer wrapper would name a
      // presentational div instead of the select.
      'aria-labelledby': ariaLabelledBy,
      'aria-describedby': ariaDescribedBy,
      ...rest
    },
    ref,
  ) {
    /* Wiring published by an enclosing `<Field>`. Named `fieldControl` rather
     * than `field`, because inside `renderField` below `field` is the registry's
     * FieldSpec — two entirely different things, and shadowing one with the
     * other is exactly how a future edit silently reads the wrong object. */
    const fieldControl = useFieldControl();
    const reactId = useId();
    const id = idProp ?? `${reactId}-protocol`;
    const size = sizeProp ?? fieldControl.size;
    const required = requiredProp ?? fieldControl.required;
    /* `id` here is a PREFIX for the declared fields, so the protocol selector
     * gets a derived id of its own — except inside a Field, whose `<label for>`
     * already points at `fieldControl.id` and must be honoured. */
    const selectId = fieldControl.id ?? `${id}-select`;
    /* An `aria-label` silently outranks a real `<label>`, so the built-in name
     * is only applied when nothing else has named the control. */
    const namedExternally = Boolean(ariaLabelledBy ?? fieldControl.labelId);
    const descriptionId = `${id}-description`;
    const messageId = `${id}-message`;

    const [selected, setSelected] = useControllableState<string | null>({
      value,
      defaultValue,
      onChange,
    });

    const protocol = useMemo(
      () => protocols.find((p) => p.id === selected),
      [protocols, selected],
    );

    /* The registry projected onto the framework Select's option shape. The
     * per-protocol `icon` is deliberately NOT carried over: it is already
     * rendered as an adornment beside the trigger, and Select would draw the
     * selected option's icon there as well, giving two. */
    const protocolOptions = useMemo<SelectOption[]>(
      () =>
        protocols.map((entry) => ({
          value: entry.id,
          label: entry.label,
          disabled: entry.disabled ?? false,
        })),
      [protocols],
    );

    const [fieldValues, setFieldValues] = useControllableState<FieldValues>({
      value: values,
      defaultValue: defaultValues ?? defaultsFor(protocols.find((p) => p.id === defaultValue)),
      onChange: onValuesChange,
    });

    const [fieldValidity, setFieldValidity] = useState<Record<string, boolean>>({});
    const [touched, setTouched] = useState(false);

    const setValid = useCallback((name: string, valid: boolean) => {
      setFieldValidity((prev) => (prev[name] === valid ? prev : { ...prev, [name]: valid }));
    }, []);

    const setFieldValue = useCallback(
      (name: string, next: FieldValue) => {
        setFieldValues((prev) => (prev[name] === next ? prev : { ...prev, [name]: next }));
      },
      [setFieldValues],
    );

    const handleProtocolChange = useCallback(
      (nextId: string | null) => {
        const nextProtocol = protocols.find((p) => p.id === nextId);
        const nextDefaults = defaultsFor(nextProtocol);
        if (keepSharedValues) {
          for (const key of Object.keys(nextDefaults)) {
            const carried = fieldValues[key];
            if (carried !== undefined && !isEmptyValue(carried)) nextDefaults[key] = carried;
          }
        }
        // Validity is per protocol; keeping stale entries would let a field
        // that no longer exists hold the whole form invalid.
        setFieldValidity({});
        setFieldValues(nextDefaults);
        setSelected(nextId);
      },
      [protocols, keepSharedValues, fieldValues, setFieldValues, setSelected],
    );

    /* -- Aggregate validity ------------------------------------------------ */
    const validation = useMemo<ProtocolValidation>(() => {
      const fields: Record<string, boolean> = {};
      const missing: string[] = [];

      for (const field of protocol?.fields ?? []) {
        const raw = fieldValues[field.name];
        const empty = isEmptyValue(raw ?? null);
        const reported = fieldValidity[field.name];
        // A field that has not reported is assumed satisfied unless it is
        // required and empty. Assuming the opposite would mark a freshly
        // rendered form invalid before the operator has done anything.
        const ok = field.required && empty ? false : (reported ?? true);
        fields[field.name] = ok;
        if (field.required && empty) missing.push(field.name);
      }

      const protocolOk = !required || selected !== null;
      const valid = protocolOk && Object.values(fields).every(Boolean);

      return { protocol: selected, valid, fields, missing };
    }, [protocol, fieldValues, fieldValidity, required, selected]);

    const emitValid = useEventCallback(onValidChange);
    const lastSignature = useRef<string | null>(null);
    useEffect(() => {
      const signature = `${validation.protocol ?? ''}|${validation.valid}|${validation.missing.join(',')}|${Object.entries(
        validation.fields,
      )
        .map(([k, v]) => `${k}:${v}`)
        .join(',')}`;
      if (lastSignature.current === signature) return;
      lastSignature.current = signature;
      emitValid(validation);
    }, [validation, emitValid]);

    /* Two separate facts. `protocolMissing` is THIS component's own finding and
     * is what renders its message; `protocolInvalid` is the visual and ARIA
     * state, which an enclosing Field can also assert because it may know about
     * a rejection this component cannot see. Conflating them would print our
     * "select a protocol" text underneath the Field's unrelated error. */
    const protocolMissing = required && selected === null && (touched || showRequiredErrors);
    const protocolInvalid = protocolMissing || fieldControl.invalid;

    const describedBy = joinIds(
      fieldControl.describedBy,
      ariaDescribedBy,
      protocol?.description && showDescription ? descriptionId : undefined,
      protocolMissing ? messageId : undefined,
    );

    /* -- Field rendering --------------------------------------------------- */
    const renderField = (field: FieldSpec): ReactNode => {
      const fieldId = `${id}-f-${field.name}`;
      /* Referenced by `kind: 'select'`, which renders a `<button>` — and
       * `<label for>` does not contribute to a button's accessible name. */
      const fieldLabelId = `${fieldId}-label`;
      const fieldDescriptionId = field.description ? `${fieldId}-description` : undefined;
      const raw = fieldValues[field.name] ?? null;
      const kind = field.kind ?? 'text';
      const empty = isEmptyValue(raw);
      const invalid = Boolean(field.required && empty && showRequiredErrors);

      const commonDescribedBy = joinIds(fieldDescriptionId);

      let control: ReactNode;

      switch (kind) {
        case 'address':
          control = (
            <AddressInput
              id={fieldId}
              size={size}
              disabled={disabled}
              required={field.required ?? false}
              {...(field.accept ? { accept: field.accept } : {})}
              {...(field.placeholder ? { placeholder: field.placeholder } : {})}
              aria-describedby={commonDescribedBy}
              value={typeof raw === 'string' ? raw : ''}
              onChange={(next) => setFieldValue(field.name, next)}
              onValidChange={(v) => setValid(field.name, v.valid || (v.empty && !field.required))}
            />
          );
          break;

        case 'ports':
          control = (
            <PortRangeInput
              id={fieldId}
              size={size}
              disabled={disabled}
              required={field.required ?? false}
              {...(field.placeholder ? { placeholder: field.placeholder } : {})}
              aria-describedby={commonDescribedBy}
              value={typeof raw === 'string' ? raw : ''}
              onChange={(next) => setFieldValue(field.name, next)}
              onValidChange={(v) => setValid(field.name, v.valid || (v.empty && !field.required))}
            />
          );
          break;

        case 'duration':
          control = (
            <DurationInput
              id={fieldId}
              size={size}
              disabled={disabled}
              required={field.required ?? false}
              {...(field.durationUnits ? { units: field.durationUnits } : {})}
              {...(field.min !== undefined ? { min: field.min } : {})}
              {...(field.max !== undefined ? { max: field.max } : {})}
              aria-describedby={commonDescribedBy}
              value={typeof raw === 'number' ? raw : null}
              onChange={(ms) => setFieldValue(field.name, ms)}
              onValidChange={(v) => setValid(field.name, v.valid)}
            />
          );
          break;

        case 'bytes':
          control = (
            <ByteSizeInput
              id={fieldId}
              size={size}
              disabled={disabled}
              required={field.required ?? false}
              {...(field.byteUnits ? { units: field.byteUnits } : {})}
              {...(field.base ? { base: field.base } : {})}
              {...(field.min !== undefined ? { min: field.min } : {})}
              {...(field.max !== undefined ? { max: field.max } : {})}
              aria-describedby={commonDescribedBy}
              value={typeof raw === 'number' ? raw : null}
              onChange={(bytes) => setFieldValue(field.name, bytes)}
              onValidChange={(v) => setValid(field.name, v.valid)}
            />
          );
          break;

        case 'secret':
          control = (
            <CredentialField
              id={fieldId}
              size={size}
              disabled={disabled}
              invalid={invalid}
              {...(field.redactWhenHidden ? { redactWhenHidden: true } : {})}
              {...(field.revealTimeout !== undefined ? { revealTimeout: field.revealTimeout } : {})}
              {...(field.placeholder ? { placeholder: field.placeholder } : {})}
              aria-describedby={commonDescribedBy}
              value={typeof raw === 'string' ? raw : ''}
              onChange={(next) => setFieldValue(field.name, next)}
            />
          );
          break;

        case 'select':
          control = (
            <div
              className="stratum-net-control"
              data-size={size}
              data-validity={invalid ? 'invalid' : undefined}
              data-disabled={disabled || undefined}
            >
              {/* The framework's Select. "Nothing chosen" is `value: null` plus
                * `placeholder` here, where a native select needed a blank
                * `<option>` to represent the same thing. */}
              <Select
                id={fieldId}
                className="stratum-net-input stratum-protocol-picker__select"
                size={size}
                fullWidth
                disabled={disabled}
                placeholder={field.placeholder ?? placeholderLabel}
                options={(field.options ?? []).map((option) => ({
                  value: option.value,
                  label: option.label,
                  disabled: option.disabled ?? false,
                }))}
                value={raw === '' || typeof raw !== 'string' ? null : raw}
                aria-labelledby={fieldLabelId}
                aria-describedby={commonDescribedBy}
                aria-invalid={invalid || undefined}
                aria-required={field.required || undefined}
                // `|| null` keeps the old normalisation: a registry that
                // declares an option with an empty `value` still stores "unset"
                // rather than an empty string.
                onChange={(next) => setFieldValue(field.name, next || null)}
              />
            </div>
          );
          break;

        case 'boolean':
          control = (
            <Checkbox
              id={fieldId}
              className="stratum-protocol-picker__checkbox"
              disabled={disabled}
              aria-describedby={commonDescribedBy}
              checked={raw === true}
              onCheckedChange={(next) => setFieldValue(field.name, next)}
            >
              {field.label}
            </Checkbox>
          );
          break;

        case 'number':
          control = (
            <div
              className="stratum-net-control"
              data-size={size}
              data-validity={invalid ? 'invalid' : undefined}
              data-disabled={disabled || undefined}
            >
              <input
                id={fieldId}
                /* `text` + `inputMode`, not `type="number"`: a native number
                 * input discards what it cannot parse, so a bad paste becomes
                 * an empty field with no explanation. */
                type="text"
                inputMode="numeric"
                className="stratum-net-input"
                data-numeric=""
                disabled={disabled}
                placeholder={field.placeholder}
                autoComplete="off"
                spellCheck={false}
                aria-describedby={commonDescribedBy}
                aria-invalid={invalid || undefined}
                aria-required={field.required || undefined}
                value={raw === null || raw === false || raw === true ? '' : String(raw)}
                onChange={(event) => {
                  const next = event.currentTarget.value;
                  if (next.trim() === '') {
                    // Blank means UNSET, not zero.
                    setFieldValue(field.name, null);
                    setValid(field.name, !field.required);
                    return;
                  }
                  const parsed = Number(next);
                  setFieldValue(field.name, Number.isFinite(parsed) ? parsed : next);
                  const withinBounds =
                    Number.isFinite(parsed) &&
                    (field.min === undefined || parsed >= field.min) &&
                    (field.max === undefined || parsed <= field.max);
                  setValid(field.name, withinBounds);
                }}
              />
            </div>
          );
          break;

        case 'custom':
          control = field.render
            ? field.render({
                id: fieldId,
                value: raw,
                setValue: (next) => setFieldValue(field.name, next),
                describedBy: commonDescribedBy,
                setValid: (valid) => setValid(field.name, valid),
                disabled,
                size,
              })
            : null;
          break;

        case 'text':
        default:
          control = (
            <div
              className="stratum-net-control"
              data-size={size}
              data-validity={invalid ? 'invalid' : undefined}
              data-disabled={disabled || undefined}
            >
              <input
                id={fieldId}
                type="text"
                className="stratum-net-input"
                disabled={disabled}
                placeholder={field.placeholder}
                autoComplete="off"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                aria-describedby={commonDescribedBy}
                aria-invalid={invalid || undefined}
                aria-required={field.required || undefined}
                value={typeof raw === 'string' ? raw : ''}
                onChange={(event) => setFieldValue(field.name, event.currentTarget.value || null)}
              />
            </div>
          );
          break;
      }

      return (
        <div
          key={field.name}
          className="stratum-protocol-picker__field"
          data-kind={kind}
          data-required={field.required || undefined}
          data-invalid={invalid || undefined}
        >
          {/* A checkbox supplies its own inline label, so a second one would
            * name the control twice. */}
          {kind !== 'boolean' && (
            <label id={fieldLabelId} className="stratum-protocol-picker__label" htmlFor={fieldId}>
              {field.label}
              {field.required && (
                <span className="stratum-visually-hidden">{` ${requiredLabel}`}</span>
              )}
              {field.required && (
                <span className="stratum-protocol-picker__required" aria-hidden="true">
                  *
                </span>
              )}
            </label>
          )}

          {control}

          {field.description && (
            <p id={fieldDescriptionId} className="stratum-net-message" data-tone="hint">
              <span className="stratum-net-message__text">{field.description}</span>
            </p>
          )}

          {invalid && (
            <p className="stratum-net-message" data-tone="error">
              <span className="stratum-net-message__icon" aria-hidden="true">
                {statusGlyph('danger')}
              </span>
              <span className="stratum-net-message__text">{requiredMessage(field.label)}</span>
            </p>
          )}
        </div>
      );
    };

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="protocol-picker"
        data-size={size}
        data-selected={selected ?? undefined}
        className={clsx('stratum-protocol-picker', wrapperClassName)}
      >
        <div className="stratum-net-field stratum-protocol-picker__picker">
          <div
            className="stratum-net-control stratum-protocol-picker__control"
            data-size={size}
            data-validity={protocolInvalid ? 'invalid' : undefined}
            data-disabled={disabled || undefined}
          >
            {protocol?.icon && (
              <span className="stratum-protocol-picker__icon" aria-hidden="true">
                {protocol.icon}
              </span>
            )}

            <Select
              id={selectId}
              className={clsx('stratum-net-input', 'stratum-protocol-picker__select', className)}
              size={size}
              fullWidth
              disabled={disabled}
              placeholder={placeholderLabel}
              options={protocolOptions}
              value={selected}
              /* `aria-labelledby` from a Field wrapper takes precedence; the
               * built-in name is only used when the control is otherwise
               * anonymous. The Field's own label is picked up here too, because
               * its `<label for>` cannot name the `<button>` Select renders. */
              aria-labelledby={ariaLabelledBy ?? fieldControl.labelId}
              aria-label={namedExternally ? undefined : protocolLabel}
              aria-describedby={describedBy}
              aria-invalid={protocolInvalid || undefined}
              aria-required={required || undefined}
              onChange={(next) => {
                setTouched(true);
                handleProtocolChange(next);
              }}
              /* `touched` is marked when the listbox CLOSES rather than on blur.
               * The popup is portalled and takes DOM focus, so the trigger blurs
               * the moment it opens — a blur handler would paint the field red
               * while the operator is still reading the options. */
              onOpenChange={(open) => {
                if (!open) setTouched(true);
              }}
            />
          </div>

          {showDescription && protocol?.description && (
            <p id={descriptionId} className="stratum-net-message" data-tone="hint">
              <span className="stratum-net-message__text">{protocol.description}</span>
            </p>
          )}

          {protocolMissing && (
            <p id={messageId} className="stratum-net-message" data-tone="error">
              <span className="stratum-net-message__icon" aria-hidden="true">
                {statusGlyph('danger')}
              </span>
              <span className="stratum-net-message__text">{protocolRequiredMessage}</span>
            </p>
          )}
        </div>

        {showFields && protocol && (protocol.fields?.length ?? 0) > 0 && (
          <div
            className="stratum-protocol-picker__fields"
            role="group"
            aria-label={fieldsLabel(protocol.label)}
          >
            {protocol.fields?.map(renderField)}
          </div>
        )}
      </div>
    );
  },
);
