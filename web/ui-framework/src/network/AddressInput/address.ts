/* ---------------------------------------------------------------------------
 * Address validation.
 *
 * Pure functions, zero imports, no React, no CSS. They are re-exported from
 * `AddressInput.tsx` but live here on their own so they can be used for table
 * filtering, CSV import, form-library resolvers and unit tests without dragging
 * a component into the bundle.
 *
 * WHY THIS IS HAND-WRITTEN AND NOT A REGEX
 * ----------------------------------------
 * Published "IPv6 regexes" are almost universally wrong. The common failures:
 *
 *   - accepting more than one `::` (`1::2::3`),
 *   - accepting `::` standing for zero groups (`1:2:3:4:5:6:7:8::`),
 *   - accepting a leading or trailing lone `:` (`:1:2:3:4:5:6:7:8`),
 *   - accepting five-hex-digit groups (`12345::1`),
 *   - accepting an embedded IPv4 anywhere but the tail (`1.2.3.4::`),
 *   - accepting an embedded IPv4 at the wrong group offset (`1:2:3:4:5:1.2.3.4`),
 *   - rejecting zone identifiers (`fe80::1%eth0`) outright.
 *
 * The parser below is a direct transliteration of the algorithm in Go's
 * `net/netip`, which handles every one of those cases correctly, and it returns
 * the 16 address bytes rather than a boolean — so prefix masking, canonical
 * RFC 5952 rendering and family detection all fall out of the same pass.
 *
 * STRICTNESS DECISIONS, STATED RATHER THAN IMPLIED
 * ------------------------------------------------
 *   - Leading zeros in an IPv4 octet are REJECTED (`010.1.1.1`). Historically
 *     `inet_aton` read those as octal while most modern parsers read them as
 *     decimal, so the same string means two different hosts depending on who
 *     resolves it. That ambiguity has been the basis of real SSRF bypasses, and
 *     an operator console is exactly the wrong place to guess.
 *   - A hostname whose final label is entirely numeric is REJECTED, because
 *     RFC 1123 §2.1 warns that such a name is indistinguishable from a dotted
 *     quad. `192.168.1.1` is an address, never a hostname.
 *   - A zone identifier is rejected inside CIDR notation, matching
 *     `netip.ParsePrefix`: a zone names a local interface, and a prefix names a
 *     range of addresses, so the combination has no meaning.
 * ------------------------------------------------------------------------- */

/** The address shapes this module can recognise. */
export type AddressKind = 'ipv4' | 'ipv6' | 'cidr4' | 'cidr6' | 'hostname' | 'hostport';

/**
 * Stable, machine-readable reason a value was rejected.
 *
 * Codes exist so the user-visible wording stays the consumer's property: pass
 * a `messages` map to `AddressInput` (or read `code` yourself) rather than
 * matching on English text.
 */
export type AddressErrorCode =
  | 'required'
  | 'not-accepted'
  | 'ipv4-shape'
  | 'ipv4-octet-range'
  | 'ipv4-leading-zero'
  | 'ipv6-shape'
  | 'ipv6-zone'
  | 'cidr-shape'
  | 'cidr-prefix-range'
  | 'cidr-zone'
  | 'hostname-empty-label'
  | 'hostname-label-length'
  | 'hostname-label-charset'
  | 'hostname-label-hyphen'
  | 'hostname-total-length'
  | 'hostname-numeric-tld'
  | 'hostname-single-label'
  | 'hostport-missing-port'
  | 'hostport-brackets'
  | 'hostport-unbracketed-ipv6'
  | 'hostport-port-shape'
  | 'hostport-port-range'
  | 'hostport-port-zero'
  | 'hostport-host';

export interface HostnameOptions {
  /**
   * Permit `_` inside a label. Off by default: underscores are legal in DNS
   * records (`_dmarc`, SRV) but not in host NAMES, and several TLS stacks will
   * refuse to present a certificate for one.
   */
  allowUnderscore?: boolean;
  /** Permit a single trailing dot marking an absolute name. Default `true`. */
  allowTrailingDot?: boolean;
  /** Require at least two labels, i.e. reject bare `localhost`. Default `false`. */
  requireMultiLabel?: boolean;
  /** Permit an all-numeric final label. Default `false`; see the header. */
  allowNumericTld?: boolean;
}

export interface IPv6Options {
  /** Permit an RFC 4007 zone id, `fe80::1%eth0`. Default `true`. */
  allowZone?: boolean;
}

