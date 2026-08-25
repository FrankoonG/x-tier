/* ===========================================================================
 * DIAGNOSTICS — THE TROUBLESHOOTING RECORD
 *
 * What this panel has asked the daemon, what came back, and — just as
 * important — the questions the control plane cannot answer at all.
 *
 * WHY THE REQUEST LOG IS NOT OPTIONAL
 * -----------------------------------
 * When a change does not take effect, an operator has exactly three suspects —
 * the panel asked for the wrong thing, the daemon refused it, or the write
 * landed and the read is stale — and no way to tell them apart from a screen
 * that only shows results.
 *
 * The record collapses that. Every request is here with the exact arguments it
 * carried, the revision it was composed against, its exit code and the
 * daemon's own error token. It is the difference between "it didn't work" and
 * "the address change went out against revision 7 and came back
 * `config.revision_conflict`".
 *
 * This is the one place in the panel where the wire form belongs on the
 * surface. It is a diagnostic artefact — the record of a conversation with the
 * daemon — and not how any other screen expects to be operated. Nothing here
 * is an instruction to go and use a terminal instead; it is evidence, kept so
 * that a fault can be described precisely to whoever fixes it.
 *
 * `LogViewer` carries it rather than a table because that is what this is: an
 * append-only stream that wants filtering and tail-following, not sorting and
 * pagination.
 * ======================================================================== */
import { useEffect, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  Code,
  Columns,
  Disclosure,
  EmptyState,
  IconAlert,
  IconBook,
  IconDaemon,
  IconSettings,
  IconTrash,
  JsonViewer,
  LogViewer,
  PageHeader,
  Row,
  Screen,
  SegmentedControl,
  StateMatrix,
  Tag,
  Timestamp,
} from '@stratum/ui';
import type { LogLevel, LogLine } from '@stratum/ui';
import type { DaemonStatus, LocalStatus } from '../api/types';
import {
  clearJournal,
  subscribeJournal,
  type CommandShell,
  type JournalEntry,
} from '../api/control';
import { adviseCode } from '../api/errors';
import { useControl } from '../state/store';
import { FailureNotice } from '../components/FailureNotice';
import { Absent } from '../components/Absent';
import { toDiagnosticLine } from './diagnosticLog';

/* The level filter is the log's own vocabulary, so it uses this screen's words
 * rather than the generic severity names. `trace` is never produced and is left
 * out of the options entirely — an empty filter chip is a dead control.
 *
 * Long for a gutter, and left that way deliberately. `levelLabels` is one
 * vocabulary that `LogViewer` spends three ways: the filter chips, the row
 * gutter, and the row's accessible level name. Cutting these to scan tokens for
 * the gutter's sake would cut the chips with them — and a chip is a control the
 * operator reads before choosing what to look at, not a column they skim — and
 * would hand a screen reader the abbreviation too. Shortening the gutter wants
 * a second prop on the component, not a shorter word here. */
const LEVEL_LABELS: Partial<Record<LogLevel, string>> = {
  debug: 'In flight',
  info: 'Succeeded',
  warn: 'Refused',
  error: 'Unreachable',
  fatal: 'Outcome unknown',
};

const LEVEL_OPTIONS: readonly LogLevel[] = ['debug', 'info', 'warn', 'error', 'fatal'];

const adviseFailure = (code: string) => ({ ...adviseCode(code), code, detail: '' });

/**
 * One plane's last payload, in the only three states it can be in.
 *
 * `null` data is NOT an empty payload — it means the plane never answered, and
 * rendering `null` into the viewer would present "we did not look" as "the
 * daemon said nothing".
 */
function Payload({ error, data }: { error: string | null; data: LocalStatus | DaemonStatus | null }) {
  if (error) return <FailureNotice failure={adviseFailure(error)} variant="inline" />;
  if (data === null) {
    return <Absent>no payload has come back from this plane</Absent>;
  }
  return <JsonViewer data={data} initialDepth={2} maxHeight="18rem" />;
}

