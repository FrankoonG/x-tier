import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { Button } from '../../components/Button/Button';
import { useFieldControl } from '../../components/Field/Field';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import {
  joinIds,
  resolveAriaInvalid,
  warnMissingNetFieldLabel,
  type NetControlSize,
} from '../_shared/netField';
import './CredentialField.css';

/* -------------------------------------------------------------------------- */
/* Strength estimation                                                        */
/* -------------------------------------------------------------------------- */

export interface SecretStrength {
  /** Coarse band, 0 (very weak) to 4 (very strong). */
  score: 0 | 1 | 2 | 3 | 4;
  /**
   * Estimated bits of entropy, or `null` when it was not measured.
   *
   * `null` is not zero. An unmeasured secret and a zero-entropy secret are
   * different facts, and the meter renders them differently.
   */
  bits: number | null;
}

const SYMBOL_POOL = 33; // printable ASCII that is neither a letter nor a digit

/**
 * Rough entropy estimate for a secret, in bits.
 *
 * READ THIS BEFORE TRUSTING THE NUMBER
 * ------------------------------------
 * This computes `length x log2(alphabet)`, which is the entropy of a secret
 * drawn UNIFORMLY AT RANDOM from the character classes present. That is the
 * right model for a generated key, token or passphrase — the case this
 * component is built for, since network panels mostly display machine-issued
 * credentials.
 *
 * It is the WRONG model for a human-chosen password: `Password1!` scores 65
 * bits here and is cracked instantly, because humans do not sample uniformly.
 * A dictionary-aware estimator (zxcvbn and friends) is the only honest way to
 * score human input, and shipping one would cost several hundred kilobytes —
 * so the framework does not pretend to. Pass your own value to the `strength`
 * prop if you have a better estimator; this is the fallback, not the authority.
 */
export function estimateSecretStrength(secret: string): SecretStrength {
  if (secret.length === 0) return { score: 0, bits: null };

  let lower = false;
  let upper = false;
  let digit = false;
  let symbol = false;
  let wide = false;

  for (const ch of secret) {
    const code = ch.codePointAt(0) ?? 0;
    if (code >= 97 && code <= 122) lower = true;
    else if (code >= 65 && code <= 90) upper = true;
    else if (code >= 48 && code <= 57) digit = true;
    else if (code >= 32 && code < 127) symbol = true;
    else wide = true;
  }

  let pool = 0;
  if (lower) pool += 26;
  if (upper) pool += 26;
  if (digit) pool += 10;
  if (symbol) pool += SYMBOL_POOL;
  // Anything outside printable ASCII is credited conservatively; assuming the
  // full Unicode range would let a single emoji claim 20 bits.
  if (wide) pool += 100;
  if (pool <= 1) pool = 2;

  // Distinct-character count caps the estimate, so `aaaaaaaaaaaaaaaa` cannot
  // claim the entropy of sixteen independent draws.
  const distinct = new Set(Array.from(secret)).size;
  const effectiveLength = Math.min(secret.length, distinct * 2);
  const bits = Math.round(effectiveLength * Math.log2(pool));

  const score: SecretStrength['score'] =
    bits < 28 ? 0 : bits < 40 ? 1 : bits < 64 ? 2 : bits < 112 ? 3 : 4;

  return { score, bits };
}

/* -------------------------------------------------------------------------- */
/* Icons — 1em, currentColor, so they survive dark-mode extensions            */
/* -------------------------------------------------------------------------- */

const iconBase = {
  viewBox: '0 0 16 16',
  width: '1em',
  height: '1em',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.4,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  focusable: 'false' as const,
  'aria-hidden': true,
};

const EyeIcon = (
  <svg {...iconBase}>
    <path d="M1.4 8s2.5-4.3 6.6-4.3S14.6 8 14.6 8s-2.5 4.3-6.6 4.3S1.4 8 1.4 8Z" />
    <circle cx="8" cy="8" r="1.9" />
  </svg>
);

const EyeOffIcon = (
  <svg {...iconBase}>
    <path d="M6.3 3.9A7.5 7.5 0 0 1 8 3.7c4.1 0 6.6 4.3 6.6 4.3a13 13 0 0 1-2.2 2.7" />
    <path d="M3.9 5.2A12.7 12.7 0 0 0 1.4 8s2.5 4.3 6.6 4.3a7 7 0 0 0 2.3-.4" />
    <path d="m2.2 2.2 11.6 11.6" />
  </svg>
);

