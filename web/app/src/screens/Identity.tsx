/* ===========================================================================
 * IDENTITY
 *
 * Whether a seed file on disk actually backs the node ID this configuration
 * claims. Six states, and the interesting thing about them is that three are
 * DEAD ENDS: nothing this panel can do moves a node out of `legacy_unbacked`,
 * `backing_missing` or `mismatch`. Recovery is filesystem work performed on the
 * host, outside this panel entirely.
 *
 * That shapes the whole screen. The temptation in a panel is to offer a button
 * for every bad state, because a bad state with no button feels unfinished. But
 * a "Repair" control that cannot repair anything is worse than none: the
 * operator presses it, something fails obscurely, and they lose the one piece
 * of information the screen actually had — that the fix is not in here.
 *
 * So each dead end gets an explicit statement of what went wrong, what it means
 * for the node, and where the fix actually lives.
 *
 * WHERE THE DATA COMES FROM
 * -------------------------
 * The shared config-plane snapshot, not a dedicated identity read. That plane
 * is readable from disk with the daemon stopped, which is why a failure here
 * means the configuration file itself is the problem rather than the daemon.
 *
 * WHY `mismatch` SHOWS TWO IDENTITIES
 * -----------------------------------
 * It is the only state where the configuration and the seed file disagree, and
 * the disagreement IS the diagnosis. Showing one of them — or worse, merging
 * them — throws away the entire content of the error. So both are rendered in
 * full, side by side, untruncated, with the point of divergence computed and
 * named: two 40-character identifiers are not comparable by eye without help.
 * ======================================================================== */
import { useRef, useState } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardHeader,
  Columns,
  Disclosure,
  Field,
  Hint,
  IconCheck,
  IconEdit,
  IconLock,
  IconPlay,
  IconPlus,
  IconSearch,
  IconSettings,
  IconShield,
  InlineMessage,
  Input,
  PageHeader,
  Row,
  ScrollArea,
  Screen,
  SectionLabel,
  Separator,
  Snippet,
  StateMatrix,
  StatusDot,
  Table,
  Tag,
} from '@stratum/ui';
import type { BadgeVariant, TableColumn } from '@stratum/ui';
import type { IdentityState, IdentityView } from '../api/types';
import { mutations } from '../api/control';
import { useControl } from '../state/store';
import { Absent } from '../components/Absent';
import { FailureNotice } from '../components/FailureNotice';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';
import { NodeId } from '../components/NodeId';

interface StateInfo {
  label: string;
  summary: string;
  /** What it means for the node right now. */
  consequence: string;
  /** Null where no in-panel action exists. Stated, not hidden. */
  remedy: string | null;
  /**
   * What resolves the state, named after the WORK rather than after a command.
   * An operator needs to know whether the fix is here or on the host; which
   * argv carries it out is not the question this column answers.
   */
  resolution: string;
  severity: 'ok' | 'warning' | 'danger';
}

const STATES: Record<IdentityState, StateInfo> = {
  backed: {
    label: 'Backed',
    summary: 'The node ID in the configuration is derived from a seed file on disk.',
    consequence: 'This node can prove its identity across restarts.',
    remedy: null,
    resolution: '',
    severity: 'ok',
  },
  uninitialized: {
    label: 'Uninitialized',
    summary: 'No identity has been generated yet.',
    consequence:
      'The node has no ID to present. Nothing that depends on identity will work until one exists.',
    remedy:
      'Generate an identity. A seed is created on the host and the node ID is derived from it. '
      + 'This is the one bad state the panel can resolve on its own.',
    resolution: 'generating an identity',
    severity: 'warning',
  },
  recoverable: {
    label: 'Recoverable',
    summary:
      'A seed file exists, but the configuration records no identity — a previous setup wrote the '
      + 'seed and did not finish.',
    consequence:
      'The node has no ID to present yet. Nothing has been lost: the seed is intact and still '
      + 'determines what the identity will be.',
    remedy:
      'Finish what the interrupted run started. The existing seed is reused rather than replaced, '
      + 'so the identity this node ends up with is the one that seed already determines. No new '
      + 'key material is created.',
    resolution: 'completing the identity',
    severity: 'warning',
  },
  legacy_unbacked: {
    label: 'Legacy, unbacked',
    summary:
      'The configuration carries a v1 node ID that predates seed backing. There is no seed behind it.',
    consequence:
      'The node keeps working with the ID it has, but cannot prove ownership of it and cannot rotate it.',
    remedy: null,
    resolution: 'host-side migration',
    severity: 'danger',
  },
  backing_missing: {
    label: 'Backing missing',
    summary: 'The configuration names a node ID, but the seed file that should back it is gone.',
    consequence: 'The ID cannot be re-derived. If it was ever provable, it is not any more.',
    remedy: null,
    resolution: 'restoring the seed on the host',
    severity: 'danger',
  },
  mismatch: {
    label: 'Mismatch',
    summary: 'The seed file on disk backs a DIFFERENT node ID from the one in the configuration.',
    consequence:
      'The node is presenting an identity it cannot derive. Which of the two is correct is not '
      + 'something the panel can determine — only you know which file was restored from where.',
    remedy: null,
    resolution: 'choosing the right file on the host',
    severity: 'danger',
  },
};

