/* ---------------------------------------------------------------------------
 * Internal status glyphs.
 *
 * NOT exported from the package. Toast, Banner and InlineMessage all need a
 * default icon per tone, and WCAG 1.4.1 forbids carrying that meaning in
 * colour alone — so the SHAPES are deliberately distinct rather than five
 * recolourings of one circle:
 *
 *   success   circle + tick
 *   warning   triangle + bar        <- silhouette differs at a glance
 *   danger    octagon + cross       <- silhouette differs at a glance
 *   info      circle + i
 *   neutral   circle + dash
 *   live      bolt                  <- "applied instantly"
 *
 * Every glyph is 1em square and painted with `currentColor`, so it inherits
 * the tone colour set by the host component and survives dark-mode browser
 * extensions, which substitute `currentColor` reliably where they mangle
 * `var()` inside SVG paint.
 * ------------------------------------------------------------------------- */
import type { ReactNode, SVGProps } from 'react';

export type StatusTone =
  | 'success'
  | 'warning'
  | 'danger'
  | 'info'
  | 'neutral'
  | 'accent'
  | 'live';

const base: SVGProps<SVGSVGElement> = {
  viewBox: '0 0 16 16',
  width: '1em',
  height: '1em',
  // Inline rather than a `fill="none"` attribute: the framework reset sets
  // `svg { fill: currentColor }`, and any CSS declaration beats a presentation
  // attribute — which would silently turn every outline glyph into a blob.
  style: { fill: 'none' },
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  focusable: 'false',
  'aria-hidden': true,
};

const glyphs: Record<StatusTone, ReactNode> = {
  success: (
    <svg {...base} key="success">
      <circle cx="8" cy="8" r="6.25" />
      <path d="m5.25 8.15 1.9 1.9 3.6-3.9" />
    </svg>
  ),
  warning: (
    <svg {...base} key="warning">
      <path d="M8 2.2 1.9 12.9a.9.9 0 0 0 .78 1.35h10.64a.9.9 0 0 0 .78-1.35Z" />
      <path d="M8 6.3v3.1" />
      <path d="M8 11.6h.01" strokeWidth={1.8} />
    </svg>
  ),
  danger: (
    <svg {...base} key="danger">
      <path d="M5.6 1.75h4.8l3.85 3.85v4.8l-3.85 3.85H5.6L1.75 10.4V5.6Z" />
      <path d="m6.1 6.1 3.8 3.8M9.9 6.1 6.1 9.9" />
    </svg>
  ),
  info: (
    <svg {...base} key="info">
      <circle cx="8" cy="8" r="6.25" />
      <path d="M8 7.4v3.6" />
      <path d="M8 5.05h.01" strokeWidth={1.8} />
    </svg>
  ),
  neutral: (
    <svg {...base} key="neutral">
      <circle cx="8" cy="8" r="6.25" />
      <path d="M5.4 8h5.2" />
    </svg>
  ),
  accent: (
    <svg {...base} key="accent">
      <circle cx="8" cy="8" r="6.25" />
      <path d="M8 7.4v3.6" />
      <path d="M8 5.05h.01" strokeWidth={1.8} />
    </svg>
  ),
  live: (
    <svg {...base} key="live">
      <path d="M8.9 1.75 3.4 9.05h3.9l-.7 5.2 5.6-7.35H8.3Z" />
    </svg>
  ),
};

/** Returns the default glyph for a tone. Always decorative. */
export function statusGlyph(tone: StatusTone): ReactNode {
  return glyphs[tone];
}
