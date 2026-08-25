/* ===========================================================================
 * THE PATH EDITOR
 *
 * A dedicated surface at #/paths/edit, because composing a group is the one
 * genuinely structural task in this panel and it had been getting a single
 * inline field.
 *
 * THE ARCHITECTURE, IN ONE LINE
 * -----------------------------
 * The expression string is the document. The ladder, the inspector and the
 * diagnostics are projections of it, and every structural gesture writes back
 * as a splice. There is no second model to keep in sync, so there is no round
 * trip to lose and no drag that can reformat a line.
 *
 * WHY A GROUP IS "N WAYS TO ONE TERMINAL"
 * ---------------------------------------
 * `validateStrategy` requires every path in an intent to reach the same
 * terminal (compiler.go:38-51). A group is multipath to one destination, not
 * load balancing across destinations, and the terminal is therefore drawn ONCE,
 * to the right of all the rows, as the thing they converge on. When they do not
 * agree, the shared anchor splits and the dissenting row visibly fails to reach
 * it — before any compile.
 *
 * NOTHING HERE IS SAVED
 * ---------------------
 * `path` has one subcommand, `compile`, and `RouteIntent` is never persisted.
 * The draft lives in the address bar and nowhere else. That is stated in the
 * header rather than implied, and there is deliberately no `beforeunload`
 * guard: the URL holds the document, so a guard would be theatre.
 * ======================================================================== */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardHeader,
  Combobox,
  EditableList,
  EmptyState,
  Field,
  IconArrowRight,
  IconCheck,
  IconCopy,
  IconPath,
  IconPlay,
  InlineMessage,
  PageHeader,
  Row,
  Screen,
  SegmentedControl,
  Select,
  Textarea,
  Tooltip,
} from '@stratum/ui';
import type { CompileResult, EndpointKind, PeersResponse, Strategy } from '../api/types';
import { compilePath as requestPathCompile, getPeers } from '../api/control';
import { useDomainRead } from '../state/useDomainRead';
import { describeFailure, type FailureView } from '../api/errors';
import { useControl } from '../state/store';
import { Absent } from '../components/Absent';
import {
  addPath,
  applySplice,
  canonical,
  compilablePaths,
  insertHopAfter,
  pathAtCaret,
  removeHop,
  removePath,
  reorderPaths,
  replaceHop,
  tokenize,
  transformCaret,
  type Parsed,
  type Splice,
} from '../path/grammar';
import { buildTopology, hopCandidates, type Topology } from '../path/topology';
import { check, type Finding } from '../path/check';

const STRATEGIES: { value: Strategy; label: string }[] = [
  { value: 'selector', label: 'selector' },
  { value: 'race', label: 'race' },
  { value: 'bond', label: 'bond' },
  { value: 'peak', label: 'peak' },
];

/** What order means, which is different for every strategy and load-bearing for one. */
const ORDER_MEANS: Record<Strategy, string> = {
  selector: 'Order sets the selector preference. The first path is tried first.',
  race: 'Order does not matter. All paths are raced together.',
  bond: 'Order does not matter. All paths carry traffic together.',
  peak: 'Order decides the peak candidate — it is the last path.',
};

const ENDPOINTS: { value: EndpointKind; label: string; description: string }[] = [
  { value: 'rendr_stream', label: 'rendr stream', description: 'Stream-oriented rendr endpoint.' },
  { value: 'rendr_packet', label: 'rendr packet', description: 'Packet-oriented rendr endpoint.' },
  {
    value: 'egress',
    label: 'egress',
    description: 'Leaves the mesh at the terminal. The only endpoint that does not require the terminal to speak rendr.',
  },
];

/* -- The draft lives in the query string ----------------------------------- */

interface Draft {
  expression: string;
  strategy: Strategy;
  endpoint: EndpointKind;
}