export interface HostPortOptions extends HostnameOptions, IPv6Options {
  /**
   * Permit port `0`. Meaningful for a listen address ("pick any free port"),
   * meaningless for a destination. Default `false`.
   */
  allowPortZero?: boolean;
}

export interface AddressOptions extends HostPortOptions {
  /** Shapes to accept. Default: every shape. */
  accept?: readonly AddressKind[];
}

export interface AddressSuccess {
  ok: true;
  kind: AddressKind;
  /** The input as originally supplied, trimmed. */
  input: string;
  /**
   * Canonical rendering: lowercase hex, RFC 5952 zero compression for IPv6,
   * bracketed IPv6 hosts in `host:port`, trailing dot removed from a hostname.
   */
  normalized: string;
  /** 4 or 16 address bytes. Absent for `hostname`. */
  bytes?: number[];
  /** Prefix length. Present only for `cidr4` / `cidr6`. */
  prefix?: number;
  /** The masked network address, e.g. `10.1.2.3/24` normalises to `10.1.2.0/24`. */
  network?: string;
  /** `true` when bits below the prefix were set — a host address, not a network. */
  hostBitsSet?: boolean;
  /** RFC 4007 zone id, without the `%`. IPv6 only. */
  zone?: string;
  /** Host portion. Present only for `hostport`. */
  host?: string;
  /** Port number. Present only for `hostport`. */
  port?: number;
}

export interface AddressFailure {
  ok: false;
  /** The shape the input most resembled, or `null` when nothing matched. */
  kind: AddressKind | null;
  input: string;
  code: AddressErrorCode;
  /** English default. Override per code via `AddressInput`'s `messages` prop. */
  message: string;
  /** The offending fragment, where one could be isolated. */
  detail?: string;
}

export type AddressResult = AddressSuccess | AddressFailure;

/* ========================================================================== */
/* Character helpers — cheaper and clearer than regex for single characters.  */
/* ========================================================================== */

function digitValue(code: number): number {
  return code >= 48 && code <= 57 ? code - 48 : -1;
}

function hexValue(code: number): number {
  if (code >= 48 && code <= 57) return code - 48;
  if (code >= 97 && code <= 102) return code - 87; // a-f
  if (code >= 65 && code <= 70) return code - 55; // A-F
  return -1;
}

function isLetterOrDigit(code: number): boolean {
  return (
    (code >= 48 && code <= 57) ||
    (code >= 97 && code <= 122) ||
    (code >= 65 && code <= 90)
  );
}

function fail(
  kind: AddressKind | null,
  input: string,
  code: AddressErrorCode,
  message: string,
  detail?: string,
): AddressFailure {
  return detail === undefined
    ? { ok: false, kind, input, code, message }
    : { ok: false, kind, input, code, message, detail };
}

/* ========================================================================== */
/* IPv4                                                                       */
/* ========================================================================== */

/**
 * Parses a strict dotted-quad into four bytes. Returns `null` on any deviation.
 *
 * Strict means exactly four decimal octets, each 0-255, each without leading
 * zeros. Shorthand forms `inet_aton` accepts — `10.1`, `0x7f.1`, `2130706433` —
 * are deliberately not supported: they resolve differently across libraries.
 */
export function parseIPv4Bytes(value: string): number[] | null {
  const parts = value.split('.');
  if (parts.length !== 4) return null;

  const bytes: number[] = [];
  for (const part of parts) {
    if (part.length === 0 || part.length > 3) return null;
    if (part.length > 1 && part.charCodeAt(0) === 48) return null; // leading zero
    let n = 0;
    for (let i = 0; i < part.length; i += 1) {
      const d = digitValue(part.charCodeAt(i));
      if (d < 0) return null;
      n = n * 10 + d;
    }
    if (n > 255) return null;
    bytes.push(n);
  }
  return bytes;
}

/** `true` when `value` is a strict dotted-quad IPv4 address. */
export function isIPv4(value: string): boolean {
  return parseIPv4Bytes(value) !== null;
}

