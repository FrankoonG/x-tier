import {
  forwardRef,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type InputHTMLAttributes,
} from 'react';
import clsx from 'clsx';
import { useFieldControl } from '../../components/Field/Field';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import {
  ADDRESS_KIND_LABELS,
  ALL_ADDRESS_KINDS,
  parseAddress,
  type AddressErrorCode,
  type AddressKind,
  type AddressOptions,
  type AddressResult,
  type AddressSuccess,
} from './address';
import {
  ValidityMark,
  joinIds,
  resolveAriaInvalid,
  warnMissingNetFieldLabel,
  type NetControlSize,
  type ValidityState,
} from '../_shared/netField';
import { statusGlyph } from '../../components/_shared/statusIcons';
import './AddressInput.css';

/* Re-exported so a consumer needs only one import path. The implementations
 * live in `address.ts` and pull in neither React nor CSS. */
export {
  parseAddress,
  parseIPv4,
  parseIPv6,
  parseCidr,
  parseHostname,
  parseHostPort,
  parsePort,
  parseIPv4Bytes,
  parseIPv6Bytes,
  formatIPv6,
  guessAddressKind,
  isAddress,
  isIPv4,
  isIPv6,
  isCidr4,
  isCidr6,
  isHostname,
  isHostPort,
  ALL_ADDRESS_KINDS,
  ADDRESS_KIND_LABELS,
  MIN_PORT,
  MAX_PORT,
} from './address';
export type {
  AddressKind,
  AddressErrorCode,
  AddressOptions,
  AddressResult,
  AddressSuccess,
  AddressFailure,
  HostnameOptions,
  HostPortOptions,
  IPv6Options,
  IPv6Parsed,
} from './address';

/** What the field currently knows about its value. */
export interface AddressValidation {
  /** `true` when the field is blank. Blank is not invalid unless `required`. */
  empty: boolean;
  valid: boolean;
  /** The shape that matched, or the shape the input most resembled. */
  kind: AddressKind | null;
  /** Canonical rendering of a valid value; `null` otherwise. */
  normalized: string | null;
  /** Resolved, consumer-overridable message. `null` when there is nothing to say. */
  message: string | null;
  code: AddressErrorCode | null;
  /** Full parse result, including CIDR masking and `host`/`port` breakdown. */
  detail: AddressSuccess | null;
}

type NativeInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  // `accept` is the file-picker attribute and is meaningless on a text input;
  // the name is reclaimed here for the set of address shapes to allow.
  'value' | 'defaultValue' | 'onChange' | 'type' | 'size' | 'accept'
>;

export interface AddressInputProps extends NativeInputProps, AddressOptions {
  /** Shapes to accept. Default: all six. */
  accept?: readonly AddressKind[];
  value?: string;
  defaultValue?: string;
  onChange?: (value: string) => void;
  /** Fires whenever the validation outcome changes — not on every keystroke. */
  onValidChange?: (validation: AddressValidation) => void;
  size?: NetControlSize;
  /** Blank fails validation. Default `false`. */
  required?: boolean;
  /**
   * Rewrite the field to the canonical form on blur — lowercase IPv6 with
   * `::` compression, trailing dot dropped from a hostname.
   *
   * Off by default. Silently rewriting what an operator typed is startling,
   * and `normalized` is on the validation object either way.
   */
  normalizeOnBlur?: boolean;
  /** Show the tick / cross / dash mark inside the control. Default `true`. */
  showValidity?: boolean;
  /**
   * Show the detected shape as a tag inside the control ("IPv6", "IPv4 CIDR").
   * This is genuinely useful feedback: it tells the operator the field read
   * `[::1]:80` as a host:port and not as a malformed address. Default `true`.
   */
  showKind?: boolean;
  /** Render the message below the control. Default `true`. */
  showMessage?: boolean;
  /**
   * Suppress the error until the field has been blurred once. Default `true`.
   * Marking a field red while the operator is still typing the second octet is
   * the most disliked behaviour in form validation.
   */
  validateOnBlurOnly?: boolean;
  /** Overrides for the built-in English messages, keyed by error code. */
  messages?: Partial<Record<AddressErrorCode, string>>;
  /** Overrides for shape names used in the tag and in "expected …" messages. */
  kindLabels?: Partial<Record<AddressKind, string>>;
  /** Helper text shown when there is no error. */
  hint?: string;
  /** Announced name for the validity mark when valid. Default `'valid'`. */
  labelValid?: string;
  /** Announced name for the validity mark when invalid. Default `'invalid'`. */
  labelInvalid?: string;
  /** Extra note appended for a CIDR whose host bits are set. */
  hostBitsNote?: (network: string) => string;
  /** Renders the control at full width. Default `true`. */
  fullWidth?: boolean;
  /** Class applied to the outer wrapper; `className` lands on the `<input>`. */
  wrapperClassName?: string;
}

/** Module scope so the default does not change identity on every render. */
const DEFAULT_HOST_BITS_NOTE = (network: string) =>
  `Host bits are set; this describes ${network}.`;