function readDraft(query: string): Draft {
  const q = new URLSearchParams(query);
  const s = q.get('s');
  const k = q.get('k');
  return {
    expression: q.get('e') ?? '',
    strategy: STRATEGIES.some((x) => x.value === s) ? (s as Strategy) : 'selector',
    endpoint: ENDPOINTS.some((x) => x.value === k) ? (k as EndpointKind) : 'rendr_stream',
  };
}

const draftQuery = (d: Draft): string =>
  `e=${encodeURIComponent(d.expression)}&s=${d.strategy}&k=${d.endpoint}`;

/* -- Syntax-coloured expression -------------------------------------------- */

function ExpressionField({
  value,
  findings,
  onChange,
  onCaret,
  textareaRef,
}: {
  value: string;
  findings: Finding[];
  onChange: (next: string, caret: number) => void;
  onCaret: (caret: number) => void;
  textareaRef: React.RefObject<HTMLTextAreaElement | null>;
}) {
  return (
    <Textarea
      ref={textareaRef}
      value={value}
      monospace
      fullWidth
      invalid={findings.some((finding) => finding.blocking)}
      spellCheck={false}
      autoComplete="off"
      autoCorrect="off"
      autoCapitalize="off"
      aria-label="Path expression"
      aria-describedby="xtier-expr-help"
      rows={2}
      onChange={(e) => onChange(e.target.value, e.target.selectionStart)}
      onKeyUp={(e) => onCaret(e.currentTarget.selectionStart)}
      onClick={(e) => onCaret(e.currentTarget.selectionStart)}
    />
  );
}

/* -- The screen ------------------------------------------------------------ */

