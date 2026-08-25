/* ===========================================================================
 * THE LOCAL MIRROR
 *
 * Runs the resolver's checks against the address book the panel already holds,
 * so the editor can answer before the network does.
 *
 * WHY THIS EARNS ITS KEEP, rather than being a nicety
 * ---------------------------------------------------
 * `ResolveIntent` returns on the FIRST failing path (compiler.go:19-27):
 *
 *     for _, expr := range intent.Paths {
 *         rp, err := ResolvePath(t, expr, endpoint)
 *         if err != nil { return nil, err }      // <- stops here
 *         out = append(out, rp)
 *     }
 *
 * So one compile reports exactly one error about exactly one path, and says
 * nothing at all about the others. Fix it, compile again, learn about the next
 * one. That is not a limitation worth working around with extra round trips —
 * it is structural, and the mirror simply does not have it: it reports every
 * finding in every path in one pass.
 *
 * The mirror is ADVISORY. The daemon is authoritative and its answer always
 * wins. Where the two disagree, that is a defect here, and the editor says so
 * rather than arguing.
 *
 * WHAT IS DELIBERATELY NOT IMPLEMENTED, because it cannot fire
 * -----------------------------------------------------------
 *   path.node_disabled              peer nodes never carry Disabled (see topology.ts)
 *   path.empty                      splitCSV drops empty segments before the resolver
 *   route.*_session_kind_mismatch   session kind derives from the single intent-level
 *                                   endpoint and is stamped identically on every path
 *   route.*_instance_mismatch       the terminal check returns first, so by the time
 *                                   instances are compared all terminals are equal,
 *                                   hence the same node, hence the same instance
 *
 * Building UI for an unreachable state is how a panel starts teaching operators
 * things that are not true.
 * ======================================================================== */
import type { EndpointKind, Strategy } from '../api/types';
import { compilablePaths, type Parsed, type PathToken } from './grammar';
import { canDialOutbound, dialEdge, edgeBetween, type Topology } from './topology';

export type FindingCode =
  | 'path.cycle'
  | 'path.unknown_node'
  | 'path.edge_missing'
  | 'path.edge_not_outbound'
  | 'path.edge_disabled'
  | 'path.nested_disabled'
  | 'path.terminal_not_rendr'
  | 'route.paths_empty'
  | 'route.terminal_mismatch'
  | 'advice.duplicate_path'
  | 'advice.peak_degenerate';

export interface Finding {
  code: FindingCode;
  /** Index into `Parsed.paths`, or -1 for a finding about the whole intent. */
  path: number;
  /** Index into that path's `hops`, or -1 when the finding is not about one hop. */
  hop: number;
  /** True when the finding blocks compilation. Advice does not. */
  blocking: boolean;
  message: string;
}

export interface PathResolution {
  /** Index into `Parsed.paths`. */
  path: number;
  /** [local, ...written hops]. */
  hops: string[];
  terminal: string;
  /** Only meaningful when the path resolved cleanly. */
  ok: boolean;
  carrier: 'direct' | 'relay_chain';
  instanceId: string;
}

export interface CheckResult {
  findings: Finding[];
  resolutions: PathResolution[];
  /** The terminal every path agrees on, or null when they do not agree. */
  sharedTerminal: string | null;
  /** Index into `Parsed.paths` of the peak candidate, or -1. */
  peakCandidate: number;
  /** No blocking findings. Says nothing about what the daemon will decide. */
  clean: boolean;
}

const requiresRendrTerminal = (endpoint: EndpointKind): boolean =>
  endpoint === 'rendr_stream' || endpoint === 'rendr_packet';

export function check(
  parsed: Parsed,
  topo: Topology,
  strategy: Strategy,
  endpoint: EndpointKind,
): CheckResult {
  const findings: Finding[] = [];
  const resolutions: PathResolution[] = [];
  const live = compilablePaths(parsed);

  if (live.length === 0) {
    findings.push({
      code: 'route.paths_empty',
      path: -1,
      hop: -1,
      blocking: true,
      message: 'At least one path is required.',
    });
  }

  for (const p of parsed.paths) {
    if (p.ordinal < 0) continue;
    const index = parsed.paths.indexOf(p);
    resolutions.push(resolveOne(p, index, topo, endpoint, findings));
  }

  // The one group constraint that can actually fire.
  const resolvedTerminals = resolutions.filter((r) => r.terminal);
  let sharedTerminal: string | null = null;
  if (resolvedTerminals.length > 0) {
    const first = resolvedTerminals[0] as PathResolution;
    sharedTerminal = first.terminal;
    for (const r of resolvedTerminals.slice(1)) {
      if (r.terminal !== first.terminal) {
        sharedTerminal = null;
        findings.push({
          code: 'route.terminal_mismatch',
          path: r.path,
          hop: -1,
          blocking: true,
          message: `This path ends at ${r.terminal}. Every path in a group must reach the same terminal, and the first one reaches ${first.terminal}.`,
        });
      }
    }
  }

  // Advisory: identical paths compile, and then collide.
  const seen = new Map<string, number>();
  for (const p of live) {
    const key = p.hops.map((h) => h.text).join('/');
    const firstAt = seen.get(key);
    if (firstAt === undefined) {
      seen.set(key, p.ordinal + 1);
      continue;
    }
    findings.push({
      code: 'advice.duplicate_path',
      path: parsed.paths.indexOf(p),
      hop: -1,
      blocking: false,
      message: `Identical to path ${firstAt}. It compiles, but both leaves are named "${key.replace(/\//g, '-')}", so this one adds nothing and collides with the first.`,
    });
  }

  // Advisory: peak with one path is silently just selector.
  const peakCandidate =
    strategy === 'peak' && live.length > 0
      ? parsed.paths.indexOf(live[live.length - 1] as PathToken)
      : -1;
  if (strategy === 'peak' && live.length === 1) {
    findings.push({
      code: 'advice.peak_degenerate',
      path: -1,
      hop: -1,
      blocking: false,
      message:
        'Peak needs two or more paths to do anything. With one path it becomes a plain selector, and nothing in the compiled plan will say so.',
    });
  }

  return {
    findings,
    resolutions,
    sharedTerminal,
    peakCandidate,
    clean: !findings.some((f) => f.blocking),
  };
}

