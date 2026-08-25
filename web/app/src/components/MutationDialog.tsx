/* ===========================================================================
 * THE CHANGE DIALOG
 *
 * Every change in this panel goes through the same three steps, because the
 * daemon offers all three and skipping them throws away safety the backend has
 * already paid for:
 *
 *   1  CHECK     the change is submitted with `dry_run` the moment the dialog
 *                opens. It alters nothing, so there is no reason to make the
 *                operator ask for it, and its answer is the daemon's own
 *                account of what would happen rather than the panel's guess.
 *   2  SHOW      what would change, in the operator's terms.
 *   3  APPLY     the same change, once, against the revision it was checked at.
 *
 * WHY THE CHECK IS AUTOMATIC
 * --------------------------
 * A confirmation dialog that only restates the operator's intent is theatre: it
 * asks "are you sure?" about the thing they just clicked, and the answer is
 * always yes. One that has already asked the daemon can say something they did
 * not know — that a name collides, that a hop forbids nesting, that the
 * configuration has already moved. That turns the confirm step from a speed
 * bump into an actual check.
 *
 * A check that FAILS is the most valuable outcome: the change is refused before
 * it is attempted, with the daemon's reason visible. `blocked` from the error
 * catalogue decides whether Apply comes back at all — some refusals are worth
 * another look after a re-read, and some cannot possibly succeed.
 *
 * WHY THE REQUEST DETAIL IS SUBORDINATE
 * -------------------------------------
 * It sits behind a disclosure rather than at the top. The panel and the CLI are
 * two ways into the same daemon; neither is a skin over the other. An earlier
 * version led with the exact argv and framed its forms as ways of assembling
 * one, which taught the operator to think in a vocabulary that belongs to a
 * different client. The detail stays available — this is a tool for people who
 * will want to reproduce something in a terminal — but it is evidence, not the
 * thing being approved.
 *
 * WHY APPLY IS STILL OFFERED AFTER A CONFLICT
 * -------------------------------------------
 * A revision conflict means another writer committed. That does NOT make the
 * operator's intent wrong — only stale. The dialog re-reads, re-checks against
 * the new revision, and lets them look again. What it never does is retry
 * silently: the other writer's change is unknown here, and quietly overwriting
 * it is the failure this whole mechanism exists to prevent.
 * ======================================================================== */
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Banner,
  Button,
  Code,
  Dialog,
  Disclosure,
  IconCheck,
  InlineMessage,
  JsonViewer,
  Row,
  Spinner,
  toast,
} from '@stratum/ui';
import {
  CommandFailure,
  TransportFailure,
  newRequestId,
  type DomainMutation,
} from '../api/control';
import type { MutationResponse } from '../api/types';
import { describeFailure, type FailureView } from '../api/errors';
import { useControl } from '../state/store';
import { FailureNotice } from './FailureNotice';

export interface MutationSpec {
  /** Typed backend operation used unchanged for the dry run and apply. */
  operation: DomainMutation;
  /** Dialog title. State the effect, not the mechanism. */
  title: string;
  /** One line of context above the command. */
  description?: React.ReactNode;
  /** Label on the confirm button. Default `'Apply'`. */
  confirmLabel?: string;
  /** Uses the destructive button treatment. */
  destructive?: boolean;
  /**
   * Renders the daemon's dry-run payload as prose.
   *
   * Worth supplying: the raw payload is a `would_set` object, and an operator
   * reading "peer fra will be disabled" is better served than one reading
   * `{"name":"fra","enabled":false}`. The raw form stays available underneath
   * either way — this replaces the summary, never the evidence.
   */
  summarise?: (payload: unknown) => React.ReactNode;
}

export interface MutationDialogProps {
  spec: MutationSpec | null;
  onClose: () => void;
  /** Fired once the write has actually landed. */
  onApplied?: (result: unknown) => void;
}

type Phase =
  | 'checking'
  | 'ready'
  | 'refused'
  | 'applying'
  | 'applied'
  | 'failed'
  /**
   * The request left the panel and the answer never came back.
   *
   * This is NOT a failure, and presenting it as one is the worst thing this
   * dialog could do: the daemon may well have executed the write and only the
   * response was lost. The dialog therefore withholds Apply, re-reads, and
   * says plainly that it does not know.
   */
  | 'unknown';