/* The six states, healthy first. NOT a preference ladder and NOT a sequence:
 * these are mutually exclusive CONDITIONS and a node is simply in one of them.
 *
 * An earlier version rendered this with `DegradationLadder`, which was wrong
 * twice over. It painted every dead end as a mild amber degradation — while
 * the same screen was calling those states danger — and it labelled the five
 * states the node is NOT in as "not attempted", as though something had tried
 * them and moved on. Nothing attempts an identity state. */
const ORDER: IdentityState[] = [
  'backed',
  'recoverable',
  'uninitialized',
  'legacy_unbacked',
  'backing_missing',
  'mismatch',
];

/** States in which a seed file actually exists on disk. */
const SEED_BEARING = new Set<IdentityState>(['backed', 'recoverable', 'mismatch']);

const BADGE: Record<StateInfo['severity'], BadgeVariant> = {
  ok: 'success',
  warning: 'warning',
  danger: 'danger',
};

type StateRow = StateInfo & { id: IdentityState };

/**
 * Index of the first character at which two identifiers differ.
 *
 * `-1` means they are character-for-character identical. Two ids of different
 * lengths that agree as far as the shorter one goes diverge at that length.
 */
function firstDifference(a: string, b: string): number {
  const shared = Math.min(a.length, b.length);
  for (let i = 0; i < shared; i += 1) {
    if (a[i] !== b[i]) return i;
  }
  return a.length === b.length ? -1 : shared;
}

/**
 * One half of the mismatch comparison.
 *
 * Both halves are laid out identically so the eye can run down them in
 * parallel; the only thing that should differ between the two columns is the
 * value itself.
 */
function IdentitySide({
  icon,
  heading,
  claim,
  nodeId,
  publicKey,
  keyHeading,
}: {
  icon: React.ReactNode;
  heading: string;
  claim: string;
  nodeId: string | undefined;
  publicKey: string | undefined;
  keyHeading: string;
}) {
  return (
    <div style={{ display: 'grid', gap: 'var(--stratum-space-6)', minWidth: 0 }}>
      <div style={{ display: 'grid', gap: 'var(--stratum-space-2)' }}>
        <span
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 'var(--stratum-space-4)',
            fontWeight: 600,
          }}
        >
          {icon}
          {heading}
        </span>
        <Hint>{claim}</Hint>
      </div>

      {/* Full, not truncated. When two IDs must be compared by eye, hiding the
        * middle removes the only part that differs. */}
      {nodeId ? (
        <Snippet
          value={nodeId}
          size="sm"
          wrap
          /* Named individually. Without this both regions announce as "Code
           * block", on the one screen whose whole purpose is telling the two of
           * them apart. */
          scrollLabel={`${heading} — node ID`}
          copyLabel={`Copy the node ID ${heading.toLowerCase()}`}
        />
      ) : (
        <Absent>not reported</Absent>
      )}

      <Disclosure title={keyHeading} size="sm" variant="plain" headingLevel={3}>
        {publicKey ? (
          <Snippet
            value={publicKey}
            size="sm"
            wrap
            scrollLabel={keyHeading}
            copyLabel={`Copy the ${keyHeading.toLowerCase()}`}
          />
        ) : (
          <Absent>not reported</Absent>
        )}
      </Disclosure>
    </div>
  );
}

