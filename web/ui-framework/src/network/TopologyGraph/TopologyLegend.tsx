import { forwardRef, type CSSProperties, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import { NodeGlyph } from './NodeGlyph';
import type {
  EdgeStatus,
  GraphRole,
  TopologyEdgeStatus,
  TopologyRole,
  TopologyRoleStyle,
  TopologyStatusStyle,
} from './layout';
import './TopologyLegend.css';

const ROLE_ORDER: GraphRole[] = [
  'self',
  'reachable',
  'medium',
  'offline',
  'native',
  'nested',
  'disabled',
];

const STATUS_ORDER: EdgeStatus[] = ['ok', 'degraded', 'down', 'unknown'];

/** What the stylesheet already knows how to paint from `data-role` alone. */
const BUILT_IN_ROLES = new Set<string>(ROLE_ORDER);
const BUILT_IN_STATUSES = new Set<string>(STATUS_ORDER);

const vars = (o: Record<string, string>): CSSProperties => o as CSSProperties;

const DEFAULT_ROLE_LABELS: Record<GraphRole, string> = {
  self: 'This node',
  reachable: 'Reachable',
  medium: 'Partially reachable',
  offline: 'Offline',
  native: 'Native',
  nested: 'Nested',
  disabled: 'Disabled',
};

const DEFAULT_STATUS_LABELS: Record<EdgeStatus, string> = {
  ok: 'Healthy',
  degraded: 'Degraded',
  down: 'Down',
  unknown: 'Not observed',
};

export interface TopologyLegendProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  /**
   * Roles to show, in order. Defaults to the seven built-ins.
   *
   * Any string a consumer registered through `<TopologyGraph roles>` may appear
   * here too — pass the same map as `roleStyles` so the row is drawn the same
   * way the graph draws it.
   */
  roles?: TopologyRole[];
  /**
   * The same map given to `<TopologyGraph roles>`.
   *
   * Only registered roles need it; the built-ins are painted by the stylesheet.
   * A role listed in `roles` but absent from both is drawn as a hexagon, which
   * is what the graph does with it as well.
   */
  roleStyles?: Record<string, TopologyRoleStyle>;
  roleLabels?: Partial<Record<TopologyRole, string>>;
  /** Optional second line per role. */
  roleDescriptions?: Partial<Record<TopologyRole, string>>;

  /** Adds the link-status and traffic key. Default `true`. */
  showEdgeKey?: boolean;
  edgeStatuses?: TopologyEdgeStatus[];
  /** The same map given to `<TopologyGraph statuses>`. */
  statusStyles?: Record<string, TopologyStatusStyle>;
  edgeStatusLabels?: Partial<Record<TopologyEdgeStatus, string>>;

  /** Adds the traffic key: carrying / idle / not observed. Default `true`. */
  showTrafficKey?: boolean;
  trafficActiveLabel?: string;
  trafficIdleLabel?: string;
  trafficUnobservedLabel?: string;

  /** Section captions. Rendered visibly unless `hideHeadings`. */
  nodesHeading?: string;
  linksHeading?: string;
  trafficHeading?: string;
  hideHeadings?: boolean;

  /** Accessible name for the whole legend. */
  label?: string;

  orientation?: 'horizontal' | 'vertical';
  size?: 'sm' | 'md';
}

/**
 * The key to every channel {@link TopologyGraph} uses.
 *
 * WHY IT SHARES `NodeGlyph`
 * -------------------------
 * The glyphs here are rendered by exactly the component the graph uses, at a
 * fixed radius. A legend drawn separately drifts the first time a role's shape
 * changes, and a legend that disagrees with the diagram is worse than no legend
 * — it actively teaches the wrong reading.
 *
 * WHY SHAPE, NOT JUST COLOUR
 * --------------------------
 * Two of the seven role colours are close for colour-vision-deficient users
 * (medium/native are both warm, nested/disabled both grey) and under
 * `forced-colors` all seven collapse to two system keywords. So every row is
 * identified by shape and by text; colour is the third, redundant channel.
 *
 * The glyphs are `aria-hidden`: the visible label already carries the meaning,
 * and announcing "image" before each row is pure noise.
 */
