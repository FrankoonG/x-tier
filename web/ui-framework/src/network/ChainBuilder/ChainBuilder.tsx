import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  type HTMLAttributes,
} from 'react';
import clsx from 'clsx';
import { Combobox } from '../../components/Combobox/Combobox';
import type { ComboboxOption } from '../../components/Combobox/Combobox';
import { InlineMessage } from '../../components/InlineMessage/InlineMessage';
import { useEventCallback } from '../../hooks/useEventCallback';
import { PathChain, type HopStatus, type PathHop } from '../PathChain/PathChain';
import {
  AddRowButton,
  InsertIcon,
  ListAnnouncer,
  RemoveIcon,
  useAnnouncer,
  useFocusRelay,
} from '../_shared/rowList';
import './ChainBuilder.css';

export interface ChainOption {
  value: string;
  /** Display text. Falls back to `value`. */
  label?: string;
  description?: string;
  /** Groups options under a heading in the picker. */
  group?: string;
  disabled?: boolean;
  /**
   * Why this option cannot be chosen. Rendered as the option's description, so
   * an unavailable step explains itself instead of being a greyed-out row.
   */
  disabledReason?: string;
  /** Health of this hop, used to colour the preview chain. */
  status?: HopStatus;
  /** Extra fact surfaced on the preview hop — latency, transport, address. */
  detail?: string;
}

/**
 * What the component knows about one step.
 *
 * `unknown` is not a synonym for invalid. It means the consumer returned no
 * options for that position, so the step could not be checked — a chain built
 * against a topology that has not loaded yet must not be reported as broken.
 */
export type ChainStepValidity = 'valid' | 'invalid' | 'unknown' | 'empty';

export interface ChainStepReport {
  index: number;
  value: string;
  validity: ChainStepValidity;
  /** Present when `validity` is `'invalid'`. */
  reason?: string;
}

export interface ChainBuilderProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  /** Ordered step values. An empty string is a step that has not been chosen. */
  steps: readonly string[];
  onStepsChange: (steps: string[]) => void;
  /**
   * Options legal at `index`, given every earlier value.
   *
   * `chainSoFar` is the literal prefix, including any value that is itself
   * invalid — the consumer owns the constraint logic and may well want to say
   * "nothing is reachable through that" by returning an empty array.
   */
  resolveOptions: (chainSoFar: readonly string[], index: number) => readonly ChainOption[];

  minSteps?: number;
  maxSteps?: number;
  size?: 'sm' | 'md';
  /** Accessible name for the step list. */
  label?: string;
  /** Separator in the preview chain. Default `'/'`. */
  separator?: string;
  showPreview?: boolean;
  previewMaxHops?: number;
  /** Fires on transitions only, not on every render. */
  onValidityChange?: (report: {
    valid: boolean;
    steps: ChainStepReport[];
    invalidIndexes: number[];
  }) => void;

  placeholder?: string;
  labelStep?: (position: number, total: number) => string;
  labelAdd?: string;
  labelInsert?: (position: number) => string;
  labelRemove?: (position: number) => string;
  labelEmptyStep?: string;
  labelInvalidStep?: (value: string) => string;
  labelUnknownStep?: string;
  labelUnavailableOption?: string;
  labelDownstream?: (count: number, position: number) => string;
  labelClearDownstream?: (count: number) => string;
  labelMaxReached?: (max: number) => string;
  labelMinReached?: (min: number) => string;
  labelEmptyChain?: string;
  labelPreview?: string;
  announceAdded?: (position: number, total: number) => string;
  announceRemoved?: (position: number, total: number) => string;
  announceCleared?: (count: number) => string;
}

/** Marker used for a step nobody has chosen yet. */
const UNSET_HOP = '?';

function optionLabel(option: ChainOption): string {
  return option.label ?? option.value;
}

