/* ===========================================================================
 * PEERS — THE ADDRESS BOOK
 *
 * A book of WRITTEN-DOWN INTENT, not a list of connections. Every column comes
 * from a config file a human typed, and the backend offers no reachability, no
 * latency, no session count and no last-seen for any of it. `PeerConfig` does
 * not even carry a public key — `node_id` is an arbitrary string that is never
 * parsed or validated.
 *
 * So the discipline is mostly about what this screen REFUSES to draw:
 *
 *   NO status dot         green/red reads as up/down, and nothing here has
 *                         ever been dialled.
 *   NO `Identifier`       that affordance means "canonical, verified id". The
 *                         local node id earns it; a peer's does not.
 *   NO derived health     `enabled` is administrative intent someone typed,
 *                         not a probe result, and must not be styled like one.
 *
 * What it does draw is the four independent dimensions the config actually has,
 * kept apart rather than collapsed into one badge.
 *
 * LAYOUT
 * ------
 * The card follows the framework's own table composition: `padding="none"` on
 * the card, an explicitly padded header, a filter row that is its own band, and
 * a `padding="none"` body so the table's cell padding is the only inset. The
 * previous version omitted the header padding, which is what put every cell one
 * pixel from the card border.
 *
 * The card also `fill`s, so the screen is one card from the banner to the
 * bottom of the window and the table scrolls inside it. A short address book
 * used to stop halfway down and leave the rest of the page bare.
 * ======================================================================== */
import { Fragment, useMemo, useState } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Code,
  Disclosure,
  Drawer,
  EmptyState,
  IconArrowBoth,
  IconArrowLeft,
  IconArrowRight,
  IconMore,
  IconPlus,
  IconRelay,
  IconTrash,
  InlineMessage,
  Menu,
  MenuItem,
  MenuSeparator,
  PageHeader,
  Screen,
  SearchInput,
  SegmentedControl,
  Separator,
  Switch,
  Table,
  Tag,
  Tooltip,
} from '@stratum/ui';
import type { BadgeVariant, TableColumn } from '@stratum/ui';
import type {
  Direction,
  MutationResponse,
  NodeEgressGrantsResponse,
  PeerConfig,
  PeersResponse,
  XrayProfilesResponse,
} from '../api/types';
import { getNodeEgressGrants, getPeers, getXrayProfiles, mutations } from '../api/control';
import { useDomainRead } from '../state/useDomainRead';
import { useControl } from '../state/store';
import { FailureNotice } from '../components/FailureNotice';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';
import { Absent } from '../components/Absent';
import { PeerForm } from './peers/PeerForm';
import { PeerDetail } from './peers/PeerDetail';
import { PeerEgressGrantEditor } from './peers/PeerEgressGrantEditor';

/* Direction is the most misread field in a mesh panel: it looks like a traffic
 * direction and is not. The label says "may dial" every time, and the arrow
 * carries the same meaning for anyone scanning rather than reading. */
const DIRECTION: Record<
  Direction,
  { label: string; variant: BadgeVariant; icon: React.ReactNode; meaning: string }
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

/* Hoisted for the same reason `meaning` is: the transit cell and the glossary
 * below the table have to say the same thing, and two copies of a sentence are
 * two sentences that drift. */
const TRANSIT = {
  permitted: 'May carry traffic onward as an intermediate hop.',
  endpoint:
    'Final hop only. Still a valid single-hop destination — this is a permission, not a fault.',
} as const;

type DirectionFilter = 'all' | Direction;

/**
 * A GLOSSARY, WHICH IS NOT A STATE MATRIX
 *
 * This strip used to be a `StateMatrix`, and it was the wrong component twice
 * over. Half of it defined what a badge means, half of it declared what the
 * control plane does not expose, and every row carried the same "explicitly
 * set" marker — so a dictionary entry arrived dressed as an observation. A
 * marker that appears next to a definition means nothing anywhere else in the
 * panel, and the missing-signal claim is not a dimension of a peer at all. The
 * two claims are now in different places, in different clothes: the API's
 * silence is stated once in prose on the card header, and the badge meanings
 * live here, in a `<dl>` that says "term, definition" in the markup itself.
 *
 * It exists rather than living only in the badge tooltips because `Badge` and
 * `Tag` render a plain `<span>`. `Tooltip` opens on hover AND focus-visible,
 * but a span is not focusable, so for anyone driving this panel from the
 * keyboard the tooltip meanings are simply unreachable. Folded shut, because
 * the column header already says "May dial" and the labels do most of the work.
 */
