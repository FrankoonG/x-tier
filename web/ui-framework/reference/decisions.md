# Design decisions

Every non-trivial departure from the inherited hy2scale design, and every place
this framework chose something other than the obvious default. Each entry says
what was rejected and why, so a future maintainer can reverse a decision on
purpose rather than by accident.

---

## D-001 — No animation library

**Decision.** Motion is pure CSS: `@starting-style`, `transition-behavior:
allow-discrete`, and a `usePresence` hook that waits on real
`Element.getAnimations()` promises. Zero runtime animation dependency.

**Rejected.** `motion` 13.1 (the framer-motion successor), which the inherited
codebase used throughout.

**Why.** The inherited implementation was 29–42 KB gzip and carried three
defects that are structural to the library's exit-animation model, not
incidental bugs:

- `Modal.tsx:38-48` — a 500 ms `setTimeout` plus direct DOM mutation to
  suppress pointer events during exit. Needed because `AnimatePresence`
  snapshots the React tree at exit, so prop updates never reach the exiting
  subtree. The comment in that file records that `onAnimationStart` /
  `onAnimationComplete` were tried first and proved unreliable.
- `Modal.tsx:132` — `count.current++` executed during render, to decide whether
  the body height animation should play. Under React's concurrent double-invoke
  this counts twice and skips a real animation.
- `TreeTable.tsx:114` — `AnimatePresence` wrapping `<tbody>`. Its only direct
  child is the always-present `<tbody>`, so the `motion.tr` exit animations
  inside it never ran at all. The code was written, shipped, and silently dead.

None of these are expressible in CSS, because CSS has no exit snapshot: the
browser interpolates an element out of the DOM natively. A fourth defect —
`useReducedMotion()` being non-reactive, snapshotting at mount with a literal
`TODO` in the source — also disappears, since a media query is inherently live.
That one matters here specifically: an operations panel stays open for hours,
across a user changing their accessibility settings.

**Cost.** Gesture-driven and FLIP/shared-element transitions would need hand
rolling. Neither appears in the component inventory, and the topology graph's
drag is direct pointer manipulation, not physics.

---

## D-002 — The motion *language* is inherited verbatim

**Decision.** The signature curve `cubic-bezier(0.16, 1, 0.3, 1)`, the
470 ms / 270 ms modal split, and the `stiffness: 300, damping: 30` spring feel
are all preserved exactly. Only the mechanism changed (D-001).

**Why.** The inherited animation was genuinely good and its best property is
easy to lose by accident: **opacity always finishes faster than transform.**
The element is fully visible while it is still settling, so it reads as
"arrived" well before it stops moving. Reversing the ratio at identical
durations feels sluggish. That is craft, not decoration, so it is encoded
structurally as paired `--stratum-transition-*-enter` / `-exit` tokens rather
than left to each component to remember.

The spring is reproduced by sampling the actual damped-oscillator solution

```
x(t) = 1 − e^(−ζω₀t)(cos(ω_d t) + (ζω₀/ω_d)·sin(ω_d t))
```

into a CSS `linear()` easing, so the overshoot is the real curve rather than an
eyeballed approximation. Settle times were computed, not guessed: 480 ms for
300/30, 363 ms for 500/35, 577 ms for 210/24.

---

## D-003 — Reduced motion reduces; it does not remove

**Decision.** Three independent multipliers:
`--stratum-motion-scale` (durations, 1 → 0.5),
`--stratum-motion-distance` (translation, 1 → 0),
`--stratum-motion-zoom` (scale, 1 → 0).

**Rejected.** The common `animation: none !important` blanket rule.

**Why.** Users set the preference because of vestibular response to large
translation and scale, not because a 120 ms fade is a problem. Removing all
motion destroys the state-change feedback a dense operator UI depends on —
"did that row just update?" becomes unanswerable. Splitting the axes keeps
every fade and every colour transition while stopping all movement.

