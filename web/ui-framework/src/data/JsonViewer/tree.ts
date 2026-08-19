/* ---------------------------------------------------------------------------
 * Object -> displayable tree.
 *
 * Three hazards this exists to defuse, all of which a naive recursive renderer
 * hits the first time it is pointed at a live runtime object rather than at a
 * parsed API response:
 *
 *   CYCLES     `a.b = a` makes a recursive walk loop until the stack dies.
 *              Detected with a set of ANCESTORS, not a set of everything seen:
 *              an object referenced twice as a sibling is not a cycle and must
 *              still expand. A "visited" set would wrongly collapse the second
 *              occurrence — a bug that looks like missing data.
 *
 *   SIZE       A 200k-element array must not become 200k nodes. Building is
 *              bounded by `maxNodes` and `maxDepth`, and what is left out is
 *              reported as `truncated` rather than silently dropped.
 *
 *   GETTERS    Own enumerable properties only, read through
 *              `Object.getOwnPropertyDescriptor` — invoking an accessor while
 *              rendering a debug pane can mutate the thing being debugged.
 *              An unread accessor is shown as such rather than as a value.
 * ------------------------------------------------------------------------- */

export type JsonValueKind =
  | 'object'
  | 'array'
  | 'map'
  | 'set'
  | 'string'
  | 'number'
  | 'boolean'
  | 'null'
  | 'undefined'
  | 'bigint'
  | 'symbol'
  | 'function'
  | 'date'
  | 'regexp'
  | 'error'
  | 'accessor'
  | 'circular';

export type JsonKeyKind = 'root' | 'property' | 'index' | 'entry';

export interface JsonNode {
  /** Stable identity, and the value `copy path` yields. */
  path: string;
  /** Display key. `null` at the root. */
  key: string | null;
  keyKind: JsonKeyKind;
  kind: JsonValueKind;
  /** The underlying value, for `copy value`. */
  value: unknown;
  /** Rendered form of a leaf. Empty for containers. */
  text: string;
  /** Collapsed summary of a container, e.g. `{3}`, `[128]`. */
  summary: string;
  /** `null` for a leaf. An empty array is a container with no children. */
  children: JsonNode[] | null;
  /** Children before any limit was applied. */
  childCount: number;
  depth: number;
  /** Children were dropped because a build limit was reached. */
  truncated: boolean;
}

export interface BuildTreeOptions {
  /** Depth past which containers are not expanded further. Default 64. */
  maxDepth?: number;
  /** Ceiling on total nodes built. Default 20000. */
  maxNodes?: number;
  /** Longest rendered string before an ellipsis. Copy is unaffected. Default 400. */
  maxStringLength?: number;
  /** Label for the root node. Default `'$'`. */
  rootKey?: string;
}

const IDENTIFIER = /^[A-Za-z_$][A-Za-z0-9_$]*$/;

function joinPath(parent: string, key: string, kind: JsonKeyKind): string {
  if (kind === 'index') return `${parent}[${key}]`;
  if (kind === 'entry') return `${parent}.get(${JSON.stringify(key)})`;
  return IDENTIFIER.test(key) ? `${parent}.${key}` : `${parent}[${JSON.stringify(key)}]`;
}

export function kindOf(value: unknown): JsonValueKind {
  if (value === null) return 'null';
  const type = typeof value;
  switch (type) {
    case 'undefined': return 'undefined';
    case 'string': return 'string';
    case 'number': return 'number';
    case 'boolean': return 'boolean';
    case 'bigint': return 'bigint';
    case 'symbol': return 'symbol';
    case 'function': return 'function';
    default: break;
  }
  if (Array.isArray(value)) return 'array';
  if (value instanceof Date) return 'date';
  if (value instanceof RegExp) return 'regexp';
  if (value instanceof Error) return 'error';
  if (value instanceof Map) return 'map';
  if (value instanceof Set) return 'set';
  return 'object';
}

function truncate(text: string, limit: number): string {
  return text.length <= limit ? text : `${text.slice(0, limit)}…`;
}