/** Detailed IPv4 parse, with a specific reason on failure. */
export function parseIPv4(value: string): AddressResult {
  const input = value.trim();
  const bytes = parseIPv4Bytes(input);
  if (bytes) {
    return { ok: true, kind: 'ipv4', input, normalized: bytes.join('.'), bytes };
  }

  // Work out the most useful thing to say about why.
  const parts = input.split('.');
  if (parts.length !== 4) {
    return fail(
      'ipv4',
      input,
      'ipv4-shape',
      `IPv4 needs exactly four dot-separated octets; found ${parts.length}.`,
    );
  }
  for (const part of parts) {
    if (part.length > 1 && part.charCodeAt(0) === 48) {
      return fail(
        'ipv4',
        input,
        'ipv4-leading-zero',
        `Octet "${part}" has a leading zero, which is read as octal by some resolvers and decimal by others.`,
        part,
      );
    }
    let numeric = part.length > 0;
    for (let i = 0; i < part.length; i += 1) {
      if (digitValue(part.charCodeAt(i)) < 0) numeric = false;
    }
    if (!numeric) {
      return fail('ipv4', input, 'ipv4-shape', `Octet "${part}" is not a decimal number.`, part);
    }
    if (Number(part) > 255) {
      return fail(
        'ipv4',
        input,
        'ipv4-octet-range',
        `Octet "${part}" is above 255.`,
        part,
      );
    }
  }
  return fail('ipv4', input, 'ipv4-shape', 'Not a valid IPv4 address.');
}

/* ========================================================================== */
/* IPv6                                                                       */
/* ========================================================================== */

export interface IPv6Parsed {
  bytes: number[];
  /** Empty string when no zone was present. */
  zone: string;
}

/**
 * Parses an IPv6 address into 16 bytes plus an optional zone.
 *
 * Transliterated from Go's `net/netip` parser. Every branch below corresponds
 * to one of the failure modes listed in this file's header; see the trace
 * comments inline.
 */
export function parseIPv6Bytes(value: string, options: IPv6Options = {}): IPv6Parsed | null {
  const { allowZone = true } = options;

  let s = value;
  let zone = '';

  const pct = s.indexOf('%');
  if (pct >= 0) {
    if (!allowZone) return null;
    zone = s.slice(pct + 1);
    s = s.slice(0, pct);
    // A zone must exist and must not itself contain address punctuation; the
    // percent-encoded `%25` form belongs to URIs, not to a raw address.
    if (zone.length === 0) return null;
    for (let i = 0; i < zone.length; i += 1) {
      const c = zone.charAt(i);
      if (c === '%' || c === '/' || c === '[' || c === ']' || c === ':') return null;
    }
  }

  if (s.length === 0) return null;

  const ip: number[] = new Array<number>(16).fill(0);
  /** Byte offset at which `::` appeared, or -1. */
  let ellipsis = -1;
  /** Bytes written so far. */
  let i = 0;
  /** Cursor into `s`. */
  let p = 0;

  if (s.charAt(0) === ':') {
    // A single leading colon is invalid; only `::` may start an address.
    if (s.charAt(1) !== ':') return null;
    p = 2;
    ellipsis = 0;
  }

  while (p < s.length && i < 16) {
    const groupStart = p;

    // -- One group of 1..4 hex digits ------------------------------------
    let n = 0;
    let digits = 0;
    while (p < s.length) {
      const v = hexValue(s.charCodeAt(p));
      if (v < 0) break;
      n = n * 16 + v;
      digits += 1;
      if (digits > 4) return null; // `12345::1`
      p += 1;
    }
    if (digits === 0) return null; // `:::`, `1:::2`, `1:`

    // -- Embedded IPv4 tail ----------------------------------------------
    if (p < s.length && s.charAt(p) === '.') {
      // Without a `::` the quad must sit exactly at group 7-8, i.e. byte 12.
      if (ellipsis < 0 && i !== 12) return null; // `1:2:3:4:5:1.2.3.4`
      if (i + 4 > 16) return null; // not enough room
      const v4 = parseIPv4Bytes(s.slice(groupStart));
      if (!v4) return null; // `::1.2.3.4.5`, `::ffff:1.2.3.4:5`
      ip[i] = v4[0]!;
      ip[i + 1] = v4[1]!;
      ip[i + 2] = v4[2]!;
      ip[i + 3] = v4[3]!;
      i += 4;
      p = s.length; // the quad must consume the remainder
      break;
    }

    ip[i] = (n >> 8) & 0xff;
    ip[i + 1] = n & 0xff;
    i += 2;

    if (p === s.length) break;

    // -- Separator --------------------------------------------------------
    if (s.charAt(p) !== ':') return null; // stray character
    p += 1;
    if (p === s.length) return null; // trailing lone `:` — `1:2:3:4:5:6:7:8:`

    if (s.charAt(p) === ':') {
      if (ellipsis >= 0) return null; // second `::` — `1::2::3`
      ellipsis = i;
      p += 1;
      if (p === s.length) break; // address ends `...::`
    }
  }

  // The whole string must have been consumed — catches `1:2:3:4:5:6:7:8:9`,
  // where the loop stops at 16 bytes with input left over.
  if (p !== s.length) return null;

  if (i < 16) {
    if (ellipsis < 0) return null; // too few groups and no `::`
    const gap = 16 - i;
    // Descending, so the move never clobbers a byte it has yet to read.
    for (let j = i - 1; j >= ellipsis; j -= 1) ip[j + gap] = ip[j]!;
    for (let j = ellipsis + gap - 1; j >= ellipsis; j -= 1) ip[j] = 0;
  } else if (ellipsis >= 0) {
    // `::` must stand for at least one zero group — `1:2:3:4:5:6:7:8::`.
    return null;
  }

  return { bytes: ip, zone };
}

