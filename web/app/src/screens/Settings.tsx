/* ===========================================================================
 * SETTINGS — THE DAEMON'S OWN LIMITS
 *
 * Seven stored values, six of them writable, five of those safety limits. A
 * limit here does not shape work; it REFUSES work that crosses it, which is
 * why the permitted range is a column an operator can scan rather than a
 * sentence they have to read.
 *
 * THE FLAG FACT (load-bearing — do not simplify the argv)
 * ------------------------------------------------------
 * `settings set` takes NAMED FLAGS, and Go's `flag` package stops parsing at
 * the first non-flag token. That single fact decides this whole screen, and
 * getting it wrong is not a cosmetic error:
 *
 *   xtierctl local settings set log_level info <- parses cleanly, visits
 *                                                 NOTHING, writes NOTHING,
 *                                                 commits anyway, advances the
 *                                                 revision, exits 0
 *
 * A panel composing that form reports "Applied, now at revision N" — and the
 * revision bump corroborates the lie. The value never changed. This screen was
 * written exactly that way first; it only isn't still, because the mock daemon
 * was rebuilt to parse flags the way Go does and promptly reproduced the no-op.
 *
 * So every control here emits `--<flag> <value>`, using the flag names
 * `localSettings` actually declares (cli.go:466-500). The flags are plumbing:
 * the operator is shown the stored KEY, which is what appears in the config
 * file and in the daemon's own refusal messages.
 *
 * WHAT IS DELIBERATELY NOT HERE
 * -----------------------------
 * `control_addr` and the two strict-outbound flags. None is a setting —
 * `settings.Config` has no such field. The control address is the daemon's
 * bind address, and the strict flags are runtime readings on `DaemonStatus`,
 * shown read-only on the Daemon screen where they belong. Modelling them here
 * produced "not set" for values that structurally cannot exist. The screen
 * says so out loud rather than leaving the operator hunting.
 *
 * LAYOUT
 * ------
 * The settings card follows the framework's table composition: `padding="none"`
 * on the card, an explicitly padded header, a `padding="none"` body so the
 * table's own cell padding is the only inset. Omitting the header padding is
 * what puts every cell one pixel from the card border.
 * ======================================================================== */
import { useState } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Code,
  Disclosure,
  EmptyState,
  IconInfo,
  IconLock,
  IconRefresh,
  InlineMessage,
  Kbd,
  NumberInput,
  PageHeader,
  Row,
  Screen,
  ScrollArea,
  Select,
  Table,
  Tag,
  formatBytes,
} from '@stratum/ui';
import type { TableColumn } from '@stratum/ui';
import type { MutationResponse, SettingsResponse, SystemSettings } from '../api/types';
import { getSettings, mutations, type SettingsPatchInput } from '../api/control';
import { useControl } from '../state/store';
import { useDomainRead } from '../state/useDomainRead';
import { Absent } from '../components/Absent';
import { FailureNotice } from '../components/FailureNotice';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';

type Key = keyof SystemSettings;

interface SettingSpec {
  key: Key;
  /** The CLI flag. This is the load-bearing field — see the header. */
  flag: string;
  label: string;
  description: string;
  kind: 'enum' | 'number' | 'readonly';
  options?: { value: string; label: string; description?: string }[];
  min?: number;
  max?: number;
  /** Why changing it matters. A hint, not a standing message component. */
  caution?: string;
  /**
   * Second reading of the same number in units a human holds — bytes as MiB.
   * Never replaces the stored value, only accompanies it: the stored value is
   * what the operator copies.
   */
  humanise?: (value: number) => string;
}

