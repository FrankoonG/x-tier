/* ===========================================================================
 * PATHS
 *
 * Compiling a path is a PURE FUNCTION. It resolves an expression against the
 * address book, checks the constraints, and returns a plan. It installs
 * nothing, dials nothing and moves no traffic — `Runtime.Apply` has no
 * production caller anywhere in this build.
 *
 * That is stated on the screen, at the top, in those words. A screen called
 * "Paths" with a green "compiled successfully" is otherwise read as "the route
 * is up", and it is not: it means the sentence parsed.
 *
 * HOPS RESOLVE BY NODE ID, NEVER BY NAME
 * --------------------------------------
 * This is the single sharpest edge in the product. The address book has three
 * name-shaped fields — `name`, `display_name`, `node_id` — and only the last
 * one resolves. An operator who types the peer name gets `path.unknown_node`
 * for a peer that is plainly sitting in the table.
 *
 * So the builder never asks anyone to type a hop. `ChainBuilder` offers peers
 * as options, emits their node IDs, and labels each option with the name the
 * operator recognises. The failure mode is designed out rather than explained.
 *
 * WHY `peak` IS NOT A FOURTH RADIO BUTTON
 * ---------------------------------------
 * The Go enum lists `selector | race | bond | peak` as four peers. In rendr —
 * the runtime that actually implements this — Peak is an OVERLAY on a selector
 * group (`target.go:85-98`: `GroupTarget.Peak *PeakTransfer`), not a sibling
 * strategy. Rendering it as a fourth equal choice would teach the operator a
 * model the runtime does not have. It is offered, because the backend accepts
 * it, and annotated with what it actually is.
 *
 * WHAT THE OPERATOR IS BUILDING
 * -----------------------------
 * A path, not a command. The control command behind Compile is unchanged and
 * still assembled here, but it is not the subject of the interface: nothing on
 * this screen asks anyone to think in argv. The result reads as a result —
 * each resolved path is a panel whose HEADING is the hop chain, because the
 * chain is the answer and everything else on the panel qualifies it. The raw
 * resolver response stays reachable behind a disclosure, where evidence
 * belongs.
 * ======================================================================== */
import { useMemo, useState, type ReactNode } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  ChainBuilder,
  Code,
  Columns,
  Disclosure,
  EmptyState,
  Field,
  IconArrowBoth,
  IconArrowLeft,
  IconArrowRight,
  IconClose,
  IconEdit,
  IconPath,
  IconPeers,
  IconRelay,
  InlineMessage,
  JsonViewer,
  PageHeader,
  Panel,
  PathChain,
  Screen,
  ScrollArea,
  SegmentedControl,
  Select,
  StateMatrix,
  Table,
  Tag,
  Tooltip,
} from '@stratum/ui';
import type { BadgeVariant, ChainOption, PathHop, TableColumn } from '@stratum/ui';
import { compilePath as requestPathCompile, getPeers } from '../api/control';
import { describeFailure, type FailureView } from '../api/errors';
import type {
  CompileResult,
  Direction,
  Edge,
  EndpointKind,
  PeersResponse,
  ResolvedPath,
  Strategy,
} from '../api/types';
import { useControl } from '../state/store';
import { useDomainRead } from '../state/useDomainRead';
import { Absent } from '../components/Absent';
import { FailureNotice } from '../components/FailureNotice';

const STRATEGIES: Record<Strategy, { label: string; blurb: string; caveat?: string }> = {
  selector: {
    label: 'selector',
    blurb: 'One path is chosen and used. The others stand by.',
  },
  race: {
    label: 'race',
    blurb: 'Every path is attempted; the first to answer wins.',
  },
  bond: {
    label: 'bond',
    blurb: 'Paths are used together as one aggregate.',
  },
  peak: {
    label: 'peak',
    blurb: 'Shifts to a faster member once a transfer proves large enough to be worth moving.',
    caveat:
      'In rendr, Peak is an overlay on a selector group rather than a strategy of its own — it '
      + 'decorates the members a selector already chose. The control plane accepts it as a fourth '
      + 'enum value, so it is offered here, but the runtime model underneath is not four-way.',
  },
};

