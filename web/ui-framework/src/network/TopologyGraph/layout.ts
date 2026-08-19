/* ---------------------------------------------------------------------------
 * TOPOLOGY LAYOUT — deterministic, dependency-free
 *
 * WHY NO GRAPH LIBRARY
 * --------------------
 * d3-force, cytoscape and elk all ship their own scheduler, their own RNG and,
 * in two of the three, their own DOM writes. That is 40-120 KB to place a few
 * dozen discs, and it takes ownership of the render loop away from React. It
 * also makes the layout non-reproducible: d3-force seeds from `Math.random`
 * unless you replace `d3.randomLcg`, so the same peer set draws differently on
 * every reload and an operator can never build spatial memory of their own
 * mesh. Every function here is pure and synchronous, and the only randomness
 * comes from a seeded PRNG, so identical input always produces an identical
 * picture.
 *
 * Everything in this file is geometry. It knows nothing about React, the DOM,
 * colour, or what the nodes represent.
 * ------------------------------------------------------------------------- */

const TAU = Math.PI * 2;
/** Golden angle — spreads disconnected components without a visible period. */
const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));

/** Two decimals is below one device pixel at any sane zoom and keeps path data short. */
export const round2 = (v: number): number => Math.round(v * 100) / 100;

export const clamp = (v: number, lo: number, hi: number): number =>
  v < lo ? lo : v > hi ? hi : v;

/* -------------------------------------------------------------------------- */
/* Public data shapes                                                          */
/* -------------------------------------------------------------------------- */

/**
 * What a node *is*, in the abstract. Deliberately generic: the framework must
 * not know what any of these mean in a particular deployment, only that they
 * are seven visually distinguishable classes.
 */
export type GraphRole =
  | 'self'
  | 'reachable'
  | 'medium'
  | 'offline'
  | 'native'
  | 'nested'
  | 'disabled';

/** Health of a link. `'unknown'` means NOT OBSERVED — never "probably fine". */
export type EdgeStatus = 'ok' | 'degraded' | 'down' | 'unknown';

/**
 * Anything a node's `role` may be.
 *
 * The seven built-ins keep their autocomplete; any other string is a role the
 * consumer registered through `<TopologyGraph roles>`. The intersection with an
 * empty object is the standard trick that stops TypeScript collapsing the union
 * to bare `string` and throwing the literal suggestions away.
 */
export type TopologyRole = GraphRole | (string & Record<never, never>);

/** Anything an edge's `status` may be. See {@link TopologyRole}. */
export type TopologyEdgeStatus = EdgeStatus | (string & Record<never, never>);

/**
 * The non-colour half of a role's identity.
 *
 * Every role MUST differ from every other role in a channel that is not hue,
 * because two of the built-in palettes are close under deuteranopia and all of
 * them collapse to two system keywords under `forced-colors`. These are the
 * shapes `NodeGlyph` can draw; a registered role picks one (or a dash pattern,
 * or both).
 *
 *   disc        plain filled circle — the baseline
 *   concentric  circle with an inner disc            (built-in `self`)
 *   ring        circle with an open outer arc        (built-in `medium`)
 *   cross       circle with a diagonal cross         (built-in `offline`)
 *   square      rounded square                       (built-in `native`)
 *   diamond     square on its point
 *   triangle    equilateral, point up
 *   hexagon     regular, point up
 */
export type TopologyRoleShape =
  | 'disc'
  | 'concentric'
  | 'ring'
  | 'cross'
  | 'square'
  | 'diamond'
  | 'triangle'
  | 'hexagon';

/**
 * How a role is drawn.
 *
 * `colour` is GRAPHICS-grade: it is a fill and a stroke, so it needs 3:1
 * against the canvas (SC 1.4.11), not the 4.5:1 a glyph a human reads needs.
 *
 * At least one of `shape` and `dash` should be set. Colour is never allowed to
 * be the only channel — two of the built-in hues are close under deuteranopia
 * and every one of them collapses to a system keyword under `forced-colors`. A
 * role registered with neither falls back to a hexagon, which no built-in uses,
 * and logs in development.
 *
 * Lives here rather than next to the component so the graph and the legend can
 * share one declaration; the legend that disagrees with the diagram is worse
 * than no legend at all.
 */
export interface TopologyRoleStyle {
  /** Stroke colour. Any CSS colour, including a `var(--your-token)`. */
  colour: string;
  /** Interior fill. Use a wash of `colour` so an 11px disc stays readable. */
  fill: string;
  /** Non-colour channel: the mark's geometry. */
  shape?: TopologyRoleShape;
  /** Non-colour channel: dash pattern for the mark's stroke, e.g. `'4 3'`. */
  dash?: string;
  /** Drops the interior fill, as `nested` and `disabled` do. */
  hollow?: boolean;
  /** Text used in accessible names and legend rows. Falls back to the key. */
  label?: string;
}

/**
 * How an edge status is drawn.
 *
 * `dash` should always be set for a registered status, for the same reason
 * {@link TopologyRoleStyle} wants a shape.
 */
export interface TopologyStatusStyle {
  colour: string;
  /** Non-colour channel: `'none'`, or a dash pattern such as `'9 5'`. */
  dash?: string;
  /** Text used in accessible names, edge tooltips and legend rows. */
  label?: string;
}

export interface GraphPoint {
  x: number;
  y: number;
}

export interface GraphNode {
  /** Stable identity. Also the fallback label. */
  id: string;
  label?: string;
  /**
   * One of the seven built-in roles, or any string registered through
   * `<TopologyGraph roles>`. An unregistered string still draws — as a hexagon,
   * so it cannot be confused with a built-in — and logs in development.
   */
  role?: TopologyRole;
  /** Free-form grouping key. Not used by layout; surfaced to assistive tech. */
  group?: string;
  /** Opaque payload handed back to the consumer's callbacks. */
  meta?: Record<string, unknown>;
  /** Pins the node. Layout places everything else around it. */
  fixed?: GraphPoint;
}