const SETTINGS: SettingSpec[] = [
  {
    key: 'log_level',
    flag: 'log-level',
    label: 'Log level',
    description: 'Verbosity of the daemon’s own log.',
    kind: 'enum',
    options: [
      { value: 'error', label: 'error', description: 'Failures only.' },
      { value: 'warn', label: 'warn', description: 'Failures and warnings.' },
      { value: 'info', label: 'info', description: 'Normal operational detail.' },
      { value: 'debug', label: 'debug', description: 'Verbose. Per-request detail.' },
    ],
  },
  {
    key: 'max_nested_depth',
    flag: 'max-nested-depth',
    label: 'Maximum nesting depth',
    description: 'How many hops a resolved path may contain.',
    kind: 'number',
    min: 1,
    // settings.HardMaxNestedDepth
    max: 10,
    caution: 'A longer path is refused at compile time, not at run time.',
  },
  {
    key: 'max_response_nodes',
    flag: 'max-response-nodes',
    label: 'Response node ceiling',
    description: 'Largest number of nodes a single response may carry.',
    kind: 'number',
    min: 1,
    // settings.HardMaxResponseNodes
    max: 65536,
  },
  {
    key: 'max_response_bytes',
    flag: 'max-response-bytes',
    label: 'Response size ceiling',
    description: 'Largest response the daemon will assemble, in bytes.',
    kind: 'number',
    min: 1,
    // settings.HardMaxResponseBytes — 16 MiB.
    max: 16 * 1024 * 1024,
    humanise: (v) => formatBytes(v),
  },
  {
    key: 'max_cache_entries',
    flag: 'max-cache-entries',
    label: 'Cache entries',
    description: 'How many entries the resolver cache retains.',
    kind: 'number',
    // `Validate` rejects anything below 1. Offering 0 invited a refusal.
    min: 1,
    // settings.HardMaxCacheEntries
    max: 100000,
  },
  {
    key: 'max_fetch_fan_out',
    flag: 'max-fetch-fan-out',
    label: 'Fetch fan-out',
    description: 'Concurrent upstream fetches permitted per resolution.',
    kind: 'number',
    min: 1,
    // settings.HardMaxFetchFanOut
    max: 64,
    caution: 'A ceiling, not a target — raising it trades upstream load for latency.',
  },
  {
    key: 'data_dir',
    flag: '',
    label: 'Data directory',
    description: 'Where the daemon keeps its state.',
    // `localSettings` declares no flag for it, so it cannot be written here.
    kind: 'readonly',
    caution: 'Edited in the config file and picked up on restart.',
  },
];

/**
 * The stored reading for one key, or `null` when the daemon did not report it.
 *
 * Absence is folded to a single `null` on purpose: an unreported key and a key
 * carrying a JSON null are equally unknown, and neither is a zero.
 */
function reading(settings: SystemSettings, key: Key): string | number | null {
  const value = settings[key];
  return value === undefined ? null : value;
}

const SUBTLE = { fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-subtle)' } as const;

