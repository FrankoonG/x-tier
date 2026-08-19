# TopologyGraph — extension points

Everything here is opt-in. `<TopologyGraph nodes edges />` behaves exactly as it
always has; nothing below changes a default.

Five axes are open:

| Axis | Prop |
|---|---|
| Layout | `layout` |
| Rendering | `renderNode`, `renderNodeLabel`, `renderEdge`, `renderEdgeLabel` |
| Scene injection | `layers` |
| Pan and zoom | `viewport`, `onViewportChange`, `minScale`, `maxScale`, `apiRef` |
| Vocabulary | `roles`, `statuses` |

Three things are deliberately **not** open, because they are the reason to use
the component instead of hand-rolling an SVG:

1. **The accessible name.** Always built from the adjacency, never from a render
   prop, so a custom mark cannot produce an unnamed node. The visually-hidden
   adjacency list is built the same way and no override touches it.
2. **Hit testing and focus.** The focus ring, the fat invisible pointer target
   and the roving tabindex sit outside every render prop. An override changes
   what a node looks like, never whether it can be reached.
3. **Colour as the only channel.** A registered role without a `shape` or a
   `dash` falls back to a hexagon and logs in development, and every registered
   colour is dropped under `forced-colors`, where shape and dash carry the
   meaning instead.

---

## 1. Layout

A layout is pure geometry:

```ts
type TopologyLayout = (input: TopologyLayoutInput) => Map<string, GraphPoint>;
```

`input` carries `nodes`, `edges`, `viewport` (container size in CSS pixels, or
`null` before the first measurement) and `seed`, plus the precomputed work a
layout would otherwise redo: `index` (adjacency and degree), `components`
(connected components, primary first, each with its root), `radii`, `nodeRadius`,
`ringGap`, `rootId`, `forceIterations`, and `random` — a PRNG seeded from `seed`.

Return one entry per node, in graph units, centred anywhere. The component
derives its own viewBox from the result, then applies `node.fixed` pins and
disc-overlap resolution, so a layout never has to think about either. Ids you
omit fall back to the origin and non-finite values are treated as missing, so a
layout that misbehaves degrades instead of corrupting the drawing.

The built-ins are exported as values as well as names:

```tsx
import { radialLayout, forceLayout, hierarchicalLayout, presetLayout } from '@stratum/ui';

<TopologyGraph nodes={nodes} edges={edges} layout="hierarchical" />
<TopologyGraph nodes={nodes} edges={edges} layout={hierarchicalLayout} />
```

### Custom layout: columns by tier

Note the two rules: use `input.random` rather than `Math.random`, and define the
layout outside render so its identity is stable — the graph re-lays out whenever
the `layout` prop changes identity.

```tsx
import { useMemo } from 'react';
import { TopologyGraph, type TopologyLayout, type GraphPoint } from '@stratum/ui';

/** One column per `group`, sorted by group name, nodes stacked within it. */
const byTier: TopologyLayout = ({ nodes, ringGap, nodeRadius, random }) => {
  const columns = new Map<string, string[]>();
  for (const node of nodes) {
    const key = node.group ?? 'ungrouped';
    const bucket = columns.get(key);
    if (bucket) bucket.push(node.id);
    else columns.set(key, [node.id]);
  }

  const out = new Map<string, GraphPoint>();
  const keys = [...columns.keys()].sort();
  keys.forEach((key, column) => {
    const ids = columns.get(key) ?? [];
    ids.forEach((id, row) => {
      out.set(id, {
        x: column * ringGap * 1.6,
        // A seeded jitter, so two identical columns do not read as one shape.
        // `random` keeps this reproducible; Math.random would not.
        y: row * nodeRadius * 3.4 + (random() - 0.5) * 4,
      });
    });
  });
  return out;
};

<TopologyGraph nodes={nodes} edges={edges} layout={byTier} seed="tier-view" />;
```

### Saved coordinates

`presetLayout` uses coordinates you already have. Anything absent is placed by a
fallback (radial by default), so adding one node to a saved arrangement does not
drop it at the origin under everything else.

