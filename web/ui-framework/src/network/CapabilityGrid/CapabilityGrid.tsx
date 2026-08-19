import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { formatRelativeTime } from '../format';
import './CapabilityGrid.css';

/**
 * What is known about one capability of one subject.
 *
 * THE FOUR STATES ARE NOT A SCALE
 * -------------------------------
 * They are four different epistemic positions, and no two of them may be
 * derived from one another:
 *
 *   confirmed       We observed it working.
 *   unconfirmed     We have NO observation. It may be unavailable, unprobed,
 *                   irrelevant, or simply not something this subject reports.
 *                   This is the default for anything absent from the data.
 *   unsupported     The subject told us it does not have this. A settled
 *                   negative — still not an error, and never a red cross.
 *   not-applicable  The capability has no meaning for this subject.
 *
 * `unconfirmed` is the ambiguous one on purpose. Rendering it as `unsupported`
 * turns "we did not look" into "it is off", which is the exact failure this
 * component exists to prevent.
 */
export type CapabilityState = 'confirmed' | 'unconfirmed' | 'unsupported' | 'not-applicable';

export interface CapabilityCell {
  state: CapabilityState;
  /** Free text shown on hover and exposed to assistive technology. */
  note?: string;
  /**
   * When this reading was taken. Only meaningful for a state that was actually
   * observed; it is rendered as relative time in the cell's tooltip.
   */
  observedAt?: Date | number | string | null;
}

/** A cell may be given as a bare state or as a cell object with detail. */
export type CapabilityValue = CapabilityState | CapabilityCell;

export interface CapabilityColumn {
  id: string;
  /** Full name, used in the detailed layout and in every tooltip. */
  label: string;
  /** Short form for the compact layout's header. Falls back to `label`. */
  abbr?: string;
  /** One line explaining what the capability is. */
  description?: string;
}

export interface CapabilitySubject {
  id: string;
  label: string;
  /** Secondary line under the subject label, e.g. an address or a role. */
  detail?: string;
  /**
   * Capability id -> value. A column MISSING from this record resolves to
   * `unconfirmed`, never to `unsupported`: absence of a report is not a
   * report of absence.
   */
  capabilities: Record<string, CapabilityValue | undefined>;
}

export interface CapabilityGridProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  columns: CapabilityColumn[];
  subjects: CapabilitySubject[];
  /**
   * `compact` shows glyphs only — a dense matrix for scanning many subjects.
   * `detailed` spells the state out in every cell and shows notes.
   */
  layout?: 'compact' | 'detailed';
  size?: 'sm' | 'md';
  /** Accessible caption for the table. */
  label?: string;
  /**
   * Accessible name for the horizontal scroll region wrapping the table.
   *
   * The region is a keyboard tab stop, because the capability columns overflow
   * and nothing inside the table is focusable — so it needs a name whether or
   * not the grid itself was captioned. `label` wins when it is supplied.
   * Default `'Capability matrix'`.
   */
  scrollRegionLabel?: string;
  /** Header above the subject column. Default `'Subject'`. */
  subjectHeader?: string;
  /** Keeps the subject column visible while the capability columns scroll. */
  stickySubjects?: boolean;
  /** Renders the four-state key beneath the grid. Default `true`. */
  showLegend?: boolean;
  /** Heading for the legend. Default `'Key'`. */
  legendLabel?: string;

  labelConfirmed?: string;
  labelUnconfirmed?: string;
  labelUnsupported?: string;
  labelNotApplicable?: string;

  descriptionConfirmed?: string;
  descriptionUnconfirmed?: string;
  descriptionUnsupported?: string;
  descriptionNotApplicable?: string;

  /** Prefix for the observation time in a tooltip. Default `'observed'`. */
  observedAtLabel?: string;
  locale?: string;
}

const STATE_ORDER: readonly CapabilityState[] = [
  'confirmed',
  'unconfirmed',
  'unsupported',
  'not-applicable',
];

/**
 * Four deliberately different silhouettes, so the states remain separable
 * without hue — for colour-vision-deficient readers and under `forced-colors`,
 * where the palette is discarded entirely (WCAG 2.2 SC 1.4.1).
 *
 * Everything is painted in `currentColor`. Dark-mode browser extensions
 * substitute `currentColor` reliably where they mangle `var()` inside SVG
 * paint, so the glyphs survive Dark Reader and friends intact.
 */
