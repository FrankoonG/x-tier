import {
  createContext,
  forwardRef,
  useCallback,
  useContext,
  useMemo,
  useRef,
  type HTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import {
  Disclosure,
  type DisclosureHeadingLevel,
  type DisclosureIndicatorPlacement,
  type DisclosureSize,
} from '../Disclosure/Disclosure';
import './Accordion.css';

export type AccordionType = 'single' | 'multiple';
export type AccordionVariant = 'plain' | 'contained' | 'separated';
export type AccordionSize = DisclosureSize;

interface AccordionBaseProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue'> {
  size?: AccordionSize;
  variant?: AccordionVariant;
  headingLevel?: DisclosureHeadingLevel;
  indicatorPlacement?: DisclosureIndicatorPlacement;
  /** Remove item content from the DOM once its collapse animation finishes. */
  unmountOnClose?: boolean;
  /**
   * `region` follows APG, but each one is a landmark. Past roughly six on a
   * screen the landmark list becomes the harder thing to navigate.
   */
  contentRole?: 'region' | 'none';
  /** Disables every item at once. Individual items can still opt in. */
  disabled?: boolean;
  children?: ReactNode;
}

export interface AccordionSingleProps extends AccordionBaseProps {
  type?: 'single';
  value?: string | null;
  defaultValue?: string | null;
  onValueChange?: (value: string | null) => void;
  /** Whether clicking the open item closes it. Defaults to `true`. */
  collapsible?: boolean;
}

export interface AccordionMultipleProps extends AccordionBaseProps {
  type: 'multiple';
  value?: string[];
  defaultValue?: string[];
  onValueChange?: (value: string[]) => void;
}

export type AccordionProps = AccordionSingleProps | AccordionMultipleProps;

/** The union collapsed to one shape for the implementation body. */
type AccordionResolvedProps = AccordionBaseProps & {
  type?: AccordionType;
  value?: string | string[] | null;
  defaultValue?: string | string[] | null;
  onValueChange?: ((value: string | null) => void) | ((value: string[]) => void);
  collapsible?: boolean;
};

interface AccordionContextValue {
  isOpen: (value: string) => boolean;
  toggle: (value: string) => void;
  size: AccordionSize;
  variant: AccordionVariant;
  headingLevel: DisclosureHeadingLevel;
  indicatorPlacement: DisclosureIndicatorPlacement;
  unmountOnClose: boolean;
  contentRole: 'region' | 'none';
  disabled: boolean;
}

const AccordionContext = createContext<AccordionContextValue | null>(null);

function useAccordionContext(component: string): AccordionContextValue {
  const context = useContext(AccordionContext);
  if (!context) {
    throw new Error(`[stratum] <${component}> must be rendered inside <Accordion>.`);
  }
  return context;
}

const toArray = (value: string | string[] | null | undefined): string[] =>
  value == null ? [] : Array.isArray(value) ? value : [value];

/* ========================================================================== */
/* Accordion                                                                   */
/* ========================================================================== */

/**
 * A coordinated set of `Disclosure`s.
 *
 * `type="single"` exposes `value: string | null`, `type="multiple"` exposes
 * `value: string[]`. Internally both are a string array; the single case simply
 * never holds more than one entry, which keeps the toggle logic to four lines
 * instead of two parallel implementations that drift.
 *
 * KEYBOARD
 * --------
 * Up/Down move between headers and wrap, Home/End jump to the ends — the APG
 * optional-but-expected behaviour. It is delegated from the root rather than
 * bound per item so that dynamically added items need no registration, and
 * headers belonging to a nested accordion are filtered out by ownership rather
 * than by depth, which is the case a `querySelectorAll` alone gets wrong.
 */
