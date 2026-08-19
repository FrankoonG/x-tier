import { forwardRef, useRef, type ReactNode } from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { Input, setNativeInputValue, useMergedRefs, type InputProps } from '../Input/Input';
import './PasswordInput.css';

/**
 * Default generator alphabet: 70 characters, with the ambiguous glyphs removed
 * (`0/O`, `1/l/I`) because these secrets get read aloud, typed from a screen
 * and pasted into terminals. Dropping them costs ~0.1 bits per character and
 * removes an entire class of support ticket.
 */
export const PASSWORD_ALPHABET =
  'abcdefghijkmnopqrstuvwxyz' + 'ABCDEFGHJKLMNPQRSTUVWXYZ' + '23456789' + '!@#$%^&*-_=+?';

const CLASS_PATTERNS = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^a-zA-Z0-9]/];
const MAX_CLASS_ATTEMPTS = 64;

function randomBytes(length: number): Uint8Array {
  const source = globalThis.crypto;
  if (!source || typeof source.getRandomValues !== 'function') {
    throw new Error(
      '[stratum] <PasswordInput> cannot generate a password: crypto.getRandomValues is ' +
        'unavailable. Pass `generateValue` to supply your own generator. Math.random is ' +
        'never used as a fallback.',
    );
  }
  return source.getRandomValues(new Uint8Array(length));
}

/**
 * Cryptographically uniform random string.
 *
 * REJECTION SAMPLING, AND WHY IT MATTERS
 * --------------------------------------
 * The obvious implementation, `alphabet[byte % alphabet.length]`, is biased
 * whenever the alphabet length does not divide 256. hy2scale shipped exactly
 * that with a 36-character alphabet: 256 = 7*36 + 4, so the first four
 * characters of its alphabet came up eight times per 256 draws and the other
 * thirty-two came up seven — those four are ~14% more likely than the rest.
 * That is a measurable entropy loss on every character, and it is silent.
 *
 * The fix is to reject the values that make the range uneven. `limit` is the
 * largest multiple of the alphabet length at or below 256; bytes at or above it
 * are discarded and redrawn, which leaves the surviving bytes exactly uniform
 * over the alphabet. With the 70-character default about 18% of bytes are
 * discarded, so a generous over-draw keeps this to a single `getRandomValues`
 * call in practice.
 *
 * When the default alphabet is in use, strings missing a character class are
 * also rejected *as a whole* and redrawn. Patching a missing class into a fixed
 * position — the usual shortcut — would reintroduce bias at that position;
 * whole-string rejection does not, because it only conditions on the result.
 *
 * @param length Number of characters. Defaults to 20.
 * @param alphabet Characters to draw from. Must be 2-256 unique characters.
 */
export function generatePassword(length = 20, alphabet: string = PASSWORD_ALPHABET): string {
  if (!Number.isInteger(length) || length < 1) {
    throw new Error('[stratum] generatePassword: `length` must be a positive integer.');
  }
  const chars = Array.from(alphabet);
  const size = chars.length;
  if (size < 2 || size > 256) {
    throw new Error('[stratum] generatePassword: `alphabet` must contain 2-256 characters.');
  }

  const limit = Math.floor(256 / size) * size;
  const enforceClasses = alphabet === PASSWORD_ALPHABET && length >= CLASS_PATTERNS.length;

  const draw = (): string => {
    const out: string[] = [];
    while (out.length < length) {
      // Over-draw by the expected rejection rate so one syscall usually does.
      const need = length - out.length;
      const bytes = randomBytes(Math.ceil((need * 256) / limit) + 8);
      for (const byte of bytes) {
        if (byte >= limit) continue; // biased tail — discard, do not fold it in
        const char = chars[byte % size];
        if (char === undefined) continue;
        out.push(char);
        if (out.length === length) break;
      }
    }
    return out.join('');
  };

  let candidate = draw();
  if (enforceClasses) {
    for (let attempt = 1; attempt < MAX_CLASS_ATTEMPTS; attempt += 1) {
      if (CLASS_PATTERNS.every((pattern) => pattern.test(candidate))) break;
      candidate = draw();
    }
  }
  return candidate;
}

const IconEye = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.4"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="M1.5 8s2.4-4 6.5-4 6.5 4 6.5 4-2.4 4-6.5 4-6.5-4-6.5-4Z" />
    <circle cx="8" cy="8" r="1.9" />
  </svg>
);

const IconEyeOff = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.4"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="M6.3 3.7A6.9 6.9 0 0 1 8 3.5c4.1 0 6.5 4 6.5 4a12 12 0 0 1-2 2.4M4.2 4.9A11.9 11.9 0 0 0 1.5 7.5s2.4 4 6.5 4c1 0 1.9-.2 2.7-.6" />
    <path d="M6.7 6.3a1.9 1.9 0 0 0 2.6 2.7M2 2l12 12" />
  </svg>
);

const IconGenerate = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.4"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="M8 2.2 9 5l2.8 1-2.8 1L8 9.8 7 7 4.2 6 7 5l1-2.8ZM12.6 9.4l.5 1.4 1.4.5-1.4.5-.5 1.4-.5-1.4-1.4-.5 1.4-.5.5-1.4ZM3.6 9.9l.4 1.1 1.1.4-1.1.4-.4 1.1-.4-1.1-1.1-.4 1.1-.4.4-1.1Z" />
  </svg>
);

