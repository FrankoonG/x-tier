/* ===========================================================================
 * THE TOPOLOGY, AS THE RESOLVER SEES IT
 *
 * A port of internal/localview/localview.go and the edge lookups in
 * internal/route/types.go. It exists so the editor can answer "is this hop
 * legal" without a round trip, and so the answer agrees with the daemon.
 *
 * The daemon is authoritative. This is a mirror, and a mirror that drifts is
 * worse than no mirror — so every rule below cites the line it came from, and
 * the two places where the obvious model is WRONG are called out.
 *
 * WRONG MODEL #1: "edges go parent to child"
 * ------------------------------------------
 * `addPeers` only ever appends parent->child edges, so it is tempting to model
 * reachability as descent through the address book. `Topology.DialEdge`
 * (types.go:125-133) then does this:
 *
 *     if e, ok := t.Edge(from, to); ok && e.Direction.CanDialOutbound()  -> e
 *     if e, ok := t.Edge(to, from); ok && e.Direction.CanAcceptInbound() -> reverse(e)
 *
 * The second clause means a CHILD -> PARENT hop is dialable whenever the
 * parent->child edge is inbound or bidirectional, and `reverseDialEdge`
 * inherits `NestedEnabled` and `Enabled` from that reverse edge. A picker that
 * offers only the children of the previous hop produces false negatives.
 *
 * WRONG MODEL #2: "a peer is disabled, so the node is disabled"
 * ------------------------------------------------------------
 * `addPeers` builds peer nodes with {ID, DisplayName, RendrCapable, InstanceID}
 * and nothing else (localview.go:29-35). `Disabled` is only ever set on the
 * LOCAL node. A disabled peer therefore surfaces as `path.edge_disabled`, never
 * `path.node_disabled` — the flag lives on the link, and so must the message.
 * ======================================================================== */
import type { Direction, PeerConfig } from '../api/types';

export interface TopoNode {
  id: string;
  displayName: string;
  rendrCapable: boolean;
  instanceId: string;
  disabled: boolean;
  disabledCause: string;
}

export interface TopoEdge {
  from: string;
  to: string;
  peerName: string;
  direction: Direction;
  xrayProfileId: string;
  gatewayAddr: string;
  nestedEnabled: boolean;
  enabled: boolean;
  disabledCause: string;
}

export interface Topology {
  local: string;
  nodes: Map<string, TopoNode>;
  /** Depth-first append order. `edgeBetween` returns the FIRST match, as Go does. */
  edges: TopoEdge[];
}

export const canDialOutbound = (d: Direction | undefined): boolean =>
  d === 'outbound' || d === 'bidirectional';

export const canAcceptInbound = (d: Direction | undefined): boolean =>
  d === 'inbound' || d === 'bidirectional';

export interface LocalNodeInput {
  node_id?: string;
  display_name?: string;
  rendr_capable?: boolean;
  rendr_instance_id?: string;
  disabled?: boolean;
  disabled_cause?: string;
}

/**
 * localview.TopologyFromConfig.
 *
 * Note that `AddNode` OVERWRITES by id while `AddEdge` APPENDS. A node listed
 * at two places in the tree — which is exactly how "reachable directly or via a
 * relay" is expressed — collapses to one node and two edges. Do not dedupe the
 * edges; their order is observable through first-match lookup.
 */