function BadgeGlossary() {
  const entries: { key: string; term: React.ReactNode; definition: string }[] = [
    ...(Object.keys(DIRECTION) as Direction[]).map((key) => ({
      key,
      term: (
        <Badge variant={DIRECTION[key].variant} size="sm" icon={DIRECTION[key].icon}>
          {DIRECTION[key].label}
        </Badge>
      ),
      definition: DIRECTION[key].meaning,
    })),
    {
      key: 'transit',
      term: (
        <Tag variant="accent" size="sm" icon={<IconRelay />}>
          permitted
        </Tag>
      ),
      definition: TRANSIT.permitted,
    },
    {
      key: 'endpoint',
      term: (
        <Tag variant="neutral" size="sm" outline>
          endpoint only
        </Tag>
      ),
      definition: TRANSIT.endpoint,
    },
  ];

  return (
    <dl
      style={{
        display: 'grid',
        // The term column is sized by the widest badge rather than guessed, so
        // the definitions share one left edge without a magic width.
        gridTemplateColumns: 'auto 1fr',
        gap: 'var(--stratum-space-4) var(--stratum-space-8)',
        alignItems: 'baseline',
        margin: 0,
        fontSize: 'var(--stratum-text-xs)',
      }}
    >
      {entries.map((entry) => (
        <Fragment key={entry.key}>
          <dt>{entry.term}</dt>
          {/* `margin: 0` because the UA stylesheet indents `dd` by 40px, which
            * would push every definition off the grid column it was placed in. */}
          <dd style={{ margin: 0, color: 'var(--stratum-text-muted)' }}>{entry.definition}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

/** The peer the daemon says it would end up with, if it said. */
function resultPeer(payload: unknown): PeerConfig | null {
  const result = (payload as MutationResponse<{ peer?: PeerConfig }> | null)?.result;
  return result?.peer ?? null;
}

/**
 * Before and after, per field.
 *
 * This is what the confirm step is FOR. The values come from the daemon's own
 * check rather than from what the panel intended, so a value it would clamp,
 * default or reject shows up here instead of after the fact.
 */
function ChangeSummary({
  rows,
  note,
}: {
  rows: { label: string; from: string | null; to: string | null }[];
  note?: string;
}) {
  return (
    <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
      <div style={{ display: 'grid', gap: 'var(--stratum-space-4)' }}>
        {rows.map((r) => (
          <div
            key={r.label}
            style={{
              display: 'grid',
              gridTemplateColumns: 'minmax(6rem, auto) 1fr auto 1fr',
              gap: 'var(--stratum-space-6)',
              alignItems: 'center',
              fontSize: 'var(--stratum-text-sm)',
            }}
          >
            <span style={{ color: 'var(--stratum-text-subtle)' }}>{r.label}</span>
            <span style={{ color: 'var(--stratum-text-muted)' }}>
              {r.from ?? <Absent />}
            </span>
            <IconArrowRight aria-hidden />
            <strong>{r.to ?? <Absent>removed</Absent>}</strong>
          </div>
        ))}
      </div>
      {note ? (
        <span style={{ fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-muted)' }}>
          {note}
        </span>
      ) : null}
    </div>
  );
}

export function Peers() {
  const { revision, revisionRead, epoch, refresh } = useControl();
  const read = useDomainRead<PeersResponse>('peers', getPeers, [revision, epoch]);
  // Profiles come from the configuration, not a hardcoded list — a peer can
  // name one this panel has never heard of.
  const profilesRead = useDomainRead<XrayProfilesResponse>(
    'xray-profiles', getXrayProfiles, [revision, epoch],
  );
  const grantsRead = useDomainRead<NodeEgressGrantsResponse>(
    'node-egress-grants', getNodeEgressGrants, [revision, epoch],
  );
  const mutation = useMutationDialog();

  const [query, setQuery] = useState('');
  const [direction, setDirection] = useState<DirectionFilter>('all');
  const [detail, setDetail] = useState<PeerConfig | null>(null);
  const [editing, setEditing] = useState<PeerConfig | 'new' | null>(null);

  /*
   * Three states, not two, and every one is real:
   *   no response yet / failed   `known === null`  — nothing is known
   *   response, empty book       `known === []`    — a fact
   *   response, populated                          — a fact
   *
   * The presence of the RESPONSE decides read-vs-unread; the field inside it
   * decides empty-vs-populated. `peers` arrives as `null` for an empty address
   * book because Go marshals a nil slice that way.
   */
  const known = read.data ? (read.data.peers ?? []) : null;
  const peers = known ?? [];
  const grantsCurrent = revisionRead
    && grantsRead.failure === null
    && read.data?.revision === revision
    && grantsRead.data?.revision === revision;
  const grantFrom = (snapshot: NodeEgressGrantsResponse, peer: PeerConfig) => {
    const grants = snapshot.node_egress_grants;
    return Object.hasOwn(grants, peer.node_id) ? grants[peer.node_id] ?? null : null;
  };
  const grantFor = (peer: PeerConfig) => (
    grantsCurrent ? grantFrom(grantsRead.data!, peer) : null
  );
  const currentEditingPeer = editing !== null && editing !== 'new'
    ? peers.find((peer) => peer.name === editing.name) ?? editing
    : null;

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return peers.filter((p) => {
      if (direction !== 'all' && p.direction !== direction) return false;
      if (!q) return true;
      // Exactly the fields an operator types from memory. `node_id` is included
      // because it is what a path expression resolves — the value they will be
      // copying out of an error.
      return [p.name, p.display_name, p.node_id, p.addr]
        .filter(Boolean)
        .some((v) => v!.toLowerCase().includes(q));
    });
  }, [peers, query, direction]);

  const filtered = query.trim() !== '' || direction !== 'all';

  const columns: TableColumn<PeerConfig>[] = [
    {
      key: 'name',
      header: 'Peer',
      width: 190,
      cell: (p) => (
        <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
          <span style={{ fontWeight: 500 }}>{p.name}</span>
          {p.display_name && p.display_name !== p.name ? (
            <span
              style={{
                fontSize: 'var(--stratum-text-2xs)',
                color: 'var(--stratum-text-subtle)',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {p.display_name}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'node_id',
      header: 'Node ID',
      width: 170,
      /* `Code`, not `Identifier`. This string is never parsed, never validated
       * and has no key behind it — dressing it as a fingerprint would imply a
       * verification that happens nowhere in the backend. */
      cell: (p) => <Code variant="subtle">{p.node_id}</Code>,
    },
    {
      key: 'direction',
      header: 'May dial',
      width: 160,
      cell: (p) => {
        const d = DIRECTION[p.direction];
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
      key: 'addr',
      header: 'Address',
      width: 160,
      cell: (p) => (p.addr ? <Code variant="plain">{p.addr}</Code> : <Absent>not set</Absent>),
    },
    {
      key: 'transit',
      header: 'Transit',
      width: 130,
      cell: (p) =>
        p.nested_enabled ? (
          <Tooltip
            trigger={
              <Tag variant="accent" size="sm" icon={<IconRelay />}>
                permitted
              </Tag>
            }
          >
            {TRANSIT.permitted}
          </Tooltip>
        ) : (
          <Tooltip
            trigger={
              <Tag variant="neutral" size="sm" outline>
                endpoint only
              </Tag>
            }
          >
            {TRANSIT.endpoint}
          </Tooltip>
        ),
    },
    {
      key: 'profile',
      header: 'Profile',
      width: 120,
      /* `not set`, not a bare dash. A peer with no profile is a peer nobody
       * named one for — a fact about the configuration — and the glyph alone
       * reads as the unobserved marker used everywhere else in this panel for
       * something the daemon declined to report. Same wording as `addr`, which
       * is the same kind of absence. */
      cell: (p) =>
        p.xray_profile_id ? (
          <Tag size="sm" variant="neutral">
            {p.xray_profile_id}
          </Tag>
        ) : (
          <Absent>not set</Absent>
        ),
    },
    {
      key: 'egress',
      header: 'Node egress',
      width: 120,
      cell: (p) => {
        if (!grantsCurrent) return <Absent>not current</Absent>;
        if (p.direction === 'outbound') return <Absent>not applicable</Absent>;
        return grantFor(p) ? (
          <Tag size="sm" variant="accent">granted</Tag>
        ) : (
          <Tag size="sm" variant="neutral" outline>default deny</Tag>
        );
      },
    },
    {
      key: 'enabled',
      header: 'Enabled',
      width: 140,
      /* A Switch, because this is a decision an operator makes — not a reading
       * the system took. The cause sits underneath because "why" is the first
       * question anyone asks about a disabled peer. */
      cell: (p) => (
        <div style={{ display: 'grid', gap: '1px' }}>
          <Switch
            size="sm"
            checked={p.enabled}
            onCheckedChange={(next) =>
              mutation.open({
                operation: mutations.peerState(
                  p.name,
                  next,
                  next ? '' : 'disabled from the panel',
                ),
                title: next ? `Enable ${p.name}` : `Disable ${p.name}`,
                description: next
                  ? 'Marks the peer usable. Nothing is dialled.'
                  : 'Marks the peer unusable. Paths stop resolving; its node egress grant is retained.',
                confirmLabel: next ? 'Enable' : 'Disable',
                destructive: !next,
                summarise: (payload) => {
                  const peer = resultPeer(payload);
                  return (
                    <ChangeSummary
                      rows={[
                        {
                          label: 'Enabled',
                          from: p.enabled ? 'yes' : 'no',
                          to: peer ? (peer.enabled ? 'yes' : 'no') : next ? 'yes' : 'no',
                        },
                        ...(peer && !peer.enabled && peer.disabled_cause
                          ? [{ label: 'Recorded cause', from: p.disabled_cause || null, to: peer.disabled_cause }]
                          : []),
                      ]}
                      note={
                        next
                          ? 'Paths through this peer will resolve again.'
                          : 'Any path that traverses this peer stops resolving immediately. Its node egress grant remains saved.'
                      }
                    />
                  );
                },
              })
            }
            aria-label={`${p.enabled ? 'Disable' : 'Enable'} ${p.name}`}
          />
          {!p.enabled && p.disabled_cause ? (
            <span
              style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}
            >
              {p.disabled_cause}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'actions',
      header: '',
      headerLabel: 'Row actions',
      width: 48,
      align: 'end',
      cell: (p) => (
        <Menu
          trigger={
            <Button
              variant="ghost"
              size="xs"
              iconOnly
              icon={<IconMore />}
              aria-label={`Actions for ${p.name}`}
            />
          }
        >
          <MenuItem onSelect={() => setDetail(p)}>Inspect</MenuItem>
          <MenuItem closeOnSelect={false} onSelect={() => setEditing(p)}>
            Edit…
          </MenuItem>
          <MenuSeparator />
          <MenuItem
            danger
            icon={<IconTrash />}
            closeOnSelect={false}
            onSelect={() =>
              mutation.open({
                operation: mutations.peerRemove(p.name),
                title: `Remove ${p.name}`,
                description: 'Deletes the entry from the address book.',
                confirmLabel: 'Remove',
                destructive: true,
                summarise: () => (
                  <ChangeSummary
                    rows={[
                      { label: 'Peer', from: p.name, to: null },
                      { label: 'Node ID', from: p.node_id, to: null },
                    ]}
                    note={
                      `Any path naming ${p.node_id} stops resolving. `
                      + (grantFor(p)
                        ? 'Its node egress grant is revoked in the same commit. '
                        : 'Any attached node egress grant is removed in the same commit. ')
                      + 'This cannot be undone from the panel.'
                    }
                  />
                ),
              })
            }
          >
            Remove…
          </MenuItem>
        </Menu>
      ),
    },
  ];

  return (
    <Screen
      fill
      header={
        <PageHeader
          title="Peers"
          description="Who this node knows about, and what it is permitted to do with them."
          meta={
            known ? (
              <Badge variant="neutral" size="sm">
                {known.length} {known.length === 1 ? 'entry' : 'entries'}
              </Badge>
            ) : (
              <Badge variant="unknown" size="sm">
                not read
              </Badge>
            )
          }
          actions={
            <Button variant="primary" icon={<IconPlus />} onClick={() => setEditing('new')}>
              Add peer
            </Button>
          }
        />
      }
    >
      {/* Stated once, at the top, rather than implied by the absence of dots.
        * An operator arriving from any other mesh panel expects liveness here
        * and will read a quiet table as "everything is fine". */}
      <Banner
        variant="neutral"
        size="sm"
        title="Configured intent — nothing on this page is observed"
        dismissible
        storageKey="xtier.peers.intent-notice"
      >
        Every column comes from the configuration. The control plane reports no reachability,
        latency, session count or last-seen for peers, so this table cannot say whether any of them
        is up. <Code>Enabled</Code> is a decision someone typed, not a probe result.
      </Banner>

      {read.failure && (
        <FailureNotice
          failure={read.failure}
          actions={
            !read.failure.blocked ? (
              <Button size="sm" variant="default" onClick={() => void read.reload()}>
                Try again
              </Button>
            ) : undefined
          }
        />
      )}

      {grantsRead.failure && (
        <FailureNotice
          failure={grantsRead.failure}
          actions={
            !grantsRead.failure.blocked ? (
              <Button size="sm" variant="default" onClick={() => void grantsRead.reload()}>
                Try again
              </Button>
            ) : undefined
          }
        />
      )}

      {/* `fill` + `flex: 1`: the card takes whatever height the window has left
        * and its body scrolls inside it, instead of a 28rem table sitting on
        * top of several hundred pixels of empty page. `Card[data-fill]` already
        * declares `min-block-size: 0`, which is what lets it shrink past the
        * table's natural height — and what keeps it from contributing that
        * height to the page and re-introducing a document scroll. */}
      <Card variant="outlined" padding="none" fill style={{ flex: 1 }}>
        {/* `padding="sm"` is load-bearing: with the card at `padding="none"`
          * the header inherits none, and its title lands flush against the
          * border while every table cell does the same.
          *
          * The description is the one claim the removed legend was right to
          * make, said once and in prose. It is repeated here rather than left
          * to the banner above because that banner is dismissible and this is
          * not something an operator should be able to turn off — it is the
          * reason the table has no reachability column at all. */}
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="Address book"
          description="The control plane exposes no reachability, session count or last-seen for peers, so there is no column for them — that absence is a property of the API, not a reading it took."
          actions={
            known ? (
              <span
                style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}
              >
                {visible.length} of {known.length} shown
              </span>
            ) : undefined
          }
        />

        {/* The filter bar is its own band with its own rule — not crammed into
          * the header's action slot, where it fought the title for width. */}
        <div
          style={{
            display: 'flex',
            gap: 'var(--stratum-space-6)',
            alignItems: 'center',
            flexWrap: 'wrap',
            padding: 'var(--stratum-space-5) var(--stratum-space-6)',
            borderBlockEnd: '1px solid var(--stratum-border-subtle)',
          }}
        >
            <SearchInput
              size="sm"
              placeholder="Name, node ID, address…"
              value={query}
              onChange={(e) => setQuery(e.currentTarget.value)}
              onSearch={setQuery}
              aria-label="Filter peers"
              style={{ inlineSize: '15rem' }}
            />
            <SegmentedControl
              size="sm"
              label="May dial"
              value={direction}
              onValueChange={(v) => setDirection(v as DirectionFilter)}
              items={[
                { value: 'all', children: 'All' },
                { value: 'outbound', children: 'Outbound' },
                { value: 'inbound', children: 'Inbound' },
                { value: 'bidirectional', children: 'Both' },
              ]}
            />
            {filtered && (
              <>
                <Separator orientation="vertical" decorative style={{ blockSize: 18 }} />
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() => {
                    setQuery('');
                    setDirection('all');
                  }}
                >
                  Clear filters
                </Button>
              </>
            )}
        </div>

        {/* No `ScrollArea` of its own any more. Inside a `fill` card `CardBody`
          * is already the scrolling region, and it names that region from the
          * card's own title — two nested scroll boxes would put two tab stops
          * around one table. The sticky header still pins, because it pins to
          * the nearest scrollport and that is now the body's. */}
        <CardBody padding="none" scrollOrientation="both">
          <Table
            data={visible}
            columns={columns}
            rowKey={(p) => p.name}
            layout="fixed"
            /* `default`, not `compact`. Compact is 2px block padding — right
             * for a 10k-row log, wrong for rows carrying a name over a
             * subtitle, where it closes the gap between one peer's second
             * line and the next peer's first. */
            density="default"
            stickyHeader
            zebra
            loading={read.pristine && read.loading}
            onRowClick={(p) => setDetail(p)}
            caption="Each row is a written permission, not an observation"
            emptyState={
              !known ? (
                <EmptyState
                  title="Address book not read"
                  headingLevel={3}
                  description="The daemon did not return the peer list, so what is configured is unknown. This is not an empty address book."
                />
              ) : known.length === 0 ? (
                <EmptyState
                  title="No peers configured"
                  headingLevel={3}
                  description="Nothing to route through yet. Add a peer to give paths something to resolve."
                  actions={
                    <Button variant="primary" icon={<IconPlus />} onClick={() => setEditing('new')}>
                      Add peer
                    </Button>
                  }
                />
              ) : (
                <EmptyState
                  title="No peers match"
                  headingLevel={3}
                  description={`${known.length} configured, none matching the current filter.`}
                  actions={
                    <Button
                      variant="default"
                      onClick={() => {
                        setQuery('');
                        setDirection('all');
                      }}
                    >
                      Clear filters
                    </Button>
                  }
                />
              )
            }
          />
        </CardBody>

        {/* At the foot of the table it explains, and pinned there: a `fill`
          * card gives its footer `flex: 0 0 auto`, so opening the glossary
          * takes height from the scrolling body rather than from the page. */}
        <CardFooter padding="sm" align="start">
          <Disclosure
            size="sm"
            variant="plain"
            headingLevel={3}
            title="What the direction and transit badges mean"
            description="Definitions. Every one of them is a permission written in the configuration, not a state that was measured."
            style={{ flex: 1, minInlineSize: 0 }}
          >
            <BadgeGlossary />
          </Disclosure>
        </CardFooter>
      </Card>

      <PeerDetail peer={detail} onClose={() => setDetail(null)} />

      <Drawer
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
        side="right"
        size="lg"
        title={editing === 'new' ? 'Add peer' : `Edit ${editing?.name ?? ''}`}
        description={
          editing === 'new'
            ? 'Writes a new entry to the address book. Nothing is dialled.'
            : 'Changes the written record. Nothing is dialled.'
        }
      >
        {editing && (
          <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
            <PeerForm
              key={editing === 'new' ? 'new' : `${editing.name}:${read.data?.revision ?? 'unread'}`}
              peer={editing === 'new' ? null : currentEditingPeer}
              existing={peers}
              profiles={
                profilesRead.data ? Object.values(profilesRead.data.xray_profiles ?? {}) : null
              }
              hasNodeEgressGrant={
                editing === 'new'
                  ? false
                  : !grantsCurrent
                    ? null
                    : grantFor(currentEditingPeer!) !== null
              }
              onSubmit={(operation, title, revokesNodeEgressGrant) => {
                setEditing(null);
                mutation.open({
                  operation,
                  title,
                  description: revokesNodeEgressGrant
                    ? 'The direction change and grant revocation are one atomic configuration mutation.'
                    : undefined,
                  confirmLabel: 'Apply',
                  destructive: revokesNodeEgressGrant,
                });
              }}
              onCancel={() => setEditing(null)}
            />

            {editing !== 'new' && (
              <>
                <Separator decorative />
                {currentEditingPeer!.direction === 'outbound' ? (
                  <InlineMessage variant="info">
                    Node egress grants require an inbound or bidirectional peer. Change the peer
                    direction first, then reopen it to configure destinations.
                  </InlineMessage>
                ) : grantsRead.data === null && grantsRead.failure ? (
                  <FailureNotice
                    failure={grantsRead.failure}
                    actions={
                      !grantsRead.failure.blocked ? (
                        <Button size="sm" variant="default" onClick={() => void grantsRead.reload()}>
                          Try again
                        </Button>
                      ) : undefined
                    }
                  />
                ) : grantsRead.data === null ? (
                  <InlineMessage variant="info">Reading node egress grants at the current revision…</InlineMessage>
                ) : (
                  <PeerEgressGrantEditor
                    key={currentEditingPeer!.node_id}
                    peer={currentEditingPeer!}
                    grant={grantFrom(grantsRead.data, currentEditingPeer!)}
                    revision={grantsRead.data!.revision}
                    current={grantsCurrent}
                    onReview={(spec) => {
                      setEditing(null);
                      mutation.open(spec);
                    }}
                  />
                )}
              </>
            )}
          </div>
        )}
      </Drawer>

      <MutationDialog
        spec={mutation.spec}
        onClose={mutation.close}
        onApplied={() => void refresh()}
      />
    </Screen>
  );
}
