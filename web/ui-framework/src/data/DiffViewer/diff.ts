/* ---------------------------------------------------------------------------
 * Line and word diff.
 *
 * Implemented rather than imported: the whole framework has two optional peers
 * and neither is a diff library, and this has to work in a project that has
 * installed nothing but React.
 *
 * ALGORITHM
 * ---------
 * Myers' O(ND) greedy edit-graph search, from "An O(ND) Difference Algorithm
 * and Its Variations" (1986) — the same algorithm git uses. The naive DP LCS is
 * O(N*M) in both time and memory, which for two 5,000-line configs is 25M cells
 * before it renders a single row; Myers is O(N*D) where D is the number of
 * differences, so the common case of "a few lines changed in a large file" is
 * close to linear.
 *
 * Three things make it practical here:
 *
 *   1. Common prefix and suffix are trimmed before the search. For a config
 *      diff this usually removes 95% of the input, and it is what keeps D small
 *      enough for the trace to stay cheap.
 *   2. The V snapshot at step d is stored banded to radius d rather than at
 *      full width, so the trace costs O(D^2) ints instead of O(D * (N+M)).
 *   3. D is bounded. Past the bound the two texts are, for display purposes,
 *      unrelated, and the honest rendering is "this block was replaced" rather
 *      than a shuffle of coincidentally matching blank lines. The result is
 *      flagged `truncated` so the UI can say so instead of pretending.
 * ------------------------------------------------------------------------- */

export type DiffOpType = 'equal' | 'delete' | 'insert';

export interface DiffOp {
  type: DiffOpType;
  /** Half-open range in the "before" line array. */
  aStart: number;
  aEnd: number;
  /** Half-open range in the "after" line array. */
  bStart: number;
  bEnd: number;
}

export interface WordSegment {
  text: string;
  changed: boolean;
}

export interface DiffLine {
  text: string;
  /** 1-based line number on the before side. `null` where the line is new. */
  beforeNumber: number | null;
  /** 1-based line number on the after side. `null` where the line was removed. */
  afterNumber: number | null;
  /**
   * Word-level segmentation, present only where this line was paired with a
   * counterpart similar enough for the comparison to mean anything. `null` is
   * NOT "no words changed" — it is "no intra-line comparison was made".
   */
  segments: WordSegment[] | null;
}

export interface DiffEqualBlock {
  kind: 'equal';
  index: number;
  lines: DiffLine[];
}

export interface DiffChangeBlock {
  kind: 'change';
  index: number;
  deletions: DiffLine[];
  additions: DiffLine[];
}

export type DiffBlock = DiffEqualBlock | DiffChangeBlock;

export interface DiffResult {
  blocks: DiffBlock[];
  additions: number;
  deletions: number;
  /** The edit-distance bound was reached; the changed region is a whole-block
   *  replacement rather than a minimal edit script. */
  truncated: boolean;
  identical: boolean;
}

export interface DiffOptions {
  /**
   * Upper bound on the Myers search depth. Beyond it the changed region is
   * reported as a wholesale replacement. Default 1200, which costs at most
   * ~6 MB of trace in the pathological case.
   */
  maxEditDistance?: number;
  /** Compute per-word segments for paired changed lines. Default true. */
  wordDiff?: boolean;
  /**
   * Minimum token overlap before two paired lines are treated as edits of one
   * another rather than as unrelated. Default 0.3. Below it, no intra-line
   * highlighting is produced — marking two unrelated lines word-by-word
   * produces confetti, not information.
   */
  similarityThreshold?: number;
}

const DEFAULT_MAX_EDIT_DISTANCE = 1200;
const DEFAULT_SIMILARITY = 0.3;
/** Cap on tokens per side for the intra-line DP, after prefix/suffix trimming. */
const MAX_WORD_TOKENS = 400;

/* -------------------------------------------------------------------------- */
/* Line splitting                                                              */
/* -------------------------------------------------------------------------- */