/** `true` when `value` is a valid IPv6 address. */
export function isIPv6(value: string, options: IPv6Options = {}): boolean {
  return parseIPv6Bytes(value, options) !== null;
}

/** Detailed IPv6 parse. */
export function parseIPv6(value: string, options: IPv6Options = {}): AddressResult {
  const input = value.trim();
  const parsed = parseIPv6Bytes(input, options);
  if (parsed) {
    const success: AddressSuccess = {
      ok: true,
      kind: 'ipv6',
      input,
      normalized: formatIPv6(parsed.bytes, parsed.zone),
      bytes: parsed.bytes,
    };
    if (parsed.zone) success.zone = parsed.zone;
    return success;
  }

  const pct = input.indexOf('%');
  if (pct >= 0) {
    if (options.allowZone === false) {
      return fail('ipv6', input, 'ipv6-zone', 'A zone identifier is not allowed here.');
    }
    if (pct === input.length - 1) {
      return fail('ipv6', input, 'ipv6-zone', 'A zone identifier must follow the "%".');
    }
  }
  if (input.split('::').length > 2) {
    return fail(
      'ipv6',
      input,
      'ipv6-shape',
      'An IPv6 address may contain "::" at most once.',
    );
  }
  return fail('ipv6', input, 'ipv6-shape', 'Not a valid IPv6 address.');
}

/**
 * Renders 16 bytes in canonical RFC 5952 form.
 *
 * The rules that actually matter and are usually missed:
 *   - lowercase hex, leading zeros within a group dropped;
 *   - the LONGEST run of zero groups collapses to `::`, and only when it spans
 *     two or more groups — `1:0:2:...` stays written out;
 *   - on a tie the LEFTMOST run wins, so one address has exactly one rendering;
 *   - an IPv4-mapped address keeps its dotted tail (`::ffff:192.0.2.1`).
 */
export function formatIPv6(bytes: readonly number[], zone = ''): string {
  const groups: number[] = [];
  for (let i = 0; i < 16; i += 2) {
    groups.push(((bytes[i] ?? 0) << 8) | (bytes[i + 1] ?? 0));
  }

  let count = 8;
  let tail = '';
  const mapped =
    groups[0] === 0 &&
    groups[1] === 0 &&
    groups[2] === 0 &&
    groups[3] === 0 &&
    groups[4] === 0 &&
    groups[5] === 0xffff;
  if (mapped) {
    tail = `${bytes[12] ?? 0}.${bytes[13] ?? 0}.${bytes[14] ?? 0}.${bytes[15] ?? 0}`;
    count = 6;
  }

  let bestStart = -1;
  let bestLen = 0;
  let runStart = -1;
  let runLen = 0;
  for (let i = 0; i < count; i += 1) {
    if (groups[i] === 0) {
      if (runStart < 0) {
        runStart = i;
        runLen = 0;
      }
      runLen += 1;
      // Strictly greater keeps the leftmost run on a tie.
      if (runLen > bestLen) {
        bestLen = runLen;
        bestStart = runStart;
      }
    } else {
      runStart = -1;
      runLen = 0;
    }
  }
  if (bestLen < 2) {
    bestStart = -1;
    bestLen = 0;
  }

  let core = '';
  let i = 0;
  while (i < count) {
    if (i === bestStart) {
      core += '::';
      i += bestLen;
      continue;
    }
    if (core.length > 0 && !core.endsWith(':')) core += ':';
    core += (groups[i] ?? 0).toString(16);
    i += 1;
  }
  if (core === '') core = '::';
  if (tail) core += core.endsWith(':') ? tail : `:${tail}`;

  return zone ? `${core}%${zone}` : core;
}

/* ========================================================================== */
/* CIDR                                                                       */
/* ========================================================================== */

