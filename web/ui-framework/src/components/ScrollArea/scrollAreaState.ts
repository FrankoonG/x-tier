import type { ScrollAreaOrientation } from './ScrollArea';

export interface ScrollAreaMetrics {
  scrollTop: number;
  scrollLeft: number;
  scrollHeight: number;
  scrollWidth: number;
  clientHeight: number;
  clientWidth: number;
}

export interface ScrollAreaEdgeState {
  scrollable: boolean;
  top: boolean;
  bottom: boolean;
  start: boolean;
  end: boolean;
}

export const NO_SCROLL_AREA_OVERFLOW: ScrollAreaEdgeState = {
  scrollable: false,
  top: false,
  bottom: false,
  start: false,
  end: false,
};

/** Computes overflow only on axes the component promises to expose. */
export function measureScrollArea(
  metrics: ScrollAreaMetrics,
  orientation: ScrollAreaOrientation,
  slack = 1,
): ScrollAreaEdgeState {
  const allowY = orientation === 'vertical' || orientation === 'both';
  const allowX = orientation === 'horizontal' || orientation === 'both';
  const overflowY = allowY && metrics.scrollHeight - metrics.clientHeight > slack;
  const overflowX = allowX && metrics.scrollWidth - metrics.clientWidth > slack;
  const inlineOffset = Math.abs(metrics.scrollLeft);
  const maxInline = metrics.scrollWidth - metrics.clientWidth;

  return {
    scrollable: overflowY || overflowX,
    top: overflowY && metrics.scrollTop > slack,
    bottom:
      overflowY && metrics.scrollTop + metrics.clientHeight < metrics.scrollHeight - slack,
    start: overflowX && inlineOffset > slack,
    end: overflowX && inlineOffset < maxInline - slack,
  };
}