const EMPTY_VALIDATION: AddressValidation = {
  empty: true,
  valid: false,
  kind: null,
  normalized: null,
  message: null,
  code: null,
  detail: null,
};

/**
 * An address field for IPv4, IPv6, CIDR, hostnames and `host:port`.
 *
 * WHAT IT ACTUALLY DOES
 * ---------------------
 * Everything interesting is in `address.ts`, which parses rather than
 * pattern-matches; this component is the presentation and the ARIA wiring
 * around it. See that file for why a regex was not used.
 *
 * VALIDITY IS NOT BINARY HERE
 * ---------------------------
 * Three states, deliberately:
 *   - EMPTY   — nothing typed. Not an error unless `required`. Drawn with the
 *               neutral dash, never red.
 *   - VALID   — parsed. The tag names WHICH shape it parsed as, so an operator
 *               can see that `10.0.0.1/8` was read as a CIDR block rather than
 *               silently truncated.
 *   - INVALID — parsed and failed, with the specific reason. "Octet 300 is
 *               above 255" beats "invalid address" every time, which is why
 *               the parser returns codes and fragments rather than a boolean.
 *
 * By default the error only appears after first blur (`validateOnBlurOnly`);
 * the field turning red on the second character of `192.168…` trains operators
 * to ignore it.
 *
 * FIELD INTEGRATION
 * -----------------
 * `id` and `aria-describedby` pass straight through, and the ids this control
 * generates for its own message and hint are MERGED with whatever the wrapper
 * supplied rather than replacing it. `aria-invalid` can be forced by the
 * wrapper — it may know about a server-side rejection this control cannot see.
 */