function glyphFor(state: CapabilityState): ReactNode {
  switch (state) {
    // Solid ring + tick. The only affirmative mark.
    case 'confirmed':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" />
          <path d="m5.25 8.2 1.95 1.95 3.6-4.1" />
        </>
      );
    // DASHED ring + centre dot. Provisional by construction: the broken
    // outline says the boundary of what we know is not closed.
    case 'unconfirmed':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" strokeDasharray="2.4 2.2" />
          <circle cx="8" cy="8" r="1.2" fill="currentColor" stroke="none" />
        </>
      );
    // Solid ring + bar. A settled negative. Deliberately NOT a cross: a cross
    // reads as an error, and "this peer does not do UDP" is not an error.
    case 'unsupported':
      return (
        <>
          <circle cx="8" cy="8" r="6.25" />
          <path d="M5.05 8h5.9" />
        </>
      );
    // Bare slash, no ring. Nothing is being asserted about the subject at all.
    case 'not-applicable':
      return <path d="M3.7 12.3 12.3 3.7" />;
  }
}

function normalise(value: CapabilityValue | undefined): CapabilityCell {
  if (value === undefined) return { state: 'unconfirmed' };
  if (typeof value === 'string') return { state: value };
  return value;
}

/**
 * A tri-state-plus capability matrix: subjects down, capabilities across.
 *
 * WHY FOUR STATES AND NOT A BOOLEAN
 * ---------------------------------
 * A boolean capability map forces every unknown into `false`, and `false`
 * renders as a red cross, and a red cross is read by an operator as "broken".
 * The information loss happens at the type level, long before the pixels: once
 * the data says `udp: false` there is no way for the UI to recover the
 * difference between "the peer said no" and "we never asked".
 *
 * So the matrix keeps them apart end to end. A capability absent from a
 * subject's record resolves to `unconfirmed` — it is never promoted to
 * `unsupported`, and `unsupported` is never demoted to `unconfirmed`. Neither
 * is painted in the danger role, because neither is a fault.
 *
 * ACCESSIBILITY
 * -------------
 * The grid is a real `<table>` with row and column headers, so a screen reader
 * announces "Peer B, QUIC, not confirmed" when the user lands on a cell. In
 * the compact layout the state word is visually hidden but still present in
 * the accessibility tree, so nothing is carried by the glyph alone.
 */
