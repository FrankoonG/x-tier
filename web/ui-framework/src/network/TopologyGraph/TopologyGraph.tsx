import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { useMeasure } from '../../hooks/useMeasure';
import { UNOBSERVED, formatBytes, formatPercent, formatRate, truncateId } from '../format';
import { NodeGlyph } from './NodeGlyph';
import {
  arrowPath,
  buildGraphIndex,
  clamp,
  computeLayout,
  edgeGeometry,
  edgeTraffic,
  edgeWidth,
  isTransit,
  round2,
  selfLoopGeometry,
  type EdgeGeometry,
  type EdgeStatus,
  type EdgeTraffic,
  type GraphEdge,
  type GraphNode,
  type GraphPoint,
  type GraphRole,
  type LayoutMode,
  type TopologyEdgeStatus,
  type TopologyLayout,
  type TopologyLayoutViewport,
  type TopologyRole,
  type TopologyRoleShape,
  type TopologyRoleStyle,
  type TopologyStatusStyle,
} from './layout';
import './TopologyGraph.css';

export type {
  GraphNode,
  GraphEdge,
  GraphPoint,
  GraphRole,
  EdgeStatus,
  EdgeTraffic,
  LayoutMode,
  TopologyEdgeStatus,
  TopologyLayout,
  TopologyLayoutComponent,
  TopologyLayoutInput,
  TopologyLayoutViewport,
  TopologyRole,
  TopologyRoleShape,
  TopologyRoleStyle,
  TopologyStatusStyle,
} from './layout';

/**
 * Where the operator's viewport currently sits. Graph units, not pixels.
 *
 * `zoom` is the historical name; {@link TopologyViewport} is the same thing
 * spelled `scale`, and either may be used to control the view.
 */
export interface TopologyView {
  x: number;
  y: number;
  zoom: number;
}

/** {@link TopologyView} under the name the `viewport` prop uses. */
export interface TopologyViewport {
  x: number;
  y: number;
  scale: number;
}

/** Resolved style, after the `roles` prop has been merged over the built-ins. */
interface ResolvedRoleStyle {
  colour: string;
  fill: string;
  shape: TopologyRoleShape;
  dash: string | null;
  hollow: boolean;
  label: string | null;
  /** `'custom'` when the style came from the `roles` prop. */
  source: 'built-in' | 'custom';
}

interface ResolvedStatusStyle {
  colour: string;
  dash: string | null;
  label: string | null;
  source: 'built-in' | 'custom';
}

/**
 * The scene as the component resolved it, handed to every layer.
 *
 * Coordinates are GRAPH units. `background`, `underEdges`, `overEdges` and
 * `overNodes` are painted inside the pan/zoom transform, so anything drawn with
 * these numbers lands exactly on the nodes. `overlay` sits outside it, in
 * viewBox units, for chrome that must not zoom.
 */
export interface TopologyScene {
  nodes: GraphNode[];
  /** Only edges whose endpoints both exist. */
  edges: GraphEdge[];
  /** Final positions, including manual overrides and pins. */
  positions: Map<string, GraphPoint>;
  /** Drawn radius per node. */
  radii: Map<string, number>;
  /** Current pan and zoom. */
  viewport: TopologyViewport;
  /** The SVG viewBox, in graph units, before pan and zoom. */
  viewBox: { x: number; y: number; width: number; height: number };
  /** Container size in CSS pixels, or `null` before the first measurement. */
  size: TopologyLayoutViewport | null;
  selectedNodeId: string | null;
  hoveredNodeId: string | null;
  focusedNodeId: string | null;
}

export type TopologyLayer = (scene: TopologyScene) => ReactNode;

/**
 * Consumer SVG, injected at defined z-positions.
 *
 * Every layer is wrapped in a `<g>` that is `pointer-events: none`, so an
 * annotation can never steal a node's or an edge's hit target. Set
 * `pointer-events` on your own element to opt back in.
 */
export interface TopologyLayers {
  /** Below the dot grid. Region hulls belong here — the grid reads on top. */
  background?: TopologyLayer;
  /** Above the grid, below the edges. For heat overlays that must hide the grid. */
  underEdges?: TopologyLayer;
  /** Between edges and nodes. */
  overEdges?: TopologyLayer;
  /** Above everything in the scene, still in graph coordinates. */
  overNodes?: TopologyLayer;
  /** Outside the pan/zoom transform, in viewBox units. Does not zoom. */
  overlay?: TopologyLayer;
}

/** Everything a `renderNode` needs that it cannot derive from the node itself. */
export interface TopologyNodeState {
  /** Resolved role — `node.role`, or `'reachable'` when it was omitted. */
  role: TopologyRole;
  /** Centre, in graph units. The `<g>` is already translated here. */
  position: GraphPoint;
  /** Drawn radius, including the transit and root multipliers. */
  radius: number;
  /** Distinct neighbour count. */
  degree: number;
  transit: boolean;
  selected: boolean;
  hovered: boolean;
  focused: boolean;
  /** 1-based hop number when the node lies on `selectedPath`, else `null`. */
  pathHop: number | null;
  /** `'off'` while another node is emphasised, `'on'` when part of it. */
  emphasis: 'on' | 'off' | null;
  /** The CSS values the built-in glyph would paint with. */
  colours: { colour: string; fill: string };
  shape: TopologyRoleShape;
  dash: string | null;
  hollow: boolean;
  /** `node.label`, or a truncated id. NOT clipped to `maxLabelChars`. */
  label: string;
  /** The same text the node's `aria-label` carries. */
  accessibleName: string;
  styleSource: 'built-in' | 'custom';
}

export type TopologyNodeRenderer = (node: GraphNode, state: TopologyNodeState) => ReactNode;

/** Everything a `renderEdge` needs that it cannot derive from the edge itself. */
export interface TopologyEdgeState {
  edge: GraphEdge;
  geometry: EdgeGeometry;
  /** Resolved status — `edge.status`, or `'unknown'` when it was omitted. */
  status: TopologyEdgeStatus;
  /** Observed throughput, reduced. `kind: 'unobserved'` is NOT idle. */
  traffic: EdgeTraffic;
  /** Live stroke width in graph units. */
  width: number;
  /** Capacity ghost width, or `null` when capacity is unknown. */
  capacityWidth: number | null;
  /** Observed rate over capacity, or `null` when either is unknown. */
  utilisation: number | null;
  directed: boolean;
  /** Arrow heading in degrees, already flipped for observed reverse flow. */
  arrowAngle: number;
  arrowSize: number;
  /** Non-null when the edge lies on `selectedPath`. */
  path: { index: number; startFraction: number; fraction: number } | null;
  showLabel: boolean;
  /** What the rate label says. Already `'—'` when nothing was observed. */
  labelText: string;
  reversed: boolean;
  flowFactor: number;
  /** Perpendicular offset from the midpoint to the label anchor. */
  labelOffset: number;
  /** The CSS value the built-in stroke paints with. */
  colour: string;
  dash: string | null;
  styleSource: 'built-in' | 'custom';
  /** `'off'` while another node is emphasised, `'on'` when part of it. */
  emphasis: 'on' | 'off' | null;
  /** The same text the edge's `<title>` carries. */
  description: string;
}

/**
 * The memoised part of an edge's state.
 *
 * `emphasis` and `description` are excluded because both change with hover and
 * with text props rather than with geometry — recomputing the whole edge memo
 * on every pointer move would make a drag quadratic in the edge count.
 */
type DrawnEdge = Omit<TopologyEdgeState, 'emphasis' | 'description'>;

export type TopologyEdgeRenderer = (edge: GraphEdge, state: TopologyEdgeState) => ReactNode;

/**
 * Imperative viewport control, exposed through the `apiRef` prop.
 *
 * The component's own `ref` stays the root `<div>`, so this is a second handle
 * rather than a replacement — a graph in a resizable panel usually needs both.
 */
export interface TopologyGraphApi {
  /**
   * Returns to the identity view, which is the fit: the viewBox is derived from
   * the content on every render, so at `scale: 1` the graph already fills the
   * panel exactly.
   */
  fitToContent: () => void;
  /** Pans so a node sits at the centre of the panel. Zoom is untouched. */
  centerOn: (nodeId: string) => void;
  /** Multiplies the scale about the centre, clamped to `minScale`/`maxScale`. */
  zoomBy: (factor: number) => void;
  /** Sets an absolute scale about the centre, clamped the same way. */
  zoomTo: (scale: number) => void;
  /** Current pan and zoom. */
  getViewport: () => TopologyViewport;
  /** Moves DOM focus to a node, as an arrow key would. */
  focusNode: (nodeId: string) => void;
}

const ROLES: GraphRole[] = [
  'self',
  'reachable',
  'medium',
  'offline',
  'native',
  'nested',
  'disabled',
];

const DEFAULT_ROLE_LABELS: Record<GraphRole, string> = {
  self: 'this node',
  reachable: 'reachable',
  medium: 'partially reachable',
  offline: 'offline',
  native: 'native',
  nested: 'nested',
  disabled: 'disabled',
};

const DEFAULT_STATUS_LABELS: Record<EdgeStatus, string> = {
  ok: 'link healthy',
  degraded: 'link degraded',
  down: 'link down',
  unknown: 'link state not observed',
};

/**
 * The built-in roles, in TypeScript.
 *
 * This table exists so `renderNode` can be told what the default glyph WOULD
 * have painted. It is deliberately not the source of truth for the built-ins:
 * their paint is applied by NodeGlyph.css keyed on `data-role`, and pushing
 * these values back in as inline styles would outrank any consumer stylesheet
 * that reaches for `--_stroke`. Only a registered role is painted inline.
 */
