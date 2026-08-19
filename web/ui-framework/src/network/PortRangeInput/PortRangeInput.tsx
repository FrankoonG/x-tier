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
import { formatCount } from '../format';
import {
  formatPortRanges,
  parsePortList,
  type PortListOptions,
  type PortRange,
  type PortRangeIssue,
  type PortRangeIssueCode,
} from './portRange';
import {
  ValidityMark,
  joinIds,
  resolveAriaInvalid,
  warnMissingNetFieldLabel,
  type NetControlSize,
  type ValidityState,
} from '../_shared/netField';
import { statusGlyph } from '../../components/_shared/statusIcons';
import './PortRangeInput.css';

export {
  parsePortList,
  mergePortRanges,
  countPorts,
  formatPortRange,
  formatPortRanges,
  portRangesEqual,
  portInRanges,
  MIN_PORT_NUMBER,
  MAX_PORT_NUMBER,
} from './portRange';
export type {
  PortRange,
  PortRangeIssue,
  PortRangeIssueCode,
  PortListOptions,
  PortListResult,
} from './portRange';

export interface PortRangeValidation {
  empty: boolean;
  valid: boolean;
  /** Ranges as written. */
  ranges: PortRange[];
  /** Sorted, de-duplicated, coalesced. */
  merged: PortRange[];
  /** Total distinct ports, or `null` when nothing has been entered. */
  total: number | null;
  issues: PortRangeIssue[];
  /** Canonical text for `merged`; `null` when nothing parsed. */
  normalized: string | null;
}

type NativeInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'value' | 'defaultValue' | 'onChange' | 'type' | 'size'
>;

export interface PortRangeInputProps extends NativeInputProps, PortListOptions {
  value?: string;
  defaultValue?: string;
  onChange?: (value: string) => void;
  /** Fires when the parse outcome changes, not on every keystroke. */
  onValidChange?: (validation: PortRangeValidation) => void;
  /** Convenience callback carrying only the merged ranges. */
  onRangesChange?: (ranges: PortRange[]) => void;
  size?: NetControlSize;
  required?: boolean;
  /**
   * Rewrite the field to the canonical merged form on blur. Default `true`.
   *
   * This is on by default here — unlike `AddressInput` — because merging is
   * exactly what the operator asked for by typing overlapping entries, and the
   * port SET is unchanged by it. Nothing is discarded; only the spelling
   * changes.
   */
  mergeOnBlur?: boolean;
  /** Separator used when rewriting. Default `', '`. */
  separator?: string;
  /** Show the running port total inside the control. Default `true`. */
  showCount?: boolean;
  showValidity?: boolean;
  showMessage?: boolean;
  /** Suppress errors until first blur. Default `true`. */
  validateOnBlurOnly?: boolean;
  /** Overrides for the built-in messages, keyed by issue code. */
  messages?: Partial<Record<PortRangeIssueCode, string>>;
  /** Label for the port total. Default `'1 port'` / `'8,192 ports'`. */
  countLabel?: (total: number) => string;
  /** Text for the blank-but-required case. */
  requiredMessage?: string;
  hint?: string;
  labelValid?: string;
  labelInvalid?: string;
  fullWidth?: boolean;
  wrapperClassName?: string;
}

const DEFAULT_COUNT_LABEL = (total: number) =>
  total === 1 ? '1 port' : `${formatCount(total)} ports`;

const EMPTY_VALIDATION: PortRangeValidation = {
  empty: true,
  valid: false,
  ranges: [],
  merged: [],
  total: null,
  issues: [],
  normalized: null,
};

/**
 * A field for single ports, ranges and comma-separated lists.
 *
 * `80, 443, 8000-8100` parses to three ranges covering 103 ports. The parser
 * lives in `portRange.ts`; this is the presentation.
 *
 * THE TOTAL IS THE POINT
 * ----------------------
 * The running port count is shown inside the control because it is the only
 * cheap way to catch the class of mistake this input exists to prevent:
 * `1-65535` and `1-6553` look nearly identical in a text field and differ by
 * 58 982 open ports. A number that changes as you type makes that obvious.
 *
 * When the field is blank the count is ABSENT, not zero. "No ports specified"
 * and "zero ports" are different claims, and the framework never renders the
 * first as the second.
 *
 * MERGE, DO NOT REWRITE
 * ---------------------
 * On blur the value is normalised: sorted, de-duplicated, and overlapping or
 * adjacent ranges coalesced. The port SET is never changed by this, only its
 * spelling — which is why it is safe to do automatically. An inverted range
 * (`8100-8000`) is the one case that is NOT auto-corrected: swapping the
 * endpoints would turn a typo into a rule the operator never reviewed, so it
 * is reported as an error instead.
 */