export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  /**
   * Draws a direction arrow. Default `true`. Set `false` for a link whose
   * direction is meaningless (a symmetric mesh adjacency, say).
   */
  directed?: boolean;
  /**
   * One of the four built-in statuses, or any string registered through
   * `<TopologyGraph statuses>`.
   */
  status?: TopologyEdgeStatus;
  /**
   * Observed throughput in BYTES per second travelling `to` -> `from`.
   * `null`/`undefined` means NOT OBSERVED and must never be drawn as zero.
   */
  rateIn?: number | null;
  /** Observed throughput in BYTES per second travelling `from` -> `to`. */
  rateOut?: number | null;
  /** Link capacity in BYTES per second, if known. Drawn as a ghost stroke. */
  capacity?: number | null;
  label?: string;
}

/* -------------------------------------------------------------------------- */
/* Seeded PRNG                                                                 */
/* -------------------------------------------------------------------------- */

/** FNV-1a. Turns a seed string into a 32-bit integer. */
export function hashSeed(seed: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < seed.length; i += 1) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/**
 * mulberry32 — 32 bits of state, passes gjrand, ~10 lines.
 *
 * A PRNG rather than `Math.random` because layout must be reproducible: an
 * operator learns where their own nodes sit, and a graph that reshuffles on
 * every poll destroys that far more effectively than it communicates change.
 */
export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/* -------------------------------------------------------------------------- */
/* Adjacency index                                                             */
/* -------------------------------------------------------------------------- */

export interface GraphIndex {
  /** id -> position in the caller's `nodes` array. The tie-break for every sort. */
  order: Map<string, number>;
  /** id -> distinct neighbour ids, in caller order. Self-loops excluded. */
  neighbours: Map<string, string[]>;
  /** id -> distinct neighbour count. Drives transit-node sizing. */
  degree: Map<string, number>;
  /** Edges whose endpoints both exist. Everything else is dropped. */
  validEdges: GraphEdge[];
  /** Edges referencing a node that is not in `nodes`. Reported, not silently lost. */
  danglingEdges: GraphEdge[];
}

export function buildGraphIndex(nodes: GraphNode[], edges: GraphEdge[]): GraphIndex {
  const order = new Map<string, number>();
  nodes.forEach((n, i) => {
    if (!order.has(n.id)) order.set(n.id, i);
  });

  const sets = new Map<string, Set<string>>();
  for (const id of order.keys()) sets.set(id, new Set<string>());

  const validEdges: GraphEdge[] = [];
  const danglingEdges: GraphEdge[] = [];

  for (const e of edges) {
    if (!order.has(e.from) || !order.has(e.to)) {
      danglingEdges.push(e);
      continue;
    }
    validEdges.push(e);
    if (e.from === e.to) continue;
    sets.get(e.from)?.add(e.to);
    sets.get(e.to)?.add(e.from);
  }

  const neighbours = new Map<string, string[]>();
  const degree = new Map<string, number>();
  for (const [id, set] of sets) {
    const list = [...set].sort((a, b) => (order.get(a) ?? 0) - (order.get(b) ?? 0));
    neighbours.set(id, list);
    degree.set(id, list.length);
  }

  return { order, neighbours, degree, validEdges, danglingEdges };
}

/**
 * Node radius from degree and role.
 *
 * Transit nodes are drawn larger because size is a pre-attentive channel: an
 * operator finds the three nodes everything routes through before they have
 * consciously read a single label. Colour cannot do this — it is already
 * carrying role.
 */
export function nodeRadius(
  base: number,
  degree: number,
  role: TopologyRole | undefined,
  isRoot: boolean,
): number {
  let r = base;
  if (degree >= 6) r *= 1.42;
  else if (degree >= 3) r *= 1.2;
  if (role === 'self' || isRoot) r = Math.max(r, base * 1.3);
  return round2(r);
}

/** A node with three or more distinct neighbours is a transit point. */
export const isTransit = (degree: number): boolean => degree >= 3;

/* -------------------------------------------------------------------------- */
/* Layout                                                                      */
/* -------------------------------------------------------------------------- */

/** The layouts the library ships. Also usable as a plain `layout` prop value. */
export type LayoutMode = 'radial' | 'force' | 'hierarchical';

/** Size of the drawing surface in CSS pixels, when it has been measured. */
export interface TopologyLayoutViewport {
  width: number;
  height: number;
}

/** One connected component, as the built-in layouts see it. */
export interface TopologyLayoutComponent {
  /** Member ids in BFS order from `root`. */
  members: string[];
  /**
   * The member the built-ins radiate from: the graph root for the first
   * component, then the highest-degree member of each remaining one.
   */
  root: string;
}

/**
 * Everything a layout is given.
 *
 * `nodes`, `edges`, `viewport` and `seed` are the contract; the rest is
 * precomputed work a layout would otherwise have to redo — adjacency, the
 * component split, per-node radii and a seeded PRNG.
 */
export interface TopologyLayoutInput {
  /** Deduplicated, in the order the consumer supplied them. */
  nodes: GraphNode[];
  /** Only edges whose endpoints both exist. Dangling edges are already dropped. */
  edges: GraphEdge[];
  /** Adjacency, degree and validity, built once per data change. */
  index: GraphIndex;
  /** Connected components, primary first. */
  components: TopologyLayoutComponent[];
  /**
   * Container size in CSS pixels, or `null` before the first measurement.
   *
   * A layout that reads this re-runs whenever the panel is resized, which is
   * why the built-ins deliberately ignore it: an operator's spatial memory of
   * their own mesh is worth more than filling the box exactly.
   */
  viewport: TopologyLayoutViewport | null;
  /** The `seed` prop. Same seed + same data must give the same picture. */
  seed: string;
  /** Resolved root: explicit `rootId`, else the `self` node, else max degree. */
  rootId: string | undefined;
  /** id -> drawn radius, already including the transit and root multipliers. */
  radii: Map<string, number>;
  /** Base radius before those multipliers. */
  nodeRadius: number;
  /** Minimum ring separation, and the force solver's ideal edge length. */
  ringGap: number;
  /** Iteration budget for an iterative solver. */
  forceIterations: number;
  /**
   * A PRNG seeded from `seed`. Use this rather than `Math.random` — a layout
   * that reshuffles on every poll destroys spatial memory far more effectively
   * than it communicates change.
   */
  random: () => number;
}

