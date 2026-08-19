# Stratum UI

A React component framework for **network operations panels** — the kind of
dense, live, keyboard-driven console you build for a proxy manager, a mesh
controller, an SDN dashboard, a firewall or a router admin.

Generic component kits stop at a presentational table. This one is built around
the parts they leave out: topology graphs, multi-hop path cells, connection
state that has five independent axes, virtualised log panes, CIDR and port-range
inputs, latency heatmaps, and telemetry that never jitters as it updates.

- **No UI-kit dependency.** React, `@floating-ui/react` and `clsx`. That is all.
- **No animation library.** Motion is CSS — `@starting-style`,
  `transition-behavior: allow-discrete`, and a presence hook that waits on real
  `getAnimations()` promises.
- **Light, dark, high-contrast and forced-colors**, all first-class.
- **Accessible by construction.** Full keyboard operation, correct ARIA, and
  no information carried by colour alone.
- **Domain-neutral.** Nothing in here knows what it is being used to manage.

---

## Install

```bash
npm install @stratum/ui
```

```tsx
import { ThemeProvider, Button, Table } from '@stratum/ui';
import '@stratum/ui/styles.css';
```

`DataTable` — the engine-backed grid with virtualisation and column sizing —
ships from its own entry point, because it is the only component with
dependencies of its own:

```bash
npm install @tanstack/react-table @tanstack/react-virtual
```
```tsx
import { DataTable } from '@stratum/ui/data-table';
```

Everything else, including the plain `Table` (sorting, selection, sticky
headers, density), needs nothing beyond the two dependencies above. Keeping
DataTable on a subpath is what makes those peers genuinely optional — see
`reference/decisions.md`.

---

## Quick start

```tsx
import { ThemeProvider, AppShell, Sidebar, Button, themeInitScript } from '@stratum/ui';
import '@stratum/ui/styles.css';

export function App() {
  return (
    <ThemeProvider>
      <AppShell
        sidebar={<Sidebar sections={sections} activeKey={page} onSelect={setPage} />}
        topbar={<Topbar />}
      >
        <YourPage />
      </AppShell>
    </ThemeProvider>
  );
}
```

To avoid a flash of the light theme on first paint, inline the init script in
`<head>` before any stylesheet:

```html
<script>/* contents of themeInitScript */</script>
```

---

## What is in it

**Foundation** — three-tier token system (OKLCH primitives → semantic roles →
component-private properties), motion tokens, cascade layers, `Slot` /
`VisuallyHidden`, and hooks: `usePresence`, `useControllableState`,
`useMeasure`, `useMediaQuery`, `useReducedMotion`, `useEventCallback`.

**Primitives** — Button, Input, Textarea, NumberInput, PasswordInput,
SearchInput, Select, Combobox, MultiSelect, Checkbox, Radio, Switch, Slider,
SegmentedControl, Field, FormGrid, Fieldset, Dialog, Drawer, Popover, Tooltip,
Menu, ContextMenu, Tabs, Accordion, Disclosure, Breadcrumb, Pagination, Steps,
Toast, Banner, InlineMessage, Progress, Meter, Skeleton, Spinner, Card, Panel,
Badge, Tag, Avatar, Separator, Kbd, Code, Snippet, CopyButton, EmptyState,
ErrorState, ScrollArea.

**Data-dense** — Table, DataTable (sorting, virtualisation, sticky header and
columns, expandable rows), TreeTable, Sparkline, TimeSeriesChart, Heatmap,
Gauge, BarSeries, LogViewer (virtualised, ANSI-aware, follow-tail), DiffViewer,
JsonViewer, CodeBlock.

**Network** — TopologyGraph, PathChain, StateMatrix, CapabilityGrid, StatusDot,
HealthBar, DegradationLadder, ConnectionStateBadge, ReachabilityBadge,
AddressInput, PortRangeInput, CredentialField, ProtocolPicker, and metric
components with matching pure formatters.

**Patterns** — AppShell, Sidebar, Topbar, PageHeader.

---

## Two ideas the library is built on

### Not observed is not the same as off

The systems this framework targets report only what they have confirmed.
A capability that is absent may be unavailable, unprobed, irrelevant, or simply
not something that peer reports — and rendering "we did not look" identically to
"we looked and it is off" is how a panel ends up asserting something the data
never said.

So `unknown` is a first-class status role, `null` never formats as `0`,
`CapabilityGrid` is tri-state, and `Sparkline` breaks its line at a gap rather
than interpolating across it.

### Orthogonal state stays orthogonal

Whether a peer's identity is verified, whether it is a member of your group,
whether you can reach it right now, whether it is authorised to relay, and what
the runtime is actually doing are five separate facts. Collapsing them into one
status dot is convenient and eventually wrong.

`StateMatrix` therefore offers no aggregate at all — no overall colour, no
summary badge. A caller who wants a headline has to decide the roll-up rule and
own it.

---

## Theming

Override semantic tokens anywhere in your own CSS:

```css
:root {
  --stratum-accent: oklch(58% 0.17 195);   /* teal instead of indigo */
  --stratum-radius-md: 2px;                /* sharper */
}
```

Component styles live in `@layer stratum.components`, and unlayered CSS beats
layered CSS, so plain author styles always win — overriding a component never
needs `!important`.

Theme is switched by `data-theme="light" | "dark"` on `<html>`; `ThemeProvider`
owns that attribute and tracks the system preference live.

See `reference/decisions.md` for why `light-dark()` is deliberately not used.

---

## Accessibility

Targets WCAG 2.2 AA. Every component is keyboard-operable, exposes correct
roles and relationships, and pairs any colour signal with a shape, icon or text
channel. `prefers-reduced-motion` halves durations and stops all translation and
scale rather than removing feedback entirely, and every looping animation is
disabled outright.

Verified with `@axe-core/playwright` in both themes; critical and serious
violations fail the build.

---

## Docs

- `reference/decisions.md` — every non-trivial design decision and what it rejected
- `reference/motion-spec.md` — the complete motion reference
- `PATTERNS.md` — authoring rules for contributors

## Licence

MIT
