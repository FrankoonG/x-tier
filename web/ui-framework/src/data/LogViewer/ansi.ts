/* ---------------------------------------------------------------------------
 * ANSI SGR parser.
 *
 * WHY A PARSER AND NOT A REGEX + innerHTML
 * ----------------------------------------
 * The usual implementation of this feature converts escape sequences to
 * `<span style=…>` strings and hands the result to `dangerouslySetInnerHTML`.
 * Log text is the least trustworthy string in an operator panel — it is
 * whatever a remote peer, a config file or an attacker managed to get written
 * to a log — so that approach turns any log line into an HTML injection sink.
 * This parser emits data; LogViewer turns the data into React elements, which
 * escape by construction. No code path in this component produces HTML from a
 * string.
 *
 * WHAT IS SUPPORTED
 * -----------------
 *   0                     reset
 *   1 2 3 4 7 8 9         bold, dim, italic, underline, inverse, conceal, strike
 *   21 22 23 24 27 28 29  the corresponding resets
 *   30-37 / 40-47         the 8 standard colours, foreground / background
 *   90-97 / 100-107       the 8 bright colours
 *   38;5;n  / 48;5;n      256-colour palette
 *   38;2;r;g;b            24-bit truecolor
 *   38:5:n / 38:2::r:g:b  the ITU T.416 colon-delimited forms
 *   39 / 49               default foreground / background
 *
 * WHAT IS DISCARDED, DELIBERATELY
 * -------------------------------
 * Every other CSI sequence — cursor movement, erase-in-line, scroll regions —
 * is consumed and dropped. A log pane is not a terminal emulator; honouring
 * `ESC[2J` would let a log line blank the operator's view. OSC sequences
 * (window title, hyperlinks, notifications) are consumed up to their terminator
 * and dropped for the same reason: `OSC 8` carries an arbitrary URI, and
 * rendering it as a live link would make a log line a navigation vector.
 *
 * An unterminated sequence at the end of the line is truncated rather than
 * emitted as literal text, so a cut-off log line cannot leak escape bytes into
 * the DOM.
 *
 * COLOUR RESOLUTION
 * -----------------
 * The 16 standard colours resolve to `--stratum-ansi-*` custom properties, so
 * they follow the theme and a consumer can retune them. The 256-colour cube and
 * truecolor resolve to literal `rgb()`: those are exact values chosen by the log
 * producer, not design decisions, and there is no meaningful semantic token for
 * "colour 137". Contrast for author-supplied colours cannot be guaranteed, which
 * is why `LogViewer` exposes `ansi={false}`.
 * ------------------------------------------------------------------------- */

export interface AnsiStyle {
  /** Resolved CSS colour, or `undefined` for the default foreground. */
  fg?: string;
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  strike?: boolean;
  inverse?: boolean;
  /** SGR 8. Text is kept for selection and copy, but painted transparent. */
  conceal?: boolean;
}

export interface AnsiSpan {
  text: string;
  style: AnsiStyle;
}

/* The 8 standard colours followed by the 8 bright ones, in SGR order. */
const PALETTE_16 = [
  'var(--stratum-ansi-black)',
  'var(--stratum-ansi-red)',
  'var(--stratum-ansi-green)',
  'var(--stratum-ansi-yellow)',
  'var(--stratum-ansi-blue)',
  'var(--stratum-ansi-magenta)',
  'var(--stratum-ansi-cyan)',
  'var(--stratum-ansi-white)',
  'var(--stratum-ansi-bright-black)',
  'var(--stratum-ansi-bright-red)',
  'var(--stratum-ansi-bright-green)',
  'var(--stratum-ansi-bright-yellow)',
  'var(--stratum-ansi-bright-blue)',
  'var(--stratum-ansi-bright-magenta)',
  'var(--stratum-ansi-bright-cyan)',
  'var(--stratum-ansi-bright-white)',
] as const;

/** The xterm 6x6x6 cube steps. Not a linear ramp — the first gap is wider. */
const CUBE_STEPS = [0, 95, 135, 175, 215, 255] as const;

const ESC = 0x1b;
const BEL = 0x07;
const CSI_OPEN = 0x5b; // [
const OSC_OPEN = 0x5d; // ]
const ST_TAIL = 0x5c; // \
const SGR_FINAL = 0x6d; // m
const TAB = 0x09;
const DEL = 0x7f;

