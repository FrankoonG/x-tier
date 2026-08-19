import {
  Children,
  cloneElement,
  forwardRef,
  isValidElement,
  type HTMLAttributes,
  type ReactElement,
  type ReactNode,
  type Ref,
  type RefCallback,
} from 'react';
import clsx from 'clsx';

/* ---------------------------------------------------------------------------
 * Ref composition
 * ------------------------------------------------------------------------- */

type PossibleRef<T> = Ref<T> | undefined;
type RefCleanup = () => void;

/**
 * Writes `node` into one ref, whatever shape that ref has.
 *
 * Returns the cleanup function if the ref was a React 19 style callback that
 * returned one, so the caller can run it instead of the legacy `ref(null)`
 * detach call. React 18 callbacks return nothing and get the legacy path.
 */
function assignRef<T>(ref: PossibleRef<T>, node: T | null): RefCleanup | undefined {
  if (typeof ref === 'function') {
    const result = ref(node) as unknown;
    return typeof result === 'function' ? (result as RefCleanup) : undefined;
  }
  if (ref) {
    // React 18 types call this MutableRefObject, React 19 types call it
    // RefObject with a mutable `current`. Neither name exists in both, so the
    // structural cast is what keeps this file compiling against either.
    (ref as { current: T | null }).current = node;
  }
  return undefined;
}

/**
 * Merges any number of refs into a single callback ref.
 *
 * WHY THIS IS NOT THREE LINES
 * ---------------------------
 * React 19 lets a callback ref return a cleanup function, and when it does,
 * React calls that cleanup instead of calling the ref again with `null`. A
 * naive composer swallows the returned cleanup — so the child's cleanup never
 * runs, and its `ref(null)` never runs either, which leaks whatever the child
 * captured. This implementation remembers the cleanup per ref and runs the
 * right detach path for each one.
 *
 * The composed callback deliberately returns `undefined` in every branch:
 * React 18 logs a warning when a callback ref returns a function, so we cannot
 * propagate cleanups upward even though React 19 would accept them.
 */
export function composeRefs<T>(...refs: PossibleRef<T>[]): RefCallback<T> {
  let cleanups: Array<RefCleanup | undefined> = [];

  return (node: T | null) => {
    if (node === null) {
      for (let i = 0; i < refs.length; i += 1) {
        const cleanup = cleanups[i];
        if (cleanup) cleanup();
        else assignRef(refs[i], null);
      }
      cleanups = [];
      return;
    }
    cleanups = refs.map((ref) => assignRef(ref, node));
  };
}

/**
 * Reads the ref off an element across React versions.
 *
 * React 19 moved `ref` into `props` and left a warning getter on
 * `element.ref`; React 18 has the opposite arrangement. Touching the wrong one
 * logs a deprecation warning in development, so the accessor is chosen by
 * probing for React's own warning getter rather than by sniffing a version.
 */
function getElementRef(element: ReactElement): PossibleRef<unknown> {
  // React <= 18 in development: `props.ref` is the warning getter.
  let getter = Object.getOwnPropertyDescriptor(element.props, 'ref')?.get;
  if (getter && 'isReactWarning' in getter && getter.isReactWarning) {
    return (element as unknown as { ref?: Ref<unknown> }).ref;
  }

  // React 19 in development: `element.ref` is the warning getter.
  getter = Object.getOwnPropertyDescriptor(element, 'ref')?.get;
  if (getter && 'isReactWarning' in getter && getter.isReactWarning) {
    return (element.props as { ref?: Ref<unknown> }).ref;
  }

  // Production builds of both, where neither getter is installed.
  return (
    (element.props as { ref?: Ref<unknown> }).ref ??
    (element as unknown as { ref?: Ref<unknown> }).ref
  );
}

/* ---------------------------------------------------------------------------
 * Prop merging
 * ------------------------------------------------------------------------- */

type UnknownProps = Record<string, unknown>;
type AnyHandler = (...args: unknown[]) => unknown;

const EVENT_HANDLER = /^on[A-Z]/;

/**
 * Merge rules, in priority order:
 *
 * 1. Event handlers COMPOSE. Both run, child first. Child-first matters: it
 *    lets the consumer call `preventDefault()` and lets our own handler check
 *    `defaultPrevented` before acting, which is how an `asChild` trigger stays
 *    cancellable.
 * 2. `className` and `style` MERGE, with the child winning on conflicting
 *    style properties.
 * 3. Everything else: the CHILD wins. The child is the real element and the
 *    author wrote those props deliberately.
 *
 * Slot props the child does not mention pass through untouched — that is how
 * `id`, `aria-*` and `data-*` reach an arbitrary trigger element.
 */
