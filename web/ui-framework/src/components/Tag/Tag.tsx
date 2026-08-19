import {
  forwardRef,
  type HTMLAttributes,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import type { BadgeVariant } from '../Badge/Badge';
// Tag reuses Badge's colour contract and inner part classes verbatim, so the
// two can never drift. The import is explicit rather than implied by the
// re-used class names, so a consumer importing only `Tag` still gets them.
import '../Badge/Badge.css';
import './Tag.css';

export type TagVariant = BadgeVariant;
export type TagSize = 'sm' | 'md';

export interface TagProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'onSelect'> {
  variant?: TagVariant;
  size?: TagSize;
  /** Border and text only, no fill. */
  outline?: boolean;
  /** Fully rounded ends. */
  pill?: boolean;
  /** Draws a filled dot before the label, matching the variant's colour. */
  dot?: boolean;
  /** Leading adornment. Hidden from assistive tech. */
  icon?: ReactNode;

  /** Adds a remove control. Also enables Backspace/Delete on a selectable tag. */
  onRemove?: () => void;
  /**
   * Accessible name for the remove control. Include the tag's own text —
   * a page of buttons all called "Remove" is unusable from a list of
   * controls. Default `'Remove'`.
   */
  removeLabel?: string;
  /** Replaces the default cross glyph. */
  removeIcon?: ReactNode;

  /** Turns the tag body into a toggle button. */
  selectable?: boolean;
  /** Controlled selected state. */
  selected?: boolean;
  /** Uncontrolled initial selected state. */
  defaultSelected?: boolean;
  onSelectedChange?: (selected: boolean) => void;

  disabled?: boolean;
}

const DefaultRemoveIcon = (
  <svg viewBox="0 0 12 12" fill="none" focusable="false" aria-hidden="true">
    <path
      d="M3.25 3.25 8.75 8.75M8.75 3.25 3.25 8.75"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
    />
  </svg>
);

/**
 * A `Badge` that can be removed and/or toggled.
 *
 * STRUCTURE
 * ---------
 * The root is never the interactive element. When the tag is both selectable
 * and removable it needs two independent controls, and a button inside a
 * button is invalid HTML that browsers silently un-nest — so the label is one
 * button, the remove control is its sibling, and the root stays a plain span
 * that only carries styling state.
 *
 * KEYBOARD
 * --------
 * - The label toggles with Enter/Space (native button behaviour).
 * - Backspace and Delete on the focused label remove the tag, which is what
 *   users of filter-chip inputs reach for first.
 * - The remove button is a real button, so it is reachable and operable on its
 *   own; the removal is never keyboard-only or pointer-only.
 */
export const Tag = forwardRef<HTMLSpanElement, TagProps>(function Tag(
  {
    variant = 'neutral',
    size = 'sm',
    outline = false,
    pill = false,
    dot = false,
    icon,
    onRemove,
    removeLabel = 'Remove',
    removeIcon,
    selectable = false,
    selected,
    defaultSelected = false,
    onSelectedChange,
    disabled = false,
    className,
    children,
    ...rest
  },
  ref,
) {
  const [isSelected, setSelected] = useControllableState<boolean>({
    value: selected,
    defaultValue: defaultSelected,
    onChange: onSelectedChange,
  });

  const removable = typeof onRemove === 'function';

  const handleRemove = (event: MouseEvent<HTMLButtonElement>) => {
    // The tag frequently sits inside a clickable row; removing it must not
    // also select that row.
    event.stopPropagation();
    if (disabled) return;
    onRemove?.();
  };

  const handleBodyKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (!removable || disabled) return;
    if (event.key !== 'Backspace' && event.key !== 'Delete') return;
    event.preventDefault();
    onRemove?.();
  };

  const content = (
    <>
      {dot && <span className="stratum-badge__dot" aria-hidden="true" />}
      {icon && (
        <span className="stratum-badge__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      {children != null && children !== false && (
        <span className="stratum-badge__label">{children}</span>
      )}
    </>
  );

  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="tag"
      data-variant={variant}
      data-size={size}
      data-outline={outline || undefined}
      data-pill={pill || undefined}
      data-selectable={selectable || undefined}
      data-selected={(selectable && isSelected) || undefined}
      data-removable={removable || undefined}
      data-disabled={disabled || undefined}
      className={clsx('stratum-badge', 'stratum-tag', className)}
    >
      {selectable ? (
        <button
          type="button"
          className="stratum-tag__body"
          aria-pressed={isSelected}
          disabled={disabled}
          onClick={() => setSelected(!isSelected)}
          onKeyDown={handleBodyKeyDown}
        >
          {content}
        </button>
      ) : (
        <span className="stratum-tag__body">{content}</span>
      )}

      {removable && (
        <button
          type="button"
          className="stratum-tag__remove"
          aria-label={removeLabel}
          disabled={disabled}
          onClick={handleRemove}
        >
          {removeIcon ?? DefaultRemoveIcon}
        </button>
      )}
    </span>
  );
});