export const Accordion = forwardRef<HTMLDivElement, AccordionProps>(function Accordion(
  props,
  ref,
) {
  const {
    type = 'single',
    value: valueProp,
    defaultValue,
    onValueChange,
    collapsible = true,
    size = 'md',
    variant = 'contained',
    headingLevel = 3,
    indicatorPlacement = 'end',
    unmountOnClose = false,
    contentRole = 'region',
    disabled = false,
    className,
    children,
    onKeyDown,
    ...rest
  } = props as AccordionResolvedProps;

  const isMultiple = type === 'multiple';
  const rootRef = useRef<HTMLDivElement | null>(null);

  const setRefs = useCallback(
    (node: HTMLDivElement | null) => {
      rootRef.current = node;
      if (typeof ref === 'function') ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  const emit = useEventCallback((next: string[]) => {
    if (isMultiple) {
      (onValueChange as ((value: string[]) => void) | undefined)?.(next);
    } else {
      (onValueChange as ((value: string | null) => void) | undefined)?.(next[0] ?? null);
    }
  });

  const [openValues, setOpenValues] = useControllableState<string[]>({
    value: valueProp === undefined ? undefined : toArray(valueProp),
    defaultValue: toArray(defaultValue),
    onChange: emit,
  });

  const toggle = useCallback(
    (itemValue: string) => {
      setOpenValues((previous) => {
        const isOpen = previous.includes(itemValue);
        if (isMultiple) {
          return isOpen ? previous.filter((item) => item !== itemValue) : [...previous, itemValue];
        }
        if (isOpen) {
          // Returning the same reference is how "not collapsible" becomes a
          // genuine no-op: useControllableState bails on an identical value, so
          // onValueChange does not fire for a change that did not happen.
          return collapsible ? [] : previous;
        }
        return [itemValue];
      });
    },
    [setOpenValues, isMultiple, collapsible],
  );

  const context = useMemo<AccordionContextValue>(
    () => ({
      isOpen: (itemValue: string) => openValues.includes(itemValue),
      toggle,
      size,
      variant,
      headingLevel,
      indicatorPlacement,
      unmountOnClose,
      contentRole,
      disabled,
    }),
    [
      openValues,
      toggle,
      size,
      variant,
      headingLevel,
      indicatorPlacement,
      unmountOnClose,
      contentRole,
      disabled,
    ],
  );

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;

    const root = rootRef.current;
    const target = event.target as HTMLElement | null;
    if (!root || !target || target.dataset['stratum'] !== 'disclosure-trigger') return;

    const triggers = Array.from(
      root.querySelectorAll<HTMLElement>('[data-stratum="disclosure-trigger"]'),
    ).filter((element) => element.closest('[data-stratum="accordion"]') === root);

    const current = triggers.indexOf(target);
    if (current === -1 || triggers.length === 0) return;

    let next: number;
    switch (event.key) {
      case 'ArrowDown':
        next = (current + 1) % triggers.length;
        break;
      case 'ArrowUp':
        next = (current - 1 + triggers.length) % triggers.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = triggers.length - 1;
        break;
      default:
        return;
    }

    const element = triggers[next];
    if (!element) return;
    event.preventDefault();
    element.focus();
  };

  return (
    <AccordionContext.Provider value={context}>
      <div
        {...rest}
        ref={setRefs}
        data-stratum="accordion"
        data-type={type}
        data-variant={variant}
        data-size={size}
        className={clsx('stratum-accordion', className)}
        onKeyDown={handleKeyDown}
      >
        {children}
      </div>
    </AccordionContext.Provider>
  );
});

/* ========================================================================== */
/* AccordionItem                                                               */
/* ========================================================================== */

export interface AccordionItemProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'onChange'> {
  /** Identity used in the accordion's `value`. */
  value: string;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  icon?: ReactNode;
  indicator?: ReactNode;
  disabled?: boolean;
  children?: ReactNode;
}

export const AccordionItem = forwardRef<HTMLDivElement, AccordionItemProps>(
  function AccordionItem(
    { value, title, description, meta, icon, indicator, disabled = false, className, children, ...rest },
    ref,
  ) {
    const ctx = useAccordionContext('AccordionItem');
    const open = ctx.isOpen(value);
    const isDisabled = disabled || ctx.disabled;

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="accordion-item"
        data-state={open ? 'open' : 'closed'}
        data-disabled={isDisabled || undefined}
        className={clsx('stratum-accordion__item', className)}
      >
        <Disclosure
          title={title}
          description={description}
          meta={meta}
          icon={icon}
          indicator={indicator}
          open={open}
          onOpenChange={() => ctx.toggle(value)}
          disabled={isDisabled}
          size={ctx.size}
          variant="plain"
          headingLevel={ctx.headingLevel}
          indicatorPlacement={ctx.indicatorPlacement}
          unmountOnClose={ctx.unmountOnClose}
          contentRole={ctx.contentRole}
          className="stratum-accordion__disclosure"
        >
          {children}
        </Disclosure>
      </div>
    );
  },
);