function mergeSlotProps(slotProps: UnknownProps, childProps: UnknownProps): UnknownProps {
  const merged: UnknownProps = { ...slotProps };

  for (const key of Object.keys(childProps)) {
    const slotValue = slotProps[key];
    const childValue = childProps[key];

    if (EVENT_HANDLER.test(key)) {
      if (typeof slotValue === 'function' && typeof childValue === 'function') {
        const childHandler = childValue as AnyHandler;
        const slotHandler = slotValue as AnyHandler;
        merged[key] = (...args: unknown[]) => {
          childHandler(...args);
          slotHandler(...args);
        };
      } else {
        merged[key] = childValue ?? slotValue;
      }
      continue;
    }

    if (key === 'style') {
      merged[key] = {
        ...(slotValue as object | undefined),
        ...(childValue as object | undefined),
      };
      continue;
    }

    if (key === 'className') {
      merged[key] = clsx(slotValue as string | undefined, childValue as string | undefined);
      continue;
    }

    merged[key] = childValue;
  }

  return merged;
}

/* ---------------------------------------------------------------------------
 * Slottable
 * ------------------------------------------------------------------------- */

export interface SlottableProps {
  children?: ReactNode;
}

/**
 * Marks which part of a Slot's children is the consumer's element.
 *
 * Needed when a component renders decoration around its children — an icon, a
 * spinner — and still wants `asChild`. Without it, `<Slot>` would see three
 * children and have no way to know which one to merge onto:
 *
 * ```tsx
 * <Slot {...rest}>
 *   {icon}
 *   <Slottable>{children}</Slottable>
 * </Slot>
 * ```
 */
export function Slottable({ children }: SlottableProps) {
  return <>{children}</>;
}
Slottable.displayName = 'Slottable';

function isSlottable(child: ReactNode): child is ReactElement<SlottableProps> {
  return isValidElement(child) && child.type === Slottable;
}

/* ---------------------------------------------------------------------------
 * Slot
 * ------------------------------------------------------------------------- */

export interface SlotProps extends HTMLAttributes<HTMLElement> {
  children?: ReactNode;
}

/**
 * Renders no element of its own: merges its props and ref onto its single
 * child element instead.
 *
 * This is the `asChild` mechanism used across the framework, so a trigger can
 * be a `<button>`, an `<a>`, a router `<Link>`, or a consumer's own component,
 * without the library wrapping it in a `<span>` that breaks flex layout,
 * duplicates the accessible name, or swallows the focus ring.
 *
 * A wrong number of children is a development-time `console.error` rather than
 * a throw: an operations panel should not white-screen because one trigger was
 * authored badly, and the error names the exact fix.
 */
export const Slot = forwardRef<HTMLElement, SlotProps>(function Slot(
  { children, ...slotProps },
  forwardedRef,
) {
  const childArray = Children.toArray(children);
  const slottable = childArray.find(isSlottable);

  // `<Slottable>` present: the element to clone is the Slottable's own child,
  // and the surrounding decoration is re-parented into it.
  if (slottable) {
    const target = slottable.props.children;

    if (!isValidElement(target)) {
      if (import.meta.env?.DEV) {
        console.error(
          '[stratum] <Slottable> must wrap exactly one React element when `asChild` is used.',
        );
      }
      return <>{children}</>;
    }

    const rehomed = childArray.map((child) =>
      child === slottable ? (target.props as { children?: ReactNode }).children : child,
    );

    const slottableProps = mergeSlotProps(
      slotProps as UnknownProps,
      target.props as UnknownProps,
    );
    slottableProps['ref'] = composeRefs(forwardedRef, getElementRef(target));

    return cloneElement(target, slottableProps as never, ...rehomed);
  }

  if (childArray.length !== 1 || !isValidElement(children)) {
    if (import.meta.env?.DEV) {
      console.error(
        '[stratum] `asChild` expects exactly one React element child, and received ' +
          `${childArray.length} node(s). Wrap the content in a single element, or drop ` +
          '`asChild` to let the component render its own.',
      );
    }
    return <>{children}</>;
  }

  const merged = mergeSlotProps(slotProps as UnknownProps, children.props as UnknownProps);
  // `ref` travels in the props object: React 18's cloneElement lifts it out of
  // the config, React 19 keeps it in props. Both end up on the element.
  merged['ref'] = composeRefs(forwardedRef, getElementRef(children));

  return cloneElement(children, merged as never);
});
