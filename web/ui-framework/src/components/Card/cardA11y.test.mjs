import assert from 'node:assert/strict';
import test from 'node:test';
import { cardRoleAllowsAutomaticLabel } from './cardA11y.ts';

test('Card never automatically names absent or presentational roles', () => {
  assert.equal(cardRoleAllowsAutomaticLabel(undefined), false);
  for (const role of [
    'caption', 'code', 'deletion', 'emphasis', 'generic', 'insertion', 'none',
    'paragraph', 'presentation', 'strong', 'subscript', 'superscript',
  ]) {
    assert.equal(cardRoleAllowsAutomaticLabel(role), false, role);
  }
});

test('Card may automatically name a semantic role', () => {
  assert.equal(cardRoleAllowsAutomaticLabel('region'), true);
  assert.equal(cardRoleAllowsAutomaticLabel('group'), true);
});
