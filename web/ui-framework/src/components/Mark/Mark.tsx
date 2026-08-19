/* ===========================================================================
 * THE MARK
 *
 * X-Tier's logo, and the framework's. They are the same mark: this library
 * exists for one product, so a shared brand asset is not a coupling to
 * apologise for.
 *
 * It is a component rather than a static asset because it has to do four jobs
 * that a flat SVG cannot: adapt its colour to whatever it sits on, redraw
 * itself heavier at small sizes, animate as a loading indicator, and display a
 * real progress value. `Spinner` is built on it, so every busy state in the
 * library is this shape.
 * ======================================================================== */
import type { CSSProperties } from 'react';
import clsx from 'clsx';
import './Mark.css';

/* ---------------------------------------------------------------------------
 * GEOMETRY
 *
 * A TRUE SQUARE, standing on a corner. The original was not: the nodes sat at
 * a half-height of 8.4 and a half-width of 7.6, so the figure was 16.8 tall
 * against 15.2 wide. Nobody spotted it in the static mark — a 10% difference
 * reads as a diamond either way — but `swarm` exposed it at once, because
 * rotating an almost-square constellation makes it drift in and out of
 * alignment with its own outline. That wobble got written up as a feature. It
 * was a defect, and it is fixed here rather than dressed up: one radius, four
 * nodes on the axes, 90 degrees apart, equidistant from the centre.
 * ------------------------------------------------------------------------ */
const C = 12;
const R = 8.2;

const TOP = { x: C, y: C - R };
const LEFT = { x: C - R, y: C };
const RIGHT = { x: C + R, y: C };
const BOTTOM = { x: C, y: C + R };

/** The chosen path. Drawn twice — filled, then stroked — so the two must never
 *  drift apart, which is why it is declared once. */
const ROUTE = `M${TOP.x} ${TOP.y} ${LEFT.x} ${LEFT.y} ${BOTTOM.x} ${BOTTOM.y}`;

/**
 * The four edges as separate two-point subpaths — two points enclose no area,
 * so this can never pick up a fill however the cascade changes underneath.
 *
 * The two edges meeting the resting node stop at its rim instead of running
 * under it. They used to pass beneath a 45%-opaque disc and show straight
 * through it, which looked like a mistake because it was one. Trimming is the
 * fix that holds in every tone: the disc can then be fully opaque where a solid
 * colour is available, and still looks clean where only `currentColor` is.
 */
function diamondPath(restRadius: number): string {
  /* Both edges approach RIGHT at 45 degrees, so backing off along an edge by
   * one radius is a step of r/sqrt(2) on each axis. */
  const d = restRadius * Math.SQRT1_2;
  const stopTop = { x: RIGHT.x - d, y: RIGHT.y - d };
  const stopBottom = { x: RIGHT.x - d, y: RIGHT.y + d };
  return [
    `M${TOP.x} ${TOP.y} ${LEFT.x} ${LEFT.y}`,
    `M${LEFT.x} ${LEFT.y} ${BOTTOM.x} ${BOTTOM.y}`,
    `M${TOP.x} ${TOP.y} ${stopTop.x.toFixed(3)} ${stopTop.y.toFixed(3)}`,
    `M${stopBottom.x.toFixed(3)} ${stopBottom.y.toFixed(3)} ${BOTTOM.x} ${BOTTOM.y}`,
  ].join('');
}

/* ---------------------------------------------------------------------------
 * OPTICAL SIZING
 *
 * Scaling the drawing linearly is what a viewBox does for free, and it is
 * wrong. Weights that read at 64px are hairlines at 16: the quiet edges are
 * 1.5 units, which lands at 1.0 device pixels once 24 units map onto 16, so
 * the outline all but disappears and the mark collapses to four dots and a
 * blob. Real icon families and variable fonts solve this on an optical-size
 * axis — the small cut is drawn heavier, it is not the large cut shrunk.
 *
 * Compensation is partial and one-directional: `(24 / size) ^ n` is 1 at and
 * above the 24-unit reference, so nothing changes where nothing was wrong, and
 * grows below it. Capped so an absurdly small size cannot blow the geometry
 * apart.
 *
 * STROKES AND DOTS GET DIFFERENT EXPONENTS
 * ----------------------------------------
 * A single exponent is wrong in both directions at once. Strong enough to save
 * the edges, it inflates the nodes until at 12px they nearly touch and the
 * diamond closes into a clover — the mark loses its shape at exactly the size
 * the compensation exists to protect. The two are not failing equally: at 16px
 * the edge renders 1.0 device pixels and is genuinely disappearing, while a
 * node is 1.47px in radius, nearly 3px across, and is merely small.
 *
 * Strokes now get FULL compensation (exponent 1) — they hold a constant device
 * width below the reference rather than merely losing weight more slowly.
 * Radii get a middling exponent, enough to feel deliberately heavier without
 * eating the gaps.
 *
 * OPACITY IS THE THIRD AXIS, AND IT WAS THE REAL CULPRIT
 * -----------------------------------------------------
 * Widening alone read as "barely any change", and it should have: the quiet
 * edges are drawn at 0.34 opacity, so at 16px the compensation was buying a
 * 1.38px line that was still 66% transparent. Against a white surface that is
 * an effective contrast of a few percent — the width was never what made it
 * invisible. Small cuts therefore also come UP in opacity, to 0.51 at 16px and
 * 0.59 at 12px. Weight, size and tone all move together, which is what makes
 * the small cut read as a different drawing rather than a nudged one.
 * ------------------------------------------------------------------------ */
