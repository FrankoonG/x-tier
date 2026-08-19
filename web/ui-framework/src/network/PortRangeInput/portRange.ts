/* ---------------------------------------------------------------------------
 * Port list parsing, normalisation and merging.
 *
 * Pure functions, zero imports. Reusable for firewall-rule diffing, config
 * export and table filtering, not only for the input in this folder.
 *
 * THE GRAMMAR
 * -----------
 *   list  := item (SEP item)*
 *   item  := port | port "-" port
 *   SEP   := "," | whitespace | ";"
 *
 * ERROR VERSUS WARNING — the distinction that makes this component usable
 * ----------------------------------------------------------------------
 * An INVERTED range (`8100-8000`) is an error: there is no defensible
 * interpretation, and silently swapping the endpoints would mean the operator's
 * typo becomes a rule they never reviewed.
 *
 * OVERLAPPING or DUPLICATE ranges (`80,80-90`) are warnings. They are perfectly
 * well defined — the union is unambiguous — and they arise naturally when
 * someone pastes two lists together. Refusing them would be pedantic, so they
 * are flagged and then resolved by normalisation.
 *
 * ADJACENCY
 * ---------
 * `mergePortRanges` also coalesces ADJACENT ranges: `80-90` plus `91-100`
 * becomes `80-100`. The set of ports is identical either way, and collapsing
 * gives one canonical spelling per set — which is what makes it possible to
 * compare two rules for equality without reasoning about how they were typed.
 * ------------------------------------------------------------------------- */

/** An inclusive range. A single port is a range where `start === end`. */
export interface PortRange {
  start: number;
  end: number;
}

export type PortRangeIssueCode =
  | 'required'
  | 'empty-item'
  | 'item-shape'
  | 'port-shape'
  | 'port-range'
  | 'port-zero'
  | 'inverted'
  | 'overlap'
  | 'duplicate'
  | 'too-many-items';

export interface PortRangeIssue {
  code: PortRangeIssueCode;
  /** `error` blocks; `warning` is resolved by normalisation. */
  severity: 'error' | 'warning';
  /** English default; override by code at the component level. */
  message: string;
  /** The offending fragment, where one could be isolated. */
  item?: string;
}

export interface PortListOptions {
  /** Permit port 0. Default `false` — 0 is "any free port", not a real port. */
  allowZero?: boolean;
  /** Lower bound. Default `0` when `allowZero`, otherwise `1`. */
  minPort?: number;
  /** Upper bound. Default `65535`. */
  maxPort?: number;
  /** Reject a list with more than this many items. Default: unlimited. */
  maxItems?: number;
}

export interface PortListResult {
  /** `true` when no `error`-severity issue was found. Warnings do not clear it. */
  ok: boolean;
  /** Ranges exactly as written, in input order, excluding unparseable items. */
  ranges: PortRange[];
  /** Sorted, de-duplicated, coalesced. The canonical spelling of the same set. */
  merged: PortRange[];
  /**
   * Total distinct ports covered. `null` when nothing parsed — an empty field
   * has an UNKNOWN port count, which is not the same fact as zero ports.
   */
  total: number | null;
  issues: PortRangeIssue[];
}

export const MIN_PORT_NUMBER = 0;
export const MAX_PORT_NUMBER = 65535;

function digitValue(code: number): number {
  return code >= 48 && code <= 57 ? code - 48 : -1;
}

/**
 * Parses one bare port.
 *
 * Leading zeros are rejected: `0080` is decimal 80 to most parsers and a syntax
 * error to others, and a firewall rule is a bad place for that ambiguity.
 */
function parseSinglePort(text: string): number | null {
  if (text.length === 0 || text.length > 5) return null;
  if (text.length > 1 && text.charCodeAt(0) === 48) return null;
  let n = 0;
  for (let i = 0; i < text.length; i += 1) {
    const d = digitValue(text.charCodeAt(i));
    if (d < 0) return null;
    n = n * 10 + d;
  }
  return n;
}

/** Splits on commas, semicolons and runs of whitespace, dropping blanks. */
function splitItems(text: string): { raw: string; trimmed: string }[] {
  const out: { raw: string; trimmed: string }[] = [];
  for (const raw of text.split(/[,;\s]+/)) {
    const trimmed = raw.trim();
    if (trimmed.length > 0) out.push({ raw, trimmed });
  }
  return out;
}

/**
 * Parses a comma-separated list of ports and ranges.
 *
 * Always returns whatever it could parse, even when some items failed, so an
 * input can show a running port total while the operator is still fixing one
 * bad entry rather than blanking out the moment anything is wrong.
 */
