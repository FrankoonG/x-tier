/* ---------------------------------------------------------------------------
 * The geometry half of a drag reorder, shared by both list engines.
 *
 * There are two of those engines. `network/_shared/rowList.tsx` drives
 * PriorityList, AddressList, RangeList and ChainBuilder; `components/
 * EditableList` drives itself and, through composition, KeyValueEditor. They
 * have different keyboard models, different announcement vocabularies and
 * different row anatomy, and merging them wholesale would be a rewrite of a
 * component that currently passes the accessibility gate.
 *
 * What they do NOT need two copies of is this: the arithmetic that decides
 * where a dragged row would land and how far every other row has to move to
 * make room. That is the part with the subtle failure modes, so it lives here
 * once and both engines call it.
 *
 * WHY A SNAPSHOT
 * --------------
 * Every measurement is taken once, at pick-up, and never again. Re-measuring
 * per pointermove is the classic way to build a reorder that oscillates: the
 * rows are being displaced on screen, so measuring them feeds the displacement
 * back into the target calculation, and near a boundary the target flickers
 * between two values. Against a frozen snapshot the target is a pure function
 * of the pointer offset and is monotonic in it, so it cannot oscillate — and a
 * poll that re-renders the list mid-drag cannot move the drop targets either.
 *
 * WHY THE WRITES BYPASS REACT
 * ---------------------------
 * `applyShift` writes custom properties straight to the DOM. A pointermove that
 * goes through `useState` re-renders the whole list before the row moves, which
 * is exactly the lag that makes a drag feel broken; this is the same reason
 * framer-motion's Reorder uses motion values rather than state. React state
 * holds only "a drag is in progress".
 *
 * The commit stays deferred to pointerup. The displacement each row is already
 * showing is exactly the layout delta the reorder will produce, so clearing the
 * transforms in the same commit that reorders the array is visually a no-op.
 * Committing continuously — which is what the predecessor project did — would
 * rewrite the list under the pointer and invalidate the very snapshot that
 * keeps the target stable.
 * ------------------------------------------------------------------------- */

/** Frozen row geometry, captured at drag start. */
export interface ReorderSnapshot {
  /** Vertical centre of each row. Drives the target scan. */
  centers: number[];
  /** Height of each row. */
  heights: number[];
  /** Gap between a row and the one before it. Index 0 is always 0. */
  gaps: number[];
  /** Last shift written to each row, so a move only touches what changed. */
  shifts: number[];
}

/** Anything that can hand back the element for a row index. */
export type RowLookup = (index: number) => HTMLElement | null | undefined;

/**
 * Measures every row once. Returns null if any row is missing, because a
 * partial snapshot would produce a confidently wrong drop target.
 */
export function snapshotRows(getRow: RowLookup, count: number): ReorderSnapshot | null {
  const centers: number[] = [];
  const heights: number[] = [];
  const tops: number[] = [];

  for (let i = 0; i < count; i += 1) {
    const rect = getRow(i)?.getBoundingClientRect();
    if (!rect) return null;
    centers.push(rect.top + rect.height / 2);
    heights.push(rect.height);
    tops.push(rect.top);
  }

  // The real gap per row rather than one assumed constant, so a list whose rows
  // are separated unevenly — a validation message under one field, a separator,
  // a taller row — still displaces by the exact distance the commit produces.
  const gaps = tops.map((top, i) =>
    i === 0 ? 0 : Math.max(0, top - ((tops[i - 1] ?? 0) + (heights[i - 1] ?? 0))),
  );

  return { centers, heights, gaps, shifts: new Array<number>(count).fill(0) };
}

/**
 * How far past the ends of the list a row may be dragged, as a fraction of its
 * own height. The rubber band is asymptotic, so this is a hard ceiling the row
 * approaches but never reaches, not a clamp it slams into.
 */
const OVERDRAG_LIMIT = 0.55;

/**
 * Rubber-banding at the ends of the list.
 *
 * Without this the dragged row follows the pointer forever: drag past the last
 * row and it keeps going, far below a list it can no longer reorder within,
 * which reads as the gesture having come loose from the widget. A hard clamp
 * fixes the position but feels worse — the row sticks dead against an invisible
 * wall while the pointer keeps moving, and the two visibly disagree.
 *
 * So travel is free inside the list and progressively resisted outside it. The
 * curve is the standard asymptotic one:
 *
 *     f(x) = (x · L) / (L + x)      x = overshoot, L = the limit
 *
 * At small overshoot f(x) ≈ x, so the transition at the boundary is smooth with
 * no perceptible kink. As x grows f(x) → L, so the row keeps responding to the
 * pointer — it never freezes, which is what distinguishes damping from a clamp —
 * but the response shrinks toward nothing and the row cannot escape the list.
 *
 * The limit is derived from the row's own height rather than being a constant,
 * so the give is proportionally the same on a dense list and a tall one.
 *
 * The predecessor got this from `dragElastic={0.1}` on framer-motion's
 * `dragConstraints`. That is a linear scaling of the overshoot, which is
 * unbounded — drag far enough and the row still escapes, it just takes ten
 * times as long. This is bounded.
 */
