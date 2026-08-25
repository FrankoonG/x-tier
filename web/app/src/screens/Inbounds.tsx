/* ===========================================================================
 * INBOUNDS
 *
 * Local listeners for client proxying and node interconnects.
 *
 * THE KEY IS THE KIND, NOT THE ADDRESS
 * ------------------------------------
 * `inboundIndex` resolves by `kind`, so there is at most one listener of each
 * supported kind. `inbound set socks ...` rewrites the existing socks row; it
 * does not add a second one. There is no `remove` verb.
 *
 * The first version of this screen modelled listeners as a list keyed by
 * socket, with an Add button, a Remove button, and a uniqueness check on the
 * listen address. Every one of those was wrong, and the uniqueness check was
 * the worst of them: it passed a second socks listener on a different port —
 * certifying as safe the exact command that silently overwrites the first
 * one's address.
 *
 * So this screen shows one row per KIND, present or not, and editing a kind is
 * openly an overwrite.
 *
 * LAYOUT
 * ------
 * The table sits in the framework's table composition: `padding="none"` on the
 * card, an explicitly padded header, and a `padding="none"` body so the table's
 * own cell padding is the only inset. Omitting the header padding is what put
 * every cell one pixel from the card border.
 * ======================================================================== */
import { useState, type ReactNode } from 'react';
import {
  AddressInput,
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardHeader,
  Code,
  EmptyState,
  Field,
  FormActions,
  Hint,
  IconArrowBoth,
  IconClose,
  IconEdit,
  IconPlug,
  IconPlus,
  InlineMessage,
  PageHeader,
  ScrollArea,
  Screen,
  Select,
  StateMatrix,
  Switch,
  Table,
} from '@stratum/ui';
import type { SelectOption, TableColumn } from '@stratum/ui';
import type {
  InboundConfig,
  InboundsResponse,
  PeersResponse,
  XrayProfilesResponse,
} from '../api/types';
import { getInbounds, getPeers, getXrayProfiles, mutations } from '../api/control';
import { useControl } from '../state/store';
import { useDomainRead } from '../state/useDomainRead';
import { Absent } from '../components/Absent';
import { FailureNotice } from '../components/FailureNotice';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';

interface Kind {
  /** The primary key the backend indexes on, and the word its errors use. */
  kind: 'socks' | 'node-vless';
  profileKind?: 'socks';
  label: string;
  icon: ReactNode;
  /** What the socket speaks. Shown while configuring, not in the table. */
  blurb: string;
}

/** The two inbound kinds compiled by the current runtime slice. */
const KINDS: Kind[] = [
  {
    kind: 'socks',
    profileKind: 'socks',
    label: 'SOCKS5 CONNECT / TCP',
    icon: <IconPlug />,
    blurb: 'Authenticated SOCKS5 CONNECT over TCP, routed through one configured exit peer.',
  },
  {
    kind: 'node-vless',
    label: 'Node VLESS / TCP',
    icon: <IconArrowBoth />,
    blurb: 'Experimental plaintext VLESS/TCP for node interconnects on private networks only.',
  },
];

interface Row extends Kind {
  /** Absent when no listener of this kind is configured. */
  config?: InboundConfig;
}

const runtimeTag = (kind: Kind['kind']) =>
  kind === 'socks' ? 'xtier-user-socks' : 'xtier-node-vless';

