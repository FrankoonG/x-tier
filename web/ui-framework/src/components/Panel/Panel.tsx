import {
  forwardRef,
  useId,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useMeasure } from '../../hooks/useMeasure';
import './Panel.css';

export type PanelVariant = 'outlined' | 'sunken' | 'plain';
export type PanelPadding = 'none' | 'xs' | 'sm' | 'md';
export type PanelHeadingLevel = 1 | 2 | 3 | 4 | 5 | 6;

export interface PanelProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  title?: ReactNode;
  description?: ReactNode;
  /** Trailing controls, rendered outside the collapse trigger. */
  actions?: ReactNode;
  variant?: PanelVariant;
  padding?: PanelPadding;
  headingLevel?: PanelHeadingLevel;
  /** Turns the header into a disclosure trigger. */
  collapsible?: boolean;
  /** Controlled open state. */
  open?: boolean;
  /** Uncontrolled initial open state. */
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Accessible name for the trigger when `title` is not plain text. */
  toggleLabel?: string;
  /** Rule between the header and the body. */
  divider?: boolean;
}

/**
 * A lighter grouping surface than `Card`, with an optional disclosure header.
 *
 * MOTION
 * ------
 * The collapse animates `height` from a measured value rather than from
 * `auto`, which does not interpolate. The measurement comes from a
 * `ResizeObserver` on the body, so a panel whose content grows while open
 * stays the right height without any imperative re-measure.
 *
 * `visibility` is transitioned alongside it with a zero-duration transition
 * and a delay equal to the height duration. That keeps the content in the
 * accessibility tree and the tab order for the whole opening animation, and
 * removes it from both only once the closing animation has finished — a
 * `height: 0; overflow: hidden` box on its own still contains focusable
 * children, which is a real keyboard trap.
 *
 * The first render never animates: `data-animate` only appears after the first
 * user toggle, so a panel that mounts closed does not play a collapse.
 */
export const Panel = forwardRef<HTMLDivElement, PanelProps>(function Panel(
  {
    title,
    description,
    actions,
    variant = 'outlined',
    padding = 'sm',
    headingLevel = 3,
    collapsible = false,
    open,
    defaultOpen = true,
    onOpenChange,
    toggleLabel,
    divider,
    className,
    children,
    ...rest
  },
  ref,
) {
  const baseId = useId();
  const triggerId = `${baseId}-trigger`;
  const contentId = `${baseId}-content`;

  const [openState, setOpen] = useControllableState<boolean>({
    value: open,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });
  // A non-collapsible panel is always open, but the state above stays intact
  // so toggling `collapsible` never strands it or trips the controlled/
  // uncontrolled warning.
  const isOpen = collapsible ? openState : true;

  // Gates the height transition. State rather than a ref so nothing is
  // mutated during render, and it flips on the first real interaction.
  const [hasToggled, setHasToggled] = useState(false);

  const { ref: bodyRef, size } = useMeasure<HTMLDivElement>();

  const Heading = `h${headingLevel}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';
  const hasTitle = title != null && title !== false;
  const hasDescription = description != null && description !== false;
  const hasActions = actions != null && actions !== false;
  const hasHeader = hasTitle || hasDescription || hasActions || collapsible;
  const showDivider = divider ?? (variant !== 'plain' && hasHeader);

  if (import.meta.env?.DEV && collapsible && !hasTitle && !toggleLabel) {
    console.error(
      '[stratum] <Panel collapsible> without `title` requires `toggleLabel`. ' +
        'The chevron is aria-hidden, so the trigger has no accessible name.',
    );
  }

  // The collapse only claims a landmark when it can also be named. An unnamed
  // `role="region"` on every collapsible panel floods the landmark list, which
  // is the same reason Card refuses the role outright.
  const regionNamed = hasTitle || Boolean(toggleLabel);

  const collapseStyle: CSSProperties & Record<string, string | number> = {};
  if (size) collapseStyle['--_content-h'] = `${size.height}px`;

  const titleNode = hasTitle ? <span className="stratum-panel__title">{title}</span> : null;

  const trigger = (
    <button
      type="button"
      id={triggerId}
      className="stratum-panel__trigger"
      aria-expanded={isOpen}
      aria-controls={contentId}
      aria-label={hasTitle ? undefined : toggleLabel}
      onClick={() => {
        setHasToggled(true);
        setOpen(!isOpen);
      }}
    >
      <span className="stratum-panel__chevron" aria-hidden="true">
        <svg viewBox="0 0 14 14" fill="none" focusable="false">
          <path
            d="M3.25 5.25 7 9l3.75-3.75"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
      {titleNode}
    </button>
  );

  const body = (
    <div ref={bodyRef} className="stratum-panel__body">
      {children}
    </div>
  );

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="panel"
      data-variant={variant}
      data-padding={padding}
      data-collapsible={collapsible || undefined}
      data-state={isOpen ? 'open' : 'closed'}
      className={clsx('stratum-panel', className)}
    >
      {hasHeader && (
        <div className="stratum-panel__header" data-divider={showDivider || undefined}>
          {collapsible ? (
            // Only wrapped in a heading when there is title text — a heading
            // whose sole content is the aria-hidden chevron is an empty entry
            // in the document outline.
            hasTitle ? (
              <Heading className="stratum-panel__heading">{trigger}</Heading>
            ) : (
              trigger
            )
          ) : (
            hasTitle && <Heading className="stratum-panel__heading">{titleNode}</Heading>
          )}

          {hasDescription && <p className="stratum-panel__description">{description}</p>}

          {hasActions && <div className="stratum-panel__actions">{actions}</div>}
        </div>
      )}

      {collapsible ? (
        <div
          id={contentId}
          role={regionNamed ? 'region' : undefined}
          aria-labelledby={regionNamed ? triggerId : undefined}
          className="stratum-panel__collapse"
          data-state={isOpen ? 'open' : 'closed'}
          data-animate={hasToggled || undefined}
          style={collapseStyle}
        >
          {body}
        </div>
      ) : (
        body
      )}
    </div>
  );
});
