import { forwardRef, type CSSProperties, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { useTabsContext } from './Tabs';
import './TabPanel.css';

type StyleWithVars = CSSProperties & Record<`--${string}`, string | number>;

export interface TabPanelProps extends HTMLAttributes<HTMLDivElement> {
  /** Ties this panel to the `Tab` with the same value. */
  value: string;
  /**
   * Render the panel even while inactive, hidden with the `hidden` attribute.
   * Costs the render but preserves uncontrolled form state, scroll position and
   * any in-flight subscription inside the panel. Overrides `Tabs keepMounted`.
   */
  keepMounted?: boolean;
  children?: ReactNode;
}

/**
 * The content region for one tab.
 *
 * DIRECTIONAL ENTRY
 * -----------------
 * `Tabs` computes a +1 / -1 / 0 delta from the tab order and hands it down; the
 * panel publishes it as `--_dx` / `--_dy` and the `@starting-style` rule
 * multiplies `--stratum-slide` by it. Two consequences fall out of doing it in
 * CSS rather than JS:
 *
 *   - `--stratum-slide` is already scaled by `--stratum-motion-distance`, so
 *     reduced motion collapses the travel to zero and leaves the cross-fade,
 *     with no JS branch anywhere.
 *   - No ref is mutated during render to remember the previous index. hy2scale's
 *     TabPanel did exactly that, which double-counts under React's concurrent
 *     double-invoke and intermittently slides the wrong way.
 *
 * ONLY THE ENTRY IS ANIMATED
 * --------------------------
 * The outgoing panel is not animated out. Overlapping two panels requires
 * taking one out of flow, which changes the container's height mid-transition
 * and yanks the scroll position of anything below — visible and unpleasant in a
 * dense panel. A fast directional entry reads as movement without that cost.
 *
 * `tabIndex={0}` is deliberate: APG asks that a panel be focusable so a
 * keyboard user leaving the tab list lands in the content, and it is the only
 * way a panel whose content is not itself focusable can be reached at all.
 */
export const TabPanel = forwardRef<HTMLDivElement, TabPanelProps>(function TabPanel(
  { value, keepMounted, className, children, style, ...rest },
  ref,
) {
  const ctx = useTabsContext('TabPanel');
  const isActive = ctx.value === value;
  const mounted = keepMounted ?? ctx.keepMounted;

  if (!isActive && !mounted) return null;

  const vertical = ctx.orientation === 'vertical';
  const panelStyle: StyleWithVars = {
    ...style,
    '--_dx': vertical ? 0 : ctx.direction,
    '--_dy': vertical ? ctx.direction : 0,
  };

  return (
    <div
      {...rest}
      ref={ref}
      role="tabpanel"
      id={ctx.panelId(value)}
      aria-labelledby={ctx.tabId(value)}
      tabIndex={0}
      hidden={!isActive || undefined}
      data-stratum="tab-panel"
      data-state={isActive ? 'active' : 'hidden'}
      data-orientation={ctx.orientation}
      data-size={ctx.size}
      className={clsx('stratum-tab-panel', className)}
      style={panelStyle}
    >
      {children}
    </div>
  );
});