export const PortRangeInput = forwardRef<HTMLInputElement, PortRangeInputProps>(
  function PortRangeInput(
    {
      value,
      defaultValue = '',
      onChange,
      onValidChange,
      onRangesChange,
      size: sizeProp,
      required: requiredProp,
      mergeOnBlur = true,
      separator = ', ',
      showCount = true,
      showValidity = true,
      showMessage = true,
      validateOnBlurOnly = true,
      messages,
      countLabel = DEFAULT_COUNT_LABEL,
      requiredMessage = 'Enter at least one port.',
      hint,
      labelValid = 'valid',
      labelInvalid = 'invalid',
      fullWidth = true,
      wrapperClassName,
      className,
      disabled,
      readOnly,
      placeholder = '80, 443, 8000-8100',
      allowZero,
      minPort,
      maxPort,
      maxItems,
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
    const id = idProp ?? field.id ?? `${reactId}-ports`;
    const size = sizeProp ?? field.size;
    const required = requiredProp ?? field.required;
    const messageId = `${id}-message`;
    const hintId = `${id}-hint`;

    // The `80, 443, 8000-8100` placeholder teaches the syntax; it does not say
    // which ports these are, and it is gone as soon as the operator types.
    warnMissingNetFieldLabel('PortRangeInput', {
      fieldId: field.id,
      fieldLabelId: field.labelId,
      ariaLabel: rest['aria-label'],
      ariaLabelledBy: rest['aria-labelledby'],
      explicitId: idProp,
    });

    const [text, setText] = useControllableState<string>({ value, defaultValue, onChange });
    const [touched, setTouched] = useState(false);

    const parseOptions = useMemo<PortListOptions>(
      () => ({
        ...(allowZero !== undefined ? { allowZero } : {}),
        ...(minPort !== undefined ? { minPort } : {}),
        ...(maxPort !== undefined ? { maxPort } : {}),
        ...(maxItems !== undefined ? { maxItems } : {}),
      }),
      [allowZero, minPort, maxPort, maxItems],
    );

    const validation = useMemo<PortRangeValidation>(() => {
      if (text.trim().length === 0) {
        return required
          ? { ...EMPTY_VALIDATION, issues: [{ code: 'required', severity: 'error', message: requiredMessage }] }
          : EMPTY_VALIDATION;
      }
      const result = parsePortList(text, parseOptions);
      return {
        empty: false,
        valid: result.ok,
        ranges: result.ranges,
        merged: result.merged,
        total: result.total,
        issues: result.issues.map((issue) =>
          messages?.[issue.code] ? { ...issue, message: messages[issue.code]! } : issue,
        ),
        normalized: result.merged.length > 0 ? formatPortRanges(result.merged, separator) : null,
      };
    }, [text, required, requiredMessage, parseOptions, messages, separator]);

    const emitValid = useEventCallback(onValidChange);
    const emitRanges = useEventCallback(onRangesChange);
    const lastSignature = useRef<string | null>(null);
    useEffect(() => {
      const signature = [
        validation.valid,
        validation.empty,
        validation.total ?? '',
        validation.normalized ?? '',
        validation.issues.map((i) => `${i.code}:${i.item ?? ''}`).join('|'),
      ].join('~');
      if (lastSignature.current === signature) return;
      lastSignature.current = signature;
      emitValid(validation);
      emitRanges(validation.merged);
    }, [validation, emitValid, emitRanges]);

    const errors = validation.issues.filter((i) => i.severity === 'error');
    const warnings = validation.issues.filter((i) => i.severity === 'warning');

    const showProblem = errors.length > 0 && (touched || !validateOnBlurOnly);
    const state: ValidityState = validation.empty
      ? showProblem
        ? 'invalid'
        : 'empty'
      : showProblem
        ? 'invalid'
        : warnings.length > 0
          ? 'warning'
          : validation.valid
            ? 'valid'
            : 'empty';

    const isInvalid = state === 'invalid';
    const activeIssue = isInvalid ? errors[0] : warnings[0];
    const activeMessage = activeIssue?.message ?? null;

    const describedBy = joinIds(
      field.describedBy,
      rest['aria-describedby'],
      activeMessage && showMessage ? messageId : undefined,
      hint && showMessage ? hintId : undefined,
    );

    const handleBlur = useEventCallback(onBlur);
    const handleFocus = useEventCallback(onFocus);

    return (
      <div
        data-stratum="port-range-input"
        data-size={size}
        data-validity={state}
        data-full-width={fullWidth || undefined}
        className={clsx('stratum-net-field', 'stratum-port-range', wrapperClassName)}
      >
        <div
          className="stratum-net-control stratum-port-range__control"
          data-size={size}
          data-validity={state === 'invalid' ? 'invalid' : state === 'warning' ? 'warning' : undefined}
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
            autoComplete={rest.autoComplete ?? 'off'}
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            // `inputMode="numeric"` would hide the comma and hyphen on a soft
            // keyboard, which are half the grammar. `text` keeps them reachable.
            inputMode="text"
            aria-invalid={resolveAriaInvalid(isInvalid ? 'invalid' : 'valid', rest['aria-invalid'])}
            aria-describedby={describedBy}
            // Merged rather than assigned: this sits after the spread, so a
            // bare `required || undefined` would delete a consumer's own value.
            aria-required={required ? true : rest['aria-required']}
            onChange={(event) => setText(event.currentTarget.value)}
            onFocus={(event) => handleFocus(event)}
            onBlur={(event) => {
              setTouched(true);
              // Only normalise a fully clean value. Rewriting a list that still
              // contains a bad entry would move the operator's cursor away from
              // the thing they are trying to fix.
              if (mergeOnBlur && validation.valid && validation.normalized) {
                setText(validation.normalized);
              }
              handleBlur(event);
            }}
          />

          {showCount && validation.total !== null && (
            <span className="stratum-net-tag stratum-port-range__count">
              {countLabel(validation.total)}
            </span>
          )}

          {showValidity && (
            <ValidityMark
              state={state}
              label={
                state === 'invalid'
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
            // See AddressInput: aria-describedby + aria-invalid is the SC 3.3.1
            // mechanism; a live region here would double-announce.
          >
            <span className="stratum-net-message__icon" aria-hidden="true">
              {statusGlyph(isInvalid ? 'danger' : 'warning')}
            </span>
            <span className="stratum-net-message__text">
              {activeMessage}
              {errors.length + warnings.length > 1 && (
                <span className="stratum-port-range__more">
                  {` (+${errors.length + warnings.length - 1} more)`}
                </span>
              )}
            </span>
          </p>
        )}

        {showMessage && hint && !activeMessage && (
          <p id={hintId} className="stratum-net-message" data-tone="hint">
            <span className="stratum-net-message__text">{hint}</span>
          </p>
        )}
      </div>
    );
  },
);