/**
 * Splits text into lines.
 *
 * CRLF is normalised, and a single trailing newline does not produce a phantom
 * empty last line — otherwise every POSIX file would diff as having one more
 * line than it has.
 */
export function splitLines(text: string): string[] {
  const normalised = text.replace(/\r\n?/g, '\n');
  const lines = normalised.split('\n');
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/* -------------------------------------------------------------------------- */
/* Myers                                                                       */
/* -------------------------------------------------------------------------- */

function mergeOps(ops: DiffOp[]): DiffOp[] {
  const out: DiffOp[] = [];
  for (let i = 0; i < ops.length; i += 1) {
    const op = ops[i];
    if (!op) continue;
    if (op.aEnd === op.aStart && op.bEnd === op.bStart) continue;
    const last = out[out.length - 1];
    if (last && last.type === op.type) {
      last.aEnd = op.aEnd;
      last.bEnd = op.bEnd;
    } else {
      out.push({ ...op });
    }
  }
  return out;
}

function backtrack(trace: Int32Array[], n: number, m: number, dEnd: number): DiffOp[] {
  const reversed: DiffOp[] = [];
  let x = n;
  let y = m;

  for (let d = dEnd; d > 0; d -= 1) {
    const band = trace[d];
    if (!band) break;
    const k = x - y;

    // Mirrors the forward step: came from k+1 (a downward move, an insertion)
    // or from k-1 (a rightward move, a deletion).
    let prevK: number;
    if (k === -d) prevK = k + 1;
    else if (k === d) prevK = k - 1;
    else {
      const left = band[k - 1 + d] ?? 0;
      const right = band[k + 1 + d] ?? 0;
      prevK = left < right ? k + 1 : k - 1;
    }

    const prevX = band[prevK + d] ?? 0;
    const prevY = prevX - prevK;
    const midX = prevK === k + 1 ? prevX : prevX + 1;
    const midY = midX - k;

    if (x > midX) {
      reversed.push({ type: 'equal', aStart: midX, aEnd: x, bStart: midY, bEnd: y });
    }
    if (prevK === k + 1) {
      reversed.push({
        type: 'insert',
        aStart: prevX,
        aEnd: prevX,
        bStart: prevY,
        bEnd: prevY + 1,
      });
    } else {
      reversed.push({
        type: 'delete',
        aStart: prevX,
        aEnd: prevX + 1,
        bStart: prevY,
        bEnd: prevY,
      });
    }

    x = prevX;
    y = prevY;
  }

  if (x > 0) reversed.push({ type: 'equal', aStart: 0, aEnd: x, bStart: 0, bEnd: y });
  reversed.reverse();
  return reversed;
}

function diffCore(
  a: readonly string[],
  b: readonly string[],
  maxEditDistance: number,
): { ops: DiffOp[]; truncated: boolean } {
  const n = a.length;
  const m = b.length;

  if (n === 0 && m === 0) return { ops: [], truncated: false };
  if (n === 0) {
    return { ops: [{ type: 'insert', aStart: 0, aEnd: 0, bStart: 0, bEnd: m }], truncated: false };
  }
  if (m === 0) {
    return { ops: [{ type: 'delete', aStart: 0, aEnd: n, bStart: 0, bEnd: 0 }], truncated: false };
  }

  const max = Math.min(n + m, Math.max(1, maxEditDistance));
  const offset = max;
  const v = new Int32Array(2 * max + 2);
  const trace: Int32Array[] = [];

  for (let d = 0; d <= max; d += 1) {
    // Banded snapshot: only |k| <= d can be reached at step d, so storing the
    // full width would waste O(D * (N+M)).
    trace.push(v.slice(offset - d, offset + d + 1));

    for (let k = -d; k <= d; k += 2) {
      let x: number;
      if (k === -d || (k !== d && (v[offset + k - 1] ?? 0) < (v[offset + k + 1] ?? 0))) {
        x = v[offset + k + 1] ?? 0;
      } else {
        x = (v[offset + k - 1] ?? 0) + 1;
      }
      let y = x - k;

      while (x < n && y < m && a[x] === b[y]) {
        x += 1;
        y += 1;
      }
      v[offset + k] = x;

      if (x >= n && y >= m) {
        return { ops: backtrack(trace, n, m, d), truncated: false };
      }
    }
  }

  // Bound reached: report the region as replaced wholesale.
  return {
    ops: [
      { type: 'delete', aStart: 0, aEnd: n, bStart: 0, bEnd: 0 },
      { type: 'insert', aStart: n, aEnd: n, bStart: 0, bEnd: m },
    ],
    truncated: true,
  };
}

/** Line-level diff with common prefix and suffix trimmed first. */
export function diffLines(
  a: readonly string[],
  b: readonly string[],
  maxEditDistance = DEFAULT_MAX_EDIT_DISTANCE,
): { ops: DiffOp[]; truncated: boolean } {
  const n = a.length;
  const m = b.length;

  let prefix = 0;
  while (prefix < n && prefix < m && a[prefix] === b[prefix]) prefix += 1;

  let suffix = 0;
  while (
    suffix < n - prefix &&
    suffix < m - prefix &&
    a[n - 1 - suffix] === b[m - 1 - suffix]
  ) {
    suffix += 1;
  }

  const ops: DiffOp[] = [];
  if (prefix > 0) {
    ops.push({ type: 'equal', aStart: 0, aEnd: prefix, bStart: 0, bEnd: prefix });
  }

  const core = diffCore(a.slice(prefix, n - suffix), b.slice(prefix, m - suffix), maxEditDistance);
  for (let i = 0; i < core.ops.length; i += 1) {
    const op = core.ops[i];
    if (!op) continue;
    ops.push({
      type: op.type,
      aStart: op.aStart + prefix,
      aEnd: op.aEnd + prefix,
      bStart: op.bStart + prefix,
      bEnd: op.bEnd + prefix,
    });
  }

  if (suffix > 0) {
    ops.push({ type: 'equal', aStart: n - suffix, aEnd: n, bStart: m - suffix, bEnd: m });
  }

  return { ops: mergeOps(ops), truncated: core.truncated };
}

/* -------------------------------------------------------------------------- */
/* Intra-line                                                                  */
/* -------------------------------------------------------------------------- */

/**
 * Splits a line into words, whitespace runs and single punctuation marks.
 *
 * Punctuation is emitted one character at a time so that `a.b.c` -> `a.b.d`
 * highlights only `c`, and whitespace is kept as its own token so reindentation
 * shows up as a change rather than being silently absorbed.
 */
export function tokenizeLine(line: string): string[] {
  return line.match(/[\p{L}\p{N}_]+|\s+|[^\p{L}\p{N}_\s]/gu) ?? [];
}

export interface WordDiffResult {
  before: WordSegment[];
  after: WordSegment[];
  similarity: number;
}

/** A token that carries meaning — anything but a run of whitespace. */
function isSignificant(token: string): boolean {
  return token.length > 0 && token.trim().length > 0;
}

function countSignificant(tokens: readonly string[]): number {
  let count = 0;
  for (let i = 0; i < tokens.length; i += 1) {
    if (isSignificant(tokens[i] ?? '')) count += 1;
  }
  return count;
}

function pushSegment(out: WordSegment[], text: string, changed: boolean): void {
  if (!text) return;
  const last = out[out.length - 1];
  if (last && last.changed === changed) last.text += text;
  else out.push({ text, changed });
}

/**
 * Word-level diff of two single lines.
 *
 * Returns `null` when no meaningful comparison was made — either the lines are
 * too long for the bounded DP, or their token overlap is below the threshold,
 * meaning they are different lines rather than an edit of one line.
 */
export function diffWords(
  before: string,
  after: string,
  similarityThreshold = DEFAULT_SIMILARITY,
): WordDiffResult | null {
  const aTokens = tokenizeLine(before);
  const bTokens = tokenizeLine(after);
  // Two genuinely empty lines have nothing to compare.
  if (aTokens.length + bTokens.length === 0) return null;

  let prefix = 0;
  while (
    prefix < aTokens.length &&
    prefix < bTokens.length &&
    aTokens[prefix] === bTokens[prefix]
  ) {
    prefix += 1;
  }

  let suffix = 0;
  while (
    suffix < aTokens.length - prefix &&
    suffix < bTokens.length - prefix &&
    aTokens[aTokens.length - 1 - suffix] === bTokens[bTokens.length - 1 - suffix]
  ) {
    suffix += 1;
  }

  const aMid = aTokens.slice(prefix, aTokens.length - suffix);
  const bMid = bTokens.slice(prefix, bTokens.length - suffix);

  if (aMid.length > MAX_WORD_TOKENS || bMid.length > MAX_WORD_TOKENS) return null;

  const n = aMid.length;
  const m = bMid.length;
  const stride = m + 1;
  // Uint16 is enough: both sides are capped at MAX_WORD_TOKENS.
  const dp = new Uint16Array((n + 1) * stride);

  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      dp[i * stride + j] =
        aMid[i] === bMid[j]
          ? (dp[(i + 1) * stride + j + 1] ?? 0) + 1
          : Math.max(dp[(i + 1) * stride + j] ?? 0, dp[i * stride + j + 1] ?? 0);
    }
  }

  const beforeOut: WordSegment[] = [];
  const afterOut: WordSegment[] = [];

  // Similarity is measured over SIGNIFICANT tokens only. Counting whitespace
  // runs as shared material makes any two prose lines look 35-40% alike purely
  // because they both contain spaces, which sails past the threshold and
  // produces word-level confetti on lines that have nothing to do with one
  // another. Only words and punctuation count.
  let commonSignificant = 0;

  for (let i = 0; i < prefix; i += 1) {
    const token = aTokens[i] ?? '';
    pushSegment(beforeOut, token, false);
    pushSegment(afterOut, bTokens[i] ?? '', false);
    if (isSignificant(token)) commonSignificant += 1;
  }

  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (aMid[i] === bMid[j]) {
      const token = aMid[i] ?? '';
      pushSegment(beforeOut, token, false);
      pushSegment(afterOut, bMid[j] ?? '', false);
      if (isSignificant(token)) commonSignificant += 1;
      i += 1;
      j += 1;
    } else if ((dp[(i + 1) * stride + j] ?? 0) >= (dp[i * stride + j + 1] ?? 0)) {
      pushSegment(beforeOut, aMid[i] ?? '', true);
      i += 1;
    } else {
      pushSegment(afterOut, bMid[j] ?? '', true);
      j += 1;
    }
  }
  while (i < n) {
    pushSegment(beforeOut, aMid[i] ?? '', true);
    i += 1;
  }
  while (j < m) {
    pushSegment(afterOut, bMid[j] ?? '', true);
    j += 1;
  }

  for (let s = suffix; s > 0; s -= 1) {
    const token = aTokens[aTokens.length - s] ?? '';
    pushSegment(beforeOut, token, false);
    pushSegment(afterOut, bTokens[bTokens.length - s] ?? '', false);
    if (isSignificant(token)) commonSignificant += 1;
  }

  const significantTotal = countSignificant(aTokens) + countSignificant(bTokens);
  // Two all-whitespace lines are trivially comparable rather than unrelated.
  const similarity =
    significantTotal === 0 ? 1 : (2 * commonSignificant) / significantTotal;
  if (similarity < similarityThreshold) return null;

  return { before: beforeOut, after: afterOut, similarity };
}

