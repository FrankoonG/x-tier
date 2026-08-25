import assert from 'node:assert/strict';
import test from 'node:test';
import { measureScrollArea } from './scrollAreaState.ts';

const fits = {
  scrollTop: 0,
  scrollLeft: 0,
  scrollHeight: 100,
  scrollWidth: 100,
  clientHeight: 100,
  clientWidth: 100,
};

test('vertical ScrollArea ignores overflow on its hidden horizontal axis', () => {
  assert.deepEqual(measureScrollArea({ ...fits, scrollWidth: 300 }, 'vertical'), {
    scrollable: false,
    top: false,
    bottom: false,
    start: false,
    end: false,
  });
});

test('horizontal ScrollArea ignores overflow on its hidden vertical axis', () => {
  assert.deepEqual(measureScrollArea({ ...fits, scrollHeight: 300 }, 'horizontal'), {
    scrollable: false,
    top: false,
    bottom: false,
    start: false,
    end: false,
  });
});

test('both axes report only the edges that still have content beyond them', () => {
  assert.deepEqual(
    measureScrollArea({
      ...fits,
      scrollTop: 50,
      scrollLeft: 40,
      scrollHeight: 300,
      scrollWidth: 250,
    }, 'both'),
    { scrollable: true, top: true, bottom: true, start: true, end: true },
  );
});

test('an allowed overflowing axis makes the viewport scrollable and exposes its far edge', () => {
  assert.deepEqual(measureScrollArea({ ...fits, scrollHeight: 300 }, 'vertical'), {
    scrollable: true,
    top: false,
    bottom: true,
    start: false,
    end: false,
  });
  assert.deepEqual(measureScrollArea({ ...fits, scrollWidth: 300 }, 'horizontal'), {
    scrollable: true,
    top: false,
    bottom: false,
    start: false,
    end: true,
  });
});
