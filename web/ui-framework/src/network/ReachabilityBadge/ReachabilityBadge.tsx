import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './ReachabilityBadge.css';

/**
 * How the endpoint is reached.
 *
 * `mediated` is NOT a degraded form of `direct`. Plenty of systems reach their
 * best throughput through an intermediary, and plenty of direct paths are
 * terrible. This is a description of the topology, not a grade, so the two are
 * given different hues at the same severity rather than a good/bad pairing.
 *
 * `unknown` means the directness was not determined — which is independent of
 * whether the transport itself is known.
 */
export type ReachabilityQuality = 'direct' | 'mediated' | 'unknown';

export interface ReachabilityMode {
  id: string;
  label: string;
  /** One line explaining the mode, surfaced on hover and to assistive tech. */
  description?: string;
  /** Defaults to `'unknown'`. A named transport does not imply a direct path. */
  quality?: ReachabilityQuality;
}

export interface ReachabilityBadgeProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  /**
   * The mode. `null` / `undefined` renders the unobserved presentation — never
   * a guess, and never "direct" by default.
   */
  mode?: ReachabilityMode | null;
  size?: 'sm' | 'md';
  /** `'badge'` draws a filled chip; `'inline'` is bare text with an icon. */
  variant?: 'badge' | 'inline';
  /** Hides the label, leaving the icon. Requires `aria-label`. */
  iconOnly?: boolean;
  /** Replaces the built-in icon. Kept decorative — the label carries meaning. */
  icon?: ReactNode;
  /** Secondary text, e.g. a hop count or an address family. */
  detail?: string;
  /** Shown when `mode` is absent. Default `'reachability not observed'`. */
  unknownLabel?: string;
  /** Appended for assistive tech on a direct path. Default `'direct path'`. */
  directLabel?: string;
  /** Appended for assistive tech on a mediated path. Default `'via an intermediary'`. */
  mediatedLabel?: string;
  /** Appended for assistive tech when directness is undetermined. Default `'path not determined'`. */
  qualityUnknownLabel?: string;
}

/**
 * Three topologies, three silhouettes. Painted in `currentColor` so the icon
 * survives dark-mode browser extensions, which substitute `currentColor`
 * reliably where they mangle `var()` inside SVG paint.
 */
function iconFor(quality: ReachabilityQuality): ReactNode {
  switch (quality) {
    // Two endpoints, one unbroken line between them.
    case 'direct':
      return (
        <>
          <path d="M5.4 8h5.2" />
          <circle cx="2.9" cy="8" r="1.9" fill="currentColor" stroke="none" />
          <circle cx="13.1" cy="8" r="1.9" fill="currentColor" stroke="none" />
        </>
      );
    // Two endpoints, the line detouring through a third node drawn as a
    // SQUARE — a different shape, not a warning colour.
    case 'mediated':
      return (
        <>
          <path d="M3.6 10.2 8 5.6l4.4 4.6" />
          <rect x="6.2" y="2.1" width="3.6" height="3.6" rx="0.7" fill="currentColor" stroke="none" />
          <circle cx="2.6" cy="11.6" r="1.9" fill="currentColor" stroke="none" />
          <circle cx="13.4" cy="11.6" r="1.9" fill="currentColor" stroke="none" />
        </>
      );
    // Hollow endpoints, dashed line: the shape of a path we have not traced.
    case 'unknown':
      return (
        <>
          <path d="M5.4 8h5.2" strokeDasharray="1.8 1.8" />
          <circle cx="2.9" cy="8" r="1.75" />
          <circle cx="13.1" cy="8" r="1.75" />
        </>
      );
  }
}

/**
 * A badge for how an endpoint is reached.
 *
 * MEDIATED IS NOT BROKEN
 * ----------------------
 * The obvious design — green for direct, amber for relayed — teaches operators
 * that a relay is a fault, and they start chasing perfectly healthy connections
 * that were configured that way on purpose. So neither quality gets a status
 * hue: `direct` is neutral and `mediated` is informational, at the same visual
 * weight. The difference is carried by the icon's topology, which is the thing
 * that actually differs.
 *
 * TWO INDEPENDENT UNKNOWNS
 * ------------------------
 * Whether we know the *mode* and whether we know the *directness* are separate
 * facts, and the badge keeps them separate. A named transport with no quality
 * reported shows its name in full strength with an undetermined-path icon; it
 * is not promoted to `direct` because a transport that is usually direct was
 * named. Only a completely absent mode gets the dashed, italic unobserved
 * treatment.
 */
export const ReachabilityBadge = forwardRef<HTMLSpanElement, ReachabilityBadgeProps>(
  function ReachabilityBadge(
    {
      mode,
      size = 'md',
      variant = 'badge',
      iconOnly = false,
      icon,
      detail,
      unknownLabel = 'reachability not observed',
      directLabel = 'direct path',
      mediatedLabel = 'via an intermediary',
      qualityUnknownLabel = 'path not determined',
      className,
      ...rest
    },
    ref,
  ) {
    if (import.meta.env?.DEV && iconOnly && !rest['aria-label'] && !rest['aria-labelledby']) {
      console.error(
        '[stratum] <ReachabilityBadge iconOnly> requires `aria-label` or `aria-labelledby`. ' +
          'A `title` attribute is not a reliable substitute.',
      );
    }

    const resolved = mode != null;
    const quality: ReachabilityQuality = mode?.quality ?? 'unknown';
    const text = resolved ? mode.label : unknownLabel;

    const qualityText =
      quality === 'direct' ? directLabel : quality === 'mediated' ? mediatedLabel : qualityUnknownLabel;

    return (
      <span
        // Before the spread: an unrecognised mode leaves `description`
        // undefined, and after the spread that would delete a consumer's
        // own `title` rather than fall back to it.
        title={mode?.description}
        {...rest}
        ref={ref}
        data-stratum="reachability-badge"
        data-quality={quality}
        data-variant={variant}
        data-size={size}
        data-resolved={resolved || undefined}
        data-icon-only={iconOnly || undefined}
        className={clsx('stratum-reachability-badge', className)}
      >
        <span className="stratum-reachability-badge__icon" aria-hidden="true">
          {icon ?? (
            <svg
              viewBox="0 0 16 16"
              width="1em"
              height="1em"
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              strokeLinecap="round"
              strokeLinejoin="round"
              focusable="false"
              aria-hidden="true"
            >
              {iconFor(quality)}
            </svg>
          )}
        </span>

        {iconOnly ? (
          <span className="stratum-visually-hidden">
            {text}, {qualityText}
          </span>
        ) : (
          <>
            <span className="stratum-reachability-badge__label">{text}</span>
            {/* The icon is never the only carrier of the topology. */}
            <span className="stratum-visually-hidden">, {qualityText}</span>
          </>
        )}

        {detail && !iconOnly && (
          <span className="stratum-reachability-badge__detail">{detail}</span>
        )}
        {mode?.description && <span className="stratum-visually-hidden">. {mode.description}</span>}
      </span>
    );
  },
);
