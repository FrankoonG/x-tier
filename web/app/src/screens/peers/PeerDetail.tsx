/* ===========================================================================
 * PEER DETAIL
 *
 * Two sections, and the split between them is the point.
 *
 *   WRITTEN DOWN     everything the config file says. Every value here is
 *                    `explicit` — an operator typed it — so none of it is
 *                    painted as a degradation or a fault.
 *
 *   CONFIRMED        what has actually been observed about this peer. The
 *                    honest answer is NOTHING: the control plane exposes no
 *                    probe, no dial result, no last-seen. Every cell reads
 *                    `unconfirmed`.
 *
 * A grid where every cell is unconfirmed looks, at first glance, like a bug.
 * It is the most accurate thing this screen can render, and it is worth the
 * space precisely because the alternative — quietly promoting a config
 * declaration into a capability — is how a panel starts telling operators that
 * a peer supports something nobody ever checked.
 *
 * `rendr_capable: true` in a config file is a CLAIM. Treating a claim as a
 * confirmation is the exact failure the rendr design notes call out: a
 * capability set must list only what is confirmed, and absence has to stay
 * ambiguous rather than resolving to "no".
 * ======================================================================== */
import {
  Badge,
  CapabilityGrid,
  Card,
  CardBody,
  CardHeader,
  Code,
  Drawer,
  InlineMessage,
  StateMatrix,
  Tag,
} from '@stratum/ui';
import type { PeerConfig } from '../../api/types';

export interface PeerDetailProps {
  peer: PeerConfig | null;
  onClose: () => void;
}

const DIRECTION_NOTE: Record<string, string> = {
  outbound: 'This node may open connections to the peer. The peer may not open connections here.',
  inbound: 'The peer may open connections to this node. This node may not dial out to it.',
  bidirectional: 'Either end may open a connection to the other.',
};