```tsx
const saved = { 'sg-home': { x: 0, y: 0 }, 'edge-ams-01': { x: 180, y: -60 } };

function Panel() {
  // Memoised: presetLayout returns a fresh function on every call.
  const layout = useMemo(() => presetLayout(saved), []);
  return <TopologyGraph nodes={nodes} edges={edges} layout={layout} />;
}
```

To make the arrangement editable and persistable, combine it with the existing
`positions` / `onLayoutChange` pair — drag and `Shift`+arrow both write there.

---

## 2. Render overrides

Each render prop replaces one piece and leaves the rest of the component's
machinery in place.

| Prop | Replaces | Keeps |
|---|---|---|
| `renderNode` | the node's mark | focus ring, pointer target, selection ring, label |
| `renderNodeLabel` | the node's `<text>` | everything else |
| `renderEdge` | ghost, stroke, flow, path draw, arrow | `<title>`, hit target, label |
| `renderEdgeLabel` | the rate `<text>` | everything else |

The node `<g>` is already translated to the node's position, so **draw around
the origin**, not around `state.position`.

```tsx
<TopologyGraph
  nodes={nodes}
  edges={edges}
  renderNode={(node, state) => (
    <g>
      <rect
        x={-state.radius * 1.6}
        y={-state.radius}
        width={state.radius * 3.2}
        height={state.radius * 2}
        rx={3}
        fill={state.colours.fill}
        stroke={state.colours.colour}
        strokeWidth={state.transit ? 2.6 : 2}
        strokeDasharray={state.dash ?? undefined}
      />
      {state.pathHop !== null && (
        <text textAnchor="middle" dy="0.32em" fontSize={9}>
          {state.pathHop}
        </text>
      )}
    </g>
  )}
/>
```

`state` carries the resolved `role`, `position`, `radius`, `degree`, `transit`,
the `selected` / `hovered` / `focused` flags, `pathHop`, `emphasis`, the resolved
`colours`, `shape`, `dash` and `hollow`, the untruncated `label`, and
`accessibleName` — the same string the node's `aria-label` carries, so a custom
mark can reuse it in a `<title>` without inventing its own wording.

`renderEdge` receives the full geometry (`state.geometry.d`, `.length`, `.mid`,
`.normal`, `.arrow`), the traffic reduction, `width`, `capacityWidth`,
`utilisation`, `path`, `colour`, `dash` and `description`. Remember the
component's one hard rule about traffic: `state.traffic.kind === 'unobserved'`
means *never measured*, and must not be drawn as an idle link.

---

## 3. Layers

Layers receive the resolved scene and return SVG. There are five z-positions:

| Slot | Coordinates | Sits |
|---|---|---|
| `background` | graph | below the dot grid — region hulls, so the grid reads on top |
| `underEdges` | graph | above the grid, below the links — heat overlays |
| `overEdges` | graph | between links and nodes |
| `overNodes` | graph | above the scene |
| `overlay` | viewBox | outside pan/zoom — chrome that must not scale |

Every layer is wrapped in a `pointer-events: none` group, so an annotation can
never take a node's or an edge's hit target away. Set `pointer-events` on your
own element to opt back in.

```tsx
<TopologyGraph
  nodes={nodes}
  edges={edges}
  layers={{
    // A dashed hull around one region, in graph coordinates.
    background: ({ nodes, positions, radii }) => {
      const region = nodes.filter((n) => n.group === 'eu-west');
      if (region.length === 0) return null;
      const xs = region.map((n) => positions.get(n.id)?.x ?? 0);
      const ys = region.map((n) => positions.get(n.id)?.y ?? 0);
      const pad = Math.max(...region.map((n) => radii.get(n.id) ?? 0)) + 24;
      return (
        <rect
          x={Math.min(...xs) - pad}
          y={Math.min(...ys) - pad}
          width={Math.max(...xs) - Math.min(...xs) + pad * 2}
          height={Math.max(...ys) - Math.min(...ys) + pad * 2}
          rx={12}
          fill="none"
          stroke="var(--stratum-border-strong)"
          strokeDasharray="6 4"
        />
      );
    },

    // A scale readout that does not grow as the operator zooms.
    overlay: ({ viewBox, viewport }) => (
      <text
        x={viewBox.x + 12}
        y={viewBox.y + 20}
        fill="var(--stratum-text-muted)"
        fontSize="var(--stratum-text-2xs)"
      >
        {`${Math.round(viewport.scale * 100)}%`}
      </text>
    ),
  }}
/>
```

