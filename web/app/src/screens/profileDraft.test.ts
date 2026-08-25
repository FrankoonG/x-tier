import assert from 'node:assert/strict';
import test from 'node:test';
import { discardProfileDraft, type ProfileDraft } from './profileDraft.ts';

test('discarding a profile preview removes every draft value including the credential', () => {
  const secret = 'credential-that-must-not-survive-cancel';
  const populated: ProfileDraft = {
    kind: 'socks',
    id: 'client-egress',
    username: 'operator',
    credential: secret,
    submitted: true,
  };

  const discarded = discardProfileDraft();

  assert.deepEqual(discarded, {
    kind: 'vless',
    id: '',
    username: '',
    credential: '',
    submitted: false,
  });
  assert.equal(JSON.stringify(discarded).includes(secret), false);
  assert.notStrictEqual(discarded, populated);
});