export function dampOffset(snapshot: ReorderSnapshot, index: number, dy: number): number {
  const { centers, heights } = snapshot;
  const last = centers.length - 1;
  if (last <= 0) return dy;

  // The dragged row's centre may travel between the first and last row centres.
  // Beyond that there is no reachable position, so there is nothing to gain
  // from letting it move freely.
  const min = (centers[0] ?? 0) - (centers[index] ?? 0);
  const max = (centers[last] ?? 0) - (centers[index] ?? 0);
  if (dy >= min && dy <= max) return dy;

  const limit = Math.max(1, (heights[index] ?? 0) * OVERDRAG_LIMIT);
  const bound = dy < min ? min : max;
  const overshoot = Math.abs(dy - bound);
  return bound + Math.sign(dy - bound) * ((overshoot * limit) / (limit + overshoot));
}

/**
 * The row index the dragged row would land on, given how far it has moved.
 *
 * Scans outward from the origin rather than tracking incrementally, so the
 * result depends only on the current offset. There is no accumulated state to
 * drift and no hysteresis to tune.
 */
export function resolveTarget(snapshot: ReorderSnapshot, index: number, dy: number): number {
  const moved = (snapshot.centers[index] ?? 0) + dy;
  let target = index;
  while (target > 0 && moved < (snapshot.centers[target - 1] ?? -Infinity)) target -= 1;
  while (
    target < snapshot.centers.length - 1 &&
    moved > (snapshot.centers[target + 1] ?? Infinity)
  ) {
    target += 1;
  }
  return target;
}

/**
 * Moves the dragged row under the pointer and slides the rows it has passed out
 * of its way, by exactly one dragged-row height plus the gap.
 *
 * A row is displaced only while it lies between where the dragged row started
 * and where it would land, and it always moves toward the slot being vacated.
 */
export function applyShift(
  getRow: RowLookup,
  snapshot: ReorderSnapshot,
  index: number,
  target: number,
  dy: number,
): void {
  getRow(index)?.style.setProperty('--_drag-dy', `${dy}px`);

  const span = (snapshot.heights[index] ?? 0) + (snapshot.gaps[index] ?? 0);
  for (let j = 0; j < snapshot.centers.length; j += 1) {
    if (j === index) continue;
    const shift = j > index && j <= target ? -span : j < index && j >= target ? span : 0;
    if (snapshot.shifts[j] === shift) continue;
    snapshot.shifts[j] = shift;
    getRow(j)?.style.setProperty('--_shift', `${shift}px`);
  }
}

/** Removes every property `applyShift` wrote. Safe to call twice. */
export function clearShift(getRow: RowLookup, snapshot: ReorderSnapshot, index: number): void {
  getRow(index)?.style.removeProperty('--_drag-dy');
  for (let j = 0; j < snapshot.shifts.length; j += 1) {
    if (snapshot.shifts[j] === 0) continue;
    snapshot.shifts[j] = 0;
    getRow(j)?.style.removeProperty('--_shift');
  }
}