export const TopologyLegend = forwardRef<HTMLDivElement, TopologyLegendProps>(
  function TopologyLegend(
    {
      roles = ROLE_ORDER,
      roleStyles,
      roleLabels,
      roleDescriptions,

      showEdgeKey = true,
      edgeStatuses = STATUS_ORDER,
      statusStyles,
      edgeStatusLabels,

      showTrafficKey = true,
      trafficActiveLabel = 'Carrying traffic',
      trafficIdleLabel = 'Measured, idle',
      trafficUnobservedLabel = 'Throughput not observed',

      nodesHeading = 'Nodes',
      linksHeading = 'Links',
      trafficHeading = 'Traffic',
      hideHeadings = false,

      label = 'Topology legend',

      orientation = 'horizontal',
      size = 'md',
      className,
      ...rest
    },
    ref,
  ) {
    // Precedence: the English defaults, then a registered style's own `label`,
    // then the explicit label props. A row never falls through to nothing — the
    // key itself is used as a last resort, because an unlabelled legend row is
    // strictly worse than no row at all.
    const roleText: Record<string, string> = { ...DEFAULT_ROLE_LABELS };
    for (const [key, style] of Object.entries(roleStyles ?? {})) {
      if (style.label) roleText[key] = style.label;
    }
    if (roleLabels) {
      for (const [key, text] of Object.entries(roleLabels)) {
        if (typeof text === 'string') roleText[key] = text;
      }
    }

    const statusText: Record<string, string> = { ...DEFAULT_STATUS_LABELS };
    for (const [key, style] of Object.entries(statusStyles ?? {})) {
      if (style.label) statusText[key] = style.label;
    }
    if (edgeStatusLabels) {
      for (const [key, text] of Object.entries(edgeStatusLabels)) {
        if (typeof text === 'string') statusText[key] = text;
      }
    }

    const heading = (text: string) => (
      <h4 className={clsx('stratum-topology-legend__heading', hideHeadings && 'stratum-visually-hidden')}>
        {text}
      </h4>
    );

    return (
      <div
        // Before the spread so the English defaults stay defaults: after it,
        // `label`'s fallback would overwrite a consumer's own `aria-label`.
        role="group"
        aria-label={label}
        {...rest}
        ref={ref}
        data-stratum="topology-legend"
        data-orientation={orientation}
        data-size={size}
        className={clsx('stratum-topology-legend', className)}
      >
        <section className="stratum-topology-legend__section">
          {heading(nodesHeading)}
          <ul className="stratum-topology-legend__list">
            {roles.map((role) => {
              const style = roleStyles?.[role];
              const known = BUILT_IN_ROLES.has(role);
              // A built-in passes nothing but its key, so its paint stays in the
              // stylesheet — exactly as the graph does it, which is the only
              // reason the two surfaces cannot drift.
              const glyph = style
                ? {
                    shape: style.shape ?? (known ? undefined : ('hexagon' as const)),
                    colour: style.colour,
                    fill: style.fill,
                    hollow: style.hollow ?? false,
                    ...(style.dash !== undefined ? { dash: style.dash } : {}),
                  }
                : known
                  ? {}
                  : { shape: 'hexagon' as const };

              return (
                <li key={role} className="stratum-topology-legend__item" data-role={role}>
                  <svg
                    className="stratum-topology-legend__glyph"
                    viewBox="-11 -11 22 22"
                    width="18"
                    height="18"
                    aria-hidden="true"
                    focusable="false"
                  >
                    <NodeGlyph role={role} r={7} {...glyph} />
                  </svg>
                  <span className="stratum-topology-legend__text">
                    <span className="stratum-topology-legend__label">
                      {roleText[role] ?? role}
                    </span>
                    {roleDescriptions?.[role] && (
                      <span className="stratum-topology-legend__note">
                        {roleDescriptions[role]}
                      </span>
                    )}
                  </span>
                </li>
              );
            })}
          </ul>
        </section>

        {showEdgeKey && (
          <section className="stratum-topology-legend__section">
            {heading(linksHeading)}
            <ul className="stratum-topology-legend__list">
              {edgeStatuses.map((status) => {
                const style = statusStyles?.[status];
                const known = BUILT_IN_STATUSES.has(status);
                return (
                  <li
                    key={status}
                    className="stratum-topology-legend__item"
                    data-status={status}
                    // The line already reads --_line-colour / --_line-dash with
                    // token fallbacks, so a registered status only has to fill
                    // them in. Under forced-colors the stylesheet overrides the
                    // stroke outright and the dash carries the meaning.
                    style={
                      style
                        ? vars({
                            '--_line-colour': style.colour,
                            '--_line-dash': style.dash ?? (known ? 'none' : '7 3 2 3'),
                          })
                        : known
                          ? undefined
                          : vars({ '--_line-dash': '7 3 2 3' })
                    }
                  >
                    <svg
                      className="stratum-topology-legend__glyph"
                      viewBox="0 0 26 18"
                      width="26"
                      height="18"
                      aria-hidden="true"
                      focusable="false"
                    >
                      <path className="stratum-topology-legend__line" d="M1 9 H25" />
                    </svg>
                    <span className="stratum-topology-legend__text">
                      <span className="stratum-topology-legend__label">
                        {statusText[status] ?? status}
                      </span>
                    </span>
                  </li>
                );
              })}
            </ul>
          </section>
        )}

        {showTrafficKey && (
          <section className="stratum-topology-legend__section">
            {heading(trafficHeading)}
            <ul className="stratum-topology-legend__list">
              {(
                [
                  ['active', trafficActiveLabel],
                  ['idle', trafficIdleLabel],
                  ['unobserved', trafficUnobservedLabel],
                ] as const
              ).map(([kind, text]) => (
                <li key={kind} className="stratum-topology-legend__item" data-traffic={kind}>
                  <svg
                    className="stratum-topology-legend__glyph"
                    viewBox="0 0 26 18"
                    width="26"
                    height="18"
                    aria-hidden="true"
                    focusable="false"
                  >
                    <path className="stratum-topology-legend__line" d="M1 9 H25" />
                    {kind === 'active' && (
                      <path className="stratum-topology-legend__flow" d="M1 9 H25" />
                    )}
                  </svg>
                  <span className="stratum-topology-legend__text">
                    <span className="stratum-topology-legend__label">{text}</span>
                  </span>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    );
  },
);