/* -------------------------------------------------------------------------- */
/* Block assembly                                                              */
/* -------------------------------------------------------------------------- */

/**
 * Full diff, assembled into alternating equal and change blocks.
 *
 * Blocks rather than a flat line list because both the unified and the split
 * rendering, and the collapsing of unchanged regions, are all views of the same
 * structure — and because a change block is where a deletion run and an
 * insertion run get paired for intra-line comparison.
 */
export function buildDiff(
  beforeText: string,
  afterText: string,
  options: DiffOptions = {},
): DiffResult {
  const {
    maxEditDistance = DEFAULT_MAX_EDIT_DISTANCE,
    wordDiff = true,
    similarityThreshold = DEFAULT_SIMILARITY,
  } = options;

  const a = splitLines(beforeText);
  const b = splitLines(afterText);
  const { ops, truncated } = diffLines(a, b, maxEditDistance);

  const blocks: DiffBlock[] = [];
  let additions = 0;
  let deletions = 0;
  let index = 0;
  let cursor = 0;

  while (cursor < ops.length) {
    const op = ops[cursor];
    if (!op) {
      cursor += 1;
      continue;
    }

    if (op.type === 'equal') {
      const lines: DiffLine[] = [];
      for (let i = op.aStart; i < op.aEnd; i += 1) {
        lines.push({
          text: a[i] ?? '',
          beforeNumber: i + 1,
          afterNumber: op.bStart + (i - op.aStart) + 1,
          segments: null,
        });
      }
      blocks.push({ kind: 'equal', index: index++, lines });
      cursor += 1;
      continue;
    }

    // Gather the whole run of non-equal ops into one change block, so a
    // delete-then-insert and an insert-then-delete produce the same shape.
    const dels: DiffLine[] = [];
    const adds: DiffLine[] = [];
    while (cursor < ops.length) {
      const run = ops[cursor];
      if (!run || run.type === 'equal') break;
      if (run.type === 'delete') {
        for (let i = run.aStart; i < run.aEnd; i += 1) {
          dels.push({ text: a[i] ?? '', beforeNumber: i + 1, afterNumber: null, segments: null });
        }
      } else {
        for (let j = run.bStart; j < run.bEnd; j += 1) {
          adds.push({ text: b[j] ?? '', beforeNumber: null, afterNumber: j + 1, segments: null });
        }
      }
      cursor += 1;
    }

    deletions += dels.length;
    additions += adds.length;

    if (wordDiff && !truncated) {
      const pairs = Math.min(dels.length, adds.length);
      for (let p = 0; p < pairs; p += 1) {
        const del = dels[p];
        const add = adds[p];
        if (!del || !add) continue;
        const words = diffWords(del.text, add.text, similarityThreshold);
        if (words) {
          del.segments = words.before;
          add.segments = words.after;
        }
      }
    }

    blocks.push({ kind: 'change', index: index++, deletions: dels, additions: adds });
  }

  return {
    blocks,
    additions,
    deletions,
    truncated,
    identical: additions === 0 && deletions === 0,
  };
}