/**
 * A layout: pure geometry, from graph to coordinates.
 *
 * Return one entry per node in graph units, centred anywhere — the component
 * derives its own viewBox from the result. Ids you omit fall back to the
 * origin; non-finite values are treated as missing. Pins (`node.fixed`) and
 * disc-overlap resolution are applied by the component afterwards, so a layout
 * never has to think about either.
 */
export type TopologyLayout = (input: TopologyLayoutInput) => Map<string, GraphPoint>;

export interface LayoutOptions {
  /** A built-in name, or any {@link TopologyLayout}. */
  mode: LayoutMode | TopologyLayout;
  /** Preferred root. Falls back to the `self` node, then to highest degree. */
  rootId?: string | undefined;
  seed: string;
  /** Minimum distance between consecutive rings, in graph units. */
  ringGap: number;
  /** Base node radius, before the transit multiplier. */
  nodeRadius: number;
  /** Iterations of the force solver. Ignored in radial mode. */
  forceIterations: number;
  /** Surface size, for layouts that adapt to it. Built-ins ignore it. */
  viewport?: TopologyLayoutViewport | null | undefined;
}

export interface LayoutResult {
  positions: Map<string, GraphPoint>;
  radii: Map<string, number>;
  rootId: string | undefined;
  /** One entry per connected component, in placement order. */
  components: string[][];
}

/**
 * Picks the node the layout radiates from.
 *
 * Order of preference: an explicit `rootId`, then the `self` node (the one the
 * operator is looking out from), then the highest-degree node, tie-broken by
 * input order so the choice never depends on `Map` iteration luck.
 */
function chooseRoot(
  nodes: GraphNode[],
  index: GraphIndex,
  preferred: string | undefined,
): string | undefined {
  if (preferred && index.order.has(preferred)) return preferred;
  const self = nodes.find((n) => n.role === 'self');
  if (self) return self.id;

  let best: string | undefined;
  let bestDegree = -1;
  for (const n of nodes) {
    const d = index.degree.get(n.id) ?? 0;
    if (d > bestDegree) {
      bestDegree = d;
      best = n.id;
    }
  }
  return best;
}

/** All ids reachable from `seed`, in BFS order. Marks them in `seen`. */
function collectComponent(seed: string, index: GraphIndex, seen: Set<string>): string[] {
  const out: string[] = [seed];
  seen.add(seed);
  for (let i = 0; i < out.length; i += 1) {
    const id = out[i]!;
    for (const nb of index.neighbours.get(id) ?? []) {
      if (seen.has(nb)) continue;
      seen.add(nb);
      out.push(nb);
    }
  }
  return out;
}

interface ComponentLayout {
  positions: Map<string, GraphPoint>;
  /** Radius of the circle that encloses the component, centred on its root. */
  extent: number;
}

/**
 * Reingold–Tilford, in polar coordinates.
 *
 * A BFS tree is extracted from the component and each subtree is given an
 * angular wedge proportional to its leaf count, so a bushy branch gets the room
 * it needs and a chain does not waste a quadrant. Ring radius grows to whatever
 * the ring's population actually requires, which is what stops the classic
 * radial failure where the third ring is a solid band of overlapping discs.
 */
function radialComponent(
  rootId: string,
  members: string[],
  index: GraphIndex,
  radii: Map<string, number>,
  ringGap: number,
  baseRadius: number,
): ComponentLayout {
  const member = new Set(members);

  const depth = new Map<string, number>();
  const children = new Map<string, string[]>();
  const queue: string[] = [rootId];
  const visited = new Set<string>([rootId]);
  depth.set(rootId, 0);

  for (let qi = 0; qi < queue.length; qi += 1) {
    const id = queue[qi]!;
    const kids: string[] = [];
    for (const nb of index.neighbours.get(id) ?? []) {
      if (visited.has(nb) || !member.has(nb)) continue;
      visited.add(nb);
      depth.set(nb, (depth.get(id) ?? 0) + 1);
      kids.push(nb);
      queue.push(nb);
    }
    children.set(id, kids);
  }

  // Leaf weight, computed in reverse BFS order — a valid post-order for a tree.
  const weight = new Map<string, number>();
  for (let i = queue.length - 1; i >= 0; i -= 1) {
    const id = queue[i]!;
    const kids = children.get(id) ?? [];
    if (kids.length === 0) {
      weight.set(id, 1);
      continue;
    }
    let sum = 0;
    for (const k of kids) sum += weight.get(k) ?? 1;
    weight.set(id, sum);
  }

  // Angular wedges, allocated top-down.
  const angle = new Map<string, number>();
  const stack: Array<[string, number, number]> = [[rootId, 0, TAU]];
  while (stack.length > 0) {
    const frame = stack.pop()!;
    const [id, a0, a1] = frame;
    angle.set(id, (a0 + a1) / 2);
    const kids = children.get(id) ?? [];
    if (kids.length === 0) continue;

    let total = 0;
    for (const k of kids) total += weight.get(k) ?? 1;
    if (total <= 0) total = 1;

    let a = a0;
    for (const k of kids) {
      const w = ((weight.get(k) ?? 1) / total) * (a1 - a0);
      stack.push([k, a, a + w]);
      a += w;
    }
  }

  // Ring radii: at least `ringGap` past the previous ring, and always wide
  // enough that the ring's population fits around its circumference.
  let maxDepth = 0;
  const perDepth = new Map<number, number>();
  for (const d of depth.values()) {
    if (d > maxDepth) maxDepth = d;
    perDepth.set(d, (perDepth.get(d) ?? 0) + 1);
  }

  const spacing = baseRadius * 2.9;
  const ringR = new Map<number, number>([[0, 0]]);
  let prev = 0;
  for (let d = 1; d <= maxDepth; d += 1) {
    const count = perDepth.get(d) ?? 1;
    const circumferenceNeed = (spacing * count) / TAU;
    const r = Math.max(prev + ringGap, circumferenceNeed);
    ringR.set(d, r);
    prev = r;
  }

  const positions = new Map<string, GraphPoint>();
  let extent = baseRadius;
  for (const id of members) {
    const d = depth.get(id) ?? 0;
    const r = ringR.get(d) ?? 0;
    const th = angle.get(id) ?? 0;
    positions.set(id, { x: Math.cos(th) * r, y: Math.sin(th) * r });
    extent = Math.max(extent, r + (radii.get(id) ?? baseRadius));
  }

  return { positions, extent };
}