/**
 * Composes an ordered chain in which each step CONSTRAINS the next.
 *
 * This is the generalised "route through A, then B, then C": an outbound chain,
 * a transport stack, a relay path. The consumer owns the constraint logic
 * through `resolveOptions`; the component owns the interaction.
 *
 * IT NEVER TRUNCATES
 * ------------------
 * The naive cascade — clear every later step whenever an earlier one changes —
 * is right for country/city and badly wrong here, where each later step may
 * have taken real thought. So a change upstream REVALIDATES what follows rather
 * than deleting it: every step keeps its value, is re-checked against the
 * options for its position, and is marked `valid`, `invalid` or `unknown`. When
 * something no longer applies the component says how many steps are affected and
 * offers one click to clear them. Silently dropping steps 3-5 because step 2
 * moved is a data loss the operator never authorised.
 *
 * `unknown` IS NOT `invalid`
 * --------------------------
 * A position for which the consumer returned no options has not been checked.
 * Reporting that as invalid would paint an entire chain red the moment a
 * topology query fails, which is exactly when an operator needs to read it.
 *
 * THERE IS NO REORDER
 * -------------------
 * Deliberately. In a constrained chain a step's legality depends on everything
 * before it, so dragging step 4 above step 2 does not produce a different valid
 * chain — it produces a different, usually invalid, one. Insert and remove are
 * the honest operations, and both are offered per position.
 *
 * THE PREVIEW IS THE CONTRACT
 * ---------------------------
 * The chain is rendered in the same monospace `PathChain` form used to MONITOR
 * a path elsewhere in the library, so the thing you compose looks like the thing
 * you later watch. Its tail-dye rule carries real meaning here: everything after
 * a broken or unchosen step is unreachable through this chain regardless of its
 * own health.
 */
