import {
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useId,
  useMemo,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { ScrollArea } from '../ScrollArea/ScrollArea';
import type { ScrollAreaOrientation } from '../ScrollArea/ScrollArea';
import { cardRoleAllowsAutomaticLabel } from './cardA11y';
import './Card.css';

export type CardVariant = 'elevated' | 'outlined' | 'ghost';
export type CardPadding = 'none' | 'xs' | 'sm' | 'md' | 'lg';
export type CardHeadingLevel = 1 | 2 | 3 | 4 | 5 | 6;

interface CardContextValue {
  titleId: string;
  hasTitle: boolean;
  padding: CardPadding;
  variant: CardVariant;
  fill: boolean;
  /** Lets the header tell the card whether a title exists to point at. */
  registerTitle: (present: boolean) => void;
}

const CardContext = createContext<CardContextValue | null>(null);

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  variant?: CardVariant;
  /** Default inline padding for every section. Sections may override it. */
  padding?: CardPadding;
  /**
   * Stretches the card to its parent's height and makes `Card.Body` the
   * scrolling region. Header and footer stay pinned, which is what a dense
   * table inside a card needs.
   */
  fill?: boolean;
}

/**
 * A bounded surface with optional header, body and footer.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - The card does not claim a landmark role. `role="region"` on every card in
 *   a dashboard floods the landmark list and makes the genuinely important
 *   regions unfindable. Once a consumer supplies a semantic role, the card
 *   wires `aria-labelledby` to `Card.Header`'s title automatically. Generic
 *   cards remain unnamed because ARIA naming is prohibited on that role.
 * - `fill` puts the scroll on the body rather than the card, so the focus
 *   outline of a control in the header is never clipped by the scroll box.
 */
const CardRoot = forwardRef<HTMLDivElement, CardProps>(function Card(
  { variant = 'elevated', padding = 'md', fill = false, className, children, ...rest },
  ref,
) {
  const titleId = useId();
  const [hasTitle, setHasTitle] = useState(false);

  const context = useMemo<CardContextValue>(
    () => ({ titleId, hasTitle, padding, variant, fill, registerTitle: setHasTitle }),
    [titleId, hasTitle, padding, variant, fill],
  );

  return (
    <CardContext.Provider value={context}>
      <div
        // Placed before the spread so a consumer-supplied label always wins.
        aria-labelledby={
          hasTitle && cardRoleAllowsAutomaticLabel(rest.role) ? titleId : undefined
        }
        {...rest}
        ref={ref}
        data-stratum="card"
        data-variant={variant}
        data-padding={padding}
        data-fill={fill || undefined}
        className={clsx('stratum-card', className)}
      >
        {children}
      </div>
    </CardContext.Provider>
  );
});

export interface CardHeaderProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  title?: ReactNode;
  description?: ReactNode;
  /** Trailing controls. Kept out of the heading so it stays a clean label. */
  actions?: ReactNode;
  /** Heading rank. Pick the one that fits the surrounding outline. */
  headingLevel?: CardHeadingLevel;
  /** Rule between the header and the body. Defaults on unless the card is ghost. */
  divider?: boolean;
  padding?: CardPadding;
}

export const CardHeader = forwardRef<HTMLDivElement, CardHeaderProps>(function CardHeader(
  {
    title,
    description,
    actions,
    headingLevel = 3,
    divider,
    padding,
    className,
    children,
    ...rest
  },
  ref,
) {
  const context = useContext(CardContext);
  const register = context?.registerTitle;
  const hasTitle = title != null && title !== false;

  useEffect(() => {
    if (!register) return;
    register(hasTitle);
    return () => register(false);
  }, [register, hasTitle]);

  const Heading = `h${headingLevel}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';
  const showDivider = divider ?? context?.variant !== 'ghost';

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="card-header"
      data-divider={showDivider || undefined}
      data-padding={padding}
      className={clsx('stratum-card__header', className)}
    >
      <div className="stratum-card__header-text">
        {hasTitle && (
          <Heading id={context?.titleId} className="stratum-card__title">
            {title}
          </Heading>
        )}
        {description != null && description !== false && (
          <p className="stratum-card__description">{description}</p>
        )}
        {children}
      </div>
      {actions != null && actions !== false && (
        <div className="stratum-card__actions">{actions}</div>
      )}
    </div>
  );
});

export interface CardBodyProps extends HTMLAttributes<HTMLDivElement> {
  padding?: CardPadding;
  /**
   * Makes the body the scrolling region. Defaults to `true` inside a `fill`
   * card and `false` otherwise.
   *
   * Scrolling is delegated to `ScrollArea` rather than to a bare
   * `overflow: auto`, because a scroll container that is not focusable cannot
   * be scrolled by keyboard in Chromium at all — WCAG 2.1.1, and the failure
   * mode is that the last rows of a table in a card are simply unreachable
   * without a mouse. `ScrollArea` adds the tab stop only when the content
   * genuinely overflows, and brings the edge affordances with it.
   */
  scroll?: boolean;
  /** Axes owned by the generated scroll region. Defaults to `vertical`. */
  scrollOrientation?: ScrollAreaOrientation;
  /**
   * Accessible name for the scroll region, used only when the card has no
   * title to borrow. Default `'Content'`.
   */
  scrollLabel?: string;
}

export const CardBody = forwardRef<HTMLDivElement, CardBodyProps>(function CardBody(
  {
    padding,
    scroll,
    scrollOrientation = 'vertical',
    scrollLabel = 'Content',
    className,
    children,
    ...rest
  },
  ref,
) {
  const context = useContext(CardContext);
  const isScrolling = scroll ?? context?.fill ?? false;
  const labelledBy = context?.hasTitle ? context.titleId : undefined;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="card-body"
      data-padding={padding}
      data-scroll={isScrolling || undefined}
      className={clsx('stratum-card__body', className)}
    >
      {isScrolling ? (
        <ScrollArea
          className="stratum-card__scroll"
          orientation={scrollOrientation}
          {...(labelledBy ? { labelledBy } : { label: scrollLabel })}
        >
          {children}
        </ScrollArea>
      ) : (
        children
      )}
    </div>
  );
});

export interface CardFooterProps extends HTMLAttributes<HTMLDivElement> {
  /** Rule between the body and the footer. Defaults on unless the card is ghost. */
  divider?: boolean;
  padding?: CardPadding;
  /** Horizontal placement of the footer content. */
  align?: 'start' | 'center' | 'end' | 'between';
}

export const CardFooter = forwardRef<HTMLDivElement, CardFooterProps>(function CardFooter(
  { divider, padding, align = 'end', className, children, ...rest },
  ref,
) {
  const context = useContext(CardContext);
  const showDivider = divider ?? context?.variant !== 'ghost';

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="card-footer"
      data-divider={showDivider || undefined}
      data-padding={padding}
      data-align={align}
      className={clsx('stratum-card__footer', className)}
    >
      {children}
    </div>
  );
});

export const Card = Object.assign(CardRoot, {
  Header: CardHeader,
  Body: CardBody,
  Footer: CardFooter,
});