Looping animation is the exception and is genuinely disabled: spinners stop
rotating (and pulse instead), shimmer stops, topology flow stops. A perpetual
motion loop is exactly what the preference exists to suppress.

---

## D-004 — Palette rebuilt in OKLCH; primary moved off blue

**Decision.** New three-tier palette authored in OKLCH. Primary is a
violet-leaning indigo at hue 277, not the inherited `#4361ee`.

**Why OKLCH.** It is perceptually uniform, so step 500 of every hue reads at the
same lightness. In the inherited sRGB palette, green `#22c55e` (L≈73%) and red
`#ef4444` (L≈63%) were nominally the same weight but needed different
foreground treatments — a recurring source of one-off overrides.

**Why move the hue.** The mesh palette needs blue for `self`. If the interactive
accent is also blue, an operator cannot tell at a glance whether a blue element
is "you" or "clickable". Separating them by hue removes an entire class of
misreading. Indigo-277 also sits clear of all four status hues.

**Verified, not asserted.** Every mesh colour was checked against WCAG 2.2
SC 1.4.11 (3:1 for meaningful graphics). Two initial choices failed and were
changed: `native` went from 2.99:1 to 3.64:1, `nested` from 2.53:1 to 4.75:1.

---

## D-005 — Tokens split into three tiers, with mesh colours as literal hex

**Decision.** Tier 1 primitives (OKLCH ramps) → tier 2 semantic roles →
component-private `--_*` properties. Components may only touch tier 2. The
mesh/topology palette is a separate file of **literal sRGB hex**.

**Why the mesh exception.** SVG paint that resolves through `var()` is
unreliable under dark-mode browser extensions: the analyser reads the literal
declaration, cannot resolve the variable, and either skips the element or
inverts it inconsistently — producing the "half the topology inverted" failure.
Literal hex and `currentColor` are both substituted reliably. This is the one
place where consistency loses to a real deployment constraint.

**Why not `light-dark()`.** It is Baseline, and it was still rejected:
(a) it is ignored entirely under `forced-colors: active`, so a separate layer is
needed regardless; (b) dark-mode extensions decide a site is already dark by
sampling computed background colours and do **not** honour `color-scheme`, so it
buys nothing against the constraint that motivated it; (c) an explicit
`[data-theme]` attribute is inspectable and assertable in Playwright.

The inherited codebase had no real token layer at all — 18 variables in
`tokens.css` against 232 scattered through `components.css`.

---

## D-006 — Native dark mode, which the predecessor never had

**Decision.** A complete second theme, plus `prefers-contrast: more` and
`forced-colors: active` layers.

**Why.** `components.css:526` in the inherited codebase states it plainly: *"The
app has no native dark chrome, so we don't ship a prefers-colour-scheme
fallback."* Dark mode was entirely delegated to browser extensions. Users who
wanted dark got whatever Dark Reader inferred.

Dark is not an inversion. Surfaces climb toward the viewer (`bg` darkest,
each elevation step lighter) because a black shadow on a near-black panel is
invisible; body text backs off from pure white to `neutral-100` to avoid
halation; subtle status fills become low-alpha washes; series colours shift one
step lighter to hold saturation against a dark plot area.

---

## D-007 — Sharper radii, tighter type, denser spacing

**Decision.** Radius 6 px default (inherited: 10 px). Body 13 px with a tight
ramp (11/12/13/14/16/18/22). Spacing has half-steps at the low end (2 px, 6 px,
10 px, 14 px).

**Why.** These are instruments, not cards. Tighter corners raise apparent
density at identical padding, and a compressed type ramp forces hierarchy to
come from weight and colour rather than from size jumps that waste vertical
space. A table row showing six fields has to stay readable; every 2 px of
leading is a row that does not fit on screen.

---

## D-008 — Data attributes are the styling contract

**Decision.** `data-stratum` / `data-variant` / `data-size` / `data-state`,
inside `@layer stratum.components`, with per-component `--_*` properties.