export function PeerDetail({ peer, onClose }: PeerDetailProps) {
  return (
    <Drawer
      open={peer !== null}
      onOpenChange={(open) => !open && onClose()}
      side="right"
      size="lg"
      title={peer ? `Peer ${peer.name}` : ''}
      description="Everything the configuration records, and everything it does not."
    >
      {peer && (
        <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title="Written down"
              description="From the config file. Readable with the daemon stopped."
              actions={
                <Badge variant={peer.enabled ? 'success' : 'neutral'}>
                  {peer.enabled ? 'enabled' : 'disabled'}
                </Badge>
              }
            />
            <CardBody>
              <StateMatrix
                label={`Configured record for ${peer.name}`}
                layout="stack"
                dimensions={[
                  {
                    key: 'name',
                    label: 'Name',
                    value: <Code variant="plain">{peer.name}</Code>,
                    note: 'The primary key. Every operation on this peer addresses it by this, and it is unique.',
                  },
                  {
                    key: 'node_id',
                    label: 'Node ID',
                    value: <Code variant="plain">{peer.node_id}</Code>,
                    note:
                      'An opaque string. It is never parsed, never validated and has no key '
                      + 'behind it — but it IS what path expressions resolve hops by, so a typo '
                      + 'here surfaces as an unknown-hop error rather than as a bad identity.',
                  },
                  {
                    key: 'display_name',
                    label: 'Display name',
                    value: peer.display_name || null,
                    note:
                      'A label for humans. Nothing resolves by it, and a new peer starts with it '
                      + 'set to the peer name.',
                  },
                  {
                    key: 'direction',
                    label: 'May dial',
                    value: peer.direction,
                    status: 'info',
                    explicit: true,
                    note: DIRECTION_NOTE[peer.direction],
                  },
                  {
                    key: 'addr',
                    label: 'Address',
                    value: peer.addr ? <Code variant="plain">{peer.addr}</Code> : null,
                    note: 'Where to dial. Absent means nobody wrote one down.',
                  },
                  {
                    key: 'gateway',
                    label: 'Gateway address',
                    value: peer.gateway_addr ? <Code variant="plain">{peer.gateway_addr}</Code> : null,
                    note:
                      'The address is copied into this field. They are one value in the backend, '
                      + 'not two, so setting the address sets both.',
                  },
                  {
                    key: 'nested',
                    label: 'Transit permission',
                    value: peer.nested_enabled ? 'may be an intermediate hop' : 'final hop only',
                    /* `info`, never `degraded`. A peer that cannot relay is not
                     * broken — it is configured, and it remains a perfectly
                     * good single-hop destination. */
                    status: 'info',
                    explicit: true,
                    note:
                      peer.nested_enabled
                        ? 'Paths may route onward through this peer.'
                        : 'Paths may end here but not pass through. This is a permission, not a fault.',
                  },
                  {
                    key: 'profile',
                    label: 'Transport profile',
                    value: peer.xray_profile_id ? <Tag size="sm">{peer.xray_profile_id}</Tag> : null,
                    explicit: Boolean(peer.xray_profile_id),
                    note: 'Chosen from the profiles this configuration defines.',
                  },
                  {
                    key: 'enabled',
                    label: 'Administrative state',
                    value: peer.enabled ? 'enabled' : `disabled — ${peer.disabled_cause ?? 'no cause recorded'}`,
                    status: peer.enabled ? 'ok' : 'inactive',
                    explicit: true,
                    note: 'A decision someone typed. Never derived from a dial result.',
                  },
                ]}
              />
            </CardBody>
          </Card>

          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title="Confirmed by observation"
              description="What has actually been checked about this peer."
            />
            <CardBody>
              <div style={{ display: 'grid', gap: 'var(--stratum-space-4)' }}>
                <InlineMessage variant="info">
                  Nothing here has been confirmed, because this control plane performs no peer
                  probing at all. That is different from a failed check — an unconfirmed
                  capability may work perfectly.
                </InlineMessage>
                <CapabilityGrid
                  label={`Confirmed capabilities for ${peer.name}`}
                  layout="detailed"
                  size="sm"
                  subjectHeader="Capability"
                  columns={[{ id: 'status', label: 'Status' }]}
                  subjects={[
                    {
                      id: 'reachable',
                      label: 'Reachable',
                      detail: peer.addr ? `configured at ${peer.addr}` : 'no address configured',
                      capabilities: {
                        status: {
                          state: 'unconfirmed',
                          note: 'No dial has been attempted. The API exposes no reachability for peers.',
                        },
                      },
                    },
                    {
                      id: 'rendr',
                      label: 'rendr transport',
                      detail: peer.rendr_capable
                        ? 'declared capable in the config'
                        : 'not declared in the config',
                      capabilities: {
                        status: {
                          state: 'unconfirmed',
                          note: peer.rendr_capable
                            ? 'The config claims this peer speaks rendr. Runtime status does not verify a specific remote peer.'
                            : 'The config makes no claim either way. Absence of a declaration is not evidence the peer lacks the capability.',
                        },
                      },
                    },
                    {
                      id: 'relay',
                      label: 'Relays for others',
                      detail: peer.nested_enabled ? 'permitted by config' : 'not permitted by config',
                      capabilities: {
                        status: peer.nested_enabled
                          ? {
                              state: 'unconfirmed',
                              note: 'Permitted, but no path has been established through it.',
                            }
                          : {
                              /* The one honest not-applicable on this screen:
                               * the config forbids it, so there is nothing to
                               * confirm. Not "unsupported" — the peer may well
                               * be capable; it is simply not allowed. */
                              state: 'not-applicable',
                              note: 'Configuration forbids this peer from being an intermediate hop, so the question does not arise.',
                            },
                      },
                    },
                    {
                      id: 'identity',
                      label: 'Identity verified',
                      detail: 'no key material recorded',
                      capabilities: {
                        status: {
                          state: 'unconfirmed',
                          note:
                            'Peers carry no public key in this configuration format. There is '
                            + 'nothing to verify against, so identity is unconfirmed by '
                            + 'construction rather than by circumstance.',
                        },
                      },
                    },
                  ]}
                />
              </div>
            </CardBody>
          </Card>
        </div>
      )}
    </Drawer>
  );
}
