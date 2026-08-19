import {
  forwardRef,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FieldsetHTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import './Fieldset.css';

const useIsomorphicLayoutEffect = typeof window === 'undefined' ? useEffect : useLayoutEffect;

export type FieldsetSize = 'sm' | 'md' | 'lg';
export type FieldsetVariant = 'plain' | 'card';

export interface FieldsetProps
  extends Omit<FieldsetHTMLAttributes<HTMLFieldSetElement>, 'onChange'> {
  /** Group name. Rendered in a real `<legend>`, which names the group. */
  legend?: ReactNode;
  /** Help text for the group as a whole. */
  description?: ReactNode;
  variant?: FieldsetVariant;
  size?: FieldsetSize;
  /** Turns the legend into a disclosure button. */
  collapsible?: boolean;
  /** Controlled open state. */
  open?: boolean;
  /** Initial open state when uncontrolled. */
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * Native fieldset disabling. Disables every control inside in one attribute
   * — including ones the framework did not render.
   */
  disabled?: boolean;
  children?: ReactNode;
}

const Chevron = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" fill="none" focusable="false" aria-hidden="true">
    <path
      d="m6 4 4 4-4 4"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

function has(node: ReactNode): boolean {
  return node != null && node !== false && node !== '';
}

/**
 * A native `<fieldset>` and `<legend>`, optionally collapsible.
 *
 * WHY NATIVE
 * ----------
 * `<fieldset disabled>` disables every control it contains, including ones
 * this framework knows nothing about, and no ARIA pattern reproduces that.
 * `<legend>` is also the only element that names a group without needing an
 * `aria-labelledby` that a consumer can forget to keep in sync.
 *
 * COLLAPSING WITHOUT UNMOUNTING
 * -----------------------------
 * The panel keeps its children mounted and animates
 * `grid-template-rows: 1fr → 0fr`, which is the only way to transition to
 * intrinsic height in CSS. Unmounting would throw away every value a user had
 * typed the moment a section was collapsed, and a fixed `max-height` would
 * either clip a long section or make a short one animate through empty space.
 *
 * Collapsed content is taken out of the tab order and the accessibility tree
 * with `visibility: hidden`, applied on a zero-length transition delayed by
 * the collapse duration so it lands only once the panel has finished closing.
 * `visibility` is used rather than `inert` because `inert` is spelled
 * differently in React 18 and 19 and would need a version check to set safely.
 */
export const Fieldset = forwardRef<HTMLFieldSetElement, FieldsetProps>(function Fieldset(
  {
    legend,
    description,
    variant = 'plain',
    size = 'md',
    collapsible = false,
    open: openProp,
    defaultOpen = true,
    onOpenChange,
    disabled = false,
    className,
    children,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const panelId = `${uid}panel`;
  const descriptionId = `${uid}description`;

  const [open, setOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });

  const hasLegend = has(legend);
  const hasDescription = has(description);

  // The disclosure control lives inside the <legend>, so without one there is
  // nothing to expand the section with.
  if (import.meta.env?.DEV && collapsible && !hasLegend) {
    console.error(
      '[stratum] <Fieldset collapsible> requires `legend` — the disclosure control is rendered inside the <legend>, so without one the section can never be expanded.',
    );
  }

  // A non-collapsible fieldset is always open, but the state is still tracked
  // so that toggling `collapsible` at runtime does not flip the hook between
  // controlled and uncontrolled. A collapsible fieldset with no legend is also
  // forced open: collapsing it would strand its children behind a toggle that
  // was never rendered, unreachable by pointer, keyboard and assistive tech.
  const isOpen = collapsible && hasLegend ? open : true;

  const panelRef = useRef<HTMLDivElement | null>(null);
  const [settled, setSettled] = useState(isOpen);

  /* `overflow: hidden` on the panel is what makes the collapse clip, but left
   * in place while open it also clips the focus ring of the first and last
   * control inside, and any popover a control opens. So it is lifted once the
   * open transition has actually finished — read off the real animations
   * rather than a duplicated duration, exactly as usePresence does, so the
   * reduced-motion durations are honoured for free.
   *
   * A layout effect, not a passive one: `setSettled(false)` has to land before
   * the browser paints the first frame of the collapse, or the panel paints at
   * least once with the row already shrinking while the clip is still lifted. */
  useIsomorphicLayoutEffect(() => {
    if (!isOpen) {
      setSettled(false);
      return;
    }

    let cancelled = false;
    const frame = requestAnimationFrame(() => {
      const node = panelRef.current;
      const running = node?.getAnimations
        ? node.getAnimations().filter((a) => a.playState === 'running')
        : [];

      if (running.length === 0) {
        if (!cancelled) setSettled(true);
        return;
      }

      void Promise.allSettled(running.map((a) => a.finished)).then(() => {
        if (!cancelled) setSettled(true);
      });
    });

    return () => {
      cancelled = true;
      cancelAnimationFrame(frame);
    };
  }, [isOpen]);

  const legendContent = (
    <>
      {collapsible && (
        <span className="stratum-fieldset__chevron" aria-hidden="true">
          <Chevron />
        </span>
      )}
      <span className="stratum-fieldset__legend-text">{legend}</span>
    </>
  );

  return (
    <fieldset
      {...rest}
      ref={ref}
      disabled={disabled}
      data-stratum="fieldset"
      data-variant={variant}
      data-size={size}
      data-collapsible={collapsible || undefined}
      data-has-header={hasLegend || hasDescription || undefined}
      data-state={isOpen ? 'open' : 'closed'}
      className={clsx('stratum-fieldset', className)}
      // Merged rather than assigned: written plainly after the spread, the
      // `undefined` branch deletes a consumer's own `aria-describedby`, and the
      // populated branch replaces it. Multiple `aria-describedby` attributes do
      // not combine, so the id list has to be built here.
      aria-describedby={clsx(hasDescription && descriptionId, rest['aria-describedby']) || undefined}
    >
      {hasLegend && (
        <legend className="stratum-fieldset__legend">
          {collapsible ? (
            // A control inside the first legend of a disabled fieldset is
            // deliberately NOT disabled by the spec, so a disabled section can
            // still be collapsed and expanded to read it.
            <button
              type="button"
              className="stratum-fieldset__toggle"
              aria-expanded={isOpen}
              aria-controls={panelId}
              onClick={() => setOpen(!isOpen)}
            >
              {legendContent}
            </button>
          ) : (
            legendContent
          )}
        </legend>
      )}

      {hasDescription && (
        <div className="stratum-fieldset__description" id={descriptionId}>
          {description}
        </div>
      )}

      <div
        ref={panelRef}
        id={panelId}
        className="stratum-fieldset__panel"
        data-state={isOpen ? 'open' : 'closed'}
        data-settled={settled || undefined}
      >
        <div className="stratum-fieldset__panel-inner">{children}</div>
      </div>
    </fieldset>
  );
});