/** Resolves an xterm-256 index to a CSS colour. */
export function ansi256(index: number): string | undefined {
  if (!Number.isInteger(index) || index < 0 || index > 255) return undefined;
  if (index < 16) return PALETTE_16[index];
  if (index < 232) {
    const n = index - 16;
    const r = CUBE_STEPS[Math.floor(n / 36)] ?? 0;
    const g = CUBE_STEPS[Math.floor((n % 36) / 6)] ?? 0;
    const b = CUBE_STEPS[n % 6] ?? 0;
    return `rgb(${r} ${g} ${b})`;
  }
  const level = 8 + (index - 232) * 10;
  return `rgb(${level} ${level} ${level})`;
}

/**
 * True when the string contains anything the parser must do work for: an escape
 * byte, or any other C0 control except tab. This is the fast path — the
 * overwhelming majority of log lines are plain and must cost nothing.
 */
/* The character class is assembled from char codes rather than written with
 * \uXXXX escapes, so this source file itself contains no literal control
 * bytes — a file with a raw NUL in it breaks a surprising number of tools. */
const CONTROL_CLASS =
  '[' +
  String.fromCharCode(0) + '-' + String.fromCharCode(8) +
  String.fromCharCode(10) + '-' + String.fromCharCode(31) +
  String.fromCharCode(127) +
  ']';

const NEEDS_PARSE = new RegExp(CONTROL_CLASS);

export function needsAnsiParse(text: string): boolean {
  return NEEDS_PARSE.test(text);
}

function clampByte(value: string | undefined): number | null {
  const n = Number.parseInt(value ?? '', 10);
  if (!Number.isFinite(n)) return null;
  return Math.max(0, Math.min(255, n));
}

/** `38:5:n` / `38:2::r:g:b` / `38:2:r:g:b` — the colour-space id is optional. */
function resolveColonColour(sub: readonly string[]): string | undefined {
  const mode = Number.parseInt(sub[1] ?? '', 10);
  if (mode === 5) return ansi256(Number.parseInt(sub[2] ?? '', 10));
  if (mode === 2) {
    // T.416 puts a colour-space id at index 2; xterm omits it. Detected by
    // component count rather than guessed.
    const offset = sub.length >= 6 ? 3 : 2;
    const r = clampByte(sub[offset]);
    const g = clampByte(sub[offset + 1]);
    const b = clampByte(sub[offset + 2]);
    if (r === null || g === null || b === null) return undefined;
    return `rgb(${r} ${g} ${b})`;
  }
  return undefined;
}

function applyCode(style: AnsiStyle, code: number): void {
  switch (code) {
    case 1: style.bold = true; break;
    case 2: style.dim = true; break;
    case 3: style.italic = true; break;
    case 4: style.underline = true; break;
    case 7: style.inverse = true; break;
    case 8: style.conceal = true; break;
    case 9: style.strike = true; break;
    // 21 is double-underline in ECMA-48 and bold-off in several terminals. It
    // is treated as bold-off: the ambiguity is unresolvable, and losing a weight
    // is less damaging than a bold run that never ends.
    case 21: delete style.bold; break;
    case 22: delete style.bold; delete style.dim; break;
    case 23: delete style.italic; break;
    case 24: delete style.underline; break;
    case 27: delete style.inverse; break;
    case 28: delete style.conceal; break;
    case 29: delete style.strike; break;
    case 39: delete style.fg; break;
    case 49: delete style.bg; break;
    default:
      if (code >= 30 && code <= 37) style.fg = PALETTE_16[code - 30];
      else if (code >= 40 && code <= 47) style.bg = PALETTE_16[code - 40];
      else if (code >= 90 && code <= 97) style.fg = PALETTE_16[code - 90 + 8];
      else if (code >= 100 && code <= 107) style.bg = PALETTE_16[code - 100 + 8];
      // Everything else (framed, encircled, overlined, font selection, ideogram
      // attributes) is recognised as valid and intentionally not rendered.
      break;
  }
}