function parsePrefixLength(text: string, max: number): number | null {
  if (text.length === 0 || text.length > 3) return null;
  if (text.length > 1 && text.charCodeAt(0) === 48) return null; // `/08`
  let n = 0;
  for (let i = 0; i < text.length; i += 1) {
    const d = digitValue(text.charCodeAt(i));
    if (d < 0) return null;
    n = n * 10 + d;
  }
  return n <= max ? n : null;
}

function maskBytes(bytes: readonly number[], prefix: number): { masked: number[]; hostBitsSet: boolean } {
  const masked: number[] = [];
  let hostBitsSet = false;
  for (let i = 0; i < bytes.length; i += 1) {
    const remaining = prefix - i * 8;
    const keep = remaining >= 8 ? 8 : remaining <= 0 ? 0 : remaining;
    const mask = keep === 0 ? 0 : (0xff << (8 - keep)) & 0xff;
    const original = bytes[i] ?? 0;
    const value = original & mask;
    if (value !== original) hostBitsSet = true;
    masked.push(value);
  }
  return { masked, hostBitsSet };
}

/**
 * Parses `address/prefix` for either family.
 *
 * `family` pins the expected family; omit it to accept whichever the address
 * portion turns out to be. The result always reports the masked `network`, so a
 * caller can tell an operator that `10.1.2.3/24` describes `10.1.2.0/24`.
 */
export function parseCidr(value: string, family?: 4 | 6): AddressResult {
  const input = value.trim();
  const kindGuess: AddressKind = family === 6 ? 'cidr6' : 'cidr4';

  const slash = input.indexOf('/');
  if (slash < 0) {
    return fail(kindGuess, input, 'cidr-shape', 'A CIDR block needs a "/" and a prefix length.');
  }
  if (input.indexOf('/', slash + 1) >= 0) {
    return fail(kindGuess, input, 'cidr-shape', 'A CIDR block may contain only one "/".');
  }

  const addrText = input.slice(0, slash);
  const prefixText = input.slice(slash + 1);

  const looksV6 = addrText.includes(':');
  if (family === 4 && looksV6) {
    return fail('cidr4', input, 'cidr-shape', 'Expected an IPv4 CIDR block.');
  }
  if (family === 6 && !looksV6) {
    return fail('cidr6', input, 'cidr-shape', 'Expected an IPv6 CIDR block.');
  }

  if (looksV6) {
    if (addrText.includes('%')) {
      return fail(
        'cidr6',
        input,
        'cidr-zone',
        'A zone identifier names one interface and cannot describe a prefix.',
      );
    }
    const parsed = parseIPv6Bytes(addrText, { allowZone: false });
    if (!parsed) {
      const inner = parseIPv6(addrText, { allowZone: false });
      return inner.ok
        ? fail('cidr6', input, 'cidr-shape', 'Not a valid IPv6 CIDR block.')
        : fail('cidr6', input, inner.code, inner.message, inner.detail);
    }
    const prefix = parsePrefixLength(prefixText, 128);
    if (prefix === null) {
      return fail(
        'cidr6',
        input,
        'cidr-prefix-range',
        'An IPv6 prefix length must be a whole number from 0 to 128.',
        prefixText,
      );
    }
    const { masked, hostBitsSet } = maskBytes(parsed.bytes, prefix);
    return {
      ok: true,
      kind: 'cidr6',
      input,
      normalized: `${formatIPv6(parsed.bytes)}/${prefix}`,
      bytes: parsed.bytes,
      prefix,
      network: `${formatIPv6(masked)}/${prefix}`,
      hostBitsSet,
    };
  }

  const v4 = parseIPv4Bytes(addrText);
  if (!v4) {
    const inner = parseIPv4(addrText);
    return inner.ok
      ? fail('cidr4', input, 'cidr-shape', 'Not a valid IPv4 CIDR block.')
      : fail('cidr4', input, inner.code, inner.message, inner.detail);
  }
  const prefix = parsePrefixLength(prefixText, 32);
  if (prefix === null) {
    return fail(
      'cidr4',
      input,
      'cidr-prefix-range',
      'An IPv4 prefix length must be a whole number from 0 to 32.',
      prefixText,
    );
  }
  const { masked, hostBitsSet } = maskBytes(v4, prefix);
  return {
    ok: true,
    kind: 'cidr4',
    input,
    normalized: `${v4.join('.')}/${prefix}`,
    bytes: v4,
    prefix,
    network: `${masked.join('.')}/${prefix}`,
    hostBitsSet,
  };
}

/** `true` when `value` is an IPv4 CIDR block. */
export function isCidr4(value: string): boolean {
  return parseCidr(value, 4).ok;
}

