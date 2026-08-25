/* ===========================================================================
 * OVERVIEW
 *
 * The landing screen, and the one most at risk of lying.
 *
 * It carries TWO PLANES that a conventional panel would merge into a single
 * green tick, and merging them is the exact failure this product exists to
 * avoid:
 *
 *   CONFIG PLANE   what is written down. Readable from disk with the daemon
 *                  stopped. The config read self-labels
 *                  `status_source: "config_only"` and hardcodes
 *                  `runtime.available: false` precisely so a caller cannot
 *                  mistake it for liveness (internal/cli/cli.go:245-252).
 *
 *   RUNTIME PLANE  what the daemon observes. `GET /v1/status`, the only live
 *                  signal in the system, and absent entirely when the daemon
 *                  is not running.
 *
 * They are drawn as two cards, never as one status. A node can be perfectly
 * configured and completely dead, and this screen has to be able to say that.
 *
 * NOT OBSERVED IS NOT OFF
 * -----------------------
 * A runtime may explicitly report `unavailable`, while a missing daemon read
 * means nothing was observed at all. Those states stay distinct here.
 *
 * SAID ONCE
 * ---------
 * An earlier version stated the same two facts three times: a long page
 * description, a full-width notice about the missing event stream, and a
 * provenance line on every card. Worse, the revision, the daemon state and the
 * last-read time are all permanently visible in the app's own top bar, twenty
 * pixels above — so repeating them here was a fourth telling.
 *
 * So: the page description carries the split and the staleness, once. Each card
 * description carries only the fact the other card cannot claim — the config is
 * readable with the daemon down, the runtime vanishes when it is. Nothing else
 * on the screen restates either.
 *
 * WHY THERE IS NO FOURTH CARD
 * ---------------------------
 * The page is short, and the short page is the honest length. The store holds
 * exactly two objects — `local` and `daemon` — and every field on them is
 * already stated here, owned by a screen that can give it proper context, or
 * unfit to state at all. The three that look like candidates and are not:
 *
 *   node.disabled     no writer exists. Nothing in the CLI ever sets it, so it
 *   node.role         is permanently false, and a permanently-false flag drawn
 *                     as "enabled" is the not-observed-is-not-off trap running
 *                     backwards. `role` is validated but likewise never set.
 *
 *   node.rendr_capable  forced true by `local init` (cli.go:339). A constant,
 *                     not a reading — and for a peer this codebase already
 *                     treats the same field as a CLAIM (peers/PeerDetail.tsx).
 *
 *   revision drift    tempting: two planes, two revisions, compare them. But
 *                     `Daemon.Status` calls `configstore.Load` on every request
 *                     (daemon/daemon.go:145-152), so the daemon reports the
 *                     revision on disk right now, not a generation it loaded
 *                     and is holding. There is no cached generation to drift
 *                     from, so "the daemon has not picked up your change" would
 *                     be a sentence about a mechanism this backend does not
 *                     have.
 *
 * Everything genuinely left — the seed file's OS ACL grade, the strict-outbound
 * postures, the idempotency scope, the four response limits — is a real reading
 * that already has a screen, and each needs a paragraph of context to be read
 * correctly. Mirroring them here would produce a landing page whose warnings
 * all live somewhere else, which is how a dashboard starts lying by summary.
 * ======================================================================== */
import type { ReactNode } from 'react';
import {
  Card,
  CardBody,
  CardHeader,
  Columns,
  IconArrowBoth,
  IconArrowLeft,
  IconArrowRight,
  IconBook,
  IconDaemon,
  IconPeers,
  PageHeader,
  Screen,
  Stat,
  StatGroup,
  StateMatrix,
  Timestamp,
} from '@stratum/ui';
import type { IdentityState } from '../api/types';
import { runtimeStatus } from '../api/runtimeStatus';
import { adviseCode } from '../api/errors';
import { useControl } from '../state/store';
import { FailureNotice } from '../components/FailureNotice';
import { NodeId } from '../components/NodeId';

/** `backed` is the only healthy identity state. The three dead ends are
 *  failures rather than warnings because no command remediates them. */