export const AddressInput = forwardRef<HTMLInputElement, AddressInputProps>(function AddressInput(
  {
    accept = ALL_ADDRESS_KINDS,
    value,
    defaultValue = '',
    onChange,
    onValidChange,
    size: sizeProp,
    required: requiredProp,
    normalizeOnBlur = false,
    showValidity = true,
    showKind = true,
    showMessage = true,
    validateOnBlurOnly = true,
    messages,
    kindLabels,
    hint,
    labelValid = 'valid',
    labelInvalid = 'invalid',
    hostBitsNote = DEFAULT_HOST_BITS_NOTE,
    fullWidth = true,
    wrapperClassName,
    className,
    disabled,
    readOnly,
    placeholder = '192.0.2.10, 2001:db8::1, [2001:db8::1]:443, 10.0.0.0/8',
    allowUnderscore,
    allowTrailingDot,
    requireMultiLabel,
    allowNumericTld,
    allowZone,
    allowPortZero,
    onBlur,
    onFocus,
    id: idProp,
    ...rest
  },
  ref,
) {
  /* Wiring published by an enclosing `<Field>`. Outside one this is an inert
   * object, so the standalone case needs no branch. The Field's `<label for>`
   * already points at `field.id`, so adopting it is not optional. */
  const field = useFieldControl();
  const reactId = useId();
  const id = idProp ?? field.id ?? `${reactId}-address`;
  const size = sizeProp ?? field.size;
  const required = requiredProp ?? field.required;
  const messageId = `${id}-message`;
  const hintId = `${id}-hint`;

  // The example addresses in the placeholder are a format hint, not a name —
  // they vanish the moment anything is typed.
  warnMissingNetFieldLabel('AddressInput', {
    fieldId: field.id,
    fieldLabelId: field.labelId,
    ariaLabel: rest['aria-label'],
    ariaLabelledBy: rest['aria-labelledby'],
    explicitId: idProp,
  });

  const [text, setText] = useControllableState<string>({
    value,
    defaultValue,
    onChange,
  });

  const [touched, setTouched] = useState(false);

  const parseOptions = useMemo<AddressOptions>(
    () => ({
      accept,
      ...(allowUnderscore !== undefined ? { allowUnderscore } : {}),
      ...(allowTrailingDot !== undefined ? { allowTrailingDot } : {}),
      ...(requireMultiLabel !== undefined ? { requireMultiLabel } : {}),
      ...(allowNumericTld !== undefined ? { allowNumericTld } : {}),
      ...(allowZone !== undefined ? { allowZone } : {}),
      ...(allowPortZero !== undefined ? { allowPortZero } : {}),
    }),
    [
      accept,
      allowUnderscore,
      allowTrailingDot,
      requireMultiLabel,
      allowNumericTld,
      allowZone,
      allowPortZero,
    ],
  );

  const validation = useMemo<AddressValidation>(() => {
    const trimmed = text.trim();
    if (trimmed.length === 0) {
      if (!required) return EMPTY_VALIDATION;
      return {
        ...EMPTY_VALIDATION,
        message: messages?.required ?? 'Enter an address.',
        code: 'required',
      };
    }

    const result: AddressResult = parseAddress(trimmed, parseOptions);
    if (result.ok) {
      let message: string | null = null;
      if (result.hostBitsSet && result.network) message = hostBitsNote(result.network);
      return {
        empty: false,
        valid: true,
        kind: result.kind,
        normalized: result.normalized,
        message,
        code: null,
        detail: result,
      };
    }
    return {
      empty: false,
      valid: false,
      kind: result.kind,
      normalized: null,
      message: messages?.[result.code] ?? result.message,
      code: result.code,
      detail: null,
    };
  }, [text, required, parseOptions, messages, hostBitsNote]);

  // Report only on transitions. A callback that fires on every keystroke turns
  // into a re-render storm in a form with a dozen of these.
  const emitValid = useEventCallback(onValidChange);
  const lastSignature = useRef<string | null>(null);
  useEffect(() => {
    const signature = `${validation.valid}|${validation.empty}|${validation.kind ?? ''}|${validation.code ?? ''}|${validation.normalized ?? ''}`;
    if (lastSignature.current === signature) return;
    lastSignature.current = signature;
    emitValid(validation);
  }, [validation, emitValid]);

  const showProblem = !validation.valid && !validation.empty && (touched || !validateOnBlurOnly);
  const showRequired = validation.code === 'required' && touched;

  const state: ValidityState = validation.empty
    ? 'empty'
    : validation.valid
      ? validation.detail?.hostBitsSet
        ? 'warning'
        : 'valid'
      : showProblem
        ? 'invalid'
        : 'empty';

  // A Field that carries its own `error` is authoritative: it may know about a
  // server rejection this field cannot see, and its message is already linked.
  const isInvalid = state === 'invalid' || showRequired || field.invalid;
  const ownMessage = state === 'invalid' || showRequired ? validation.message : null;
  const activeMessage =
    ownMessage ?? (validation.valid && validation.message ? validation.message : null);

  const describedBy = joinIds(
    field.describedBy,
    rest['aria-describedby'],
    activeMessage && showMessage ? messageId : undefined,
    hint && showMessage ? hintId : undefined,
  );

  const kindLabel =
    validation.valid && validation.kind
      ? (kindLabels?.[validation.kind] ?? ADDRESS_KIND_LABELS[validation.kind])
      : null;

  const handleBlur = useEventCallback(onBlur);
  const handleFocus = useEventCallback(onFocus);

  return (
    <div
      data-stratum="address-input"
      data-size={size}
      data-validity={isInvalid ? 'invalid' : state}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-net-field', 'stratum-address-input', wrapperClassName)}
    >
      <div
        className="stratum-net-control stratum-address-input__control"
        data-size={size}
        data-validity={isInvalid ? 'invalid' : state === 'warning' ? 'warning' : undefined}
        data-disabled={disabled || undefined}
        data-readonly={readOnly || undefined}
      >
        <input
          {...rest}
          ref={ref}
          id={id}
          type="text"
          className={clsx('stratum-net-input', className)}
          value={text}
          disabled={disabled}
          readOnly={readOnly}
          placeholder={placeholder}
          // Addresses are not words. Every engine's autocorrect mangles them.
          autoComplete={rest.autoComplete ?? 'off'}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          inputMode="text"
          aria-invalid={resolveAriaInvalid(
            isInvalid ? 'invalid' : 'valid',
            rest['aria-invalid'],
          )}
          aria-describedby={describedBy}
          // Merged rather than assigned: this sits after the spread, so a bare
          // `required || undefined` would delete a consumer's own attribute.
          aria-required={required ? true : rest['aria-required']}
          onChange={(event) => {
            setText(event.currentTarget.value);
          }}
          onFocus={(event) => {
            handleFocus(event);
          }}
          onBlur={(event) => {
            setTouched(true);
            if (normalizeOnBlur && validation.valid && validation.normalized) {
              setText(validation.normalized);
            }
            handleBlur(event);
          }}
        />

        {showKind && kindLabel && (
          <span className="stratum-net-tag stratum-address-input__kind">{kindLabel}</span>
        )}

        {showValidity && (
          <ValidityMark
            state={isInvalid ? 'invalid' : state}
            label={
              isInvalid
                ? labelInvalid
                : state === 'valid' || state === 'warning'
                  ? labelValid
                  : undefined
            }
          />
        )}
      </div>

      {showMessage && activeMessage && (
        <p
          id={messageId}
          className="stratum-net-message"
          data-tone={isInvalid ? 'error' : 'warning'}
          // Deliberately NOT a live region. `aria-invalid` plus the
          // `aria-describedby` link above is the mechanism WCAG SC 3.3.1 asks
          // for, and it announces the reason when the operator returns to the
          // field. Adding `role="alert"` on top makes every screen reader say
          // the message twice — once on insertion, again on focus — which is
          // how error text ends up being tuned out.
        >
          <span className="stratum-net-message__icon" aria-hidden="true">
            {statusGlyph(isInvalid ? 'danger' : 'warning')}
          </span>
          <span className="stratum-net-message__text">{activeMessage}</span>
        </p>
      )}

      {showMessage && hint && !activeMessage && (
        <p id={hintId} className="stratum-net-message" data-tone="hint">
          <span className="stratum-net-message__text">{hint}</span>
        </p>
      )}
    </div>
  );
});
