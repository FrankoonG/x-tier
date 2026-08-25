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
