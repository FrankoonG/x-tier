import { Fragment, forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Kbd.css';

export type KbdSize = 'sm' | 'md';

export interface KbdProps extends HTMLAttributes<HTMLElement> {
  /**
   * Explicit key list. Takes precedence over `children` and is the way to
   * render glyphs the separator would otherwise split, e.g. `['Ctrl', '+']`.
   */
  keys?: string[];
  /** Character that joins the keys in a `children` string. Default `'+'`. */
  separator?: string;
  size?: KbdSize;
  /** Renders the separator between caps. Set `false` for a tight cluster. */
  showSeparator?: boolean;
}

/**
 * Keyboard key display. Accepts a combo string and splits it into caps.
 *
 * The separator is left in the accessibility tree on purpose. "Ctrl + K" is
 * announced as "Ctrl plus K", which is exactly how the shortcut is spoken;
 * hiding it would produce "Ctrl K" and lose the fact that the keys are
 * chorded rather than pressed in sequence.
 *
 * No platform glyph substitution happens here — mapping `Ctrl` to `⌘` on
 * macOS is an application decision that depends on which shortcut is actually
 * registered, and getting it wrong in a component is worse than not doing it.
 * Pass `keys={['⌘', 'K']}` when the app knows.
 */
export const Kbd = forwardRef<HTMLElement, KbdProps>(function Kbd(
  { keys, separator = '+', size = 'sm', showSeparator = true, className, children, ...rest },
  ref,
) {
  const parts = resolveKeys(keys, children, separator);

  return (
    <kbd
      {...rest}
      ref={ref}
      data-stratum="kbd"
      data-size={size}
      className={clsx('stratum-kbd', className)}
    >
      {parts.map((part, index) => (
        <Fragment key={`${part}-${index}`}>
          {index > 0 && showSeparator && (
            <span className="stratum-kbd__separator">{separator}</span>
          )}
          <kbd className="stratum-kbd__key">{part}</kbd>
        </Fragment>
      ))}
    </kbd>
  );
});

function resolveKeys(
  keys: string[] | undefined,
  children: ReactNode,
  separator: string,
): string[] {
  if (keys && keys.length > 0) return keys;

  if (typeof children === 'string') {
    // A combo that is only separators — "+" on its own, or "++" — must not
    // vanish. Falling back to the raw string keeps it renderable.
    const split = children
      .split(separator)
      .map((part) => part.trim())
      .filter((part) => part.length > 0);
    return split.length > 0 ? split : [children];
  }

  if (typeof children === 'number') return [String(children)];
  return [];
}
