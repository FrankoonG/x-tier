import assert from 'node:assert/strict';
import test from 'node:test';
import { isVersionedReadCurrent } from './versionedRead.ts';

test('accepts a successful snapshot at the authoritative revision', () => {
  assert.equal(isVersionedReadCurrent(true, 7, {
    data: { revision: 7, value: 'current' },
    failure: null,
  }), true);
});

test('rejects the numeric zero placeholder before any revision was read', () => {
  assert.equal(isVersionedReadCurrent(false, 0, {
    data: { revision: 0 },
    failure: null,
  }), false);
});

test('rejects retained data after the authoritative revision advances', () => {
  assert.equal(isVersionedReadCurrent(true, 8, {
    data: { revision: 7 },
    failure: null,
  }), false);
});

test('rejects retained data after its refresh fails', () => {
  assert.equal(isVersionedReadCurrent(true, 7, {
    data: { revision: 7 },
    failure: { code: 'control.unreachable' },
  }), false);
});
