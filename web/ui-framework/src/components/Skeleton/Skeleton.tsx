import {
  forwardRef,
  useEffect,
  useState,
  type CSSProperties,
  type HTMLAttributes,
} from 'react';
import clsx from 'clsx';
import './Skeleton.css';

export type SkeletonVariant = 'text' | 'circle' | 'rect';

export type SkeletonAnimation = 'shimmer' | 'pulse' | 'none';

export type SkeletonRadius = 'none' | 'xs' | 'sm' | 'md' | 'lg' | 'xl' | 'full';

export interface SkeletonProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  variant?: SkeletonVariant;
  /** Any CSS length. Numbers are treated as pixels. */
  width?: number | string;
  height?: number | string;
  radius?: SkeletonRadius;
  animation?: SkeletonAnimation;
  /**
   * When supplied the placeholder becomes a polite `status` carrying this
   * text. Leave it off — the default — when a container already sets
   * `aria-busy`, so the same loading state is not announced once per block.
   */
  label?: string;
}

function toLength(value: number | string | undefined): string | undefined {
  if (value === undefined) return undefined;
  return typeof value === 'number' ? `${value}px` : value;
}

/**
 * Defers the live-region text by one frame.
 *
 * Assistive technology registers a live region when it enters the
 * accessibility tree, and a region that arrives already populated is not
 * announced by NVDA, JAWS or VoiceOver. A skeleton mounts at exactly the
 * moment loading starts, so a `label` rendered in the same commit as its
 * `role="status"` would never be heard. The region (and its empty span) is
 * therefore committed first and the text written a frame later — the same
 * shape `ToastProvider` uses for its permanently-mounted announcer.
 */
function useDeferredAnnouncement(label: string | undefined): string {
  const [announced, setAnnounced] = useState('');

  useEffect(() => {
    if (!label) {
      setAnnounced('');
      return;
    }
    const frame = requestAnimationFrame(() => setAnnounced(label));
    return () => cancelAnimationFrame(frame);
  }, [label]);

  return announced;
}

/**
 * A content placeholder.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - Default is `aria-hidden`. A screen reader user gains nothing from being
 *   told there are seven grey rectangles; the correct signal is `aria-busy`
 *   on the region that is loading, which the consumer owns. `label` exists
 *   for the case where the skeleton IS the only indication.
 * - Never carries the real text, even visually hidden. Fake content that
 *   later swaps for different real content is worse than no content.
 *
 * MOTION
 * ------
 * The shimmer is a loop, and loops are the one thing `prefers-reduced-motion`
 * is unambiguous about. Under that preference the sweep is removed entirely
 * and the block rests at its base tone — still obviously a placeholder,
 * because its shape already says so.
 */
export const Skeleton = forwardRef<HTMLDivElement, SkeletonProps>(function Skeleton(
  {
    variant = 'text',
    width,
    height,
    radius,
    animation = 'shimmer',
    label,
    className,
    style,
    ...rest
  },
  ref,
) {
  const announced = useDeferredAnnouncement(label);

  const resolvedRadius: SkeletonRadius =
    radius ?? (variant === 'circle' ? 'full' : variant === 'text' ? 'xs' : 'sm');

  const inline: CSSProperties = { ...style };
  const w = toLength(width);
  const h = toLength(height);
  if (w !== undefined) inline.width = w;
  if (h !== undefined) inline.height = h;
  // A circle with only one dimension given is still a circle.
  if (variant === 'circle') {
    if (w !== undefined && h === undefined) inline.height = w;
    if (h !== undefined && w === undefined) inline.width = h;
  }

  return (
    <div
      // Ahead of the spread: an explicit `undefined` written after `...rest`
      // deletes a consumer-supplied value, because in JSX the later key wins
      // even when it carries no value.
      role={label ? 'status' : undefined}
      aria-hidden={label ? undefined : true}
      {...rest}
      ref={ref}
      data-stratum="skeleton"
      data-variant={variant}
      data-animation={animation}
      data-radius={resolvedRadius}
      className={clsx('stratum-skeleton', className)}
      style={inline}
    >
      {label && <span className="stratum-visually-hidden">{announced}</span>}
    </div>
  );
});

export interface SkeletonTextProps extends Omit<SkeletonProps, 'variant' | 'height'> {
  /** Number of lines to draw. Default 3. */
  lines?: number;
  /**
   * Width of the final line, so the block reads as a paragraph rather than a
   * table. Default `'62%'`. Pass `'100%'` for a justified look.
   */
  lastLineWidth?: number | string;
  /** Space between lines. Any CSS length. */
  gap?: number | string;
  /** Height of each line. Defaults to the body line box. */
  lineHeight?: number | string;
}

/**
 * A multi-line text placeholder.
 *
 * The last line is deliberately short: a stack of equal-width bars reads as
 * tabular data, and users then expect a table when the real content lands.
 */
export const SkeletonText = forwardRef<HTMLDivElement, SkeletonTextProps>(
  function SkeletonText(
    {
      lines = 3,
      lastLineWidth = '62%',
      gap,
      lineHeight,
      width,
      radius,
      animation = 'shimmer',
      label,
      className,
      style,
      ...rest
    },
    ref,
  ) {
    const announced = useDeferredAnnouncement(label);

    const count = Math.max(1, Math.floor(lines));
    const w = toLength(width);
    const g = toLength(gap);
    const lh = toLength(lineHeight);
    // Custom properties are written with the literal-key idiom the rest of the
    // family uses (Progress, Meter, Toast) rather than by asserting the object
    // to `Record<string, string>`, which would silently disable type checking
    // on every other write to it.
    const inline: CSSProperties = {
      ...style,
      ...(w !== undefined ? { width: w } : null),
      ...(g !== undefined ? { ['--_gap' as string]: g } : null),
      ...(lh !== undefined ? { ['--_line' as string]: lh } : null),
    };

    return (
      <div
        // See the note in `Skeleton`: framework defaults go before the spread
        // so a consumer's value is never deleted by an explicit `undefined`.
        role={label ? 'status' : undefined}
        aria-hidden={label ? undefined : true}
        {...rest}
        ref={ref}
        data-stratum="skeleton-text"
        className={clsx('stratum-skeleton-text', className)}
        style={inline}
      >
        {label && <span className="stratum-visually-hidden">{announced}</span>}
        {Array.from({ length: count }, (_unused, index) => (
          <Skeleton
            key={index}
            variant="text"
            animation={animation}
            {...(radius ? { radius } : null)}
            className="stratum-skeleton-text__line"
            {...(index === count - 1 && count > 1 ? { width: lastLineWidth } : null)}
          />
        ))}
      </div>
    );
  },
);
