# Stratum UI — component authoring patterns

Every component in this library follows the rules below. They exist so that 95
components written by different hands still behave like one system.

Read `src/components/Button/Button.tsx` + `Button.css` first — it is the
reference implementation and demonstrates nearly every rule here.

---

## 1. File layout

```
src/components/<Name>/
  <Name>.tsx      component + its exported types
  <Name>.css      colocated styles, imported by the .tsx
```

Sub-components that are never used alone (`Table.Row`) live in the same folder.
Add the export to `src/index.ts`, **pairing the value and type exports** —
consumers must be able to type a wrapper without reaching into `dist/`.

---

## 2. The styling contract is data attributes, not class names

```tsx
<div
  data-stratum="badge"     // component identity — stable selector for tests
  data-variant={variant}
  data-size={size}
  data-state={state}       // open | closed | entering | exiting | checked …
  className={clsx('stratum-badge', className)}
/>
```

```css
@layer stratum.components {
  .stratum-badge { … }
  .stratum-badge[data-variant='danger'] { … }
}
```

Rules:

- Always wrap CSS in `@layer stratum.components`. Consumers override with plain
  unlayered CSS and never need `!important`.
- Always accept and merge `className`. Always spread `...rest` onto the root.
- Never use element selectors (`div > span`) — only classes and data
  attributes. A consumer swapping the element must not lose styling.
- Never hardcode a colour, size, radius, duration or easing. Only tier-2
  semantic tokens (`--stratum-*`). If a token is missing, add it to
  `tokens.semantic.css` rather than inlining a value.
- Expose per-component private custom properties (`--_bg`, `--_fg`) so a
  consumer can retheme one instance without a specificity fight. Prefix them
  with `--_` so they read as internal.

---

## 3. Motion is CSS-first. There is no animation library.

Enter/exit uses `@starting-style` + `transition-behavior: allow-discrete`:

```css
.stratum-popover {
  opacity: 1;
  translate: 0 0;
  transition: var(--stratum-transition-popover-enter), display allow-discrete;
}
@starting-style {
  .stratum-popover[data-state='entering'] {
    opacity: 0;
    translate: 0 calc(-1 * var(--stratum-lift-md));
  }
}
.stratum-popover[data-state='exiting'] {
  opacity: 0;
  translate: 0 calc(-1 * var(--stratum-lift-md));
  transition: var(--stratum-transition-popover-exit);
}
```

Use `usePresence()` when the element must actually unmount. It waits on real
`getAnimations()` promises, so durations live only in CSS.

Hard rules:

- **Opacity is always faster than transform.** Use the paired
  `--stratum-transition-*-enter` / `-exit` tokens, which already encode this.
- Multiply every translate/scale by `--stratum-motion-distance` /
  `--stratum-motion-zoom` (or use `--stratum-lift-*` / `--stratum-zoom-*`,
  which already do). This is what makes reduced-motion work.
- Animate only `opacity`, `transform`/`translate`/`scale`, `filter`,
  `clip-path`, `background-color`. Anything else risks layout thrash.
- Never put `will-change` in a static rule. Set it in JS immediately before an
  animation and remove it after, or not at all.
- Never animate `layout` on table rows. Virtualised lists must not animate
  row entry — it is the classic source of scroll jank.
- Only anomalies loop. A resting component never animates.

---

## 4. Accessibility is not optional

Baseline for every component:

- Keyboard operable with no mouse. Arrow keys for composite widgets (menu,
  tabs, radio group, listbox), `Home`/`End`, type-ahead where a list is long.
- Visible focus via the global `:focus-visible` rule. If `outline` would be
  clipped by a scroll container, add `.stratum-focus-inset`.
- Correct roles and relationships. `aria-expanded`, `aria-controls`,
  `aria-selected`, `aria-current`, `aria-describedby` for errors,
  `aria-invalid` on invalid fields.
- Icon-only controls **must** have `aria-label`. `title` is not a substitute.
  Log a dev-mode `console.error` if it is missing, as Button does.
- Never convey meaning by colour alone — pair with an icon, shape, border
  style or text.

### Colour has two grades. Do not mix them.

This single mistake produced over two hundred axe failures on the first pass,
so it gets its own rule.

| Grade | Tokens | Threshold | Used for |
|---|---|---|---|
| **Graphic** | `--stratum-success`, `--stratum-danger`, `--stratum-unknown`, `--stratum-mesh-*` | 3:1 (SC 1.4.11) | fills, dots, rings, strokes, borders |
| **Text** | `--stratum-success-fg`, `--stratum-danger-fg`, `--stratum-unknown-fg`, `--stratum-mesh-hop-*` | 4.5:1 (SC 1.4.3) | any glyph a human reads |