/* -------------------------------------------------------------------------- */
/* Row projection                                                              */
/* -------------------------------------------------------------------------- */

export type DiffRowType = 'equal' | 'add' | 'delete' | 'change' | 'collapsed';

export interface DiffUnifiedRow {
  type: DiffRowType;
  key: string;
  line?: DiffLine;
  /** Number of lines hidden, for a `collapsed` row. */
  hidden?: number;
  /** Block index the collapsed row belongs to, for the expand control. */
  blockIndex?: number;
}

export interface DiffSplitRow {
  type: DiffRowType;
  key: string;
  /** `null` means there is NO line on that side — a filler cell, never an
   *  empty line. Collapsing the two would silently invent content. */
  before: DiffLine | null;
  after: DiffLine | null;
  hidden?: number;
  blockIndex?: number;
}

interface CollapseOptions {
  context: number;
  expanded: ReadonlySet<number>;
  /** Minimum number of lines that must actually be hidden to bother. */
  minCollapse: number;
}

/** Head/tail slices of an equal block, or `null` when it stays whole. */
function planEqualBlock(
  block: DiffEqualBlock,
  isFirst: boolean,
  isLast: boolean,
  { context, expanded, minCollapse }: CollapseOptions,
): { head: DiffLine[]; hidden: number; tail: DiffLine[] } | null {
  if (expanded.has(block.index)) return null;
  // A leading equal block only needs the context immediately BEFORE the first
  // change; a trailing one only the context after the last.
  const head = isFirst ? 0 : context;
  const tail = isLast ? 0 : context;
  const hidden = block.lines.length - head - tail;
  if (hidden < minCollapse) return null;
  return {
    head: block.lines.slice(0, head),
    hidden,
    tail: tail > 0 ? block.lines.slice(block.lines.length - tail) : [],
  };
}

