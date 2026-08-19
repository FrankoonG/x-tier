import { version as reactVersion, type ReactElement, type Ref } from 'react';

/* ---------------------------------------------------------------------------
 * Reading a child element's own ref, on React 18 AND React 19.
 *
 * Every overlay that takes a `trigger` element has to clone it to attach the
 * anchor ref. If the consumer already put a ref on that element we must merge
 * rather than clobber it — but WHERE the ref lives moved between versions:
 *
 *   React 18  `element.ref`         (`element.props.ref` is stripped, undefined)
 *   React 19  `element.props.ref`   (`element.ref` still resolves, but through a
 *                                    deprecation getter that logs a warning)
 *
 * So a naive `element.props.ref ?? element.ref` is wrong: on React 19 it reaches
 * the deprecated getter for every trigger that has no ref of its own, which is
 * the common case, and floods the console. Branch on the major version instead —
 * `version` is a plain string export and is safe to read at module scope.
 * ------------------------------------------------------------------------- */

const REACT_MAJOR = Number.parseInt(reactVersion, 10);

/** The ref the consumer put on a trigger element, or `undefined`. */
export function getTriggerRef(element: ReactElement): Ref<HTMLElement> | undefined {
  if (REACT_MAJOR >= 19) {
    return (element.props as { ref?: Ref<HTMLElement> }).ref;
  }
  return (element as unknown as { ref?: Ref<HTMLElement> }).ref;
}
