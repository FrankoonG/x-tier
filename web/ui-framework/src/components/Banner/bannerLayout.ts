export function bannerGridColumns(hasIcon: boolean): string {
  return hasIcon ? 'auto minmax(0, 1fr) auto' : 'minmax(0, 1fr) auto';
}

export function bannerNarrowActionColumn(hasIcon: boolean): string {
  return hasIcon ? '2 / -1' : '1 / -1';
}
