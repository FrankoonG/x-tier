import {
  forwardRef,
  useCallback,
  useMemo,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useCopyAction } from '../_shared/useCopyAction';
import { CheckIcon, CopyIcon, CrossIcon, WrapIcon } from '../_shared/icons';
import './CodeBlock.css';

/**
 * Lines to mark. A number, a list of numbers, or a compact range string such as
 * `'3,7-9,14'`. All are 1-based and relative to `startLineNumber`.
 */
export type HighlightLines = number | readonly number[] | string;

// `title` is widened from the DOM string to a ReactNode: the header title is
// content, not a tooltip, and a caller wanting the native attribute has
// `aria-label` and the far better `<Tooltip>` for that job.
export interface CodeBlockProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onCopy' | 'children' | 'title'> {
  code: string;

  /**
   * Purely informational: shown in the header and published as
   * `data-language`. This component performs NO syntax highlighting — see
   * `tokens`.
   */
  language?: string;
  title?: ReactNode;

  showLineNumbers?: boolean;
  /** Number shown against the first line. Default 1. */
  startLineNumber?: number;
  highlightLines?: HighlightLines;

  wrap?: boolean;
  defaultWrap?: boolean;
  onWrapChange?: (wrap: boolean) => void;

  /** Caps the height and scrolls past it. Default `'20rem'`. */
  maxHeight?: number | string;
  header?: boolean;
  copyable?: boolean;
  /** Renders the wrap toggle. Default true when a header is shown. */
  wrappable?: boolean;

  /**
   * Optional per-line renderer, for consumers who bring their own highlighter.
   *
   * Called once per line with the line's text and its displayed number; return
   * whatever nodes you like. Returning `undefined` falls back to plain text for
   * that line. The library ships no highlighter and no highlighting dependency:
   * a grammar bundle costs more than the rest of this package put together, and
   * which one you want is not ours to decide.
   */
  tokens?: (line: string, lineNumber: number) => ReactNode;

  onCopy?: (text: string) => void;

  /* -- Copy --------------------------------------------------------------- */
  label?: string;
  labelCopy?: string;
  labelCopied?: string;
  labelCopyFailed?: string;
  labelWrap?: string;
  /** Announced on a marked line; never rendered visually. */
  labelHighlighted?: string;
}

/** Parses `highlightLines` into a set of 1-based numbers. */
function toLineSet(input: HighlightLines | undefined): Set<number> {
  const set = new Set<number>();
  if (input === undefined) return set;

  if (typeof input === 'number') {
    if (Number.isFinite(input)) set.add(input);
    return set;
  }

  if (Array.isArray(input)) {
    for (const value of input as readonly number[]) {
      if (Number.isFinite(value)) set.add(value);
    }
    return set;
  }

  if (typeof input === 'string') {
    for (const part of input.split(',')) {
      const chunk = part.trim();
      if (!chunk) continue;
      const dash = chunk.indexOf('-', 1);
      if (dash > 0) {
        const from = Number.parseInt(chunk.slice(0, dash), 10);
        const to = Number.parseInt(chunk.slice(dash + 1), 10);
        if (Number.isFinite(from) && Number.isFinite(to)) {
          // Tolerates a reversed range rather than silently marking nothing.
          const lo = Math.min(from, to);
          const hi = Math.max(from, to);
          // Bounded so `1-999999999` cannot allocate its way out of the tab.
          for (let n = lo; n <= hi && set.size < 10_000; n += 1) set.add(n);
        }
        continue;
      }
      const single = Number.parseInt(chunk, 10);
      if (Number.isFinite(single)) set.add(single);
    }
  }

  return set;
}

/**
 * A monospace block with line numbers, line marking, wrapping and copy.
 *
 * NO SYNTAX HIGHLIGHTING, ON PURPOSE
 * ----------------------------------
 * Shipping a grammar bundle would multiply this package's size for a feature
 * most operator panels do not need — a config excerpt, a stack trace, a curl
 * command. Consumers who do need it pass `tokens`, which is called per line and
 * can return anything. That keeps the highlighter, its language set and its
 * loading strategy in the application, where the trade-off belongs.
 *
 * COPY FIDELITY
 * -------------
 * `copy` yields the original `code` string verbatim. The line-number gutter is
 * a separate element marked `user-select: none`, so even a manual selection
 * across the block copies the code and not the numbering — pasting `1  server {`
 * into a config file is a small, extremely common, entirely avoidable
 * annoyance.
 *
 * ACCESSIBILITY
 * -------------
 * The scroll container is a labelled `role="group"` with `tabIndex={0}`: a
 * scrollable area that cannot be reached or scrolled by keyboard is a WCAG 2.1
 * SC 2.1.1 failure, and one that is reachable but unnamed leaves a screen
 * reader user with an unlabelled stop.
 *
 * `group`, not `region`. `region` is a LANDMARK, and a page documenting an API
 * with six snippets on it then publishes six landmarks all named "Code" —
 * axe flags it as `landmark-unique`, and a screen reader's landmark list
 * becomes useless noise. A code block is not a structural division of the page;
 * it is a named, focusable box. `group` says exactly that and keeps the name.
 * Pass `label` per block where the distinction matters.
 *
 * Marked lines carry a hidden textual marker as well as their tint, because the
 * tint alone says nothing to anyone who cannot see it.
 */