export function buildTopology(node: LocalNodeInput | null, peers: PeerConfig[] | null): Topology {
  const nodes = new Map<string, TopoNode>();
  const edges: TopoEdge[] = [];
  const local = node?.node_id ?? '';

  if (local) {
    nodes.set(local, {
      id: local,
      displayName: node?.display_name ?? '',
      rendrCapable: !!node?.rendr_capable,
      instanceId: node?.rendr_instance_id ?? '',
      disabled: !!node?.disabled,
      disabledCause: node?.disabled_cause ?? '',
    });
  }

  const walk = (parent: string, list: PeerConfig[] | null | undefined): void => {
    for (const p of list ?? []) {
      const id = p.node_id ?? '';
      if (!id) continue;
      nodes.set(id, {
        id,
        displayName: p.display_name ?? '',
        rendrCapable: !!p.rendr_capable,
        instanceId: p.rendr_instance_id ?? '',
        // Never copied from the peer. See WRONG MODEL #2 above.
        disabled: false,
        disabledCause: '',
      });
      if (parent) {
        edges.push({
          from: parent,
          to: id,
          peerName: p.name ?? '',
          direction: p.direction,
          xrayProfileId: p.xray_profile_id ?? '',
          gatewayAddr: p.gateway_addr || p.addr || '',
          nestedEnabled: !!p.nested_enabled,
          enabled: !!p.enabled,
          disabledCause: p.disabled_cause ?? '',
        });
      }
      walk(id, p.children);
    }
  };

  walk(local, peers);
  return { local, nodes, edges };
}

/** Topology.Edge — FIRST match wins, and duplicates are legal. */
export const edgeBetween = (t: Topology, from: string, to: string): TopoEdge | null =>
  t.edges.find((e) => e.from === from && e.to === to) ?? null;

/** route.reverseDialEdge */
function reverseDialEdge(e: TopoEdge): TopoEdge {
  return {
    ...e,
    from: e.to,
    to: e.from,
    peerName: e.from,
    direction: e.direction === 'inbound' ? 'outbound' : e.direction,
  };
}

export interface DialResult {
  edge: TopoEdge;
  /** True when the edge was found in the opposite orientation and flipped. */
  reversed: boolean;
}

/** Topology.DialEdge, including the reverse fallback that the panel used to miss. */
export function dialEdge(t: Topology, from: string, to: string): DialResult | null {
  const forward = edgeBetween(t, from, to);
  if (forward && canDialOutbound(forward.direction)) return { edge: forward, reversed: false };
  const backward = edgeBetween(t, to, from);
  if (backward && canAcceptInbound(backward.direction)) {
    return { edge: reverseDialEdge(backward), reversed: true };
  }
  return null;
}

export interface HopCandidate {
  id: string;
  displayName: string;
  /** Reachable from the given origin at all. */
  dialable: boolean;
  /** Legal as a NON-final hop — i.e. the dialable edge permits nested expansion. */
  transitable: boolean;
  rendrCapable: boolean;
  /** Why it cannot be dialled, or cannot transit. Empty when it can do both. */
  reason: string;
  reversed: boolean;
}

/**
 * Everything reachable in one hop from `origin`, annotated with what it may be
 * used for. Nothing is filtered out: a peer that vanishes from a picker teaches
 * the operator nothing, while a peer shown as "destination only" teaches them
 * the rule.
 */
export function hopCandidates(t: Topology, origin: string, exclude: Set<string>): HopCandidate[] {
  const out: HopCandidate[] = [];
  for (const node of t.nodes.values()) {
    if (node.id === origin || node.id === t.local || exclude.has(node.id)) continue;
    const dial = dialEdge(t, origin, node.id);
    if (!dial) {
      const exact = edgeBetween(t, origin, node.id);
      if (!exact) continue; // Not adjacent at all — not a candidate, not an error.
      out.push({
        id: node.id,
        displayName: node.displayName,
        dialable: false,
        transitable: false,
        rendrCapable: node.rendrCapable,
        reason: `The link is ${exact.direction}, so this node cannot be dialled from here.`,
        reversed: false,
      });
      continue;
    }
    const enabled = dial.edge.enabled;
    out.push({
      id: node.id,
      displayName: node.displayName,
      dialable: enabled,
      transitable: enabled && dial.edge.nestedEnabled,
      rendrCapable: node.rendrCapable,
      reason: !enabled
        ? `The link is disabled${dial.edge.disabledCause ? `: ${dial.edge.disabledCause}` : ''}.`
        : dial.edge.nestedEnabled
          ? ''
          : 'The link is not enabled for nested expansion, so this can only be the last hop.',
      reversed: dial.reversed,
    });
  }
  out.sort((a, b) => a.id.localeCompare(b.id));
  return out;
}
