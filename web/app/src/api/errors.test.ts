import assert from 'node:assert/strict';
import test from 'node:test';

import { CommandFailure } from './control.ts';
import { adviseCode, describeFailure } from './errors.ts';

test('route compiler public codes all have operator guidance', () => {
  for (const code of [
    'path.edge_disabled',
    'topology.local_missing',
    'route.terminal_mismatch',
    'route.session_kind_mismatch',
    'route.instance_mismatch',
  ]) {
    const advice = adviseCode(code);
    assert.notEqual(advice.title, 'The command failed', code);
    assert.equal(advice.blocked, true, code);
  }
});

test('node egress grant public codes all have operator guidance', () => {
  for (const code of [
    'config.node_egress_grant_revoke_required',
    'config.node_egress_grant_source_required',
    'config.node_egress_grant_source_mismatch',
    'config.node_egress_grant_peer_unknown',
    'config.node_egress_grant_peer_inbound_required',
    'config.node_egress_grant_network_invalid',
    'config.node_egress_grant_cidr_invalid',
    'config.node_egress_grant_cidr_duplicate',
    'config.node_egress_grant_ports_required',
    'config.node_egress_grant_port_invalid',
    'config.node_egress_grant_invalid',
    'config.node_egress_grant_unknown',
  ]) {
    const advice = adviseCode(code);
    assert.notEqual(advice.title, 'The command failed', code);
    assert.equal(advice.blocked, true, code);
  }
});

test('peer runtime profile refusals all have operator guidance', () => {
  for (const code of [
    'config.peer_identity_required',
    'config.peer_profile_required',
    'config.peer_profile_incompatible',
    'config.peer_inbound_profile_incompatible',
    'config.peer_credential_duplicate',
    'config.peer_credential_quarantined',
    'config.peer_credential_invalid',
    'config.peer_credential_quarantine_fingerprint_invalid',
    'config.peer_credential_quarantine_duplicate',
    'config.peer_credential_quarantine_reason_invalid',
    'config.peer_credential_quarantine_peers_required',
    'config.peer_credential_quarantine_peer_invalid',
    'config.peer_credential_quarantine_peer_duplicate',
    'config.peer_credential_quarantine_record_missing',
    'config.peer_credential_quarantine_reason_conflict',
    'config.peer_credential_quarantine_write',
    'config.peer_credential_quarantine_invalid',
    'config.peer_credential_ledger_read',
    'config.peer_credential_ledger_decode',
    'config.peer_credential_ledger_version',
    'config.peer_credential_ledger_invalid',
    'config.peer_credential_ledger_missing',
    'config.peer_credential_ledger_mismatch',
    'config.peer_credential_ledger_stale',
    'config.peer_credential_ledger_merge',
    'config.peer_credential_ledger_write',
    'config.peer_credential_ledger_directory',
    'config.peer_credential_ledger_encode',
    'config.last_good_credential_ledger',
  ]) {
    const advice = adviseCode(code);
    assert.notEqual(advice.title, 'The command failed', code);
    assert.equal(advice.blocked, true, code);
  }
});

test('last-known-good restore safety failures all have operator guidance', () => {
  for (const code of [
    'config.restore_backup_list',
    'config.restore_backup_read',
    'config.restore_backup_revision_unavailable',
    'config.restore_schema_newer',
    'config.restore_active_quarantine_invalid',
    'config.restore_quarantine_merge',
    'config.restore_quarantine_apply',
    'config.restore_credential_ledger_unavailable',
    'config.restore_credential_ledger_missing',
    'config.restore_revision_high_water_unavailable',
    'config.restore_revision_reserve',
    'config.restore_write',
    'config.recovery_active_unavailable',
    'config.revision_high_water_read',
    'config.revision_high_water_decode',
    'config.revision_high_water_version',
    'config.revision_high_water_negative',
    'config.revision_high_water_write',
    'config.revision_high_water_encode',
  ]) {
    const advice = adviseCode(code);
    assert.notEqual(advice.title, 'The command failed', code);
    assert.equal(advice.blocked, true, code);
  }
});

test('committed-revision barrier failures all have operator guidance', () => {
  for (const code of [
    'service.reload_apply',
    'service.reload_applied_unhealthy',
    'service.reload_not_applied',
    'service.reload_result_invalid',
    'service.reload_canceled',
    'service.committed_revision_invalid',
  ]) {
    const advice = adviseCode(code);
    assert.notEqual(advice.title, 'The command failed', code);
    assert.equal(advice.blocked, true, code);
    assert.equal(advice.needsRefresh, true, code);
  }
});

test('error guidance never overrides the daemon mutation outcome', () => {
  const refused = describeFailure(new CommandFailure(
    'config.commit_visible_and_resynced',
    'synthetic refusal',
    1,
    'not_applied',
  ));
  assert.equal(refused.applied, false);
  assert.equal(refused.title.includes('applied'), false);

  const landed = describeFailure(new CommandFailure(
    'domain.failed',
    'synthetic post-commit error',
    1,
    'applied',
  ));
  assert.equal(landed.applied, true);
});
