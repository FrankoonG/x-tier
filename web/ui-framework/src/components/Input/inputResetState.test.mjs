import assert from 'node:assert/strict';
import test from 'node:test';

import { observeFormReset } from './inputResetState.ts';

const afterDefaultAction = () => new Promise((resolve) => setTimeout(resolve, 0));

test('observes the value restored by a native form reset', async () => {
  const form = new EventTarget();
  const input = { form, value: 'edited' };
  const states = [];
  const stop = observeFormReset(input, (filled) => states.push(filled));

  form.dispatchEvent(new Event('reset'));
  input.value = '';
  await afterDefaultAction();
  assert.deepEqual(states, [false]);

  form.dispatchEvent(new Event('reset'));
  input.value = 'default value';
  await afterDefaultAction();
  assert.deepEqual(states, [false, true]);
  stop();
});

test('cleanup cancels a pending reset observation', async () => {
  const form = new EventTarget();
  const input = { form, value: 'edited' };
  let updates = 0;
  const stop = observeFormReset(input, () => {
    updates += 1;
  });

  form.dispatchEvent(new Event('reset'));
  stop();
  input.value = '';
  await afterDefaultAction();
  assert.equal(updates, 0);
});