/** `true` when `value` is an IPv6 CIDR block. */
export function isCidr6(value: string): boolean {
  return parseCidr(value, 6).ok;
}

/* ========================================================================== */
/* Hostname — RFC 1123 §2.1, which relaxed RFC 952 to allow a leading digit    */
/* ========================================================================== */

const MAX_NAME_LENGTH = 253;
const MAX_LABEL_LENGTH = 63;

/** Detailed hostname parse. */
export function parseHostname(value: string, options: HostnameOptions = {}): AddressResult {
  const {
    allowUnderscore = false,
    allowTrailingDot = true,
    requireMultiLabel = false,
    allowNumericTld = false,
  } = options;

  const input = value.trim();
  if (input.length === 0) {
    return fail('hostname', input, 'hostname-empty-label', 'A hostname cannot be empty.');
  }

  let name = input;
  if (name.endsWith('.')) {
    if (!allowTrailingDot) {
      return fail(
        'hostname',
        input,
        'hostname-empty-label',
        'A trailing dot is not allowed here.',
      );
    }
    name = name.slice(0, -1);
  }

  // 253 characters is the presentation-format limit that corresponds to the
  // 255-octet wire limit once the length prefixes are accounted for.
  if (name.length > MAX_NAME_LENGTH) {
    return fail(
      'hostname',
      input,
      'hostname-total-length',
      `A hostname may be at most ${MAX_NAME_LENGTH} characters; this one is ${name.length}.`,
    );
  }
  if (name.length === 0) {
    return fail('hostname', input, 'hostname-empty-label', 'A hostname cannot be empty.');
  }

  const labels = name.split('.');
  if (requireMultiLabel && labels.length < 2) {
    return fail(
      'hostname',
      input,
      'hostname-single-label',
      'A fully qualified name needs at least two labels.',
    );
  }

  for (const label of labels) {
    if (label.length === 0) {
      return fail(
        'hostname',
        input,
        'hostname-empty-label',
        'A hostname cannot contain an empty label — check for a doubled dot.',
      );
    }
    if (label.length > MAX_LABEL_LENGTH) {
      return fail(
        'hostname',
        input,
        'hostname-label-length',
        `Label "${label.slice(0, 16)}…" is ${label.length} characters; the limit is ${MAX_LABEL_LENGTH}.`,
        label,
      );
    }
    if (label.startsWith('-') || label.endsWith('-')) {
      return fail(
        'hostname',
        input,
        'hostname-label-hyphen',
        `Label "${label}" may not start or end with a hyphen.`,
        label,
      );
    }
    for (let i = 0; i < label.length; i += 1) {
      const code = label.charCodeAt(i);
      if (isLetterOrDigit(code)) continue;
      if (code === 45) continue; // '-'
      if (allowUnderscore && code === 95) continue; // '_'
      return fail(
        'hostname',
        input,
        'hostname-label-charset',
        `Label "${label}" contains "${label.charAt(i)}"; only letters, digits and hyphens are allowed.`,
        label.charAt(i),
      );
    }
  }

  const tld = labels[labels.length - 1] ?? '';
  if (!allowNumericTld && labels.length > 1) {
    let numeric = tld.length > 0;
    for (let i = 0; i < tld.length; i += 1) {
      if (digitValue(tld.charCodeAt(i)) < 0) numeric = false;
    }
    if (numeric) {
      return fail(
        'hostname',
        input,
        'hostname-numeric-tld',
        'A name ending in an all-numeric label is indistinguishable from an IP address.',
        tld,
      );
    }
  }

  return {
    ok: true,
    kind: 'hostname',
    input,
    // DNS is case-insensitive, so the canonical form is lowercase without the
    // root dot. Comparing two hostnames any other way produces false mismatches.
    normalized: name.toLowerCase(),
  };
}

/** `true` when `value` is a syntactically valid RFC 1123 hostname. */
export function isHostname(value: string, options: HostnameOptions = {}): boolean {
  return parseHostname(value, options).ok;
}

/* ========================================================================== */
/* host:port                                                                  */
/* ========================================================================== */

export const MIN_PORT = 0;
export const MAX_PORT = 65535;

/**
 * Parses a bare port number.
 *
 * Leading zeros are rejected for the same reason as in an IPv4 octet: `0080`
 * is decimal 80 to most parsers and a syntax error to others.
 */