const REFERENCE_SIZE = 24;
const STROKE_EXPONENT = 1;
const RADIUS_EXPONENT = 0.55;
const OPTICAL_MAX = 2.4;

/** Base opacity of the quiet edges at the reference size and below. */
const EDGE_OPACITY = 0.34;
const EDGE_OPACITY_GAIN = 0.5;
const EDGE_OPACITY_MAX = 0.62;

function opticalScale(size: number, exponent: number): number {
  if (size >= REFERENCE_SIZE) return 1;
  return Math.min(OPTICAL_MAX, (REFERENCE_SIZE / size) ** exponent);
}

function edgeOpacity(size: number): number {
  if (size >= REFERENCE_SIZE) return EDGE_OPACITY;
  const shortfall = 1 - size / REFERENCE_SIZE;
  return Math.min(EDGE_OPACITY_MAX, EDGE_OPACITY + shortfall * EDGE_OPACITY_GAIN);
}

export type MarkTone = 'brand' | 'inherit';
export type MarkLoading = 'spiral' | 'swarm' | 'progress';

export interface MarkProps {
  size?: number;
  /**
   * `brand` — the mark owns its colour (indigo). For a neutral surface where
   * the mark is being the brand: topbar, favicon, app icon, splash, docs.
   * `inherit` — the mark takes `currentColor`. For coloured or monochrome
   * chrome: on an accent-filled button, in print, under forced-colors, or
   * inline in a run of text.
   */
  tone?: MarkTone;
  /** Animates the mark. Omit for the static logo. */
  loading?: MarkLoading;
  /** 0..1, only meaningful with `loading="progress"`. */
  progress?: number;
  /** Set false to defeat optical sizing and scale the drawing linearly. Exists
   *  so the lab can show the two side by side; never set it in real use. */
  optical?: boolean;
  /**
   * Widens the view box around the mark without changing the element's size,
   * so movement that travels beyond the mark's own bounds stays visible.
   *
   * `spiral` throws its nodes out to 2.7x, which is 28 user units from centre
   * against a 12-unit half-box — at `bleed: 1` the first third of the entry
   * happens entirely outside the frame and is simply clipped away. `bleed: 3`
   * gives a 36-unit half-box, so the whole approach plays inside the element.
   *
   * `size` always means the ELEMENT, never the drawing: the mark itself comes
   * out at `size / bleed`. That keeps layout predictable — ask for 240px and
   * you get a 240px box — at the cost of the glyph being smaller than the box,
   * which is exactly right for a loader that needs room to move.
   */
  bleed?: number;
  /**
   * Overrides the loop length in milliseconds. Each variant has its own
   * default — spiral 2800, swarm 2600 — chosen for a mark that is the focus of
   * the screen. A 16px spinner inside a button is not the focus, and at the
   * full tempo its formation hold reads as a stall, so `Spinner` shortens it.
   *
   * Scaling the cycle keeps every proportion intact: hold, ramps and peaks all
   * move together, because every duration in the stylesheet derives from this
   * one number.
   */
  cycle?: number;
  label?: string;
  className?: string;
  style?: CSSProperties;
}

