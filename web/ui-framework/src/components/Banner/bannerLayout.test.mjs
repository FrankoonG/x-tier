import assert from 'node:assert/strict';
import test from 'node:test';

import { bannerGridColumns, bannerNarrowActionColumn } from './bannerLayout.ts';

test('an icon banner reserves a glyph column and wraps actions under its body', () => {
  assert.equal(bannerGridColumns(true), 'auto minmax(0, 1fr) auto');
  assert.equal(bannerNarrowActionColumn(true), '2 / -1');
});

test('an iconless banner starts both body and wrapped actions in the first column', () => {
  assert.equal(bannerGridColumns(false), 'minmax(0, 1fr) auto');
  assert.equal(bannerNarrowActionColumn(false), '1 / -1');
});