const CopyIcon = (
  <svg {...iconBase}>
    <rect x="5.6" y="5.6" width="8" height="8" rx="1.4" />
    <path d="M10.4 3.6a1.4 1.4 0 0 0-1.4-1.4H3.6a1.4 1.4 0 0 0-1.4 1.4V9a1.4 1.4 0 0 0 1.4 1.4" />
  </svg>
);

const CheckIcon = (
  <svg {...iconBase}>
    <path d="m3 8.4 3.2 3.2L13 4.8" />
  </svg>
);

const RegenerateIcon = (
  <svg {...iconBase}>
    <path d="M13.3 7.1a5.4 5.4 0 1 0-.5 3.6" />
    <path d="M13.6 3.2v3.9h-3.9" />
  </svg>
);

/* -------------------------------------------------------------------------- */
/* Component                                                                  */
/* -------------------------------------------------------------------------- */

type NativeInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  // The native clipboard handlers are displaced: `onCopy` here means "the copy
  // BUTTON succeeded", which is the event a credential field is actually asked
  // about. Attach a native clipboard listener on a wrapper if you need one.
  | 'value'
  | 'defaultValue'
  | 'onChange'
  | 'type'
  | 'size'
  | 'onCopy'
  | 'onCopyCapture'
>;

export interface CredentialFieldProps extends NativeInputProps {
  value?: string;
  defaultValue?: string;
  onChange?: (value: string) => void;
  size?: NetControlSize;

  /** Show the reveal toggle. Default `true`. */
  allowReveal?: boolean;
  /** Controlled reveal state. Omit for uncontrolled. */
  revealed?: boolean;
  defaultRevealed?: boolean;
  onRevealedChange?: (revealed: boolean) => void;
  /**
   * Re-mask automatically this many milliseconds after reveal.
   *
   * A revealed secret on a shared operator screen is a standing risk that grows
   * with every minute nobody notices it. Any interaction that re-reveals
   * restarts the clock.
   */
  revealTimeout?: number;

  /**
   * Keep the secret out of the DOM entirely while masked.
   *
   * `type="password"` hides a value visually but the string is still in the
   * input's value property, so it is one devtools inspection, one DOM
   * serialisation or one screen-sharing extension away from being read. With
   * this set, the rendered field holds a fixed-length placeholder instead and
   * the real value only enters the DOM when the operator reveals it.
   *
   * Consequence: the field is read-only while masked, because there is nothing
   * meaningful to edit. Default `false`.
   */
  redactWhenHidden?: boolean;
  /** Glyph used for the redacted placeholder. Default `'•'`. */
  maskCharacter?: string;
  /**
   * Length of the redacted placeholder. Fixed by default so the mask does not
   * leak the secret's length. Pass `'value'` to mirror the real length.
   */
  maskLength?: number | 'value';

  /** Show the copy button. Default `true`. */
  allowCopy?: boolean;
  /** Called after a successful copy. */
  onCopy?: (value: string) => void;
  /** Called when the clipboard write failed or was unavailable. */
  onCopyError?: (error: unknown) => void;
  /** How long the copied confirmation stays up, in ms. Default 1600. */
  copyFeedbackMs?: number;

  /** Providing this renders the regenerate button. */
  onRegenerate?: () => void;
  regenerating?: boolean;

  /** Show the strength meter. Default `false`. */
  showStrength?: boolean;
  /**
   * Externally computed strength. Overrides the built-in estimate, which is
   * deliberately naive — see {@link estimateSecretStrength}.
   */
  strength?: SecretStrength | null;

  invalid?: boolean;
  /** Message shown below the field. */
  error?: ReactNode;
  hint?: ReactNode;

  /* -- Copy for every visible or announced string -------------------------- */
  labelReveal?: string;
  labelHide?: string;
  labelCopy?: string;
  labelCopied?: string;
  labelCopyFailed?: string;
  labelRegenerate?: string;
  labelStrength?: string;
  /** Band names, weakest first. */
  strengthLabels?: readonly [string, string, string, string, string];
  /** Shown where strength has not been measured — an empty field. */
  labelStrengthUnmeasured?: string;
  /** Announced when the reveal timeout fires. */
  labelAutoHidden?: string;
  /** Explains the read-only-while-redacted behaviour to assistive tech. */
  labelRedacted?: string;
  /** Formats the entropy readout. */
  formatBits?: (bits: number) => string;

