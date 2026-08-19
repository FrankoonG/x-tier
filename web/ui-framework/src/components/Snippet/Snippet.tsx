import { forwardRef, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import { ScrollArea } from '../ScrollArea/ScrollArea';
import { CopyButton } from '../CopyButton/CopyButton';
import './Snippet.css';

export type SnippetSize = 'sm' | 'md';

export interface SnippetProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'onCopy'> {
  /**
   * The text to display and to copy.
   *
   * A plain string rather than children, so the clipboard payload is exactly
   * what is on screen. Serialising children back to text is the usual source
   * of a copy button that quietly drops a newline or a highlight wrapper.
   */
  value: string;
  size?: SnippetSize;
  /** Wraps long lines instead of scrolling horizontally. */
  wrap?: boolean;
  /** Caps the block's height. A number is treated as pixels. */
  maxHeight?: number | string;
  copyable?: boolean;
  /** Accessible name of the copy control. Default `'Copy'`. */
  copyLabel?: string;
  /** Announced after a successful copy. Default `'Copied'`. */
  copiedLabel?: string;
  /** Announced after a failed copy. Default `'Copy failed'`. */
  copyErrorLabel?: string;
  onCopy?: (succeeded: boolean) => void;
  /**
   * Accessible name for the scroll region, used when the block overflows and
   * therefore becomes a tab stop. Default `'Code block'`.
   */
  scrollLabel?: string;
}

/**
 * A bordered block of monospace text with a copy control.
 *
 * The scrolling, the edge affordances and the keyboard reachability of the
 * overflowing content are all delegated to `ScrollArea`, so a snippet that
 * scrolls behaves identically to every other scroll region in the framework —
 * including becoming focusable only when there is genuinely something to
 * scroll.
 *
 * The copy control sits outside the scroll box, so it stays in place while the
 * content moves under it and its focus ring is never clipped by the overflow.
 */
export const Snippet = forwardRef<HTMLDivElement, SnippetProps>(function Snippet(
  {
    value,
    size = 'sm',
    wrap = false,
    maxHeight,
    copyable = true,
    copyLabel = 'Copy',
    copiedLabel = 'Copied',
    copyErrorLabel = 'Copy failed',
    onCopy,
    scrollLabel = 'Code block',
    className,
    ...rest
  },
  ref,
) {
  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="snippet"
      data-size={size}
      data-wrap={wrap || undefined}
      data-copyable={copyable || undefined}
      className={clsx('stratum-snippet', className)}
    >
      <ScrollArea
        orientation={wrap ? 'vertical' : 'both'}
        maxHeight={maxHeight}
        label={scrollLabel}
        className="stratum-snippet__scroll"
        viewportClassName="stratum-snippet__viewport"
      >
        <pre className="stratum-snippet__pre">
          <code className="stratum-snippet__code">{value}</code>
        </pre>
      </ScrollArea>

      {copyable && (
        // The button is placed by a wrapper rather than by positioning the
        // button itself. `Button` sets `position: relative` on its icon-only
        // xs size to host a transparent 24x24 pointer target, at a higher
        // specificity than a single utility class here could override — and
        // winning that fight by import order is exactly the kind of thing that
        // works in dev and breaks in a production bundle.
        <span className="stratum-snippet__copy">
          <CopyButton
            value={value}
            label={copyLabel}
            copiedLabel={copiedLabel}
            errorLabel={copyErrorLabel}
            {...(onCopy ? { onCopy } : {})}
            variant="default"
            size="xs"
          />
        </span>
      )}
    </div>
  );
});
