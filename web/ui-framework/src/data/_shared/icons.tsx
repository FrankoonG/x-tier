/* ---------------------------------------------------------------------------
 * Internal glyphs for the data layer.
 *
 * NOT exported from the package. Every one is 1em square, painted with
 * `currentColor`, and marked `aria-hidden` — the accessible name always comes
 * from the control that contains them, never from the glyph.
 *
 * `currentColor` rather than `var()` inside SVG paint, for the reason set out
 * in tokens.mesh.css: dark-mode browser extensions substitute `currentColor`
 * reliably and mangle custom properties.
 * ------------------------------------------------------------------------- */
import type { SVGProps } from 'react';

const base: SVGProps<SVGSVGElement> = {
  viewBox: '0 0 16 16',
  width: '1em',
  height: '1em',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  focusable: 'false',
  'aria-hidden': true,
};

export function CopyIcon() {
  return (
    <svg {...base}>
      <rect x="5.75" y="5.75" width="8.5" height="8.5" rx="1.75" />
      <path d="M10.6 3.4a1.75 1.75 0 0 0-1.65-1.65H3.5A1.75 1.75 0 0 0 1.75 3.5v5.45c0 .84.59 1.54 1.38 1.71" />
    </svg>
  );
}

export function CheckIcon() {
  return (
    <svg {...base}>
      <path d="m2.75 8.6 3.4 3.4 7.1-8" />
    </svg>
  );
}

/** Copy failed. A distinct silhouette, not a recoloured tick. */
export function CrossIcon() {
  return (
    <svg {...base}>
      <path d="m3.75 3.75 8.5 8.5M12.25 3.75l-8.5 8.5" />
    </svg>
  );
}

export function ChevronDownIcon() {
  return (
    <svg {...base}>
      <path d="m3.75 6 4.25 4.25L12.25 6" />
    </svg>
  );
}

export function ChevronRightIcon() {
  return (
    <svg {...base}>
      <path d="M6 3.75 10.25 8 6 12.25" />
    </svg>
  );
}

/** Jump to the newest entry. */
export function ArrowToBottomIcon() {
  return (
    <svg {...base}>
      <path d="M8 1.75v8.5M4.5 7l3.5 3.5L11.5 7" />
      <path d="M2.75 14.25h10.5" />
    </svg>
  );
}

/** Line wrapping. The hooked arrow is the conventional editor glyph. */
export function WrapIcon() {
  return (
    <svg {...base}>
      <path d="M1.75 3.75h12.5" />
      <path d="M1.75 8h9.4a2.6 2.6 0 0 1 0 5.2H8.6" />
      <path d="m10.4 11.4-1.9 1.8 1.9 1.8" />
      <path d="M1.75 12.25h3.6" />
    </svg>
  );
}

/** Follow tail on/off. */
export function FollowIcon() {
  return (
    <svg {...base}>
      <path d="M2.25 4.25h11.5M2.25 8h7.5M2.25 11.75h4.5" />
      <path d="M12 9.5v4.25M10.3 12l1.7 1.75L13.7 12" />
    </svg>
  );
}

export function SearchIcon() {
  return (
    <svg {...base}>
      <circle cx="7.1" cy="7.1" r="4.6" />
      <path d="m10.6 10.6 3.15 3.15" />
    </svg>
  );
}

/** Expand a collapsed region — two arrows moving apart. */
export function ExpandIcon() {
  return (
    <svg {...base}>
      <path d="M5.25 6 8 3.25 10.75 6" />
      <path d="M5.25 10 8 12.75 10.75 10" />
    </svg>
  );
}

/** Split-view toggle. */
export function SplitIcon() {
  return (
    <svg {...base}>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5" />
      <path d="M8 2.75v10.5" />
    </svg>
  );
}

/** Unified-view toggle. */
export function UnifiedIcon() {
  return (
    <svg {...base}>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5" />
      <path d="M4.25 6.25h7.5M4.25 9.75h7.5" />
    </svg>
  );
}
