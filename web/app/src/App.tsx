/* ===========================================================================
 * THE SHELL
 *
 * Navigation is grouped by WHERE THE ANSWER COMES FROM, not by feature, because
 * the backend has two planes that must never be confused:
 *
 *   Configuration   read from disk, valid with the daemon stopped
 *   Runtime         observed by the daemon, absent when it is not running
 *
 * A flat list would leave the operator to work out which half survives a daemon
 * restart. Split, it is structural: when the daemon is unreachable the runtime
 * group goes quiet and the configuration group is still authoritative.
 *
 * THE REVISION LIVES IN THE TOPBAR
 * --------------------------------
 * It is a compare-and-swap token, so it is the one number that invalidates work
 * in progress on any screen. In the chrome — with the time of the last read
 * beside it — an operator who has had a form open for ten minutes can see that
 * the configuration has moved without discovering it at submit.
 * ======================================================================== */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import {
  AppShell,
  Badge,
  Button,
  IconDaemon,
  IconDiagnostics,
  IconIdentity,
  IconInbound,
  IconMenu,
  IconMoon,
  IconOverview,
  IconPath,
  IconPeers,
  IconRefresh,
  IconSettings,
  IconShield,
  IconSun,
  IconSystem,
  Mark,
  Row,
  Separator,
  Sidebar,
  StatusDot,
  Timestamp,
  Tooltip,
  Topbar,
  useMediaQuery,
  useTheme,
} from '@stratum/ui';
import type { SidebarSection } from '@stratum/ui';
import { ControlProvider, useControl } from './state/store';
import { Overview } from './screens/Overview';
import { Peers } from './screens/Peers';
import { Paths } from './screens/Paths';
import { Identity } from './screens/Identity';
import { Runtime } from './screens/Runtime';
import { Inbounds } from './screens/Inbounds';
import { Settings } from './screens/Settings';
import { Profiles } from './screens/Profiles';
import { Diagnostics } from './screens/Diagnostics';
import { PathEditor } from './screens/PathEditor';
import { ScenarioBar } from './dev/ScenarioBar';

const SCREENS = {
  overview: { title: 'Overview', icon: <IconOverview />, render: () => <Overview /> },
  identity: { title: 'Identity', icon: <IconIdentity />, render: () => <Identity /> },
  profiles: { title: 'Profiles', icon: <IconShield />, render: () => <Profiles /> },
  peers: { title: 'Peers', icon: <IconPeers />, render: () => <Peers /> },
  paths: { title: 'Paths', icon: <IconPath />, render: () => <Paths /> },
  inbounds: { title: 'Inbounds', icon: <IconInbound />, render: () => <Inbounds /> },
  settings: { title: 'Settings', icon: <IconSettings />, render: () => <Settings /> },
  runtime: { title: 'Daemon', icon: <IconDaemon />, render: () => <Runtime /> },
  diagnostics: { title: 'Diagnostics', icon: <IconDiagnostics />, render: () => <Diagnostics /> },
} as const;

type ScreenKey = keyof typeof SCREENS;

/**
 * Sub-route renderers, keyed `screen/sub`.
 *
 * Kept beside SCREENS rather than inside it so the nav, the transition order
 * and the sidebar highlight all keep working off the top-level key alone —
 * Paths stays lit while its editor is open, which is what makes the editor read
 * as a place inside Paths rather than a ninth destination.
 */
const SUB_RENDER: Record<string, (route: Route, go: (s: ScreenKey, sub?: string | null) => void) => ReactNode> = {
  'paths/edit': (route, go) => <PathEditor query={route.query} onLeave={() => go('paths')} />,
};

const isScreen = (v: string): v is ScreenKey => v in SCREENS;

/**
 * The sub-routes a screen may own. Two segments is the whole grammar; there is
 * no third level and no dependency.
 */
const SUBROUTES: Partial<Record<ScreenKey, readonly string[]>> = {
  paths: ['edit'],
};

export interface Route {
  screen: ScreenKey;
  /** The second segment, or null. */
  sub: string | null;
  /** Everything after `?`, verbatim, so a screen can own its own draft state. */
  query: string;
}

