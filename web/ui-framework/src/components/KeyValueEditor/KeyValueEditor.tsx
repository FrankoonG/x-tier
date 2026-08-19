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
import { useControllableState } from '../../hooks/useControllableState';
import { VisuallyHidden } from '../../primitives/VisuallyHidden';
import { Checkbox } from '../Checkbox/Checkbox';
import { EditableList } from '../EditableList/EditableList';
import { InlineMessage } from '../InlineMessage/InlineMessage';
import { Input } from '../Input/Input';
import { SegmentedControl } from '../SegmentedControl/SegmentedControl';
import { Textarea } from '../Textarea/Textarea';
import './KeyValueEditor.css';

/* -------------------------------------------------------------------------- */
/* Types                                                                       */
/* -------------------------------------------------------------------------- */

export interface KeyValuePair {
  key: string;
  value: string;
  /**
   * `false` renders the row struck through and serialises it behind the
   * comment marker. Only meaningful when `allowDisabledRows` is set.
   */
  enabled?: boolean;
}

export type KeyValueEditorMode = 'form' | 'text';

/** A problem with one line of the raw text buffer. `line` is 1-based. */
export interface KeyValueLineIssue {
  line: number;
  text: string;
  message: string;
}

/** Which half of a row a validation message belongs to. */
export type KeyValueField = 'key' | 'value';

export interface KeyValueRowIssue {
  field: KeyValueField;
  message: string;
}

interface Grammar {
  separator: string;
  disabledMarker: string;
  allowDisabledRows: boolean;
  trimEntries: boolean;
}

export interface KeyValueEditorProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'onChange' | 'defaultValue'> {
  pairs?: KeyValuePair[];
  defaultPairs?: KeyValuePair[];
  onPairsChange?: (pairs: KeyValuePair[]) => void;

  mode?: KeyValueEditorMode;
  defaultMode?: KeyValueEditorMode;
  onModeChange?: (mode: KeyValueEditorMode) => void;

  /**
   * Text between key and value. Split is on the FIRST occurrence, so a value
   * may contain the separator freely and a key may not — which is exactly the
   * rule that keeps the round trip lossless.
   */
  separator?: string;
  /** Prefix marking a disabled row in text mode. Default `'#'`. */
  disabledMarker?: string;
  /** Adds a per-row enable switch, and the comment form in text mode. */
  allowDisabledRows?: boolean;
  allowDuplicateKeys?: boolean;
  /** Strips surrounding whitespace from both halves on parse. */
  trimEntries?: boolean;

  minRows?: number;
  maxRows?: number;
  reorderable?: boolean;
  showRank?: boolean;
  size?: 'sm' | 'md';
  disabled?: boolean;
  /** Rows in the raw text area. */
  textRows?: number;
  emptyState?: ReactNode;

  onIssuesChange?: (issues: KeyValueLineIssue[]) => void;

  /* -- Copy --------------------------------------------------------------- */
  itemLabel?: string;
  addLabel?: string;
  keyLabel?: string;
  valueLabel?: string;
  enabledLabel?: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  textPlaceholder?: string;
  modeLabel?: string;
  formModeLabel?: string;
  textModeLabel?: string;
  textHint?: string;
  labelKeyRequired?: string;
  labelKeyHasSeparator?: (separator: string) => string;
  labelKeyHasNewline?: string;
  labelValueHasNewline?: string;
  labelDuplicateKey?: (key: string) => string;
  labelMissingSeparator?: (separator: string) => string;
  labelLineIssue?: (line: number, message: string) => string;
  labelIssueSummary?: (count: number) => string;
}

/* -------------------------------------------------------------------------- */
/* Grammar                                                                     */
/* -------------------------------------------------------------------------- */

/**
 * Rows -> text. The inverse of `parseText` for every pair that passes
 * `validatePair`, which is what "lossless for valid input" means here.
 */
export function serializeKeyValuePairs(
  pairs: readonly KeyValuePair[],
  grammar: Grammar,
): string {
  return pairs
    .map((pair) => {
      const prefix =
        grammar.allowDisabledRows && pair.enabled === false ? `${grammar.disabledMarker} ` : '';
      return `${prefix}${pair.key}${grammar.separator}${pair.value}`;
    })
    .join('\n');
}

