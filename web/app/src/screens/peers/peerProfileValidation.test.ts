import assert from 'node:assert/strict';
import test from 'node:test';
import {
  peerCredentialGroupOwners,
  peerProfileErrors,
  peerProfileOwner,
} from './peerProfileValidation.ts';

const profiles = [
  { id: 'node-link', kind: 'vless', credential_group_id: 'group-link' },
  { id: 'node-link-alias', kind: 'vless', credential_group_id: 'group-link' },
  { id: 'node-other', kind: 'vless', credential_group_id: 'group-other' },
  { id: 'terminal', kind: 'socks' },
];

test('every enabled peer requires a profile, including inbound-only peers', () => {
  assert.deepEqual(peerProfileErrors('', profiles, true), {
    xray_profile_id: 'Required for an enabled peer.',
  });
});

test('profile validation fails closed when profiles were not read', () => {
  assert.deepEqual(peerProfileErrors('node-link', null, true), {
    xray_profile_id: 'Profiles were not read, so this selection cannot be verified.',
  });
});

test('profile validation rejects unknown and non-VLESS profiles', () => {
  assert.deepEqual(peerProfileErrors('missing', profiles, true), {
    xray_profile_id: 'The configuration does not define profile missing.',
  });
  assert.deepEqual(peerProfileErrors('terminal', profiles, true), {
    xray_profile_id: 'Enabled peers require a VLESS profile; terminal is socks.',
  });
});

test('valid and optional empty profiles contribute no error keys', () => {
  const valid = peerProfileErrors(' node-link ', profiles, true);
  const optionalEmpty = peerProfileErrors('', profiles, false);
  assert.deepEqual(valid, {});
  assert.deepEqual(optionalEmpty, {});
  assert.equal(Object.keys(valid).length, 0);
  assert.equal(Object.keys(optionalEmpty).length, 0);
});

test('credential-group owners cover equivalent profile IDs and ignore disabled peers', () => {
  const peers = [
    {
      name: 'A', node_id: 'node-a', display_name: 'Alpha', direction: 'outbound' as const,
      xray_profile_id: 'node-link', nested_enabled: false, enabled: true, rendr_capable: true,
    },
    {
      name: 'B', node_id: 'node-b', direction: 'outbound' as const,
      xray_profile_id: 'node-other', nested_enabled: false, enabled: false, rendr_capable: true,
    },
  ];
  const owners = peerCredentialGroupOwners(peers, profiles);
  assert.equal(peerProfileOwner('node-link-alias', profiles, owners), 'Alpha');
  assert.equal(peerProfileOwner('node-other', profiles, owners), undefined);
});

test('owner lookup excludes the peer being edited but remains available for disabled-peer warnings', () => {
  const peers = [{
    name: 'A', node_id: 'node-a', direction: 'outbound' as const,
    xray_profile_id: 'node-link', nested_enabled: false, enabled: true, rendr_capable: true,
  }];
  const editingOwners = peerCredentialGroupOwners(peers, profiles, 'A');
  assert.equal(peerProfileOwner('node-link-alias', profiles, editingOwners), undefined);

  const disabledDraftOwners = peerCredentialGroupOwners(peers, profiles, 'disabled-draft');
  assert.equal(peerProfileOwner('node-link-alias', profiles, disabledDraftOwners), 'A');
});
