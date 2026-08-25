/* ===========================================================================
 * THE PATH EXPRESSION, AS A DOCUMENT
 *
 * The expression string is the canonical form, because it is exactly what the
 * daemon takes. Everything else in the editor — the ladder, the inspector, the
 * diagnostics — is a projection of it. That is the whole architecture, and it
 * exists to kill one class of bug: an editor that holds a structural model
 * *and* a string has to defend a round trip forever, and the day it loses a
 * character is the day it eats someone's half-typed work.
 *
 * TWO RULES MAKE THAT SAFE
 * ------------------------
 * 1. THE PARSE IS A PARTITION. `tokenize` returns spans that tile [0, len)
 *    with no gaps and no overlaps, and a span stores offsets, never text. So
 *    `render(tokenize(s)) === s` is an identity, not a property to be tested —
 *    there is no code path that could drop a character, because nothing
 *    anywhere holds a copy of one.
 *
 * 2. STRUCTURAL EDITS EMIT SPLICES, NOT RE-SERIALISATIONS. Dragging a path
 *    rewrites one range. It never rebuilds the string, so whitespace the
 *    operator typed survives every structural gesture, and one gesture is one
 *    undo step.
 *
 * THE GRAMMAR, FROM THE SOURCE
 * ----------------------------
 * Two separators at two levels, and they do not nest:
 *
 *   ","        splits PATHS.  cli.go splitCSV: split, TrimSpace, drop empties.
 *   "/" or "\" splits HOPS.   route.splitPath: FieldsFunc, TrimSpace, drop empties.
 *
 * Consequences that this module has to model exactly, because each one is a
 * place where a plausible guess is wrong:
 *
 *   - `hops[0]` is ALWAYS the local node and is NEVER written. A one-hop
 *     expression is a two-hop path.
 *   - Empty segments VANISH. "a//b" is the same path as "a/b", and "a,,b" is
 *     TWO paths, not three. They still occupy text, so the editor shows them
 *     as ignored rather than pretending they were never typed.
 *   - Whitespace is trimmed at the EDGES of a segment only. FieldsFunc does
 *     not split on spaces, so "a b" is one hop whose node ID contains a space.
 *     It will not resolve, but that is the resolver's answer to give, not this
 *     module's — a tokenizer that "helpfully" split on the space would be
 *     inventing a grammar the daemon does not have.
 * ======================================================================== */

/** What a span covers. The kinds tile the source with no gaps. */
export type SpanKind =
  /** A node ID as written, already trimmed of its own edge whitespace. */
  | 'hop'
  /** A "/" or "\". */
  | 'sep-hop'
  /** A ",". */
  | 'sep-path'
  /** Whitespace, or an empty segment's worth of nothing between separators. */
  | 'space';

export interface Span {
  kind: SpanKind;
  start: number;
  end: number;
  /** Index into `Parsed.paths` of the path this span belongs to. */
  path: number;
  /** For `hop` spans: index within that path's `hops`. Otherwise -1. */
  hop: number;
}

export interface HopToken {
  start: number;
  end: number;
  /** The trimmed text. This is the node ID the daemon will match on. */
  text: string;
}

export interface PathToken {
  /**
   * Position among the paths the DAEMON will see, or -1 for a segment that is
   * empty and will therefore be dropped by splitCSV before it ever arrives.
   *
   * The distinction matters for peak, whose candidate is the last path the
   * daemon sees — not the last segment that happens to be in the text.
   */
  ordinal: number;
  /**
   * Segment bounds, EXCLUDING the commas on either side but INCLUDING the
   * segment's own leading and trailing whitespace.
   *
   * Whitespace belongs to the segment so that a reorder can rebuild a region
   * by joining raw segments with "," and preserve exactly what was typed.
   */
  start: number;
  end: number;
  raw: string;
  /** Non-empty hops, in order. Empty segments are absent, as the daemon drops them. */
  hops: HopToken[];
  /** Where an empty hop segment was dropped, so the editor can show it as ignored. */
  ignored: { start: number; end: number }[];
}

export interface Parsed {
  source: string;
  /** Every segment, INCLUDING empty ones. Use `ordinal >= 0` for what compiles. */
  paths: PathToken[];
  spans: Span[];
}

const isHopSep = (c: string) => c === '/' || c === '\\';
const isSpace = (c: string) => c === ' ' || c === '\t' || c === '\n' || c === '\r';

/**
 * Partition the source.
 *
 * Deliberately a single left-to-right pass with no lookahead and no
 * backtracking: the grammar has no nesting, so anything cleverer would only be
 * a place for the partition invariant to break.
 */
