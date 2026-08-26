/* ===========================================================================
 * DAEMON — THE OBSERVED PLANE
 *
 * `GET /v1/status` is the live surface for daemon, Rendr, Xray and reconcile
 * state. An explicit `unavailable` remains distinct from a read that never
 * arrived.
 *
 * THE ZERO-VALUE TRAP
 * -------------------
 * `xrayStatus()` returns a bare `XrayStatus{State: unavailable}` when the
 * daemon holds no xray manager. Every other field on that struct is therefore
 * its Go zero value — `Current: nil`, `Draining: []`, and BOTH strictness flags
 * `false` — none of which is a reading about xray. Only when a manager exists
 * are those fields populated from it, and only then does a `false` mean "not
 * enforced" rather than "nobody asked".
 *
 * So the xray card gates every field behind the runtime's own state, and the
 * fields fall back to not-observed rather than to a negative. `current: nil`
 * WITH a manager present is a different fact again: a reported none, which is a
 * reading and is shown as one.
 * ======================================================================== */
import type { ReactNode } from 'react';
import {
  Badge,
  Banner,
  Button,
  CapabilityGrid,
  Card,
  CardBody,
  CardHeader,
  Code,
  Columns,
  Disclosure,
  EmptyState,
  ErrorState,
  IconAlert,
  IconCheck,
  IconClock,
  IconDaemon,
  IconEye,
  IconMinus,
  IconPlay,
  IconRefresh,
  InlineMessage,
  PageHeader,
  Screen,
  Separator,
  StateMatrix,
  Timestamp,
} from '@stratum/ui';
import type { BadgeVariant } from '@stratum/ui';
import type { DaemonState, RuntimeState } from '../api/types';
import { isConfigContentInvalid, runtimeStatus } from '../api/runtimeStatus';
import { mutations } from '../api/control';
import { useControl } from '../state/store';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';

/**
 * Presentation for a runtime state, keeping the three negatives apart.
 *
 * `stopped` and `failed` are things the daemon SAID; `unavailable` is the
 * daemon declining to report. Painting the last one like the first two is how a
 * panel turns missing wiring into a phantom outage, so it gets the `unknown`
 * treatment and no glyph at all — there is nothing affirmative to draw.
 */
const RUNTIME_BADGE: Record<RuntimeState | DaemonState, { variant: BadgeVariant; icon?: ReactNode }> = {
  running: { variant: 'success', icon: <IconCheck /> },
  degraded: { variant: 'warning', icon: <IconAlert /> },
  starting: { variant: 'info', icon: <IconClock /> },
  stopping: { variant: 'warning', icon: <IconClock /> },
  stopped: { variant: 'neutral', icon: <IconMinus /> },
  failed: { variant: 'danger', icon: <IconAlert /> },
  unavailable: { variant: 'unknown' },
};

/** Daemon and component runtime states share one explicit display map. */
function RuntimeBadge({ state }: { state: RuntimeState | DaemonState }) {
  const { variant, icon } = RUNTIME_BADGE[state];
  return (
    <Badge variant={variant} size="sm" icon={icon}>
      {state}
    </Badge>
  );
}

