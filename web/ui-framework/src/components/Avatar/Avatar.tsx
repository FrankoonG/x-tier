import {
  Children,
  createContext,
  forwardRef,
  useContext,
  useMemo,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import './Avatar.css';

export type AvatarSize = 'xs' | 'sm' | 'md' | 'lg' | 'xl';
export type AvatarShape = 'circle' | 'square';

interface AvatarGroupContextValue {
  size: AvatarSize | undefined;
  shape: AvatarShape | undefined;
}

const AvatarGroupContext = createContext<AvatarGroupContextValue | null>(null);

export interface AvatarProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  src?: string;
  /** Image alternative text. Falls back to `name`. */
  alt?: string;
  /** Full display name. Supplies both the initials and the accessible name. */
  name?: string;
  /** Overrides the derived initials, e.g. for a two-character system code. */
  initials?: string;
  /** Shown when there is neither an image nor a name — typically a person icon. */
  fallback?: ReactNode;
  size?: AvatarSize;
  shape?: AvatarShape;
}

/**
 * Derives up to two initials from a display name.
 *
 * Iterates code points rather than UTF-16 units, so a name beginning with an
 * astral-plane character (an emoji, or many CJK extension characters) yields
 * that whole character instead of a lone surrogate half, which renders as a
 * replacement glyph.
 */
export function getInitials(name: string, max = 2): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '';

  if (parts.length === 1) {
    const only = parts[0] ?? '';
    return Array.from(only).slice(0, max).join('').toLocaleUpperCase();
  }

  const first = Array.from(parts[0] ?? '')[0] ?? '';
  const last = Array.from(parts[parts.length - 1] ?? '')[0] ?? '';
  // Sliced as code points, not as UTF-16 units: `'🎩h'.slice(0, 2)` keeps only
  // the emoji, because it alone occupies both units.
  return [first, last].slice(0, max).join('').toLocaleUpperCase();
}

/**
 * A compact identity chip: image with an initials fallback.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - With an image, the `<img>` carries the name through `alt`, which is what
 *   image-description tooling expects to find.
 * - Without one, the root becomes `role="img"` with `aria-label`, and the
 *   initials themselves are hidden. Letting a reader announce the raw initials
 *   produces "ay bee" rather than the person's name.
 * - With neither an image nor a name the avatar is purely decorative and is
 *   removed from the accessibility tree entirely, rather than announcing an
 *   unlabelled image.
 * - A broken image URL falls back to initials rather than to a broken-image
 *   glyph, and the fallback resets whenever `src` changes so a retry is
 *   possible.
 */
export const Avatar = forwardRef<HTMLSpanElement, AvatarProps>(function Avatar(
  { src, alt, name, initials, fallback, size, shape, className, ...rest },
  ref,
) {
  const group = useContext(AvatarGroupContext);
  const resolvedSize = size ?? group?.size ?? 'md';
  const resolvedShape = shape ?? group?.shape ?? 'circle';

  // Reset during render rather than in an effect. A passive effect runs after
  // paint, so a `src` that changes while the previous one is marked failed
  // would commit one frame showing the initials fallback for an image that has
  // not even been attempted yet. React's documented prev-prop comparison
  // discards the render in progress and re-runs it before anything is painted.
  const [failed, setFailed] = useState(false);
  const [lastSrc, setLastSrc] = useState(src);
  if (src !== lastSrc) {
    setLastSrc(src);
    setFailed(false);
  }

  // A consumer-supplied `aria-label` is as good a name as `alt`/`name`, so it
  // also keeps the image out of the decorative branch below. `aria-labelledby`
  // cannot be read here, but it still names the avatar, so it must not be
  // force-hidden by the decorative branch either.
  const label = alt ?? name ?? rest['aria-label'];
  const named = Boolean(label) || Boolean(rest['aria-labelledby']);
  const showImage = Boolean(src) && !failed;
  const text = initials ?? (name ? getInitials(name) : '');
  const hasText = text.length > 0;

  return (
    <span
      // Placed before the spread so a consumer-supplied role/label always wins.
      // After it, an explicit `undefined` would delete the consumer's value.
      role={!showImage && named ? 'img' : undefined}
      aria-label={!showImage && label ? label : undefined}
      aria-hidden={!showImage && !named ? true : undefined}
      {...rest}
      ref={ref}
      data-stratum="avatar"
      data-size={resolvedSize}
      data-shape={resolvedShape}
      className={clsx('stratum-avatar', className)}
    >
      {showImage ? (
        <img
          className="stratum-avatar__image"
          src={src}
          alt={label ?? ''}
          onError={() => setFailed(true)}
          draggable={false}
        />
      ) : hasText ? (
        <span className="stratum-avatar__initials" aria-hidden="true">
          {text}
        </span>
      ) : (
        <span className="stratum-avatar__fallback" aria-hidden="true">
          {fallback ?? <DefaultFallbackIcon />}
        </span>
      )}
    </span>
  );
});

function DefaultFallbackIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" focusable="false">
      <circle cx="12" cy="9" r="3.4" stroke="currentColor" strokeWidth="1.7" />
      <path
        d="M4.8 19.6a7.4 7.4 0 0 1 14.4 0"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
      />
    </svg>
  );
}

export interface AvatarGroupProps extends HTMLAttributes<HTMLDivElement> {
  /** Maximum avatars to render before collapsing the rest into a counter. */
  max?: number;
  /**
   * True population size, when it is larger than the children supplied. Lets a
   * server-paged list show "+248" without rendering 248 nodes.
   */
  total?: number;
  size?: AvatarSize;
  shape?: AvatarShape;
  /** Visible text of the overflow chip. Default `(n) => '+' + n`. */
  formatOverflow?: (count: number) => ReactNode;
  /** Accessible name of the overflow chip. Default `(n) => n + ' more'`. */
  overflowLabel?: (count: number) => string;
}

const defaultFormatOverflow = (count: number): ReactNode => `+${count}`;
const defaultOverflowLabel = (count: number): string => `${count} more`;

/**
 * Overlapping row of avatars with a "+N" overflow chip.
 *
 * Size and shape are distributed through context rather than by cloning the
 * children, so a consumer can wrap an `Avatar` in their own component — a
 * tooltip trigger, a link — and it still inherits the group's scale. Cloning
 * would only reach direct children and silently stop working the moment
 * anything is wrapped.
 */
export const AvatarGroup = forwardRef<HTMLDivElement, AvatarGroupProps>(function AvatarGroup(
  {
    max,
    total,
    size,
    shape,
    formatOverflow = defaultFormatOverflow,
    overflowLabel = defaultOverflowLabel,
    className,
    children,
    ...rest
  },
  ref,
) {
  const items = Children.toArray(children);
  const shown = typeof max === 'number' && max >= 0 ? items.slice(0, max) : items;
  const overflow = Math.max(0, (total ?? items.length) - shown.length);

  const context = useMemo<AvatarGroupContextValue>(() => ({ size, shape }), [size, shape]);

  return (
    <AvatarGroupContext.Provider value={context}>
      <div
        {...rest}
        ref={ref}
        data-stratum="avatar-group"
        data-size={size}
        className={clsx('stratum-avatar-group', className)}
      >
        {shown}
        {overflow > 0 && (
          <span
            data-stratum="avatar"
            data-size={size ?? 'md'}
            data-shape={shape ?? 'circle'}
            data-overflow="true"
            className="stratum-avatar stratum-avatar--overflow"
            role="img"
            aria-label={overflowLabel(overflow)}
          >
            <span className="stratum-avatar__initials" aria-hidden="true">
              {formatOverflow(overflow)}
            </span>
          </span>
        )}
      </div>
    </AvatarGroupContext.Provider>
  );
});