/* Shared with the Peers table so one concept has one encoding. A direction that
 * is green there and blue here reads as two different things, and the arrow has
 * to point the same way in both places for anyone scanning rather than reading. */
const DIRECTION: Record<
  Direction,
  { label: string; variant: BadgeVariant; icon: ReactNode; meaning: string }
> = {
  outbound: {
    label: 'outbound',
    variant: 'info',
    icon: <IconArrowRight />,
    meaning: 'This node may dial the peer. The peer may not dial back.',
  },
  inbound: {
    label: 'inbound',
    variant: 'accent',
    icon: <IconArrowLeft />,
    meaning: 'The peer may dial this node. This node may not dial out to it.',
  },
  bidirectional: {
    label: 'bidirectional',
    variant: 'success',
    icon: <IconArrowBoth />,
    meaning: 'Either end may dial the other.',
  },
};

const ENDPOINTS: { value: EndpointKind; label: string; description: string }[] = [
  { value: 'rendr_stream', label: 'rendr stream', description: 'Stream-oriented rendr endpoint.' },
  { value: 'rendr_packet', label: 'rendr packet', description: 'Packet-oriented rendr endpoint.' },
  {
    value: 'egress',
    label: 'egress',
    // The distinction that matters: the two rendr kinds check
    // finalNode.RendrCapable and refuse a terminal that does not advertise it
    // (resolver.go:100-104). Egress does not, so it is the only way to end a
    // path on a node that does not speak rendr.
    description: 'Leaves the mesh at the terminal. The only endpoint that does not require the terminal to speak rendr.',
  },
];

/*
 * The per-hop permission table.
 *
 * Edges are where the per-hop permissions live, and they are the thing an
 * operator debugging a refusal needs — so they are shown, not tucked behind a
 * disclosure. Four columns, all of them written config: nothing here has been
 * dialled.
 *
 * `layout="auto"` rather than `fixed`: no column is pinned, and this table
 * lives inside a panel that is narrower than the page, so the columns have to
 * be able to give way. Fixed widths would overflow the panel instead.
 */
const EDGE_COLUMNS: TableColumn<Edge>[] = [
  {
    key: 'hop',
    header: 'Hop',
    cell: (edge) => (
      <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
        <span style={{ fontWeight: 500 }}>{edge.peer_name ?? edge.to}</span>
        {edge.peer_name ? (
          <Code
            variant="plain"
            style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}
          >
            {edge.to}
          </Code>
        ) : null}
      </div>
    ),
  },
  {
    key: 'direction',
    header: 'May dial',
    width: 160,
    cell: (edge) => {
      const d = DIRECTION[edge.direction];
      return (
        <Tooltip
          trigger={
            <Badge variant={d.variant} size="sm" icon={d.icon}>
              {d.label}
            </Badge>
          }
        >
          {d.meaning}
        </Tooltip>
      );
    },
  },
  {
    key: 'transit',
    header: 'Transit',
    width: 150,
    cell: (edge) =>
      edge.nested_enabled ? (
        <Tooltip
          trigger={
            <Tag variant="accent" size="sm" icon={<IconRelay />}>
              permitted
            </Tag>
          }
        >
          May carry traffic onward as an intermediate hop.
        </Tooltip>
      ) : (
        <Tooltip
          trigger={
            <Tag variant="neutral" size="sm" outline>
              endpoint only
            </Tag>
          }
        >
          Final hop only. Still a valid single-hop destination — this is a permission, not a fault.
        </Tooltip>
      ),
  },
  {
    key: 'profile',
    header: 'Profile',
    width: 130,
    cell: (edge) =>
      edge.xray_profile_id ? (
        <Tag size="sm" variant="neutral">
          {edge.xray_profile_id}
        </Tag>
      ) : (
        <Absent />
      ),
  },
];