export function Settings() {
  const { revision, revisionRead, epoch, refresh } = useControl();
  const read = useDomainRead<SettingsResponse>('settings', getSettings, [revision, epoch]);
  const mutation = useMutationDialog();

  /*
   * The presence of the RESPONSE decides read-vs-unread; the field inside it
   * decides which keys were reported. Rendering controls against `undefined`
   * would show every value as "not set" — an assertion made from data nobody
   * read — so `settings === null` collapses the whole table into one honest
   * empty state instead.
   */
  const settings = read.data ? (read.data.settings ?? null) : null;

  /** Local edits to the numeric fields, committed by an explicit action. */
  const [drafts, setDrafts] = useState<Partial<Record<Key, number>>>({});

  const commit = (spec: SettingSpec, value: string) =>
    mutation.open({
      operation: mutations.settingsUpdate({
        [spec.key]: spec.key === 'log_level' ? value : Number(value),
      } as SettingsPatchInput),
      title: `Set ${spec.label}`,
      description: (
        <>
          Writes <Code>{spec.key}</Code> and nothing else. Every other setting keeps its current
          value.
        </>
      ),
      confirmLabel: 'Set',
      summarise: (payload) => {
        const result = (payload as MutationResponse<{ settings: SystemSettings }> | null)?.result;
        const applied = result?.settings?.[spec.key];
        const matches = String(applied) === value;
        return (
          <>
            <Code>{spec.key}</Code> becomes <Code>{String(applied ?? value)}</Code>.
            {/* The dry run reports what the daemon WOULD store, which is not
              * always what was asked for. Saying so beats assuming. */}
            {!matches && (
              <> The daemon reports a value other than the one requested — it may have clamped it.</>
            )}
          </>
        );
      },
    });

  /* Keys this panel models that the daemon did not report back. Named once,
   * under the table, rather than repeated as a message beside every row. */
  const unreported = settings ? SETTINGS.filter((s) => reading(settings, s.key) === null) : [];

  const columns: TableColumn<SettingSpec>[] = [
    {
      key: 'setting',
      header: 'Setting',
      /* Deliberately unsized: this is the column that takes the card's spare
       * width. It carries the label, the stored key and a full sentence of
       * description — the only unbounded content in the row — and at a fixed
       * 300px every description broke to three lines while ~450px of card sat
       * empty past the last column. The width was allocated exactly backwards.
       * The three columns after it hold a reading, a range and a control, all
       * with knowable maxima, so the prose is what should flex. */
      cell: (spec) => (
        <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
          <Row gap="var(--stratum-space-4)" align="baseline">
            <span style={{ fontWeight: 500 }}>{spec.label}</span>
            {/* The stored key, not the flag. This is the string that appears in
              * the config file and in the daemon's refusal messages. */}
            <Code variant="subtle">{spec.key}</Code>
          </Row>
          <span style={SUBTLE}>
            {spec.description}
            {spec.caution ? ` ${spec.caution}` : ''}
          </span>
        </div>
      ),
    },
    {
      key: 'current',
      header: 'In effect',
      width: 150,
      cell: (spec) => {
        if (!settings) return <Absent />;
        const current = reading(settings, spec.key);
        if (current === null) return <Absent>not reported</Absent>;
        return (
          <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
            <Code variant="plain">{String(current)}</Code>
            {spec.humanise && typeof current === 'number' ? (
              <span style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}>
                {spec.humanise(current)}
              </span>
            ) : null}
          </div>
        );
      },
    },
    {
      key: 'permitted',
      header: 'Permitted',
      /* Wide enough to hold the four log levels as separate caps on one line;
       * the numeric rows use a fraction of it. */
      width: 200,
      /* The bounds are the point of a limits screen: they are what tells an
       * operator whether the number in front of them has room to move. They
       * mirror the Go hard limits exactly — see the constants beside each spec. */
      cell: (spec) => {
        if (spec.kind === 'enum' && spec.options) {
          /* Discrete elements, not a `·`-joined run of text. A decorative
           * separator competes with the markers this panel spends elsewhere on
           * meaning, and glued into one string it reads as part of the value —
           * exactly wrong for the column an operator copies a value out of.
           *
           * `<Kbd>` rather than `<Tag>` or `<Badge>`: these are not statuses
           * and not categories, they are the literal tokens accepted for this
           * key, which is what `<kbd>` means. It also stays clear of `<Code>`,
           * already spoken for two columns over, where it marks the value that
           * IS stored — permitted and in-effect must not look alike. `keys`
           * rather than children so a value is never split on the separator. */
          return (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 'var(--stratum-space-2)' }}>
              {spec.options.map((o) => (
                <Kbd key={o.value} size="sm" keys={[o.value]} />
              ))}
            </div>
          );
        }
        if (spec.kind === 'number' && spec.min !== undefined && spec.max !== undefined) {
          return (
            <div style={{ display: 'grid', gap: '1px', minWidth: 0 }}>
              <span style={{ fontSize: 'var(--stratum-text-xs)' }}>
                {spec.min.toLocaleString()} – {spec.max.toLocaleString()}
              </span>
              {spec.humanise ? (
                <span style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}>
                  up to {spec.humanise(spec.max)}
                </span>
              ) : null}
            </div>
          );
        }
        return <Absent />;
      },
    },
    {
      key: 'change',
      header: 'Change to',
      width: 230,
      cell: (spec) => {
        if (!settings) return <Absent />;
        const current = reading(settings, spec.key);
        const present = current !== null;

        if (spec.kind === 'readonly') {
          return (
            <Tag variant="neutral" size="sm" outline icon={<IconLock />}>
              not writable here
            </Tag>
          );
        }

        if (spec.kind === 'enum' && spec.options) {
          return (
            <Select
              size="sm"
              options={spec.options}
              value={present ? String(current) : null}
              onChange={(v) => v && v !== String(current) && commit(spec, v)}
              placeholder="not reported"
              aria-label={spec.label}
              disabled={!present}
            />
          );
        }

        const draft = drafts[spec.key];
        const dirty = draft !== undefined && draft !== Number(current);
        const inBounds =
          draft === undefined ||
          ((spec.min === undefined || draft >= spec.min) &&
            (spec.max === undefined || draft <= spec.max));

        return (
          <Row gap="var(--stratum-space-4)" wrap={false}>
            <NumberInput
              size="sm"
              value={draft ?? (present ? Number(current) : null)}
              onValueChange={(v) => setDrafts((d) => ({ ...d, [spec.key]: v ?? undefined }))}
              min={spec.min}
              max={spec.max}
              disabled={!present}
              aria-label={spec.label}
              style={{ inlineSize: '8.5rem' }}
            />
            <Button
              size="sm"
              variant="default"
              disabled={!dirty || !inBounds}
              onClick={() => commit(spec, String(draft))}
            >
              Set
            </Button>
          </Row>
        );
      },
    },
  ];

  return (
    <Screen
      header={
        <PageHeader
          title="Settings"
          description="Thresholds the daemon refuses work past, and where it keeps its state."
          meta={
            <>
              {/* Gated, or this reads "revision 0" next to the "not read"
                * badge below it — two badges stating opposite things about the
                * same fetch. 0 is a real revision on a node nobody has written
                * yet, so the number cannot carry the distinction itself. */}
              <Badge variant={revisionRead ? 'neutral' : 'unknown'} size="sm">
                {revisionRead ? `revision ${revision}` : 'revision not read'}
              </Badge>
              {!settings && (
                <Badge variant="unknown" size="sm">
                  not read
                </Badge>
              )}
            </>
          }
        />
      }
    >
      {/* The no-Save rationale, stated once and never again. */}
      <Banner
        variant="neutral"
        size="sm"
        title="Each setting is written on its own"
        dismissible
        storageKey="xtier.settings.no-save"
      >
        There is no Save button because the daemon stores one key per write. A single Save spanning
        four keys would be four writes with four chances to fail halfway, and nothing to undo the
        ones that already landed. Each change is previewed, then committed, and advances the
        revision by itself.
      </Banner>

      {read.failure && (
        <FailureNotice
          failure={read.failure}
          actions={
            !read.failure.blocked ? (
              <Button
                size="sm"
                variant="default"
                icon={<IconRefresh />}
                onClick={() => void read.reload()}
              >
                Try again
              </Button>
            ) : undefined
          }
        />
      )}

      <Card variant="outlined" padding="none">
        {/* `padding="sm"` is load-bearing: with the card at `padding="none"` the
          * header inherits none, and its title lands flush against the border
          * while every table cell does the same. */}
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="Stored settings"
          description="Five refusal thresholds, one log preference, one path the daemon reads at startup."
        />

        <CardBody padding="none">
          <ScrollArea orientation="both" maxHeight="34rem" label="Stored settings">
            <Table
              data={settings ? SETTINGS : []}
              columns={columns}
              rowKey={(spec) => spec.key}
              /* `auto`, not `fixed`, and the reason is specific: under `fixed`
               * the Table DROPS the trailing column's width so that column
               * absorbs the container's spare width (Table.tsx, the
               * `absorbsSlack` branch). That is right when the last column is
               * prose and wrong here, where it is "Change to" — `width: 230`
               * was a no-op, the column inherited ~760px, and its controls sat
               * against ~450px of empty card while the descriptions three rows
               * to the left wrapped for want of the same space.
               *
               * No column here is pinned, which is the one thing `fixed` is
               * genuinely required for, and the sticky header stays aligned
               * either way — head and body are one <table> sharing one
               * <colgroup>, so a single set of column widths serves both in
               * both layout modes. SETTINGS is a constant seven rows, so there
               * is no re-measure for auto layout to twitch on. Every column is
               * sized except the setting itself, which leaves exactly one place
               * for the slack to land.
               *
               * The unmodelled-keys table below stays `fixed`: its last column
               * IS the value, so absorbing the slack there is correct. */
              layout="auto"
              /* `default`, not `compact`. Compact is 2px block padding — right
               * for a 10k-row log, wrong for rows carrying a label over a
               * description and an input beside it. */
              density="default"
              stickyHeader
              zebra
              loading={read.pristine && read.loading}
              caption="Each row is one stored value, written independently of the rest"
              emptyState={
                <EmptyState
                  title="Settings not read"
                  headingLevel={3}
                  description="The daemon did not return its settings, so none can be shown or changed. This is not a set of empty values."
                />
              }
            />
          </ScrollArea>
        </CardBody>

        {unreported.length > 0 && (
          <CardFooter padding="sm" align="start">
            {/* Absence is not a reading. Said once, with the keys named, rather
              * than as a warning stripe under every affected row. */}
            <InlineMessage variant="warning" size="xs">
              The daemon did not report{' '}
              {unreported.map((s, i) => (
                <span key={s.key}>
                  {i > 0 ? ', ' : ''}
                  <Code variant="subtle">{s.key}</Code>
                </span>
              ))}
              . Those values are unknown — not zero, not off — so their controls are inert.
            </InlineMessage>
          </CardFooter>
        )}
      </Card>

      {settings && <UnmodelledSettings settings={settings} known={SETTINGS.map((s) => s.key)} />}

      {/* Progressive disclosure, because the answer is only wanted by whoever
        * came looking for a knob that is not here. */}
      <Disclosure
        title="Why the control address and strict outbound are not on this page"
        headingLevel={2}
        variant="contained"
        size="sm"
        icon={<IconInfo />}
      >
        <div style={{ display: 'grid', gap: 'var(--stratum-space-6)', maxWidth: '60ch' }}>
          <p style={{ margin: 0 }}>
            Neither is a setting. The daemon’s stored configuration has no field for either one, so
            modelling them here produced “not set” for values that structurally cannot exist.
          </p>
          <p style={{ margin: 0 }}>
            The control address is the address the daemon binds, fixed for the life of the process.
            Strict outbound is a runtime reading of what the transport is currently enforcing — both
            are reported, read-only, on the Daemon screen.
          </p>
        </div>
      </Disclosure>

      <MutationDialog
        spec={mutation.spec}
        onClose={mutation.close}
        onApplied={() => {
          setDrafts({});
          void refresh();
        }}
      />
    </Screen>
  );
}