export const CodeBlock = forwardRef<HTMLDivElement, CodeBlockProps>(function CodeBlock(
  {
    code,
    language,
    title,
    showLineNumbers = false,
    startLineNumber = 1,
    highlightLines,
    wrap,
    defaultWrap = false,
    onWrapChange,
    maxHeight = '20rem',
    header,
    copyable = true,
    wrappable = true,
    tokens,
    onCopy,
    label = 'Code',
    labelCopy = 'Copy',
    labelCopied = 'Copied',
    labelCopyFailed = 'Copy failed',
    labelWrap = 'Wrap lines',
    labelHighlighted = 'highlighted',
    className,
    style,
    ...rest
  },
  ref,
) {
  const [wrapValue, setWrapValue] = useControllableState<boolean>({
    value: wrap,
    defaultValue: defaultWrap,
    onChange: onWrapChange,
  });

  const copy = useCopyAction();

  const lines = useMemo(() => {
    const normalised = code.replace(/\r\n?/g, '\n');
    const split = normalised.split('\n');
    // A single trailing newline terminates the last line; it does not add one.
    if (split.length > 1 && split[split.length - 1] === '') split.pop();
    return split;
  }, [code]);

  const highlighted = useMemo(() => toLineSet(highlightLines), [highlightLines]);

  const handleCopy = useCallback(() => {
    copy.copy(code);
    onCopy?.(code);
  }, [copy, code, onCopy]);

  const showHeader = header ?? (Boolean(title) || Boolean(language) || copyable || wrappable);

  const preStyle = {
    maxHeight: typeof maxHeight === 'number' ? `${maxHeight}px` : maxHeight,
  } as CSSProperties;

  const copyIcon =
    copy.status === 'copied' ? <CheckIcon /> : copy.status === 'error' ? <CrossIcon /> : <CopyIcon />;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="code-block"
      data-language={language}
      data-wrap={wrapValue || undefined}
      data-line-numbers={showLineNumbers || undefined}
      className={clsx('stratum-code', className)}
      style={style}
    >
      {showHeader && (
        <div className="stratum-code__header">
          {title != null && <span className="stratum-code__title">{title}</span>}
          {language && (
            <span className="stratum-code__language" aria-hidden="true">
              {language}
            </span>
          )}
          <div className="stratum-code__actions">
            {wrappable && (
              <button
                type="button"
                className="stratum-code__action"
                aria-pressed={wrapValue}
                aria-label={labelWrap}
                title={labelWrap}
                onClick={() => setWrapValue(!wrapValue)}
              >
                <WrapIcon />
              </button>
            )}
            {copyable && (
              <button
                type="button"
                className="stratum-code__action"
                data-copy-state={copy.status}
                aria-label={
                  copy.status === 'copied'
                    ? labelCopied
                    : copy.status === 'error'
                      ? labelCopyFailed
                      : labelCopy
                }
                title={labelCopy}
                onClick={handleCopy}
              >
                {copyIcon}
              </button>
            )}
          </div>
        </div>
      )}

      <pre
        className="stratum-code__pre stratum-focus-inset"
        style={preStyle}
        tabIndex={0}
        role="group"
        aria-label={label}
      >
        <code className="stratum-code__code">
          {lines.map((line, index) => {
            const number = startLineNumber + index;
            const isMarked = highlighted.has(number);
            const rendered = tokens ? tokens(line, number) : undefined;

            return (
              <span
                key={index}
                className="stratum-code__line"
                data-highlight={isMarked || undefined}
              >
                {showLineNumbers && (
                  <span className="stratum-code__num" aria-hidden="true">
                    {number}
                  </span>
                )}
                <span className="stratum-code__text">
                  {isMarked && (
                    <span className="stratum-visually-hidden">{labelHighlighted} </span>
                  )}
                  {rendered === undefined ? line : rendered}
                </span>
              </span>
            );
          })}
        </code>
      </pre>
    </div>
  );
});
