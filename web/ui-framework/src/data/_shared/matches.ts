/* ---------------------------------------------------------------------------
 * Text-match ranges.
 *
 * Shared by LogViewer and JsonViewer so highlighting behaves identically in
 * both. Kept as pure range arithmetic — no React, no DOM — because the same
 * ranges are computed over text that may then be split across several styled
 * spans (an ANSI run, a JSON key) and must survive that split unchanged.
 * ------------------------------------------------------------------------- */

export interface MatchRange {
  /** Inclusive start offset in the source string. */
  start: number;
  /** Exclusive end offset. */
  end: number;
}

/** Upper bound on matches per string, so a one-character query over a very long
 *  line cannot produce a hundred thousand DOM nodes. */
const MAX_MATCHES_PER_STRING = 200;

/**
 * Literal, non-overlapping substring matches.
 *
 * `haystack` is expected to be pre-lowercased by the caller when
 * `caseSensitive` is false — for a 100k-line log the lowercasing must happen
 * once per line, not once per keystroke, so it cannot live in here.
 */
export function findLiteralRanges(
  haystack: string,
  needle: string,
  { sourceLength }: { sourceLength?: number } = {},
): MatchRange[] {
  if (!needle) return [];
  const limit = sourceLength ?? haystack.length;
  const out: MatchRange[] = [];
  let from = 0;
  while (out.length < MAX_MATCHES_PER_STRING) {
    const i = haystack.indexOf(needle, from);
    if (i === -1 || i >= limit) break;
    out.push({ start: i, end: Math.min(i + needle.length, limit) });
    from = i + needle.length;
  }
  return out;
}

/** Convenience wrapper that lowercases inline. Use only on short strings. */
export function findMatchRanges(text: string, query: string, caseSensitive = false): MatchRange[] {
  if (!query) return [];
  return caseSensitive
    ? findLiteralRanges(text, query)
    : findLiteralRanges(text.toLowerCase(), query.toLowerCase(), { sourceLength: text.length });
}

/**
 * Matches from a regular expression.
 *
 * The expression is cloned with the global flag and `lastIndex` is advanced
 * manually so a zero-length match (`a*`, `^`) cannot spin forever — a real
 * hazard when the pattern comes from a text box the operator is still typing.
 */
export function findRegexRanges(text: string, source: RegExp): MatchRange[] {
  const flags = source.flags.includes('g') ? source.flags : `${source.flags}g`;
  let re: RegExp;
  try {
    re = new RegExp(source.source, flags);
  } catch {
    return [];
  }

  const out: MatchRange[] = [];
  let guard = 0;
  for (;;) {
    const m = re.exec(text);
    if (!m) break;
    const start = m.index;
    const end = start + m[0].length;
    if (end > start) out.push({ start, end });
    re.lastIndex = end > start ? end : start + 1;
    if (++guard >= MAX_MATCHES_PER_STRING || re.lastIndex > text.length) break;
  }
  return out;
}

/**
 * Compiles a filter expression, falling back to a literal search when the
 * pattern is not yet valid.
 *
 * Returning `invalid` rather than throwing lets the caller keep showing results
 * for the last good pattern and mark the input — an input that clears the view
 * on every intermediate keystroke of `[a-` is unusable.
 */
export function compileFilter(
  query: string,
  mode: 'text' | 'regex',
  caseSensitive: boolean,
): { regex: RegExp | null; invalid: boolean } {
  if (mode !== 'regex' || !query) return { regex: null, invalid: false };
  try {
    return { regex: new RegExp(query, caseSensitive ? 'g' : 'gi'), invalid: false };
  } catch {
    return { regex: null, invalid: true };
  }
}

export interface RangePiece {
  start: number;
  end: number;
  match: boolean;
}

/**
 * Splits the half-open interval `[start, end)` against a sorted, non-overlapping
 * range list, returning alternating matched and unmatched pieces.
 *
 * This is what lets a highlight cross an ANSI colour boundary: the colour runs
 * and the match runs are two independent partitions of the same string and both
 * have to be honoured.
 */
export function splitByRanges(start: number, end: number, ranges: MatchRange[]): RangePiece[] {
  if (end <= start) return [];
  if (ranges.length === 0) return [{ start, end, match: false }];

  const pieces: RangePiece[] = [];
  let cursor = start;

  for (let i = 0; i < ranges.length; i += 1) {
    const r = ranges[i];
    if (!r) continue;
    if (r.end <= cursor) continue;
    if (r.start >= end) break;

    const mStart = Math.max(r.start, cursor);
    const mEnd = Math.min(r.end, end);
    if (mStart > cursor) pieces.push({ start: cursor, end: mStart, match: false });
    if (mEnd > mStart) pieces.push({ start: mStart, end: mEnd, match: true });
    cursor = mEnd;
    if (cursor >= end) break;
  }

  if (cursor < end) pieces.push({ start: cursor, end, match: false });
  return pieces;
}