export function Identity() {
  const { local, localError, refresh } = useControl();
  const mutation = useMutationDialog();

  const [name, setName] = useState(local?.display_name ?? '');
  // Re-seeds when a different config is read — but only on identity change, so
  // a re-read cannot wipe a name the operator is halfway through typing.
  const lastSeen = useRef(local?.display_name);
  if (lastSeen.current !== local?.display_name) {
    lastSeen.current = local?.display_name;
    if (!name) setName(local?.display_name ?? '');
  }

  const identity: IdentityView | undefined = local?.identity;
  const state = identity?.state;
  const info = state ? STATES[state] : null;
  const isDeadEnd = Boolean(info && info.remedy === null && state !== 'backed');

  /* Where the two identifiers part company, computed rather than asserted. A
   * `null` here means the comparison could not be made at all — one side was
   * not reported — which is a different statement from "they match". */
  const divergesAt =
    identity?.node_id && identity.backing_node_id
      ? firstDifference(identity.node_id, identity.backing_node_id)
      : null;

  const columns: TableColumn<StateRow>[] = [
    {
      key: 'state',
      header: 'State',
      width: 180,
      cell: (r) => (
        <span
          style={{
            display: 'inline-flex',
            gap: 'var(--stratum-space-4)',
            alignItems: 'center',
            fontWeight: r.id === state ? 600 : 400,
          }}
        >
          <StatusDot
            status={r.severity === 'ok' ? 'ok' : r.severity === 'warning' ? 'degraded' : 'failed'}
            size="sm"
          />
          {r.label}
        </span>
      ),
    },
    {
      key: 'current',
      header: 'Current',
      width: 110,
      cell: (r) =>
        r.id === state ? (
          <Badge variant="accent" size="sm">
            this node
          </Badge>
        ) : (
          <Absent />
        ),
    },
    {
      key: 'resolution',
      header: 'Resolved by',
      width: 220,
      cell: (r) =>
        r.id === 'backed' ? (
          <Absent>nothing to resolve</Absent>
        ) : r.remedy === null ? (
          /* The column that matters. A dead end says so in the row itself, so
           * an operator scanning the table never has to discover it from the
           * absence of a button somewhere else on the page. */
          <Tag size="sm" variant="danger" outline icon={<IconLock />}>
            {r.resolution}
          </Tag>
        ) : (
          <Tag size="sm" variant="warning" icon={<IconPlay />}>
            {r.resolution}
          </Tag>
        ),
    },
    {
      key: 'meaning',
      header: 'What it means',
      /* Last, and deliberately unsized: under `layout="fixed"` the trailing
       * column absorbs the container's spare width, so this is the one that
       * should flex. */
      cell: (r) => <span style={{ fontSize: 'var(--stratum-text-sm)' }}>{r.summary}</span>,
    },
  ];

  return (
    <Screen
      header={
        <PageHeader
          title="Identity"
          description="Whether a seed file on disk actually backs the node ID this configuration claims."
          meta={
            info ? (
              <Badge variant={BADGE[info.severity]}>{info.label}</Badge>
            ) : (
              <Badge variant="unknown">not read</Badge>
            )
          }
        />
      }
    >
      {localError && (
        <FailureNotice
          failure={{
            title: 'Configuration could not be read',
            guidance:
              'Identity comes from the config plane, which is readable from disk with the daemon '
              + 'stopped. If this failed, the file itself is the problem.',
            severity: 'danger',
            blocked: false,
            code: localError,
            detail: '',
            applied: false,
          }}
        />
      )}

      {/* A dead end gets a banner, not a button. Stating that the fix lives
        * outside the panel is the most useful thing this screen can do — and
        * the banner carries only that conclusion, because the state's summary
        * and consequence already have one home each below. */}
      {isDeadEnd && info && (
        <Banner
          variant="danger"
          icon={<IconLock />}
          title={`${info.label} — nothing in this panel resolves it`}
        >
          Recovery is filesystem work on the host: {info.resolution}. There is no control here that
          would do it, so the panel offers none rather than one that fails obscurely.
        </Banner>
      )}

      {/* The disagreement IS the diagnosis, so it goes first and takes the full
        * width. Nothing below it matters until it is settled. */}
      {state === 'mismatch' && identity && (
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title="Two identities, and they disagree"
            description="Both are shown because neither can be assumed correct."
          />
          <CardBody>
            <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
              <Columns min="22rem">
                <IdentitySide
                  icon={<IconSettings />}
                  heading="Recorded in the configuration"
                  claim="What this node presents to others."
                  nodeId={identity.node_id}
                  publicKey={identity.public_key}
                  keyHeading="Configured public key"
                />
                <IdentitySide
                  icon={<IconShield />}
                  heading="Derived from the seed file"
                  claim="What this node could actually prove."
                  nodeId={identity.backing_node_id}
                  publicKey={identity.backing_public_key}
                  keyHeading="Seed-derived public key"
                />
              </Columns>

              {/* Computed from the two strings, not narrated. Identifiers of
                * this length share a long head and differ somewhere in the
                * middle, which is precisely where an eye stops looking. */}
              <InlineMessage variant="info" icon={<IconSearch />} role="none">
                {divergesAt === null
                  ? 'Only one of the two identifiers was reported, so they cannot be compared here.'
                  : divergesAt === -1
                    ? 'The two identifiers read identically, so the disagreement is in the key material behind them rather than in the printed ID.'
                    : divergesAt === 0
                      ? 'The two identifiers differ from their very first character.'
                      : `The two identifiers share their first ${divergesAt} characters and then diverge. Compare from character ${divergesAt + 1}.`}
              </InlineMessage>
            </div>
          </CardBody>
        </Card>
      )}

      <Columns min="26rem">
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title={info ? info.label : 'Not read'}
            description={
              info
                ? info.summary
                : 'The configuration has not been read, so the identity state is unknown. That is not a claim that no identity exists.'
            }
          />
          <CardBody>
            <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
              {info && (
                <>
                  <p style={{ margin: 0 }}>{info.consequence}</p>
                  <Separator />
                </>
              )}
              <StateMatrix
                label="Identity facts"
                layout="stack"
                dimensions={[
                  {
                    key: 'node_id',
                    label: 'Node ID',
                    /* The local node ID is canonically derived and validated, so
                     * it earns the `Identifier` affordance a peer's does not. */
                    value: identity?.node_id ? <NodeId value={identity.node_id} /> : null,
                    note: 'Derived from the seed, and validated against the identity package.',
                  },
                  {
                    key: 'algorithm',
                    label: 'Algorithm',
                    value: identity?.algorithm ?? null,
                    note: 'Absent before an identity exists.',
                  },
                  {
                    key: 'version',
                    label: 'Format version',
                    value: identity?.version != null ? `v${identity.version}` : null,
                  },
                  {
                    key: 'acl',
                    label: 'Seed file permissions',
                    /* Only meaningful where a seed exists. In `uninitialized`
                     * there is no file, so the Go zero value `false` means
                     * "nothing to qualify" — and rendering that as "not
                     * release-qualified" asserts a permissions problem against a
                     * file that is not there. */
                    value:
                      !identity || !SEED_BEARING.has(identity.state)
                        ? null
                        : identity.os_acl_release_qualified
                          ? 'release-qualified'
                          : 'not release-qualified',
                    status:
                      identity && SEED_BEARING.has(identity.state)
                        ? identity.os_acl_release_qualified
                          ? 'ok'
                          : 'degraded'
                        : undefined,
                    note:
                      identity && !SEED_BEARING.has(identity.state)
                        ? 'There is no seed file in this state, so there are no permissions to report.'
                        : 'Whether the seed file’s permissions meet the release bar. Loose '
                          + 'permissions do not invalidate the identity — they widen who can steal it.',
                  },
                ]}
              />

              {/* Not a matrix row. The matrix renders inert text, and this is a
                * 52-character value whose whole use is being copied somewhere
                * else — so it gets the same `Snippet` treatment the mismatch
                * card gives the two keys it compares.
                *
                * Deliberately UNMASKED. It is the verifying half: masking it
                * would teach the operator that a value built to be handed out
                * is one to keep hidden, and the half that must never leave the
                * host — the seed — is not on this screen at all.
                *
                * Rendered in every state, including those with no key, because
                * "the daemon reported none" is a fact about the identity and
                * omitting the block would leave it unsaid. */}
              <Separator />
              <div style={{ display: 'grid', gap: 'var(--stratum-space-3)', minWidth: 0 }}>
                <SectionLabel>Public key</SectionLabel>
                <Hint>
                  What others check this node’s signatures against. Safe to hand out — it is the
                  seed behind it that is secret, and that never leaves the host.
                </Hint>
                {identity?.public_key ? (
                  <Snippet
                    value={identity.public_key}
                    size="sm"
                    wrap
                    scrollLabel="Public key"
                    copyLabel="Copy the public key"
                  />
                ) : (
                  <Absent>not reported</Absent>
                )}
              </div>
            </div>
          </CardBody>
        </Card>

        {info?.remedy && (
          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title={
                state === 'recoverable' ? 'Finish the interrupted setup' : 'Give this node an identity'
              }
            />
            <CardBody>
              <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
                <p style={{ margin: 0 }}>{info.remedy}</p>

                {(state === 'uninitialized' || state === 'recoverable') && (
                  <>
                    {/* A real mutating write, so it goes through the same
                      * dry-run gate as every other change on the panel.
                      * Offering it only as a copyable snippet withheld the
                      * single remedy this screen actually has. */}
                    <Row>
                      <Button
                        variant="primary"
                        icon={state === 'recoverable' ? <IconCheck /> : <IconPlus />}
                        onClick={() =>
                          mutation.open({
                            operation: mutations.identityInit(name.trim()),
                            title:
                              state === 'recoverable'
                                ? 'Complete the identity'
                                : 'Generate an identity',
                            description:
                              state === 'recoverable'
                                ? 'Reuses the seed already on disk and records the identity it '
                                  + 'determines. No new key material is created.'
                                : 'Creates a seed on the host and derives a node ID from it. The '
                                  + 'node ID it produces is the one this node presents from then '
                                  + 'on, and the panel cannot undo it.',
                            confirmLabel: state === 'recoverable' ? 'Complete' : 'Generate',
                          })
                        }
                      >
                        {state === 'recoverable' ? 'Complete identity' : 'Generate identity'}
                      </Button>
                    </Row>

                    <Hint>
                      A dry run reports what would be created before anything is written to disk.
                      {name.trim() ? ` The display name “${name.trim()}” below is recorded with it.` : ''}
                    </Hint>
                  </>
                )}
              </div>
            </CardBody>
          </Card>
        )}
      </Columns>

      <Card variant="outlined" padding="none">
        {/* `padding="sm"` is load-bearing: with the card at `padding="none"` the
          * header inherits none, and its title lands flush against the border
          * while every table cell does the same. */}
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="The six identity states"
          description="Mutually exclusive conditions — a node is in exactly one. Resolved by is the column that matters."
        />
        <CardBody padding="none">
          <ScrollArea orientation="both" maxHeight="26rem" label="Identity states">
            <Table
              data={ORDER.map((id) => ({ id, ...STATES[id] }))}
              columns={columns}
              rowKey={(r) => r.id}
              layout="fixed"
              /* `default`, not `compact`. Compact is 2px block padding — right
               * for a 10k-row log, wrong for rows whose last column wraps to
               * two or three lines of explanation. */
              density="default"
              stickyHeader
              zebra
              caption="Identity states, and what resolves each one"
            />
          </ScrollArea>
        </CardBody>
      </Card>

      <Card variant="outlined">
        <CardHeader
          headingLevel={2}
          /* Not "Display name": that is the field's label, six lines down, and
            * a card whose title repeats its only control's label has told the
            * operator nothing twice. The title says what the card is FOR. */
          title="What this node is called"
          description="A label for people reading this panel. Nothing resolves by it — the node ID is the identifier."
        />
        <CardBody>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const next = name.trim();
              if (!next || next === local?.display_name) return;
              mutation.open({
                operation: mutations.identityRename(next),
                title: 'Rename this node',
                description:
                  'Changes the label only. The node ID and the seed behind it are untouched.',
              });
            }}
          >
            <Row align="flex-end">
              {/* Capped rather than grown, and the input fills the cap.
                * `flex: 1 1 …` stretched the field BOX to the full card width
                * while the input kept its intrinsic size, which is what left
                * 900px of nothing between a name and the button that commits
                * it. A display name is short; the field has no business being
                * page-wide, and the button belongs beside the control it
                * submits. */}
              <Field label="Display name" style={{ flex: '0 1 20rem' }}>
                <Input
                  value={name}
                  onChange={(e) => setName(e.currentTarget.value)}
                  placeholder={local?.display_name || 'not set'}
                  fullWidth
                />
              </Field>
              <Button
                type="submit"
                size="md"
                variant="default"
                icon={<IconEdit />}
                disabled={!name.trim() || name.trim() === local?.display_name}
              >
                Rename node
              </Button>
            </Row>
          </form>
        </CardBody>
      </Card>

      <MutationDialog spec={mutation.spec} onClose={mutation.close} onApplied={() => void refresh()} />
    </Screen>
  );
}