export function PathEditor({ query, onLeave }: { query: string; onLeave: () => void }) {
  const { local, revision, revisionRead } = useControl();
  // Peers are their own read: `local status` carries counts, not the book.
  const peers = useDomainRead<PeersResponse>('peers', getPeers, [revision]);

  const [draft, setDraft] = useState<Draft>(() => readDraft(query));
  const [caret, setCaret] = useState(0);
  const [result, setResult] = useState<CompileResult | null>(null);
  const [failure, setFailure] = useState<FailureView | null>(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const pendingCaret = useRef<number | null>(null);

  const parsed = useMemo(() => tokenize(draft.expression), [draft.expression]);
  const live = useMemo(() => compilablePaths(parsed), [parsed]);

  const topo: Topology = useMemo(
    () => buildTopology(local?.node ?? null, peers.data?.peers ?? null),
    [local, peers.data],
  );
  // A read that returned is a book we have. A read that failed is not, and the
  // difference has to reach the operator rather than looking like an empty book.
  const bookRead = !!peers.data;

  const verdict = useMemo(
    () => check(parsed, topo, draft.strategy, draft.endpoint),
    [parsed, topo, draft.strategy, draft.endpoint],
  );

  /* The address bar IS the document store. `replaceState` rather than
   * assignment: one history entry per keystroke would make Back useless. */
  const lastWritten = useRef<string>(draftQuery(draft));
  useEffect(() => {
    const id = window.setTimeout(() => {
      const q = draftQuery(draft);
      lastWritten.current = q;
      window.history.replaceState(null, '', `#/paths/edit?${q}`);
    }, 400);
    return () => window.clearTimeout(id);
  }, [draft]);

  /*
   * Re-seed when the query changes from OUTSIDE.
   *
   * The route key is `paths/edit` either way, so opening a different draft
   * while the editor is already mounted does not remount it — and a `useState`
   * initialiser runs once. Without this, following a shared "Copy link" from
   * inside the editor silently keeps your own draft and shows someone else's
   * URL, which is the worst of both.
   *
   * The guard is what makes it safe: this component rewrites the query itself
   * on every edit, and re-seeding from its own write would clobber the caret
   * and fight every keystroke. Only a query it did not write is a navigation.
   */
  useEffect(() => {
    if (query === lastWritten.current) return;
    lastWritten.current = query;
    setDraft(readDraft(query));
    setResult(null);
    setFailure(null);
  }, [query]);

  // Restore the caret after a splice, once React has painted the new value.
  useEffect(() => {
    if (pendingCaret.current == null) return;
    const el = textareaRef.current;
    const at = pendingCaret.current;
    pendingCaret.current = null;
    if (!el) return;
    el.focus();
    el.setSelectionRange(at, at);
    setCaret(at);
  }, [draft.expression]);

  const splice = useCallback(
    (s: Splice) => {
      if (s.start === 0 && s.end === 0 && s.text === '') return;
      setDraft((d) => ({ ...d, expression: applySplice(d.expression, s) }));
      pendingCaret.current = transformCaret(caret, s);
    },
    [caret],
  );

  const activePath = useMemo(() => pathAtCaret(parsed, caret), [parsed, caret]);
  const active = parsed.paths[activePath] ?? null;

  const compile = useCallback(async () => {
    const expr = canonical(parsed);
    if (!expr) return;
    setBusy(true);
    setFailure(null);
    try {
      const res = await requestPathCompile(expr, draft.strategy, draft.endpoint);
      setResult(res);
    } catch (e) {
      setResult(null);
      setFailure(describeFailure(e));
    } finally {
      setBusy(false);
    }
  }, [parsed, draft.strategy, draft.endpoint]);

  const copy = useCallback((what: string, text: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(what);
    window.setTimeout(() => setCopied((c) => (c === what ? null : c)), 1600);
  }, []);

  const cliText = `xtier path compile '${canonical(parsed)}' --strategy=${draft.strategy} --endpoint=${draft.endpoint} --json`;

  /* -- Rows for the ladder. Objects, because EditableList hands the SAME
   * objects back after a reorder and identity is how the permutation is
   * recovered without inventing keys. */
  const rows = useMemo(
    () => parsed.paths.map((p, i) => ({ i, raw: p.raw })),
    [parsed],
  );

  const onRows = useCallback(
    (next: { i: number; raw: string }[]) => {
      if (next.length === rows.length) {
        splice(reorderPaths(parsed, next.map((r) => r.i)));
        return;
      }
      if (next.length > rows.length) {
        splice(addPath(parsed));
        return;
      }
      const gone = rows.find((r) => !next.includes(r));
      if (gone) splice(removePath(parsed, gone.i));
    },
    [rows, parsed, splice],
  );

  const findingsFor = (pathIndex: number): Finding[] =>
    verdict.findings.filter((f) => f.path === pathIndex);

  const blocking = verdict.findings.filter((f) => f.blocking);
  const advice = verdict.findings.filter((f) => !f.blocking);

  return (
    <Screen
      header={
        <PageHeader
          title="Edit intent"
          description="Compose a group of paths and read the plan it produces. Nothing here is saved."
          meta={
            <Badge variant="neutral" size="sm">
              plan only
            </Badge>
          }
          actions={
            <Row gap="var(--stratum-space-4)" wrap={false}>
              <Button size="sm" variant="ghost" onClick={onLeave}>
                Back to Paths
              </Button>
              <Button
                size="sm"
                variant="default"
                icon={copied === 'link' ? <IconCheck /> : <IconCopy />}
                onClick={() =>
                  copy('link', `${window.location.origin}${window.location.pathname}#/paths/edit?${draftQuery(draft)}`)
                }
              >
                {copied === 'link' ? 'Copied' : 'Copy link'}
              </Button>
              <Button
                size="sm"
                variant="default"
                icon={copied === 'cli' ? <IconCheck /> : <IconCopy />}
                disabled={live.length === 0}
                onClick={() => copy('cli', cliText)}
              >
                {copied === 'cli' ? 'Copied' : 'Copy command'}
              </Button>
            </Row>
          }
        />
      }
    >
      <Banner variant="info" title="The draft lives in the address bar">
        The control plane has no place to store a path — <code>path</code> only compiles. Copy the
        link to keep this draft or hand it to someone else.
      </Banner>

      {/* -- The document ---------------------------------------------- */}
      <Card variant="outlined" padding="none">
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="Expression"
          description="Comma separates paths. Slash separates hops. This node is hop zero and is never written."
        />
        <CardBody padding="sm">
          <ExpressionField
            value={draft.expression}
            findings={verdict.findings}
            textareaRef={textareaRef}
            onCaret={setCaret}
            onChange={(next, at) => {
              setDraft((d) => ({ ...d, expression: next }));
              setCaret(at);
            }}
          />
          <p
            id="xtier-expr-help"
            style={{
              margin: 'var(--stratum-space-6) 0 0',
              fontSize: 'var(--stratum-text-xs)',
              color: 'var(--stratum-text-muted)',
            }}
          >
            {live.length === 0 ? (
              'No paths yet.'
            ) : (
              <>
                {live.length} {live.length === 1 ? 'path' : 'paths'} ·{' '}
                {verdict.sharedTerminal ? (
                  <>
                    terminal <strong>{verdict.sharedTerminal}</strong>
                  </>
                ) : (
                  <strong style={{ color: 'var(--stratum-danger-fg)' }}>terminals disagree</strong>
                )}
                {' · '}
                {!bookRead
                  ? 'address book not read, so nothing has been checked'
                  : blocking.length === 0
                    ? 'no local objection'
                    : `${blocking.length} local ${blocking.length === 1 ? 'objection' : 'objections'}`}
                {verdict.peakCandidate >= 0 && (
                  <>
                    {' · peak candidate '}
                    <strong>path {(parsed.paths[verdict.peakCandidate]?.ordinal ?? 0) + 1}</strong>
                  </>
                )}
              </>
            )}
          </p>
        </CardBody>
      </Card>

      {/* -- Structure --------------------------------------------------- */}
      <Card variant="outlined" padding="none">
        <CardHeader padding="sm" headingLevel={2} title="Structure" />
        <CardBody padding="sm">
          <div style={{ display: 'grid', gap: 'var(--stratum-space-10)' }}>
            <div
              style={{
                display: 'grid',
                gap: 'var(--stratum-space-8)',
                gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 20rem), 1fr))',
              }}
            >
              {/*
                * Both controls take their accessible name from the Field's
                * label through the render prop.
                *
                * `Select` renders a `role="combobox"` BUTTON, not an input, so
                * the Field's `<label for>` cannot name it the way it names a
                * text field — the label has to be pointed at explicitly. Axe
                * caught this as a critical `button-name` violation on exactly
                * this node, which is why the editor is in the a11y sweep.
                */}
              <Field label="Strategy" description={ORDER_MEANS[draft.strategy]}>
                {(control) => (
                  <SegmentedControl
                    size="sm"
                    value={draft.strategy}
                    onValueChange={(v) => setDraft((d) => ({ ...d, strategy: v as Strategy }))}
                    aria-labelledby={control.labelId}
                    aria-describedby={control.describedBy}
                    items={STRATEGIES.map((s) => ({ value: s.value, children: s.label }))}
                  />
                )}
              </Field>
              <Field
                label="Endpoint"
                description={ENDPOINTS.find((e) => e.value === draft.endpoint)?.description}
              >
                {(control) => (
                  <Select
                    size="sm"
                    fullWidth
                    id={control.id}
                    aria-labelledby={control.labelId}
                    aria-describedby={control.describedBy}
                    value={draft.endpoint}
                    onChange={(v) => v && setDraft((d) => ({ ...d, endpoint: v as EndpointKind }))}
                    options={ENDPOINTS.map((e) => ({
                      value: e.value,
                      label: e.label,
                      description: e.description,
                    }))}
                  />
                )}
              </Field>
            </div>

            {draft.strategy === 'peak' && live.length === 1 && (
              <InlineMessage variant="warning">
                Peak needs two or more paths to do anything. With one path the daemon builds a plain
                selector, and nothing in the compiled plan will say so.
              </InlineMessage>
            )}

            <EditableList
              items={rows}
              onItemsChange={onRows}
              reorderable
              showRank
              addable
              removable
              minRows={1}
              itemLabel="Path"
              addLabel="Add path"
              createItem={() => ({ i: -1, raw: '' })}
              emptyState={
                <EmptyState
                  size="sm"
                  icon={<IconPath />}
                  title="No paths yet"
                  description="Add a path, then choose the hops it goes through."
                />
              }
              labelMoved={(from, to, count) => {
                if (draft.strategy !== 'peak') {
                  return `Path ${from + 1} moved to position ${to + 1} of ${count}.`;
                }
                return `Path ${from + 1} moved to position ${to + 1} of ${count}. The peak candidate is now the path in position ${count}.`;
              }}
              renderRow={(item) => {
                const p = parsed.paths[item.i];
                if (!p) return null;
                const res = verdict.resolutions.find((r) => r.path === item.i);
                const mine = findingsFor(item.i);
                const isPeak = verdict.peakCandidate === item.i;
                return (
                  <div style={{ display: 'grid', gap: 'var(--stratum-space-4)', flex: 1 }}>
                    <Row gap="var(--stratum-space-4)">
                      <span
                        style={{
                          fontSize: 'var(--stratum-text-2xs)',
                          color: 'var(--stratum-text-subtle)',
                        }}
                      >
                        this node
                      </span>
                      {p.hops.length === 0 ? (
                        <span
                          style={{
                            fontSize: 'var(--stratum-text-xs)',
                            color: 'var(--stratum-text-muted)',
                            fontStyle: 'italic',
                          }}
                        >
                          empty — ignored by the resolver
                        </span>
                      ) : (
                        p.hops.map((h, hi) => {
                          const bad = mine.some((f) => f.blocking && f.hop === hi);
                          return (
                            <Row key={hi} gap="var(--stratum-space-3)" wrap={false}>
                              <IconArrowRight
                                style={{ color: 'var(--stratum-text-subtle)' }}
                                size="0.85em"
                              />
                              <code
                                style={{
                                  fontSize: 'var(--stratum-text-xs)',
                                  color: bad
                                    ? 'var(--stratum-danger-fg)'
                                    : 'var(--stratum-text)',
                                }}
                              >
                                {h.text}
                              </code>
                            </Row>
                          );
                        })
                      )}
                      {isPeak && (
                        <Tooltip
                          trigger={
                            <Badge size="xs" variant="accent">
                              peak candidate
                            </Badge>
                          }
                        >
                          The last path is the peak candidate. Move a different path here to change
                          it.
                        </Tooltip>
                      )}
                      {res?.terminal && res.ok && (
                        <Badge size="xs" variant="neutral">
                          {res.carrier === 'direct' ? 'direct' : 'relay chain'}
                        </Badge>
                      )}
                    </Row>
                    {mine.map((f, i) => (
                      <InlineMessage key={i} variant={f.blocking ? 'danger' : 'warning'} size="sm">
                        {f.message}
                      </InlineMessage>
                    ))}
                  </div>
                );
              }}
            />
          </div>
        </CardBody>
      </Card>

      {/* -- The hops of the path the caret is in ------------------------ */}
      {active && (
        <Card variant="outlined" padding="none">
          <CardHeader
            padding="sm"
            headingLevel={2}
            title={
              active.ordinal >= 0
                ? `Path ${active.ordinal + 1} of ${live.length}`
                : 'Empty segment'
            }
            description={
              active.ordinal >= 0
                ? 'Hops resolve by node ID. The list shows what can be dialled from the hop before it.'
                : 'This segment has no hops, so the resolver drops it.'
            }
          />
          <CardBody padding="sm">
            <HopInspector
              parsed={parsed}
              pathIndex={activePath}
              topo={topo}
              bookRead={bookRead}
              onSplice={splice}
            />
          </CardBody>
        </Card>
      )}

      {/* -- Compile ----------------------------------------------------- */}
      <Card variant="outlined" padding="sm">
        <Row gap="var(--stratum-space-8)">
          <Button
            variant="primary"
            icon={<IconPlay />}
            loading={busy}
            disabled={live.length === 0}
            onClick={() => void compile()}
          >
            Compile
          </Button>
          <span style={{ fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-muted)' }}>
            {revisionRead ? (
              <>Local checks ran against revision {revision}.</>
            ) : (
              'The address book has not been read, so nothing has been checked locally.'
            )}{' '}
            Compiling resolves the hops and returns a plan. It installs nothing and moves no
            traffic.
          </span>
        </Row>
        {advice.length > 0 && (
          <div style={{ display: 'grid', gap: 'var(--stratum-space-4)', marginBlockStart: 'var(--stratum-space-8)' }}>
            {advice
              .filter((f) => f.path === -1)
              .map((f, i) => (
                <InlineMessage key={i} variant="warning" size="sm">
                  {f.message}
                </InlineMessage>
              ))}
          </div>
        )}
      </Card>

      {failure && (
        <Banner variant="danger" title={failure.title}>
          {failure.guidance}
        </Banner>
      )}

      {result && <CompiledPlan result={result} />}
    </Screen>
  );
}

