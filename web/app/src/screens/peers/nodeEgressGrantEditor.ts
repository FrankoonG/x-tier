import { mutations, type DomainMutation } from '../../api/control.ts';
import type { NodeEgressGrant, PeerConfig } from '../../api/types.ts';

export type NodeEgressGrantEditorMode = 'not_applicable' | 'create' | 'replace';

export function nodeEgressGrantEditorMode(
  peer: PeerConfig,
  grant: NodeEgressGrant | null,
): NodeEgressGrantEditorMode {
  if (peer.direction === 'outbound') return 'not_applicable';
  return grant === null ? 'create' : 'replace';
}

export function nodeEgressGrantPutOperation(
  peer: PeerConfig,
  replacement: NodeEgressGrant,
): DomainMutation {
  return mutations.nodeEgressGrantPut(peer.name, {
    sourceNodeId: replacement.source_node_id,
    network: replacement.network,
    allowCIDRs: replacement.allow_cidrs,
    allowPrivateCIDRs: replacement.allow_private_cidrs,
    denyCIDRs: replacement.deny_cidrs,
    allowPorts: replacement.allow_ports,
  });
}

export function nodeEgressGrantRevokeOperation(peer: PeerConfig): DomainMutation {
  return mutations.nodeEgressGrantRevoke(peer.name, peer.node_id);
}