export interface PasswordInputProps
  extends Omit<InputProps, 'type' | 'suffix' | 'clearable' | 'onClear' | 'clearLabel'> {
  /** Controlled reveal state. */
  revealed?: boolean;
  defaultRevealed?: boolean;
  onRevealedChange?: (revealed: boolean) => void;
  /** Hides the reveal toggle entirely. */
  hideRevealToggle?: boolean;
  /**
   * Accessible name for the reveal toggle. It names the *action* and stays
   * stable in both states; `aria-pressed` reports whether it is currently on.
   */
  revealLabel?: string;
  /**
   * @deprecated No longer used. The toggle keeps a stable name (`revealLabel`)
   * and carries its state in `aria-pressed`; a name that flips between "Show"
   * and "Hide" contradicts the pressed state and is announced inconsistently.
   * Kept for source compatibility and ignored.
   */
  hideLabel?: string;
  /** Accessible name for the generate button. */
  generateLabel?: string;
  /**
   * Called with a freshly generated value. Providing it shows the generate
   * button; the value is also written into the field, so a plain `onChange`
   * handler or a form library sees it too.
   */
  onGenerate?: (value: string) => void;
  /** Shows the generate button without an `onGenerate` handler. */
  generate?: boolean;
  /** Overrides the built-in generator. */
  generateValue?: () => string;
  generateLength?: number;
  generateAlphabet?: string;
  /** Reveals the field after generating, so the value can be checked. */
  revealOnGenerate?: boolean;
  /** Extra adornment, rendered before the toggle and generate buttons. */
  suffix?: ReactNode;
}

/**
 * Masked text field with a reveal toggle and an optional generator.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - The toggle is a real toggle button: `aria-pressed` reports the current
 *   state and the label names the *action*, which is how a screen reader user
 *   hears "Show password, toggle button, not pressed". A `title` would not be
 *   announced reliably and never appears on touch.
 * - The toggle stays mounted across the state change, so focus is never
 *   destroyed underneath a keyboard user mid-interaction.
 * - Generating reveals by default. A value you cannot read is a value you
 *   cannot verify before saving, and the reveal is the direct result of an
 *   explicit user action.
 */
export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  function PasswordInput(
    {
      revealed: revealedProp,
      defaultRevealed = false,
      onRevealedChange,
      hideRevealToggle = false,
      revealLabel = 'Show password',
      // Deprecated and deliberately unread: destructured only so it never
      // reaches the DOM through `...rest`.
      hideLabel: _hideLabel,
      generateLabel = 'Generate password',
      onGenerate,
      generate = false,
      generateValue,
      generateLength = 20,
      generateAlphabet = PASSWORD_ALPHABET,
      revealOnGenerate = true,
      suffix,
      size = 'md',
      disabled = false,
      readOnly = false,
      className,
      autoComplete,
      ...rest
    },
    ref,
  ) {
    const innerRef = useRef<HTMLInputElement | null>(null);
    const mergedRef = useMergedRefs<HTMLInputElement>(innerRef, ref);

    const [revealed, setRevealed] = useControllableState<boolean>({
      value: revealedProp,
      defaultValue: defaultRevealed,
      onChange: onRevealedChange,
    });

    const showGenerate = generate || onGenerate !== undefined;

    const handleGenerate = () => {
      const next = generateValue
        ? generateValue()
        : generatePassword(generateLength, generateAlphabet);
      const element = innerRef.current;
      // Writing through the native setter is what makes the generated value
      // reach an `onChange` handler and any form library listening to it.
      // Focus deliberately stays on the button: people generate several times
      // before settling, and stealing focus into the field would cost a
      // shift+tab on every attempt.
      const written = element ? setNativeInputValue(element, next) : false;
      if (!written) {
        // Reporting a value the field never took would leave the visible
        // control and the consumer's form state permanently disagreeing —
        // on a password, silently.
        if (import.meta.env?.DEV) {
          console.error(
            '[stratum] <PasswordInput> could not write the generated value into the field.',
          );
        }
        return;
      }
      if (revealOnGenerate) setRevealed(true);
      onGenerate?.(next);
    };

    return (
      <Input
        {...rest}
        ref={mergedRef}
        data-stratum="password-input"
        className={clsx('stratum-password-input', className)}
        type={revealed ? 'text' : 'password'}
        size={size}
        disabled={disabled}
        readOnly={readOnly}
        autoComplete={autoComplete}
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        suffix={
          <>
            {suffix}
            {showGenerate && (
              <button
                type="button"
                className="stratum-input__action"
                data-action="generate"
                aria-label={generateLabel}
                disabled={disabled || readOnly}
                onClick={handleGenerate}
              >
                <IconGenerate />
              </button>
            )}
            {!hideRevealToggle && (
              <button
                type="button"
                className="stratum-input__action"
                data-action="reveal"
                // STABLE name plus `aria-pressed`. A name that flipped to
                // "Hide password" while pressed would say the action is still
                // available and the state is already on, in the same breath.
                aria-label={revealLabel}
                aria-pressed={revealed}
                // Revealing does not modify the value, so it stays available
                // on a read-only field.
                disabled={disabled}
                onClick={() => setRevealed(!revealed)}
              >
                {revealed ? <IconEyeOff /> : <IconEye />}
              </button>
            )}
          </>
        }
      />
    );
  },
);