/**
 * Text -> rows.
 *
 * A malformed line NEVER disappears. It becomes a row carrying the raw text
 * and an issue, so switching to the form view and back cannot silently eat
 * work — the failure mode that makes people distrust a mode toggle.
 *
 * Blank lines are the one thing dropped, and only in this direction: they
 * carry no pair, and `pairs -> text -> pairs` is unaffected by that.
 */
export function parseKeyValueText(
  text: string,
  grammar: Grammar,
  copy: {
    missingSeparator: (separator: string) => string;
    keyRequired: string;
  },
): { pairs: KeyValuePair[]; issues: KeyValueLineIssue[] } {
  const pairs: KeyValuePair[] = [];
  const issues: KeyValueLineIssue[] = [];
  const lines = text.split(/\r\n|\r|\n/);

  lines.forEach((line, index) => {
    if (line.trim() === '') return;

    let body = line;
    let enabled = true;

    if (grammar.allowDisabledRows && body.trimStart().startsWith(grammar.disabledMarker)) {
      enabled = false;
      body = body.trimStart().slice(grammar.disabledMarker.length);
      if (grammar.trimEntries) body = body.trimStart();
    }

    const at = body.indexOf(grammar.separator);
    if (at < 0) {
      issues.push({
        line: index + 1,
        text: line,
        message: copy.missingSeparator(grammar.separator),
      });
      pairs.push({ key: grammar.trimEntries ? body.trim() : body, value: '', enabled });
      return;
    }

    let key = body.slice(0, at);
    let value = body.slice(at + grammar.separator.length);
    if (grammar.trimEntries) {
      key = key.trim();
      value = value.trim();
    }
    if (key === '') {
      issues.push({ line: index + 1, text: line, message: copy.keyRequired });
    }
    pairs.push({ key, value, enabled });
  });

  return { pairs, issues };
}

/* -------------------------------------------------------------------------- */
/* Component                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * Key/value rows with a raw-text view behind a toggle.
 *
 * WHY THE TEXT VIEW EARNS ITS KEEP
 * --------------------------------
 * The rows are how you edit one entry; the text is how you move twelve. Copy
 * a block out of one panel, paste it into another, done — versus retyping a
 * dozen rows. Every serious tool that has this pattern (Postman's Bulk Edit,
 * Grafana's Builder/Code, Cloudflare's expression editor) exists for that one
 * workflow.
 *
 * THE SYNC CONTRACT IS "BIDIRECTIONAL, NON-DESTRUCTIVE"
 * -----------------------------------------------------
 * The four contracts in the wild are bidirectional, bidirectional-lossy,
 * one-way-with-copy-escape, and blocked-with-discard. All but the first make
 * the user gamble on whether switching views will cost them work.
 *
 * This component takes the first, and pays for it in the grammar rather than
 * in a dialog:
 *
 *   - split on the FIRST separator, so values may contain it and only keys
 *     may not — and a key that does is flagged, not silently truncated;
 *   - a line with no separator becomes a row plus an issue, never a discard;
 *   - the disabled state has a text form (`# key=value`), so it survives the
 *     round trip instead of being one of those "things the other view cannot
 *     express" that quietly vanish.
 *
 * Consequently there is no confirm-or-lose dialog on the toggle, because
 * there is nothing to lose.
 *
 * WHILE TEXT MODE IS OPEN, TEXT IS THE SOURCE OF TRUTH
 * ----------------------------------------------------
 * The buffer is seeded from `pairs` when the view opens and is not re-seeded
 * afterwards. Re-serialising an echoed `pairs` back into the textarea on every
 * keystroke would fight the caret and normalise half-typed lines under the
 * user's hands. Edits flow outward continuously, so nothing is pending at the
 * moment of the switch back.
 */