export const CapabilityGrid = forwardRef<HTMLDivElement, CapabilityGridProps>(
  function CapabilityGrid(
    {
      columns,
      subjects,
      layout = 'compact',
      size = 'md',
      label,
      scrollRegionLabel = 'Capability matrix',
      subjectHeader = 'Subject',
      stickySubjects = false,
      showLegend = true,
      legendLabel = 'Key',
      labelConfirmed = 'confirmed',
      labelUnconfirmed = 'not confirmed',
      labelUnsupported = 'not supported',
      labelNotApplicable = 'not applicable',
      descriptionConfirmed = 'Observed working.',
      descriptionUnconfirmed = 'No observation. May be unavailable, unprobed, irrelevant, or simply not reported.',
      descriptionUnsupported = 'Reported as not supported.',
      descriptionNotApplicable = 'Does not apply to this subject.',
      observedAtLabel = 'observed',
      locale,
      className,
      ...rest
    },
    ref,
  ) {
    const stateLabel: Record<CapabilityState, string> = {
      confirmed: labelConfirmed,
      unconfirmed: labelUnconfirmed,
      unsupported: labelUnsupported,
      'not-applicable': labelNotApplicable,
    };

    const stateDescription: Record<CapabilityState, string> = {
      confirmed: descriptionConfirmed,
      unconfirmed: descriptionUnconfirmed,
      unsupported: descriptionUnsupported,
      'not-applicable': descriptionNotApplicable,
    };

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="capability-grid"
        data-layout={layout}
        data-size={size}
        data-sticky={stickySubjects || undefined}
        className={clsx('stratum-capability-grid', className)}
      >
        {/* A capability matrix with more columns than fit scrolls sideways, and
          * nothing inside it is focusable — so without a tab stop the extra
          * columns are unreachable by keyboard (WCAG 2.1.1, axe
          * `scrollable-region-focusable`). `focus-inset` because an outline on a
          * scroll container is clipped by its own overflow.
          *
          * The role and the name are not optional extras. A bare `tabindex="0"`
          * on a generic div is a stop in the tab order that announces nothing,
          * and `aria-label` alone is prohibited on a generic element — the pair
          * is what makes the stop legible. Named from `label` when the grid has
          * one, so the region and the table's caption agree. */}
        <div
          className="stratum-capability-grid__scroll stratum-focus-inset"
          tabIndex={0}
          role="region"
          aria-label={label ?? scrollRegionLabel}
        >
          <table className="stratum-capability-grid__table">
            {label && <caption className="stratum-visually-hidden">{label}</caption>}
            <thead className="stratum-capability-grid__head">
              <tr className="stratum-capability-grid__row">
                <th scope="col" className="stratum-capability-grid__corner">
                  {subjectHeader}
                </th>
                {columns.map((column) => (
                  <th
                    key={column.id}
                    scope="col"
                    className="stratum-capability-grid__col-head"
                    title={column.description ?? column.label}
                  >
                    <span className="stratum-capability-grid__col-label">
                      {layout === 'compact' ? (column.abbr ?? column.label) : column.label}
                    </span>
                    {/* The abbreviation is never the only carrier of the
                     * column's identity — unless it is identical to the full
                     * label, in which case repeating it would make a screen
                     * reader say the header twice. */}
                    {layout === 'compact' && column.abbr && column.abbr !== column.label && (
                      <span className="stratum-visually-hidden">{column.label}</span>
                    )}
                    {layout === 'detailed' && column.description && (
                      <span className="stratum-capability-grid__col-desc">{column.description}</span>
                    )}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="stratum-capability-grid__body">
              {subjects.map((subject) => (
                <tr key={subject.id} className="stratum-capability-grid__row">
                  <th scope="row" className="stratum-capability-grid__row-head">
                    <span className="stratum-capability-grid__subject">{subject.label}</span>
                    {subject.detail && (
                      <span className="stratum-capability-grid__subject-detail">
                        {subject.detail}
                      </span>
                    )}
                  </th>
                  {columns.map((column) => {
                    const cell = normalise(subject.capabilities[column.id]);
                    const text = stateLabel[cell.state];
                    const when =
                      cell.observedAt != null
                        ? formatRelativeTime(cell.observedAt, Date.now(), locale)
                        : null;
                    const title = [
                      `${column.label}: ${text}`,
                      cell.note,
                      when ? `${observedAtLabel} ${when}` : null,
                    ]
                      .filter(Boolean)
                      .join(' · ');

                    return (
                      <td
                        key={column.id}
                        className="stratum-capability-grid__cell"
                        data-state={cell.state}
                        title={title}
                      >
                        <span className="stratum-capability-grid__cell-inner">
                          <svg
                            className="stratum-capability-grid__glyph"
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
                            {glyphFor(cell.state)}
                          </svg>
                          {layout === 'detailed' ? (
                            <span className="stratum-capability-grid__cell-text">{text}</span>
                          ) : (
                            <span className="stratum-visually-hidden">{text}</span>
                          )}
                          {cell.note && (
                            <span
                              className={
                                layout === 'detailed'
                                  ? 'stratum-capability-grid__cell-note'
                                  : 'stratum-visually-hidden'
                              }
                            >
                              {cell.note}
                            </span>
                          )}
                        </span>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {showLegend && (
          <div className="stratum-capability-grid__legend" role="group" aria-label={legendLabel}>
            <span className="stratum-capability-grid__legend-title">{legendLabel}</span>
            {STATE_ORDER.map((state) => (
              <span
                key={state}
                className="stratum-capability-grid__legend-item"
                data-state={state}
                title={stateDescription[state]}
              >
                <svg
                  className="stratum-capability-grid__glyph"
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
                  {glyphFor(state)}
                </svg>
                <span className="stratum-capability-grid__legend-label">{stateLabel[state]}</span>
                <span className="stratum-visually-hidden">. {stateDescription[state]}</span>
              </span>
            ))}
          </div>
        )}
      </div>
    );
  },
);