**Why.** Cascade layers mean unlayered consumer CSS always wins, so overriding
a component never needs `!important` or a specificity fight. Data attributes
survive class-name mangling and give tests stable selectors. The `--_*`
properties let a consumer retheme one instance without touching selectors at
all.

---

## D-009 — `unknown` is a first-class status role

**Decision.** A dedicated `unknown` semantic role, and every component that
displays state must distinguish *not observed* from *observed and negative*.
`null` never formats as `0`.

**Why.** This is the framework's central discipline and it comes from the
domain. The systems it targets deliberately report only confirmed-available
capabilities: absence may mean unavailable, unprobed, irrelevant, or simply not
reported by that peer. Rendering "we did not look" identically to "we looked and
it is off" is how a panel ends up asserting something the data never said.

The same failure caused years of bugs in the predecessor — its own architecture
notes attribute the recurring inconsistencies to multiple distinct layers being
collapsed into one `peer` object and one boolean.

Concretely: `StateMatrix` offers no aggregate status at all, `CapabilityGrid` is
tri-state, unobserved metrics render as `—` in the sans face rather than a
zero in the mono face, and `Sparkline` breaks the line at a null instead of
interpolating across it.

---

## D-010 — Bespoke components on Floating UI, not a headless kit

**Decision.** All primitives are written here. `@floating-ui/react` handles
positioning, dismiss layers, and focus management. Nothing else.

**Rejected.** Base UI (the current default recommendation) and Radix.

**Why.** The brief calls for an independent framework whose own tokens are the
final visual authority. Floating UI is the piece worth borrowing because
positioning, collision flipping and dismiss-layer stacking are where hand-rolled
overlays actually break — and it is what Base UI and Radix use internally
anyway. Taking it directly gets the hard part without inheriting a component
API. CSS anchor positioning is not yet Baseline, so it is not an alternative.

---

## D-011 — The lab replaces Storybook

**Decision.** A first-party Vite app under `web/lab` renders every specimen,
with theme, reduced-motion and density switches, and doubles as the Playwright
target.

**Why.** Storybook is a large dependency with its own build and its own
opinions, and it would be the single biggest thing in this repo's `node_modules`.
The lab is a few hundred lines, hot-reloads framework source directly, and is
itself a demonstration that the framework can build a real UI — it is written
using only public Stratum tokens.

---

## D-012 — Selection behaviour inherited unchanged

**Decision.** Checkbox toggles additively; a click on the row body selects
exclusively; clicking the sole selected row deselects it; the entire checkbox
cell including its padding is the hit target; an `isInteractiveDescendant` guard
prevents a click on an inline control from also selecting the row.

**Why.** This is the one piece of interaction design inherited without
modification. The source comments record it as coming from observed user error —
"accidental row-clicks while aiming at a small checkbox were the most common
error in user testing". That is real evidence, and re-deriving it from first
principles would only reproduce it.

---

## D-013 — The framework ships no user-visible copy

**Decision.** Every string is a prop with an English default. No i18n library.

**Why.** Localisation belongs to the application. A framework that imports i18next
forces its choice on every consumer and makes the library untestable in
isolation. Props with defaults let an app pass translated strings from whatever
system it already uses.

---

## D-014 — Heavy data dependencies are optional peers

**Decision.** `@tanstack/react-table` and `@tanstack/react-virtual` are optional
peer dependencies, imported only inside the components that need them, with a
clear runtime error when absent.

**Why.** A consumer building a settings panel should not pay for a virtualised
grid engine. Charts, the log viewer and the diff viewer are bespoke for the same
reason — pulling in ECharts or a diff library for one view would dwarf the rest
of the library.

---

## D-015 — An open combobox hides the rest of the page, on purpose

**Observed.** While a Combobox popup is open, axe reports `aria-hidden-focus`
against focusable elements elsewhere on the page.