export function parsePort(
  text: string,
  { allowZero = false }: { allowZero?: boolean } = {},
): { ok: true; port: number } | { ok: false; code: AddressErrorCode; message: string } {
  const t = text.trim();
  if (t.length === 0) {
    return { ok: false, code: 'hostport-port-shape', message: 'A port number is required.' };
  }
  if (t.length > 5) {
    return { ok: false, code: 'hostport-port-range', message: `Ports run from ${MIN_PORT} to ${MAX_PORT}.` };
  }
  if (t.length > 1 && t.charCodeAt(0) === 48) {
    return {
      ok: false,
      code: 'hostport-port-shape',
      message: `Port "${t}" has a leading zero.`,
    };
  }
  let n = 0;
  for (let i = 0; i < t.length; i += 1) {
    const d = digitValue(t.charCodeAt(i));
    if (d < 0) {
      return {
        ok: false,
        code: 'hostport-port-shape',
        message: `Port "${t}" is not a whole number.`,
      };
    }
    n = n * 10 + d;
  }
  if (n > MAX_PORT) {
    return { ok: false, code: 'hostport-port-range', message: `Port ${n} is above ${MAX_PORT}.` };
  }
  if (n === 0 && !allowZero) {
    return {
      ok: false,
      code: 'hostport-port-zero',
      message: 'Port 0 means "any free port" and is not a valid destination.',
    };
  }
  return { ok: true, port: n };
}

/**
 * Parses `host:port`, `ipv4:port` or `[ipv6]:port`.
 *
 * An unbracketed IPv6 literal is rejected rather than guessed at. `::1:8080` is
 * genuinely ambiguous — it is a complete IPv6 address on its own — and RFC 3986
 * §3.2.2 resolves that ambiguity with brackets, so this does too.
 */
export function parseHostPort(value: string, options: HostPortOptions = {}): AddressResult {
  const { allowPortZero = false, allowZone = true, ...hostnameOptions } = options;
  const input = value.trim();

  let hostText: string;
  let portText: string;
  let bracketed = false;

  if (input.startsWith('[')) {
    const close = input.indexOf(']');
    if (close < 0) {
      return fail('hostport', input, 'hostport-brackets', 'Missing the closing "]".');
    }
    bracketed = true;
    hostText = input.slice(1, close);
    const rest = input.slice(close + 1);
    if (rest.length === 0) {
      return fail(
        'hostport',
        input,
        'hostport-missing-port',
        'A bracketed address still needs ":" and a port.',
      );
    }
    if (!rest.startsWith(':')) {
      return fail('hostport', input, 'hostport-brackets', 'Expected ":" after "]".');
    }
    portText = rest.slice(1);
  } else {
    const colon = input.lastIndexOf(':');
    if (colon < 0) {
      return fail(
        'hostport',
        input,
        'hostport-missing-port',
        'Expected host and port separated by ":".',
      );
    }
    hostText = input.slice(0, colon);
    portText = input.slice(colon + 1);
    if (hostText.includes(':')) {
      return fail(
        'hostport',
        input,
        'hostport-unbracketed-ipv6',
        'An IPv6 host must be wrapped in square brackets, e.g. [2001:db8::1]:443.',
      );
    }
  }

  const port = parsePort(portText, { allowZero: allowPortZero });
  if (!port.ok) {
    return fail('hostport', input, port.code, port.message, portText);
  }

  if (hostText.length === 0) {
    return fail('hostport', input, 'hostport-host', 'The host part is empty.');
  }

  // Bracketed hosts are IPv6 by definition.
  if (bracketed) {
    const v6 = parseIPv6Bytes(hostText, { allowZone });
    if (!v6) {
      const inner = parseIPv6(hostText, { allowZone });
      return inner.ok
        ? fail('hostport', input, 'hostport-host', 'Not a valid bracketed IPv6 host.')
        : fail('hostport', input, inner.code, inner.message, inner.detail);
    }
    const host = formatIPv6(v6.bytes, v6.zone);
    const success: AddressSuccess = {
      ok: true,
      kind: 'hostport',
      input,
      normalized: `[${host}]:${port.port}`,
      bytes: v6.bytes,
      host,
      port: port.port,
    };
    if (v6.zone) success.zone = v6.zone;
    return success;
  }

  const v4 = parseIPv4Bytes(hostText);
  if (v4) {
    const host = v4.join('.');
    return {
      ok: true,
      kind: 'hostport',
      input,
      normalized: `${host}:${port.port}`,
      bytes: v4,
      host,
      port: port.port,
    };
  }

  const name = parseHostname(hostText, hostnameOptions);
  if (!name.ok) {
    return fail('hostport', input, name.code, name.message, name.detail);
  }
  return {
    ok: true,
    kind: 'hostport',
    input,
    normalized: `${name.normalized}:${port.port}`,
    host: name.normalized,
    port: port.port,
  };
}