/**
 * Fruchterman–Reingold, seeded from the radial layout.
 *
 * Seeding from the tree rather than from random points is what makes this
 * deterministic *and* well conditioned: the solver starts in a sane basin and
 * only has to relax it, so 300 iterations is plenty and the result does not
 * depend on how long we let it run.
 */
function forceRelax(
  ids: string[],
  pairs: Array<[number, number]>,
  seedPositions: Map<string, GraphPoint>,
  fixedIds: Set<string>,
  iterations: number,
  ideal: number,
  frameRadius: number,
  rand: () => number,
): Map<string, GraphPoint> {
  const n = ids.length;
  const xs: number[] = new Array<number>(n);
  const ys: number[] = new Array<number>(n);

  for (let i = 0; i < n; i += 1) {
    const p = seedPositions.get(ids[i]!);
    // A tiny seeded nudge breaks the symmetry of coincident seeds without
    // making the result depend on wall-clock or call order.
    xs[i] = (p?.x ?? 0) + (rand() - 0.5) * 0.5;
    ys[i] = (p?.y ?? 0) + (rand() - 0.5) * 0.5;
  }

  const k = ideal;
  const k2 = k * k;
  const t0 = k * 1.2;
  const dx: number[] = new Array<number>(n).fill(0);
  const dy: number[] = new Array<number>(n).fill(0);

  for (let step = 0; step < iterations; step += 1) {
    const progress = step / Math.max(1, iterations);
    const temp = t0 * Math.pow(1 - progress, 1.5);

    dx.fill(0);
    dy.fill(0);

    // Repulsion — every pair.
    for (let i = 0; i < n; i += 1) {
      for (let j = i + 1; j < n; j += 1) {
        let vx = xs[i]! - xs[j]!;
        let vy = ys[i]! - ys[j]!;
        let d2 = vx * vx + vy * vy;
        if (d2 < 0.0001) {
          vx = 0.01;
          vy = 0.01;
          d2 = 0.0002;
        }
        const d = Math.sqrt(d2);
        const f = k2 / d;
        const ux = (vx / d) * f;
        const uy = (vy / d) * f;
        dx[i]! += ux;
        dy[i]! += uy;
        dx[j]! -= ux;
        dy[j]! -= uy;
      }
    }

    // Attraction — along edges.
    for (const [a, b] of pairs) {
      const vx = xs[a]! - xs[b]!;
      const vy = ys[a]! - ys[b]!;
      const d = Math.sqrt(vx * vx + vy * vy) || 0.01;
      const f = (d * d) / k;
      const ux = (vx / d) * f;
      const uy = (vy / d) * f;
      dx[a]! -= ux;
      dy[a]! -= uy;
      dx[b]! += ux;
      dy[b]! += uy;
    }

    // Mild gravity centres the drawing.
    for (let i = 0; i < n; i += 1) {
      dx[i]! -= xs[i]! * 0.08;
      dy[i]! -= ys[i]! * 0.08;
    }

    for (let i = 0; i < n; i += 1) {
      if (fixedIds.has(ids[i]!)) continue;
      const len = Math.sqrt(dx[i]! * dx[i]! + dy[i]! * dy[i]!) || 1;
      const limited = Math.min(len, temp);
      let nx = xs[i]! + (dx[i]! / len) * limited;
      let ny = ys[i]! + (dy[i]! / len) * limited;

      // Frame clamp, as in the original Fruchterman–Reingold. Gravity alone
      // cannot hold this: a node with no edges feels only repulsion, which
      // falls off as 1/d, so it accelerates away until the frame stops it. The
      // frame is derived from the seed layout's extent, which also keeps the
      // two layout modes at a comparable scale — a graph that changes size when
      // you switch mode reads as a different graph.
      const radius = Math.sqrt(nx * nx + ny * ny);
      if (radius > frameRadius) {
        const k2 = frameRadius / radius;
        nx *= k2;
        ny *= k2;
      }
      xs[i] = nx;
      ys[i] = ny;
    }
  }

  const out = new Map<string, GraphPoint>();
  for (let i = 0; i < n; i += 1) out.set(ids[i]!, { x: xs[i]!, y: ys[i]! });
  return out;
}

/**
 * Pushes overlapping discs apart.
 *
 * Runs after every layout mode, because a graph where two node discs touch is
 * a graph where the operator cannot tell how many nodes they are looking at —
 * which is worse than any amount of aesthetic imbalance.
 */
function resolveCollisions(
  ids: string[],
  positions: Map<string, GraphPoint>,
  radii: Map<string, number>,
  fixedIds: Set<string>,
  pad: number,
  passes = 48,
): void {
  const n = ids.length;
  for (let pass = 0; pass < passes; pass += 1) {
    let moved = false;
    for (let i = 0; i < n; i += 1) {
      const ai = ids[i]!;
      const a = positions.get(ai);
      if (!a) continue;
      const ra = radii.get(ai) ?? 0;
      for (let j = i + 1; j < n; j += 1) {
        const bi = ids[j]!;
        const b = positions.get(bi);
        if (!b) continue;
        const rb = radii.get(bi) ?? 0;
        const min = ra + rb + pad;

        let vx = b.x - a.x;
        let vy = b.y - a.y;
        let d = Math.sqrt(vx * vx + vy * vy);
        if (d >= min) continue;

        if (d < 0.0001) {
          // Perfectly coincident: separate along a stable axis derived from
          // the pair's ordering rather than from a random direction.
          const th = ((i * 7 + j * 13) % 360) * (Math.PI / 180);
          vx = Math.cos(th);
          vy = Math.sin(th);
          d = 1;
        }

        const overlap = min - d;
        const ux = (vx / d) * overlap;
        const uy = (vy / d) * overlap;

        const aFixed = fixedIds.has(ai);
        const bFixed = fixedIds.has(bi);
        if (aFixed && bFixed) continue;

        if (aFixed) {
          b.x += ux;
          b.y += uy;
        } else if (bFixed) {
          a.x -= ux;
          a.y -= uy;
        } else {
          a.x -= ux * 0.5;
          a.y -= uy * 0.5;
          b.x += ux * 0.5;
          b.y += uy * 0.5;
        }
        moved = true;
      }
    }
    if (!moved) break;
  }
}