function identityStatus(state: IdentityState | undefined) {
  if (!state) return undefined;
  if (state === 'backed') return 'ok' as const;
  if (state === 'uninitialized' || state === 'recoverable') return 'degraded' as const;
  return 'failed' as const;
}

/** The store reports a read failure as a bare code; the catalogue turns it
 *  into the same shape every other failure on screen uses. */
const adviseFailure = (code: string) => ({ ...adviseCode(code), code, detail: '' });

/** A card title with its subject's icon. Decorative — the text carries the name. */
function Titled({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <span
      style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--stratum-space-4)' }}
    >
      {icon}
      {children}
    </span>
  );
}

/* The arrows are the same three used on the Peers screen, deliberately: an
 * operator who learns "→ means this node may dial them" there must not have to
 * relearn it here. And "may dial" is repeated in every caption because a
 * direction in a mesh panel is read as a traffic direction by default, which it
 * is not — nothing on this screen has ever been dialled. */
const DIAL = [
  {
    key: 'outbound' as const,
    label: 'Outbound',
    icon: <IconArrowRight />,
    meaning: 'this node may dial them',
  },
  {
    key: 'inbound' as const,
    label: 'Inbound',
    icon: <IconArrowLeft />,
    meaning: 'they may dial this node',
  },
  {
    key: 'bidirectional' as const,
    label: 'Bidirectional',
    icon: <IconArrowBoth />,
    meaning: 'either may dial the other',
  },
];

