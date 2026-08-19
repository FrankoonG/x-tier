import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ClipboardEvent as ReactClipboardEvent,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { VisuallyHidden } from '../../primitives/VisuallyHidden';
import { useFieldControl } from '../Field/Field';
import { InlineMessage } from '../InlineMessage/InlineMessage';
import { useIsomorphicLayoutEffect } from '../Popover/overlayFocus';
import { Tag } from '../Tag/Tag';
import './TagInput.css';

/* -------------------------------------------------------------------------- */
/* Types                                                                       */
/* -------------------------------------------------------------------------- */

/**
 * What happens when a token that is already present is entered again.
 *
 * - `reject` — refused, with a message. Use where a repeat is a mistake.
 * - `merge`  — refused silently but announced, and the existing token is
 *              flashed so the user can see it was already there.
 * - `allow`  — kept. In an ACL a repeat is sometimes deliberate.
 */
export type TagInputDuplicatePolicy = 'reject' | 'merge' | 'allow';

export type TagInputSize = 'sm' | 'md' | 'lg';

/**
 * PROP ROUTING mirrors `Input`: `className` and `style` land on the wrapper,
 * because that is the box a consumer needs to size and space; everything else
 * in `...rest` lands on the `<input>`, because those are native input
 * attributes and putting them on a wrapper breaks forms and screen readers.
 * The forwarded ref goes to the `<input>` for the same reason.
 */
export interface TagInputProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'value' | 'defaultValue' | 'onChange' | 'size' | 'type' | 'onPaste' | 'children'
  > {
  value?: string[];
  defaultValue?: string[];
  onChange?: (tokens: string[]) => void;

  inputValue?: string;
  defaultInputValue?: string;
  onInputValueChange?: (value: string) => void;

  /**
   * Returns a message when a token is not acceptable, or nothing when it is.
   *
   * An invalid token is COMMITTED anyway, marked, and left editable. Refusing
   * to tokenise strands the text in an input the user is about to leave, and
   * dropping it on blur loses data silently — paste fifty domains where three
   * are malformed and three vanish without a word. A token you can see and
   * fix beats a token that was never there.
   */
  validate?: (token: string, index: number, tokens: string[]) => string | null | undefined;
  /** Canonicalises a token on commit — lowercase, compress, strip a scheme. */
  normalize?: (raw: string) => string;
  /** Identity for duplicate detection. Defaults to the token itself. */
  tokenKey?: (token: string) => string;

  duplicates?: TagInputDuplicatePolicy;
  /** Hard cap. Further tokens are refused and announced. */
  max?: number;

  /**
   * Splits pasted text and typed input. Default splits on comma, semicolon,
   * tab and newline — deliberately NOT on a bare space, which is legitimate
   * inside many labels. Set `splitOnWhitespace` when tokens cannot contain
   * spaces.
   */
  delimiters?: RegExp;
  splitOnWhitespace?: boolean;
  /** Keys that commit the pending text. Default `Enter` and `,`. */
  commitKeys?: readonly string[];
  /** Commit on Tab as well as moving focus. Off by default: Tab means leave. */
  commitOnTab?: boolean;
  commitOnBlur?: boolean;
  /** Escape clears pending text before it reaches an enclosing dialog. */
  clearOnEscape?: boolean;

  size?: TagInputSize;
  invalid?: boolean;
  fullWidth?: boolean;
  /** Aggregated "n tokens need attention" message. */
  showIssueSummary?: boolean;

  onValidityChange?: (state: { valid: boolean; invalid: string[] }) => void;

  /* -- Copy --------------------------------------------------------------- */
  labelRemove?: (token: string) => string;
  labelEdit?: (token: string) => string;
  labelHelp?: string;
  labelCount?: (count: number) => string;
  labelAdded?: (count: number) => string;
  labelDuplicate?: (token: string) => string;
  labelRejectedDuplicate?: (count: number) => string;
  labelMaxReached?: (max: number) => string;
  labelRemoved?: (token: string, remaining: number) => string;
  labelEditing?: (token: string) => string;
  labelIssueSummary?: (count: number) => string;

  /** Class applied to the inner `<input>` rather than the wrapper. */
  inputClassName?: string;
}

const DEFAULT_DELIMITERS = /[,;\t\r\n]+/;
const WHITESPACE_DELIMITERS = /[\s,;]+/;

/**
 * How long the "you already added this one" highlight stays on the existing
 * chip. Not a motion duration — the CSS animation owns that and is halved
 * under reduced motion, where the highlight degrades to a static ring that
 * this timeout is what removes.
 */
