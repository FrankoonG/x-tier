const NAME_PROHIBITED_ROLES = new Set([
  'caption',
  'code',
  'deletion',
  'emphasis',
  'generic',
  'insertion',
  'none',
  'paragraph',
  'presentation',
  'strong',
  'subscript',
  'superscript',
]);

/** Roles whose ARIA definition permits an author-provided accessible name. */
export function cardRoleAllowsAutomaticLabel(role: string | undefined): boolean {
  return role !== undefined && !NAME_PROHIBITED_ROLES.has(role);
}