/** Rendered form of a leaf value. */
export function renderLeaf(value: unknown, kind: JsonValueKind, maxStringLength: number): string {
  switch (kind) {
    case 'string':
      return truncate(JSON.stringify(value) ?? '""', maxStringLength + 2);
    case 'number':
      // `String` rather than `JSON.stringify`, which turns NaN and Infinity
      // into `null` — the two values you most want to see in a debug pane.
      return Object.is(value, -0) ? '-0' : String(value);
    case 'boolean':
      return String(value);
    case 'null':
      return 'null';
    case 'undefined':
      return 'undefined';
    case 'bigint':
      return `${String(value)}n`;
    case 'symbol':
      return String(value);
    case 'function': {
      const name = (value as { name?: string }).name;
      return name ? `function ${name}()` : 'function ()';
    }
    case 'date': {
      const time = (value as Date).getTime();
      return Number.isNaN(time) ? 'Invalid Date' : (value as Date).toISOString();
    }
    case 'regexp':
      return String(value);
    case 'error': {
      const error = value as Error;
      return truncate(`${error.name}: ${error.message}`, maxStringLength);
    }
    case 'accessor':
      return '(accessor, not read)';
    case 'circular':
      return '[circular reference]';
    default:
      return '';
  }
}

function summarise(kind: JsonValueKind, count: number): string {
  switch (kind) {
    case 'array': return `[${count}]`;
    case 'object': return `{${count}}`;
    case 'map': return `Map(${count})`;
    case 'set': return `Set(${count})`;
    default: return '';
  }
}

interface BuildContext {
  budget: number;
  maxDepth: number;
  maxStringLength: number;
}

function isContainer(kind: JsonValueKind): boolean {
  return kind === 'object' || kind === 'array' || kind === 'map' || kind === 'set';
}

function buildNode(
  value: unknown,
  key: string | null,
  keyKind: JsonKeyKind,
  path: string,
  depth: number,
  ancestors: Set<object>,
  context: BuildContext,
): JsonNode {
  const kind = kindOf(value);

  const base: JsonNode = {
    path,
    key,
    keyKind,
    kind,
    value,
    text: '',
    summary: '',
    children: null,
    childCount: 0,
    depth,
    truncated: false,
  };

  if (!isContainer(kind)) {
    base.text = renderLeaf(value, kind, context.maxStringLength);
    return base;
  }

  const object = value as object;

  // Ancestors, not "everything seen": a shared reference is not a cycle.
  if (ancestors.has(object)) {
    base.kind = 'circular';
    base.text = renderLeaf(value, 'circular', context.maxStringLength);
    return base;
  }

  const entries: Array<{ key: string; keyKind: JsonKeyKind; value: unknown; accessor: boolean }> =
    [];

  if (kind === 'array') {
    const array = value as unknown[];
    for (let i = 0; i < array.length; i += 1) {
      entries.push({ key: String(i), keyKind: 'index', value: array[i], accessor: false });
    }
  } else if (kind === 'map') {
    let i = 0;
    (value as Map<unknown, unknown>).forEach((entryValue, entryKey) => {
      entries.push({
        key: typeof entryKey === 'object' && entryKey !== null ? `<object ${i}>` : String(entryKey),
        keyKind: 'entry',
        value: entryValue,
        accessor: false,
      });
      i += 1;
    });
  } else if (kind === 'set') {
    let i = 0;
    (value as Set<unknown>).forEach((entryValue) => {
      entries.push({ key: String(i), keyKind: 'index', value: entryValue, accessor: false });
      i += 1;
    });
  } else {
    for (const propertyKey of Object.keys(object)) {
      const descriptor = Object.getOwnPropertyDescriptor(object, propertyKey);
      if (!descriptor) continue;
      if ('get' in descriptor && descriptor.get) {
        // Never invoke an accessor to render it.
        entries.push({ key: propertyKey, keyKind: 'property', value: undefined, accessor: true });
      } else {
        entries.push({
          key: propertyKey,
          keyKind: 'property',
          value: descriptor.value,
          accessor: false,
        });
      }
    }
  }

  base.childCount = entries.length;
  base.summary = summarise(kind, entries.length);

  if (depth >= context.maxDepth) {
    base.truncated = entries.length > 0;
    base.children = entries.length > 0 ? [] : [];
    return base;
  }

  const children: JsonNode[] = [];
  ancestors.add(object);

  for (let i = 0; i < entries.length; i += 1) {
    const entry = entries[i];
    if (!entry) continue;
    if (context.budget <= 0) {
      base.truncated = true;
      break;
    }
    context.budget -= 1;

    const childPath = joinPath(path, entry.key, entry.keyKind);

    if (entry.accessor) {
      children.push({
        path: childPath,
        key: entry.key,
        keyKind: entry.keyKind,
        kind: 'accessor',
        value: undefined,
        text: renderLeaf(undefined, 'accessor', context.maxStringLength),
        summary: '',
        children: null,
        childCount: 0,
        depth: depth + 1,
        truncated: false,
      });
      continue;
    }

    children.push(
      buildNode(entry.value, entry.key, entry.keyKind, childPath, depth + 1, ancestors, context),
    );
  }

  ancestors.delete(object);
  base.children = children;
  return base;
}

