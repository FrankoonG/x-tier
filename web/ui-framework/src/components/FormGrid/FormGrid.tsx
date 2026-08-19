import {
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type CSSProperties,
  type ElementType,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { Slot, composeRefs } from '../../primitives/Slot';
import './FormGrid.css';

export type FormGridColumns = 1 | 2 | 3 | 'auto';
export type FormGridGap = 'none' | 'sm' | 'md' | 'lg';

interface FormGridContextValue {
  /** Declared maximum, or `null` when the count is intrinsic (`auto`). */
  columns: number | null;
  /** Tracks the grid is currently rendering, once measured. */
  tracks: number | null;
}

const FormGridContext = createContext<FormGridContextValue | null>(null);

const useIsomorphicLayoutEffect = typeof window === 'undefined' ? useEffect : useLayoutEffect;

/**
 * Counts the tracks in a used `grid-template-columns` value.
 *
 * The computed value of a laid-out grid is a list of pixel lengths, which is
 * how many columns `repeat(auto-fill, …)` resolved to at the current width. A
 * detached or `display: none` grid returns the specified value instead (still
 * containing `repeat(`), and that case reports `null` so the caller keeps its
 * previous answer.
 *
 * Zero-width tracks are excluded, and that exclusion is load-bearing. An item
 * spanning past the last column creates an implicit track, which Blink reports
 * in this same list — so a naive count would see the extra track, conclude the
 * span was legal, and keep it forever. `grid-auto-columns: 0` in the
 * stylesheet pins every implicit track to zero so it can be told apart from a
 * real one, which breaks that feedback loop.
 */
function countTracks(value: string): number | null {
  if (!value || value === 'none') return null;
  const tracks = value
    .split(/\s+/)
    .filter((token) => token.endsWith('px') && Number.parseFloat(token) > 0).length;
  return tracks > 0 ? tracks : null;
}

export interface FormGridProps extends HTMLAttributes<HTMLDivElement> {
  /**
   * Maximum number of columns. `auto` fits as many `minColumnWidth` columns
   * as the container allows.
   */
  columns?: FormGridColumns;
  gap?: FormGridGap;
  /**
   * Narrowest a column may become before the grid drops one. Any CSS length;
   * a number is treated as pixels.
   */
  minColumnWidth?: string | number;
  asChild?: boolean;
  children?: ReactNode;
}

/**
 * Responsive form columns that answer to their container, not the viewport.
 *
 * WHY THERE ARE NO BREAKPOINTS
 * ----------------------------
 * The track list is `repeat(auto-fill, minmax(min(100%, max(<min>, <exact>)),
 * 1fr))`, where `<exact>` is the width one column would have at the declared
 * column count. When the container is wide enough, `<exact>` wins and the grid
 * renders exactly `columns` columns; when it narrows, `<min>` takes over and
 * the grid sheds a column; the `min(100%, …)` guard keeps a container narrower
 * than one column from overflowing. The result reacts to the container at
 * every width rather than at two or three chosen ones, and there is no
 * breakpoint constant to keep in sync with `minColumnWidth`.
 *
 * `container-type: inline-size` is still declared, so descendants — a
 * `FormActions` footer, a consumer's own component — can run container
 * queries against the form's width.
 */
export const FormGrid = forwardRef<HTMLDivElement, FormGridProps>(function FormGrid(
  {
    columns = 2,
    gap = 'md',
    minColumnWidth = '18rem',
    asChild = false,
    className,
    style,
    children,
    ...rest
  },
  ref,
) {
  // State rather than a ref: the host element can be replaced without this
  // component unmounting (toggling `asChild`, or an `asChild` consumer swapping
  // their child's element type), and a ref never re-runs the effect below — the
  // ResizeObserver would keep watching the detached node forever and `tracks`
  // would freeze at a stale ceiling.
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const [tracks, setTracks] = useState<number | null>(null);

  /* Items may span several columns, and a span wider than the number of tracks
   * currently rendered would create an implicit column — silently pushing the
   * grid past its container. The count cannot be derived in CSS (a container
   * query cannot read `minColumnWidth`), so it is observed here and published
   * for FormGridItem to clamp against. The read happens in a layout effect and
   * inside the ResizeObserver callback, both of which run before paint, so the
   * clamp is applied without a visible reflow. */
  useIsomorphicLayoutEffect(() => {
    if (!node) return;

    const measure = () => {
      const next = countTracks(getComputedStyle(node).gridTemplateColumns);
      if (next !== null) setTracks((prev) => (prev === next ? prev : next));
    };

    measure();

    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [node, columns, gap, minColumnWidth]);

  const styleVars: Record<string, string | number> = {
    '--_min': typeof minColumnWidth === 'number' ? `${minColumnWidth}px` : minColumnWidth,
  };

  // `composeRefs` returns a fresh closure per call, and a ref prop with a new
  // identity is detached (called with `null`) and reattached on every render —
  // which, under `asChild`, tears down and rebuilds the consumer's own callback
  // ref for every unrelated parent update.
  const composedRef = useMemo(() => composeRefs<HTMLDivElement>(ref, setNode), [ref]);

  // Memoised for the same reason every other provider in this library is: a
  // fresh object identity re-renders every FormGridItem in the subtree.
  const gridContext = useMemo<FormGridContextValue>(
    () => ({ columns: columns === 'auto' ? null : columns, tracks }),
    [columns, tracks],
  );

  const Comp: ElementType = asChild ? Slot : 'div';

  return (
    <FormGridContext.Provider value={gridContext}>
      <Comp
        {...rest}
        ref={composedRef}
        data-stratum="form-grid"
        data-columns={columns}
        data-gap={gap}
        className={clsx('stratum-form-grid', className)}
        style={{ ...(styleVars as CSSProperties), ...style }}
      >
        {children}
      </Comp>
    </FormGridContext.Provider>
  );
});

export interface FormGridItemProps extends HTMLAttributes<HTMLDivElement> {
  /**
   * Columns to occupy. `full` always spans the whole row. A numeric span is
   * clamped to the columns actually rendered, so it degrades to a single
   * column in a narrow container instead of forcing the grid to overflow.
   */
  span?: number | 'full';
  asChild?: boolean;
  children?: ReactNode;
}

/**
 * A cell that can span several form columns.
 *
 * Only needed for items that span; a plain child of `FormGrid` occupies one
 * column with no wrapper.
 */
export const FormGridItem = forwardRef<HTMLDivElement, FormGridItemProps>(function FormGridItem(
  { span = 1, asChild = false, className, style, children, ...rest },
  ref,
) {
  const grid = useContext(FormGridContext);

  let resolved: number | null = null;
  if (span !== 'full') {
    const requested = Math.max(1, Math.floor(span));
    // Clamp against what is on screen now, falling back to the declared
    // maximum until the first measurement lands, then to the request itself
    // for an `auto` grid whose count is unbounded.
    const ceiling = grid?.tracks ?? grid?.columns ?? requested;
    resolved = Math.min(requested, ceiling);

    if (import.meta.env?.DEV && grid?.columns != null && requested > grid.columns) {
      console.warn(
        `[stratum] <FormGridItem span={${requested}}> exceeds the grid's ${grid.columns} ` +
          'columns and was clamped. Use span="full" if it should always fill the row.',
      );
    }
  }

  const styleVars: Record<string, string | number> = {};
  if (resolved !== null) styleVars['--_span'] = resolved;

  const Comp: ElementType = asChild ? Slot : 'div';

  return (
    <Comp
      {...rest}
      ref={ref}
      data-stratum="form-grid-item"
      data-span={span === 'full' ? 'full' : undefined}
      className={clsx('stratum-form-grid__item', className)}
      style={{ ...(styleVars as CSSProperties), ...style }}
    >
      {children}
    </Comp>
  );
});
