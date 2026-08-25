/* ===========================================================================
 * THE ICON SET
 *
 * Until now the framework shipped icons only as PRIVATE glyphs for its own
 * components — `components/_shared/statusIcons`, `data/_shared/icons` — and
 * exported none. Consumers therefore had nothing, and the x-tier panel ended
 * up with a bare text sidebar and an emoji for its theme toggle. A component
 * library for operator panels that cannot draw a key, a plug or a route is not
 * finished.
 *
 * THE DRAWING CONTRACT
 * --------------------
 * Every glyph is drawn to the same rules, which are the ones the framework's
 * private glyphs already follow:
 *
 *   16x16 viewBox        one grid, so shapes align when set side by side
 *   1em box              scales with the type size it sits in, no size prop
 *                        needed for the common case
 *   currentColor stroke  inherits tone from the control that contains it —
 *                        and survives dark-mode browser extensions, which
 *                        substitute `currentColor` reliably where they mangle
 *                        `var()` inside SVG paint
 *   1.5 stroke           the weight that reads at 14px without going muddy
 *   round cap and join   softer terminals, and no mitre spikes at 16px
 *   fill via `style`     NOT a `fill="none"` attribute: the framework reset
 *                        sets `svg { fill: currentColor }` and a CSS
 *                        declaration beats a presentation attribute, so an
 *                        attribute would silently fill every outline glyph
 *
 * ACCESSIBILITY
 * -------------
 * Every icon is `aria-hidden` and `focusable="false"` by default, because an
 * icon in this library is always DECORATIVE — the accessible name comes from
 * the control that contains it. Passing a `title` opts into `role="img"` with
 * that name, for the rare standalone case.
 * ======================================================================== */
import { forwardRef, type SVGProps } from 'react';

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, 'children'> {
  /**
   * Box size. Omit to inherit the surrounding font size, which is what makes
   * an icon sit correctly on a text baseline without being measured.
   */
  size?: number | string;
  /**
   * Accessible name. Supplying one promotes the glyph from decoration to
   * `role="img"` — only correct when the icon is the sole carrier of meaning,
   * which inside a labelled control it is not.
   */
  title?: string;
}

/** Shared attributes. Exported so a consumer can draw a matching one-off. */
export const ICON_ATTRS: SVGProps<SVGSVGElement> = {
  viewBox: '0 0 16 16',
  // `style`, not the `fill` attribute — see the note on the reset above.
  style: { fill: 'none' },
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
};

type Draw = (props: SVGProps<SVGSVGElement>) => React.ReactNode;

/**
 * Builds a named icon component from its path data.
 *
 * A factory rather than 60 hand-written components: the attribute set is the
 * contract, and repeating it 60 times is 60 chances to get one wrong.
 */
function icon(displayName: string, draw: Draw) {
  const Component = forwardRef<SVGSVGElement, IconProps>(function Icon(
    { size, title, style, ...rest },
    ref,
  ) {
    const labelled = title !== undefined;
    return (
      <svg
        ref={ref}
        {...ICON_ATTRS}
        width={size ?? '1em'}
        height={size ?? '1em'}
        style={{ ...ICON_ATTRS.style, flexShrink: 0, ...style }}
        role={labelled ? 'img' : undefined}
        aria-hidden={labelled ? undefined : true}
        focusable="false"
        {...rest}
      >
        {labelled ? <title>{title}</title> : null}
        {draw(rest)}
      </svg>
    );
  });
  Component.displayName = displayName;
  return Component;
}

const p = (d: string) => <path d={d} />;

/* -- Navigation ----------------------------------------------------------- */