export function Paths() {
  const { revision, epoch } = useControl();
  const peersRead = useDomainRead<PeersResponse>('peers', getPeers, [revision, epoch]);
  // The RESPONSE decides read-vs-unread; the field inside it decides empty.
  // `peers` arrives as `null` for an empty address book, so testing the field
  // alone would report a clean read as a failure.
  const known = peersRead.data ? (peersRead.data.peers ?? []) : null;
  const peers = known ?? [];

  const [steps, setSteps] = useState<string[]>(['']);
  const [strategy, setStrategy] = useState<Strategy>('selector');
  const [endpoint, setEndpoint] = useState<EndpointKind>('rendr_stream');

  const [result, setResult] = useState<CompileResult | null>(null);
  const [failure, setFailure] = useState<FailureView | null>(null);
  const [running, setRunning] = useState(false);

  /**
   * Options for hop `index`.
   *
   * Two constraints are enforced here rather than left to the daemon, because
   * the daemon can only report them AFTER a compile and the builder can show
   * them while the operator is still choosing:
   *
   *   disabled peers          `path.edge_disabled`
   *   non-final transit       `path.nesting_forbidden` — and this one is
   *                           position-dependent, which is exactly the kind of
   *                           rule a flat dropdown gets wrong. A peer that
   *                           cannot be an intermediate hop is still a
   *                           perfectly good LAST hop, so it is only excluded
   *                           while something follows it.
   */
  const resolveOptions = (chainSoFar: readonly string[], index: number): ChainOption[] => {
    const isFinal = index === steps.length - 1;
    const used = new Set(chainSoFar.filter((v, i) => v && i !== index));

    return peers.map<ChainOption>((p) => {
      const disabledReason = !p.enabled
        ? `Administratively disabled${p.disabled_cause ? ` — ${p.disabled_cause}` : ''}. Paths through it will not compile.`
        // An inbound-only peer may not be DIALLED, so it cannot appear on an
        // outbound path in any position — not just as a transit hop. The
        // daemon says `path.edge_not_outbound`; saying it here is better.
        : p.direction === 'inbound'
          ? 'Inbound-only: this node may not dial it, so it cannot be a hop at all.'
          : !isFinal && !p.nested_enabled
            ? 'May not be an intermediate hop. Still valid as the final hop.'
            : used.has(p.node_id)
              ? 'Already used earlier in this path.'
              : undefined;

      return {
        value: p.node_id,
        // The operator recognises the name; the daemon needs the node ID. Both
        // are shown so the mapping is learnable rather than magic.
        label: p.display_name || p.name,
        description: `node id ${p.node_id}${p.addr ? ` · ${p.addr}` : ''}`,
        group: p.direction === 'inbound' ? 'Cannot be dialled' : 'Dialable',
        disabled: Boolean(disabledReason),
        disabledReason,
        // Neither 'ok' nor 'broken': nothing on this list has been dialled.
        // `enabled` is a decision someone typed, so a disabled peer is
        // inactive, not faulty.
        status: p.enabled ? 'unknown' : 'degraded',
        detail: p.xray_profile_id || undefined,
      };
    });
  };

  /*
   * "/" is the hop separator, not ">".
   *
   * route.splitPath does `strings.FieldsFunc(expr, r == '/' || r == '\\')`
   * (resolver.go:127-137). An expression joined with ">" is therefore ONE hop
   * whose node ID happens to contain ">" characters, and the daemon answers
   * path.unknown_node for it — every time, for every expression this screen
   * has ever produced.
   *
   * It went unnoticed because the fake backend split on ">" as well: a mock
   * that shares the frontend's guess certifies the guess. The mock now ports
   * the real grammar, which is what surfaced this.
   */
  const expression = steps.filter(Boolean).join('/');
  const compile = async () => {
    setRunning(true);
    setFailure(null);
    try {
      // A read, not a mutation: no revision is attached, because compiling
      // writes nothing and there is nothing to conflict with.
      const out = await requestPathCompile(expression, strategy, endpoint);
      setResult(out);
    } catch (err) {
      setFailure(describeFailure(err));
      setResult(null);
    } finally {
      setRunning(false);
    }
  };

  const nameFor = (nodeId: string) => peers.find((p) => p.node_id === nodeId)?.name ?? nodeId;

  const toHops = (path: ResolvedPath): PathHop[] => {
    // Go marshals a nil slice as `null`, so a path that resolved to no hops
    // arrives as an absent array rather than an empty one.
    const hops = path.hops ?? [];
    return hops.map((h, i) => ({
      id: `${path.id}-${i}`,
      label: nameFor(h),
      status: 'unknown',
      // Every hop is `unknown`, deliberately. A compiled path has been
      // RESOLVED, not tested — nothing on it has been dialled, so painting a
      // hop green because it parsed would be the exact overclaim this screen
      // exists to avoid.
      detail: `node id ${h}${i === hops.length - 1 ? ' · final hop' : ' · transit'}`,
    }));
  };

  // Same nil-slice rule as `hops`: the presence of `compiled` is the reading,
  // and an absent array inside it is not a different kind of nothing.
  const resolved = result ? (result.compiled.resolved_paths ?? []) : [];

  return (
    <Screen
      header={
        <PageHeader
          title="Paths"
          description="Resolve a path expression against the address book and read the plan it produces."
          meta={
            <Badge variant="neutral" size="sm">
              plan only
            </Badge>
          }
          /*
           * The builder on this screen composes exactly ONE path, so a group —
           * the entire reason `strategy` exists — has never been reachable from
           * it. Composing several is a structural job and it has its own
           * surface; this carries whatever is on screen across to it.
           */
          actions={
            <Button
              size="sm"
              variant="primary"
              icon={<IconEdit />}
              onClick={() => {
                const e = encodeURIComponent(expression);
                window.location.hash = `#/paths/edit?e=${e}&s=${strategy}&k=${endpoint}`;
              }}
            >
              Open editor
            </Button>
          }
        />
      }
    >
      {/* Said once, at the top. Everything else on this screen assumes it. */}
      <Banner
        variant="warning"
        size="sm"
        title="Compiling a path installs nothing"
        dismissible
        storageKey="xtier.paths.plan-notice"
      >
        Compilation resolves the hops, checks the constraints and returns a plan. It does not dial,
        does not install a route and does not move traffic — the runtime apply path has no caller in
        this build. A path that compiles has been <em>parsed</em>, not <em>tested</em>.
      </Banner>

      <Card variant="outlined">
        <CardHeader
          headingLevel={2}
          title="Build a path"
          description="Hops resolve by node ID, so peers are chosen rather than typed — the names in the address book are not what the daemon matches on."
        />
        <CardBody>
          <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
            {peersRead.failure && <FailureNotice failure={peersRead.failure} variant="inline" />}

            {!known && !peersRead.loading ? (
              <EmptyState
                title="Address book not read"
                headingLevel={3}
                icon={<IconPeers />}
                description="Hops resolve out of the address book, and it could not be read — so nothing can be offered here. This is not an empty address book."
                bordered
              />
            ) : known && known.length === 0 ? (
              <EmptyState
                title="No peers to route through"
                headingLevel={3}
                icon={<IconPeers />}
                description="A path expression resolves hops out of the address book, and it is empty."
                bordered
              />
            ) : (
              <ChainBuilder
                label="Path hops"
                steps={steps}
                onStepsChange={setSteps}
                resolveOptions={resolveOptions}
                minSteps={1}
                maxSteps={6}
                placeholder="Choose a peer…"
                labelEmptyChain="No hops chosen yet"
                separator=" › "
              />
            )}

            <Columns min="20rem">
              <Field group label="Strategy">
                <div style={{ display: 'grid', gap: 'var(--stratum-space-3)' }}>
                  <SegmentedControl
                    /* Must MATCH the visible text above it. An accessible name
                     * that merely paraphrases the label breaks voice control:
                     * "click Strategy" then matches nothing. */
                    label="Strategy"
                    value={strategy}
                    onValueChange={(v) => setStrategy(v as Strategy)}
                    items={(Object.keys(STRATEGIES) as Strategy[]).map((s) => ({
                      value: s,
                      children: STRATEGIES[s].label,
                    }))}
                  />
                  <span
                    style={{
                      fontSize: 'var(--stratum-text-xs)',
                      color: 'var(--stratum-text-muted)',
                    }}
                  >
                    {STRATEGIES[strategy].blurb}
                  </span>
                  {/* The modelling caveat travels with the option rather than
                    * living in documentation nobody opens. */}
                  {STRATEGIES[strategy].caveat && (
                    <InlineMessage variant="info" size="xs" role="none">
                      {STRATEGIES[strategy].caveat}
                    </InlineMessage>
                  )}
                </div>
              </Field>

              <Field group label="Endpoint kind">
                <div style={{ display: 'grid', gap: 'var(--stratum-space-3)' }}>
                  <Select
                    options={ENDPOINTS}
                    value={endpoint}
                    onChange={(v) => setEndpoint((v as EndpointKind) ?? 'rendr_stream')}
                    /* `Select` takes its name only from aria-*: it does not
                     * consume the field context, and `Field group` renders a
                     * plain span rather than a `<label for>` precisely because
                     * a label may only point at one control. */
                    aria-label="Endpoint kind"
                    fullWidth
                  />
                  <span
                    style={{
                      fontSize: 'var(--stratum-text-xs)',
                      color: 'var(--stratum-text-muted)',
                    }}
                  >
                    {ENDPOINTS.find((e) => e.value === endpoint)?.description}
                  </span>
                </div>
              </Field>
            </Columns>
          </div>
        </CardBody>

        <CardFooter align="start">
          <Button
            variant="primary"
            icon={<IconPath />}
            onClick={() => void compile()}
            loading={running}
            disabled={!expression}
          >
            Compile
          </Button>
          {(result || failure) && (
            <Button
              variant="subtle"
              icon={<IconClose />}
              onClick={() => {
                setResult(null);
                setFailure(null);
              }}
            >
              Clear result
            </Button>
          )}
        </CardFooter>
      </Card>

      {failure && <FailureNotice failure={failure} />}

      {/* The result region, reserved rather than absent.
        *
        * Without it the screen stops at the form and a compile has visibly
        * nowhere to land — the page reads as "form, then void" instead of
        * "form, then result". It carries the same outlined-card silhouette the
        * plan will occupy, so nothing jumps when one arrives.
        *
        * It describes what WILL appear and claims nothing about the network:
        * "no plan yet" is the absence of a compile, not a compile that found
        * no path. That second thing has its own empty state inside the result,
        * and conflating them would let a screen that has never been used look
        * like one that came back with nothing. */}
      {!result && !failure && (
        <Card variant="outlined">
          <CardBody>
            <EmptyState
              title="No plan yet"
              headingLevel={2}
              icon={<IconPath />}
              description="Choose the hops above and compile. The paths the expression resolves to, the per-hop permissions behind each one, and the resolver's exact response all appear here. Nothing is dialled to produce them."
            />
          </CardBody>
        </Card>
      )}

      {result && (
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title="Resolved plan"
            description="What the expression means. Not what the network did."
            actions={
              <Badge variant="neutral" size="sm">
                revision {result.revision}
              </Badge>
            }
          />
          <CardBody>
            <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
              <StateMatrix
                label="What the resolver returned"
                layout="grid"
                size="sm"
                dimensions={[
                  {
                    key: 'strategy',
                    label: 'Strategy',
                    value: result.compiled.intent.strategy,
                    status: 'info',
                    explicit: true,
                  },
                  {
                    key: 'endpoint',
                    label: 'Endpoint kind',
                    value: result.compiled.intent.endpoint_kind,
                    status: 'info',
                    explicit: true,
                  },
                  {
                    key: 'paths',
                    label: 'Paths resolved',
                    value: String(resolved.length),
                    status: 'ok',
                  },
                  {
                    key: 'installed',
                    label: 'Installed',
                    /* Structurally absent. The apply path has no caller, so
                     * there is nothing to report — not "no" and not "0". */
                    value: null,
                    note: 'Compilation creates no generation, so there is nothing here to observe.',
                  },
                ]}
              />

              {resolved.length === 0 ? (
                /* A stated zero: the resolver answered and named no path. That
                 * is a reading, not a missing one, so it gets an empty state
                 * rather than an absence marker. */
                <EmptyState
                  title="No path resolved"
                  headingLevel={3}
                  icon={<IconPath />}
                  description="The response carried no resolved path. That is what the resolver returned, not a failure to read it."
                  bordered
                />
              ) : (
                resolved.map((path) => (
                  <Panel
                    key={path.id}
                    variant="outlined"
                    padding="sm"
                    headingLevel={3}
                    /* The chain is the answer, so it is the heading. Everything
                     * else in the panel qualifies it. */
                    title={<PathChain hops={toHops(path)} size="md" separator=" › " />}
                    /*
                     * There was a `primary` tag here, keyed on
                     * `intent.primary_path`. The field exists on the Go struct
                     * and NOTHING in the daemon ever assigns it — grep the repo
                     * — so the tag could never appear. A decoration that is
                     * structurally unreachable is worse than no decoration: it
                     * implies the concept is live.
                     */
                    actions={
                      <Tag size="sm" variant="neutral">
                        {path.legacy_carrier_kind === 'relay_chain' ? 'relay chain' : 'direct'}
                      </Tag>
                    }
                  >
                    <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
                      <StateMatrix
                        label="Where this path terminates"
                        layout="inline"
                        size="sm"
                        /* Not "not observed": these fields were returned, and
                         * an empty one means the resolver named nothing. */
                        unobservedLabel="not reported"
                        dimensions={[
                          {
                            key: 'terminal',
                            label: 'Terminal',
                            value: path.rendr_terminal || null,
                            status: 'info',
                            note: 'The node the path ends at. A rendr endpoint kind requires this node to be rendr-capable.',
                          },
                          {
                            key: 'session',
                            label: 'Session',
                            value: path.session_kind || null,
                            status: 'info',
                            note: 'Whether the endpoint carries a byte stream or discrete packets. Follows from the endpoint kind.',
                          },
                          {
                            key: 'leaf',
                            label: 'Leaf transport',
                            value: path.leaf_transport || null,
                            status: 'info',
                            note: 'The transport used on the last hop.',
                          },
                        ]}
                      />

                      {path.disabled_reason && (
                        <InlineMessage variant="warning">{path.disabled_reason}</InlineMessage>
                      )}

                      <ScrollArea orientation="horizontal" label="Resolved path edges">
                        <Table
                          data={path.edges ?? []}
                          columns={EDGE_COLUMNS}
                          rowKey={(edge, i) => `${edge.from}-${edge.to}-${i}`}
                          layout="auto"
                          density="default"
                          zebra
                          caption="Per-hop permissions. Written config, not a dial result."
                          emptyState={
                            <EmptyState
                              title="No edges returned"
                              headingLevel={4}
                              description="The resolver named this path but listed no edges for it."
                            />
                          }
                        />
                      </ScrollArea>
                    </div>
                  </Panel>
                ))
              )}

              {/* Evidence, not the subject: the exact response, for anyone
                * reconciling this screen against the daemon. */}
              <Disclosure
                size="sm"
                title="Raw resolver output"
                description="The unedited response, for reconciling this plan against the daemon."
              >
                <JsonViewer data={result} initialDepth={3} maxHeight="24rem" />
              </Disclosure>
            </div>
          </CardBody>
        </Card>
      )}
    </Screen>
  );
}
