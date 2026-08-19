import {
  createElement,
  forwardRef,
  useId,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { usePresence } from '../../hooks/usePresence';
import './Disclosure.css';

export type DisclosureSize = 'sm' | 'md' | 'lg';
export type DisclosureVariant = 'plain' | 'contained';
export type DisclosureHeadingLevel = 1 | 2 | 3 | 4 | 5 | 6 | 'none';
export type DisclosureIndicatorPlacement = 'start' | 'end';

export interface DisclosureProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'onChange'> {
  /** Header content. Any node — it is placed inside the trigger button. */
  title: ReactNode;
  /** Secondary line under the title. */
  description?: ReactNode;
  /** Trailing header adornment: a count, a status dot, a timestamp. */
  meta?: ReactNode;
  /** Leading header adornment. Decorative. */
  icon?: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  disabled?: boolean;
  size?: DisclosureSize;
  variant?: DisclosureVariant;
  /**
   * Heading element wrapping the trigger. A disclosure that is part of a
   * document outline needs one; a standalone toggle inside a toolbar does not,
   * hence `'none'`.
   */
  headingLevel?: DisclosureHeadingLevel;
  indicatorPlacement?: DisclosureIndicatorPlacement;
  /** Replaces the chevron. Rotation is applied by CSS either way. */
  indicator?: ReactNode;
  /** Remove the content from the DOM once the collapse animation has finished. */
  unmountOnClose?: boolean;
  /**
   * `region` is the APG recommendation, but every region is a landmark, and a
   * page with twenty of them is materially harder to navigate than one with
   * none. Set `'none'` past roughly half a dozen on a screen.
   */
  contentRole?: 'region' | 'none';
  children?: ReactNode;
}

/* `fill="none"` sits on the path, not on the <svg>: the reset layer declares
 * `svg { fill: currentColor }`, and author CSS beats a presentational attribute
 * on the root, so a stroked icon relying on the root attribute floods. */
const ChevronIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <path
      d="m6 4 4 4-4 4"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

/**
 * A single expand/collapse region. `Accordion` is a coordinator around this.
 *
 * WHY grid-template-rows AND NOT A MEASURED HEIGHT
 * ------------------------------------------------
 * The content is wrapped in a grid whose single row animates `0fr -> 1fr`.
 * The alternative — `useMeasure` to read the natural height, then transition
 * `height` from `0` to that pixel value — was rejected for three reasons:
 *
 *   1. The pixel target goes stale. Content that reflows while open (a lazy
 *      image, a font swap, a live row appended) leaves the container clamped to
 *      a height that no longer matches, and the fix is another observer feeding
 *      another write.
 *   2. It needs a `height: auto` handoff at the end of the transition to stay
 *      correct, and that handoff is where interrupted animations break.
 *   3. It writes to the DOM from JS on every open, which is a forced reflow in
 *      the middle of a user interaction.
 *
 * The grid version has no measurement, no JS in the animation path, is
 * interruptible mid-flight, and tracks content that changes size while open.
 *
 * This does animate a layout property, which the framework otherwise forbids.
 * The exception is deliberate and bounded: an accordion has no other honest
 * mechanism, the work is confined to one subtree, and the alternative
 * (`max-height` to a guessed ceiling) produces a visibly wrong easing curve
 * whenever the guess is off.
 *
 * `visibility` is transitioned alongside it so collapsed content leaves the tab
 * order and the accessibility tree — and because `visibility` interpolates
 * asymmetrically (immediate to `visible`, deferred to `hidden`) it does that at
 * exactly the right moment in each direction, with no JS timer.
 */
export const Disclosure = forwardRef<HTMLDivElement, DisclosureProps>(function Disclosure(
  {
    title,
    description,
    meta,
    icon,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    disabled = false,
    size = 'md',
    variant = 'plain',
    headingLevel = 3,
    indicatorPlacement = 'end',
    indicator,
    unmountOnClose = false,
    contentRole = 'region',
    id,
    className,
    children,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const baseId = id ?? `stratum-disclosure-${reactId}`;
  const triggerId = `${baseId}-trigger`;
  const contentId = `${baseId}-content`;

  const [open, setOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });

  // usePresence waits on the real grid-row transition via getAnimations(), so
  // the unmount lands exactly when the collapse finishes and the duration is
  // never duplicated in JS. Pinned to `true` when content is kept mounted, so
  // the hook settles once at mount instead of cycling on every toggle.
  const presence = usePresence(unmountOnClose ? open : true);
  const isMounted = unmountOnClose ? presence.isPresent : true;
  const contentState = unmountOnClose ? presence.state : open ? 'open' : 'closed';

  const trigger = (
    <button
      type="button"
      id={triggerId}
      data-stratum="disclosure-trigger"
      data-size={size}
      data-indicator-placement={indicatorPlacement}
      data-disabled={disabled || undefined}
      aria-expanded={open}
      aria-controls={contentId}
      aria-disabled={disabled || undefined}
      className="stratum-disclosure__trigger stratum-focus-inset"
      onClick={(event) => {
        if (disabled) {
          event.preventDefault();
          return;
        }
        setOpen(!open);
      }}
    >
      <span className="stratum-disclosure__indicator" aria-hidden="true">
        {indicator ?? <ChevronIcon />}
      </span>
      {icon && (
        <span className="stratum-disclosure__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      <span className="stratum-disclosure__text">
        <span className="stratum-disclosure__title">{title}</span>
        {description != null && description !== false && (
          <span className="stratum-disclosure__description">{description}</span>
        )}
      </span>
      {meta != null && meta !== false && <span className="stratum-disclosure__meta">{meta}</span>}
    </button>
  );

  return (
    <div
      {...rest}
      ref={ref}
      id={id}
      data-stratum="disclosure"
      data-state={open ? 'open' : 'closed'}
      data-size={size}
      data-variant={variant}
      data-disabled={disabled || undefined}
      className={clsx('stratum-disclosure', className)}
    >
      {headingLevel === 'none' ? (
        trigger
      ) : (
        createElement(
          `h${headingLevel}`,
          { className: 'stratum-disclosure__heading' },
          trigger,
        )
      )}

      {isMounted && (
        <div
          id={contentId}
          ref={unmountOnClose ? presence.ref : undefined}
          role={contentRole === 'region' ? 'region' : undefined}
          aria-labelledby={contentRole === 'region' ? triggerId : undefined}
          data-stratum="disclosure-content"
          data-state={contentState}
          data-size={size}
          className="stratum-disclosure__content"
        >
          {/* Clip carries the overflow; padding lives on the body so the row
           * can actually reach 0fr. Padding on the clip would leave a stripe. */}
          <div className="stratum-disclosure__clip">
            <div className="stratum-disclosure__body">{children}</div>
          </div>
        </div>
      )}
    </div>
  );
});