export function MutationDialog({ spec, onClose, onApplied }: MutationDialogProps) {
  const { mutate, revision, refresh } = useControl();
  const operationKey = spec
    ? `${spec.operation.method}\u0000${spec.operation.path}\u0000${JSON.stringify(spec.operation.body)}`
    : null;

  const [phase, setPhase] = useState<Phase>('checking');
  const [preview, setPreview] = useState<unknown>(null);
  const [failure, setFailure] = useState<FailureView | null>(null);
  const [result, setResult] = useState<unknown>(null);
  /** Revision the visible dry run was checked against. */
  const [checkedAt, setCheckedAt] = useState(revision);
  const [checkedOperationKey, setCheckedOperationKey] = useState<string | null>(null);

  // Guards a dry run that returns after the dialog moved on — closed, or a
  // second check started. Without it a slow first check can overwrite the
  // result of the retry that replaced it.
  const run = useRef(0);

  const dryRun = useCallback(async () => {
    if (!spec) return;
    const ticket = ++run.current;
    setPhase('checking');
    setFailure(null);
    setCheckedOperationKey(null);
    try {
      const payload = await mutate<unknown>(spec.operation, { dryRun: true });
      if (ticket !== run.current) return;
      setPreview(payload);
      /* From the daemon's own answer, not from this render.
       *
       * `before_revision` IS the revision the check ran against, so it is
       * correct even when the dry run was issued after a refresh whose new
       * value this closure never saw. Reading the rendered `revision` here
      * would mark a perfectly fresh check as stale. */
      const before = (payload as MutationResponse | null)?.before_revision;
      if (typeof before !== 'number' || !Number.isSafeInteger(before)) {
        throw new Error('control.response_invalid: dry run omitted before_revision');
      }
      setCheckedAt(before);
      setCheckedOperationKey(operationKey);
      setPhase('ready');
    } catch (err) {
      if (ticket !== run.current) return;
      setFailure(describeFailure(err));
      setPreview(null);
      setPhase('refused');
    }
  }, [spec, mutate, operationKey, revision]);

  // Re-checks whenever a different typed operation is dialled up. The CLI
  // equivalent is only a stable local key and never selects a network route.
  useEffect(() => {
    if (!spec) return;
    setResult(null);
    setCheckedOperationKey(null);
    applyRequestId.current = null;
    applyRevision.current = null;
    applyOperationKey.current = null;
    void dryRun();
    // `dryRun` is intentionally excluded: it closes over `revision`, which
    // changes on every write, and re-checking on that would re-run the dialog's
    // dry run immediately after its own apply.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [operationKey]);

  /**
   * Reused across a retry of THIS intent.
   *
   * While the process-local request record exists, the daemon returns its
   * original outcome. After restart or cache expiry, replaying the original
   * revision is still fail-closed: CAS rejects it if the first write landed,
   * and executes it only if that revision remains current.
   */
  const applyRequestId = useRef<string | null>(null);
  /**
   * The revision the apply was actually SENT with.
   *
   * Two jobs. A resend has to replay it, because the daemon compares the whole
   * request and not just the id — reusing the id under a moved-on revision is
   * a different request and comes back 409, which is exactly what the first
   * version of this did. And the command shown after an unknown outcome has to
   * be the one that was sent, not one re-rendered against a revision the
   * refresh has since moved.
   */
  const applyRevision = useRef<number | null>(null);
  const applyOperationKey = useRef<string | null>(null);

  const apply = async () => {
    if (!spec) return;
    const replayUnknown = phase === 'unknown';
    if (!replayUnknown && (
      phase !== 'ready' || checkedAt !== revision || checkedOperationKey !== operationKey
    )) return;
    if (replayUnknown && (
      applyRequestId.current === null || applyRevision.current === null
      || applyOperationKey.current !== operationKey
    )) return;
    setPhase('applying');
    setFailure(null);
    applyRequestId.current ??= newRequestId();
    applyRevision.current ??= checkedAt;
    applyOperationKey.current ??= operationKey;
    try {
      const applied = await mutate<unknown>(spec.operation, {
        requestId: applyRequestId.current,
        revision: applyRevision.current,
      });
      setResult(applied);
      setPhase('applied');
      /*
       * The dialog's own success line dies with the dialog. An operator who
       * closes it is left with a revision badge that quietly incremented in a
       * corner — no statement that the thing they asked for happened. The
       * toast names the effect and gets out of the way.
       */
      /*
       * `after_revision` from the response, not the rendered value.
       *
       * `mutate` refreshes before it resolves, but this code runs synchronously
       * after that await — before React has committed the render carrying the
       * new revision. Reading state here reports the revision the change was
       * composed against, which is the one number guaranteed to be wrong.
       */
      const after = (applied as MutationResponse | null)?.after_revision;
      toast.success(
        after != null ? `Applied at revision ${after}` : 'Applied',
        { title: spec.title },
      );
      onApplied?.(applied);
    } catch (err) {
      // The response was lost in transit. The write may have landed; the panel
      // cannot tell, and must not claim either way.
      if (err instanceof TransportFailure && err.outcomeUnknown) {
        setFailure(describeFailure(err));
        setPhase('unknown');
        /*
         * Pinned — `duration: null`. This is the one outcome an operator must
         * not scroll past: the change may or may not have been made, and the
         * panel cannot tell. A toast that expired would take the only notice
         * of it with it.
         */
        toast({
          variant: 'warning',
          duration: null,
          title: 'Outcome unknown',
          message: `${spec.title} — the daemon did not answer. It may have been applied.`,
        });
        // Re-read so whatever IS visible on screen is current, and so the
        // revision tells the operator whether something moved.
        void refresh();
        return;
      }
      const view = describeFailure(err);
      setFailure(view);

      /*
       * A settled failure means the daemon ANSWERED, so there is nothing to
       * replay — and the answer is cached against this request_id regardless
       * of its exit code. Reusing the id would make every subsequent press
       * return that same cached failure without the command ever running
       * again, so a conflict the operator then resolves could never be
       * applied. Retrying is a NEW intent and gets a new id.
       */
      applyRequestId.current = null;
      applyRevision.current = null;
      applyOperationKey.current = null;

      // The write landed despite the error. Reporting it as a failure would
      // tell the operator their change was lost while it is on disk.
      setPhase(view.applied ? 'applied' : 'failed');
      if (view.applied) {
        toast({
          variant: 'warning',
          title: 'Applied with an error',
          message: `${spec.title} was applied, but the daemon reported ${view.code}.`,
        });
        onApplied?.(null);
      }
    }
  };

  const recheck = async () => {
    await refresh();
    await dryRun();
  };

  const busy = phase === 'checking' || phase === 'applying';
  const checkedOperation = checkedOperationKey === operationKey;
  const ready = phase === 'ready' && checkedOperation;
  const stale = ready && checkedAt !== revision;
  const replayableUnknown = phase === 'unknown' && applyOperationKey.current === operationKey;

  return (
    <Dialog
      open={spec !== null}
      onOpenChange={(open) => !open && onClose()}
      title={spec?.title ?? ''}
      description={spec?.description}
      size="md"
      dismissible={!busy}
      autoResize
      footer={
        <div
          style={{
            display: 'flex',
            gap: 'var(--stratum-space-3)',
            justifyContent: 'flex-end',
            width: '100%',
          }}
        >
          <Button variant="subtle" onClick={onClose} disabled={busy}>
            {phase === 'applied' ? 'Close' : 'Cancel'}
          </Button>

          {/* Offered whenever the dry run's answer can no longer be trusted:
            * it was refused for a reason a re-read might clear, or the store
            * moved on underneath a check that already passed. */}
          {/* Offered whenever the check on screen can no longer be trusted:
            * a refusal, a settled failure, or a store that moved under a check
            * that had passed. Omitting the `failed` case left an operator who
            * had just lost a revision race with no way to re-check. */}
          {(phase === 'refused' || phase === 'failed' || stale) && (
            <Button variant="default" onClick={() => void recheck()} disabled={busy}>
              Check again
            </Button>
          )}

          {/* A fresh apply exists only after a successful, current check. The
            * applying state keeps the same stable control visible while the
            * request is in flight. */}
          {(ready || phase === 'applying') && (
            <Button
              variant={spec?.destructive ? 'danger' : 'primary'}
              onClick={() => void apply()}
              loading={phase === 'applying'}
              /* `stale` disables rather than warns. The banner used to say
               * "applying now would be rejected" while the button sent the
               * CURRENT revision — so it was not rejected at all, and the write
               * landed against a state nobody had previewed. */
              disabled={phase === 'applying' || stale}
            >
              {spec?.confirmLabel ?? 'Apply'}
            </Button>
          )}

          {/* Reuse both request_id and revision. The live daemon can replay a
            * cached outcome. After restart, config writes are guarded by CAS;
            * runtime reload reconciles the same revision idempotently. */}
          {replayableUnknown && (
            <Button variant="default" onClick={() => void apply()}>
              Ask the daemon again
            </Button>
          )}
        </div>
      }
    >
      <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
        {phase === 'checking' && (
          <Row gap="var(--stratum-space-6)" wrap={false}>
            <Spinner size="sm" />
            <span style={{ color: 'var(--stratum-text-muted)', fontSize: 'var(--stratum-text-sm)' }}>
              Checking what this would change…
            </span>
          </Row>
        )}

        {ready && (
          <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
            {/* The daemon has already been asked. That is the whole value of
              * this step: a confirmation dialog that only restates the
              * operator's own click is theatre, while one that has checked can
              * say something they did not know. */}
            <InlineMessage variant="success" role="none" icon={<IconCheck />}>
              Checked against the current configuration. Nothing has changed yet.
            </InlineMessage>

            {spec?.summarise ? (
              <div
                style={{
                  fontSize: 'var(--stratum-text-sm)',
                  lineHeight: 'var(--stratum-leading-normal)',
                }}
              >
                {spec.summarise(preview)}
              </div>
            ) : null}

            {/* Subordinate, not the subject. The panel and the CLI are two ways
              * into the same daemon — neither is a skin over the other — so the
              * request belongs here as evidence for anyone who wants it, not as
              * the thing the operator is being asked to approve. */}
            <Disclosure size="sm" title="Request detail" meta={`revision ${checkedAt}`}>
              <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
                <JsonViewer data={preview} initialDepth={2} maxHeight="12rem" />
              </div>
            </Disclosure>
          </div>
        )}

        {stale && (
          <InlineMessage variant="warning">
            The configuration moved to revision {revision} while this was open, so the check above
            describes a state that no longer exists. Check again before applying.
          </InlineMessage>
        )}

        {(phase === 'refused' || phase === 'failed') && failure && (
          <FailureNotice failure={failure} />
        )}

        {replayableUnknown && (
          <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
            <Banner variant="warning" title="The outcome of this change is unknown">
              <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
                <p style={{ margin: 0 }}>
                  The request was sent and no answer came back. It may have been applied — the
                  panel cannot tell from here, and will not guess.
                </p>
                <p style={{ margin: 0 }}>
                  It was composed against revision <Code>{applyRevision.current}</Code>. The
                  configuration now reads <Code>{revision}</Code>
                  {applyRevision.current !== null && revision > applyRevision.current
                    ? ' — higher, so something was committed. If nothing else is writing, it was this.'
                    : ' — unchanged, so as far as the configuration can tell, nothing was committed.'}
                </p>
                <p style={{ margin: 0 }}>
                  Asking again keeps the original request and revision. The current daemon may
                  replay its recorded outcome; if it restarted, revision checks prevent a write
                  that already landed from running again.
                </p>
              </div>
            </Banner>
            {failure && <FailureNotice failure={failure} variant="inline" />}
          </div>
        )}

        {phase === 'applied' && (
          <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
            {/* `config.commit_visible_and_resynced` lands here: applied, but
              * with a non-nil error the operator should still see. */}
            {failure ? (
              <FailureNotice failure={failure} />
            ) : (
              <InlineMessage variant="success" icon={<IconCheck />}>
                Applied. The configuration is now at revision <Code>{revision}</Code>.
              </InlineMessage>
            )}
            {result ? (
              <Disclosure size="sm" title="What the daemon returned">
                <JsonViewer data={result} initialDepth={2} maxHeight="12rem" />
              </Disclosure>
            ) : null}
          </div>
        )}
      </div>
    </Dialog>
  );
}

/** Small helper so a screen can drive the dialog with one piece of state. */
export function useMutationDialog() {
  const [spec, setSpec] = useState<MutationSpec | null>(null);
  return {
    spec,
    open: setSpec,
    close: useCallback(() => setSpec(null), []),
  };
}

export { CommandFailure };
