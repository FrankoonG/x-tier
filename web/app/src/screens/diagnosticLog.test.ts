import assert from 'node:assert/strict';
import test from 'node:test';
import type { JournalEntry } from '../api/control.ts';
import { toDiagnosticLine } from './diagnosticLog.ts';

const entry: JournalEntry = {
  id: 7,
  at: new Date('2026-08-24T00:00:00Z'),
  argv: ['local', 'identity', 'rename', 'Edge A'],
  method: 'PATCH',
  path: '/v1/domain/identity',
  revision: 4,
  dryRun: false,
  mutating: true,
  requestId: '0123456789abcdef0123456789abcdef',
  outcome: 'ok',
  exitCode: 0,
  durationMs: 12,
};

test('diagnostic rows show the real HTTP request and keep CLI as secondary copy data', () => {
  const line = toDiagnosticLine(entry, 'posix', {
    configPath: '/var/lib/xtier/config.json',
    controlAddr: '127.0.0.1:19090',
  });

  assert.equal(line.text, 'PATCH /v1/domain/identity  -> ok (12ms)');
  assert.equal(line.text.includes('xtierctl'), false);
  assert.match(line.copyText ?? '', /^xtierctl /);
  assert.match(line.copyText ?? '', /--revision 4/);
});

test('HTTP request identity remains visible when no CLI target is available', () => {
  const line = toDiagnosticLine(
    { ...entry, outcome: 'pending', durationMs: undefined },
    'powershell',
    null,
  );

  assert.equal(line.text, 'PATCH /v1/domain/identity ...');
  assert.match(line.copyText ?? '', /^# CLI equivalent unavailable:/);
});