export function Overview() {
  const { local, daemon, localError, daemonError } = useControl();

  const identity = local?.identity;
  /* `peer_counts` is a struct, not a slice, so it is present whenever the
   * config plane was read at all. Its absence therefore means "not read",
   * never "no peers" — and a zero inside it is a stated reading. Hence `??`
   * rather than `||`: a real 0 must survive. */
  const counts = local?.peer_counts;

  return (
    <Screen
      header={
        <PageHeader
          title="Overview"
          /* KEEP THIS UNDER 80 CHARACTERS.
           *
           * PageHeader caps its description at `80ch` (PageHeader.css:79) —
           * about 518px at the `text-xs` size it sets. That is the component
           * working, not failing: the cap is a measure, and its comment says so
           * ("One line's worth. A longer explanation is page content, not
           * chrome."). The previous wording ran to 102 characters and broke
           * after "refreshes on", which read as a wrapping bug sitting above
           * cards four times its width.
           *
           * So the fix belongs here rather than in the framework, and it is a
           * writing fix: say it in one line. 76 characters, and both required
           * facts survive — the split, and that nothing refreshes by itself.
           * Every screen's description now clears the cap unwrapped. */
          description="Written config and observed runtime, never merged. Neither refreshes itself."
        />
      }
    >
      <Columns>
        {/* ---- CONFIG PLANE ---------------------------------------------- */}
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title={<Titled icon={<IconBook />}>Configuration</Titled>}
            description="Readable from disk with the daemon stopped."
          />
          <CardBody>
            {localError ? (
              <FailureNotice failure={adviseFailure(localError)} variant="inline" />
            ) : (
              <StateMatrix
                label="Configuration state"
                layout="stack"
                dimensions={[
                  {
                    key: 'identity',
                    label: 'Identity',
                    value: identity?.state ?? null,
                    status: identityStatus(identity?.state),
                    note:
                      'Whether a seed file on disk backs the configured node ID. '
                      + 'Says nothing about whether the daemon is running.',
                  },
                  {
                    key: 'node',
                    label: 'Node ID',
                    // The local node ID is the one identifier here that is
                    // cryptographically derived and canonically validated, so
                    // it is the only one given an identifier affordance.
                    value: identity?.node_id ? <NodeId value={identity.node_id} /> : null,
                    note: 'Derived from the seed and canonically validated.',
                  },
                  {
                    key: 'name',
                    label: 'Display name',
                    value: local?.display_name || null,
                    note: 'A label. Not an identifier — nothing resolves by it.',
                  },
                  {
                    key: 'inbound',
                    label: 'Inbound listeners',
                    /* `local.inbounds` is `null` when none are configured — a
                     * nil Go slice, not an empty array. Calling `.filter` on it
                     * throws, and with no error boundary that unmounts the
                     * whole panel on a node that is simply new. */
                    value: local
                      ? `${(local.inbounds ?? []).filter((i) => i.enabled).length} of ${(local.inbounds ?? []).length} enabled`
                      : null,
                  },
                ]}
              />
            )}
          </CardBody>
        </Card>

        {/* ---- RUNTIME PLANE --------------------------------------------- */}
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title={<Titled icon={<IconDaemon />}>Runtime</Titled>}
            description="Absent entirely when the daemon is not running."
          />
          <CardBody>
            {daemonError ? (
              /* Unreachable describes the panel's view, not the daemon's
               * health. The catalogue entry says so in one place. */
              <FailureNotice failure={adviseFailure(daemonError)} variant="inline" />
            ) : (
              <StateMatrix
                label="Runtime state"
                layout="stack"
                dimensions={[
                  {
                    key: 'daemon',
                    label: 'Daemon',
                    value: daemon?.state ?? null,
                    // A daemon that reports `stopped` has told us something.
                    // Only an absent read is unknown.
                    status: daemon
                      ? daemon.state === 'running'
                        ? 'ok'
                        : 'inactive'
                      : 'unknown',
                  },
                  {
                    key: 'started',
                    label: 'Started',
                    value: daemon ? (
                      <Timestamp value={daemon.started_at} display="relative" />
                    ) : null,
                  },
                  {
                    key: 'rendr',
                    label: 'rendr',
                    value: daemon?.rendr.state ?? null,
                    status: runtimeStatus(daemon?.rendr.state),
                  },
                  {
                    key: 'xray',
                    label: 'xray',
                    value: daemon?.xray.state ?? null,
                    status: runtimeStatus(daemon?.xray.state),
                  },
                  {
                    key: 'generation',
                    label: 'Applied generation',
                    value:
                      daemon?.xray.state === 'unavailable'
                        ? null
                        : daemon?.xray.current
                          ? `#${daemon.xray.current.generation} · ${daemon.xray.current.ref_count} refs`
                          : 'none installed',
                  },
                ]}
              />
            )}
          </CardBody>
        </Card>
      </Columns>

      {/* ---- Peers, by dial permission ------------------------------------
        * These count DECLARED DIAL PERMISSION, not connections. Labelling them
        * "connections" would invent a signal this backend does not have. */}
      <Card variant="outlined">
        <CardHeader
          headingLevel={2}
          title={<Titled icon={<IconPeers />}>Peers by dial permission</Titled>}
          description="Written permissions. No connection, session or reachability is observable."
          /* No "not read" badge here any more. Absent counts still mean the
           * config plane was not read rather than an empty address book — but
           * each Stat now says exactly that in its own value slot, so a badge
           * would be the same claim a fourth time on one card. Drawing three
           * zeros remains the thing to avoid; that is what `?? null` below is
           * for. */
        />
        <CardBody>
          {/* `separators` is on for the one case the framework reserves it for:
            * three stats edge to edge inside a card, where the column gap alone
            * leaves it ambiguous which hint belongs to which number. The rules
            * are also what stop a single-digit count reading as a stray mark in
            * an otherwise empty band. */}
          <StatGroup label="Peer counts by dial permission" separators>
            {DIAL.map((d) => (
              <Stat
                key={d.key}
                label={d.label}
                icon={d.icon}
                /* A RAW number, never a wrapped metric. Stat sets the digits
                 * itself, and an element is always truthy — wrapping one would
                 * mark the stat observed while the thing inside printed its own
                 * unobserved dash.
                 *
                 * `?? null` and not `|| 0`: a real 0 is a stated reading — the
                 * config plane was read and no peer carries this permission —
                 * and must survive. `null` is the other thing entirely, and Stat
                 * renders it as not observed rather than as a zero nobody
                 * measured. */
                value={counts?.[d.key] ?? null}
                /* The hint is the point of the card, not decoration: a
                 * direction in a mesh panel is read as a traffic direction by
                 * default, and these are written permissions. */
                hint={d.meaning}
              />
            ))}
          </StatGroup>
        </CardBody>
      </Card>
    </Screen>
  );
}