/** route.ResolvePath, in its exact order, collecting rather than returning early. */
function resolveOne(
  p: PathToken,
  index: number,
  topo: Topology,
  endpoint: EndpointKind,
  findings: Finding[],
): PathResolution {
  const written = p.hops.map((h) => h.text);
  const hops = [topo.local, ...written];
  let ok = true;

  // Cycle first, over the whole chain INCLUDING the local node.
  const seen = new Set<string>();
  for (let i = 0; i < hops.length; i += 1) {
    const id = hops[i] as string;
    if (seen.has(id)) {
      ok = false;
      findings.push({
        code: 'path.cycle',
        path: index,
        hop: Math.max(0, i - 1),
        blocking: true,
        message:
          id === topo.local
            ? 'This path returns to the local node. A node may appear only once, and the local node is already hop zero.'
            : `${id} appears more than once in this path.`,
      });
      break;
    }
    seen.add(id);
  }

  for (let i = 0; i < hops.length - 1; i += 1) {
    const from = hops[i] as string;
    const to = hops[i + 1] as string;
    const hopIndex = i; // index into `written`

    const node = topo.nodes.get(to);
    if (!node) {
      ok = false;
      findings.push({
        code: 'path.unknown_node',
        path: index,
        hop: hopIndex,
        blocking: true,
        message: `${to} is not in the address book. Hops match on node ID, not on the name a peer is filed under.`,
      });
      continue;
    }

    const dial = dialEdge(topo, from, to);
    if (!dial) {
      ok = false;
      const exact = edgeBetween(topo, from, to);
      if (exact && !canDialOutbound(exact.direction)) {
        findings.push({
          code: 'path.edge_not_outbound',
          path: index,
          hop: hopIndex,
          blocking: true,
          message: `The link to ${to} is ${exact.direction}, so this node cannot dial it.`,
        });
      } else {
        findings.push({
          code: 'path.edge_missing',
          path: index,
          hop: hopIndex,
          blocking: true,
          message: `There is no link from ${from === topo.local ? 'this node' : from} to ${to}. A hop after another hop has to be reachable from it, which usually means filing it under that peer.`,
        });
      }
      continue;
    }

    if (!dial.edge.enabled) {
      ok = false;
      findings.push({
        code: 'path.edge_disabled',
        path: index,
        hop: hopIndex,
        blocking: true,
        message: `The link to ${to} is disabled${dial.edge.disabledCause ? `: ${dial.edge.disabledCause}` : '.'}`,
      });
      continue;
    }

    // Only an INTERMEDIATE hop needs nested expansion. Appending a hop can
    // therefore make an already-legal edge illegal, which is why the message
    // names the link rather than the node.
    if (i < hops.length - 2 && !dial.edge.nestedEnabled) {
      ok = false;
      findings.push({
        code: 'path.nested_disabled',
        path: index,
        hop: hopIndex,
        blocking: true,
        message: `The link to ${to} is not enabled for nested expansion, so ${to} can only be the last hop. It became a waypoint when a hop was added after it.`,
      });
    }
  }

  const terminal = (hops[hops.length - 1] as string) ?? '';
  const terminalNode = topo.nodes.get(terminal);
  if (terminalNode && requiresRendrTerminal(endpoint)) {
    if (!terminalNode.rendrCapable) {
      ok = false;
      findings.push({
        code: 'path.terminal_not_rendr',
        path: index,
        hop: Math.max(0, written.length - 1),
        blocking: true,
        message: `${terminal} does not advertise rendr, so it cannot terminate a ${endpoint} path. The egress endpoint does not require it.`,
      });
    }
  }

  return {
    path: index,
    hops,
    terminal,
    ok,
    carrier: hops.length > 2 ? 'relay_chain' : 'direct',
    instanceId: terminalNode?.instanceId ?? '',
  };
}
