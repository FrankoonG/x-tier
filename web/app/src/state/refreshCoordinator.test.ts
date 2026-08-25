import assert from 'node:assert/strict';
import test from 'node:test';
import { createRefreshCoordinator } from './refreshCoordinator.ts';

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

test('coalesces slow reads instead of superseding them', async () => {
  const gate = deferred();
  let calls = 0;
  const coordinator = createRefreshCoordinator(async () => {
    calls += 1;
    await gate.promise;
  });

  const first = coordinator.refresh();
  const second = coordinator.refresh();
  const third = coordinator.refresh();
  await Promise.resolve();
  assert.equal(calls, 1);
  assert.strictEqual(second, first);
  assert.strictEqual(third, first);

  gate.resolve();
  await Promise.all([first, second, third]);
});

test('refreshFresh starts a new read after an older read settles', async () => {
  const gates = [deferred(), deferred()];
  let calls = 0;
  const coordinator = createRefreshCoordinator(async () => {
    const gate = gates[calls];
    calls += 1;
    assert.ok(gate);
    await gate.promise;
  });

  const older = coordinator.refresh();
  await Promise.resolve();
  const fresh = coordinator.refreshFresh();
  await Promise.resolve();
  assert.equal(calls, 1);

  gates[0]!.resolve();
  await older;
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
  assert.equal(calls, 2);

  gates[1]!.resolve();
  await fresh;
});
