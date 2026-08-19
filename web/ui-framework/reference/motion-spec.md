# Motion specification

The authoritative values live in `src/styles/tokens.motion.css`. This is the
reference table plus the rules that are not expressible as tokens.

---

## Principles

1. **Motion serves state perception.** It answers "did that just change?" and
   "did my action take effect?". It is never decoration.
2. **Restraint.** At most four distinct motion types running on one screen.
3. **Fast.** Ordinary interaction under 300 ms; complex transition under 500 ms.
   470 ms is the ceiling and only the dialog uses it.
4. **Calm at rest, conspicuous when abnormal.** Nothing loops in a healthy
   steady state. Pulse and flow are reserved for anomaly and live traffic.
5. **Opacity finishes before transform.** Always. See below.

---

## The opacity/transform split

This is the single most important detail inherited from the predecessor.

| Surface | transform | opacity |
|---|---|---|
| Dialog, drawer (enter) | 470 ms | 250 ms |
| Dialog, drawer (exit) | 320 ms | 150 ms |
| Popover, tooltip, menu (enter) | 250 ms | 150 ms |
| Popover, tooltip, menu (exit) | 120 ms | 120 ms |

The element reaches full opacity while it is still settling into place, so it
reads as *arrived* well before it stops moving. Swapping the ratio at identical
durations feels sluggish. Use the paired
`--stratum-transition-overlay-enter` / `-exit` and
`--stratum-transition-popover-enter` / `-exit` tokens — they encode this so a
component cannot get it wrong by hand.

---

## Durations

| Token | Value | Used for |
|---|---|---|
| `--stratum-duration-2xs` | 80 ms | hover, press, focus feedback |
| `--stratum-duration-xs` | 120 ms | popover exit |
| `--stratum-duration-sm` | 150 ms | small state change, dropdown |
| `--stratum-duration-md` | 200 ms | default |
| `--stratum-duration-lg` | 250 ms | popover enter, toast |
| `--stratum-duration-xl` | 320 ms | tab change, accordion, overlay exit |
| `--stratum-duration-2xl` | 470 ms | dialog enter — the ceiling |
| `--stratum-duration-spring` | 480 ms | measured height, page enter |
| `--stratum-duration-spring-snappy` | 363 ms | list item settle |
| `--stratum-duration-spring-gentle` | 577 ms | large surfaces |

All are multiplied by `--stratum-motion-scale`.

---

## Easing

| Token | Curve | Character |
|---|---|---|
| `--stratum-ease-signature` | `cubic-bezier(0.16, 1, 0.3, 1)` | hard expo-out. ~80% of the distance in the first third. The default for anything entering. |
| `--stratum-ease-out` | `cubic-bezier(0, 0, 0.2, 1)` | standard decelerate |
| `--stratum-ease-in` | `cubic-bezier(0.4, 0, 1, 1)` | standard accelerate — for things leaving |
| `--stratum-ease-in-out` | `cubic-bezier(0.4, 0, 0.2, 1)` | on-screen A → B |
| `--stratum-ease-emphasized` | `cubic-bezier(0.2, 0, 0, 1)` | long tail, for meaningful transitions |
| `--stratum-ease-spring` | `linear(…)` | stiffness 300, damping 30 |
| `--stratum-ease-spring-snappy` | `linear(…)` | stiffness 500, damping 35, ~1.9% overshoot |
| `--stratum-ease-spring-gentle` | `linear(…)` | stiffness 210, damping 24 |

The three springs are sampled from the real damped-oscillator solution

```
ω₀ = √(k/m)        ζ = c / (2√(km))        ω_d = ω₀√(1−ζ²)
x(t) = 1 − e^(−ζω₀t)·(cos(ω_d t) + (ζω₀/ω_d)·sin(ω_d t))
```

at 27 points up to the settle time, so the overshoot is physical rather than
eyeballed. Pair each with its matching `--stratum-duration-spring*`.

---

## Movement amounts

| Token | Value | Used for |
|---|---|---|
| `--stratum-lift-xs` | 2 px | press depression |
| `--stratum-lift-sm` | 4 px | inline message, chip |
| `--stratum-lift-md` | 8 px | popover, dropdown, toast |
| `--stratum-lift-lg` | 16 px | page enter |
| `--stratum-slide` | 20 px | tab panel directional slide |
| `--stratum-zoom-subtle` | 0.96 | popover |
| `--stratum-zoom-pop` | 0.60 | tooltip |
| `--stratum-zoom-modal` | 0.15 | dialog origin zoom |

All are pre-multiplied by `--stratum-motion-distance` / `-zoom`. Never write a
raw px translate or a raw scale factor in a component.

---

## Named behaviours

**Dialog origin zoom.** The dialog scales up from the point that opened it. The
origin is captured at open time and the *same* origin is reused on close, so it
appears to return to the button it came from. Implemented as a CSS custom
property holding the translate from viewport centre to the origin point.

**Constant-time path draw.** When a multi-hop path animates on, per-edge
`animation-delay` and `animation-duration` are proportional to each edge's
share of the total path length. A five-hop path takes exactly as long as a
one-hop path. The naive per-edge-fixed-duration version makes long paths feel
progressively more sluggish precisely when they matter most.

**Edge flow.** `stroke-dashoffset` translation on links that are carrying
traffic. Links with no traffic do not animate at all — the animation *is* the
signal.

**Optimistic row.** A row with a pending mutation gets a pulsing dot and drops
to 60% opacity. Deliberately not a page-level spinner: the operator must stay
able to act on other rows while one is in flight.

**Anomaly pulse.** `stratum-pulse` on status indicators in an abnormal state
only. A healthy indicator is static.

---

## Reduced motion

Three independent multipliers rather than a blanket kill switch:

| Property | Normal | Reduced |
|---|---|---|
| `--stratum-motion-scale` | 1 | 0.5 |
| `--stratum-motion-distance` | 1 | 0 |
| `--stratum-motion-zoom` | 1 | 0 |

Fades survive at half duration; translation and scale stop entirely. Every
looping animation — spinner rotation, shimmer, topology flow, pulse — is
disabled outright, because a perpetual loop is exactly what the preference
exists to suppress. Spinners switch to an opacity pulse so "busy" is still
communicated.

Where a percentage translate is used (an off-canvas drawer), the multiplier
cannot zero it, so those components override the resting position explicitly
and cross-fade instead. `AppShell.css` is the reference.

---

## Performance rules

- Animate only `opacity`, `transform` / `translate` / `scale`, `filter`,
  `clip-path`, `background-color`. Everything else risks layout or paint on
  every frame.
- Never put `will-change` in a static rule. It permanently promotes a layer and
  costs memory for an animation that may never run.
- Never animate table rows, and never apply layout animation to a virtualised
  list. Both are O(n) per commit and are the standard cause of scroll jank.
- Prefer `translate` / `scale` / `rotate` over a composite `transform` string so
  independent animations do not clobber each other.
