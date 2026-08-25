/* ===========================================================================
 * THE SCENARIO BAR — DEVELOPMENT ONLY
 *
 * A fault injector for reviewing the panel. It is the ONLY thing in the app
 * that talks to `/v1/__scenario`, which is the mock's own route and does not
 * exist on the real daemon — so a stray call from anywhere else is obvious.
 *
 * The whole file is behind `import.meta.env.DEV` and tree-shakes out of a
 * production build.
 *
 * WHY IT EXISTS
 * -------------
 * Most of this panel's careful behaviour only appears in states a healthy
 * backend will not produce on request: a mismatched identity, a daemon that
 * has gone away mid-session, a losing race with another writer, a write that
 * lands and reports an error anyway. Those are exactly the paths worth
 * reviewing, and reviewing them means being able to cause them on demand
 * rather than waiting for one in production.
 * ======================================================================== */
import { useEffect, useState } from 'react';
import { Badge, Button, Popover, Select, Switch } from '@stratum/ui';
import { useControl } from '../state/store';

interface Scenario {
  identity: string;
  daemon: string;
  stealRevision: boolean;
  commitVisible: boolean;
  emptyPeers: boolean;
  peersUnreadable: boolean;
  slow: number;
}

const IDENTITY_STATES = [
  { value: 'backed', label: 'backed', description: 'The healthy state.' },
  { value: 'uninitialized', label: 'uninitialized', description: 'No identity yet. CLI can fix.' },
  { value: 'recoverable', label: 'recoverable', description: 'Valid, but loose seed permissions.' },
  { value: 'legacy_unbacked', label: 'legacy_unbacked', description: 'Dead end: v1 ID, no seed.' },
  { value: 'backing_missing', label: 'backing_missing', description: 'Dead end: seed deleted.' },
  { value: 'mismatch', label: 'mismatch', description: 'Dead end: seed backs a different ID.' },
];

const DAEMON_STATES = [
  { value: 'observed', label: 'running', description: 'Answers normally.' },
  { value: 'unreachable', label: 'unreachable', description: 'Socket refuses. Panel must not call this "stopped".' },
  { value: 'stopped', label: 'stopped', description: 'Answers, and reports itself stopped.' },
  { value: 'starting', label: 'starting', description: 'Answers, mid-startup.' },
];

export function ScenarioBar() {
  // Vite replaces this with a literal, so the whole component body — and the
  // fetches below — drop out of a production bundle.
  if (!import.meta.env.DEV) return null;

  // eslint-disable-next-line react-hooks/rules-of-hooks
  return <ScenarioBarInner />;
}

function ScenarioBarInner() {
  const { refresh } = useControl();
  const [scenario, setScenario] = useState<Scenario | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    void fetch('/v1/__scenario')
      .then((r) => (r.ok ? r.json() : null))
      .then(setScenario)
      .catch(() => setScenario(null));
  }, []);

  const patch = async (next: Partial<Scenario>) => {
    const r = await fetch('/v1/__scenario', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(next),
    });
    setScenario(await r.json());
    await refresh();
  };

  // No mock running — the app is presumably pointed at something real.
  if (!scenario) return null;

  const armed =
    scenario.identity !== 'backed'
    || scenario.daemon !== 'observed'
    || scenario.stealRevision
    || scenario.commitVisible
    || scenario.emptyPeers
    || scenario.peersUnreadable
    || scenario.slow > 0;

  return (
    <div
      /* Bottom-LEFT. The toast region owns bottom-right, and a fault injector
       * sitting on top of the feedback channel would hide the very
       * confirmations it exists to provoke. */
      style={{
        position: 'fixed',
        insetBlockEnd: 'var(--stratum-space-5)',
        insetInlineStart: 'var(--stratum-space-5)',
        zIndex: 'var(--stratum-z-sticky)',
      }}
    >
      <Popover
        open={open}
        onOpenChange={setOpen}
        placement="top-start"
        aria-label="Fault injection controls"
        trigger={
          <Button variant={armed ? 'danger' : 'default'} size="sm">
            {armed ? 'Faults armed' : 'Scenario'}
          </Button>
        }
      >
        <div style={{ display: 'grid', gap: 'var(--stratum-space-5)', width: '22rem' }}>
          <div style={{ display: 'grid', gap: 'var(--stratum-space-1)' }}>
            <strong>Fault injection</strong>
            <span
              style={{ fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-muted)' }}
            >
              Development only. These drive the mock daemon into states a healthy backend will not
              produce on request. Nothing here exists on the real control plane.
            </span>
          </div>

          <label style={{ display: 'grid', gap: 'var(--stratum-space-2)' }}>
            <span style={{ fontSize: 'var(--stratum-text-sm)', fontWeight: 500 }}>
              Identity state
            </span>
            <Select
              options={IDENTITY_STATES}
              value={scenario.identity}
              onChange={(v) => v && void patch({ identity: v })}
              fullWidth
              aria-label="Identity state"
            />
          </label>

          <label style={{ display: 'grid', gap: 'var(--stratum-space-2)' }}>
            <span style={{ fontSize: 'var(--stratum-text-sm)', fontWeight: 500 }}>Daemon</span>
            <Select
              options={DAEMON_STATES}
              value={scenario.daemon}
              onChange={(v) => v && void patch({ daemon: v })}
              fullWidth
              aria-label="Daemon behaviour"
            />
          </label>

          <div style={{ display: 'grid', gap: 'var(--stratum-space-3)' }}>
            <Switch
              size="sm"
              checked={scenario.stealRevision}
              onCheckedChange={(v) => void patch({ stealRevision: v })}
              description="Fires once, then disarms — so the operator's retry succeeds, which is the half worth reviewing."
            >
              Lose the next revision race
            </Switch>

            <Switch
              size="sm"
              checked={scenario.commitVisible}
              onCheckedChange={(v) => void patch({ commitVisible: v })}
              description="Returns config.commit_visible_and_resynced: an error whose write DID land."
            >
              Next write applies but errors
            </Switch>

            <Switch
              size="sm"
              checked={scenario.emptyPeers}
              onCheckedChange={(v) => void patch({ emptyPeers: v })}
            >
              Empty address book
            </Switch>

            <Switch
              size="sm"
              checked={scenario.peersUnreadable}
              onCheckedChange={(v) => void patch({ peersUnreadable: v })}
              description="config.read_failed on the peer list."
            >
              Peer list unreadable
            </Switch>

            <Switch
              size="sm"
              checked={scenario.slow > 0}
              onCheckedChange={(v) => void patch({ slow: v ? 2000 : 0 })}
            >
              Add 2s latency
            </Switch>
          </div>

          {armed && (
            <div style={{ display: 'flex', gap: 'var(--stratum-space-3)', alignItems: 'center' }}>
              <Badge variant="danger">armed</Badge>
              <Button
                size="sm"
                variant="subtle"
                onClick={() =>
                  void patch({
                    identity: 'backed',
                    daemon: 'observed',
                    stealRevision: false,
                    commitVisible: false,
                    emptyPeers: false,
                    peersUnreadable: false,
                    slow: 0,
                  })
                }
              >
                Reset all
              </Button>
            </div>
          )}
        </div>
      </Popover>
    </div>
  );
}