/* -- Hop inspector --------------------------------------------------------- */

function HopInspector({
  parsed,
  pathIndex,
  topo,
  bookRead,
  onSplice,
}: {
  parsed: Parsed;
  pathIndex: number;
  topo: Topology;
  bookRead: boolean;
  onSplice: (s: Splice) => void;
}) {
  const p = parsed.paths[pathIndex];
  if (!p) return null;

  const hopIds = p.hops.map((h) => h.text);

  /**
   * The candidate list is position-dependent, because the rule is:
   * an INTERMEDIATE hop needs the link to permit nested expansion, the last one
   * does not. So the same peer can be a legal destination and an illegal middle
   * hop, and the picker says which at the position it is being used.
   *
   * Nothing is filtered out. A peer that silently vanishes from a list teaches
   * the operator nothing; one shown as "destination only" teaches them the rule.
   */
  const optionsFor = (index: number) => {
    const origin = index === 0 ? topo.local : (hopIds[index - 1] ?? topo.local);
    const exclude = new Set(hopIds.filter((_, i) => i !== index));
    const isFinal = index === hopIds.length - 1;
    return hopCandidates(topo, origin, exclude).map((c) => {
      const usable = c.dialable && (isFinal || c.transitable);
      return {
        value: c.id,
        label: c.displayName ? `${c.id} — ${c.displayName}` : c.id,
        disabled: !usable,
        group: usable
          ? isFinal
            ? 'Dialable'
            : 'Dialable · may transit'
          : !c.dialable
            ? 'Cannot be dialled from here'
            : 'Destination only — cannot be a middle hop',
        description: !c.dialable
          ? c.reason
          : !isFinal && !c.transitable
            ? 'The link is not enabled for nested expansion.'
            : c.reversed
              ? 'Reachable because the link accepts inbound in the other direction.'
              : undefined,
      };
    });
  };

  // Can the chain be extended? Only if the link into the LAST hop permits it.
  const lastId = hopIds[hopIds.length - 1];
  const prevId = hopIds.length > 1 ? hopIds[hopIds.length - 2] : topo.local;
  const lastCandidate = lastId
    ? hopCandidates(topo, prevId ?? topo.local, new Set()).find((c) => c.id === lastId)
    : undefined;
  const canExtend = hopIds.length === 0 || !!lastCandidate?.transitable;

  return (
    <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
      {!bookRead && (
        <InlineMessage variant="warning" size="sm">
          The address book could not be read, so the choices below are unavailable. Hops can still
          be typed in the expression, and compiling will tell you the truth.
        </InlineMessage>
      )}

      {p.hops.map((h, i) => (
        <Row key={i} gap="var(--stratum-space-6)" wrap={false}>
          <span
            style={{
              fontSize: 'var(--stratum-text-2xs)',
              color: 'var(--stratum-text-subtle)',
              minInlineSize: '1.5rem',
            }}
          >
            {i + 1}
          </span>
          <div style={{ flex: 1, minInlineSize: 0 }}>
            {/*
              * Keyed on the value so an edit made in the EXPRESSION FIELD is
              * reflected here.
              *
              * `value` is the committed option; the text in the field is
              * `inputValue`, which is separately controllable. Controlling it
              * outright would mean re-deriving it on every keystroke and
              * fighting the caret, and leaving it uncontrolled would strand it
              * when the document changes underneath. Seeding a default and
              * remounting on a genuine document change gets both: local typing
              * is native, and an external splice re-seeds.
              */}
            <Combobox
              key={`${pathIndex}:${i}:${h.text}`}
              size="sm"
              fullWidth
              defaultValue={h.text}
              defaultInputValue={h.text}
              options={optionsFor(i)}
              allowCustomValue
              placeholder="Node ID"
              aria-label={`Hop ${i + 1}`}
              onChange={(v) => onSplice(replaceHop(parsed, pathIndex, i, v ?? ''))}
            />
          </div>
          <Button
            size="xs"
            variant="ghost"
            onClick={() => onSplice(removeHop(parsed, pathIndex, i))}
            aria-label={`Remove hop ${i + 1}`}
          >
            Remove
          </Button>
        </Row>
      ))}

      <Row gap="var(--stratum-space-6)">
        {canExtend ? (
          <Button
            size="sm"
            variant="default"
            onClick={() => onSplice(insertHopAfter(parsed, pathIndex, p.hops.length - 1))}
          >
            Add hop
          </Button>
        ) : (
          <Tooltip
            trigger={
              <span style={{ display: 'inline-flex' }}>
                <Button size="sm" variant="default" disabled>
                  Add hop
                </Button>
              </span>
            }
          >
            The link into <code>{lastId}</code> is not enabled for nested expansion, so{' '}
            <code>{lastId}</code> can only be the last hop.
          </Tooltip>
        )}
      </Row>
    </div>
  );
}