const DUPLICATE_HIGHLIGHT_MS = 900;

const WarnIcon = () => (
  <svg viewBox="0 0 12 12" width="10" height="10" fill="none" focusable="false" aria-hidden="true">
    <path d="M6 1.4 11.2 10.6H0.8L6 1.4Z" stroke="currentColor" strokeWidth="1.1" strokeLinejoin="round" />
    <path d="M6 4.6v2.4" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    <circle cx="6" cy="8.6" r="0.65" fill="currentColor" />
  </svg>
);

/* -------------------------------------------------------------------------- */
/* Component                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * Free text committing into removable chips.
 *
 * PASTE IS THE PRIMARY INPUT, NOT AN EXTRA
 * ----------------------------------------
 * The operator's data arrives as a block — a column out of a spreadsheet, a
 * newline-separated list out of a terminal. A tokenising field without paste
 * splitting turns that into fifty round trips, so the paste handler splits,
 * normalises and commits every fragment in one go and reports what happened.
 *
 * BACKSPACE STEPS BACK IN, IT DOES NOT DELETE
 * -------------------------------------------
 * The common implementation removes the last chip on Backspace in an empty
 * input. That is destructive and unrecoverable, which is intolerable when a
 * token is a forty-character CIDR list entry: one stray keypress and it is
 * retyped from memory. Here Backspace pulls the last token back into the
 * input as editable text — reversible, and it doubles as the repair route for
 * a token that was committed invalid.
 *
 * KEYBOARD
 * --------
 * - Enter / comma (configurable) commits the pending text.
 * - Backspace on an empty input reopens the last token for editing.
 * - ArrowLeft from the start of an empty input moves into the tokens;
 *   ArrowLeft/ArrowRight/Home/End walk them, Enter or F2 reopens one for
 *   editing, Backspace or Delete removes one.
 * - Escape clears pending text and stops there. It does not bubble while it
 *   had something to do, so dismissing a half-typed token cannot also close
 *   the dialog the field is sitting in.
 */