The scene also carries `viewBox`, `size` (container pixels, or `null`),
`selectedNodeId`, `hoveredNodeId` and `focusedNodeId`, so a layer can react to
selection and hover without duplicating state.

---

## 4. Viewport

`view` / `onViewChange` (`zoom`) and `viewport` / `onViewportChange` (`scale`)
are the same state under two spellings. Control one or the other; both callbacks
fire either way.

```tsx
const [viewport, setViewport] = useState({ x: 0, y: 0, scale: 1 });
const api = useRef<TopologyGraphApi>(null);

<>
  <TopologyGraph
    nodes={nodes}
    edges={edges}
    viewport={viewport}
    onViewportChange={setViewport}
    minScale={0.5}
    maxScale={4}
    apiRef={api}
  />
  <Button onClick={() => api.current?.fitToContent()}>Fit</Button>
  <Button onClick={() => api.current?.centerOn('sg-home')}>Find me</Button>
  <Button onClick={() => api.current?.zoomBy(1.25)}>Zoom in</Button>
</>
```

`apiRef` is a second handle, not a replacement — the component's own `ref` is
still the root `<div>`. Its identity is stable across pan and zoom, so it is
safe to stash `apiRef.current`. It exposes `fitToContent()`, `centerOn(nodeId)`,
`zoomBy(factor)`, `zoomTo(scale)`, `getViewport()` and `focusNode(nodeId)`.

`fitToContent()` returns to the identity view: the viewBox is rebuilt from the
content on every render, so at `scale: 1` the graph already fills the panel.

---

## 5. Roles and statuses

`roles` and `statuses` merge over the built-ins. Registering an existing key
overrides that built-in and inherits anything you leave out — restyling
`offline` does not silently lose the cross.

```tsx
<TopologyGraph
  nodes={nodes}
  edges={edges}
  roles={{
    quarantined: {
      colour: 'var(--my-quarantine)',
      fill: 'var(--my-quarantine-wash)',
      shape: 'triangle',   // the non-colour channel — required in practice
      label: 'quarantined',
    },
    'read-only': {
      colour: 'var(--my-readonly)',
      fill: 'var(--my-readonly-wash)',
      shape: 'diamond',
      dash: '3 2',
      hollow: true,
      label: 'read only',
    },
  }}
  statuses={{
    throttled: { colour: 'var(--my-throttle)', dash: '2 2', label: 'link throttled' },
  }}
/>
```

Shapes: `disc`, `concentric`, `ring`, `cross`, `square`, `diamond`, `triangle`,
`hexagon`. The first five are what the built-in roles use; the last three exist
for registered roles, so a new role never has to impersonate an existing one.

`colour` is **graphics-grade** — it is a fill and a stroke, so it needs 3:1
against the canvas (SC 1.4.11), not the 4.5:1 that text would need. `label` is
what assistive tech announces; without one, the role key itself is used, so a
registered role is never announced as nothing.

A registered role's colours are applied through a private custom property rather
than written straight onto the stroke, which is what lets `forced-colors` take
them back: there, every role collapses to `CanvasText`/`Canvas` and the shape
and dash carry the meaning. That is also why a role registered with neither a
`shape` nor a `dash` is rejected onto a hexagon and logged — under forced
colours it would otherwise be indistinguishable from every other role.

### Keeping the legend in step

`TopologyLegend` takes the same maps, so the key cannot drift from the diagram:

```tsx
const roles = {
  quarantined: { colour: 'var(--my-quarantine)', fill: 'var(--my-quarantine-wash)', shape: 'triangle', label: 'quarantined' },
} as const;

<TopologyGraph nodes={nodes} edges={edges} roles={roles} />
<TopologyLegend roles={['self', 'reachable', 'quarantined']} roleStyles={roles} />
```

A role listed in the legend but absent from `roleStyles` — and not one of the
seven built-ins — draws as a hexagon, exactly as the graph draws it. For a row
the legend does not model at all, `<NodeGlyph>` takes `shape`, `colour`, `fill`,
`dash` and `hollow` directly.