export const IconOverview = icon('IconOverview', () =>
  p('M2.5 2.5h4.5v4.5H2.5zM9 2.5h4.5v4.5H9zM2.5 9h4.5v4.5H2.5zM9 9h4.5v4.5H9z'),
);
export const IconPeers = icon('IconPeers', () => (
  <>
    <circle cx="8" cy="3.4" r="1.9" />
    <circle cx="3.2" cy="12.2" r="1.9" />
    <circle cx="12.8" cy="12.2" r="1.9" />
    <path d="M6.6 5 4.3 10.4M9.4 5l2.3 5.4M5.1 12.2h5.8" />
  </>
));
export const IconPath = icon('IconPath', () =>
  p('M3.5 12.5V6a2.5 2.5 0 0 1 5 0v4a2.5 2.5 0 0 0 5 0V3.5M3.5 13.5v-1M13.5 2.5v1'),
);
export const IconInbound = icon('IconInbound', () => (
  <>
    <path d="M8 2.5v6.5M5.4 6.6 8 9.2l2.6-2.6" />
    <path d="M2.5 10.5v2a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-2" />
  </>
));
export const IconIdentity = icon('IconIdentity', () =>
  p('M10.5 2.5a3 3 0 1 0-2.7 4.3L3 11.6v1.9h2.4v-1.5h1.5v-1.5h1.5L10 8.9a3 3 0 0 0 .5-6.4'),
);
export const IconSettings = icon('IconSettings', () => (
  <>
    <path d="M2.5 4.5h3M8.5 4.5h5M2.5 11.5h5M10.5 11.5h3" />
    <circle cx="7" cy="4.5" r="1.6" />
    <circle cx="9" cy="11.5" r="1.6" />
  </>
));
export const IconDaemon = icon('IconDaemon', () => (
  <>
    <rect x="2.5" y="2.5" width="11" height="4.5" rx="1.2" />
    <rect x="2.5" y="9" width="11" height="4.5" rx="1.2" />
    <path d="M4.8 4.75h.01M4.8 11.25h.01" />
  </>
));
export const IconDiagnostics = icon('IconDiagnostics', () =>
  p('M1.8 8h3l1.7-4.4 2.6 8.8L11 8h3.2'),
);
export const IconTopology = icon('IconTopology', () => (
  <>
    <circle cx="8" cy="8" r="2" />
    <circle cx="8" cy="2.6" r="1.4" />
    <circle cx="2.8" cy="12" r="1.4" />
    <circle cx="13.2" cy="12" r="1.4" />
    <path d="M8 4v2M6.4 9.3 4 10.9M9.6 9.3 12 10.9" />
  </>
));

/* -- Actions -------------------------------------------------------------- */

export const IconRefresh = icon('IconRefresh', () =>
  p('M13.2 7A5.2 5.2 0 1 0 12 11.4M13.5 3.5V7h-3.5'),
);
export const IconPlus = icon('IconPlus', () => p('M8 3.5v9M3.5 8h9'));
export const IconMinus = icon('IconMinus', () => p('M3.5 8h9'));
export const IconClose = icon('IconClose', () => p('M4 4l8 8M12 4l-8 8'));
export const IconCheck = icon('IconCheck', () => p('M3.5 8.5 6.5 11.5 12.5 4.5'));
export const IconEdit = icon('IconEdit', () =>
  p('M11.2 2.8a1.7 1.7 0 0 1 2.4 2.4L6 12.8l-3.2.8.8-3.2z'),
);
export const IconTrash = icon('IconTrash', () => (
  <>
    <path d="M2.8 4.3h10.4M6.3 4.3V3a.8.8 0 0 1 .8-.8h1.8a.8.8 0 0 1 .8.8v1.3" />
    <path d="M4.1 4.3l.6 8.2a1 1 0 0 0 1 .9h4.6a1 1 0 0 0 1-.9l.6-8.2" />
  </>
));
export const IconCopy = icon('IconCopy', () => (
  <>
    <rect x="5.75" y="5.75" width="8.5" height="8.5" rx="1.75" />
    <path d="M10.25 5.75V3.5a1.75 1.75 0 0 0-1.75-1.75H3.5A1.75 1.75 0 0 0 1.75 3.5v5a1.75 1.75 0 0 0 1.75 1.75h2.25" />
  </>
));
export const IconSearch = icon('IconSearch', () => (
  <>
    <circle cx="7" cy="7" r="4.3" />
    <path d="M10.2 10.2 13.8 13.8" />
  </>
));
export const IconFilter = icon('IconFilter', () => p('M2.5 3.5h11L9.3 8.4v4.4L6.7 14V8.4z'));
export const IconMore = icon('IconMore', () => (
  <>
    <circle cx="3.4" cy="8" r="1.1" style={{ fill: 'currentColor' }} stroke="none" />
    <circle cx="8" cy="8" r="1.1" style={{ fill: 'currentColor' }} stroke="none" />
    <circle cx="12.6" cy="8" r="1.1" style={{ fill: 'currentColor' }} stroke="none" />
  </>
));
export const IconMenu = icon('IconMenu', () => p('M2.5 4.5h11M2.5 8h11M2.5 11.5h11'));
export const IconExternal = icon('IconExternal', () => (
  <>
    <path d="M13.5 8.8v3.4a1.3 1.3 0 0 1-1.3 1.3H3.8a1.3 1.3 0 0 1-1.3-1.3V3.8a1.3 1.3 0 0 1 1.3-1.3h3.4" />
    <path d="M10 2.5h3.5V6M13.5 2.5 7.8 8.2" />
  </>
));
export const IconPlay = icon('IconPlay', () => p('M4.5 2.8 12.5 8l-8 5.2z'));

/* -- Direction and disclosure --------------------------------------------- */

