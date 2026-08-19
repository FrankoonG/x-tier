/* ---------------------------------------------------------------------------
 * Geometry shared by the anchored overlays (Popover, Tooltip).
 *
 * The entrance animation for an anchored panel is a scale, and a scale only
 * reads as "this grew out of the thing I clicked" if `transform-origin` sits on
 * the edge nearest the anchor — and, when there is an arrow, on the arrow's
 * tip rather than the middle of that edge. Both are derived from the placement
 * Floating UI actually resolved (after flip/shift), never from the requested
 * one, so a panel that flipped above its trigger still grows downward-to-up.
 * ------------------------------------------------------------------------- */

/**
 * Edge length of the arrow's square box, in px, before the 45° rotation.
 * Mirrors `--_arrow-size` in Popover.css (`--stratum-space-5`). It has to
 * exist in JS as well because the arrow's centre is needed to place
 * `transform-origin`.
 *
 * Tooltip's arrow is deliberately smaller and passes its own size to the two
 * functions below rather than sharing this one.
 */
export const ARROW_SIZE = 10;

/**
 * How far a rotated square of edge length `size` sticks out past the panel
 * edge: half of its diagonal, `size * √2 / 2`. Added to the caller's `offset`
 * so the arrow tip — not the panel edge — ends up the requested distance from
 * the anchor.
 */
export function arrowProtrusion(size: number): number {
  return Math.round(size * Math.SQRT1_2);
}

/** `arrowProtrusion(ARROW_SIZE)`: `10 * √2 / 2 ≈ 7`. */
export const ARROW_PROTRUSION = arrowProtrusion(ARROW_SIZE);

/** Padding kept between the arrow and the panel's rounded corners. */
export const ARROW_PADDING = 8;

export type OverlaySide = 'top' | 'right' | 'bottom' | 'left';
export type OverlayAlign = 'start' | 'center' | 'end';

export interface ResolvedPlacement {
  side: OverlaySide;
  align: OverlayAlign;
}

/** Splits a Floating UI placement into its two independent axes. */
export function splitPlacement(placement: string): ResolvedPlacement {
  const parts = placement.split('-');
  const side = parts[0];
  const align = parts[1];
  return {
    side:
      side === 'top' || side === 'right' || side === 'bottom' || side === 'left' ? side : 'bottom',
    align: align === 'start' || align === 'end' ? align : 'center',
  };
}

/**
 * `transform-origin` for a panel anchored on `side`/`align`.
 *
 * When the arrow middleware ran, `arrowX`/`arrowY` locate the arrow box inside
 * the panel and the origin is pinned to its centre. Without an arrow the origin
 * falls back to the aligned corner or midpoint of the anchored edge.
 *
 * `arrowSize` must be the edge length the component actually renders, or the
 * origin lands off the arrow tip by half the difference.
 */
export function resolveTransformOrigin(
  placement: ResolvedPlacement,
  arrowX: number | undefined,
  arrowY: number | undefined,
  arrowSize: number = ARROW_SIZE,
): string {
  const { side, align } = placement;
  const fallback = align === 'start' ? '0%' : align === 'end' ? '100%' : '50%';
  const alongX = arrowX == null ? fallback : `${arrowX + arrowSize / 2}px`;
  const alongY = arrowY == null ? fallback : `${arrowY + arrowSize / 2}px`;

  switch (side) {
    case 'top':
      return `${alongX} 100%`;
    case 'bottom':
      return `${alongX} 0%`;
    case 'left':
      return `100% ${alongY}`;
    case 'right':
      return `0% ${alongY}`;
  }
}
