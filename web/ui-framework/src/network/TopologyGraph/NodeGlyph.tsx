import type { CSSProperties } from 'react';
import clsx from 'clsx';
import { arcPath, round2, type TopologyRole, type TopologyRoleShape } from './layout';
import './NodeGlyph.css';

/** The shape each built-in role draws with. A registered role picks its own. */
const ROLE_SHAPES = new Map<string, TopologyRoleShape>([
  ['self', 'concentric'],
  ['reachable', 'disc'],
  ['medium', 'ring'],
  ['offline', 'cross'],
  ['native', 'square'],
  ['nested', 'disc'],
  ['disabled', 'disc'],
]);

/** Regular polygon centred on the origin, first vertex pointing up. */
function polygonPath(sides: number, r: number): string {
  const points: string[] = [];
  for (let i = 0; i < sides; i += 1) {
    const th = -Math.PI / 2 + (i * (Math.PI * 2)) / sides;
    points.push(`${round2(Math.cos(th) * r)} ${round2(Math.sin(th) * r)}`);
  }
  return `M ${points.join(' L ')} Z`;
}

export interface NodeGlyphProps {
  /** A built-in role, or any string a consumer registered. */
  role: TopologyRole;
  /** Disc radius in the surrounding coordinate system. */
  r: number;
  /** Marks a transit node. Purely presentational; sizing is decided by the caller. */
  transit?: boolean;
  /**
   * Overrides the shape the role would imply. Supply this for any role the
   * library does not know about — colour must never be the only channel.
   */
  shape?: TopologyRoleShape;
  /** Dash pattern for the mark's stroke, e.g. `'4 3'`. A non-colour channel. */
  dash?: string;
  /** Drops the interior fill, as the built-in `nested` and `disabled` roles do. */
  hollow?: boolean;
  /**
   * Graphic colour. Omit for a built-in role: the stylesheet already maps those
   * to their mesh tokens, and passing a value here would beat a consumer's own
   * CSS. It is dropped under `forced-colors`, where the OS owns the palette and
   * `shape`/`dash` carry the role instead.
   */
  colour?: string;
  /** Interior fill. Same caveats as {@link NodeGlyphProps.colour}. */
  fill?: string;
  className?: string;
}

const vars = (o: Record<string, string>): CSSProperties => o as CSSProperties;

/**
 * The shape half of a node's identity, shared by the graph and the legend so
 * the two can never drift apart.
 *
 * COLOUR IS NOT THE CHANNEL
 * -------------------------
 * `tokens.mesh.css` documents two genuinely confusable pairs: medium/native are
 * both warm, nested/disabled are both grey. Under deuteranopia — and under
 * `forced-colors`, where every one of these tokens collapses to CanvasText or
 * GrayText — hue carries nothing. So every role also owns a distinct SHAPE:
 *
 *   self       concentric inner disc
 *   reachable  plain filled disc  (the baseline the others are read against)
 *   medium     partial ring outside the disc
 *   offline    diagonal cross
 *   native     square, not a circle
 *   nested     dashed stroke, hollow
 *   disabled   hollow, solid stroke, reduced opacity
 *
 * Read the glyphs with the page in greyscale: all seven remain separable.
 *
 * EXTENDING IT
 * ------------
 * `shape`, `dash`, `hollow`, `colour` and `fill` exist so a consumer can add an
 * eighth role without editing the library — see `<TopologyGraph roles>`. The
 * built-in roles deliberately pass none of them, so their appearance stays
 * entirely in the stylesheet where a consumer's own CSS can still reach it.
 *
 * Renders a `<g>` and must be placed inside an SVG.
 */
export function NodeGlyph({
  role,
  r,
  transit = false,
  shape,
  dash,
  hollow = false,
  colour,
  fill,
  className,
}: NodeGlyphProps) {
  const inner = r * 0.42;
  const resolved: TopologyRoleShape = shape ?? ROLE_SHAPES.get(role) ?? 'disc';

  // Only emitted when the caller actually supplied one, so a built-in role's
  // paint keeps resolving through the stylesheet rather than an inline value
  // that no consumer rule could ever override.
  const paint: Record<string, string> = {};
  if (colour !== undefined) paint['--_role-colour'] = colour;
  if (fill !== undefined) paint['--_role-fill'] = fill;
  if (dash !== undefined) paint['--_role-dash'] = dash;

  return (
    <g
      className={clsx('stratum-node-glyph', className)}
      data-role={role}
      data-shape={resolved}
      data-transit={transit || undefined}
      data-hollow={hollow || undefined}
      style={Object.keys(paint).length > 0 ? vars(paint) : undefined}
    >
      {/* The class stays `__disc` whatever the geometry is: it is the styling
          contract for a node's mark, and renaming it per shape would break
          every consumer stylesheet that targets it. */}
      {resolved === 'square' ? (
        <rect
          className="stratum-node-glyph__disc"
          x={-r}
          y={-r}
          width={r * 2}
          height={r * 2}
          rx={Math.max(1, r * 0.16)}
        />
      ) : resolved === 'diamond' ? (
        <path className="stratum-node-glyph__disc" d={polygonPath(4, r * 1.16)} />
      ) : resolved === 'triangle' ? (
        <path className="stratum-node-glyph__disc" d={polygonPath(3, r * 1.28)} />
      ) : resolved === 'hexagon' ? (
        <path className="stratum-node-glyph__disc" d={polygonPath(6, r * 1.08)} />
      ) : (
        <circle className="stratum-node-glyph__disc" r={r} />
      )}

      {resolved === 'concentric' && <circle className="stratum-node-glyph__inner" r={inner} />}

      {resolved === 'ring' && (
        <path
          className="stratum-node-glyph__ring"
          // Roughly 200 degrees of ring, opening top-right, so the gap is
          // unmistakable even at an 11px radius.
          d={arcPath(r + Math.max(2.5, r * 0.28), -0.35 * Math.PI, 0.75 * Math.PI)}
        />
      )}

      {resolved === 'cross' && (
        <path
          className="stratum-node-glyph__cross"
          d={`M ${-inner} ${-inner} L ${inner} ${inner} M ${-inner} ${inner} L ${inner} ${-inner}`}
        />
      )}
    </g>
  );
}