A status colour that looks right on a 7px dot measures ~3.8:1 as body text and
fails. If one component needs both — a coloured value with a coloured dot beside
it — split the custom property in two:

```css
.thing[data-status='ok'] { --_fg: var(--stratum-success-fg); --_mark: var(--stratum-success); }
.thing__text   { color: var(--_fg); }
.thing__marker { background: var(--_mark); }
```

Two further traps:

- **`--stratum-text-disabled` is below AA on purpose.** WCAG exempts inactive
  controls, and a disabled field at full contrast does not look disabled. It is
  legal *only* on a genuinely disabled control. A description, a legend label,
  an "unobserved" placeholder or an unselected legend item is real content —
  use `--stratum-text-muted` or `--stratum-text-subtle`.
- **Never stack `opacity` on text that is near the threshold.** Opacity
  multiplies contrast down. Express a secondary state with a different token or
  a non-colour channel instead.
- `loading` should set `aria-busy` and swallow activation. Do **not** set
  `disabled`: it removes the element from the tab order and destroys focus
  under a keyboard user mid-action.
- Support `forced-colors: active`. Fills and shadows disappear there, so any
  boundary that carries meaning also needs a real border.

---

## 5. State: controlled and uncontrolled from one implementation

Use `useControllableState`. `value` + `onChange` for controlled, `defaultValue`
for uncontrolled, and `onChange` fires in both modes.

Never mutate a ref during render. If you need "has this settled yet",
`useMeasure` exposes `hasSettled` from inside an effect.

---

## 6. Text belongs to the consumer

The framework hardcodes no user-visible copy. Every string is a prop with an
English default:

```tsx
export interface PaginationProps {
  labelPrevious?: string;   // default 'Previous'
  labelNext?: string;       // default 'Next'
}
```

Never import an i18n library here. Localisation is the application's job.

---

## 7. Domain neutrality

This package must contain **zero x-tier knowledge**. No `NodeID`, no
`RouteIntent`, no daemon endpoints. Network-layer components take generic
shapes (`{ id, label, status }`) so the library drops into any panel.

The acceptance test is literal: copying `ui-framework/` into an empty Vite
project must work with no edits.

---

## 8. Optional heavy dependencies

`@tanstack/react-table` and `@tanstack/react-virtual` are optional peers.
Import them only inside the component that needs them, and throw a clear error
if missing:

```ts
if (!useReactTable) {
  throw new Error('[stratum] <DataTable> requires @tanstack/react-table. Install it.');
}
```

---

## 9. A scroll container must also be a containing block

Any rule that declares `overflow: auto` or `overflow: scroll` must also declare
`position: relative`.

This is not stylistic. A scroll container clips an absolutely positioned
descendant **only when it is that descendant's containing block** — that is, only
when it is itself positioned. Leave it `static` and the abspos boxes inside it
resolve against whatever ancestor happens to be positioned next, which may be the
initial containing block: the page. Their static position is derived from the full
intrinsic width of the scrolled content, so that width reappears as real overflow
somewhere further out, while the scroller itself measures clean.

Every component here puts `.stratum-visually-hidden` labels inside its content, so
every unpositioned scroller leaks. Measured before the fix: `DiffViewer` scrolled
its own body correctly and still pushed 332px of overflow onto the lab canvas at a
900px viewport, from a11y labels alone. The same mechanism on the vertical axis
grew the document to 2766px inside an 800px viewport — invisible, because the
shell is `overflow: hidden`, but a hidden overflow is still *programmatically*
scrollable, so one `focus()` on an escaped node scrolled the entire shell out of
view with no scrollbar to bring it back.

Floating UI popups are the one exception: they receive `position: fixed` from an
inline style at runtime, so they are already containing blocks and a CSS
`position` on them would be dead.

Audit with:

```
node tools/scan-scrollers.mjs ui-framework/src
```

It prints `NEEDS` for any rule that scrolls without positioning. The only expected
hits are the five floating popups.

---

## 10. Checklist before you call a component done

- [ ] Typechecks (`npx tsc --noEmit` in `ui-framework/`)
- [ ] Exported from `src/index.ts`, value **and** types
- [ ] Specimens added to `lab/src/specimens.tsx`
- [ ] Readable in light and dark
- [ ] Full keyboard operation, visible focus
- [ ] Sensible under `prefers-reduced-motion: reduce`
- [ ] No hardcoded colour, size or duration
- [ ] `className` merged, `...rest` spread, ref forwarded
- [ ] Every `overflow: auto|scroll` rule also sets `position: relative` (§9)
- [ ] Any `useFloating` call passes `strategy: 'fixed'` and `transform: false`