export function Mark({
  size = 24,
  tone = 'brand',
  loading,
  progress,
  label,
  className,
  style,
  optical = true,
  bleed = 1,
  cycle,
}: MarkProps) {
  /* Weights are attributes, deliberately, and the stylesheet declares none of
   * them. A CSS declaration always beats a presentation attribute — the same
   * rule that let `svg { fill: currentColor }` silently override every
   * `fill="none"` in this file's history — so a `stroke-width` left in the CSS
   * would quietly win over every optical size computed here. */
  /* Optical sizing keys off the size the DRAWING renders at, not the element.
   * With bleed the two differ, and using the element size would under-weight a
   * bled mark by exactly the bleed factor — a 240px spiral draws an 80px mark
   * and needs an 80px mark's weights. */
  const drawnSize = size / bleed;
  const ks = optical ? opticalScale(drawnSize, STROKE_EXPONENT) : 1;
  const kr = optical ? opticalScale(drawnSize, RADIUS_EXPONENT) : 1;
  const w = {
    edge: 1.5 * ks,
    route: 2 * ks,
    node: 2.2 * kr,
    rest: 2 * kr,
  };
  const edgeAlpha = optical ? edgeOpacity(drawnSize) : EDGE_OPACITY;

  const NODES = [
    { id: 'rest', cx: RIGHT.x, cy: RIGHT.y, r: w.rest },
    { id: 'entry', cx: TOP.x, cy: TOP.y, r: w.node },
    { id: 'via', cx: LEFT.x, cy: LEFT.y, r: w.node },
    { id: 'exit', cx: BOTTOM.x, cy: BOTTOM.y, r: w.node },
  ] as const;

  return (
    <svg
      data-stratum="mark"
      className={clsx('stratum-mark', className)}
      data-tone={tone}
      data-loading={loading}
      width={size}
      height={size}
      /* Grown symmetrically about the centre, so (12,12) stays the centre at
       * every bleed and every transform-origin in the stylesheet stays valid. */
      viewBox={
        bleed === 1
          ? '0 0 24 24'
          : `${C - C * bleed} ${C - C * bleed} ${2 * C * bleed} ${2 * C * bleed}`
      }
      fill="none"
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      style={
        {
          ...style,
          ...(progress === undefined
            ? null
            : { '--_progress': String(Math.min(1, Math.max(0, progress))) }),
          ...(cycle === undefined ? null : { '--_cycle': `${cycle}ms` }),
        } as CSSProperties
      }
    >
      {/* Quiet structure: the whole diamond, including the edges not taken.
        * `pathLength` normalises all four edges onto a single 0..1 run, so a
        * dash offset draws them end to end without knowing their real length. */}
      {/* `__figure` wraps everything, so a whole-mark move — the spiral squeeze,
        * the swarm orbit — can be expressed without disturbing what the nodes
        * are doing inside it. Nested transforms compose; a single flat group
        * would force a choice between the two. */}
      <g className="stratum-mark__figure">
      {/* The edges are wrapped so their FADE and their own TONE can be separate
        * things. Group opacity multiplies with child opacity, so an animation
        * can take the group 0 -> 1 while the path keeps whatever the optical
        * size decided — 0.34 at 24px, 0.59 at 12px. Animating the path's own
        * opacity instead drove it to a flat 1 mid-cycle and the quiet edges
        * came out full-strength black, louder than the accent they exist to
        * sit behind. A keyframe cannot read the attribute, so the value has to
        * live one level down from the thing being animated. */}
      <g className="stratum-mark__edges">
        <path
          className="stratum-mark__diamond"
          d={diamondPath(w.rest)}
          strokeWidth={w.edge}
          opacity={edgeAlpha}
          pathLength={1}
        />
      </g>

      <g className="stratum-mark__body">
        {/* Filled, then stroked over its own edge so the wedge picks up the
          * round caps and joins rather than reading as a hard shard. */}
        <path className="stratum-mark__wedge" d={ROUTE} />
        <path className="stratum-mark__route" d={ROUTE} strokeWidth={w.route} pathLength={1} />

        {/* THREE NESTED TRANSFORM LEVELS, each owning one kind of movement, so
          * they compose instead of fighting:
          *
          *   __nodes  the constellation. Origin at the mark's centre, so a
          *            scale moves every node radially and a rotate orbits them.
          *   __slot   one node's place in the ring. Same centre origin, so a
          *            scale on a single slot flies THAT node out on its own
          *            radius — which is what staggering an arrival needs.
          *   __node   the dot itself. Origin at its own centre, so a scale
          *            swells it in place without moving it.
          *
          * Flattened into one group, a per-node arrival and a whole-ring orbit
          * cannot both be expressed: whichever is written last replaces the
          * other, because an element has exactly one transform. */}
        <g className="stratum-mark__nodes">
          {NODES.map((n) => (
            <g className="stratum-mark__slot" data-node={n.id} key={n.id}>
              <circle className="stratum-mark__node" data-node={n.id} cx={n.cx} cy={n.cy} r={n.r} />
            </g>
          ))}
        </g>
      </g>
      </g>
    </svg>
  );
}