const BUILT_IN_ROLE_STYLES: Record<GraphRole, ResolvedRoleStyle> = {
  self: {
    colour: 'var(--stratum-mesh-self)',
    fill: 'var(--stratum-mesh-self-fill)',
    shape: 'concentric',
    dash: null,
    hollow: false,
    label: null,
    source: 'built-in',
  },
  reachable: {
    colour: 'var(--stratum-mesh-reachable)',
    fill: 'var(--stratum-mesh-reachable-fill)',
    shape: 'disc',
    dash: null,
    hollow: false,
    label: null,
    source: 'built-in',
  },
  medium: {
    colour: 'var(--stratum-mesh-medium)',
    fill: 'var(--stratum-mesh-medium-fill)',
    shape: 'ring',
    dash: null,
    hollow: false,
    label: null,
    source: 'built-in',
  },
  offline: {
    colour: 'var(--stratum-mesh-offline)',
    fill: 'var(--stratum-mesh-offline-fill)',
    shape: 'cross',
    dash: null,
    hollow: false,
    label: null,
    source: 'built-in',
  },
  native: {
    colour: 'var(--stratum-mesh-native)',
    fill: 'var(--stratum-mesh-native-fill)',
    shape: 'square',
    dash: null,
    hollow: false,
    label: null,
    source: 'built-in',
  },
  nested: {
    colour: 'var(--stratum-mesh-nested)',
    fill: 'var(--stratum-mesh-nested-fill)',
    shape: 'disc',
    dash: '4 3',
    hollow: true,
    label: null,
    source: 'built-in',
  },
  disabled: {
    colour: 'var(--stratum-mesh-disabled)',
    fill: 'var(--stratum-mesh-disabled-fill)',
    shape: 'disc',
    dash: null,
    hollow: true,
    label: null,
    source: 'built-in',
  },
};

/** The built-in statuses, in TypeScript. Same caveat as the role table. */
const BUILT_IN_STATUS_STYLES: Record<EdgeStatus, ResolvedStatusStyle> = {
  ok: { colour: 'var(--stratum-mesh-edge-idle)', dash: null, label: null, source: 'built-in' },
  degraded: { colour: 'var(--stratum-mesh-medium)', dash: '9 5', label: null, source: 'built-in' },
  down: { colour: 'var(--stratum-mesh-edge-down)', dash: '3 4', label: null, source: 'built-in' },
  unknown: { colour: 'var(--stratum-mesh-edge-idle)', dash: '1 5', label: null, source: 'built-in' },
};

/**
 * Where an unregistered role or status lands.
 *
 * A hexagon and a dash-dot, because neither is used by any built-in: a typo
 * still draws something distinguishable rather than silently impersonating
 * `reachable` or `ok`.
 */
const UNKNOWN_ROLE_SHAPE: TopologyRoleShape = 'hexagon';
const UNKNOWN_STATUS_DASH = '7 3 2 3';

const DEFAULT_INSTRUCTIONS =
  'Interactive topology. Arrow keys move to a connected node. Enter or Space selects the focused node, Escape clears the selection. Hold Shift with an arrow key to move a node. Page Down and Page Up step through every node including unconnected ones. Home jumps to the root node. Plus and minus zoom, and 0 resets the view.';

const EMPTY_POSITIONS: Record<string, GraphPoint> = {};

const ZOOM_MIN = 0.35;
const ZOOM_MAX = 3.5;
const ZOOM_STEP = 1.25;

const vars = (o: Record<string, string | number>): CSSProperties => o as CSSProperties;

