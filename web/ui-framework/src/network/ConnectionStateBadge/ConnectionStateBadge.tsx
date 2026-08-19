import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './ConnectionStateBadge.css';

/**
 * The role a lifecycle state plays, independent of what it is called.
 *
 * The badge knows nothing about any particular protocol — the caller supplies
 * the state list and says what kind each entry is. `unknown` is a first-class
 * kind: a lifecycle we have not been told the position of is not a lifecycle
 * sitting at step one.
 */
export type ConnectionStateKind = 'idle' | 'progress' | 'ready' | 'terminal' | 'error' | 'unknown';

export interface ConnectionState {
  id: string;
  label: string;
  kind: ConnectionStateKind;
  /** One line explaining the state, surfaced on hover and to assistive tech. */
  description?: string;
}

export interface ConnectionStateBadgeProps
  extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  /** The lifecycle, in order. */
  states: ConnectionState[];
  /**
   * Id of the state the connection is in.
   *
   * `null`, `undefined`, or an id that is not in `states` all render the
   * unobserved presentation. None of them fall back to `states[0]` — assuming
   * a connection is at the start of its lifecycle because nobody said
   * otherwise is exactly the invented fact this library refuses to display.
   */
  current?: string | null;
  /** Renders lifecycle pips. Default `true`. Suppressed when `current` is unknown. */
  showProgress?: boolean;
  size?: 'sm' | 'md';
  /**
   * Loops a slow fade on the glyph. Defaults to `true` for a `progress` state
   * and `false` for every settled one — a resting badge never animates. Stops
   * under `prefers-reduced-motion: reduce`.
   */
  pulse?: boolean;
  /** Secondary text, e.g. an error message or a retry count. */
  detail?: string;
  /** Shown when `current` cannot be resolved. Default `'state not observed'`. */
  unknownLabel?: string;
  /** Accessible progress sentence. Default `` `step ${n} of ${total}` ``. */
  progressLabel?: (position: number, total: number) => string;
  /** Appended for assistive tech on a terminal state. Default `'terminal state'`. */
  terminalLabel?: string;
  /** Appended for assistive tech on an error state. Default `'error state'`. */
  errorLabel?: string;
}

/**
 * Four deliberately different silhouettes plus two more, so the six kinds stay
 * separable without hue (WCAG 2.2 SC 1.4.1). All paint is `currentColor`,
 * which dark-mode browser extensions substitute reliably where they mangle
 * `var()` inside SVG.
 */
function glyphFor(kind: ConnectionStateKind): ReactNode {
  switch (kind) {
    // Hollow circle — nothing is happening, and nothing is wrong.
    case 'idle':
      return <circle cx="8" cy="8" r="6.25" />;
    // Broken arc — work in flight. Static; the loop, if any, is a CSS fade.
    case 'progress':
      return <path d="M8 1.75a6.25 6.25 0 1 1-4.42 1.83" />;
    // Circle + tick.
    case 'ready':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" />
          <path d="m5.25 8.2 1.95 1.95 3.6-4.1" />
        </>
      );
    // Circle + stop square. An ending, not a fault.
    case 'terminal':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" />
          <rect x="5.9" y="5.9" width="4.2" height="4.2" rx="0.6" fill="currentColor" stroke="none" />
        </>
      );
    // Octagon + cross. The silhouette differs from every circle above, which
    // is what keeps it readable for a red/green-confused operator.
    case 'error':
      return (
        <>
          <path d="M5.6 1.75h4.8l3.85 3.85v4.8l-3.85 3.85H5.6L1.75 10.4V5.6Z" />
          <path d="m6.1 6.1 3.8 3.8M9.9 6.1 6.1 9.9" />
        </>
      );
    // Dashed circle + centre dot — the library's unobserved vocabulary.
    case 'unknown':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" strokeDasharray="2.4 2.2" />
          <circle cx="8" cy="8" r="1.2" fill="currentColor" stroke="none" />
        </>
      );
  }
}

/**
 * A compact badge for where a connection is in its lifecycle.
 *
 * PROGRESS IS ONLY SHOWN WHEN PROGRESS IS KNOWN
 * ---------------------------------------------
 * The pips are drawn from the index of `current` within `states`. If `current`
 * is null, undefined, or unrecognised, there is no index — so the pips are not
 * rendered at all rather than being rendered empty, which would read as "at
 * the beginning, nothing done yet". An unknown position and a zero position
 * are different claims.
 *
 * STATES AHEAD ARE UNREACHED, NOT FAILED
 * --------------------------------------
 * Pips after the current state are drawn in the dashed unobserved style, even
 * when the lifecycle ended in `error`. A handshake that failed at step 2 did
 * not fail steps 3, 4 and 5 — it never got to them, and painting them red
 * invents four failures out of one.
 *
 * TERMINAL IS NOT ERROR
 * ---------------------
 * `terminal` and `error` are separate kinds. A cleanly closed connection is
 * finished, not broken, and gets a neutral stop glyph rather than the danger
 * role.
 */
export const ConnectionStateBadge = forwardRef<HTMLSpanElement, ConnectionStateBadgeProps>(
  function ConnectionStateBadge(
    {
      states,
      current,
      showProgress = true,
      size = 'md',
      pulse,
      detail,
      unknownLabel = 'state not observed',
      progressLabel = (position, total) => `step ${position} of ${total}`,
      terminalLabel = 'terminal state',
      errorLabel = 'error state',
      className,
      ...rest
    },
    ref,
  ) {
    const currentIndex = current == null ? -1 : states.findIndex((state) => state.id === current);
    const resolved = currentIndex >= 0 ? states[currentIndex] : undefined;

    const kind: ConnectionStateKind = resolved?.kind ?? 'unknown';
    const text = resolved?.label ?? unknownLabel;
    const terminal = kind === 'terminal' || kind === 'error';
    const shouldPulse = pulse ?? kind === 'progress';

    // No index means no progress claim. Not zero progress — none.
    const progressVisible = showProgress && currentIndex >= 0 && states.length > 1;

    return (
      <span
        // Before the spread: an unrecognised state leaves `description`
        // undefined, and after the spread that would delete a consumer's
        // own `title` rather than fall back to it.
        title={resolved?.description}
        {...rest}
        ref={ref}
        data-stratum="connection-state-badge"
        data-kind={kind}
        data-size={size}
        data-terminal={terminal || undefined}
        data-pulse={shouldPulse || undefined}
        data-resolved={currentIndex >= 0 || undefined}
        className={clsx('stratum-connection-state-badge', className)}
      >
        <svg
          className="stratum-connection-state-badge__glyph"
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
          {glyphFor(kind)}
        </svg>

        <span className="stratum-connection-state-badge__label">{text}</span>

        {progressVisible && (
          <span className="stratum-connection-state-badge__pips" aria-hidden="true">
            {states.map((state, index) => (
              <span
                key={state.id}
                className="stratum-connection-state-badge__pip"
                data-position={
                  index < currentIndex ? 'passed' : index === currentIndex ? 'current' : 'unreached'
                }
              />
            ))}
          </span>
        )}

        {progressVisible && (
          <span className="stratum-visually-hidden">
            , {progressLabel(currentIndex + 1, states.length)}
          </span>
        )}

        {kind === 'terminal' && <span className="stratum-visually-hidden">, {terminalLabel}</span>}
        {kind === 'error' && <span className="stratum-visually-hidden">, {errorLabel}</span>}
        {resolved?.description && (
          <span className="stratum-visually-hidden">. {resolved.description}</span>
        )}

        {detail && <span className="stratum-connection-state-badge__detail">{detail}</span>}
      </span>
    );
  },
);