const isSub = (screen: ScreenKey, seg: string): boolean =>
  (SUBROUTES[screen] ?? []).includes(seg);

/**
 * Hash routing over two segments.
 *
 * Normalisation uses `replaceState`, never assignment. Assignment pushes a
 * history entry, so an unrecognised hash would need two presses of Back to
 * escape — and a screen that rewrites its own query on every keystroke would
 * bury the previous page under a hundred entries.
 */
function useHashRoute(): [Route, (screen: ScreenKey, sub?: string | null) => void] {
  const read = useCallback((): Route => {
    const raw = window.location.hash.replace(/^#\/?/, '');
    const [pathPart = '', ...rest] = raw.split('?');
    const query = rest.join('?');
    const [seg0 = '', seg1 = ''] = pathPart.split('/');

    if (!isScreen(seg0)) {
      window.history.replaceState(null, '', '#/overview');
      return { screen: 'overview', sub: null, query: '' };
    }
    if (!seg1) return { screen: seg0, sub: null, query };
    if (isSub(seg0, seg1)) return { screen: seg0, sub: seg1, query };
    window.history.replaceState(null, '', `#/${seg0}`);
    return { screen: seg0, sub: null, query };
  }, []);

  const [route, setRoute] = useState<Route>(read);

  useEffect(() => {
    const onHash = () => setRoute(read());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, [read]);

  return [
    route,
    useCallback((screen: ScreenKey, sub: string | null = null) => {
      window.location.hash = sub ? `#/${screen}/${sub}` : `#/${screen}`;
      setRoute({ screen, sub, query: '' });
    }, []),
  ];
}

/**
 * Three states, not two.
 *
 * `system` is a real preference and the one most operators want, so the control
 * cycles through all three and names which is in force. A two-way toggle
 * silently strands anyone who chose to follow the OS — and an emoji, which is
 * what stood here, is not an icon: it renders in the platform's colour font at
 * a size nobody controls and ignores `currentColor` entirely.
 */
function ThemeControl() {
  const { preference, theme, cycle } = useTheme();

  const icon =
    preference === 'system' ? <IconSystem /> : preference === 'dark' ? <IconMoon /> : <IconSun />;
  const next = preference === 'light' ? 'dark' : preference === 'dark' ? 'system' : 'light';

  return (
    <Tooltip
      trigger={
        <Button
          variant="ghost"
          size="sm"
          iconOnly
          icon={icon}
          aria-label={`Theme: ${preference}${
            preference === 'system' ? `, currently ${theme}` : ''
          }. Activate for ${next}.`}
          onClick={cycle}
        />
      }
    >
      Theme follows <strong>{preference}</strong>
      {preference === 'system' ? `, currently ${theme}` : ''}. Next: {next}.
    </Tooltip>
  );
}

/** Revision, last read, and the one control that re-reads everything. */
function ControlIndicator({ compact = false }: { compact?: boolean }) {
  const { revision, revisionRead, fetchedAt, loading, refresh, daemon, daemonError } = useControl();

  return (
    <Row gap="var(--stratum-space-6)" wrap={false}>
      <Tooltip
        trigger={
          <span style={{ display: 'inline-flex' }}>
            <StatusDot
              status={daemonError ? 'unknown' : daemon?.state === 'running' ? 'ok' : 'inactive'}
              label={daemonError ? 'not observed' : (daemon?.state ?? 'unread')}
              labelVisible={!compact}
              size="sm"
            />
          </span>
        }
      >
        {daemonError
          ? 'The daemon could not be reached, so its state is unknown — which is not the same as stopped.'
          : daemon
            ? `The daemon reports ${daemon.state}.`
            : 'The daemon has not been read yet.'}
      </Tooltip>

      {!compact && (
        <Separator orientation="vertical" decorative style={{ blockSize: 'var(--stratum-space-8)' }} />
      )}

      <Tooltip
        trigger={
          /* `rev 0` is not a safe placeholder: 0 is a real revision on a node
           * that has never been written, so the fallback is indistinguishable
           * from a reading. Unread says so instead — the dot to the left is
           * already careful about exactly this distinction, and a badge beside
           * it asserting a number nobody read undoes that. */
          <Badge variant={revisionRead ? 'neutral' : 'unknown'} size="xs">
            {revisionRead ? `rev ${revision}` : 'rev not read'}
          </Badge>
        }
      >
        {revisionRead
          ? 'Every change states the revision it was composed against, and is refused if the configuration has moved on since.'
          : 'Neither plane has answered, so the revision is unknown — which is not the same as revision 0.'}
      </Tooltip>

      {!compact && fetchedAt && (
        <span style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}>
          read <Timestamp value={fetchedAt} display="relative" size="sm" />
        </span>
      )}

      <Button
        size="xs"
        variant="default"
        iconOnly={compact}
        icon={<IconRefresh />}
        aria-label={compact ? 'Refresh status' : undefined}
        loading={loading}
        onClick={() => void refresh()}
      >
        {compact ? null : 'Refresh'}
      </Button>
    </Row>
  );
}

function Chrome() {
  const [route, navigate] = useHashRoute();
  const { screen, sub } = route;
  const { refresh, local, daemon, daemonError } = useControl();

  /*
   * The sidebar has TWO modes and one button, and conflating them broke
   * navigation outright below 768px.
   *
   * `AppShell` takes the sidebar out of the grid under `mobileBreakpoint`,
   * positions it off-canvas and marks it `inert` unless `sidebarOpen` is set —
   * while neutralising `sidebarCollapsed` entirely (`collapsed && !overlayOpen`).
   * So a menu button wired only to `collapsed` toggles the one prop that has no
   * effect there, and the panel has no reachable navigation at all in a window
   * docked beside a terminal.
   *
   * Wide: the button collapses to icon width. Narrow: it opens the drawer.
   * The overlay, scrim, Escape handler and focus restore are all already built.
   */
  const isNarrow = useMediaQuery('(max-width: 767px)');
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);

  const navToggle = isNarrow
    ? {
        label: drawerOpen ? 'Close navigation' : 'Open navigation',
        onClick: () => setDrawerOpen((o) => !o),
      }
    : {
        label: collapsed ? 'Expand navigation' : 'Collapse navigation',
        onClick: () => setCollapsed((c) => !c),
      };

  /*
   * Which way the screen should enter.
   *
   * Movement that always comes from the same side is decoration. Taking the
   * sign from the nav-order delta means the transition matches the direction
   * the operator actually travelled, so it reads as position rather than as an
   * effect. `useRef` because the previous index is not render state — reading
   * it must not itself cause one.
   */
  const order = Object.keys(SCREENS) as ScreenKey[];
  const previousIndex = useRef(order.indexOf(screen));
  const index = order.indexOf(screen);
  /*
   * Descending into a sub-route always reads as forward, and leaving it as
   * back, whatever the nav order says. Without this, entering the editor from
   * Paths would compare Paths to itself and enter from an arbitrary side.
   */
  const wasSub = useRef(sub);
  const direction = sub && !wasSub.current
    ? 1
    : !sub && wasSub.current
      ? -1
      : index >= previousIndex.current
        ? 1
        : -1;
  previousIndex.current = index;
  wasSub.current = sub;

  const counts = local?.peer_counts;
  const peerCount = counts ? counts.inbound + counts.outbound + counts.bidirectional : null;
  // `inbounds` is `null` for a node with no listeners — a nil Go slice, not an
  // empty array — so an absent count and a zero count are different facts.
  const enabledListeners = local ? (local.inbounds ?? []).filter((i) => i.enabled).length : null;

  const nav = (key: ScreenKey, count?: number | null) => ({
    key,
    label: SCREENS[key].title,
    icon: SCREENS[key].icon,
    ...(count != null
      ? {
          badge: (
            <Badge size="xs" variant="neutral">
              {count}
            </Badge>
          ),
        }
      : null),
  });

  const sections: SidebarSection[] = [
    { key: 'top', items: [nav('overview')] },
    {
      // Grouped by provenance: everything here is readable from disk with the
      // daemon stopped.
      key: 'config',
      title: 'Configuration',
      items: [nav('identity'), nav('profiles'), nav('peers', peerCount), nav('paths'), nav('inbounds', enabledListeners), nav('settings')],
    },
    {
      // …and nothing here survives the daemon going away.
      key: 'runtime',
      title: 'Runtime',
      items: [nav('runtime'), nav('diagnostics')],
    },
  ];

  // The overlay is always full width, so the compact brand belongs to the
  // collapsed DESKTOP rail only.
  const compact = collapsed && !isNarrow;

  const brand: ReactNode = (
    <Row gap="var(--stratum-space-6)" wrap={false}>
      <Mark size={22} />
      {!compact && (
        <span
          style={{
            fontWeight: 'var(--stratum-weight-semibold)',
            letterSpacing: '-0.01em',
          }}
        >
          X-Tier
        </span>
      )}
    </Row>
  );

  return (
    <AppShell
      contentLabel="Panel content"
      sidebarCollapsed={collapsed}
      sidebarOpen={drawerOpen}
      onSidebarOpenChange={setDrawerOpen}
      sidebar={
        <Sidebar
          label="Panel sections"
          activeKey={screen}
          collapsed={compact}
          onSelect={(k) => {
            navigate(k as ScreenKey);
            // The drawer is modal on narrow screens; leaving it open after a
            // selection hides the screen it just revealed.
            setDrawerOpen(false);
          }}
          sections={sections}
          header={brand}
          footer={
            compact ? (
              /* The same three-way model the topbar indicator uses, not
                * `daemon ? ok : unknown`. That collapsed two independent axes:
                * a daemon that ANSWERED "stopped" got the green dot, because
                * having a reply was being read as being healthy. Reachability
                * and run state are separate readings and the compact footer
                * does not get to merge them just because it is small.
                *
                * The label is the accessible name here — `labelVisible` is off,
                * so the dot is `role="img"` and this string is the only thing
                * a screen reader gets. "daemon" alone named the subject and
                * withheld the reading. */
              <StatusDot
                status={daemonError ? 'unknown' : daemon?.state === 'running' ? 'ok' : 'inactive'}
                label={`daemon: ${daemonError ? 'not observed' : (daemon?.state ?? 'unread')}`}
                size="sm"
              />
            ) : (
              <span
                style={{ fontSize: 'var(--stratum-text-2xs)', color: 'var(--stratum-text-subtle)' }}
              >
                {daemon ? `control API v${daemon.api_version}` : 'API version unread'}
              </span>
            )
          }
        />
      }
      topbar={
        <Topbar
          start={
            <Row gap="var(--stratum-space-6)" wrap={false}>
              <Button
                size="xs"
                variant="ghost"
                iconOnly
                icon={<IconMenu />}
                aria-label={navToggle.label}
                onClick={navToggle.onClick}
              />
              <strong
                style={{
                  fontSize: 'var(--stratum-text-sm)',
                  maxWidth: isNarrow ? '30vw' : undefined,
                  overflow: isNarrow ? 'hidden' : undefined,
                  textOverflow: isNarrow ? 'ellipsis' : undefined,
                  whiteSpace: isNarrow ? 'nowrap' : undefined,
                }}
              >
                {local?.display_name || 'this node'}
              </strong>
            </Row>
          }
          end={
            <Row gap="var(--stratum-space-6)" wrap={false}>
              <ControlIndicator compact={isNarrow} />
              <Separator orientation="vertical" decorative style={{ blockSize: 'var(--stratum-space-8)' }} />
              <ThemeControl />
            </Row>
          }
        />
      }
    >
      {/* Keyed on the screen: React replaces the element rather than updating
        * it, and `@starting-style` only applies to something entering. */}
      <div
        key={`${screen}/${sub ?? ''}`}
        className="xtier-screen"
        style={{ '--_dy': direction } as React.CSSProperties}
      >
        {(sub && SUB_RENDER[`${screen}/${sub}`]?.(route, navigate)) ?? SCREENS[screen].render()}
      </div>
      {import.meta.env.DEV && <ScenarioBar />}
    </AppShell>
  );
}

export function App() {
  return (
    <ControlProvider>
      <Chrome />
    </ControlProvider>
  );
}