export function parsePortList(text: string, options: PortListOptions = {}): PortListResult {
  const allowZero = options.allowZero ?? false;
  const minPort = options.minPort ?? (allowZero ? MIN_PORT_NUMBER : 1);
  const maxPort = options.maxPort ?? MAX_PORT_NUMBER;
  const { maxItems } = options;

  const issues: PortRangeIssue[] = [];
  const ranges: PortRange[] = [];

  const items = splitItems(text);
  if (items.length === 0) {
    return { ok: false, ranges: [], merged: [], total: null, issues };
  }
  if (maxItems !== undefined && items.length > maxItems) {
    issues.push({
      code: 'too-many-items',
      severity: 'error',
      message: `At most ${maxItems} entries are allowed; found ${items.length}.`,
    });
  }

  const bad = (
    code: PortRangeIssueCode,
    message: string,
    item: string,
  ): void => {
    issues.push({ code, severity: 'error', message, item });
  };

  for (const { trimmed } of items) {
    const dash = trimmed.indexOf('-');

    if (dash < 0) {
      const port = parseSinglePort(trimmed);
      if (port === null) {
        bad('port-shape', `"${trimmed}" is not a port number.`, trimmed);
        continue;
      }
      if (port === 0 && !allowZero) {
        bad('port-zero', 'Port 0 means "any free port" and cannot appear in a list.', trimmed);
        continue;
      }
      if (port < minPort || port > maxPort) {
        bad('port-range', `Port ${port} is outside ${minPort}-${maxPort}.`, trimmed);
        continue;
      }
      ranges.push({ start: port, end: port });
      continue;
    }

    // Exactly one dash, with something on both sides.
    if (trimmed.indexOf('-', dash + 1) >= 0) {
      bad('item-shape', `"${trimmed}" has more than one "-".`, trimmed);
      continue;
    }
    const startText = trimmed.slice(0, dash);
    const endText = trimmed.slice(dash + 1);
    if (startText.length === 0 || endText.length === 0) {
      bad('item-shape', `"${trimmed}" needs a port on both sides of the "-".`, trimmed);
      continue;
    }

    const start = parseSinglePort(startText);
    const end = parseSinglePort(endText);
    if (start === null || end === null) {
      bad('port-shape', `"${trimmed}" is not a range of port numbers.`, trimmed);
      continue;
    }
    if ((start === 0 || end === 0) && !allowZero) {
      bad('port-zero', 'Port 0 means "any free port" and cannot appear in a range.', trimmed);
      continue;
    }
    if (start < minPort || start > maxPort || end < minPort || end > maxPort) {
      bad('port-range', `"${trimmed}" is outside ${minPort}-${maxPort}.`, trimmed);
      continue;
    }
    if (start > end) {
      // Not auto-corrected. A swapped range is a typo, and quietly reversing it
      // turns the typo into a rule nobody reviewed.
      bad('inverted', `"${trimmed}" runs backwards; write ${end}-${start}.`, trimmed);
      continue;
    }
    ranges.push({ start, end });
  }

  // Overlap and duplicate detection runs over what parsed, pairwise against the
  // ranges already accepted — the lists here are short by construction.
  for (let i = 0; i < ranges.length; i += 1) {
    const a = ranges[i]!;
    for (let j = 0; j < i; j += 1) {
      const b = ranges[j]!;
      if (a.start === b.start && a.end === b.end) {
        issues.push({
          code: 'duplicate',
          severity: 'warning',
          message: `${formatPortRange(a)} appears more than once.`,
          item: formatPortRange(a),
        });
        break;
      }
      if (a.start <= b.end && b.start <= a.end) {
        issues.push({
          code: 'overlap',
          severity: 'warning',
          message: `${formatPortRange(a)} overlaps ${formatPortRange(b)}.`,
          item: formatPortRange(a),
        });
        break;
      }
    }
  }

  const merged = mergePortRanges(ranges);
  const ok = !issues.some((issue) => issue.severity === 'error');

  return {
    ok,
    ranges,
    merged,
    total: merged.length > 0 ? countPorts(merged) : null,
    issues,
  };
}

/**
 * Sorts, de-duplicates and coalesces overlapping AND adjacent ranges.
 *
 * The input is not mutated. The output is the unique minimal representation of
 * the same port set, which is what makes two rules comparable.
 */
export function mergePortRanges(ranges: readonly PortRange[]): PortRange[] {
  if (ranges.length === 0) return [];

  const sorted = [...ranges].sort((a, b) => a.start - b.start || a.end - b.end);
  const out: PortRange[] = [];

  for (const range of sorted) {
    const last = out[out.length - 1];
    // `<= last.end + 1` is what collapses adjacency as well as overlap.
    if (last && range.start <= last.end + 1) {
      if (range.end > last.end) last.end = range.end;
      continue;
    }
    out.push({ start: range.start, end: range.end });
  }
  return out;
}

/** Total distinct ports across the ranges. Assumes they have been merged. */
export function countPorts(ranges: readonly PortRange[]): number {
  let total = 0;
  for (const range of ranges) total += range.end - range.start + 1;
  return total;
}

/** `80` for a single port, `8000-8100` for a span. */
export function formatPortRange(range: PortRange): string {
  return range.start === range.end ? String(range.start) : `${range.start}-${range.end}`;
}

/** Renders a list of ranges back to text. */
export function formatPortRanges(ranges: readonly PortRange[], separator = ', '): string {
  return ranges.map(formatPortRange).join(separator);
}

/** `true` when the two lists cover exactly the same ports. */
export function portRangesEqual(a: readonly PortRange[], b: readonly PortRange[]): boolean {
  const ma = mergePortRanges(a);
  const mb = mergePortRanges(b);
  if (ma.length !== mb.length) return false;
  for (let i = 0; i < ma.length; i += 1) {
    if (ma[i]!.start !== mb[i]!.start || ma[i]!.end !== mb[i]!.end) return false;
  }
  return true;
}

/** `true` when `port` falls inside any of the ranges. */
export function portInRanges(port: number, ranges: readonly PortRange[]): boolean {
  for (const range of ranges) {
    if (port >= range.start && port <= range.end) return true;
  }
  return false;
}