export function Runtime() {
  const { daemon, daemonError, local, refresh, loading } = useControl();
  const mutation = useMutationDialog();

  const activeConfigInvalid = isConfigContentInvalid(daemon?.reconcile);

  /*
   * Does an xray runtime exist to have readings about?
   *
   * Everything on `daemon.xray` other than `state` is a zero value when it does
   * not — see the note at the top of this file — so this flag is what separates
   * a reported value from an uninitialised struct field.
   */
  const xrayPresent = daemon ? daemon.xray.state !== 'unavailable' : false;
  const authorizationDigest = daemon?.xray.egress_authorization_digest ?? '';
  const shortAuthorizationDigest = authorizationDigest
    ? `${authorizationDigest.slice(0, 12)}...`
    : null;

  return (
    <Screen
      header={
        <PageHeader
          title="Daemon"
          description="What the running daemon observes. Everything here disappears when it stops."
          meta={
            daemon ? (
              <RuntimeBadge state={daemon.state} />
            ) : (
              <Badge variant="unknown" size="sm">
                {daemonError ? 'not observed' : 'not read'}
              </Badge>
            )
          }
        />
      }
    >
      {/* Unreachable is a statement about the panel's view, never about the
        * daemon's health. The two are indistinguishable from here and the
        * screen says so instead of choosing one. */}
      {daemonError && (
        <ErrorState
          headingLevel={2}
          title="The daemon did not answer"
          description={
            'This means the panel could not reach the control plane. It does not mean the daemon '
            + 'is stopped — a stopped daemon and an unreachable one look identical from here, and '
            + 'guessing between them is how a panel ends up reporting an outage that is really a '
            + 'firewall rule.'
          }
          onRetry={() => void refresh()}
          retrying={loading}
          labelRetry="Try again"
          details={
            `Reported as ${daemonError}. `
            + 'Configuration is unaffected: it is read from disk and remains authoritative.'
            + (local ? ` Revision ${local.revision} was read successfully.` : '')
          }
          labelDetails="What is still known"
          defaultDetailsOpen
          copyable={false}
          bordered
        />
      )}

      {/* Before the first read, and after one that never landed. An unread
        * daemon is not a stopped one, and the empty page must not imply it. */}
      {!daemon && !daemonError && (
        <Card variant="outlined">
          <CardBody>
            <EmptyState
              headingLevel={3}
              icon={<IconDaemon />}
              title={loading ? 'Reading the daemon' : 'The daemon has not been read'}
              description="Nothing on this page is known until the control plane answers."
            />
          </CardBody>
        </Card>
      )}

      {daemon && !daemon.reconcile.observation_fresh && (
        <InlineMessage variant="warning">
          Runtime details were last observed{' '}
          <Timestamp value={daemon.reconcile.observed_at} display="relative" />. Reconciliation is
          in progress, so the component readings below may be stale.
        </InlineMessage>
      )}

      {daemon && (
        <>
          <Columns min="24rem">
            <Card variant="outlined">
              <CardHeader headingLevel={2} title="Process" />
              <CardBody>
                <StateMatrix
                  label="Daemon process state"
                  layout="stack"
                  dimensions={[
                    {
                      key: 'state',
                      label: 'State',
                      value: daemon.state,
                      status: runtimeStatus(daemon.state),
                    },
                    {
                      key: 'started',
                      label: 'Started',
                      /* Guarded, not handed over whole. This card renders
                       * whenever the daemon ANSWERED, not only when it is
                       * running, and a daemon that is not running has no start
                       * time to report. An element is always truthy, so passing
                       * the `Timestamp` unconditionally would have StateMatrix
                       * mark the row observed while the `Timestamp` inside
                       * printed its own unobserved dash — the row asserting a
                       * reading next to a value denying one. The element is
                       * still right here: a relative stamp re-renders as it
                       * ages, which a formatted string cannot do. */
                      value: daemon.started_at ? (
                        <Timestamp value={daemon.started_at} display="relative" />
                      ) : null,
                    },
                    {
                      key: 'api',
                      label: 'API version',
                      value: `v${daemon.api_version}`,
                      note: 'The control-plane contract this daemon speaks.',
                    },
                    {
                      key: 'config_path',
                      label: 'Config path',
                      value: <Code variant="plain">{daemon.config_path}</Code>,
                    },
                    {
                      key: 'control_addr',
                      label: 'Control address',
                      value: <Code variant="plain">{daemon.control_addr}</Code>,
                    },
                    {
                      key: 'revision',
                      label: 'Revision in memory',
                      value: String(daemon.revision),
                      status: local && local.revision !== daemon.revision ? 'degraded' : 'ok',
                      note:
                        local && local.revision !== daemon.revision
                          ? `The config file is at revision ${local.revision}. The daemon has not picked the change up.`
                          : 'Matches the configuration on disk.',
                    },
                  ]}
                />
              </CardBody>
            </Card>

            <Card variant="outlined">
              <CardHeader
                headingLevel={2}
                title="Idempotency"
                description="How long a repeated request is recognised as the same request."
              />
              <CardBody>
                <div style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
                  <StateMatrix
                    label="Idempotency guarantees"
                    layout="stack"
                    dimensions={[
                      { key: 'scope', label: 'Scope', value: daemon.idempotency.scope },
                      {
                        key: 'restart',
                        label: 'Survives restart',
                        value: daemon.idempotency.restart_persistent ? 'yes' : 'no',
                        status: daemon.idempotency.restart_persistent ? 'ok' : 'degraded',
                        note: daemon.idempotency.restart_persistent
                          ? 'Request keys are persisted, so a retry after a restart is still deduplicated.'
                          : 'Request keys live in process memory only. After a restart, a retried request is a NEW request.',
                      },
                      {
                        key: 'provisional',
                        label: 'Provisional',
                        value: daemon.idempotency.provisional ? 'yes' : 'no',
                        status: daemon.idempotency.provisional ? 'degraded' : 'ok',
                        note: daemon.idempotency.provisional
                          ? 'The guarantee is not final and may change between builds. Do not design retry logic around it.'
                          : undefined,
                      },
                    ]}
                  />
                  {!daemon.idempotency.restart_persistent && (
                    <InlineMessage variant="warning">
                      This is why the panel never auto-retries a write. A retry that crosses a
                      daemon restart is indistinguishable from a fresh request, and the config
                      store’s revision check is the only thing standing between that and a double
                      apply.
                    </InlineMessage>
                  )}
                </div>
              </CardBody>
            </Card>
          </Columns>

          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title="xray"
              description="Configuration generations and the strictness flags in force. The flags are fixed by the build; nothing in the configuration moves them."
              actions={<RuntimeBadge state={daemon.xray.state} />}
            />
            <CardBody>
              <StateMatrix
                label="xray runtime"
                layout="grid"
                size="sm"
                dimensions={[
                  {
                    key: 'state',
                    label: 'State',
                    value: daemon.xray.state,
                    status: runtimeStatus(daemon.xray.state),
                    note: xrayPresent
                      ? undefined
                      : 'No Xray runtime observation is available in this status response.',
                  },
                  {
                    key: 'generation',
                    label: 'Installed generation',
                    value: !xrayPresent
                      ? null
                      : daemon.xray.current
                        ? `#${daemon.xray.current.generation} · ${daemon.xray.current.ref_count} refs`
                        : 'none installed',
                    status: daemon.xray.current ? 'ok' : 'inactive',
                    note: !xrayPresent
                      ? 'With no xray runtime there is nothing that could hold a generation, so this is an absence and not a zero.'
                      : daemon.xray.current
                        ? 'The generation currently serving new connections.'
                        : 'The runtime reports no current generation.',
                  },
                  {
                    key: 'draining',
                    label: 'Draining generations',
                    /* A stated zero, not an absence — but only while a runtime
                     * exists to have counted. Without one the empty slice is
                     * the struct's zero value and says nothing. */
                    value: xrayPresent ? String(daemon.xray.draining.length) : null,
                    status: daemon.xray.draining.length > 0 ? 'degraded' : 'ok',
                    note: !xrayPresent
                      ? 'No Xray runtime observation is available in this status response.'
                      : daemon.xray.draining.length > 0
                        ? 'Superseded generations still serving existing connections.'
                        : 'Nothing is draining — the daemon returned an empty list, which is a reading.',
                  },
                  {
                    key: 'inbounds',
                    label: 'Bound inbounds',
                    value: xrayPresent
                      ? `${daemon.xray.inbounds.filter((item) => item.state === 'bound').length} / ${daemon.xray.inbounds.length}`
                      : null,
                    status: daemon.xray.inbounds.some((item) => item.state !== 'bound')
                      ? 'degraded'
                      : 'ok',
                    note: 'Handlers registered by the last successful apply or rollback.',
                  },
                  {
                    key: 'egress_authorization_revision',
                    label: 'Authorization revision',
                    value: !xrayPresent
                      ? null
                      : daemon.xray.egress_authorization_revision === -1
                        ? '-1 (fail-stop)'
                        : String(daemon.xray.egress_authorization_revision),
                    status: daemon.xray.fail_stopped ? 'failed' : 'ok',
                    note: 'Applied runtime revision that confirmed the immutable node-egress authorization snapshot currently installed in Xray.',
                  },
                  {
                    key: 'egress_authorization_sources',
                    label: 'Authorized sources',
                    value: xrayPresent ? String(daemon.xray.egress_authorization_sources) : null,
                    status: daemon.xray.fail_stopped ? 'inactive' : 'info',
                    note: 'Authenticated peer node IDs in the active runtime snapshot. Zero is an explicit default-deny reading.',
                  },
                  {
                    key: 'egress_authorization_denials',
                    label: 'Authorization denials',
                    value: xrayPresent ? String(daemon.xray.egress_authorization_denials) : null,
                    status: daemon.xray.egress_authorization_denials > 0 ? 'info' : 'ok',
                    note: 'Cumulative source, snapshot, or destination-policy refusals since this daemon started.',
                  },
                  {
                    key: 'egress_authorization_digest',
                    label: 'Authorization digest',
                    value: shortAuthorizationDigest ? (
                      <span title={authorizationDigest}>
                        <Code variant="plain">{shortAuthorizationDigest}</Code>
                      </span>
                    ) : null,
                    note: 'Short SHA-256 fingerprint of the semantic runtime snapshot; hover for the complete digest.',
                  },
                  {
                    key: 'strict_stream',
                    label: 'Strict stream outbound',
                    value: !xrayPresent
                      ? null
                      : daemon.xray.strict_stream_outbound
                        ? 'enforced'
                        : 'not enforced',
                    status: 'info',
                    explicit: true,
                    note: 'Refuses stream outbounds that do not match the declared endpoint kind.',
                  },
                  {
                    key: 'strict_packet',
                    label: 'Strict packet outbound',
                    value: !xrayPresent
                      ? null
                      : daemon.xray.strict_packet_outbound
                        ? 'enforced'
                        : 'not enforced',
                    status: 'info',
                    explicit: true,
                    note: 'The packet-side equivalent. Reported independently of the stream flag.',
                  },
                ]}
              />
            </CardBody>
          </Card>

          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title="rendr"
              description="The capabilities of the stream boundary X-Tier actually hands to rendr."
              actions={<RuntimeBadge state={daemon.rendr.state} />}
            />
            <CardBody>
              <StateMatrix
                label="rendr runtime"
                layout="grid"
                size="sm"
                dimensions={[
                  {
                    key: 'sessions',
                    label: 'Active sessions',
                    value:
                      daemon.rendr.state === 'unavailable'
                        ? null
                        : `${daemon.rendr.active_client_sessions ?? 0} client · ${daemon.rendr.active_accepted_sessions ?? 0} accepted`,
                    note: `${daemon.rendr.total_client_sessions ?? 0} client and ${daemon.rendr.total_accepted_sessions ?? 0} accepted sessions observed since start.`,
                  },
                  {
                    key: 'carrier',
                    label: 'Stream carrier',
                    value: daemon.rendr.stream_carrier ?? null,
                    status: daemon.rendr.stream_carrier === 'unknown' ? 'info' : 'degraded',
                    explicit: true,
                    note: 'Xray has already authenticated and decrypted this stream, so the underlying protocol family is deliberately not claimed as raw TCP.',
                  },
                  {
                    key: 'mobility',
                    label: 'Mobility',
                    value: daemon.rendr.mobility_mode ?? null,
                    status: daemon.rendr.mobility_mode === 'redial_attach' ? 'info' : 'degraded',
                    explicit: true,
                    note: 'Current generic Xray paths can be redialled and attached. Endpoint-owned TCP repair is not exposed at this boundary.',
                  },
                  {
                    key: 'endpoint',
                    label: 'Endpoint ownership',
                    value:
                      daemon.rendr.state === 'unavailable'
                        ? null
                        : daemon.rendr.endpoint_owned
                          ? 'owned'
                          : 'not owned',
                    status: 'info',
                    explicit: true,
                  },
                  {
                    key: 'packet',
                    label: 'Packet sessions',
                    value:
                      daemon.rendr.state === 'unavailable'
                        ? null
                        : daemon.rendr.packet_supported
                          ? 'available'
                          : 'unavailable',
                    status: daemon.rendr.packet_supported ? 'ok' : 'inactive',
                    explicit: true,
                    note: 'This vertical slice carries TCP streams only.',
                  },
                ]}
              />
            </CardBody>
          </Card>

          <Card variant="outlined">
            <CardHeader
              headingLevel={2}
              title="Runtime wiring"
              description="Runtime integration reported by the daemon."
            />
            <CardBody>
              <div style={{ display: 'grid', gap: 'var(--stratum-space-8)' }}>
                <CapabilityGrid
                  label="Runtime wiring"
                  layout="detailed"
                  size="sm"
                  subjectHeader="Runtime"
                  columns={[{ id: 'wired', label: 'Wired into the daemon' }]}
                  subjects={[
                    {
                      id: 'xray',
                      label: 'xray',
                      detail: `reports ${daemon.xray.state}`,
                      capabilities: {
                        wired: {
                          state: xrayPresent ? 'confirmed' : 'unconfirmed',
                          note: xrayPresent
                            ? 'The daemon reports an observed Xray runtime state.'
                            : 'The daemon did not report an available Xray runtime.',
                        },
                      },
                    },
                    {
                      id: 'rendr',
                      label: 'rendr',
                      detail: `reports ${daemon.rendr.state}`,
                      capabilities: {
                        wired: {
                          state:
                            daemon.rendr.state === 'unavailable' ? 'unconfirmed' : 'confirmed',
                          note:
                            daemon.rendr.state === 'unavailable'
                              ? 'The daemon did not report an available Rendr runtime.'
                              : 'The daemon reports an observed Rendr runtime state.',
                        },
                      },
                    },
                  ]}
                />

                <Separator decorative />

                <Disclosure
                  headingLevel={3}
                  size="sm"
                  icon={<IconEye />}
                  title="What status does not report"
                  description="Runtime dimensions absent from the control-plane status."
                >
                  <StateMatrix
                    label="Dimensions the control plane does not expose"
                    layout="grid"
                    size="sm"
                    dimensions={[
                      {
                        key: 'throughput',
                        label: 'Throughput',
                        value: null,
                        note: 'No byte or packet counter exists on the status endpoint for any runtime.',
                      },
                      {
                        key: 'latency',
                        label: 'Latency',
                        value: null,
                        note: 'Nothing here is probed, so there is no round-trip or reachability measurement to report.',
                      },
                      {
                        key: 'resources',
                        label: 'Process resources',
                        value: null,
                        note: 'The daemon reports when it started and nothing about CPU, memory or file descriptors.',
                      },
                    ]}
                  />
                </Disclosure>
              </div>
            </CardBody>
          </Card>
        </>
      )}

      {activeConfigInvalid && daemon && (
        <Banner
          variant="warning"
          size="sm"
          icon={<IconAlert />}
          title="Active configuration is invalid"
          action={
            <Button
              size="sm"
              variant="default"
              icon={<IconRefresh />}
              onClick={() =>
                mutation.open({
                  operation: mutations.configRestoreLastGood(),
                  title: 'Restore last known good configuration',
                  description:
                    'Replace the invalid active document with the validated runtime checkpoint at a new revision.',
                  confirmLabel: 'Restore',
                })
              }
            >
              Restore
            </Button>
          }
        >
          The daemon is still serving revision {daemon.configuration.last_known_good_revision}. Restore
          its validated checkpoint before attempting another reload.
        </Banner>
      )}

      <Banner
        variant="info"
        size="sm"
        icon={<IconRefresh />}
        title="Reconcile configuration"
        action={
          <Button
            size="sm"
            variant="default"
            icon={<IconPlay />}
            disabled={activeConfigInvalid}
            onClick={() =>
              mutation.open({
                operation: mutations.runtimeReload(),
                title: 'Reload configuration',
                description: 'Validate and apply the current configuration revision to the running data plane.',
              })
            }
          >
            Reload
          </Button>
        }
      >
        Apply the current on-disk revision to Rendr and Xray without starting a second runtime.
      </Banner>

      <MutationDialog spec={mutation.spec} onClose={mutation.close} onApplied={() => void refresh()} />
    </Screen>
  );
}
