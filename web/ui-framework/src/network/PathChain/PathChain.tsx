import { forwardRef, useMemo, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './PathChain.css';

/** Reachability of a single hop, independent of every other hop. */
export type HopStatus = 'ok' | 'degraded' | 'broken' | 'unknown';

export interface PathHop {
  /** Stable key. Also used as the default label. */
  id: string;
  /** Display text. Falls back to `id`. */
  label?: string;
  status?: HopStatus;
  /** Optional detail surfaced via `title` — latency, transport, address. */
  detail?: string;
}

export interface PathChainProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  hops: PathHop[];
  /** Separator drawn between hops. Default `'/'`. */
  separator?: string;
  /**
   * Alternate paths not shown inline. Rendered as a `+N` affordance so backup
   * routes never inflate the row height — the single most important sizing
   * rule for this component.
   */
  alternates?: PathHop[][];
  /** Accessible label for the `+N` affordance. Receives the count. */
  overflowLabel?: (count: number) => string;
  onOverflowClick?: () => void;
  /**
   * Cap on inline hops. Beyond this the middle is elided with `…`, keeping the
   * origin and the terminal visible — those are the two an operator scans for.
   */
  maxInlineHops?: number;
  size?: 'sm' | 'md';
  /** Marks the whole chain as not currently selected/active. */
  muted?: boolean;
}

/**
 * A multi-hop path rendered inline, in one table cell.
 *
 * WHY THE TAIL DYES
 * -----------------
 * Once a hop is unreachable, every hop after it is unreachable *through this
 * path* regardless of its own health — you cannot traverse hop 4 if hop 3 is
 * down. So from the first `broken` hop onward the entire tail is painted
 * broken, even where a later hop reports `ok` on its own. Showing hop 4 as
 * green next to a red hop 3 invites the reader to conclude the path partially
 * works, which is never true.
 *
 * A hop's own reported status is preserved in its `title`, so the underlying
 * per-hop truth is still available on inspection — it is only the colour that
 * is subordinated to path semantics.
 *
 * `degraded` does NOT dye the tail: a slow hop still forwards traffic.
 *
 * ACCESSIBILITY
 * -------------
 * Status is never colour-only. Each hop carries a `title` and the whole chain
 * exposes a text summary to assistive technology naming each hop and its
 * state, so a screen-reader user gets the same information a sighted user
 * reads from the colours.
 */
export const PathChain = forwardRef<HTMLSpanElement, PathChainProps>(function PathChain(
  {
    hops,
    separator = '/',
    alternates,
    overflowLabel = (n) => `${n} alternate path${n === 1 ? '' : 's'}`,
    onOverflowClick,
    maxInlineHops = 6,
    size = 'md',
    muted = false,
    className,
    ...rest
  },
  ref,
) {
  const resolved = useMemo(() => {
    // Index of the first hop that breaks the path. Everything at or after it
    // is displayed broken.
    const breakAt = hops.findIndex((h) => h.status === 'broken');

    return hops.map((hop, i) => {
      const own: HopStatus = hop.status ?? 'unknown';
      const effective: HopStatus = breakAt >= 0 && i > breakAt ? 'broken' : own;
      return {
        ...hop,
        own,
        effective,
        /** True where path semantics overrode the hop's own report. */
        subordinated: effective !== own,
      };
    });
  }, [hops]);

  // Elide the middle, never the ends.
  const elided = useMemo(() => {
    if (resolved.length <= maxInlineHops) return { head: resolved, tail: [], hidden: 0 };
    const headCount = Math.ceil((maxInlineHops - 1) / 2);
    const tailCount = maxInlineHops - 1 - headCount;
    return {
      head: resolved.slice(0, headCount),
      tail: tailCount > 0 ? resolved.slice(-tailCount) : [],
      hidden: resolved.length - headCount - tailCount,
    };
  }, [resolved, maxInlineHops]);

  const summary = resolved
    .map((h) => `${h.label ?? h.id} (${h.effective})`)
    .join(` ${separator} `);

  const renderHop = (h: (typeof resolved)[number], index: number) => (
    <span
      key={`${h.id}-${index}`}
      className="stratum-path-chain__hop"
      data-status={h.effective}
      data-subordinated={h.subordinated || undefined}
      title={
        h.subordinated
          ? `${h.label ?? h.id} — reports ${h.own}, unreachable via this path${h.detail ? ` · ${h.detail}` : ''}`
          : `${h.label ?? h.id} — ${h.effective}${h.detail ? ` · ${h.detail}` : ''}`
      }
    >
      {h.label ?? h.id}
    </span>
  );

  const sep = (key: string) => (
    <span key={key} className="stratum-path-chain__sep" aria-hidden="true">
      {separator}
    </span>
  );

  const inline: ReactNode[] = [];
  elided.head.forEach((h, i) => {
    if (i > 0) inline.push(sep(`hs-${i}`));
    inline.push(renderHop(h, i));
  });
  if (elided.hidden > 0) {
    inline.push(sep('he'));
    inline.push(
      <span
        key="ellipsis"
        className="stratum-path-chain__ellipsis"
        title={`${elided.hidden} more hop${elided.hidden === 1 ? '' : 's'}`}
      >
        …
      </span>,
    );
  }
  elided.tail.forEach((h, i) => {
    inline.push(sep(`ts-${i}`));
    inline.push(renderHop(h, elided.head.length + elided.hidden + i));
  });

  const alternateCount = alternates?.length ?? 0;

  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="path-chain"
      data-size={size}
      data-muted={muted || undefined}
      className={clsx('stratum-path-chain', className)}
    >
      <span className="stratum-visually-hidden">{summary}</span>
      <span className="stratum-path-chain__inline" aria-hidden="true">
        {inline}
      </span>
      {alternateCount > 0 && (
        <button
          type="button"
          className="stratum-path-chain__overflow"
          onClick={onOverflowClick}
          aria-label={overflowLabel(alternateCount)}
          title={alternates
            ?.map((p) => p.map((h) => h.label ?? h.id).join(` ${separator} `))
            .join('\n')}
        >
          +{alternateCount}
        </button>
      )}
    </span>
  );
});