  fullWidth?: boolean;
  wrapperClassName?: string;
}

const DEFAULT_STRENGTH_LABELS = [
  'very weak',
  'weak',
  'fair',
  'strong',
  'very strong',
] as const;

const DEFAULT_FORMAT_BITS = (bits: number) => `~${bits} bits`;

/**
 * A field for a secret: pre-shared key, token, password, private key.
 *
 * THE THREE RULES THIS COMPONENT EXISTS TO ENFORCE
 * -----------------------------------------------
 * 1. THE SECRET IS NEVER IN A `title`. A tooltip is not a security boundary; it
 *    is rendered on hover, captured by screenshots, read aloud by screen
 *    readers and serialised into the accessibility tree. Nothing here ever
 *    receives the value as a `title`, and the copy button carries a static
 *    label rather than a preview of what it will copy.
 *
 * 2. `redactWhenHidden` KEEPS IT OUT OF THE DOM. `type="password"` is a visual
 *    treatment: the string is still on the element and still in any DOM dump.
 *    Under `redactWhenHidden` the input holds a fixed-length placeholder and
 *    the real value is only mounted once the operator has explicitly revealed
 *    it. The placeholder length is FIXED rather than mirroring the secret,
 *    because a mask that grows with the value leaks its length.
 *
 * 3. REVEAL IS TEMPORARY. `revealTimeout` re-masks on a timer, because the
 *    realistic failure is not someone shoulder-surfing the moment of reveal —
 *    it is a key left legible on a shared screen for the rest of the afternoon.
 *
 * ACCESSIBILITY
 * -------------
 * The reveal control is a toggle button carrying `aria-pressed`, not a
 * checkbox and not a swapped label: the state must be queryable, and a button
 * whose accessible name flips between "Show" and "Hide" is announced
 * inconsistently across screen readers. Copy and auto-hide outcomes go through
 * one polite live region, so the operator hears what happened without the
 * interruption an assertive region would cause mid-task.
 */
