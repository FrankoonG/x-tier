/* ---------------------------------------------------------------------------
 * Shared plumbing for the network INPUT family.
 *
 * NOT exported from the package. AddressInput, PortRangeInput, CredentialField,
 * ProtocolPicker, DurationInput and ByteSizeInput all need the same three
 * things, and duplicating them six times is how a family drifts apart:
 *
 *   1. `joinIds`   — merging an externally supplied `aria-describedby` (which a
 *                    Field wrapper owns) with the ids the control generates for
 *                    its own message and hint. Losing the caller's id here is
 *                    the single most common way a design-system field breaks
 *                    the description a form library attached to it.
 *   2. ValidityMark — the non-colour channel for validity. Shapes come from the
 *                    framework's existing status glyphs so a valid address and
 *                    a success toast use the same silhouette.
 *   3. netField.css — one control shell, so a duration field and an address
 *                    field line up pixel-for-pixel in the same form row.
 *
 * VALIDITY VOCABULARY
 * -------------------
 * Four states, not two. `empty` is NOT `invalid`: a field the operator has not
 * filled in yet has not failed anything, and painting it red before they have
 * typed is the classic aggressive-validation mistake. `warning` is for input
 * that parses but is questionable — overlapping port ranges, a weak secret —
 * and never blocks submission on its own.
 * ------------------------------------------------------------------------- */
import clsx from 'clsx';
import { statusGlyph, type StatusTone } from '../../components/_shared/statusIcons';
import './netField.css';

export type ValidityState = 'valid' | 'invalid' | 'warning' | 'empty';

export type NetControlSize = 'sm' | 'md' | 'lg';

/**
 * Merges any number of id-list strings into one `aria-describedby` value,
 * dropping blanks and duplicates and preserving first-seen order.
 *
 * Returns `undefined` rather than `''` so the attribute is omitted entirely
 * when there is nothing to describe — an empty `aria-describedby` is treated
 * as a broken reference by several screen readers.
 */
export function joinIds(
  ...ids: Array<string | false | null | undefined>
): string | undefined {
  const seen: string[] = [];
  for (const group of ids) {
    if (!group) continue;
    for (const part of group.split(/\s+/)) {
      if (part && !seen.includes(part)) seen.push(part);
    }
  }
  return seen.length > 0 ? seen.join(' ') : undefined;
}

/**
 * What counts as an accessible name for one of these controls.
 *
 * A `placeholder` is deliberately NOT on the list. Several screen readers do
 * announce it when nothing better exists, but it disappears the moment the
 * operator types, it is not a name in the accessibility tree sense, and axe
 * reports a placeholder-only field as `label-title-only` for exactly that
 * reason. Neither is `title`: it is a tooltip, absent on touch and on keyboard.
 */
export interface NetFieldNameSources {
  /** `field.id` published by an enclosing `<Field>`. */
  fieldId: string | undefined;
  /** `field.labelId`, present only when that `<Field>` rendered a visible label. */
  fieldLabelId: string | undefined;
  ariaLabel: string | undefined;
  ariaLabelledBy: string | undefined;
  /**
   * An `id` the caller passed explicitly. Treated as a name source because the
   * usual reason to pin an id on one of these controls is to point a
   * hand-written `<label for>` at it, and warning there would be a false alarm.
   */
  explicitId: string | undefined;
}

export function hasNetFieldName(sources: NetFieldNameSources): boolean {
  if (sources.ariaLabel !== undefined && sources.ariaLabel !== '') return true;
  if (sources.ariaLabelledBy !== undefined && sources.ariaLabelledBy !== '') return true;
  if (sources.fieldId !== undefined && sources.fieldLabelId !== undefined) return true;
  if (sources.explicitId !== undefined) return true;
  return false;
}

/**
 * Dev-mode guard for the one failure a network form cannot recover from: an
 * input nobody named. Same shape as the `<Button iconOnly>` guard — loud in
 * development, absent from the production bundle.
 */
export function warnMissingNetFieldLabel(component: string, sources: NetFieldNameSources): void {
  if (!import.meta.env?.DEV) return;
  if (hasNetFieldName(sources)) return;
  console.error(
    `[stratum] <${component}> rendered an input with no accessible name. ` +
      'Wrap it in `<Field label="…">`, or pass `aria-label` / `aria-labelledby`. ' +
      'A `placeholder` is not a label — it is not exposed as a name and it ' +
      'disappears as soon as the operator types.',
  );
}

const TONE_FOR_STATE: Record<ValidityState, StatusTone> = {
  valid: 'success',
  invalid: 'danger',
  warning: 'warning',
  empty: 'neutral',
};

export interface ValidityMarkProps {
  state: ValidityState;
  /**
   * Accessible name. Omit to render the mark decoratively, which is correct
   * whenever the message text below the control already carries the meaning —
   * announcing "invalid" twice is noise.
   */
  label?: string | undefined;
  className?: string | undefined;
}

/**
 * Validity glyph.
 *
 * Each state has a distinct SILHOUETTE (tick in a circle, cross in an octagon,
 * bar in a triangle, dash in a circle) rather than four recolourings of one
 * shape, so validity survives both colour-vision deficiency and
 * `forced-colors: active`, where the fill colour is discarded outright.
 */
export function ValidityMark({ state, label, className }: ValidityMarkProps) {
  return (
    <span
      data-stratum="validity-mark"
      data-state={state}
      className={clsx('stratum-net-validity', className)}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {statusGlyph(TONE_FOR_STATE[state])}
    </span>
  );
}

/**
 * Resolves the `aria-invalid` value a control should publish.
 *
 * An explicit prop from a Field wrapper always wins — the wrapper may know
 * about a server-side failure the control cannot see — and `empty` never
 * reports invalid on its own.
 */
export function resolveAriaInvalid(
  state: ValidityState,
  override: boolean | 'true' | 'false' | 'grammar' | 'spelling' | undefined,
): boolean | 'true' | 'false' | 'grammar' | 'spelling' | undefined {
  if (override !== undefined) return override;
  return state === 'invalid' ? true : undefined;
}