/**
 * Turns transitions off for exactly one painted frame, so the drop is silent.
 *
 * THE BUG THIS EXISTS TO KILL
 * ---------------------------
 * At the drop, two things change on the same commit: the array reorders, so
 * every affected row's LAYOUT position jumps to where it belongs; and the drag
 * transforms are cleared, so every `translate` goes back to 0. Those are equal
 * and opposite by construction — the shift a row is showing is exactly the
 * layout delta the reorder produces — so together they should be invisible.
 *
 * They are not, because only one of them is instant. Layout does not transition;
 * `translate` does, on the base rule's 200ms signature curve. So the row snapped
 * to its new slot and then spent 200ms sliding in from where it used to be —
 * against the direction of the drag. Measured before this fix: after dropping a
 * row DOWNWARD, all three rows sat ~30px ABOVE their final positions and drifted
 * down (-32.2 → -3.4 over fourteen frames). The result was right and the motion
 * was backwards, which is a specific kind of wrong that is hard to name and
 * impossible to unsee.
 *
 * Suppressing for one frame makes the cancellation actual. The rows are already
 * where they belong, so the correct amount of settle animation is none: all the
 * motion this gesture deserved already happened, live, under the pointer.
 *
 * Two frames of rAF, not one. A callback scheduled from the pointerup handler
 * can run in the SAME frame — after React commits but still before paint — which
 * would re-enable transitions in time for the browser to animate the very change
 * being hidden. The second frame guarantees one full paint has landed.
 *
 * Uses the `transition` shorthand rather than disabling `translate` alone, which
 * cannot be done without rewriting the whole list. The cost is one frame of
 * non-animated colour on the row losing its lifted styling; at 16ms that is not
 * perceptible, and it is a better trade than a backwards slide.
 */
export function settleDrop(
  getRow: RowLookup,
  snapshot: ReorderSnapshot,
  index: number,
  target: number,
  dy: number,
): void {
  const { centers, heights } = snapshot;
  const count = centers.length;

  // Captured BEFORE the commit. The reorder rebuilds the index→element map, so
  // afterwards `getRow(index)` is a different row entirely; these references
  // follow the same DOM nodes as React moves them.
  const nodes: (HTMLElement | null)[] = [];
  for (let i = 0; i < count; i += 1) nodes.push(getRow(i) ?? null);
  const dragged = nodes[index] ?? null;

  /* THE RESIDUAL — the only thing here worth animating.
   *
   * You almost never release a row exactly on a slot boundary. `dy` is where
   * the pointer left it; `layoutDelta` is where the reorder is about to put it.
   * The difference is a real distance, pointing from where you let go toward
   * where it belongs, and springing it to zero is the row dropping into place.
   *
   * An earlier revision cleared this instead of animating it, on the reasoning
   * that the transforms and the layout change cancel. They do — for the OTHER
   * rows, whose shift is exactly their layout delta, which is why they settle
   * with no motion and should. The dragged row is the one with something left
   * over, and discarding it is what made the drop look like the animation had
   * simply been deleted. */
  const top = (i: number) => (centers[i] ?? 0) - (heights[i] ?? 0) / 2;
  const layoutDelta =
    target > index
      ? top(target) + (heights[target] ?? 0) - (heights[index] ?? 0) - top(index)
      : top(target) - top(index);
  const residual = dy - layoutDelta;

  /* One frozen frame. Layout jumps to the reordered positions and every drag
   * transform clears, with transitions off so neither is animated — otherwise
   * `translate` interpolates from its old value while layout has already moved,
   * and the row slides in backwards from where it used to be. */
  for (const el of nodes) if (el) el.style.transition = 'none';
  for (let j = 0; j < count; j += 1) {
    if (snapshot.shifts[j] === 0) continue;
    snapshot.shifts[j] = 0;
    nodes[j]?.style.removeProperty('--_shift');
  }
  dragged?.style.removeProperty('--_drag-dy');

  // Sub-pixel residuals are not worth a spring; they would read as a twitch.
  const springs = dragged != null && Math.abs(residual) > 0.5;
  if (springs && dragged) {
    dragged.style.setProperty('--_shift', `${residual}px`);
    dragged.setAttribute('data-settling', '');
  }

  const release = () => {
    for (const el of nodes) if (el) el.style.removeProperty('transition');
    if (!springs || !dragged) return;
    // Transitions are live again as of this same style recalc, so changing the
    // offset now is what starts the spring.
    dragged.style.setProperty('--_shift', '0px');

    let timer: ReturnType<typeof setTimeout>;
    const done = (event?: TransitionEvent) => {
      if (event && event.propertyName !== 'translate') return;
      clearTimeout(timer);
      dragged.removeEventListener('transitionend', done);
      dragged.removeAttribute('data-settling');
      dragged.style.removeProperty('--_shift');
    };
    dragged.addEventListener('transitionend', done);
    // An interrupted transition never fires `transitionend`, and a row stuck
    // with `data-settling` would keep the spring curve on its next drag.
    timer = setTimeout(done, 1000);
  };

  if (typeof requestAnimationFrame !== 'function') {
    release();
    return;
  }
  // Two frames, not one. A callback scheduled from the pointerup handler can
  // run in the SAME frame — after React commits but before paint — which would
  // re-enable transitions in time to animate the very change being hidden.
  requestAnimationFrame(() => requestAnimationFrame(release));
}