export const ChainBuilder = forwardRef<HTMLDivElement, ChainBuilderProps>(function ChainBuilder(
  {
    steps,
    onStepsChange,
    resolveOptions,
    minSteps = 0,
    maxSteps,
    size = 'md',
    label,
    separator = '/',
    showPreview = true,
    previewMaxHops = 8,
    onValidityChange,
    placeholder = 'Choose a step',
    labelStep = (position, total) => `Step ${position} of ${total}`,
    labelAdd = 'Add step',
    labelInsert = (position) => `Insert a step after step ${position}`,
    labelRemove = (position) => `Remove step ${position}`,
    labelEmptyStep = 'Not chosen yet.',
    labelInvalidStep = (value) => `"${value}" is not available at this position.`,
    labelUnknownStep = 'Could not be checked — no options are available for this position.',
    labelUnavailableOption = 'Not available here',
    labelDownstream = (count, position) =>
      count === 1
        ? `1 later step depends on step ${position} and may no longer apply.`
        : `${count} later steps depend on step ${position} and may no longer apply.`,
    labelClearDownstream = (count) => `Clear ${count} later ${count === 1 ? 'step' : 'steps'}`,
    labelMaxReached = (max) => `Limit of ${max} steps reached.`,
    labelMinReached = (min) => `At least ${min} ${min === 1 ? 'step' : 'steps'} required.`,
    labelEmptyChain = 'No steps yet.',
    labelPreview = 'Resulting chain',
    announceAdded = (position, total) => `Step added at position ${position}. ${total} steps.`,
    announceRemoved = (position, total) =>
      `Step ${position} removed. ${total} ${total === 1 ? 'step' : 'steps'} remain.`,
    announceCleared = (count) => `${count} later ${count === 1 ? 'step' : 'steps'} cleared.`,
    className,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const total = steps.length;
  const { message, announce } = useAnnouncer();
  const { register, requestFocus } = useFocusRelay();

  /* Every step is resolved against the literal prefix. Nothing is dropped and
   * nothing is auto-corrected — the report is the whole output of this pass.
   * Wording is deliberately NOT resolved here: label props are recreated on
   * every render by their own defaults, and depending on them would defeat the
   * memo that stops `resolveOptions` running on unrelated renders. */
  const resolved = useMemo(() => {
    const out: {
      value: string;
      options: readonly ChainOption[];
      option: ChainOption | null;
      validity: ChainStepValidity;
    }[] = [];

    for (let i = 0; i < steps.length; i += 1) {
      const value = steps[i] ?? '';
      const options = resolveOptions(steps.slice(0, i), i);
      const option = options.find((o) => o.value === value) ?? null;

      const validity: ChainStepValidity =
        value === ''
          ? 'empty'
          : options.length === 0
            ? 'unknown'
            : !option || option.disabled
              ? 'invalid'
              : 'valid';

      out.push({ value, options, option, validity });
    }
    return out;
  }, [steps, resolveOptions]);

  /** Reason text for a step, resolved at render because it is caller wording. */
  const reasonFor = (step: (typeof resolved)[number]): string | undefined =>
    step.validity === 'invalid'
      ? (step.option?.disabledReason ?? labelInvalidStep(step.value))
      : undefined;

  /** First position that stops the chain being usable. */
  const breakAt = resolved.findIndex((s) => s.validity === 'invalid' || s.validity === 'empty');
  const downstreamCount = breakAt >= 0 ? total - breakAt - 1 : 0;

  /* -- Validity reporting, on transitions only ---------------------------- */
  const emitValidity = useEventCallback(onValidityChange);
  const lastSignature = useRef<string | null>(null);
  useEffect(() => {
    const report = resolved.map((s, index) => {
      const entry: ChainStepReport = { index, value: s.value, validity: s.validity };
      const why = s.validity === 'invalid' ? s.option?.disabledReason : undefined;
      if (why !== undefined) entry.reason = why;
      return entry;
    });
    const signature = report.map((r) => `${r.index}:${r.value}:${r.validity}`).join('|');
    if (lastSignature.current === signature) return;
    lastSignature.current = signature;
    emitValidity({
      valid: report.length > 0 && report.every((r) => r.validity === 'valid'),
      steps: report,
      invalidIndexes: report.filter((r) => r.validity === 'invalid').map((r) => r.index),
    });
  }, [resolved, emitValidity]);

  /* -- Mutations ----------------------------------------------------------- */

  const setStep = useCallback(
    (index: number, value: string) => {
      const next = steps.slice();
      next[index] = value;
      onStepsChange(next);
    },
    [steps, onStepsChange],
  );

  const insertAt = useCallback(
    (index: number) => {
      const next = steps.slice();
      next.splice(index, 0, '');
      onStepsChange(next);
      requestFocus(`step:${index}`);
      announce(announceAdded(index + 1, next.length));
    },
    [steps, onStepsChange, requestFocus, announce, announceAdded],
  );

  const removeAt = useCallback(
    (index: number) => {
      const next = steps.slice();
      next.splice(index, 1);
      // Focus the same control in the following step, else the previous one,
      // else the add plate. Never `<body>`.
      if (index < next.length) requestFocus(`step:${index}`);
      else if (next.length > 0) requestFocus(`step:${next.length - 1}`);
      else requestFocus('add');
      onStepsChange(next);
      announce(announceRemoved(index + 1, next.length));
    },
    [steps, onStepsChange, requestFocus, announce, announceRemoved],
  );

  const clearDownstream = useCallback(() => {
    if (breakAt < 0 || downstreamCount <= 0) return;
    onStepsChange(steps.slice(0, breakAt + 1));
    announce(announceCleared(downstreamCount));
    requestFocus(`step:${breakAt}`);
  }, [breakAt, downstreamCount, steps, onStepsChange, announce, announceCleared, requestFocus]);

  /* -- Preview ------------------------------------------------------------- */

  // Not memoised: a chain is a handful of steps, and the label props this reads
  // change identity on every render anyway, so a memo would never hit.
  const hops: PathHop[] = resolved.map((step, i) => {
    const broken = step.validity === 'invalid' || step.validity === 'empty';
    const hop: PathHop = {
      id: `${uid}hop${i}`,
      label: step.value === '' ? UNSET_HOP : optionLabel(step.option ?? { value: step.value }),
      // An unchosen or unavailable step is a hole in the path, and PathChain's
      // tail-dye then correctly reports everything after it as unreachable
      // through this chain — which is exactly true.
      status: broken ? 'broken' : (step.option?.status ?? 'unknown'),
    };
    const detail = reasonFor(step) ?? step.option?.detail;
    if (detail !== undefined) hop.detail = detail;
    return hop;
  });

  const atCeiling = maxSteps !== undefined && total >= maxSteps;
  const atFloor = total <= minSteps;

  /** Options handed to one picker, with the current value always present. */
  const pickerOptions = (index: number): ComboboxOption[] => {
    const step = resolved[index];
    if (!step) return [];
    const list: ComboboxOption[] = step.options.map((option) => {
      const entry: ComboboxOption = { value: option.value, label: optionLabel(option) };
      const description = option.disabled
        ? (option.disabledReason ?? labelUnavailableOption)
        : option.description;
      if (description !== undefined) entry.description = description;
      if (option.group !== undefined) entry.group = option.group;
      if (option.disabled) entry.disabled = true;
      return entry;
    });
    // A value that no longer resolves still has to be VISIBLE. Without this the
    // picker would render an empty field and the operator would see the step
    // vanish — the exact silent truncation this component refuses to do.
    if (step.value !== '' && !step.options.some((o) => o.value === step.value)) {
      list.unshift({
        value: step.value,
        label: step.value,
        description: reasonFor(step) ?? labelUnavailableOption,
        disabled: true,
      });
    }
    return list;
  };

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="chain-builder"
      data-size={size}
      className={clsx('stratum-chain-builder', className)}
    >
      {showPreview && (
        <div className="stratum-chain-builder__preview">
          <span className="stratum-chain-builder__preview-label">{labelPreview}</span>
          {total === 0 ? (
            <span className="stratum-chain-builder__empty">{labelEmptyChain}</span>
          ) : (
            <PathChain
              hops={hops}
              separator={separator}
              maxInlineHops={previewMaxHops}
              size={size === 'sm' ? 'sm' : 'md'}
            />
          )}
        </div>
      )}

      <ol
        role="list"
        className="stratum-row-list stratum-chain-builder__list"
        aria-label={label}
      >
        {resolved.map((step, index) => {
          const noteId = `${uid}note${index}`;
          const showNote = step.validity !== 'valid';
          const tone =
            step.validity === 'invalid' ? 'danger' : step.validity === 'empty' ? 'warning' : 'muted';
          const noteText =
            step.validity === 'invalid'
              ? (reasonFor(step) ?? labelInvalidStep(step.value))
              : step.validity === 'empty'
                ? labelEmptyStep
                : labelUnknownStep;

          return (
            <li
              key={`${uid}step${index}`}
              className="stratum-row-list__row stratum-chain-builder__row"
              aria-label={labelStep(index + 1, total)}
              data-validity={step.validity}
              // Everything after the first hole is unreachable through this
              // chain, which the row shows as well as the preview.
              data-downstream={breakAt >= 0 && index > breakAt ? 'true' : undefined}
            >
              <span className="stratum-row-list__index" aria-hidden="true">
                {index + 1}
              </span>

              <div className="stratum-row-list__main">
                <Combobox
                  ref={register(`step:${index}`)}
                  size={size}
                  fullWidth
                  options={pickerOptions(index)}
                  value={step.value === '' ? null : step.value}
                  onChange={(next) => setStep(index, next ?? '')}
                  placeholder={placeholder}
                  invalid={step.validity === 'invalid'}
                  aria-label={labelStep(index + 1, total)}
                  aria-describedby={showNote ? noteId : undefined}
                />
                {showNote && (
                  <p className="stratum-row-list__note" data-tone={tone} id={noteId}>
                    {noteText}
                  </p>
                )}
              </div>

              <div className="stratum-row-list__actions">
                <button
                  type="button"
                  className="stratum-row-list__action"
                  aria-label={labelInsert(index + 1)}
                  disabled={atCeiling}
                  onClick={() => insertAt(index + 1)}
                >
                  {InsertIcon}
                </button>
                <button
                  type="button"
                  className="stratum-row-list__action"
                  data-tone="danger"
                  aria-label={labelRemove(index + 1)}
                  disabled={atFloor}
                  aria-describedby={atFloor ? `${uid}floor` : undefined}
                  onClick={() => removeAt(index)}
                >
                  {RemoveIcon}
                </button>
              </div>
            </li>
          );
        })}
      </ol>

      {atFloor && minSteps > 0 && (
        <span id={`${uid}floor`} className="stratum-row-list__ceiling">
          {labelMinReached(minSteps)}
        </span>
      )}

      {downstreamCount > 0 && (
        <InlineMessage variant="warning" size="xs" role="status">
          <span className="stratum-chain-builder__downstream">
            {labelDownstream(downstreamCount, breakAt + 1)}
            <button
              type="button"
              className="stratum-chain-builder__clear"
              onClick={clearDownstream}
            >
              {labelClearDownstream(downstreamCount)}
            </button>
          </span>
        </InlineMessage>
      )}

      <AddRowButton
        label={labelAdd}
        buttonRef={register('add')}
        disabled={atCeiling}
        disabledReason={maxSteps !== undefined ? labelMaxReached(maxSteps) : undefined}
        onClick={() => insertAt(total)}
      />

      <ListAnnouncer message={message} />
    </div>
  );
});