export function Inbounds() {
  const { revision, epoch, refresh, daemon, daemonError } = useControl();
  const read = useDomainRead<InboundsResponse>('inbounds', getInbounds, [revision, epoch]);
  const profilesRead = useDomainRead<XrayProfilesResponse>(
    'xray-profiles', getXrayProfiles, [revision, epoch],
  );
  const peersRead = useDomainRead<PeersResponse>('peers', getPeers, [revision, epoch]);
  const mutation = useMutationDialog();

  const [editing, setEditing] = useState<Row | null>(null);
  const [listen, setListen] = useState('');
  const [profileID, setProfileID] = useState('');
  const [exitPeer, setExitPeer] = useState('');

  /*
   * Three states, not two:
   *   no response yet / failed   `configured === null`  — nothing is known
   *   response, nothing set up   `configured === []`    — a fact
   *   response, populated                               — a fact
   *
   * `null` from a nil Go slice means "none configured", which is a reading.
   * Only the absence of a RESPONSE means the panel does not know.
   */
  const configured = read.data ? (read.data.inbounds ?? []) : null;
  const profiles = profilesRead.data
    ? Object.values(profilesRead.data.xray_profiles ?? {})
    : null;
  const peers = peersRead.data ? (peersRead.data.peers ?? []) : null;
  const rows: Row[] = KINDS.map((k) => ({
    ...k,
    config: configured?.find((i) => i.kind === k.kind),
  }));
  const supportedConfigured = configured?.filter((inbound) =>
    KINDS.some((kind) => kind.kind === inbound.kind),
  );

  const eligibleExitPeers =
    profiles && peers
      ? peers.filter((peer) => {
          const profile = profiles.find((candidate) => candidate.id === peer.xray_profile_id);
          return peer.enabled && peer.direction !== 'inbound' && profile?.kind === 'vless';
        })
      : null;

  const admittedInboundPeers =
    profiles && peers
      ? peers.filter((peer) => {
          const profile = profiles.find((candidate) => candidate.id === peer.xray_profile_id);
          return peer.enabled && peer.direction !== 'outbound' && profile?.kind === 'vless';
        })
      : null;

  const selectedProfile = profiles?.find((profile) => profile.id === profileID);
  const profileError = !editing
    ? null
    : editing.kind !== 'socks'
      ? null
    : !profileID
      ? 'Select a SOCKS profile.'
      : profiles === null
        ? 'Profiles were not read, so this selection cannot be verified.'
        : selectedProfile?.kind !== editing.profileKind
          ? `${profileID} is not a ${editing.profileKind} profile.`
          : null;

  const resolvedExitPeer = eligibleExitPeers?.find(
    (peer) => peer.name === exitPeer || peer.node_id === exitPeer,
  );
  const exitPeerError =
    editing?.kind !== 'socks'
      ? null
      : !exitPeer
        ? 'Select an exit peer.'
        : eligibleExitPeers === null
          ? 'Peers and profiles must be read before the exit peer can be verified.'
          : !resolvedExitPeer
            ? `${exitPeer} is not an enabled, dialable peer with a VLESS profile.`
            : null;

  const profileOptions: SelectOption[] = editing?.kind === 'socks'
    ? (profiles ?? [])
        .filter((profile) => profile.kind === editing.profileKind)
        .map((profile) => ({
          value: profile.id,
          label: profile.id,
          description: 'SOCKS authentication',
        }))
    : [];
  if (editing?.kind === 'socks' && profileID && !profileOptions.some((option) => option.value === profileID)) {
    profileOptions.push({
      value: profileID,
      label: `${profileID} (${profiles === null ? 'profiles not read' : selectedProfile ? `${selectedProfile.kind}; incompatible` : 'undefined'})`,
      description: 'Current value; select a compatible profile before applying',
      disabled: true,
    });
  }

  const exitPeerOptions: SelectOption[] = (eligibleExitPeers ?? []).map((peer) => ({
    value: peer.name,
    label: peer.display_name || peer.name,
    description: `${peer.name} · ${peer.direction} · ${peer.xray_profile_id}`,
  }));
  if (editing?.kind === 'socks' && exitPeer && !exitPeerOptions.some((option) => option.value === exitPeer)) {
    const configuredPeer = peers?.find(
      (peer) => peer.name === exitPeer || peer.node_id === exitPeer,
    );
    exitPeerOptions.push({
      value: exitPeer,
      label: configuredPeer
        ? `${configuredPeer.display_name || configuredPeer.name} (currently configured)`
        : `${exitPeer} (undefined)`,
      description: resolvedExitPeer
        ? 'Current eligible exit peer'
        : 'Current value; select an enabled dialable VLESS peer before applying',
      disabled: !resolvedExitPeer,
    });
  }

  const formReady =
    Boolean(editing && listen.trim()) && profileError === null && exitPeerError === null;

  const canEnable = (config: InboundConfig): boolean => {
    const kind = KINDS.find((candidate) => candidate.kind === config.kind);
    if (kind?.kind === 'node-vless') return Boolean(admittedInboundPeers?.length);
    const profile = profiles?.find((candidate) => candidate.id === config.xray_profile_id);
    if (!kind || profile?.kind !== kind.profileKind) return false;
    return Boolean(
      eligibleExitPeers?.some(
        (peer) => peer.name === config.exit_peer || peer.node_id === config.exit_peer,
      ),
    );
  };

  const columns: TableColumn<Row>[] = [
    {
      key: 'kind',
      header: 'Kind',
      width: 220,
      cell: (r) => (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 'var(--stratum-space-6)',
            minWidth: 0,
          }}
        >
          <span
            aria-hidden="true"
            style={{ display: 'flex', color: 'var(--stratum-text-subtle)', flexShrink: 0 }}
          >
            {r.icon}
          </span>
          <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
            <span style={{ fontWeight: 500 }}>{r.label}</span>
            {/* The slug, because it is what the configuration file and every
              * `config.inbound_unknown` error name this listener. */}
            <Code variant="subtle">{r.kind}</Code>
          </div>
        </div>
      ),
    },
    {
      key: 'listen',
      header: 'Listen address',
      /* Deliberately unsized: this is the column that takes the card's spare
       * width. It is the only one here whose content is unbounded and
       * operator-supplied — a bracketed IPv6 literal or a long hostname runs
       * well past the fixed enums either side of it — and it is the value an
       * operator scans this table for. Kind, bind scope and the switch all have
       * knowable maxima, so slack given to them would only be padding. */
      cell: (r) => {
        if (!r.config) return <Absent>not configured</Absent>;
        // Configured but addressless is a different fact from not configured.
        return r.config.listen ? <Code variant="plain">{r.config.listen}</Code> : <Absent />;
      },
    },
    {
      key: 'runtime',
      header: 'Runtime bind',
      width: 170,
      cell: (r) => {
        if (!r.config) return <Absent>not set up</Absent>;
        if (!r.config.enabled) return <Badge size="sm" variant="neutral">disabled</Badge>;
        if (daemonError) return <Badge size="sm" variant="unknown">not observed</Badge>;
        if (!daemon) return <Badge size="sm" variant="unknown">not read</Badge>;
        const observed = daemon.xray.inbounds.find((item) => item.tag === runtimeTag(r.kind));
        if (!observed) return <Badge size="sm" variant="danger" dot>missing</Badge>;
        const variant = observed.state === 'bound'
          ? 'success'
          : observed.state === 'missing'
            ? 'danger'
            : 'warning';
        return <Badge size="sm" variant={variant} dot>{observed.state}</Badge>;
      },
    },
    {
      key: 'routing',
      header: 'Profile / exit',
      width: 220,
      cell: (r) => {
        if (!r.config) return <Absent>not set up</Absent>;
        return (
          <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
            {r.kind === 'node-vless' ? (
              <Code variant="subtle">Peer credentials</Code>
            ) : r.config.xray_profile_id ? (
              <Code variant="subtle">{r.config.xray_profile_id}</Code>
            ) : (
              <Absent>profile missing</Absent>
            )}
            {r.kind === 'socks' ? (
              r.config.exit_peer ? <Hint>via {r.config.exit_peer}</Hint> : <Hint>exit missing</Hint>
            ) : (
              <Hint>{admittedInboundPeers?.length ?? 0} admitted</Hint>
            )}
          </div>
        );
      },
    },
    {
      key: 'enabled',
      header: 'Configured',
      width: 160,
      cell: (r) =>
        r.config ? (
          <div style={{ display: 'grid', gap: '1px' }}>
            <Switch
              size="sm"
              checked={r.config.enabled}
              disabled={!r.config.enabled && !canEnable(r.config)}
              aria-label={`${r.config.enabled ? 'Disable' : 'Enable'} the ${r.kind} listener`}
              onCheckedChange={(next) =>
                mutation.open({
                  operation: mutations.inboundState(
                    r.kind,
                    next,
                    next ? '' : 'disabled from the panel',
                  ),
                  title: `${next ? 'Enable' : 'Disable'} the ${r.label} listener`,
                  description:
                    'Changes whether the daemon is configured to open this socket. Runtime status '
                    + 'reports the bind result after reconciliation.',
                  confirmLabel: next ? 'Enable' : 'Disable',
                  destructive: !next,
                })
              }
            />
            {!r.config.enabled && r.config.disabled_cause ? (
              <Hint>{r.config.disabled_cause}</Hint>
            ) : null}
            {!r.config.enabled && !canEnable(r.config) ? (
              <Hint>{r.kind === 'socks' ? 'Profile and exit required' : 'No admitted inbound peer'}</Hint>
            ) : null}
          </div>
        ) : (
          /* Not a bare marker either: this column is scanned on its own, so an
           * empty cell has to say why without the reader crossing the row. */
          <Absent>not set up</Absent>
        ),
    },
    {
      key: 'actions',
      header: '',
      headerLabel: 'Row actions',
      /* Sized to the widest label it can hold — "Set up" plus its icon — rather
       * than left to inherit the slack. End-aligned inside a lane this narrow
       * puts the control a few pixels past the row's last reading, and lines
       * the two buttons up as a column; end-aligned inside the ~620px the
       * trailing column used to absorb put it 570px away, aligned with nothing
       * and belonging to no row in particular. */
      width: 120,
      align: 'end',
      cell: (r) => (
        <Button
          size="sm"
          variant="default"
          icon={r.config ? <IconEdit /> : <IconPlus />}
          onClick={() => {
            setEditing(r);
            setListen(r.config?.listen ?? '');
            setProfileID(r.kind === 'socks' ? (r.config?.xray_profile_id ?? '') : '');
            setExitPeer(r.kind === 'socks' ? (r.config?.exit_peer ?? '') : '');
          }}
        >
          {r.config ? 'Change' : 'Set up'}
        </Button>
      ),
    },
  ];

  const readFailed = !configured && !read.pristine;

  return (
    <Screen
      header={
        <PageHeader
          title="Inbounds"
          description="Configured listeners for SOCKS5 CONNECT/TCP clients and private node VLESS/TCP interconnects."
          meta={
            configured ? (
              <Badge variant="neutral" size="sm">
                {supportedConfigured?.length ?? 0} of {KINDS.length} configured
              </Badge>
            ) : (
              <Badge variant="unknown" size="sm">
                not read
              </Badge>
            )
          }
        />
      }
    >
      <Banner variant="warning" size="sm" title="TCP only; UDP is unavailable">
        The SOCKS listener supports SOCKS5 CONNECT over TCP, not the full SOCKS5 protocol set;
        UDP ASSOCIATE is disabled. Node VLESS/TCP is experimental and plaintext, and is intended
        only for private networks.
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

      {profilesRead.failure && (
        <FailureNotice
          failure={profilesRead.failure}
          actions={
            !profilesRead.failure.blocked ? (
              <Button size="sm" variant="default" onClick={() => void profilesRead.reload()}>
                Try profiles again
              </Button>
            ) : undefined
          }
        />
      )}

      {peersRead.failure && (
        <FailureNotice
          failure={peersRead.failure}
          actions={
            !peersRead.failure.blocked ? (
              <Button size="sm" variant="default" onClick={() => void peersRead.reload()}>
                Try peers again
              </Button>
            ) : undefined
          }
        />
      )}

      {editing && (
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title={
              editing.config
                ? `Change the ${editing.label} listener`
                : `Set up the ${editing.label} listener`
            }
            description={editing.blurb}
            actions={
              <Button
                variant="ghost"
                size="sm"
                iconOnly
                icon={<IconClose />}
                aria-label="Stop configuring this listener"
                onClick={() => setEditing(null)}
              />
            }
          />
          <CardBody>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (!formReady) return;
                mutation.open({
                  operation: mutations.inboundPut({
                    kind: editing.kind,
                    listen: listen.trim(),
                    xrayProfileId: editing.kind === 'socks' ? profileID : undefined,
                    exitPeer: editing.kind === 'socks' ? exitPeer : undefined,
                  }),
                  title: `${editing.config ? 'Change' : 'Set up'} the ${editing.label} listener`,
                  description: editing.config
                    ? `Rewrites the existing ${editing.kind} listener without changing whether it is enabled. There is only one per kind.`
                    : `Adds the ${editing.kind} listener.`,
                  confirmLabel: 'Apply',
                });
              }}
              style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}
            >
              {/* `set` is an overwrite, and saying so is the whole difference
                * between this form and the one it replaced. */}
              {editing.config && (
                <InlineMessage variant="warning">
                  This replaces the existing {editing.kind} listener on{' '}
                  <Code>{editing.config.listen}</Code>. There is one listener per kind, so this is
                  an overwrite rather than an addition.
                </InlineMessage>
              )}

              <Field
                label="Listen address"
                required
                description="Bind a loopback address unless clients on other hosts need to reach it."
              >
                <AddressInput
                  accept={['hostport']}
                  value={listen}
                  onChange={setListen}
                  placeholder="127.0.0.1:1080"
                  required
                />
              </Field>

              {editing.kind === 'socks' && (
                <Field
                  label="SOCKS authentication profile"
                  required
                  error={profileError ?? undefined}
                  description="Only kind=socks profiles are accepted. The runtime requires username/password authentication."
                >
                  <Select
                    options={profileOptions}
                    value={profileID || ''}
                    onChange={(value) => setProfileID(value ?? '')}
                    placeholder="Select a SOCKS profile"
                    emptyLabel="No SOCKS profiles available"
                    disabled={profiles === null}
                    invalid={Boolean(profileError)}
                    fullWidth
                    aria-label="Inbound transport profile"
                  />
                </Field>
              )}

              {editing.kind === 'node-vless' && (
                <InlineMessage variant="info">
                  Admission uses each inbound or bidirectional peer's VLESS profile.
                </InlineMessage>
              )}

              {editing.kind === 'socks' && (
                <Field
                  label="Exit peer"
                  required
                  error={exitPeerError ?? undefined}
                  description="Only enabled outbound or bidirectional peers backed by a VLESS profile are eligible."
                >
                  <Select
                    options={exitPeerOptions}
                    value={exitPeer || ''}
                    onChange={(value) => setExitPeer(value ?? '')}
                    placeholder="Select an exit peer"
                    emptyLabel="No eligible VLESS exit peers"
                    disabled={eligibleExitPeers === null}
                    invalid={Boolean(exitPeerError)}
                    fullWidth
                    aria-label="SOCKS exit peer"
                  />
                </Field>
              )}

              <InlineMessage variant="warning" size="xs">
                {editing.kind === 'socks'
                  ? 'SOCKS5 CONNECT over TCP only. UDP ASSOCIATE is unavailable.'
                  : 'Experimental node interconnect VLESS/TCP. Traffic is plaintext; use only on private networks.'}
              </InlineMessage>

              {/* `inbound set` hardcodes `Enabled = true` (cli.go:540), so a
                * "create disabled" control would promise something the backend
                * cannot honour. */}
              <InlineMessage variant="info" size="xs">
                Applying this always leaves the listener enabled. To have one configured but closed,
                apply it and then switch it off — two changes, two revisions.
              </InlineMessage>

              <FormActions align="end" divider>
                <Button type="button" variant="subtle" onClick={() => setEditing(null)}>
                  Cancel
                </Button>
                <Button type="submit" variant="primary" disabled={!formReady}>
                  Review change
                </Button>
              </FormActions>
            </form>
          </CardBody>
        </Card>
      )}

      <Card variant="outlined" padding="none">
        {/* `padding="sm"` is load-bearing: with the card at `padding="none"`
          * the header inherits none, and its title lands flush against the
          * border while every table cell does the same. */}
        <CardHeader padding="sm" headingLevel={2} title="Listeners" />

        <CardBody padding="none">
          {readFailed ? (
            <div style={{ padding: 'var(--stratum-space-12)' }}>
              {/* Not "no listeners configured". The read failed, so the panel
                * does not know what is configured — and asserting emptiness
                * from a failed read is the same class of lie as asserting
                * health from an absent probe.
                *
                * This branch replaces the table rather than filling its
                * `emptyState`: there is always one row per kind, so the table
                * is never empty and would otherwise render three rows claiming
                * "not configured" on the strength of a read that never landed. */}
              <EmptyState
                title="Listeners not read"
                headingLevel={3}
                description="The daemon did not return the inbound list, so what is configured is unknown. This is not an empty set of listeners."
              />
            </div>
          ) : (
            <ScrollArea orientation="both" maxHeight="24rem" label="Inbound listeners">
              <Table
                data={rows}
                columns={columns}
                rowKey={(r) => r.kind}
                /* `auto`, not `fixed`, and the reason is specific: under
                 * `fixed` the Table DROPS the trailing column's width so that
                 * column absorbs the container's spare width (Table.tsx, the
                 * `absorbsSlack` branch). That is right when the last column is
                 * prose — Identity's "what it means" relies on it — and wrong
                 * here, where the last column is the action column. `width: 150`
                 * was a no-op: at 1600px the column inherited ~620px and parked
                 * "Change" far from the row it rewrites.
                 *
                 * Nothing on this table is pinned, which is the one thing
                 * `fixed` is genuinely required for, and the sticky header
                 * stays aligned either way — head and body are one <table>
                 * sharing one <colgroup>, so the browser computes a single set
                 * of column widths for both regardless of layout mode. The row
                 * set is the two supported kinds and never grows, reorders or arrives
                 * late, so there is no re-measure for auto layout to twitch on.
                 * Every column is sized except the listen address, which leaves
                 * exactly one place for the slack to land. */
                layout="auto"
                density="default"
                stickyHeader
                zebra
                loading={read.pristine && read.loading}
                caption="One row per supported kind, configured or not"
              />
            </ScrollArea>
          )}
        </CardBody>
      </Card>

      <Card variant="outlined">
        <CardHeader
          headingLevel={2}
          title="Listener coverage"
          description="Configured listeners and the latest bind state observed by the daemon."
        />
        <CardBody>
          <StateMatrix
            label="Reporting coverage for inbound listeners"
            layout="grid"
            size="sm"
            dimensions={[
              {
                key: 'configured',
                label: 'Configured to listen',
                value: configured
                  ? `${supportedConfigured?.filter((i) => i.enabled).length ?? 0} of ${KINDS.length}`
                  : null,
                status: 'info',
                explicit: Boolean(configured),
              },
              {
                key: 'bound',
                label: 'Actually bound',
                value: daemon
                  ? String(daemon.xray.inbounds.filter((item) => item.state === 'bound').length)
                  : null,
                status: daemon?.xray.inbounds.some((item) => item.state !== 'bound')
                  ? 'degraded'
                  : 'ok',
                note: daemonError
                  ? 'The daemon was not observed.'
                  : 'Counted from the runtime inbound registry.',
              },
              {
                key: 'missing',
                label: 'Missing or unexpected',
                value: daemon
                  ? String(daemon.xray.inbounds.filter((item) => item.state !== 'bound').length)
                  : null,
                status: daemon?.xray.inbounds.some((item) => item.state !== 'bound')
                  ? 'degraded'
                  : 'ok',
              },
              {
                key: 'clients',
                label: 'Connected clients',
                value: null,
                note: 'No per-listener connection count exists in this API.',
              },
              {
                key: 'traffic',
                label: 'Traffic',
                value: null,
                note: 'No per-listener byte or session counters are exposed.',
              },
            ]}
          />
        </CardBody>
      </Card>

      <MutationDialog
        spec={mutation.spec}
        onClose={mutation.close}
        onApplied={() => {
          setEditing(null);
          setListen('');
          setProfileID('');
          setExitPeer('');
          void refresh();
        }}
      />
    </Screen>
  );
}