/** `true` when `value` is a valid `host:port` pair. */
export function isHostPort(value: string, options: HostPortOptions = {}): boolean {
  return parseHostPort(value, options).ok;
}

/* ========================================================================== */
/* Combined entry point                                                       */
/* ========================================================================== */

export const ALL_ADDRESS_KINDS: readonly AddressKind[] = [
  'ipv4',
  'ipv6',
  'cidr4',
  'cidr6',
  'hostport',
  'hostname',
];

/** English names used in "expected one of …" messages. Overridable per component. */
export const ADDRESS_KIND_LABELS: Record<AddressKind, string> = {
  ipv4: 'IPv4',
  ipv6: 'IPv6',
  cidr4: 'IPv4 CIDR',
  cidr6: 'IPv6 CIDR',
  hostname: 'hostname',
  hostport: 'host:port',
};

/**
 * Guesses which shape the operator was reaching for, purely from punctuation.
 *
 * This is only used to choose WHICH specific error message to show. It never
 * decides acceptance — that is done by actually parsing. Guessing well matters
 * because "not a valid address" is useless feedback next to "octet 300 is above
 * 255".
 */
export function guessAddressKind(value: string): AddressKind {
  const s = value.trim();
  if (s.includes('/')) {
    return s.slice(0, s.indexOf('/')).includes(':') ? 'cidr6' : 'cidr4';
  }
  if (s.startsWith('[')) return 'hostport';
  const first = s.indexOf(':');
  if (first >= 0) {
    // Two or more colons can only be IPv6; exactly one is host:port.
    return s.indexOf(':', first + 1) >= 0 ? 'ipv6' : 'hostport';
  }
  if (s.includes('%')) return 'ipv6';
  let digitsAndDots = s.length > 0;
  for (let i = 0; i < s.length; i += 1) {
    const c = s.charCodeAt(i);
    if (digitValue(c) < 0 && c !== 46) digitsAndDots = false;
  }
  return digitsAndDots ? 'ipv4' : 'hostname';
}

function parseAs(kind: AddressKind, value: string, options: AddressOptions): AddressResult {
  const { allowZone = true, allowPortZero = false, ...rest } = options;
  switch (kind) {
    case 'ipv4':
      return parseIPv4(value);
    case 'ipv6':
      return parseIPv6(value, { allowZone });
    case 'cidr4':
      return parseCidr(value, 4);
    case 'cidr6':
      return parseCidr(value, 6);
    case 'hostname':
      return parseHostname(value, rest);
    case 'hostport':
      return parseHostPort(value, { ...rest, allowZone, allowPortZero });
  }
}

/**
 * Validates a value against a set of accepted shapes.
 *
 * Shapes are tried in a fixed order (numeric literals before names) so that
 * `192.0.2.1` always reports as `ipv4` and never as a hostname, regardless of
 * the order the caller listed in `accept`. Determinism here matters: a value
 * whose reported kind depends on prop ordering is a bug generator.
 *
 * An empty string is a FAILURE with code `required`. Callers that treat empty
 * as acceptable should check for it before calling — the component in this
 * folder does exactly that, because "not filled in yet" is not "wrong".
 */
export function parseAddress(value: string, options: AddressOptions = {}): AddressResult {
  const input = value.trim();
  const accept = options.accept && options.accept.length > 0 ? options.accept : ALL_ADDRESS_KINDS;

  if (input.length === 0) {
    return fail(null, input, 'required', 'Enter an address.');
  }

  const ordered = ALL_ADDRESS_KINDS.filter((k) => accept.includes(k));
  for (const kind of ordered) {
    const result = parseAs(kind, input, options);
    if (result.ok) return result;
  }

  // Nothing matched. Report the failure from whichever shape the input most
  // resembles, provided that shape is actually on offer.
  const guess = guessAddressKind(input);
  if (ordered.includes(guess)) {
    const detailed = parseAs(guess, input, options);
    if (!detailed.ok) return detailed;
  }

  const names = ordered.map((k) => ADDRESS_KIND_LABELS[k]);
  const list =
    names.length <= 1
      ? (names[0] ?? 'an address')
      : `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}`;
  return fail(null, input, 'not-accepted', `Expected ${list}.`);
}

/** `true` when `value` matches any accepted shape. */
export function isAddress(value: string, options: AddressOptions = {}): boolean {
  return parseAddress(value, options).ok;
}