export function buildTree(value: unknown, options: BuildTreeOptions = {}): JsonNode {
  const {
    maxDepth = 64,
    maxNodes = 20_000,
    maxStringLength = 400,
    rootKey = '$',
  } = options;

  return buildNode(
    value,
    null,
    'root',
    rootKey,
    0,
    new Set<object>(),
    { budget: maxNodes, maxDepth, maxStringLength },
  );
}

/**
 * JSON text for `copy value`, cycle-safe.
 *
 * `JSON.stringify` throws on a circular structure, so the replacer substitutes
 * a marker for any ancestor repeat — the same rule the tree itself uses, so
 * what you copy matches what you see.
 */
export function stringifyValue(value: unknown, space = 2): string {
  const stack: unknown[] = [];
  try {
    return (
      JSON.stringify(
        value,
        function replacer(this: unknown, _key: string, current: unknown) {
          if (typeof current === 'bigint') return `${String(current)}n`;
          if (typeof current === 'function') return '[function]';
          if (typeof current === 'symbol') return String(current);
          if (typeof current === 'object' && current !== null) {
            // `this` is the container being serialised, so unwinding here keeps
            // the stack aligned with the current path rather than with
            // everything ever seen.
            while (stack.length > 0 && stack[stack.length - 1] !== this) stack.pop();
            if (stack.includes(current)) return '[circular reference]';
            stack.push(current);
          }
          return current;
        },
        space,
      ) ?? String(value)
    );
  } catch {
    return String(value);
  }
}

export interface SearchScan {
  /** Paths of nodes whose key or value text matched. */
  matches: Set<string>;
  /** Paths that must be open for every match to be reachable. */
  expand: Set<string>;
}

/**
 * One walk that finds matches and the ancestors needed to reveal them.
 *
 * Done in a single pass rather than "find matches, then look up each one's
 * ancestors" — the second shape is quadratic, and on a 20,000-node tree with a
 * one-character query that is the difference between typing smoothly and not.
 */
export function scanForMatches(
  root: JsonNode,
  test: (node: JsonNode) => boolean,
): SearchScan {
  const matches = new Set<string>();
  const expand = new Set<string>();
  const trail: string[] = [];

  const walk = (node: JsonNode): void => {
    if (test(node)) {
      matches.add(node.path);
      for (let i = 0; i < trail.length; i += 1) {
        const ancestor = trail[i];
        if (ancestor !== undefined) expand.add(ancestor);
      }
    }
    if (!node.children) return;
    trail.push(node.path);
    for (let i = 0; i < node.children.length; i += 1) {
      const child = node.children[i];
      if (child) walk(child);
    }
    trail.pop();
  };

  walk(root);
  return { matches, expand };
}
