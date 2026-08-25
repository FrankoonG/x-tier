import {
  forwardRef,
  useCallback,
  useEffect,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type Ref,
} from 'react';
import clsx from 'clsx';
import {
  measureScrollArea,
  NO_SCROLL_AREA_OVERFLOW,
  type ScrollAreaEdgeState,
} from './scrollAreaState';
import './ScrollArea.css';

export type ScrollAreaOrientation = 'vertical' | 'horizontal' | 'both';

export interface ScrollAreaProps extends HTMLAttributes<HTMLDivElement> {
  orientation?: ScrollAreaOrientation;
  /** Caps the viewport height. A number is treated as pixels. */
  maxHeight?: number | string;
  /** Edge affordances on any side with content beyond it. Default `true`. */
  fade?: boolean;
  /**
   * Accessible name for the scroll region. Supply it whenever the area can
   * actually scroll: the viewport becomes a tab stop so keyboard users can
   * reach the content (SC 2.1.1), and an unnamed tab stop is disorienting.
   */
  label?: string;
  /**
   * Names the scroll region from existing visible text instead of `label` —
   * a card title, a section heading. Takes precedence over `label`.
   */
  labelledBy?: string;
  /** Keeps the viewport in the tab order even when nothing overflows. */
  focusable?: boolean;
  viewportClassName?: string;
  /** Handle on the scrolling element itself, for `scrollTo`/`scrollIntoView`. */
  viewportRef?: Ref<HTMLDivElement>;
}

function assignRef<T>(ref: Ref<T> | undefined, value: T | null): void {
  if (typeof ref === 'function') ref(value);
  else if (ref) (ref as { current: T | null }).current = value;
}

/**
 * A styled scroll container with edge affordances.
 *
 * WHY THE OVERFLOW STATE IS COMPUTED IN JS
 * ----------------------------------------
 * The pure-CSS version of this uses `animation-timeline: scroll()`, which is
 * elegant but is not available across engines, and — more importantly — CSS
 * cannot answer the question this component actually has to answer: *is this
 * region scrollable at all?* That answer decides whether the viewport joins
 * the tab order, and it has to be right in every browser, not just the ones
 * with scroll-driven animations.
 *
 * So: one `scroll` listener (passive) plus a `ResizeObserver` on the viewport
 * and on a content wrapper. The wrapper is what makes content growth
 * observable — a `ResizeObserver` on the scroll box itself never fires when
 * only its children get taller.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - A scrollable region that is not focusable is unreachable by keyboard in
 *   Chromium. `tabIndex` is added exactly when there is something to scroll,
 *   so a region that fits its content never steals a tab stop.
 * - The focus ring is drawn inset, because an offset outline on the scroll box
 *   is clipped by the very overflow that makes this component necessary.
 * - Edges are named `start`/`end` rather than `left`/`right` and positioned
 *   with logical properties, so the affordances stay on the correct side in
 *   RTL. `scrollLeft` is read through `Math.abs`, since it is negative in RTL
 *   in every engine except legacy WebKit.
 */
export const ScrollArea = forwardRef<HTMLDivElement, ScrollAreaProps>(function ScrollArea(
  {
    orientation = 'vertical',
    maxHeight,
    fade = true,
    label,
    labelledBy,
    focusable = false,
    viewportClassName,
    viewportRef,
    className,
    style,
    children,
    ...rest
  },
  ref,
) {
  // State rather than a ref: the effect below must re-run when the node
  // mounts, and a ref never triggers that.
  const [viewport, setViewport] = useState<HTMLDivElement | null>(null);
  const [edges, setEdges] = useState<ScrollAreaEdgeState>(NO_SCROLL_AREA_OVERFLOW);

  const setViewportNode = useCallback(
    (node: HTMLDivElement | null) => {
      setViewport(node);
      assignRef(viewportRef, node);
    },
    [viewportRef],
  );

  useEffect(() => {
    if (!viewport) {
      setEdges(NO_SCROLL_AREA_OVERFLOW);
      return;
    }

    // Sub-pixel layout means an element that exactly fits still reports a
    // fractional difference. One device pixel of slack removes the flicker.
    const SLACK = 1;

    const measure = () => {
      const {
        scrollTop,
        scrollLeft,
        scrollHeight,
        scrollWidth,
        clientHeight,
        clientWidth,
      } = viewport;

      const next = measureScrollArea(
        { scrollTop, scrollLeft, scrollHeight, scrollWidth, clientHeight, clientWidth },
        orientation,
        SLACK,
      );

      setEdges((prev) =>
        prev.scrollable === next.scrollable &&
        prev.top === next.top &&
        prev.bottom === next.bottom &&
        prev.start === next.start &&
        prev.end === next.end
          ? prev
          : next,
      );
    };

    measure();
    viewport.addEventListener('scroll', measure, { passive: true });

    let observer: ResizeObserver | undefined;
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(measure);
      observer.observe(viewport);
      const content = viewport.firstElementChild;
      if (content) observer.observe(content);
    }

    return () => {
      viewport.removeEventListener('scroll', measure);
      observer?.disconnect();
    };
  }, [viewport, orientation]);

  const viewportStyle: CSSProperties & Record<string, string | number> = {};
  if (maxHeight !== undefined) {
    viewportStyle['--_max-h'] = typeof maxHeight === 'number' ? `${maxHeight}px` : maxHeight;
  }

  const isTabbable = edges.scrollable || focusable;
  const isNamed = Boolean(labelledBy) || Boolean(label);

  // The tab stop is added exactly when the region can be scrolled, and an
  // unnamed tab stop is announced as nothing at all. The prop doc calls this
  // mandatory; nothing enforced it, so it is enforced here the way Button and
  // Radio enforce theirs.
  if (
    import.meta.env?.DEV &&
    isTabbable &&
    !isNamed &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      '[stratum] <ScrollArea> that can scroll requires `label` or `labelledBy`. ' +
        'It becomes a tab stop (SC 2.1.1) and an unnamed one is unusable.',
    );
  }

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="scroll-area"
      data-orientation={orientation}
      data-scrollable={edges.scrollable || undefined}
      data-overflow-top={(fade && edges.top) || undefined}
      data-overflow-bottom={(fade && edges.bottom) || undefined}
      data-overflow-start={(fade && edges.start) || undefined}
      data-overflow-end={(fade && edges.end) || undefined}
      className={clsx('stratum-scroll-area', className)}
      style={style}
    >
      <div
        ref={setViewportNode}
        data-stratum="scroll-area-viewport"
        className={clsx('stratum-scroll-area__viewport', 'stratum-focus-inset', viewportClassName)}
        style={viewportStyle}
        tabIndex={isTabbable ? 0 : undefined}
        role={isTabbable && isNamed ? 'region' : undefined}
        aria-labelledby={isTabbable && labelledBy ? labelledBy : undefined}
        aria-label={isTabbable && !labelledBy && label ? label : undefined}
      >
        <div className="stratum-scroll-area__content">{children}</div>
      </div>

    </div>
  );
});