/** Trims a human label without pretending the rest is not there. */
function clipLabel(text: string, max: number): string {
  if (max <= 1 || text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

export interface TopologyGraphProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onSelect' | 'children'> {
  nodes: GraphNode[];
  edges: GraphEdge[];

  /* -- Layout ------------------------------------------------------------- */
  /**
   * `'radial'` roots the graph at the `self` node, `'force'` relaxes that seed,
   * `'hierarchical'` lays it out as a layered tree. Default `'radial'`.
   *
   * Also accepts any {@link TopologyLayout} — see `radialLayout`, `forceLayout`,
   * `hierarchicalLayout` and `presetLayout` for the built-ins as values. Pass a
   * stable function reference: the graph re-lays out whenever its identity
   * changes.
   */
  layout?: LayoutMode | TopologyLayout;
  /** Overrides the automatic root choice. */
  rootId?: string;
  /** Seeds the PRNG. Same seed + same data = byte-identical layout. */
  seed?: string;
  /** Minimum distance between rings, in graph units. Default 104. */
  ringGap?: number;
  /** Base node radius before the transit multiplier. Default 13. */
  nodeRadius?: number;
  /** Force-solver iterations. Automatically capped for large graphs. Default 320. */
  forceIterations?: number;

  /* -- Edge encoding ------------------------------------------------------ */
  /**
   * Rate that maps to the thickest stroke, in BYTES per second.
   * Default 12_500_000 (100 Mbps). Fixed on purpose — see {@link edgeWidth}.
   */
  rateReference?: number;
  edgeWidthMin?: number;
  edgeWidthMax?: number;
  /** `'auto'` labels edges that carry observed traffic or lie on the path. */
  rateLabels?: 'auto' | 'always' | 'never';

  /* -- Selection ---------------------------------------------------------- */
  selectedNodeId?: string | null;
  defaultSelectedNodeId?: string | null;
  onSelectNode?: (id: string | null, node: GraphNode | null) => void;
  /** Node ids in order. Drawn with a constant-duration draw-on. */
  selectedPath?: string[];
  onEdgeClick?: (edge: GraphEdge) => void;

  /* -- Manual positioning ------------------------------------------------- */
  /** Partial override map. Anything absent falls back to the computed layout. */
  positions?: Record<string, GraphPoint>;
  defaultPositions?: Record<string, GraphPoint>;
  /**
   * Fires on every pointer move during a drag and on every keyboard nudge.
   * Debounce before persisting.
   */
  onLayoutChange?: (positions: Record<string, GraphPoint>) => void;
  /** Graph units moved per Shift+Arrow press. Default 16. */
  nudgeStep?: number;

  /* -- Viewport ----------------------------------------------------------- */
  view?: TopologyView;
  defaultView?: TopologyView;
  onViewChange?: (view: TopologyView) => void;
  /**
   * The same state as `view`, spelled `scale` instead of `zoom`. Supply one or
   * the other, never both — `view` wins if you do.
   */
  viewport?: TopologyViewport;
  defaultViewport?: TopologyViewport;
  /** Fires alongside `onViewChange` on every pan, zoom and reset. */
  onViewportChange?: (viewport: TopologyViewport) => void;
  /** Lower zoom bound for wheel, pinch, keyboard and the imperative API. Default 0.35. */
  minScale?: number;
  /** Upper zoom bound. Default 3.5. */
  maxScale?: number;
  /**
   * Receives a {@link TopologyGraphApi}. The component's own `ref` is still the
   * root element, so both can be used at once.
   */
  apiRef?: Ref<TopologyGraphApi>;

  /* -- Extension ----------------------------------------------------------- */
  /**
   * Extra node roles, merged over the seven built-ins.
   *
   * Registering a key that already exists overrides that built-in. Every entry
   * should carry a `shape` or a `dash`: colour is never allowed to be the only
   * channel. See {@link TopologyRoleStyle}.
   */
  roles?: Record<string, TopologyRoleStyle>;
  /** Extra edge statuses, merged over the four built-ins. */
  statuses?: Record<string, TopologyStatusStyle>;
  /** Consumer SVG injected at defined z-positions. See {@link TopologyLayers}. */
  layers?: TopologyLayers;

  /* -- Render overrides ---------------------------------------------------- */
  /**
   * Replaces the node's mark.
   *
   * The component keeps the focus ring, the pointer target, the selection ring
   * and the whole keyboard and focus model — this swaps only what is drawn
   * inside the already-translated `<g>`, so draw around the origin, not around
   * `state.position`.
   */
  renderNode?: TopologyNodeRenderer;
  /** Replaces a node's text label. Nothing is rendered when `showLabels` is false. */
  renderNodeLabel?: TopologyNodeRenderer;
  /**
   * Replaces an edge's strokes: the capacity ghost, the live stroke, the flow
   * overlay, the path draw and the arrow. The `<title>` and the invisible
   * hit-target stroke are kept, so tooltips and `onEdgeClick` still work.
   */
  renderEdge?: TopologyEdgeRenderer;
  /** Replaces an edge's rate label. Only called when the label would be shown. */
  renderEdgeLabel?: TopologyEdgeRenderer;

  /* -- Presentation ------------------------------------------------------- */
  /** Enables drag, pan, zoom, selection and keyboard operation. Default `true`. */
  interactive?: boolean;
  showLabels?: boolean;
  /** Characters of node label kept before eliding. Default 14. */
  maxLabelChars?: number;
  showGrid?: boolean;
  showControls?: boolean;
  /** Renders the visually-hidden adjacency description. Default `true`. */
  description?: boolean;
  /** Neighbours named per node in the description before it says "and N more". */
  maxDescribedNeighbours?: number;

  /* -- Text (every string is a prop; the framework hardcodes no copy) ------ */
  label?: string;
  roleDescription?: string;
  instructions?: string;
  /** Keyed by role, including any registered through `roles`. */
  roleLabels?: Partial<Record<TopologyRole, string>>;
  /** Keyed by status, including any registered through `statuses`. */
  statusLabels?: Partial<Record<TopologyEdgeStatus, string>>;
  connectionsLabel?: (count: number) => string;
  moreLabel?: (count: number) => string;
  summaryLabel?: (nodeCount: number, edgeCount: number) => string;
  selectedLabel?: string;
  pathLabel?: (hop: number, total: number) => string;
  transitLabel?: string;
  unobservedLabel?: string;
  emptyLabel?: string;
  zoomInLabel?: string;
  zoomOutLabel?: string;
  resetViewLabel?: string;
}

/**
 * The mesh, drawn.
 *
 * WHY THIS IS BESPOKE SVG
 * -----------------------
 * See the header of `layout.ts` for why no graph library is used. The rendering
 * consequence is that every visual channel is under our control and can be held
 * to the library's rules — most importantly that a measurement which was never
 * taken is never painted as a measurement of zero.
 *
 * THE CHANNELS, AND WHAT EACH ONE MEANS
 * -------------------------------------
 * Node role      fill + stroke colour AND shape (see {@link NodeGlyph})
 * Node degree    disc radius — transit nodes are larger
 * Edge status    stroke colour AND dash pattern
 * Edge traffic   stroke width, flow animation, mid-edge rate label
 * Edge capacity  a ghost stroke behind the live one, so width reads as
 *                utilisation rather than as an absolute
 * Path           a constant-duration draw-on over the top
 *
 * Status and traffic are INDEPENDENT and are never rolled up. A link can be
 * healthy with unobserved throughput, and it must not be drawn as an idle link;
 * an idle link and an unprobed link lead to opposite operator decisions.
 *
 * WHY EDGE THICKNESS IS NOT RELATIVE
 * ----------------------------------
 * Thickness is measured against a fixed `rateReference`, not against the
 * busiest edge on screen. A relative scale silently redefines "thick" every
 * time the data changes, so the same picture means something different on every
 * poll and between two deployments. A fixed reference costs a little dynamic
 * range and buys comparability, which is the only reason to draw the graph.
 *
 * ACCESSIBILITY — THE TWO-SURFACE APPROACH
 * ----------------------------------------
 * A topology is a genuinely spatial artefact and there is no single presentation
 * that serves both a pointer user and a screen-reader user well, so this
 * component ships both:
 *
 *  1. The SVG is `role="application"` when interactive. That deliberately hands
 *     arrow keys to us rather than to the screen reader's virtual cursor, which
 *     is the only way "arrow keys walk the graph" can work. It carries an
 *     `aria-roledescription`, an `aria-label`, and `aria-describedby` pointing
 *     at the keyboard instructions. Each node is a focusable `role="button"`
 *     with an accessible name naming its role and its connections. Roving
 *     tabindex means Tab enters and leaves the graph rather than walking 200
 *     nodes.
 *  2. Outside the application region, a visually-hidden list states the same
 *     adjacency in text. This is the part a screen-reader user can browse with
 *     their normal reading keys, and it exists because `role="application"`
 *     suppresses exactly that. Turning it off with `description={false}` is
 *     supported but leaves the graph much harder to comprehend.
 *
 * When `interactive` is false the SVG is `role="img"` instead, since a static
 * diagram must not steal the virtual cursor.
 *
 * MOTION
 * ------
 * Only edges that carry observed traffic animate; a resting graph is still.
 * Flow stops entirely under `prefers-reduced-motion`, per the framework rule
 * that a perpetual pulse is precisely what that preference is for. The path
 * draw survives at half duration, because it is a one-shot state change rather
 * than a loop.
 *
 * EXTENSION — WHAT IS OPEN AND WHAT IS NOT
 * ----------------------------------------
 * See `README.md` in this folder for worked examples. Five axes are open:
 * `layout` takes any {@link TopologyLayout}; `roles` and `statuses` register new
 * vocabulary; `renderNode`, `renderNodeLabel`, `renderEdge` and `renderEdgeLabel`
 * replace what is drawn; `layers` injects SVG at five z-positions in graph
 * coordinates; and `viewport`/`onViewportChange`/`apiRef` expose pan and zoom.
 * Everything defaults to exactly the behaviour above.
 *
 * Three things are deliberately NOT open, because they are the invariants that
 * make the component worth using rather than hand-rolling:
 *
 *  1. The accessible name. `renderNode` cannot supply it — it is always built
 *     from the adjacency, so a custom mark can never produce an unnamed node.
 *     The visually-hidden adjacency list is built the same way and is likewise
 *     unaffected by any override.
 *  2. Hit testing and focus. The focus ring, the fat invisible pointer target
 *     and the roving tabindex sit outside every render prop, so an override
 *     changes what a node looks like and never whether it can be reached.
 *  3. Colour as the only channel. A registered role without a `shape` or a
 *     `dash` falls back to a hexagon and logs; every registered colour is
 *     dropped under `forced-colors`, where shape and dash carry the meaning.
 */
export const TopologyGraph = forwardRef<HTMLDivElement, TopologyGraphProps>(function TopologyGraph(
  {
    nodes: nodesProp,
    edges: edgesProp,

    layout = 'radial',
    rootId,
    seed = 'stratum',
    ringGap = 104,
    nodeRadius: nodeRadiusProp = 13,
    forceIterations = 320,

    rateReference = 12_500_000,
    edgeWidthMin = 1.4,
    edgeWidthMax = 7,
    rateLabels = 'auto',

    selectedNodeId,
    defaultSelectedNodeId = null,
    onSelectNode,
    selectedPath,
    onEdgeClick,

    positions,
    defaultPositions,
    onLayoutChange,
    nudgeStep = 16,

    view,
    defaultView,
    onViewChange,
    viewport: viewportProp,
    defaultViewport,
    onViewportChange,
    minScale = ZOOM_MIN,
    maxScale = ZOOM_MAX,
    apiRef,

    roles,
    statuses,
    layers,

    renderNode,
    renderNodeLabel,
    renderEdge,
    renderEdgeLabel,

    interactive = true,
    showLabels = true,
    maxLabelChars = 14,
    showGrid = true,
    showControls = true,
    description = true,
    maxDescribedNeighbours = 8,

    label = 'Network topology',
    roleDescription = 'network topology graph',
    instructions = DEFAULT_INSTRUCTIONS,
    roleLabels,
    statusLabels,
    connectionsLabel = (n) => (n === 1 ? '1 connection' : `${n} connections`),
    moreLabel = (n) => `and ${n} more`,
    summaryLabel = (n, e) =>
      `Topology with ${n} node${n === 1 ? '' : 's'} and ${e} link${e === 1 ? '' : 's'}.`,
    selectedLabel = 'selected',
    pathLabel = (hop, total) => `hop ${hop} of ${total} on the selected path`,
    transitLabel = 'transit node',
    unobservedLabel = 'not observed',
    emptyLabel = 'No nodes to display',
    zoomInLabel = 'Zoom in',
    zoomOutLabel = 'Zoom out',
    resetViewLabel = 'Reset view',

    className,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const gridId = `${uid}-grid`;
  const instructionsId = `${uid}-instructions`;

  const svgRef = useRef<SVGSVGElement | null>(null);
  const nodeRefs = useRef(new Map<string, SVGGElement>());

  // The container's own aspect ratio is needed to build a viewBox that fills it
  // exactly. Without it `xMidYMid meet` letterboxes a tall graph into a wide
  // panel and half the surface goes to waste.
  const { ref: measureRef, size } = useMeasure<HTMLDivElement>();
  const setRootRef = useCallback(
    (node: HTMLDivElement | null) => {
      measureRef(node);
      if (typeof ref === 'function') ref(node);
      else if (ref) (ref as { current: HTMLDivElement | null }).current = node;
    },
    [measureRef, ref],
  );

  /* -- Data ---------------------------------------------------------------- */

  const nodes = useMemo(() => {
    const seenIds = new Set<string>();
    const out: GraphNode[] = [];
    for (const n of nodesProp) {
      if (seenIds.has(n.id)) continue;
      seenIds.add(n.id);
      out.push(n);
    }
    if (import.meta.env?.DEV && out.length !== nodesProp.length) {
      console.error(
        `[stratum] <TopologyGraph> received ${nodesProp.length - out.length} duplicate node id(s). ` +
          'Only the first occurrence of each id is drawn.',
      );
    }
    return out;
  }, [nodesProp]);

  const index = useMemo(() => buildGraphIndex(nodes, edgesProp), [nodes, edgesProp]);

  if (import.meta.env?.DEV && index.danglingEdges.length > 0) {
    console.warn(
      `[stratum] <TopologyGraph> dropped ${index.danglingEdges.length} edge(s) referencing an unknown node id.`,
    );
  }

  // Only a custom layout is handed the surface size, so only a custom layout
  // re-runs when the panel is resized. The built-ins ignore it on purpose: a
  // drawing that reflows every time a split pane moves is a drawing an operator
  // can never build spatial memory of.
  const layoutViewport = useMemo<TopologyLayoutViewport | null>(
    () =>
      typeof layout === 'string' || !size
        ? null
        : { width: Math.round(size.width), height: Math.round(size.height) },
    [layout, size],
  );

  const layoutResult = useMemo(
    () =>
      computeLayout(nodes, index, {
        mode: layout,
        rootId,
        seed,
        ringGap,
        nodeRadius: nodeRadiusProp,
        forceIterations,
        viewport: layoutViewport,
      }),
    [
      nodes,
      index,
      layout,
      rootId,
      seed,
      ringGap,
      nodeRadiusProp,
      forceIterations,
      layoutViewport,
    ],
  );

  /* -- Controlled state ---------------------------------------------------- */

  const [overrides, setOverrides] = useControllableState<Record<string, GraphPoint>>({
    value: positions,
    defaultValue: defaultPositions ?? EMPTY_POSITIONS,
    onChange: onLayoutChange,
  });

  const [selected, setSelected] = useControllableState<string | null>({
    value: selectedNodeId,
    defaultValue: defaultSelectedNodeId,
    onChange: onSelectNode
      ? (id) => onSelectNode(id, id === null ? null : (nodes.find((n) => n.id === id) ?? null))
      : undefined,
  });

  if (import.meta.env?.DEV && minScale > maxScale) {
    console.error(
      `[stratum] <TopologyGraph> minScale (${minScale}) is greater than maxScale (${maxScale}). ` +
        'Zoom will be pinned to minScale.',
    );
  }

  // `view` and `viewport` are the same state under two spellings. Whichever the
  // consumer controls, both callbacks fire, so a panel can read `scale` while a
  // persisted layout store still writes `zoom`.
  const emitView = useEventCallback((next: TopologyView) => {
    onViewChange?.(next);
    onViewportChange?.({ x: next.x, y: next.y, scale: next.zoom });
  });

  const [viewport, setViewport] = useControllableState<TopologyView>({
    value:
      view ??
      (viewportProp
        ? { x: viewportProp.x, y: viewportProp.y, zoom: viewportProp.scale }
        : undefined),
    defaultValue:
      defaultView ??
      (defaultViewport
        ? { x: defaultViewport.x, y: defaultViewport.y, zoom: defaultViewport.scale }
        : { x: 0, y: 0, zoom: 1 }),
    onChange: emitView,
  });

  const [hoveredNode, setHoveredNode] = useState<string | null>(null);
  const [focusedNode, setFocusedNode] = useState<string | null>(null);

  const handleEdgeClick = useEventCallback(onEdgeClick);

  /* -- Role and status vocabulary -------------------------------------------
   * The built-ins, whatever the consumer registered on top, and a fallback for
   * anything that appears in the data but was never registered. Resolving the
   * whole vocabulary once per data change (rather than per node, per render)
   * also means the development diagnostics fire once rather than on every
   * pointer move of a drag.
   */

  const roleStyles = useMemo(() => {
    const map = new Map<string, ResolvedRoleStyle>();
    for (const [key, style] of Object.entries(BUILT_IN_ROLE_STYLES)) map.set(key, style);

    const colourOnly: string[] = [];
    for (const [key, style] of Object.entries(roles ?? {})) {
      const base = map.get(key);
      // Overriding a built-in inherits its shape, so a consumer who only wants
      // to restyle `offline` does not silently lose the cross.
      if (!base && style.shape === undefined && style.dash === undefined) colourOnly.push(key);
      map.set(key, {
        colour: style.colour,
        fill: style.fill,
        shape: style.shape ?? base?.shape ?? UNKNOWN_ROLE_SHAPE,
        dash: style.dash ?? base?.dash ?? null,
        hollow: style.hollow ?? base?.hollow ?? false,
        label: style.label ?? null,
        source: 'custom',
      });
    }

    const unregistered: string[] = [];
    for (const n of nodes) {
      const role = n.role ?? 'reachable';
      if (map.has(role)) continue;
      unregistered.push(role);
      map.set(role, {
        colour: 'var(--stratum-mesh-nested)',
        fill: 'var(--stratum-mesh-nested-fill)',
        shape: UNKNOWN_ROLE_SHAPE,
        dash: null,
        hollow: false,
        label: null,
        source: 'custom',
      });
    }

    if (import.meta.env?.DEV && colourOnly.length > 0) {
      console.error(
        `[stratum] <TopologyGraph roles> ${colourOnly.map((r) => `"${r}"`).join(', ')} ` +
          'set a colour but no `shape` or `dash`. Colour is never allowed to be the only ' +
          'channel — it is invisible under forced-colors and unreliable under deuteranopia. ' +
          'Falling back to a hexagon.',
      );
    }
    if (import.meta.env?.DEV && unregistered.length > 0) {
      console.error(
        `[stratum] <TopologyGraph> node role(s) ${unregistered.map((r) => `"${r}"`).join(', ')} ` +
          'are not built in and were not registered through the `roles` prop. Drawn as a ' +
          'hexagon in the neutral palette.',
      );
    }

    return map;
  }, [roles, nodes]);

  const statusStyles = useMemo(() => {
    const map = new Map<string, ResolvedStatusStyle>();
    for (const [key, style] of Object.entries(BUILT_IN_STATUS_STYLES)) map.set(key, style);

    const colourOnly: string[] = [];
    for (const [key, style] of Object.entries(statuses ?? {})) {
      const base = map.get(key);
      if (!base && style.dash === undefined) colourOnly.push(key);
      map.set(key, {
        colour: style.colour,
        dash: style.dash ?? base?.dash ?? UNKNOWN_STATUS_DASH,
        label: style.label ?? null,
        source: 'custom',
      });
    }

    const unregistered: string[] = [];
    for (const e of index.validEdges) {
      const status = e.status ?? 'unknown';
      if (map.has(status)) continue;
      unregistered.push(status);
      map.set(status, {
        colour: 'var(--stratum-mesh-edge-idle)',
        dash: UNKNOWN_STATUS_DASH,
        label: null,
        source: 'custom',
      });
    }

    if (import.meta.env?.DEV && colourOnly.length > 0) {
      console.error(
        `[stratum] <TopologyGraph statuses> ${colourOnly.map((s) => `"${s}"`).join(', ')} ` +
          'set a colour but no `dash`. Link state must survive forced-colors, where every ' +
          'stroke is repainted CanvasText. Falling back to a dash-dot pattern.',
      );
    }
    if (import.meta.env?.DEV && unregistered.length > 0) {
      console.error(
        `[stratum] <TopologyGraph> edge status(es) ${unregistered.map((s) => `"${s}"`).join(', ')} ` +
          'are not built in and were not registered through the `statuses` prop.',
      );
    }

    return map;
  }, [statuses, index.validEdges]);

  /* -- Resolved geometry --------------------------------------------------- */

  const resolved = useMemo(() => {
    const map = new Map<string, GraphPoint>();
    for (const n of nodes) {
      const override = overrides[n.id];
      const computed = layoutResult.positions.get(n.id);
      map.set(n.id, override ?? computed ?? { x: 0, y: 0 });
    }
    return map;
  }, [nodes, overrides, layoutResult]);

  const radii = layoutResult.radii;

  const contentBox = useMemo(() => {
    if (nodes.length === 0) return { x: 0, y: 0, w: 100, h: 100 };
    let minX = Infinity;
    let minY = Infinity;
    let maxX = -Infinity;
    let maxY = -Infinity;
    const labelSpace = showLabels ? 22 : 0;
    for (const n of nodes) {
      const p = resolved.get(n.id);
      if (!p) continue;
      const r = radii.get(n.id) ?? nodeRadiusProp;
      minX = Math.min(minX, p.x - r);
      maxX = Math.max(maxX, p.x + r);
      // Self-loops arc above; labels sit below. Both need room.
      minY = Math.min(minY, p.y - r * 4.2);
      maxY = Math.max(maxY, p.y + r + labelSpace);
    }
    if (!Number.isFinite(minX)) return { x: 0, y: 0, w: 100, h: 100 };
    const pad = 44;
    return {
      x: round2(minX - pad),
      y: round2(minY - pad),
      w: round2(Math.max(1, maxX - minX + pad * 2)),
      h: round2(Math.max(1, maxY - minY + pad * 2)),
    };
  }, [nodes, resolved, radii, nodeRadiusProp, showLabels]);

  /**
   * Widens the content box to the container's aspect ratio, centred.
   *
   * `preserveAspectRatio="xMidYMid meet"` then produces an exact fit with no
   * letterboxing, and — importantly — the pointer maths stays a straight linear
   * map, so wheel-zoom still anchors precisely under the cursor.
   */
  const viewBox = useMemo(() => {
    if (!size || size.width <= 0 || size.height <= 0) return contentBox;
    const target = size.width / size.height;
    const current = contentBox.w / contentBox.h;
    if (!Number.isFinite(target) || Math.abs(target - current) < 0.0005) return contentBox;

    if (current < target) {
      const w = contentBox.h * target;
      return {
        x: round2(contentBox.x - (w - contentBox.w) / 2),
        y: contentBox.y,
        w: round2(w),
        h: contentBox.h,
      };
    }
    const h = contentBox.w / target;
    return {
      x: contentBox.x,
      y: round2(contentBox.y - (h - contentBox.h) / 2),
      w: contentBox.w,
      h: round2(h),
    };
  }, [contentBox, size]);

  const viewBoxRef = useRef(viewBox);
  useEffect(() => {
    viewBoxRef.current = viewBox;
  }, [viewBox]);

  /* -- Selected path ------------------------------------------------------- */

  const pathInfo = useMemo(() => {
    const nodeHop = new Map<string, number>();
    const edgeHop = new Map<string, { index: number; startFraction: number; fraction: number }>();
    if (!selectedPath || selectedPath.length === 0) {
      return { nodeHop, edgeHop, total: 0, complete: true };
    }

    selectedPath.forEach((id, i) => {
      if (!nodeHop.has(id)) nodeHop.set(id, i + 1);
    });

    // Resolve each consecutive pair to a real edge; prefer the one drawn in the
    // same direction the path travels.
    const segments: Array<{ edge: GraphEdge; length: number }> = [];
    let complete = true;
    for (let i = 0; i < selectedPath.length - 1; i += 1) {
      const a = selectedPath[i]!;
      const b = selectedPath[i + 1]!;
      const forward = index.validEdges.find((e) => e.from === a && e.to === b);
      const backward = forward ?? index.validEdges.find((e) => e.from === b && e.to === a);
      if (!backward) {
        complete = false;
        continue;
      }
      const pa = resolved.get(backward.from);
      const pb = resolved.get(backward.to);
      const len = pa && pb ? Math.max(1, Math.hypot(pb.x - pa.x, pb.y - pa.y)) : 1;
      segments.push({ edge: backward, length: len });
    }

    const total = segments.reduce((s, seg) => s + seg.length, 0) || 1;
    let before = 0;
    segments.forEach((seg, i) => {
      edgeHop.set(seg.edge.id, {
        index: i,
        // THIS is the constant-total-time trick: each edge is given a slice of
        // one shared budget proportional to its own length, so five short hops
        // and one long hop finish at exactly the same instant.
        startFraction: round2(before / total),
        fraction: round2(seg.length / total),
      });
      before += seg.length;
    });

    return { nodeHop, edgeHop, total: selectedPath.length, complete };
  }, [selectedPath, index.validEdges, resolved]);

  /* -- Edges --------------------------------------------------------------- */

  const drawnEdges = useMemo<DrawnEdge[]>(() => {
    // Parallel edges between the same pair bow apart so none is hidden.
    const groups = new Map<string, number>();
    const slot = new Map<string, number>();
    for (const e of index.validEdges) {
      const key = e.from < e.to ? `${e.from}|${e.to}` : `${e.to}|${e.from}`;
      const n = groups.get(key) ?? 0;
      slot.set(e.id, n);
      groups.set(key, n + 1);
    }

    return index.validEdges.map((edge) => {
      const a = resolved.get(edge.from) ?? { x: 0, y: 0 };
      const b = resolved.get(edge.to) ?? { x: 0, y: 0 };
      const ra = radii.get(edge.from) ?? nodeRadiusProp;
      const rb = radii.get(edge.to) ?? nodeRadiusProp;

      const key = edge.from < edge.to ? `${edge.from}|${edge.to}` : `${edge.to}|${edge.from}`;
      const count = groups.get(key) ?? 1;
      const i = slot.get(edge.id) ?? 0;
      const bow = count > 1 ? (i - (count - 1) / 2) * 13 : 0;

      const traffic = edgeTraffic(edge, rateReference);
      const width = edgeWidth(traffic.intensity, edgeWidthMin, edgeWidthMax);
      const arrowInset = Math.max(4, width * 1.6);

      // Parallel edges bow apart AND their labels slide along the link, so two
      // links between the same pair never stack their rates on one another.
      const labelT = 0.5 + (count > 1 ? (i - (count - 1) / 2) * 0.22 : 0);

      const geo: EdgeGeometry =
        edge.from === edge.to
          ? selfLoopGeometry(a, ra)
          : edgeGeometry(a, b, ra, rb, bow, arrowInset, labelT);

      const status: TopologyEdgeStatus = edge.status ?? 'unknown';
      const style = statusStyles.get(status) ?? BUILT_IN_STATUS_STYLES.unknown;
      const directed = edge.directed !== false;

      // The arrow points where traffic is actually going when that has been
      // observed, and along the declared direction otherwise.
      const reversed = traffic.kind !== 'unobserved' && traffic.direction === 'backward';
      const arrowAngle = reversed ? geo.arrow.angle + 180 : geo.arrow.angle;

      const capacityWidth =
        typeof edge.capacity === 'number' && Number.isFinite(edge.capacity) && edge.capacity > 0
          ? edgeWidth(
              clamp(Math.log10(1 + edge.capacity) / Math.log10(1 + rateReference), 0, 1),
              edgeWidthMin,
              edgeWidthMax,
            )
          : null;

      const utilisation =
        capacityWidth !== null &&
        traffic.magnitude !== null &&
        typeof edge.capacity === 'number' &&
        edge.capacity > 0
          ? traffic.magnitude / edge.capacity
          : null;

      const onPath = pathInfo.edgeHop.get(edge.id);

      const showLabel =
        rateLabels === 'always' ||
        (rateLabels === 'auto' && (traffic.kind === 'active' || onPath !== undefined));

      const rateText =
        edge.label ??
        (traffic.magnitude === null ? UNOBSERVED : formatRate(traffic.magnitude, { space: ' ' }));

      // Flow period: a fraction of the component-level token, so it inherits the
      // reduced-motion multiplier instead of hardcoding milliseconds.
      const flowFactor = round2(clamp(1.75 - 1.42 * (traffic.intensity ?? 0), 0.33, 1.75));

      return {
        edge,
        geometry: geo,
        status,
        traffic,
        width,
        capacityWidth,
        utilisation,
        directed,
        arrowAngle,
        arrowSize: Math.max(3.4, width * 1.35),
        path: onPath ?? null,
        showLabel,
        labelText: rateText,
        reversed,
        flowFactor,
        labelOffset: round2(width * 0.5 + 8),
        colour: style.colour,
        dash: style.dash,
        styleSource: style.source,
      };
    });
  }, [
    index.validEdges,
    resolved,
    radii,
    nodeRadiusProp,
    rateReference,
    edgeWidthMin,
    edgeWidthMax,
    rateLabels,
    pathInfo,
    statusStyles,
  ]);

  /* -- Text ---------------------------------------------------------------- */

  // Precedence: the English defaults, then a registered style's own `label`,
  // then `roleLabels`. A registered role with neither falls back to its key,
  // which is still text — a role must never be announced as nothing at all.
  const roleText = useMemo(() => {
    const out: Record<string, string> = { ...DEFAULT_ROLE_LABELS };
    for (const [key, style] of roleStyles) if (style.label) out[key] = style.label;
    if (roleLabels) {
      for (const [key, text] of Object.entries(roleLabels)) {
        if (typeof text === 'string') out[key] = text;
      }
    }
    return out;
  }, [roleStyles, roleLabels]);

  const statusText = useMemo(() => {
    const out: Record<string, string> = { ...DEFAULT_STATUS_LABELS };
    for (const [key, style] of statusStyles) if (style.label) out[key] = style.label;
    if (statusLabels) {
      for (const [key, text] of Object.entries(statusLabels)) {
        if (typeof text === 'string') out[key] = text;
      }
    }
    return out;
  }, [statusStyles, statusLabels]);

  // A Map rather than `nodes.find`: accessible names are rebuilt on every
  // render, and a drag re-renders on every pointer move, so a linear scan per
  // node makes label generation quadratic in the node count while dragging.
  const nameById = useMemo(() => {
    const map = new Map<string, string>();
    for (const n of nodes) map.set(n.id, n.label ?? truncateId(n.id));
    return map;
  }, [nodes]);

  const nameOf = useCallback(
    (id: string): string => nameById.get(id) ?? truncateId(id),
    [nameById],
  );

  const describeNode = useCallback(
    (node: GraphNode): string => {
      const neighbours = index.neighbours.get(node.id) ?? [];
      const names = neighbours.map(nameOf);
      const shown = names.slice(0, maxDescribedNeighbours);
      const extra = names.length - shown.length;

      const role = node.role ?? 'reachable';
      // Falls back to the role key so a role a consumer registered without a
      // label still reaches assistive tech as a word rather than as silence.
      const parts: string[] = [nameOf(node.id), roleText[role] ?? role];
      if (isTransit(index.degree.get(node.id) ?? 0)) parts.push(transitLabel);
      if (node.group) parts.push(node.group);
      parts.push(
        names.length === 0
          ? connectionsLabel(0)
          : `${connectionsLabel(names.length)}: ${shown.join(', ')}${extra > 0 ? `, ${moreLabel(extra)}` : ''}`,
      );

      const hop = pathInfo.nodeHop.get(node.id);
      if (hop !== undefined && pathInfo.total > 0) parts.push(pathLabel(hop, pathInfo.total));
      if (selected === node.id) parts.push(selectedLabel);

      // The trailing stop matters: a screen reader pauses on it, and the
      // adjacency list stays readable if a consumer copies its text content.
      return `${parts.join(', ')}.`;
    },
    [
      index.neighbours,
      index.degree,
      nameOf,
      maxDescribedNeighbours,
      roleText,
      transitLabel,
      connectionsLabel,
      moreLabel,
      pathInfo,
      pathLabel,
      selected,
      selectedLabel,
    ],
  );

  const describeEdge = useCallback(
    (e: DrawnEdge): string => {
      const bits: string[] = [
        `${nameOf(e.edge.from)} ${e.directed ? '→' : '—'} ${nameOf(e.edge.to)}`,
        statusText[e.status] ?? e.status,
      ];
      if (e.traffic.kind === 'unobserved') {
        bits.push(`throughput ${unobservedLabel}`);
      } else {
        bits.push(
          `${e.traffic.direction === 'forward' ? '→' : '←'} ${formatRate(e.traffic.magnitude)}`,
        );
      }
      if (typeof e.edge.capacity === 'number' && Number.isFinite(e.edge.capacity)) {
        bits.push(`capacity ${formatBytes(e.edge.capacity, { base: 'decimal' })}/s`);
        if (e.utilisation !== null) bits.push(formatPercent(e.utilisation));
      }
      return bits.join(' · ');
    },
    [nameOf, statusText, unobservedLabel],
  );

  /* -- Emphasis ------------------------------------------------------------ */

  const activeId = hoveredNode ?? focusedNode;

  const emphasis = useMemo(() => {
    if (activeId === null) return null;
    const nodeSet = new Set<string>([activeId]);
    const edgeSet = new Set<string>();
    for (const e of index.validEdges) {
      if (e.from === activeId || e.to === activeId) {
        edgeSet.add(e.id);
        nodeSet.add(e.from);
        nodeSet.add(e.to);
      }
    }
    return { nodeSet, edgeSet };
  }, [activeId, index.validEdges]);

  /* -- Viewport arithmetic ------------------------------------------------- */

  /** Client pixels -> viewBox units, honouring `xMidYMid meet`. */
  const clientToView = useCallback((clientX: number, clientY: number): GraphPoint => {
    const svg = svgRef.current;
    const vb = viewBoxRef.current;
    if (!svg) return { x: 0, y: 0 };
    const rect = svg.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return { x: vb.x, y: vb.y };
    const scale = Math.min(rect.width / vb.w, rect.height / vb.h);
    const offsetX = (rect.width - vb.w * scale) / 2;
    const offsetY = (rect.height - vb.h * scale) / 2;
    return {
      x: vb.x + (clientX - rect.left - offsetX) / scale,
      y: vb.y + (clientY - rect.top - offsetY) / scale,
    };
  }, []);

  const viewToGraph = useCallback(
    (p: GraphPoint, v: TopologyView): GraphPoint => ({
      x: (p.x - v.x) / v.zoom,
      y: (p.y - v.y) / v.zoom,
    }),
    [],
  );

  /** Zooms about a fixed point in viewBox space, so the cursor stays anchored. */
  const zoomAbout = useCallback(
    (factor: number, anchor: GraphPoint | null) => {
      setViewport((prev) => {
        const zoom = clamp(prev.zoom * factor, minScale, maxScale);
        if (zoom === prev.zoom) return prev;
        const vb = viewBoxRef.current;
        const a = anchor ?? { x: vb.x + vb.w / 2, y: vb.y + vb.h / 2 };
        const g = { x: (a.x - prev.x) / prev.zoom, y: (a.y - prev.y) / prev.zoom };
        return { x: round2(a.x - g.x * zoom), y: round2(a.y - g.y * zoom), zoom };
      });
    },
    [setViewport, minScale, maxScale],
  );

  /** The identity view, clamped in case the consumer excluded 1 from the range. */
  const resetView = useCallback(
    () => setViewport({ x: 0, y: 0, zoom: clamp(1, minScale, maxScale) }),
    [setViewport, minScale, maxScale],
  );

  // Wheel must be attached natively: React registers `wheel` as passive at the
  // root, so `preventDefault()` from an `onWheel` prop is ignored and the page
  // scrolls out from under the graph.
  useEffect(() => {
    const svg = svgRef.current;
    if (!svg || !interactive) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      // deltaMode 1 is lines, 2 is pages. Normalise or a Firefox line-scroll
      // zooms ~40x further than the same gesture in Chrome.
      const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? 400 : 1;
      const factor = Math.exp((-event.deltaY * unit) / 420);
      zoomAbout(factor, clientToView(event.clientX, event.clientY));
    };
    svg.addEventListener('wheel', onWheel, { passive: false });
    return () => svg.removeEventListener('wheel', onWheel);
  }, [interactive, zoomAbout, clientToView]);

  /* -- Pointer interaction -------------------------------------------------- */

  type Gesture =
    | { kind: 'pan'; pointerId: number; from: GraphPoint; startView: TopologyView; moved: boolean }
    | {
        kind: 'node';
        pointerId: number;
        nodeId: string;
        offset: GraphPoint;
        moved: boolean;
      }
    | {
        kind: 'pinch';
        a: number;
        b: number;
        startDistance: number;
        startView: TopologyView;
        startMid: GraphPoint;
      };

  const gestureRef = useRef<Gesture | null>(null);
  const pointersRef = useRef(new Map<number, { x: number; y: number }>());

  const moveNodeTo = useCallback(
    (id: string, point: GraphPoint) => {
      setOverrides((prev) => ({ ...prev, [id]: { x: round2(point.x), y: round2(point.y) } }));
    },
    [setOverrides],
  );

  const beginPinch = useCallback(() => {
    const entries = [...pointersRef.current.entries()];
    const first = entries[0];
    const second = entries[1];
    if (!first || !second) return;
    const p1 = clientToView(first[1].x, first[1].y);
    const p2 = clientToView(second[1].x, second[1].y);
    gestureRef.current = {
      kind: 'pinch',
      a: first[0],
      b: second[0],
      startDistance: Math.max(1, Math.hypot(p2.x - p1.x, p2.y - p1.y)),
      startView: viewport,
      startMid: { x: (p1.x + p2.x) / 2, y: (p1.y + p2.y) / 2 },
    };
  }, [clientToView, viewport]);

  const onBackgroundPointerDown = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!interactive) return;
    pointersRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });

    if (pointersRef.current.size >= 2) {
      beginPinch();
      return;
    }

    // A pointerdown that did not land on a node pans the canvas.
    if (gestureRef.current) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    gestureRef.current = {
      kind: 'pan',
      pointerId: event.pointerId,
      from: clientToView(event.clientX, event.clientY),
      startView: viewport,
      moved: false,
    };
  };

  const onNodePointerDown = (event: ReactPointerEvent<SVGGElement>, node: GraphNode) => {
    if (!interactive) return;
    event.stopPropagation();
    pointersRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointersRef.current.size >= 2) {
      beginPinch();
      return;
    }
    const svg = svgRef.current;
    svg?.setPointerCapture(event.pointerId);

    const p = resolved.get(node.id) ?? { x: 0, y: 0 };
    const g = viewToGraph(clientToView(event.clientX, event.clientY), viewport);
    gestureRef.current = {
      kind: 'node',
      pointerId: event.pointerId,
      nodeId: node.id,
      offset: { x: p.x - g.x, y: p.y - g.y },
      moved: false,
    };
  };

  const onPointerMove = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!interactive) return;
    if (pointersRef.current.has(event.pointerId)) {
      pointersRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    }
    const gesture = gestureRef.current;
    if (!gesture) return;

    if (gesture.kind === 'pinch') {
      const pa = pointersRef.current.get(gesture.a);
      const pb = pointersRef.current.get(gesture.b);
      if (!pa || !pb) return;
      const v1 = clientToView(pa.x, pa.y);
      const v2 = clientToView(pb.x, pb.y);
      const distance = Math.max(1, Math.hypot(v2.x - v1.x, v2.y - v1.y));
      const mid = { x: (v1.x + v2.x) / 2, y: (v1.y + v2.y) / 2 };
      const zoom = clamp(
        gesture.startView.zoom * (distance / gesture.startDistance),
        minScale,
        maxScale,
      );
      const g = {
        x: (gesture.startMid.x - gesture.startView.x) / gesture.startView.zoom,
        y: (gesture.startMid.y - gesture.startView.y) / gesture.startView.zoom,
      };
      setViewport({ x: round2(mid.x - g.x * zoom), y: round2(mid.y - g.y * zoom), zoom });
      return;
    }

    if (gesture.kind === 'pan') {
      const now = clientToView(event.clientX, event.clientY);
      const dx = now.x - gesture.from.x;
      const dy = now.y - gesture.from.y;
      if (!gesture.moved && Math.hypot(dx, dy) > 2) gesture.moved = true;
      setViewport({
        x: round2(gesture.startView.x + dx),
        y: round2(gesture.startView.y + dy),
        zoom: gesture.startView.zoom,
      });
      return;
    }

    const g = viewToGraph(clientToView(event.clientX, event.clientY), viewport);
    const next = { x: g.x + gesture.offset.x, y: g.y + gesture.offset.y };
    const p = resolved.get(gesture.nodeId);
    if (!gesture.moved && p && Math.hypot(next.x - p.x, next.y - p.y) > 1.5) gesture.moved = true;
    if (gesture.moved) moveNodeTo(gesture.nodeId, next);
  };

  const endGesture = (event: ReactPointerEvent<SVGSVGElement>) => {
    pointersRef.current.delete(event.pointerId);
    const gesture = gestureRef.current;
    if (!gesture) return;

    if (gesture.kind === 'pinch') {
      if (event.pointerId === gesture.a || event.pointerId === gesture.b) gestureRef.current = null;
      return;
    }
    if (gesture.pointerId !== event.pointerId) return;

    if (gesture.kind === 'node' && !gesture.moved) {
      // A press that never moved is a click, not a drag.
      setSelected((prev) => (prev === gesture.nodeId ? null : gesture.nodeId));
      setFocusedNode(gesture.nodeId);
    } else if (gesture.kind === 'pan' && !gesture.moved) {
      setSelected(null);
    }
    gestureRef.current = null;
  };

  /* -- Keyboard ------------------------------------------------------------ */

  const focusNodeById = useCallback((id: string | undefined) => {
    if (!id) return;
    setFocusedNode(id);
    nodeRefs.current.get(id)?.focus();
  }, []);

  /**
   * Nearest connected node in a direction.
   *
   * Prefers a neighbour that genuinely lies that way (`cos > 0.25`), scoring by
   * distance divided by alignment so a slightly-off close node beats a
   * perfectly-aligned distant one. If nothing lies that way at all it still
   * moves, to the best-aligned neighbour available — a key press that does
   * nothing is indistinguishable from a broken widget.
   */
  const stepTo = useCallback(
    (from: string, dx: number, dy: number): string | undefined => {
      const p = resolved.get(from);
      if (!p) return undefined;
      const neighbours = index.neighbours.get(from) ?? [];
      let best: string | undefined;
      let bestScore = Infinity;
      let fallback: string | undefined;
      let fallbackCos = -Infinity;

      for (const nb of neighbours) {
        const q = resolved.get(nb);
        if (!q) continue;
        const vx = q.x - p.x;
        const vy = q.y - p.y;
        const len = Math.hypot(vx, vy);
        if (len < 0.001) continue;
        const cos = (vx * dx + vy * dy) / len;
        if (cos > fallbackCos) {
          fallbackCos = cos;
          fallback = nb;
        }
        if (cos <= 0.25) continue;
        const score = len / cos;
        if (score < bestScore) {
          bestScore = score;
          best = nb;
        }
      }
      return best ?? fallback;
    },
    [resolved, index.neighbours],
  );

  const onKeyDown = (event: ReactKeyboardEvent<SVGSVGElement>) => {
    if (!interactive || nodes.length === 0) return;
    const current = focusedNode ?? nodes[0]?.id;
    if (!current) return;

    const arrows: Record<string, [number, number]> = {
      ArrowUp: [0, -1],
      ArrowDown: [0, 1],
      ArrowLeft: [-1, 0],
      ArrowRight: [1, 0],
    };
    const direction = arrows[event.key];

    if (direction) {
      event.preventDefault();
      const [dx, dy] = direction;
      if (event.shiftKey) {
        // Shift+Arrow repositions the node, so layout is not a pointer-only
        // capability. Same callback as a drag.
        const p = resolved.get(current);
        if (p) moveNodeTo(current, { x: p.x + dx * nudgeStep, y: p.y + dy * nudgeStep });
        return;
      }
      focusNodeById(stepTo(current, dx, dy));
      return;
    }

    switch (event.key) {
      case 'Enter':
      case ' ':
        event.preventDefault();
        setSelected((prev) => (prev === current ? null : current));
        return;
      case 'Escape':
        if (selected !== null) {
          event.preventDefault();
          setSelected(null);
        }
        return;
      case 'Home': {
        event.preventDefault();
        focusNodeById(layoutResult.rootId ?? nodes[0]?.id);
        return;
      }
      case 'End':
        event.preventDefault();
        focusNodeById(nodes[nodes.length - 1]?.id);
        return;
      case 'PageDown':
      case 'PageUp': {
        // Steps the full node list, so a node in a disconnected component is
        // still reachable from the keyboard.
        event.preventDefault();
        const i = nodes.findIndex((n) => n.id === current);
        const delta = event.key === 'PageDown' ? 1 : -1;
        const next = nodes[(i + delta + nodes.length) % nodes.length];
        focusNodeById(next?.id);
        return;
      }
      case '+':
      case '=':
        event.preventDefault();
        zoomAbout(ZOOM_STEP, null);
        return;
      case '-':
      case '_':
        event.preventDefault();
        zoomAbout(1 / ZOOM_STEP, null);
        return;
      case '0':
        event.preventDefault();
        resetView();
        return;
      default:
    }
  };

  /* -- Imperative handle ---------------------------------------------------
   * A second handle rather than a replacement for `ref`: the root element is
   * still what `ref` gives you, because a graph in a resizable panel usually
   * needs the DOM node as well.
   *
   * Current state is read from refs rather than captured in the closure, so the
   * handle's identity survives pan and zoom. Putting `viewport` in the
   * dependency array instead would rebuild the object on every pointer move of
   * a drag, and any consumer who had stashed `apiRef.current` — the obvious
   * thing to do — would be holding a handle whose `getViewport` reported the
   * position the graph was in when they grabbed it.
   */

  const viewportRef = useRef(viewport);
  useEffect(() => {
    viewportRef.current = viewport;
  }, [viewport]);

  const resolvedRef = useRef(resolved);
  useEffect(() => {
    resolvedRef.current = resolved;
  }, [resolved]);

  useImperativeHandle<TopologyGraphApi, TopologyGraphApi>(
    apiRef,
    () => ({
      // The viewBox is rebuilt from the content on every render, so identity IS
      // the fit. There is nothing to measure.
      fitToContent: resetView,
      centerOn(nodeId) {
        const p = resolvedRef.current.get(nodeId);
        if (!p) return;
        setViewport((prev) => {
          const vb = viewBoxRef.current;
          return {
            x: round2(vb.x + vb.w / 2 - p.x * prev.zoom),
            y: round2(vb.y + vb.h / 2 - p.y * prev.zoom),
            zoom: prev.zoom,
          };
        });
      },
      zoomBy(factor) {
        zoomAbout(factor, null);
      },
      zoomTo(scale) {
        setViewport((prev) => {
          const zoom = clamp(scale, minScale, maxScale);
          if (zoom === prev.zoom) return prev;
          const vb = viewBoxRef.current;
          const a = { x: vb.x + vb.w / 2, y: vb.y + vb.h / 2 };
          const g = { x: (a.x - prev.x) / prev.zoom, y: (a.y - prev.y) / prev.zoom };
          return { x: round2(a.x - g.x * zoom), y: round2(a.y - g.y * zoom), zoom };
        });
      },
      getViewport: () => ({
        x: viewportRef.current.x,
        y: viewportRef.current.y,
        scale: viewportRef.current.zoom,
      }),
      focusNode: (nodeId) => focusNodeById(nodeId),
    }),
    [resetView, setViewport, zoomAbout, minScale, maxScale, focusNodeById],
  );

  /* -- Scene --------------------------------------------------------------- */

  const scene = useMemo<TopologyScene>(
    () => ({
      nodes,
      edges: index.validEdges,
      positions: resolved,
      radii,
      viewport: { x: viewport.x, y: viewport.y, scale: viewport.zoom },
      viewBox: { x: viewBox.x, y: viewBox.y, width: viewBox.w, height: viewBox.h },
      size: size ?? null,
      selectedNodeId: selected,
      hoveredNodeId: hoveredNode,
      focusedNodeId: focusedNode,
    }),
    [
      nodes,
      index.validEdges,
      resolved,
      radii,
      viewport,
      viewBox,
      size,
      selected,
      hoveredNode,
      focusedNode,
    ],
  );

  /* -- Render -------------------------------------------------------------- */

  const tabbableId = focusedNode ?? nodes[0]?.id;

  if (nodes.length === 0) {
    return (
      <div
        {...rest}
        ref={setRootRef}
        data-stratum="topology-graph"
        data-empty="true"
        className={clsx('stratum-topology', className)}
      >
        <p className="stratum-topology__empty">{emptyLabel}</p>
      </div>
    );
  }

  const transform = `translate(${viewport.x} ${viewport.y}) scale(${viewport.zoom})`;

  return (
    <div
      {...rest}
      ref={setRootRef}
      data-stratum="topology-graph"
      data-layout={typeof layout === 'string' ? layout : 'custom'}
      data-interactive={interactive || undefined}
      data-emphasising={activeId !== null || undefined}
      className={clsx('stratum-topology', className)}
    >
      <svg
        ref={svgRef}
        className="stratum-topology__canvas"
        viewBox={`${viewBox.x} ${viewBox.y} ${viewBox.w} ${viewBox.h}`}
        role={interactive ? 'application' : 'img'}
        aria-roledescription={roleDescription}
        aria-label={label}
        aria-describedby={interactive ? instructionsId : undefined}
        onPointerDown={onBackgroundPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endGesture}
        onPointerCancel={endGesture}
        onKeyDown={onKeyDown}
      >
        <defs>
          <pattern
            id={gridId}
            width={48}
            height={48}
            patternUnits="userSpaceOnUse"
            patternTransform={transform}
          >
            <circle className="stratum-topology__grid-dot" cx={1} cy={1} r={1} />
          </pattern>
        </defs>

        {/* Consumer layer, in graph coordinates, UNDER the grid — a region hull
            drawn here keeps the grid dots reading on top of it, which is what
            makes it read as a surface rather than as a sticker. */}
        {layers?.background && (
          <g
            className="stratum-topology__layer"
            data-layer="background"
            transform={transform}
          >
            {layers.background(scene)}
          </g>
        )}

        {showGrid && (
          <rect
            className="stratum-topology__grid"
            x={viewBox.x}
            y={viewBox.y}
            width={viewBox.w}
            height={viewBox.h}
            fill={`url(#${gridId})`}
          />
        )}

        <g className="stratum-topology__viewport" transform={transform}>
          {/* Above the grid, below the links: where a heat overlay goes. */}
          {layers?.underEdges && (
            <g className="stratum-topology__layer" data-layer="under-edges">
              {layers.underEdges(scene)}
            </g>
          )}

          {/* Edges are described in the adjacency list and in each node's
              accessible name, so the layer is hidden from assistive tech to
              avoid reading the same link twice. `<title>` still gives pointer
              users a tooltip. */}
          <g className="stratum-topology__edges" aria-hidden="true">
            {drawnEdges.map((e) => {
              const description = describeEdge(e);
              // Built only when something will read it: one object per edge per
              // render is real cost on a graph being dragged.
              const state: TopologyEdgeState | null =
                renderEdge != null || renderEdgeLabel != null
                  ? {
                      ...e,
                      emphasis: emphasis
                        ? emphasis.edgeSet.has(e.edge.id)
                          ? 'on'
                          : 'off'
                        : null,
                      description,
                    }
                  : null;

              return (
                <g
                  key={e.edge.id}
                  className="stratum-topology__edge-group"
                  data-status={e.status}
                  data-traffic={e.traffic.kind}
                  data-on-path={e.path !== null || undefined}
                  data-emphasis={
                    emphasis ? (emphasis.edgeSet.has(e.edge.id) ? 'on' : 'off') : undefined
                  }
                  data-flow-direction={e.reversed ? 'reverse' : undefined}
                  style={vars({
                    '--_edge-width': e.width,
                    '--_flow-duration': `calc(var(--stratum-topology-flow-period) * ${e.flowFactor})`,
                    // Only a registered status paints from here. A built-in one
                    // resolves through the stylesheet instead, so a consumer's
                    // own CSS can still reach it — and so forced-colors can
                    // still take the colour back.
                    ...(e.styleSource === 'custom'
                      ? {
                          '--_status-colour': e.colour,
                          '--_status-dash': e.dash ?? 'none',
                        }
                      : {}),
                  })}
                >
                  <title>{description}</title>

                  {renderEdge && state ? (
                    renderEdge(e.edge, state)
                  ) : (
                    <>
                      {/* Capacity ghost. Width encodes what the link could
                          carry, so the live stroke on top reads as utilisation
                          rather than as a bare number. */}
                      {e.capacityWidth !== null && (
                        <path
                          className="stratum-topology__edge-ghost"
                          d={e.geometry.d}
                          style={vars({ '--_ghost-width': e.capacityWidth })}
                        />
                      )}

                      <path className="stratum-topology__edge" d={e.geometry.d} />

                      {e.traffic.kind === 'active' && (
                        <path className="stratum-topology__edge-flow" d={e.geometry.d} />
                      )}

                      {e.path !== null && (
                        <path
                          className="stratum-topology__edge-path"
                          d={e.geometry.d}
                          data-path-index={e.path.index}
                          style={vars({
                            '--stratum-path-len': e.geometry.length,
                            '--_draw-duration': `calc(var(--stratum-topology-draw-total) * ${e.path.fraction})`,
                            '--_draw-delay': `calc(var(--stratum-topology-draw-total) * ${e.path.startFraction})`,
                          })}
                        />
                      )}

                      {e.directed && (
                        <path
                          className="stratum-topology__edge-arrow"
                          d={arrowPath(
                            e.geometry.arrow.x,
                            e.geometry.arrow.y,
                            e.arrowAngle,
                            e.arrowSize,
                          )}
                        />
                      )}
                    </>
                  )}

                  {e.showLabel &&
                    (renderEdgeLabel && state ? (
                      renderEdgeLabel(e.edge, state)
                    ) : (
                      <text
                        className="stratum-topology__edge-label"
                        // Never rotated. A label aligned to a steep edge reads
                        // bottom-to-top, and an operator scanning a panel should
                        // not have to tilt their head to read a rate.
                        x={round2(e.geometry.mid.x + e.geometry.normal.x * e.labelOffset)}
                        y={round2(e.geometry.mid.y + e.geometry.normal.y * e.labelOffset)}
                        dy="0.32em"
                      >
                        {e.traffic.kind === 'unobserved'
                          ? e.labelText
                          : `${e.traffic.direction === 'forward' ? '→' : '←'} ${e.labelText}`}
                      </text>
                    ))}

                  {/* Fat transparent stroke: an 8px-wide pointer target on a
                      1.4px line, without inflating the visible geometry. Kept
                      outside `renderEdge` on purpose — an override changes what
                      an edge looks like, never whether it can be clicked. */}
                  <path
                    className="stratum-topology__edge-hit"
                    d={e.geometry.d}
                    onClick={() => handleEdgeClick(e.edge)}
                  />
                </g>
              );
            })}
          </g>

          {layers?.overEdges && (
            <g className="stratum-topology__layer" data-layer="over-edges">
              {layers.overEdges(scene)}
            </g>
          )}

          <g className="stratum-topology__nodes">
            {nodes.map((node) => {
              const p = resolved.get(node.id) ?? { x: 0, y: 0 };
              const r = radii.get(node.id) ?? nodeRadiusProp;
              const degree = index.degree.get(node.id) ?? 0;
              const role = node.role ?? 'reachable';
              const style = roleStyles.get(role) ?? BUILT_IN_ROLE_STYLES.reachable;
              const isSelected = selected === node.id;
              const hop = pathInfo.nodeHop.get(node.id);
              const raw = node.label ?? truncateId(node.id);
              const wantsState = renderNode != null || renderNodeLabel != null;
              // Built once, used twice. Skipped entirely for a static graph
              // with no overrides, where nothing consumes it.
              const accessibleName =
                interactive || wantsState ? describeNode(node) : '';

              const state: TopologyNodeState | null = wantsState
                ? {
                    role,
                    position: p,
                    radius: r,
                    degree,
                    transit: isTransit(degree),
                    selected: isSelected,
                    hovered: hoveredNode === node.id,
                    focused: focusedNode === node.id,
                    pathHop: hop ?? null,
                    emphasis: emphasis
                      ? emphasis.nodeSet.has(node.id)
                        ? 'on'
                        : 'off'
                      : null,
                    colours: { colour: style.colour, fill: style.fill },
                    shape: style.shape,
                    dash: style.dash,
                    hollow: style.hollow,
                    label: raw,
                    accessibleName,
                    styleSource: style.source,
                  }
                : null;

              return (
                <g
                  key={node.id}
                  ref={(el) => {
                    if (el) nodeRefs.current.set(node.id, el);
                    else nodeRefs.current.delete(node.id);
                  }}
                  className="stratum-topology__node"
                  data-role={role}
                  data-transit={isTransit(degree) || undefined}
                  data-selected={isSelected || undefined}
                  data-on-path={hop !== undefined || undefined}
                  data-emphasis={
                    emphasis ? (emphasis.nodeSet.has(node.id) ? 'on' : 'off') : undefined
                  }
                  transform={`translate(${p.x} ${p.y})`}
                  tabIndex={interactive ? (node.id === tabbableId ? 0 : -1) : undefined}
                  role={interactive ? 'button' : undefined}
                  // Built here, never by `renderNode`. An override changes what
                  // a node looks like; it must not be able to change whether it
                  // has a name.
                  aria-label={interactive ? accessibleName : undefined}
                  aria-pressed={interactive ? isSelected : undefined}
                  onPointerDown={(event) => onNodePointerDown(event, node)}
                  onPointerEnter={() => setHoveredNode(node.id)}
                  onPointerLeave={() =>
                    setHoveredNode((prev) => (prev === node.id ? null : prev))
                  }
                  onFocus={() => setFocusedNode(node.id)}
                  onBlur={() => setFocusedNode((prev) => (prev === node.id ? null : prev))}
                >
                  {/* Drawn rather than relying on `outline`, which several
                      engines still clip or refuse to render on an SVG group.
                      Both of these stay outside `renderNode`: focus and hit
                      testing are the component's job whoever draws the mark. */}
                  <circle className="stratum-topology__node-focus" r={r + 6} />
                  <circle className="stratum-topology__node-hit" r={r + 7} />

                  {renderNode && state ? (
                    renderNode(node, state)
                  ) : (
                    <NodeGlyph
                      role={role}
                      r={r}
                      transit={isTransit(degree)}
                      // A built-in role passes nothing but its key: its paint
                      // and its shape are the stylesheet's, where a consumer's
                      // own CSS can still override them.
                      {...(style.source === 'custom'
                        ? {
                            shape: style.shape,
                            colour: style.colour,
                            fill: style.fill,
                            hollow: style.hollow,
                            ...(style.dash !== null ? { dash: style.dash } : {}),
                          }
                        : {})}
                    />
                  )}

                  {isSelected && (
                    <circle className="stratum-topology__node-selected" r={r + 4.5} />
                  )}

                  {showLabels &&
                    (renderNodeLabel && state ? (
                      renderNodeLabel(node, state)
                    ) : (
                      <text className="stratum-topology__node-label" y={r + 15}>
                        {clipLabel(raw, maxLabelChars)}
                      </text>
                    ))}
                </g>
              );
            })}
          </g>

          {layers?.overNodes && (
            <g className="stratum-topology__layer" data-layer="over-nodes">
              {layers.overNodes(scene)}
            </g>
          )}
        </g>

        {/* Outside the pan/zoom transform: viewBox units, so an in-canvas
            legend or a scale bar keeps its size as the operator zooms. */}
        {layers?.overlay && (
          <g className="stratum-topology__layer" data-layer="overlay">
            {layers.overlay(scene)}
          </g>
        )}
      </svg>

      {interactive && (
        <div className="stratum-visually-hidden" id={instructionsId}>
          {instructions}
        </div>
      )}

      {/* The browsable alternative. Sits OUTSIDE the application region on
          purpose: `role="application"` suppresses the virtual cursor, and this
          is what a screen-reader user reads instead. */}
      {description && (
        <div className="stratum-visually-hidden">
          <p>{summaryLabel(nodes.length, index.validEdges.length)}</p>
          <ul>
            {nodes.map((node) => (
              <li key={node.id}>{describeNode(node)}</li>
            ))}
          </ul>
        </div>
      )}

      {interactive && showControls && (
        <div className="stratum-topology__controls">
          <button
            type="button"
            className="stratum-topology__control"
            aria-label={zoomInLabel}
            title={zoomInLabel}
            onClick={() => zoomAbout(ZOOM_STEP, null)}
          >
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
              <path d="M8 3.5v9M3.5 8h9" />
            </svg>
          </button>
          <button
            type="button"
            className="stratum-topology__control"
            aria-label={zoomOutLabel}
            title={zoomOutLabel}
            onClick={() => zoomAbout(1 / ZOOM_STEP, null)}
          >
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
              <path d="M3.5 8h9" />
            </svg>
          </button>
          <button
            type="button"
            className="stratum-topology__control"
            aria-label={resetViewLabel}
            title={resetViewLabel}
            onClick={() => {
              resetView();
              setOverrides({});
            }}
          >
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              <path d="M13 8a5 5 0 1 1-1.6-3.7M13 2.5V5h-2.5" />
            </svg>
          </button>
        </div>
      )}
    </div>
  );
});

/** Every role the graph knows about, in legend order. */
export const TOPOLOGY_ROLES: readonly GraphRole[] = ROLES;
export { DEFAULT_ROLE_LABELS as TOPOLOGY_ROLE_LABELS };