export const KeyValueEditor = forwardRef<HTMLDivElement, KeyValueEditorProps>(
  function KeyValueEditor(
    {
      pairs: pairsProp,
      defaultPairs,
      onPairsChange,
      mode: modeProp,
      defaultMode = 'form',
      onModeChange,
      separator = '=',
      disabledMarker = '#',
      allowDisabledRows = false,
      allowDuplicateKeys = true,
      trimEntries = true,
      minRows = 0,
      maxRows,
      reorderable = true,
      showRank = false,
      size = 'md',
      disabled = false,
      textRows = 8,
      emptyState,
      onIssuesChange,
      itemLabel = 'Entry',
      addLabel,
      keyLabel = 'Key',
      valueLabel = 'Value',
      enabledLabel = 'Enabled',
      keyPlaceholder = 'key',
      valuePlaceholder = 'value',
      textPlaceholder,
      modeLabel = 'Editing mode',
      formModeLabel = 'Rows',
      textModeLabel = 'Text',
      textHint,
      labelKeyRequired = 'A key is required.',
      labelKeyHasSeparator,
      labelKeyHasNewline = 'A key cannot contain a line break.',
      labelValueHasNewline = 'A value cannot contain a line break.',
      labelDuplicateKey,
      labelMissingSeparator,
      labelLineIssue,
      labelIssueSummary,
      className,
      ...rest
    },
    ref,
  ) {
    const uid = useId();
    const issuesId = `${uid}issues`;
    const hintId = `${uid}hint`;

    const [pairs, setPairs] = useControllableState<KeyValuePair[]>({
      value: pairsProp,
      defaultValue: defaultPairs ?? [],
      onChange: onPairsChange,
    });

    const [mode, setMode] = useControllableState<KeyValueEditorMode>({
      value: modeProp,
      defaultValue: defaultMode,
      onChange: onModeChange,
    });

    const grammar = useMemo<Grammar>(
      () => ({ separator, disabledMarker, allowDisabledRows, trimEntries }),
      [separator, disabledMarker, allowDisabledRows, trimEntries],
    );

    const missingSeparator = useCallback(
      (sep: string) =>
        labelMissingSeparator ? labelMissingSeparator(sep) : `Expected "${sep}" on this line.`,
      [labelMissingSeparator],
    );

    const parseCopy = useMemo(
      () => ({ missingSeparator, keyRequired: labelKeyRequired }),
      [missingSeparator, labelKeyRequired],
    );

    /* -- Raw text buffer --------------------------------------------------- */
    const [text, setText] = useState<string>('');
    const seededModeRef = useRef<KeyValueEditorMode | null>(null);

    useEffect(() => {
      if (mode === 'text' && seededModeRef.current !== 'text') {
        setText(serializeKeyValuePairs(pairs, grammar));
      }
      seededModeRef.current = mode;
      // `pairs` is in the dependency list for honesty, but the ref guard means
      // it only ever seeds on the transition INTO text mode.
    }, [mode, pairs, grammar]);

    const parsed = useMemo(
      () => (mode === 'text' ? parseKeyValueText(text, grammar, parseCopy) : null),
      [mode, text, grammar, parseCopy],
    );
    const lineIssues = parsed?.issues ?? [];

    const issuesRef = useRef<KeyValueLineIssue[]>([]);
    useEffect(() => {
      const previous = issuesRef.current;
      const changed =
        previous.length !== lineIssues.length ||
        previous.some((issue, index) => {
          const next = lineIssues[index];
          return !next || next.line !== issue.line || next.message !== issue.message;
        });
      if (!changed) return;
      issuesRef.current = lineIssues;
      onIssuesChange?.(lineIssues);
    }, [lineIssues, onIssuesChange]);

    const handleTextChange = (next: string) => {
      setText(next);
      setPairs(parseKeyValueText(next, grammar, parseCopy).pairs);
    };

    /* -- Row validation ---------------------------------------------------- */
    const validatePair = useCallback(
      (pair: KeyValuePair, index: number, all: KeyValuePair[]): KeyValueRowIssue | null => {
        if (pair.key === '' && pair.value === '') return null;
        if (pair.key.trim() === '') {
          return { field: 'key', message: labelKeyRequired };
        }
        if (pair.key.includes(separator)) {
          return {
            field: 'key',
            message: labelKeyHasSeparator
              ? labelKeyHasSeparator(separator)
              : `A key cannot contain "${separator}".`,
          };
        }
        if (/[\r\n]/.test(pair.key)) return { field: 'key', message: labelKeyHasNewline };
        if (/[\r\n]/.test(pair.value)) return { field: 'value', message: labelValueHasNewline };
        if (!allowDuplicateKeys) {
          const first = all.findIndex((other) => other.key === pair.key);
          if (first >= 0 && first < index) {
            return {
              field: 'key',
              message: labelDuplicateKey
                ? labelDuplicateKey(pair.key)
                : `"${pair.key}" is already defined above.`,
            };
          }
        }
        return null;
      },
      [
        allowDuplicateKeys,
        labelDuplicateKey,
        labelKeyHasNewline,
        labelKeyHasSeparator,
        labelKeyRequired,
        labelValueHasNewline,
        separator,
      ],
    );

    const createPair = useCallback((): KeyValuePair => ({ key: '', value: '', enabled: true }), []);
    const isPairEmpty = useCallback(
      (pair: KeyValuePair) => pair.key === '' && pair.value === '',
      [],
    );

    const modeItems = useMemo(
      () => [
        { value: 'form', children: formModeLabel },
        { value: 'text', children: textModeLabel },
      ],
      [formModeLabel, textModeLabel],
    );

    const resolvedTextHint =
      textHint ?? `One entry per line, as key${separator}value.`;

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="key-value-editor"
        data-mode={mode}
        data-size={size}
        data-disabled={disabled || undefined}
        className={clsx('stratum-key-value-editor', className)}
      >
        <div className="stratum-key-value-editor__toolbar">
          <SegmentedControl
            size="sm"
            label={modeLabel}
            items={modeItems}
            value={mode}
            onValueChange={(next) => setMode(next as KeyValueEditorMode)}
            disabled={disabled}
          />
        </div>

        {mode === 'form' ? (
          <EditableList<KeyValuePair>
            items={pairs}
            onItemsChange={setPairs}
            createItem={createPair}
            isItemEmpty={isPairEmpty}
            validateItem={(pair, index, all) => validatePair(pair, index, all)?.message ?? null}
            minRows={minRows}
            {...(maxRows === undefined ? {} : { maxRows })}
            reorderable={reorderable}
            showRank={showRank}
            duplicable
            size={size}
            disabled={disabled}
            itemLabel={itemLabel}
            {...(addLabel === undefined ? {} : { addLabel })}
            {...(emptyState === undefined ? {} : { emptyState })}
            renderRow={(pair, api) => {
              const issue = validatePair(pair, api.index, pairs);
              const isOff = allowDisabledRows && pair.enabled === false;
              return (
                <div className="stratum-key-value-editor__row" data-off={isOff || undefined}>
                  {allowDisabledRows && (
                    <Checkbox
                      className="stratum-key-value-editor__toggle"
                      checked={pair.enabled !== false}
                      onCheckedChange={(checked) => api.update({ ...pair, enabled: checked })}
                      disabled={disabled}
                      aria-label={`${enabledLabel}, ${api.rowLabel}`}
                    />
                  )}
                  <Input
                    className="stratum-key-value-editor__key"
                    size={size === 'sm' ? 'sm' : 'md'}
                    value={pair.key}
                    onValueChange={(next) => api.update({ ...pair, key: next })}
                    placeholder={keyPlaceholder}
                    disabled={disabled}
                    aria-label={`${keyLabel}, ${api.rowLabel}`}
                    {...api.fieldProps}
                    aria-invalid={issue?.field === 'key' || undefined}
                  />
                  <Input
                    className="stratum-key-value-editor__value"
                    size={size === 'sm' ? 'sm' : 'md'}
                    value={pair.value}
                    onValueChange={(next) => api.update({ ...pair, value: next })}
                    placeholder={valuePlaceholder}
                    disabled={disabled}
                    aria-label={`${valueLabel}, ${api.rowLabel}`}
                    {...api.fieldProps}
                    aria-invalid={issue?.field === 'value' || undefined}
                  />
                </div>
              );
            }}
          />
        ) : (
          <div className="stratum-key-value-editor__text">
            <Textarea
              monospace
              fullWidth
              minRows={textRows}
              value={text}
              onValueChange={handleTextChange}
              disabled={disabled}
              invalid={lineIssues.length > 0}
              aria-label={textModeLabel}
              aria-describedby={
                lineIssues.length > 0 ? `${hintId} ${issuesId}` : hintId
              }
              {...(textPlaceholder === undefined ? {} : { placeholder: textPlaceholder })}
            />
            <VisuallyHidden id={hintId}>{resolvedTextHint}</VisuallyHidden>

            {lineIssues.length > 0 && (
              <div className="stratum-key-value-editor__issues" id={issuesId}>
                <InlineMessage variant="danger" size="xs" role="status">
                  {labelIssueSummary
                    ? labelIssueSummary(lineIssues.length)
                    : `${lineIssues.length} line${lineIssues.length === 1 ? '' : 's'} need attention.`}
                </InlineMessage>
                <ul className="stratum-key-value-editor__issue-list">
                  {lineIssues.map((issue) => (
                    <li key={`${issue.line}-${issue.message}`}>
                      {labelLineIssue
                        ? labelLineIssue(issue.line, issue.message)
                        : `Line ${issue.line}: ${issue.message}`}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </div>
    );
  },
);