export function tokenize(source: string): Parsed {
  const spans: Span[] = [];
  const paths: PathToken[] = [];

  let pathIndex = 0;
  let ordinal = 0;
  let cursor = 0;

  // One iteration per path segment. The loop runs once more than the number of
  // commas, so a trailing comma yields a final empty segment — which is real
  // text the operator can see and delete.
  for (;;) {
    let end = cursor;
    while (end < source.length && source[end] !== ',') end += 1;

    const segStart = cursor;
    const segEnd = end;
    const raw = source.slice(segStart, segEnd);

    const hops: HopToken[] = [];
    const ignored: { start: number; end: number }[] = [];

    // Walk the segment, splitting on hop separators.
    let i = segStart;
    while (i <= segEnd) {
      let j = i;
      while (j < segEnd && !isHopSep(source[j] as string)) j += 1;

      // [i, j) is one hop segment. Trim its edges; the trimmed core is the ID.
      let a = i;
      let b = j;
      while (a < b && isSpace(source[a] as string)) a += 1;
      while (b > a && isSpace(source[b - 1] as string)) b -= 1;

      if (a > i) spans.push({ kind: 'space', start: i, end: a, path: pathIndex, hop: -1 });
      if (b > a) {
        spans.push({ kind: 'hop', start: a, end: b, path: pathIndex, hop: hops.length });
        hops.push({ start: a, end: b, text: source.slice(a, b) });
      } else if (j > i || (j === i && segEnd > segStart)) {
        // An empty hop segment. It occupies no characters of its own beyond the
        // whitespace already emitted, but the editor still wants to know a slot
        // was written and dropped.
        ignored.push({ start: a, end: b });
      }
      if (b < j) spans.push({ kind: 'space', start: b, end: j, path: pathIndex, hop: -1 });

      if (j < segEnd) {
        spans.push({ kind: 'sep-hop', start: j, end: j + 1, path: pathIndex, hop: -1 });
        i = j + 1;
        continue;
      }
      break;
    }

    const compiles = hops.length > 0;
    paths.push({
      ordinal: compiles ? ordinal : -1,
      start: segStart,
      end: segEnd,
      raw,
      hops,
      ignored,
    });
    if (compiles) ordinal += 1;
    pathIndex += 1;

    if (end >= source.length) break;
    spans.push({ kind: 'sep-path', start: end, end: end + 1, path: -1, hop: -1 });
    cursor = end + 1;
  }

  return { source, paths, spans };
}

/**
 * The identity that makes this model safe. Kept as an exported function rather
 * than only a test so a caller can assert it cheaply in development.
 */
export function render(parsed: Parsed): string {
  const ordered = [...parsed.spans].sort((a, b) => a.start - b.start);
  let out = '';
  let at = 0;
  for (const s of ordered) {
    // A gap would mean the partition is broken. Copy it rather than lose it:
    // being wrong about highlighting is survivable, losing text is not.
    if (s.start > at) out += parsed.source.slice(at, s.start);
    out += parsed.source.slice(s.start, s.end);
    at = Math.max(at, s.end);
  }
  if (at < parsed.source.length) out += parsed.source.slice(at);
  return out;
}

/** The expressions the daemon will actually see, after splitCSV drops empties. */
export function compilablePaths(parsed: Parsed): PathToken[] {
  return parsed.paths.filter((p) => p.ordinal >= 0);
}

/** The canonical form: exactly what the daemon parses out of the source. */
export function canonical(parsed: Parsed): string {
  return compilablePaths(parsed)
    .map((p) => p.hops.map((h) => h.text).join('/'))
    .join(',');
}

/* -- Splices --------------------------------------------------------------- */

export interface Splice {
  start: number;
  end: number;
  text: string;
}

export const applySplice = (source: string, s: Splice): string =>
  source.slice(0, s.start) + s.text + source.slice(s.end);

/**
 * Where the caret lands after a splice.
 *
 * The third case is the one that matters: a caret INSIDE the replaced range has
 * no corresponding position afterwards, so it goes to the end of the
 * replacement rather than to an arbitrary offset inside it. Everything else is
 * a shift.
 */
export function transformCaret(caret: number, s: Splice): number {
  if (caret <= s.start) return caret;
  if (caret >= s.end) return caret + (s.text.length - (s.end - s.start));
  return s.start + s.text.length;
}