export function toUnifiedRows(blocks: readonly DiffBlock[], options: CollapseOptions): DiffUnifiedRow[] {
  const rows: DiffUnifiedRow[] = [];

  for (let bi = 0; bi < blocks.length; bi += 1) {
    const block = blocks[bi];
    if (!block) continue;

    if (block.kind === 'change') {
      for (let i = 0; i < block.deletions.length; i += 1) {
        const line = block.deletions[i];
        if (line) rows.push({ type: 'delete', key: `b${block.index}d${i}`, line });
      }
      for (let i = 0; i < block.additions.length; i += 1) {
        const line = block.additions[i];
        if (line) rows.push({ type: 'add', key: `b${block.index}a${i}`, line });
      }
      continue;
    }

    const plan = planEqualBlock(block, bi === 0, bi === blocks.length - 1, options);
    if (!plan) {
      for (let i = 0; i < block.lines.length; i += 1) {
        const line = block.lines[i];
        if (line) rows.push({ type: 'equal', key: `b${block.index}e${i}`, line });
      }
      continue;
    }

    plan.head.forEach((line, i) => rows.push({ type: 'equal', key: `b${block.index}h${i}`, line }));
    rows.push({
      type: 'collapsed',
      key: `b${block.index}c`,
      hidden: plan.hidden,
      blockIndex: block.index,
    });
    plan.tail.forEach((line, i) => rows.push({ type: 'equal', key: `b${block.index}t${i}`, line }));
  }

  return rows;
}