/**
 * Layered tree, root at the top.
 *
 * A BFS tree is extracted from the component, leaves are laid out left to right
 * at their own widths, and every internal node is centred over its children —
 * the classic tidy-tree x-assignment, done iteratively so a 10,000-hop chain
 * cannot blow the stack. Depth maps straight onto y, which is the whole point:
 * a hierarchy is the one topology where "further from the root" should be a
 * single readable axis rather than a ring.
 */
function hierarchicalComponent(
  rootId: string,
  members: string[],
  index: GraphIndex,
  radii: Map<string, number>,
  levelGap: number,
  siblingGap: number,
  baseRadius: number,
): { positions: Map<string, GraphPoint>; minX: number; maxX: number } {
  const member = new Set(members);

  const depth = new Map<string, number>([[rootId, 0]]);
  const children = new Map<string, string[]>();
  const queue: string[] = [rootId];
  const visited = new Set<string>([rootId]);

  for (let qi = 0; qi < queue.length; qi += 1) {
    const id = queue[qi]!;
    const kids: string[] = [];
    for (const nb of index.neighbours.get(id) ?? []) {
      if (visited.has(nb) || !member.has(nb)) continue;
      visited.add(nb);
      depth.set(nb, (depth.get(id) ?? 0) + 1);
      kids.push(nb);
      queue.push(nb);
    }
    children.set(id, kids);
  }

  const xs = new Map<string, number>();
  let cursor = 0;
  const stack: Array<{ id: string; expanded: boolean }> = [{ id: rootId, expanded: false }];
  while (stack.length > 0) {
    const frame = stack[stack.length - 1]!;
    const kids = children.get(frame.id) ?? [];
    if (!frame.expanded) {
      frame.expanded = true;
      for (let i = kids.length - 1; i >= 0; i -= 1) stack.push({ id: kids[i]!, expanded: false });
      continue;
    }
    stack.pop();
    const r = radii.get(frame.id) ?? baseRadius;
    if (kids.length === 0) {
      const x = cursor + r;
      xs.set(frame.id, x);
      cursor = x + r + siblingGap;
      continue;
    }
    const first = xs.get(kids[0]!) ?? 0;
    const last = xs.get(kids[kids.length - 1]!) ?? first;
    xs.set(frame.id, (first + last) / 2);
  }

  const positions = new Map<string, GraphPoint>();
  let minX = Infinity;
  let maxX = -Infinity;
  for (const id of members) {
    const x = xs.get(id) ?? 0;
    const r = radii.get(id) ?? baseRadius;
    positions.set(id, { x, y: (depth.get(id) ?? 0) * levelGap });
    minX = Math.min(minX, x - r);
    maxX = Math.max(maxX, x + r);
  }
  if (!Number.isFinite(minX)) {
    minX = 0;
    maxX = 0;
  }

  return { positions, minX, maxX };
}

/* -------------------------------------------------------------------------- */
/* Built-in layouts                                                            */
/* -------------------------------------------------------------------------- */

/**
 * Reingold–Tilford in polar coordinates, one tree per component.
 *
 * The primary component sits at the origin and the rest are packed onto a ring
 * wide enough to hold all of their diameters end to end, offset by the golden
 * angle so two components of equal size never land on the same axis and read as
 * one shape.
 */
export const radialLayout: TopologyLayout = (input) => {
  const laid = input.components.map((comp) => ({
    layout: radialComponent(
      comp.root,
      comp.members,
      input.index,
      input.radii,
      input.ringGap,
      input.nodeRadius,
    ),
  }));

  const positions = new Map<string, GraphPoint>();
  const primary = laid[0];
  if (primary) {
    for (const [id, p] of primary.layout.positions) positions.set(id, { x: p.x, y: p.y });
  }

  if (laid.length > 1) {
    const gap = input.ringGap * 0.8;
    let circumference = 0;
    for (let i = 1; i < laid.length; i += 1) circumference += laid[i]!.layout.extent * 2 + gap;

    const ring = Math.max(
      (primary?.layout.extent ?? 0) + (laid[1]?.layout.extent ?? 0) + gap,
      circumference / TAU,
    );

    let arc = 0;
    for (let i = 1; i < laid.length; i += 1) {
      const comp = laid[i]!;
      const span = (comp.layout.extent * 2 + gap) / Math.max(ring, 1);
      const th = arc + span / 2 + GOLDEN_ANGLE * i;
      arc += span;
      const cx = Math.cos(th) * ring;
      const cy = Math.sin(th) * ring;
      for (const [id, p] of comp.layout.positions) positions.set(id, { x: p.x + cx, y: p.y + cy });
    }
  }

  return positions;
};

/**
 * Fruchterman–Reingold, seeded from {@link radialLayout}.
 *
 * Seeding from the tree rather than from random points is what makes this
 * deterministic *and* well conditioned: the solver starts in a sane basin and
 * only has to relax it.
 */