/** Keys the daemon returned that this panel has no editor for. Listed rather
 *  than hidden: a setting the operator cannot see is one they cannot audit. */
function UnmodelledSettings({ settings, known }: { settings: SystemSettings; known: Key[] }) {
  const extra = Object.entries(settings).filter(([k]) => !known.includes(k as Key));
  if (extra.length === 0) return null;

  const columns: TableColumn<[string, unknown]>[] = [
    {
      key: 'name',
      header: 'Key',
      width: 260,
      cell: ([k]) => <Code variant="subtle">{k}</Code>,
    },
    {
      key: 'value',
      header: 'Reported value',
      /* `String(v)` on an object prints `[object Object]`, destroying exactly
       * the value an operator came here to audit. */
      cell: ([, v]) => (
        <Code variant="plain">
          {typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v)}
        </Code>
      ),
    },
  ];

  return (
    <Card variant="outlined" padding="none">
      <CardHeader
        padding="sm"
        headingLevel={2}
        title="Reported but not modelled here"
        description="Returned by the daemon with no editor in this panel. Shown so nothing is silently dropped."
      />
      <CardBody padding="none">
        <ScrollArea orientation="both" maxHeight="18rem" label="Settings without an editor">
          <Table
            data={extra}
            columns={columns}
            rowKey={([k]) => k}
            layout="fixed"
            density="default"
            stickyHeader
            zebra
          />
        </ScrollArea>
      </CardBody>
    </Card>
  );
}