/** Which path segment the caret is sitting in, or -1. */
export function pathAtCaret(parsed: Parsed, caret: number): number {
  for (let i = 0; i < parsed.paths.length; i += 1) {
    const p = parsed.paths[i] as PathToken;
    // Inclusive at both ends so a caret on a separator belongs to the segment
    // it is leaving, which is what makes "type a comma then keep going" feel
    // like it stayed in the same place.
    if (caret >= p.start && caret <= p.end) return i;
  }
  return parsed.paths.length ? parsed.paths.length - 1 : -1;
}

export function replaceHop(parsed: Parsed, path: number, hop: number, value: string): Splice {
  const h = parsed.paths[path]?.hops[hop];
  if (!h) throw new Error(`replaceHop: no hop ${path}.${hop}`);
  return { start: h.start, end: h.end, text: value };
}

/**
 * Remove a hop, taking one adjacent separator with it so the result is not
 * left with a dangling "/" that silently becomes an ignored empty segment.
 * Removing the first hop eats the separator AFTER it; any other eats the one
 * BEFORE it.
 */
export function removeHop(parsed: Parsed, path: number, hop: number): Splice {
  const p = parsed.paths[path];
  const h = p?.hops[hop];
  if (!p || !h) throw new Error(`removeHop: no hop ${path}.${hop}`);
  if (p.hops.length === 1) return { start: p.start, end: p.end, text: '' };
  if (hop === 0) {
    const next = p.hops[1] as HopToken;
    return { start: h.start, end: next.start, text: '' };
  }
  const prev = p.hops[hop - 1] as HopToken;
  return { start: prev.end, end: h.end, text: '' };
}

export function insertHopAfter(parsed: Parsed, path: number, hop: number, value = ''): Splice {
  const p = parsed.paths[path];
  if (!p) throw new Error(`insertHopAfter: no path ${path}`);
  if (p.hops.length === 0) return { start: p.end, end: p.end, text: value };
  const h = p.hops[Math.min(hop, p.hops.length - 1)] as HopToken;
  return { start: h.end, end: h.end, text: `/${value}` };
}

export function addPath(parsed: Parsed, value = ''): Splice {
  const at = parsed.source.length;
  // A source that is empty or only separators gets the value alone, so the
  // first "add path" on a blank editor does not produce a leading comma.
  const needsComma = parsed.paths.some((p) => p.hops.length > 0);
  return { start: at, end: at, text: needsComma ? `,${value}` : value };
}

export function removePath(parsed: Parsed, path: number): Splice {
  const p = parsed.paths[path];
  if (!p) throw new Error(`removePath: no path ${path}`);
  if (parsed.paths.length === 1) return { start: p.start, end: p.end, text: '' };
  // Eat the comma that joined it: the one before, unless this is the first.
  if (path === 0) return { start: p.start, end: (parsed.paths[1] as PathToken).start, text: '' };
  return { start: (parsed.paths[path - 1] as PathToken).end, end: p.end, text: '' };
}

/**
 * Move a path, as ONE splice over the region the move disturbs.
 *
 * Rebuilding only that region — and rebuilding it from the raw segment texts —
 * is what keeps a drag from reformatting the rest of the line. Segments carry
 * their own whitespace, so joining them with "," reproduces what was typed.
 */
/**
 * Rewrite the segments into `order`, as ONE splice over the region the
 * permutation actually disturbs.
 *
 * Only the span between the first and last moved segment is rebuilt, so a
 * reorder at the bottom of a long expression leaves the top of the line
 * byte-identical — including whatever spacing was typed there.
 */
export function reorderPaths(parsed: Parsed, order: number[]): Splice {
  const n = parsed.paths.length;
  const noop: Splice = { start: 0, end: 0, text: '' };
  if (order.length !== n) return noop;

  let lo = 0;
  while (lo < n && order[lo] === lo) lo += 1;
  if (lo === n) return noop; // identity permutation
  let hi = n - 1;
  while (hi > lo && order[hi] === hi) hi -= 1;

  const text = order
    .slice(lo, hi + 1)
    .map((i) => (parsed.paths[i] as PathToken).raw)
    .join(',');

  return {
    start: (parsed.paths[lo] as PathToken).start,
    end: (parsed.paths[hi] as PathToken).end,
    text,
  };
}

export function movePath(parsed: Parsed, from: number, to: number): Splice {
  const n = parsed.paths.length;
  if (from === to || from < 0 || to < 0 || from >= n || to >= n) {
    return { start: 0, end: 0, text: '' };
  }
  const order = parsed.paths.map((_, i) => i);
  order.splice(to, 0, order.splice(from, 1)[0] as number);
  return reorderPaths(parsed, order);
}