export const CredentialField = forwardRef<HTMLInputElement, CredentialFieldProps>(
  function CredentialField(
    {
      value,
      defaultValue = '',
      onChange,
      size: sizeProp,

      allowReveal = true,
      revealed,
      defaultRevealed = false,
      onRevealedChange,
      revealTimeout,

      redactWhenHidden = false,
      maskCharacter = '•',
      maskLength = 12,

      allowCopy = true,
      onCopy,
      onCopyError,
      copyFeedbackMs = 1600,

      onRegenerate,
      regenerating = false,

      showStrength = false,
      strength,

      invalid: invalidProp = false,
      error,
      hint,

      labelReveal = 'Show secret',
      labelHide = 'Hide secret',
      labelCopy = 'Copy secret',
      labelCopied = 'Copied to clipboard',
      labelCopyFailed = 'Could not copy',
      labelRegenerate = 'Generate a new secret',
      labelStrength = 'Strength',
      strengthLabels = DEFAULT_STRENGTH_LABELS,
      labelStrengthUnmeasured = 'not measured',
      labelAutoHidden = 'Secret hidden again',
      labelRedacted = 'Hidden. Reveal to edit.',
      formatBits = DEFAULT_FORMAT_BITS,

      fullWidth = true,
      wrapperClassName,
      className,
      disabled,
      readOnly = false,
      placeholder,
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
    const id = idProp ?? field.id ?? `${reactId}-credential`;
    const size = sizeProp ?? field.size;
    const invalid = invalidProp || field.invalid;
    const messageId = `${id}-message`;
    const hintId = `${id}-hint`;
    const strengthId = `${id}-strength`;
    const redactedId = `${id}-redacted`;

    // A secret field with no name is announced as "edit text, blank" — the one
    // control on the page where guessing wrong is most expensive.
    warnMissingNetFieldLabel('CredentialField', {
      fieldId: field.id,
      fieldLabelId: field.labelId,
      ariaLabel: rest['aria-label'],
      ariaLabelledBy: rest['aria-labelledby'],
      explicitId: idProp,
    });

    const [secret, setSecret] = useControllableState<string>({ value, defaultValue, onChange });
    const [isRevealed, setRevealed] = useControllableState<boolean>({
      value: revealed,
      defaultValue: defaultRevealed,
      onChange: onRevealedChange,
    });

    const [announcement, setAnnouncement] = useState('');
    const [copiedAt, setCopiedAt] = useState<number | null>(null);

    /* A live region only speaks when its text CHANGES, so copying twice in a
     * row would be silent the second time. Alternating an invisible
     * zero-width space guarantees a real mutation every announcement. */
    const announceParity = useRef(false);
    const announce = useCallback((text: string) => {
      announceParity.current = !announceParity.current;
      setAnnouncement(announceParity.current ? text : `${text}\u200B`);
    }, []);

    // Retire the message once it has been read, so a user browsing the page
    // later does not encounter a stale "Copied to clipboard".
    useEffect(() => {
      if (announcement === '') return;
      const timer = setTimeout(() => setAnnouncement(''), 4000);
      return () => clearTimeout(timer);
    }, [announcement]);

    /* -- Auto re-mask ----------------------------------------------------- */
    useEffect(() => {
      if (!isRevealed || !revealTimeout || revealTimeout <= 0) return;
      const timer = setTimeout(() => {
        // `setRevealed` notifies `onRevealedChange` in BOTH controlled and
        // uncontrolled modes, so a controlled parent follows the timer without
        // any extra signal here.
        setRevealed(false);
        announce(labelAutoHidden);
      }, revealTimeout);
      return () => clearTimeout(timer);
      // `isRevealed` in the deps is what restarts the clock on every re-reveal.
    }, [isRevealed, revealTimeout, setRevealed, labelAutoHidden, announce]);

    /* -- Copy feedback ----------------------------------------------------- */
    useEffect(() => {
      if (copiedAt === null) return;
      const timer = setTimeout(() => setCopiedAt(null), copyFeedbackMs);
      return () => clearTimeout(timer);
    }, [copiedAt, copyFeedbackMs]);

    const handleCopy = useCallback(async () => {
      try {
        if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
          throw new Error('Clipboard API unavailable');
        }
        await navigator.clipboard.writeText(secret);
        setCopiedAt(Date.now());
        announce(labelCopied);
        onCopy?.(secret);
      } catch (err) {
        announce(labelCopyFailed);
        onCopyError?.(err);
      }
    }, [secret, labelCopied, labelCopyFailed, onCopy, onCopyError, announce]);

    /* -- Redaction --------------------------------------------------------- */
    const redacted = redactWhenHidden && !isRevealed;
    const maskText = useMemo(() => {
      const count = maskLength === 'value' ? secret.length : maskLength;
      return maskCharacter.repeat(Math.max(0, count));
    }, [maskLength, maskCharacter, secret.length]);

    // THIS is the guarantee. When redacted, the value handed to the DOM is the
    // placeholder — the secret never reaches the element.
    const domValue = redacted ? maskText : secret;
    const effectiveReadOnly = readOnly || redacted;

    /* -- Strength ---------------------------------------------------------- */
    const resolvedStrength = useMemo<SecretStrength | null>(() => {
      if (!showStrength) return null;
      if (strength !== undefined) return strength;
      if (secret.length === 0) return { score: 0, bits: null };
      return estimateSecretStrength(secret);
    }, [showStrength, strength, secret]);

    const strengthBand =
      resolvedStrength && resolvedStrength.bits !== null
        ? (strengthLabels[resolvedStrength.score] ?? DEFAULT_STRENGTH_LABELS[resolvedStrength.score])
        : labelStrengthUnmeasured;

    const describedBy = joinIds(
      field.describedBy,
      rest['aria-describedby'],
      error ? messageId : undefined,
      hint && !error ? hintId : undefined,
      resolvedStrength ? strengthId : undefined,
      redacted ? redactedId : undefined,
    );

    const handleBlur = useEventCallback(onBlur);
    const handleFocus = useEventCallback(onFocus);
    const buttonSize = size === 'lg' ? 'sm' : 'xs';

    return (
      <div
        data-stratum="credential-field"
        data-size={size}
        data-revealed={isRevealed || undefined}
        data-redacted={redacted || undefined}
        data-full-width={fullWidth || undefined}
        className={clsx('stratum-net-field', 'stratum-credential', wrapperClassName)}
      >
        <div
          className="stratum-net-control stratum-credential__control"
          data-size={size}
          data-validity={invalid ? 'invalid' : undefined}
          data-disabled={disabled || undefined}
          data-readonly={effectiveReadOnly || undefined}
        >
          <input
            {...rest}
            ref={ref}
            id={id}
            /* `text` while redacted because the value IS the placeholder — using
             * `password` there would double-mask a string of bullets. */
            type={isRevealed || redacted ? 'text' : 'password'}
            className={clsx('stratum-net-input', 'stratum-credential__input', className)}
            value={domValue}
            disabled={disabled}
            readOnly={effectiveReadOnly}
            placeholder={placeholder}
            /* Never `title` — see the component docs. */
            autoComplete={rest.autoComplete ?? 'off'}
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            aria-invalid={resolveAriaInvalid(invalid ? 'invalid' : 'valid', rest['aria-invalid'])}
            aria-describedby={describedBy}
            onChange={(event) => {
              if (redacted) return; // the placeholder is not the value
              setSecret(event.currentTarget.value);
            }}
            onFocus={(event) => handleFocus(event)}
            onBlur={(event) => handleBlur(event)}
          />

          <span className="stratum-net-adornment stratum-credential__actions">
            {allowCopy && (
              <Button
                variant="ghost"
                size={buttonSize}
                iconOnly
                aria-label={copiedAt !== null ? labelCopied : labelCopy}
                icon={copiedAt !== null ? CheckIcon : CopyIcon}
                disabled={disabled || secret.length === 0}
                className={clsx(
                  'stratum-credential__action',
                  copiedAt !== null && 'stratum-credential__action--copied',
                )}
                onClick={() => {
                  void handleCopy();
                }}
              />
            )}

            {allowReveal && (
              <Button
                variant="ghost"
                size={buttonSize}
                iconOnly
                /* A toggle button with a STABLE name plus `aria-pressed`. A
                 * button whose name flips between "Show" and "Hide" is
                 * announced inconsistently — some readers say the new state,
                 * some the action still available. */
                aria-pressed={isRevealed}
                aria-label={isRevealed ? labelHide : labelReveal}
                aria-controls={id}
                icon={isRevealed ? EyeOffIcon : EyeIcon}
                disabled={disabled}
                className="stratum-credential__action"
                onClick={() => setRevealed(!isRevealed)}
              />
            )}

            {onRegenerate && (
              <Button
                variant="ghost"
                size={buttonSize}
                iconOnly
                aria-label={labelRegenerate}
                icon={RegenerateIcon}
                loading={regenerating}
                loadingLabel={labelRegenerate}
                disabled={disabled}
                className="stratum-credential__action"
                onClick={onRegenerate}
              />
            )}
          </span>
        </div>

        {resolvedStrength && (
          <div
            id={strengthId}
            className="stratum-credential__strength"
            data-score={resolvedStrength.bits === null ? 'unmeasured' : resolvedStrength.score}
          >
            <span className="stratum-credential__strength-label">{labelStrength}</span>
            <span
              className="stratum-credential__meter"
              role="img"
              aria-label={`${labelStrength}: ${strengthBand}`}
            >
              {[0, 1, 2, 3, 4].map((step) => (
                <span
                  key={step}
                  className="stratum-credential__segment"
                  data-filled={
                    resolvedStrength.bits !== null && step <= resolvedStrength.score
                      ? true
                      : undefined
                  }
                  aria-hidden="true"
                />
              ))}
            </span>
            <span
              className="stratum-credential__strength-band"
              data-unobserved={resolvedStrength.bits === null || undefined}
            >
              {strengthBand}
            </span>
            {resolvedStrength.bits !== null && (
              <span className="stratum-credential__bits">{formatBits(resolvedStrength.bits)}</span>
            )}
          </div>
        )}

        {redacted && (
          <span id={redactedId} className="stratum-visually-hidden">
            {labelRedacted}
          </span>
        )}

        {error && (
          <p id={messageId} className="stratum-net-message" data-tone="error">
            <span className="stratum-net-message__text">{error}</span>
          </p>
        )}
        {hint && !error && (
          <p id={hintId} className="stratum-net-message" data-tone="hint">
            <span className="stratum-net-message__text">{hint}</span>
          </p>
        )}

        {/* One polite region for copy and auto-hide outcomes. Polite rather
         * than assertive: the operator is usually mid-task and an interruption
         * costs more than the delay. */}
        <span role="status" aria-live="polite" className="stratum-visually-hidden">
          {announcement}
        </span>
      </div>
    );
  },
);