/* -- What came back -------------------------------------------------------- */

function CompiledPlan({ result }: { result: CompileResult }) {
  const compiled = result.compiled;
  if (!compiled) return null;
  return (
    <Card variant="outlined" padding="none">
      <CardHeader
        padding="sm"
        headingLevel={2}
        title="Compiled plan"
        description="Resolved by the daemon. It installed nothing and moved no traffic."
        actions={
          <Badge size="sm" variant="neutral">
            {compiled.target?.kind ?? <Absent />} · {compiled.session_kind ?? <Absent />}
          </Badge>
        }
      />
      <CardBody padding="sm">
        <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
          {(compiled.resolved_paths ?? []).map((p, i) => (
            <Row key={i} gap="var(--stratum-space-6)">
              <Badge size="xs" variant="neutral">
                {i + 1}
              </Badge>
              {/* hops[0] is always the local node and its raw ID is 60-odd
                * characters. Printing it would make every row start with the
                * same unreadable string, so it is named instead — which is also
                * how the expression treats it: never written. */}
              <code style={{ fontSize: 'var(--stratum-text-xs)' }}>
                this node
                {p.hops.slice(1).map((h) => ` / ${h}`).join('')}
              </code>
              <Badge size="xs" variant={p.legacy_carrier_kind === 'direct' ? 'neutral' : 'accent'}>
                {p.legacy_carrier_kind === 'relay_chain'
                  ? 'relay chain'
                  : (p.legacy_carrier_kind ?? <Absent />)}
              </Badge>
            </Row>
          ))}
        </div>
      </CardBody>
    </Card>
  );
}