export const forceLayout: TopologyLayout = (input) => {
  const positions = radialLayout(input);

  const ids = input.nodes.map((n) => n.id);
  const idIndex = new Map<string, number>();
  ids.forEach((id, i) => idIndex.set(id, i));

  const pairs: Array<[number, number]> = [];
  for (const e of input.index.validEdges) {
    if (e.from === e.to) continue;
    const a = idIndex.get(e.from);
    const b = idIndex.get(e.to);
    if (a === undefined || b === undefined) continue;
    pairs.push([a, b]);
  }

  // Bound the work: the solver is O(n^2) per iteration, and a panel that blocks
  // for a second on mount is worse than a slightly looser layout.
  const budget = Math.max(40, Math.floor(400_000 / (ids.length * ids.length + 1)));
  const iterations = Math.min(input.forceIterations, budget);

  let seedExtent = input.ringGap;
  for (const [id, p] of positions) {
    seedExtent = Math.max(seedExtent, Math.hypot(p.x, p.y) + (input.radii.get(id) ?? 0));
  }

  const fixedIds = new Set<string>();
  for (const n of input.nodes) if (n.fixed) fixedIds.add(n.id);

  const relaxed = forceRelax(
    ids,
    pairs,
    positions,
    fixedIds,
    iterations,
    input.ringGap * 0.95,
    seedExtent * 1.15,
    mulberry32(hashSeed(input.seed)),
  );
  for (const [id, p] of relaxed) positions.set(id, p);

  return positions;
};

/** Layered tree per component, packed left to right and centred on the origin. */
export const hierarchicalLayout: TopologyLayout = (input) => {
  const levelGap = Math.max(input.ringGap * 0.72, input.nodeRadius * 4);
  const siblingGap = Math.max(input.nodeRadius * 1.6, 12);
  const gap = input.ringGap * 0.8;

  const positions = new Map<string, GraphPoint>();
  let cursor = 0;
  let minX = Infinity;
  let maxX = -Infinity;
  let minY = Infinity;
  let maxY = -Infinity;

  for (const comp of input.components) {
    const laid = hierarchicalComponent(
      comp.root,
      comp.members,
      input.index,
      input.radii,
      levelGap,
      siblingGap,
      input.nodeRadius,
    );
    const shift = cursor - laid.minX;
    for (const [id, p] of laid.positions) {
      const q = { x: p.x + shift, y: p.y };
      positions.set(id, q);
      minX = Math.min(minX, q.x);
      maxX = Math.max(maxX, q.x);
      minY = Math.min(minY, q.y);
      maxY = Math.max(maxY, q.y);
    }
    cursor += laid.maxX - laid.minX + gap;
  }

  if (!Number.isFinite(minX)) return positions;

  // Centre on the origin, so switching layout mode does not also translate the
  // whole drawing out from under the operator.
  const cx = (minX + maxX) / 2;
  const cy = (minY + maxY) / 2;
  for (const [id, p] of positions) positions.set(id, { x: p.x - cx, y: p.y - cy });

  return positions;
};

/**
 * A layout that simply uses coordinates you already have.
 *
 * Anything absent from `positions` is placed by `fallback` — by default the
 * radial layout — so adding one node to a saved arrangement does not drop it at
 * the origin under everything else. Build this outside render (or memoise it):
 * it returns a fresh function each call, and the component re-lays out whenever
 * the `layout` prop's identity changes.
 */
export function presetLayout(
  positions: Record<string, GraphPoint> | ReadonlyMap<string, GraphPoint>,
  fallback: TopologyLayout = radialLayout,
): TopologyLayout {
  const supplied: ReadonlyMap<string, GraphPoint> =
    positions instanceof Map
      ? positions
      : new Map(Object.entries(positions as Record<string, GraphPoint>));

  return (input) => {
    let complete = true;
    for (const n of input.nodes) {
      if (!supplied.has(n.id)) {
        complete = false;
        break;
      }
    }
    // Only pay for the fallback when something is actually missing.
    const base = complete ? null : fallback(input);

    const out = new Map<string, GraphPoint>();
    for (const n of input.nodes) {
      const p = supplied.get(n.id) ?? base?.get(n.id);
      if (p) out.set(n.id, { x: p.x, y: p.y });
    }
    return out;
  };
}

/** The built-in layouts, by name. */
export const TOPOLOGY_LAYOUTS: Record<LayoutMode, TopologyLayout> = {
  radial: radialLayout,
  force: forceLayout,
  hierarchical: hierarchicalLayout,
};

/** Turns a `layout` prop value into a callable layout. */
export function resolveLayout(mode: LayoutMode | TopologyLayout): TopologyLayout {
  if (typeof mode === 'function') return mode;
  return TOPOLOGY_LAYOUTS[mode] ?? radialLayout;
}

/**
 * Splits the graph into connected components, primary first.
 *
 * The seed order is `[rootId, ...every node]`, so the component holding the
 * root is always component zero however the caller ordered `nodes`.
 */
function splitComponents(
  nodes: GraphNode[],
  index: GraphIndex,
  rootId: string | undefined,
): TopologyLayoutComponent[] {
  const seen = new Set<string>();
  const seeds: string[] = rootId ? [rootId, ...nodes.map((n) => n.id)] : nodes.map((n) => n.id);
  const out: TopologyLayoutComponent[] = [];

  for (const seed of seeds) {
    if (seen.has(seed)) continue;
    const members = collectComponent(seed, index, seen);

    let root = members[0]!;
    if (seed === rootId) {
      root = seed;
    } else {
      let bestDegree = -1;
      for (const id of members) {
        const d = index.degree.get(id) ?? 0;
        if (d > bestDegree) {
          bestDegree = d;
          root = id;
        }
      }
    }

    out.push({ members, root });
  }

  return out;
}

/**
 * Lays the whole graph out.
 *
 * Deterministic in every branch: the same `nodes`, `edges`, `mode` and `seed`
 * always produce byte-identical positions.
 *
 * The mode may be a built-in name or any {@link TopologyLayout}. Whichever it
 * is, this function owns the three things a layout must not have to care about:
 * radii, `fixed` pins, and disc-overlap resolution. A custom layout that
 * returns nonsense — a missing id, a NaN — degrades to the origin rather than
 * corrupting the viewBox.
 */