export const IconChevronDown = icon('IconChevronDown', () => p('M4 6l4 4 4-4'));
export const IconChevronUp = icon('IconChevronUp', () => p('M4 10l4-4 4 4'));
export const IconChevronRight = icon('IconChevronRight', () => p('M6 4l4 4-4 4'));
export const IconChevronLeft = icon('IconChevronLeft', () => p('M10 4 6 8l4 4'));
export const IconArrowRight = icon('IconArrowRight', () => p('M2.5 8h11M9.5 4l4 4-4 4'));
export const IconArrowLeft = icon('IconArrowLeft', () => p('M13.5 8h-11M6.5 4 2.5 8l4 4'));
export const IconArrowBoth = icon('IconArrowBoth', () =>
  p('M2.5 8h11M5 5.5 2.5 8 5 10.5M11 5.5 13.5 8 11 10.5'),
);

/* -- Status and meaning ---------------------------------------------------- */

export const IconAlert = icon('IconAlert', () => p('M8 2.5 14 13H2zM8 6.5v3M8 11.2v.3'));
export const IconInfo = icon('IconInfo', () => (
  <>
    <circle cx="8" cy="8" r="5.8" />
    <path d="M8 7.3v3.6M8 5.2v.3" />
  </>
));
export const IconClock = icon('IconClock', () => (
  <>
    <circle cx="8" cy="8" r="5.8" />
    <path d="M8 4.6V8l2.4 1.6" />
  </>
));
export const IconLock = icon('IconLock', () => (
  <>
    <rect x="3.2" y="7" width="9.6" height="6.5" rx="1.4" />
    <path d="M5.6 7V5.2a2.4 2.4 0 0 1 4.8 0V7" />
  </>
));
export const IconUnlock = icon('IconUnlock', () => (
  <>
    <rect x="3.2" y="7" width="9.6" height="6.5" rx="1.4" />
    <path d="M5.6 7V5.2a2.4 2.4 0 0 1 4.6-.8" />
  </>
));
export const IconEye = icon('IconEye', () => (
  <>
    <path d="M1.5 8S3.9 3.8 8 3.8 14.5 8 14.5 8 12.1 12.2 8 12.2 1.5 8 1.5 8" />
    <circle cx="8" cy="8" r="1.9" />
  </>
));
export const IconShield = icon('IconShield', () =>
  p('M8 1.8 13.2 3.6v4.2c0 3-2.2 5.4-5.2 6.4-3-1-5.2-3.4-5.2-6.4V3.6z'),
);
export const IconGlobe = icon('IconGlobe', () => (
  <>
    <circle cx="8" cy="8" r="5.8" />
    <path d="M2.2 8h11.6M8 2.2a9 9 0 0 1 0 11.6 9 9 0 0 1 0-11.6" />
  </>
));
export const IconRelay = icon('IconRelay', () =>
  p('M4 5.5a5 5 0 0 0 0 5M12 5.5a5 5 0 0 1 0 5M6 7a2.4 2.4 0 0 0 0 2M10 7a2.4 2.4 0 0 1 0 2M8 7.6v.8'),
);
export const IconPlug = icon('IconPlug', () => p('M6 2v3.5M10 2v3.5M4 5.5h8v2a4 4 0 0 1-8 0zM8 11.5V14'));
export const IconTerminal = icon('IconTerminal', () => (
  <>
    <rect x="1.8" y="2.8" width="12.4" height="10.4" rx="1.4" />
    <path d="M4.6 6.4 6.8 8.4l-2.2 2M8.6 11h3" />
  </>
));
export const IconBook = icon('IconBook', () =>
  p(
    'M3 3h4.2a1.8 1.8 0 0 1 1.8 1.8V13a1.5 1.5 0 0 0-1.5-1.2H3zM13 3H8.8a1.8 1.8 0 0 0-1.8 1.8V13a1.5 1.5 0 0 1 1.5-1.2H13z',
  ),
);
export const IconLink = icon('IconLink', () =>
  p('M6.6 9.4a2.6 2.6 0 0 0 3.9.3l2-2a2.6 2.6 0 0 0-3.7-3.7l-1.1 1.1M9.4 6.6a2.6 2.6 0 0 0-3.9-.3l-2 2a2.6 2.6 0 0 0 3.7 3.7l1.1-1.1'),
);

/* -- Theme ----------------------------------------------------------------- */

export const IconSun = icon('IconSun', () => (
  <>
    <circle cx="8" cy="8" r="3" />
    <path d="M8 1.4v1.4M8 13.2v1.4M1.4 8h1.4M13.2 8h1.4M3.3 3.3l1 1M11.7 11.7l1 1M12.7 3.3l-1 1M4.3 11.7l-1 1" />
  </>
));
export const IconMoon = icon('IconMoon', () =>
  p('M13.2 9.4A5.6 5.6 0 0 1 6.6 2.8a5.8 5.8 0 1 0 6.6 6.6'),
);
export const IconSystem = icon('IconSystem', () => (
  <>
    <rect x="1.8" y="2.8" width="12.4" height="8.4" rx="1.4" />
    <path d="M5.8 13.6h4.4" />
  </>
));
