import assert from 'node:assert/strict';
import test from 'node:test';

import { adviseCode } from './errors.ts';

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