/** Applies one SGR parameter list to a style, returning a new style object. */
function applySgr(current: AnsiStyle, params: readonly string[]): AnsiStyle {
  const next: AnsiStyle = { ...current };

  for (let i = 0; i < params.length; i += 1) {
    const raw = params[i] ?? '';

    // Colon-delimited extended colour. Handled whole, because its
    // sub-parameters do not participate in the outer semicolon list.
    if (raw.includes(':')) {
      const sub = raw.split(':');
      const head = Number.parseInt(sub[0] ?? '', 10);
      if (head === 38 || head === 48) {
        const colour = resolveColonColour(sub);
        if (colour !== undefined) {
          if (head === 38) next.fg = colour;
          else next.bg = colour;
        }
        continue;
      }
      // Anything else carrying sub-parameters (`4:3`, a curly underline)
      // degrades to its base attribute rather than being dropped.
      if (Number.isFinite(head)) applyCode(next, head);
      continue;
    }

    const code = raw === '' ? 0 : Number.parseInt(raw, 10);
    if (!Number.isFinite(code)) continue;

    if (code === 38 || code === 48) {
      const mode = Number.parseInt(params[i + 1] ?? '', 10);
      if (mode === 5) {
        const colour = ansi256(Number.parseInt(params[i + 2] ?? '', 10));
        if (colour !== undefined) {
          if (code === 38) next.fg = colour;
          else next.bg = colour;
        }
        i += 2;
      } else if (mode === 2) {
        const r = clampByte(params[i + 2]);
        const g = clampByte(params[i + 3]);
        const b = clampByte(params[i + 4]);
        if (r !== null && g !== null && b !== null) {
          const colour = `rgb(${r} ${g} ${b})`;
          if (code === 38) next.fg = colour;
          else next.bg = colour;
        }
        i += 4;
      }
      // An unknown extended mode consumes nothing further, so the remaining
      // parameters are still interpreted rather than silently swallowed.
      continue;
    }

    if (code === 0) {
      // Reset clears every field, so the object is emptied rather than replaced
      // — callers hold `next` already.
      for (const key of Object.keys(next) as (keyof AnsiStyle)[]) delete next[key];
      continue;
    }

    applyCode(next, code);
  }

  return next;
}

/**
 * Parses one line into styled spans.
 *
 * Runs in O(length). Called only for lines inside the rendered window, which is
 * what keeps a 100k-line log cheap: the parser never sees the other 99,950.
 */
export function parseAnsi(input: string): AnsiSpan[] {
  const spans: AnsiSpan[] = [];
  let style: AnsiStyle = {};
  let buffer = '';
  const len = input.length;
  let i = 0;

  const flush = () => {
    if (buffer.length > 0) {
      spans.push({ text: buffer, style });
      buffer = '';
    }
  };

  while (i < len) {
    const code = input.charCodeAt(i);

    if (code === ESC) {
      const next = input.charCodeAt(i + 1); // NaN past the end

      // CSI: ESC [ parameters intermediates final
      if (next === CSI_OPEN) {
        let j = i + 2;
        while (j < len) {
          const c = input.charCodeAt(j);
          if (c >= 0x30 && c <= 0x3f) j += 1;
          else break;
        }
        const paramsEnd = j;
        while (j < len) {
          const c = input.charCodeAt(j);
          if (c >= 0x20 && c <= 0x2f) j += 1;
          else break;
        }
        if (j >= len) {
          // Unterminated: truncate the remainder rather than emitting raw
          // escape bytes into the document.
          i = len;
          break;
        }
        if (input.charCodeAt(j) === SGR_FINAL) {
          flush();
          const body = input.slice(i + 2, paramsEnd);
          style = applySgr(style, body.length === 0 ? [''] : body.split(';'));
        }
        i = j + 1;
        continue;
      }

      // OSC: ESC ] … terminated by BEL or ESC backslash
      if (next === OSC_OPEN) {
        let j = i + 2;
        while (j < len) {
          const c = input.charCodeAt(j);
          if (c === BEL) {
            j += 1;
            break;
          }
          if (c === ESC && input.charCodeAt(j + 1) === ST_TAIL) {
            j += 2;
            break;
          }
          j += 1;
        }
        i = j;
        continue;
      }

      // Any other escape: a two-byte sequence, or a lone trailing ESC.
      i += Number.isNaN(next) ? 1 : 2;
      continue;
    }

    // Remaining C0 controls and DEL are dropped. Tab survives — log output is
    // routinely column-aligned with it and removing it destroys the alignment.
    if ((code < 0x20 && code !== TAB) || code === DEL) {
      i += 1;
      continue;
    }

    buffer += input[i];
    i += 1;
  }

  flush();
  return spans;
}

/**
 * Plain text with every escape sequence and control character removed.
 *
 * Used for filtering, match offsets and copy. It must produce exactly the same
 * character sequence as concatenating `parseAnsi()`'s span texts, or highlight
 * offsets computed on one would not line up with the other.
 */
export function stripAnsi(input: string): string {
  if (!needsAnsiParse(input)) return input;
  let out = '';
  const spans = parseAnsi(input);
  for (let i = 0; i < spans.length; i += 1) out += spans[i]?.text ?? '';
  return out;
}