export function computeLayout(
  nodes: GraphNode[],
  index: GraphIndex,
  options: LayoutOptions,
): LayoutResult {
  const rootId = chooseRoot(nodes, index, options.rootId);

  const radii = new Map<string, number>();
  for (const n of nodes) {
    radii.set(
      n.id,
      nodeRadius(options.nodeRadius, index.degree.get(n.id) ?? 0, n.role, n.id === rootId),
    );
  }

  if (nodes.length === 0) {
    return { positions: new Map<string, GraphPoint>(), radii, rootId, components: [] };
  }

  const parts = splitComponents(nodes, index, rootId);

  const raw = resolveLayout(options.mode)({
    nodes,
    edges: index.validEdges,
    index,
    components: parts,
    viewport: options.viewport ?? null,
    seed: options.seed,
    rootId,
    radii,
    nodeRadius: options.nodeRadius,
    ringGap: options.ringGap,
    forceIterations: options.forceIterations,
    random: mulberry32(hashSeed(options.seed)),
  });

  // Copy into node order and sanitise. A layout is consumer code now, so a
  // missing id or a NaN must not be allowed to reach the viewBox maths.
  const positions = new Map<string, GraphPoint>();
  for (const n of nodes) {
    const p = raw.get(n.id);
    positions.set(n.id, {
      x: p && Number.isFinite(p.x) ? p.x : 0,
      y: p && Number.isFinite(p.y) ? p.y : 0,
    });
  }

  const ids = nodes.map((n) => n.id);
  const fixedIds = new Set<string>();
  for (const n of nodes) if (n.fixed) fixedIds.add(n.id);

  // Pins win over anything the layout decided.
  for (const n of nodes) {
    if (n.fixed) positions.set(n.id, { x: n.fixed.x, y: n.fixed.y });
  }

  resolveCollisions(ids, positions, radii, fixedIds, options.nodeRadius * 0.9);

  for (const [id, p] of positions) {
    positions.set(id, { x: round2(p.x), y: round2(p.y) });
  }

  return { positions, radii, rootId, components: parts.map((p) => p.members) };
}

/* -------------------------------------------------------------------------- */
/* Edge geometry                                                               */
/* -------------------------------------------------------------------------- */

export interface EdgeGeometry {
  /** SVG path data. Always a quadratic; a straight edge is one with zero bow. */
  d: string;
  /** Geometric length in graph units. Drives the constant-time path draw. */
  length: number;
  /** Anchor point for the rate label. */
  mid: GraphPoint;
  /**
   * Unit normal at the midpoint, always pointing "up" the screen where the edge
   * is not vertical. Labels are offset along this so they sit BESIDE the stroke
   * without being rotated — a rotated label on a steep edge reads
   * bottom-to-top, which is unusable in a panel someone is scanning under
   * pressure.
   */
  normal: GraphPoint;
  /** Arrow tip position and heading, in degrees. */
  arrow: { x: number; y: number; angle: number };
}

const quadPoint = (p0: GraphPoint, c: GraphPoint, p1: GraphPoint, t: number): GraphPoint => {
  const u = 1 - t;
  return {
    x: u * u * p0.x + 2 * u * t * c.x + t * t * p1.x,
    y: u * u * p0.y + 2 * u * t * c.y + t * t * p1.y,
  };
};

const quadTangent = (p0: GraphPoint, c: GraphPoint, p1: GraphPoint, t: number): GraphPoint => {
  const u = 1 - t;
  return {
    x: 2 * u * (c.x - p0.x) + 2 * t * (p1.x - c.x),
    y: 2 * u * (c.y - p0.y) + 2 * t * (p1.y - c.y),
  };
};

/** Sampled arc length. 24 segments is exact to well under a pixel at these bows. */
function quadLength(p0: GraphPoint, c: GraphPoint, p1: GraphPoint): number {
  let len = 0;
  let prev = p0;
  for (let i = 1; i <= 24; i += 1) {
    const pt = quadPoint(p0, c, p1, i / 24);
    len += Math.hypot(pt.x - prev.x, pt.y - prev.y);
    prev = pt;
  }
  return len;
}

/**
 * Builds the drawable path for one edge.
 *
 * Endpoints are trimmed to the node radius so the stroke starts at the disc
 * boundary — an edge that disappears under a node makes it impossible to tell a
 * link from a near-miss. `bow` separates parallel edges between the same pair;
 * a single edge gets `bow = 0` and is a straight line.
 */
export function edgeGeometry(
  a: GraphPoint,
  b: GraphPoint,
  ra: number,
  rb: number,
  bow: number,
  arrowInset: number,
  /**
   * Where along the edge the label is anchored. Parallel edges are given
   * different values so their labels separate ALONG the link rather than
   * stacking on top of each other — perpendicular offset alone cannot move a
   * 60-unit-wide label clear of its twin.
   */
  labelT = 0.5,
): EdgeGeometry {
  const vx = b.x - a.x;
  const vy = b.y - a.y;
  const dist = Math.hypot(vx, vy) || 0.0001;
  const ux = vx / dist;
  const uy = vy / dist;

  // Trim to the disc boundary, but never past the far node — two overlapping
  // nodes still get a (degenerate) stub rather than an inverted path.
  const startTrim = Math.min(ra + 1, dist * 0.45);
  const endTrim = Math.min(rb + arrowInset, dist * 0.45);

  const p0: GraphPoint = { x: a.x + ux * startTrim, y: a.y + uy * startTrim };
  const p1: GraphPoint = { x: b.x - ux * endTrim, y: b.y - uy * endTrim };

  const mx = (p0.x + p1.x) / 2;
  const my = (p0.y + p1.y) / 2;
  const c: GraphPoint = { x: mx - uy * bow * 2, y: my + ux * bow * 2 };

  const t = clamp(labelT, 0.12, 0.88);
  const mid = quadPoint(p0, c, p1, t);
  const tan = quadTangent(p0, c, p1, t);
  const tanLen = Math.hypot(tan.x, tan.y) || 1;
  // Rotate the tangent 90 degrees, then flip so it points up-screen. A vertical
  // edge has a horizontal normal, so its label steps aside instead of up.
  let nx = -tan.y / tanLen;
  let ny = tan.x / tanLen;
  if (ny > 0) {
    nx = -nx;
    ny = -ny;
  }

  const at = quadPoint(p0, c, p1, 0.62);
  const atan = quadTangent(p0, c, p1, 0.62);

  return {
    d: `M ${round2(p0.x)} ${round2(p0.y)} Q ${round2(c.x)} ${round2(c.y)} ${round2(p1.x)} ${round2(p1.y)}`,
    length: round2(quadLength(p0, c, p1)),
    mid: { x: round2(mid.x), y: round2(mid.y) },
    normal: { x: round2(nx), y: round2(ny) },
    arrow: {
      x: round2(at.x),
      y: round2(at.y),
      angle: round2((Math.atan2(atan.y, atan.x) * 180) / Math.PI),
    },
  };
}