export function Diagnostics() {
  const { local, daemon, daemonError, localError, revision, revisionRead, fetchedAt } =
    useControl();
  const [entries, setEntries] = useState<readonly JournalEntry[]>([]);
  const [shell, setShell] = useState<CommandShell>('powershell');

  useEffect(() => subscribeJournal(setEntries), []);

  // Oldest first: a log reads forward, and the tail is where the interesting
  // thing just happened. The journal itself is newest-first for cheap capping.
  const target = daemon
    ? { configPath: daemon.config_path, controlAddr: daemon.control_addr }
    : null;
  const lines = [...entries].reverse().map((entry) => toDiagnosticLine(entry, shell, target));

  const failures = entries.filter((e) => e.outcome !== 'ok' && e.outcome !== 'pending');
  // `mutating`, not `revision > 0` — revision zero is a real writable state
  // and counting by the number under-reports the first write on a new node.
  const mutations = entries.filter((e) => e.mutating && !e.dryRun);
  const dryRuns = entries.filter((e) => e.dryRun);

  return (
    <Screen
      header={
        <PageHeader
          title="Diagnostics"
          description="What this panel has asked the daemon, and what the control plane cannot tell you."
          meta={
            <Badge variant="neutral" size="sm">
              {entries.length} {entries.length === 1 ? 'request' : 'requests'} this session
            </Badge>
          }
        />
      }
    >
      <Columns min="24rem">
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title="This session"
            description="Counted since the page was loaded. Nothing here survives a reload."
          />
          <CardBody>
            <StateMatrix
              label="Session request counts"
              layout="stack"
              size="sm"
              dimensions={[
                { key: 'total', label: 'Requests sent', value: String(entries.length) },
                {
                  key: 'dry',
                  label: 'Previews',
                  value: String(dryRuns.length),
                  status: 'info',
                  note: 'Checks that changed nothing. Every change is previewed before it is applied.',
                },
                {
                  key: 'mutations',
                  label: 'Changes attempted',
                  value: String(mutations.length),
                  note: 'Requests that carried a revision and were not previews.',
                },
                {
                  key: 'failures',
                  label: 'Failures',
                  value: String(failures.length),
                  status: failures.length > 0 ? 'degraded' : 'ok',
                },
                {
                  key: 'revision',
                  label: 'Configuration revision',
                  value: revisionRead ? String(revision) : null,
                  note: revisionRead
                    ? 'The compare-and-swap token every change is composed against.'
                    : 'Neither plane has answered, so the revision is unknown — not zero.',
                },
              ]}
            />
          </CardBody>
        </Card>

        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title="What this control plane does not have"
            description="Named explicitly, so an empty chart is never mistaken for a quiet network."
          />
          <CardBody>
            <StateMatrix
              label="Absent capabilities"
              layout="stack"
              size="sm"
              unobservedLabel="no such signal"
              dimensions={[
                {
                  key: 'events',
                  label: 'Event stream',
                  value: null,
                  note: 'No SSE, websocket or long poll. Nothing in this panel can update itself, which is why every screen shows when it last read.',
                },
                {
                  key: 'metrics',
                  label: 'Traffic counters',
                  value: null,
                  note: 'No byte, packet, session or connection counters are exposed anywhere in the API.',
                },
                {
                  key: 'probes',
                  label: 'Reachability probes',
                  value: null,
                  note: 'Peers are never dialled by the control plane. No latency, no last-seen, no up/down.',
                },
                {
                  key: 'logs',
                  label: 'Daemon log access',
                  value: null,
                  note: 'The daemon’s own log is not exposed over the control plane. The record on this page is the PANEL’s, not the daemon’s.',
                },
                {
                  key: 'history',
                  label: 'Configuration history',
                  value: null,
                  note: 'Only the current revision number is readable. There is no audit trail and no diff against a previous revision.',
                },
              ]}
            />
          </CardBody>
        </Card>
      </Columns>

      <Card variant="outlined" padding="none">
        {/* `padding="sm"` is load-bearing: with the card at `padding="none"` the
          * header inherits none, and its title lands flush against the border. */}
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="Request log"
          description="Rows show the actual HTTP method and path. Copy actions expose a secondary, independent CLI equivalent."
          actions={
            <Row>
              <SegmentedControl
                size="sm"
                label="Secondary CLI equivalent shell"
                value={shell}
                onValueChange={(value) => setShell(value as CommandShell)}
                items={[
                  { value: 'powershell', children: 'PowerShell' },
                  { value: 'posix', children: 'POSIX' },
                ]}
              />
              <Tag
                size="sm"
                variant={failures.length > 0 ? 'warning' : 'neutral'}
                icon={failures.length > 0 ? <IconAlert /> : undefined}
              >
                {failures.length} failed
              </Tag>
              <Button
                size="xs"
                variant="ghost"
                icon={<IconTrash />}
                onClick={clearJournal}
                disabled={entries.length === 0}
              >
                Clear
              </Button>
            </Row>
          }
        />
        <CardBody padding="none">
          {lines.length === 0 ? (
            <div style={{ padding: 'var(--stratum-space-12)' }}>
              <EmptyState
                icon={<IconBook />}
                title="Nothing asked yet"
                headingLevel={3}
                description="The record is held in memory only and starts empty on every page load."
              />
            </div>
          ) : (
            /* `LogViewer` owns its own scrolling viewport and toolbar, so it is
              * not wrapped in a `ScrollArea` — two nested scroll regions would
              * fight over the tail-follow behaviour. */
            <LogViewer
              lines={lines}
              height="26rem"
              defaultFollow
              showTimestamp
              timestampFormat="time"
              ansi={false}
              levelOptions={LEVEL_OPTIONS}
              levelLabels={LEVEL_LABELS}
              label="HTTP request log"
              labelCopyLine={`Copy secondary ${shell === 'powershell' ? 'PowerShell' : 'POSIX'} CLI equivalent`}
              labelCopyAll={`Copy all secondary ${shell === 'powershell' ? 'PowerShell' : 'POSIX'} CLI equivalents`}
              labelEmpty="No requests this session"
            />
          )}
        </CardBody>
      </Card>

      <Card variant="outlined">
        <CardHeader
          headingLevel={2}
          title="Last responses, verbatim"
          description="The unmodified payloads every other screen is drawn from."
          actions={
            fetchedAt ? (
              <span
                style={{ fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-muted)' }}
              >
                read{' '}
                <Timestamp
                  value={fetchedAt}
                  display="relative"
                  size="sm"
                  style={{ color: 'var(--stratum-text)' }}
                />
              </span>
            ) : (
              <Badge variant="unknown" size="sm">
                not read
              </Badge>
            )
          }
        />
        <CardBody>
          {/* Collapsed by default. These are hundreds of lines of JSON each, and
            * an operator opens them when a screen disagrees with what they
            * expect — not on arrival. */}
          <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
            <Disclosure
              variant="contained"
              headingLevel={3}
              icon={<IconSettings />}
              title="Configuration plane"
              description="Read from disk. Still authoritative while the daemon is stopped."
              meta={<Code variant="subtle">local status</Code>}
            >
              <Payload error={localError} data={local} />
            </Disclosure>

            <Disclosure
              variant="contained"
              headingLevel={3}
              icon={<IconDaemon />}
              title="Runtime plane"
              description="Observed by the daemon. Absent entirely when it is not running."
              meta={<Code variant="subtle">GET /v1/status</Code>}
            >
              <Payload error={daemonError} data={daemon} />
            </Disclosure>
          </div>
        </CardBody>
      </Card>
    </Screen>
  );
}