export const TagInput = forwardRef<HTMLInputElement, TagInputProps>(function TagInput(
  {
    value: valueProp,
    defaultValue,
    onChange,
    inputValue: inputValueProp,
    defaultInputValue = '',
    onInputValueChange,
    validate,
    normalize,
    tokenKey,
    duplicates = 'merge',
    max,
    delimiters,
    splitOnWhitespace = false,
    commitKeys = ['Enter', ','],
    commitOnTab = false,
    commitOnBlur = true,
    clearOnEscape = true,
    size = 'md',
    invalid = false,
    disabled = false,
    readOnly = false,
    fullWidth = false,
    placeholder,
    showIssueSummary = true,
    onValidityChange,
    labelRemove,
    labelEdit,
    labelHelp = 'Type and press Enter to add. Press Backspace to edit the last item.',
    labelCount,
    labelAdded,
    labelDuplicate,
    labelRejectedDuplicate,
    labelMaxReached,
    labelRemoved,
    labelEditing,
    labelIssueSummary,
    inputClassName,
    name,
    id: idProp,
    className,
    style,
    // Pulled out of `rest` so the derived values below MERGE with them rather
    // than being clobbered by the later key in the JSX spread.
    'aria-describedby': ariaDescribedByProp,
    'aria-invalid': ariaInvalidProp,
    ...rest
  },
  ref,
) {
  const uid = useId();

  // Adopt the enclosing Field's identity when there is one. Without this the
  // component minted its own id, so the Field's `<label for>` pointed at an
  // element that did not exist and the text input had NO accessible name at
  // all — axe reports it as a critical `label` violation. Standalone use still
  // falls back to a generated id, and an explicit `id` prop still wins.
  const fieldControl = useFieldControl();
  const inputId = idProp ?? fieldControl.id ?? `${uid}input`;
  const helpId = `${uid}help`;

  const innerRef = useRef<HTMLInputElement | null>(null);
  const tokensRef = useRef<HTMLDivElement | null>(null);

  const setInputNode = useCallback(
    (node: HTMLInputElement | null) => {
      innerRef.current = node;
      if (typeof ref === 'function') ref(node);
      else if (ref) (ref as { current: HTMLInputElement | null }).current = node;
    },
    [ref],
  );

  const [tokens, setTokens] = useControllableState<string[]>({
    value: valueProp,
    defaultValue: defaultValue ?? [],
    onChange,
  });

  const [pending, setPending] = useControllableState<string>({
    value: inputValueProp,
    defaultValue: defaultInputValue,
    onChange: onInputValueChange,
  });

  const [announcement, setAnnouncement] = useState('');
  const [flashIndex, setFlashIndex] = useState<number | null>(null);
  const [pendingFocus, setPendingFocus] = useState<number | 'input' | null>(null);

  const keyOf = useCallback((token: string) => (tokenKey ? tokenKey(token) : token), [tokenKey]);

  const splitter = useMemo(
    () => delimiters ?? (splitOnWhitespace ? WHITESPACE_DELIMITERS : DEFAULT_DELIMITERS),
    [delimiters, splitOnWhitespace],
  );

  /* -- Validation --------------------------------------------------------- */
  const messages = useMemo(
    () => tokens.map((token, index) => (validate ? (validate(token, index, tokens) ?? null) : null)),
    [tokens, validate],
  );
  const invalidTokens = useMemo(
    () => tokens.filter((_, index) => messages[index] != null),
    [tokens, messages],
  );

  const validityRef = useRef<string>('');
  useEffect(() => {
    const signature = invalidTokens.join(' ');
    if (validityRef.current === signature) return;
    validityRef.current = signature;
    onValidityChange?.({ valid: invalidTokens.length === 0, invalid: invalidTokens });
  }, [invalidTokens, onValidityChange]);

  /* -- Focus after a structural change ------------------------------------ */
  useIsomorphicLayoutEffect(() => {
    if (pendingFocus === null) return;
    if (pendingFocus === 'input') {
      innerRef.current?.focus();
    } else {
      const holder = tokensRef.current?.querySelector<HTMLElement>(
        `[data-token-index="${pendingFocus}"] button`,
      );
      if (holder) holder.focus();
      else innerRef.current?.focus();
    }
    setPendingFocus(null);
  }, [pendingFocus, tokens]);

  useEffect(() => {
    if (flashIndex === null) return;
    const timer = setTimeout(() => setFlashIndex(null), DUPLICATE_HIGHLIGHT_MS);
    return () => clearTimeout(timer);
  }, [flashIndex]);

  /* -- Commit ------------------------------------------------------------- */
  const addTokens = useCallback(
    (raws: readonly string[]): void => {
      if (disabled || readOnly) return;

      const next = tokens.slice();
      let added = 0;
      let duplicateCount = 0;
      let lastDuplicate: string | null = null;
      let duplicateIndex: number | null = null;
      let hitMax = false;

      for (const raw of raws) {
        const trimmed = raw.trim();
        if (trimmed === '') continue;
        const token = normalize ? normalize(trimmed) : trimmed;
        if (token === '') continue;

        if (duplicates !== 'allow') {
          const existing = next.findIndex((other) => keyOf(other) === keyOf(token));
          if (existing >= 0) {
            duplicateCount += 1;
            lastDuplicate = token;
            duplicateIndex = existing;
            continue;
          }
        }

        if (max !== undefined && next.length >= max) {
          hitMax = true;
          break;
        }

        next.push(token);
        added += 1;
      }

      if (added > 0) setTokens(next);

      const parts: string[] = [];
      if (added > 0) {
        parts.push(labelAdded ? labelAdded(added) : `${added} added.`);
      }
      if (duplicateCount > 0) {
        if (duplicateCount === 1 && lastDuplicate !== null) {
          parts.push(
            labelDuplicate ? labelDuplicate(lastDuplicate) : `"${lastDuplicate}" is already added.`,
          );
        } else {
          parts.push(
            labelRejectedDuplicate
              ? labelRejectedDuplicate(duplicateCount)
              : `${duplicateCount} duplicates skipped.`,
          );
        }
        if (duplicates === 'merge' && duplicateIndex !== null) setFlashIndex(duplicateIndex);
      }
      if (hitMax && max !== undefined) {
        parts.push(labelMaxReached ? labelMaxReached(max) : `Limit of ${max} reached.`);
      }
      if (parts.length > 0) setAnnouncement(parts.join(' '));
    },
    [
      disabled,
      duplicates,
      keyOf,
      labelAdded,
      labelDuplicate,
      labelMaxReached,
      labelRejectedDuplicate,
      max,
      normalize,
      readOnly,
      setTokens,
      tokens,
    ],
  );

  const commitPending = useCallback((): boolean => {
    const text = pending;
    if (text.trim() === '') return false;
    addTokens(text.split(splitter));
    setPending('');
    return true;
  }, [addTokens, pending, setPending, splitter]);

  const removeAt = useCallback(
    (index: number, moveFocus: 'token' | 'input') => {
      if (disabled || readOnly) return;
      const token = tokens[index];
      if (token === undefined) return;
      const next = tokens.slice();
      next.splice(index, 1);
      setTokens(next);
      setAnnouncement(
        labelRemoved ? labelRemoved(token, next.length) : `${token} removed. ${next.length} left.`,
      );
      if (moveFocus === 'input' || next.length === 0) setPendingFocus('input');
      else setPendingFocus(Math.min(index, next.length - 1));
    },
    [disabled, labelRemoved, readOnly, setTokens, tokens],
  );

  const editAt = useCallback(
    (index: number) => {
      if (disabled || readOnly) return;
      const token = tokens[index];
      if (token === undefined) return;

      const next = tokens.slice();
      next.splice(index, 1);

      // Whatever was half-typed beside the token is committed in the same
      // step, so reopening a chip never silently discards it. Done inline
      // rather than through `addTokens`, which would be reading the
      // pre-removal array from this closure and re-add what we just took out.
      const carried = pending.trim();
      if (carried !== '') {
        const normalized = normalize ? normalize(carried) : carried;
        const duplicate =
          duplicates !== 'allow' && next.some((other) => keyOf(other) === keyOf(normalized));
        const hasRoom = max === undefined || next.length < max;
        if (normalized !== '' && !duplicate && hasRoom) next.push(normalized);
      }

      setTokens(next);
      setPending(token);
      setPendingFocus('input');
      setAnnouncement(labelEditing ? labelEditing(token) : `Editing ${token}.`);
    },
    [
      disabled,
      duplicates,
      keyOf,
      labelEditing,
      max,
      normalize,
      pending,
      readOnly,
      setPending,
      setTokens,
      tokens,
    ],
  );

  /* -- Input keyboard ------------------------------------------------------ */
  const handleInputKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    if (event.defaultPrevented) return;
    const native = event.nativeEvent as unknown as { isComposing?: boolean };
    if (native.isComposing) return;

    if (event.key === 'Tab' && commitOnTab && pending.trim() !== '') {
      commitPending();
      return;
    }

    if (commitKeys.includes(event.key)) {
      if (pending.trim() === '') return;
      event.preventDefault();
      commitPending();
      return;
    }

    if (event.key === 'Escape') {
      if (!clearOnEscape || pending === '') return;
      event.preventDefault();
      // The layer this field sits in must not also close. Dismissing the
      // innermost thing that had something to dismiss is the whole rule.
      event.stopPropagation();
      setPending('');
      return;
    }

    const target = event.currentTarget;
    const atStart = target.selectionStart === 0 && target.selectionEnd === 0;

    if (event.key === 'Backspace' && pending === '' && tokens.length > 0) {
      event.preventDefault();
      const last = tokens[tokens.length - 1];
      if (last === undefined) return;
      setTokens(tokens.slice(0, -1));
      setPending(last);
      setAnnouncement(labelEditing ? labelEditing(last) : `Editing ${last}.`);
      return;
    }

    if (event.key === 'ArrowLeft' && atStart && tokens.length > 0) {
      event.preventDefault();
      setPendingFocus(tokens.length - 1);
    }
  };

  const handlePaste = (event: ReactClipboardEvent<HTMLInputElement>) => {
    if (disabled || readOnly) return;
    const text = event.clipboardData.getData('text/plain');
    if (text === '') return;
    if (!splitter.test(text)) {
      // A single value: let the browser paste it normally so the caret and
      // undo stack behave the way the user expects.
      splitter.lastIndex = 0;
      return;
    }
    splitter.lastIndex = 0;
    event.preventDefault();
    // Everything in the block commits, including the final fragment. A bulk
    // paste is a complete list; leaving its last entry stranded in the input
    // is the shape of bug where a saved config is one endpoint short.
    addTokens(`${pending}${text}`.split(splitter));
    setPending('');
  };

  /* -- Token keyboard ------------------------------------------------------ */
  const handleTokensKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement | null;
    const holder = target?.closest<HTMLElement>('[data-token-index]');
    if (!holder) return;
    const index = Number.parseInt(holder.dataset['tokenIndex'] ?? '', 10);
    if (Number.isNaN(index)) return;

    switch (event.key) {
      case 'ArrowLeft':
        event.preventDefault();
        if (index > 0) setPendingFocus(index - 1);
        break;
      case 'ArrowRight':
        event.preventDefault();
        if (index < tokens.length - 1) setPendingFocus(index + 1);
        else setPendingFocus('input');
        break;
      case 'Home':
        event.preventDefault();
        setPendingFocus(0);
        break;
      case 'End':
        event.preventDefault();
        setPendingFocus(tokens.length - 1);
        break;
      case 'Enter':
      case 'F2':
        // Enter on a <button> activates it on keydown, so this must both
        // preventDefault and run before the remove fires.
        event.preventDefault();
        event.stopPropagation();
        editAt(index);
        break;
      case 'Backspace':
      case 'Delete':
        event.preventDefault();
        removeAt(index, 'token');
        break;
      default:
        break;
    }
  };

  /* -- Shell --------------------------------------------------------------- */
  const handleShellPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return;
    const target = event.target as HTMLElement | null;
    if (!target) return;
    if (target.closest('button, a, input, select, textarea, [tabindex]')) return;
    event.preventDefault();
    innerRef.current?.focus();
  };

  const countText = labelCount
    ? labelCount(tokens.length)
    : `${tokens.length} item${tokens.length === 1 ? '' : 's'}.`;

  return (
    <div
      data-stratum="tag-input"
      data-size={size}
      data-invalid={invalid || invalidTokens.length > 0 || undefined}
      data-disabled={disabled || undefined}
      data-readonly={readOnly || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-tag-input', className)}
      style={style}
    >
      <div className="stratum-tag-input__control" onPointerDown={handleShellPointerDown}>
        <div
          ref={tokensRef}
          className="stratum-tag-input__tokens"
          onKeyDown={handleTokensKeyDown}
        >
          {tokens.map((token, index) => {
            const message = messages[index] ?? null;
            return (
              <Tag
                key={`${keyOf(token)}-${index}`}
                data-token-index={index}
                data-flash={flashIndex === index || undefined}
                className="stratum-tag-input__token"
                size={size === 'lg' ? 'md' : 'sm'}
                variant={message != null ? 'danger' : 'neutral'}
                outline={message != null}
                icon={message != null ? <WarnIcon /> : undefined}
                onRemove={disabled || readOnly ? undefined : () => removeAt(index, 'token')}
                removeLabel={labelRemove ? labelRemove(token) : `Remove ${token}`}
                disabled={disabled}
              >
                {token}
                {message != null && (
                  // Part of the token's own text, so reaching the chip reads
                  // the reason. The icon carries the same information visually,
                  // so nothing here is conveyed by colour alone.
                  <VisuallyHidden>{` — ${message}`}</VisuallyHidden>
                )}
              </Tag>
            );
          })}

          <input
            autoComplete="off"
            {...rest}
            ref={setInputNode}
            id={inputId}
            type="text"
            className={clsx('stratum-tag-input__field', inputClassName)}
            value={pending}
            placeholder={tokens.length === 0 ? placeholder : undefined}
            disabled={disabled}
            readOnly={readOnly}
            aria-invalid={ariaInvalidProp ?? (invalid || fieldControl.invalid || undefined)}
            // The Field's description, hint and error ids join the component's
            // own help text rather than replacing it.
            aria-describedby={clsx(helpId, fieldControl.describedBy, ariaDescribedByProp) || undefined}
            required={rest.required ?? fieldControl.required}
            onChange={(event) => setPending(event.target.value)}
            // The consumer's handlers run alongside ours rather than being
            // clobbered by the spread order — and theirs runs first, so a
            // `preventDefault` from a wrapper still suppresses our commit.
            onKeyDown={(event) => {
              rest.onKeyDown?.(event);
              handleInputKeyDown(event);
            }}
            onPaste={handlePaste}
            onBlur={(event) => {
              if (commitOnBlur) commitPending();
              rest.onBlur?.(event);
            }}
          />
        </div>

        {/* One hidden input per token: a form post gets the whole set, and the
            visible field never carries the `name` so a half-typed fragment
            cannot be submitted as if it were a committed token. */}
        {name !== undefined &&
          tokens.map((token, index) => (
            <input key={`${name}-${index}`} type="hidden" name={name} value={token} />
          ))}
      </div>

      <VisuallyHidden id={helpId}>{`${labelHelp} ${countText}`}</VisuallyHidden>

      {showIssueSummary && invalidTokens.length > 0 && (
        <InlineMessage variant="danger" size="xs" role="status">
          {labelIssueSummary
            ? labelIssueSummary(invalidTokens.length)
            : `${invalidTokens.length} of ${tokens.length} need attention.`}
        </InlineMessage>
      )}

      <VisuallyHidden role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </VisuallyHidden>
    </div>
  );
});