/** Self-loop: a teardrop above the node. */
export function selfLoopGeometry(p: GraphPoint, r: number): EdgeGeometry {
  const w = r * 1.5;
  const h = r * 3.1;
  const p0: GraphPoint = { x: p.x - r * 0.55, y: p.y - r * 0.82 };
  const p1: GraphPoint = { x: p.x + r * 0.55, y: p.y - r * 0.82 };
  const c0: GraphPoint = { x: p.x - w * 2, y: p.y - h };
  const c1: GraphPoint = { x: p.x + w * 2, y: p.y - h };

  // Cubic, sampled for length; the label sits at the apex.
  let len = 0;
  let prev = p0;
  const cubic = (t: number): GraphPoint => {
    const u = 1 - t;
    return {
      x: u * u * u * p0.x + 3 * u * u * t * c0.x + 3 * u * t * t * c1.x + t * t * t * p1.x,
      y: u * u * u * p0.y + 3 * u * u * t * c0.y + 3 * u * t * t * c1.y + t * t * t * p1.y,
    };
  };
  for (let i = 1; i <= 24; i += 1) {
    const pt = cubic(i / 24);
    len += Math.hypot(pt.x - prev.x, pt.y - prev.y);
    prev = pt;
  }
  const apex = cubic(0.5);

  return {
    d: `M ${round2(p0.x)} ${round2(p0.y)} C ${round2(c0.x)} ${round2(c0.y)} ${round2(c1.x)} ${round2(c1.y)} ${round2(p1.x)} ${round2(p1.y)}`,
    length: round2(len),
    mid: { x: round2(apex.x), y: round2(apex.y) },
    normal: { x: 0, y: -1 },
    arrow: { x: round2(apex.x + w * 0.5), y: round2(apex.y), angle: 90 },
  };
}

/** Triangle pointing along `angle` (degrees), centred on its own base. */
export function arrowPath(x: number, y: number, angleDeg: number, size: number): string {
  const a = (angleDeg * Math.PI) / 180;
  const cos = Math.cos(a);
  const sin = Math.sin(a);
  const pt = (px: number, py: number): string =>
    `${round2(x + px * cos - py * sin)} ${round2(y + px * sin + py * cos)}`;
  return `M ${pt(size, 0)} L ${pt(-size * 0.8, size * 0.62)} L ${pt(-size * 0.8, -size * 0.62)} Z`;
}

/** Circular arc from `a0` to `a1` (radians) at radius `r`, centred on the origin. */
export function arcPath(r: number, a0: number, a1: number): string {
  const x0 = round2(Math.cos(a0) * r);
  const y0 = round2(Math.sin(a0) * r);
  const x1 = round2(Math.cos(a1) * r);
  const y1 = round2(Math.sin(a1) * r);
  const large = Math.abs(a1 - a0) > Math.PI ? 1 : 0;
  return `M ${x0} ${y0} A ${round2(r)} ${round2(r)} 0 ${large} 1 ${x1} ${y1}`;
}

/* -------------------------------------------------------------------------- */
/* Traffic                                                                     */
/* -------------------------------------------------------------------------- */

export type TrafficKind = 'unobserved' | 'idle' | 'active';

export interface EdgeTraffic {
  kind: TrafficKind;
  /** Larger of the two observed directions, or `null` if neither was observed. */
  magnitude: number | null;
  /** `'forward'` is `from` -> `to`. */
  direction: 'forward' | 'backward';
  /** 0..1 against the fixed reference rate. `null` when unobserved. */
  intensity: number | null;
}

/**
 * Reduces an edge's two rate readings to what the renderer needs.
 *
 * NOT OBSERVED IS NOT IDLE. If both directions are `null` the edge reports
 * `'unobserved'` and is drawn at minimum weight with no flow and no rate label
 * — it is *not* drawn as a quiet link, because "we never measured this" and
 * "we measured this and it is carrying nothing" lead to opposite operator
 * decisions.
 */
export function edgeTraffic(edge: GraphEdge, reference: number): EdgeTraffic {
  const out = typeof edge.rateOut === 'number' && Number.isFinite(edge.rateOut) ? edge.rateOut : null;
  const inb = typeof edge.rateIn === 'number' && Number.isFinite(edge.rateIn) ? edge.rateIn : null;

  if (out === null && inb === null) {
    return { kind: 'unobserved', magnitude: null, direction: 'forward', intensity: null };
  }

  const o = out ?? 0;
  const i = inb ?? 0;
  const magnitude = Math.max(o, i);
  const direction: 'forward' | 'backward' = o >= i ? 'forward' : 'backward';

  // Log compression: link rates span six orders of magnitude, so a linear map
  // makes everything below 10 Mbps indistinguishable from zero.
  const ref = reference > 0 ? reference : 1;
  const intensity = clamp(Math.log10(1 + magnitude) / Math.log10(1 + ref), 0, 1);

  return {
    kind: magnitude > 0 ? 'active' : 'idle',
    magnitude,
    direction,
    intensity,
  };
}

/**
 * Stroke width for an observed rate.
 *
 * The scale is anchored to a FIXED reference rather than to the busiest link in
 * this particular graph. A relative scale silently redefines what "thick" means
 * every time the data changes, so an operator comparing two deployments — or
 * the same deployment an hour apart — is reading two different charts.
 */
export function edgeWidth(intensity: number | null, min: number, max: number): number {
  if (intensity === null) return min;
  return round2(min + (max - min) * clamp(intensity, 0, 1));
}