export function toSplitRows(blocks: readonly DiffBlock[], options: CollapseOptions): DiffSplitRow[] {
  const rows: DiffSplitRow[] = [];

  for (let bi = 0; bi < blocks.length; bi += 1) {
    const block = blocks[bi];
    if (!block) continue;

    if (block.kind === 'change') {
      const count = Math.max(block.deletions.length, block.additions.length);
      for (let i = 0; i < count; i += 1) {
        const before = block.deletions[i] ?? null;
        const after = block.additions[i] ?? null;
        rows.push({
          // A row with only one side is that side's change alone; the other
          // side is genuinely ABSENT, not blank, and is rendered as filler.
          type: before && after ? 'change' : before ? 'delete' : 'add',
          key: `b${block.index}p${i}`,
          before,
          after,
        });
      }
      continue;
    }

    const plan = planEqualBlock(block, bi === 0, bi === blocks.length - 1, options);
    if (!plan) {
      for (let i = 0; i < block.lines.length; i += 1) {
        const line = block.lines[i];
        if (line) rows.push({ type: 'equal', key: `b${block.index}e${i}`, before: line, after: line });
      }
      continue;
    }

    plan.head.forEach((line, i) =>
      rows.push({ type: 'equal', key: `b${block.index}h${i}`, before: line, after: line }),
    );
    rows.push({
      type: 'collapsed',
      key: `b${block.index}c`,
      before: null,
      after: null,
      hidden: plan.hidden,
      blockIndex: block.index,
    });
    plan.tail.forEach((line, i) =>
      rows.push({ type: 'equal', key: `b${block.index}t${i}`, before: line, after: line }),
    );
  }

  return rows;
}
