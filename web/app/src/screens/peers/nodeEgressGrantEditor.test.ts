import assert from 'node:assert/strict';
import test from 'node:test';
import type { NodeEgressGrant, PeerConfig } from '../../api/types.ts';
import {
  nodeEgressGrantEditorMode,
  nodeEgressGrantPutOperation,
  nodeEgressGrantRevokeOperation,
} from './nodeEgressGrantEditor.ts';

const peer = (direction: PeerConfig['direction']): PeerConfig => ({
  name: 'edge-b',
  display_name: 'edge-b',
  node_id: 'node-b',
  addr: 'edge-b.example:443',
  direction,
  xray_profile_id: 'vless-main',
  nested_enabled: false,
  enabled: true,
  disabled_cause: '',
  gateway_addr: 'edge-b.example:443',
  rendr_capable: true,
});

const grant: NodeEgressGrant = {
  source_node_id: 'node-b',
  network: 'tcp',
  allow_cidrs: ['203.0.113.0/24'],
  allow_private_cidrs: [],
  deny_cidrs: ['203.0.113.7/32'],
  allow_ports: [{ from: 443, to: 443 }],
};

test('grant editor distinguishes initial, replacement, and outbound states', () => {
  assert.equal(nodeEgressGrantEditorMode(peer('inbound'), null), 'create');
  assert.equal(nodeEgressGrantEditorMode(peer('bidirectional'), grant), 'replace');
  assert.equal(nodeEgressGrantEditorMode(peer('outbound'), grant), 'not_applicable');
});

test('grant editor composes full replacement and revoke Domain API operations', () => {
  const target = peer('inbound');
  const put = nodeEgressGrantPutOperation(target, grant);
  assert.equal(put.method, 'PUT');
  assert.equal(put.path, '/v1/domain/node-egress-grants');
  assert.deepEqual(put.body, grant);

  const revoke = nodeEgressGrantRevokeOperation(target);
  assert.equal(revoke.method, 'DELETE');
  assert.equal(revoke.path, '/v1/domain/node-egress-grants');
  assert.deepEqual(revoke.body, { source_node_id: 'node-b' });
});