**Investigated, not suppressed.** This is Floating UI behaving as designed.
`@floating-ui/react/dist/floating-ui.react.mjs:1992`:

```js
const cleanup = modal || isUntrappedTypeableCombobox
  ? markOthers(insideElements, !useInert, useInert)
  : markOthers(insideElements);
```

A non-modal *typeable* combobox — exactly this component — deliberately marks
the rest of the document `aria-hidden` for as long as its listbox is open, so a
screen-reader user in browse mode does not read stale page content behind the
open list. Our Combobox is already correctly configured (`modal={false}`,
`initialFocus={-1}`), and DOM focus never leaves the input.

**What changed.** Nothing in the framework. Two lab specimens used
`defaultOpen` to display the loading and empty states, which pinned a popup
open permanently — a state no real panel is ever in, and one that also
overlapped neighbouring specimens in the screenshot grid. The specimens now
render closed and the note asks the reader to open them.

Recorded here because the next person to see that axe result should not "fix"
it by removing the focus manager.

---

## D-016 — Contrast was measured, and it changed the palette twice

The first axe pass returned over two hundred contrast failures. They came from
three mistakes, all of which are now rules in `PATTERNS.md`:

1. **Status colours used as text.** `--stratum-success` and friends are tuned
   for dots and fills at the 3:1 graphics threshold; as text they measure
   ~3.8:1 and fail the 4.5:1 body-text threshold. Every component that showed a
   coloured value now uses the `-fg` variant for the glyph and keeps the base
   token for the marker beside it.
2. **`--stratum-text-subtle` was itself below AA.** It pointed at `neutral-500`,
   which measures 3.87:1 on white. A `neutral-550` half-step was added at 54%
   lightness — the lightest step that clears 4.5:1 on both the white surface
   and the page background.
3. **Dark-mode solid fills paired with white labels.** White on the dark
   theme's `indigo-500` measures 3.84:1 and on `red-500` 3.81:1. Dark keeps its
   accent bright so it still reads as a colour against near-black chrome, so
   the label goes dark instead: near-black gives 5.42:1 and 5.47:1. The
   success, warning and info rows already did this; accent and danger were an
   oversight.

Every replacement value was computed from the OKLCH source and re-measured in
the browser rather than eyeballed.

---

## D-017 — The build preserves modules, because bundling defeated tree-shaking

**Found by the independence test**, not by inspection: copying the library into
an empty Vite project and building it.

Two packaging defects surfaced, both invisible in the dev server:

1. **A single `dist/index.js` is not tree-shakeable in practice.** Every
   component is a module-scope `forwardRef(...)` call, which a bundler cannot
   prove side-effect-free, so importing one Button pulled in the entire
   library — 621 kB against 204 kB for the same app built from source. The lib
   build now uses `preserveModules`, mirroring the source tree into 114 files
   so per-file shaking works and the `sideEffects` field can do its job. A
   Button-only app is back to 204 kB, and Button alone measures 0.9 kB gzipped.

2. **The optional peers were not optional.** `DataTable` dynamically imports
   `@tanstack/react-table` and handles its absence with a clear error, but a
   *literal* specifier is resolved statically by both TypeScript and the
   bundler — so merely re-exporting `DataTable` from the barrel broke any
   consumer who had not installed two packages they never use. The specifiers
   are now hoisted into constants, which nothing resolves until the component
   actually runs.

Neither would have been caught by the test suite or the lab. The independence
test is cheap and belongs in CI.

---

## D-018 — Constant-time path draw

**Decision.** When the topology animates a selected path, per-edge
`animation-delay` and `animation-duration` are computed from each edge's share
of the total path length, so a 5-hop path draws in exactly the same wall-clock
time as a 1-hop path.

**Why.** Inherited from the predecessor and worth calling out because it is
counter-intuitive: the naive implementation gives each edge a fixed duration, so
a long path feels progressively more sluggish precisely when the operator is
most interested in it. Constant total time makes every path feel equally
responsive.
