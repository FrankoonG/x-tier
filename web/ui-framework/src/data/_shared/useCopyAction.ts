import { useCallback, useEffect, useRef, useState } from 'react';
import { copyTextToClipboard, type CopyStatus } from '../../components/CopyButton/CopyButton';

export interface UseCopyActionResult {
  /** Latest outcome. Returns to `'idle'` after `resetMs`. */
  status: CopyStatus;
  copy: (text: string) => void;
}

/**
 * One copy control's worth of transient state.
 *
 * Shared by every copy affordance in the data layer — the log's per-line and
 * copy-all buttons, the JSON tree's copy-path and copy-value, the code block's
 * header button — so that all of them acknowledge, and fail, identically.
 *
 * The acknowledgement matters more than it looks: a copy button that gives no
 * feedback is indistinguishable from one that failed, and failure is genuinely
 * common here. `navigator.clipboard` is unavailable on plain HTTP, inside a
 * cross-origin iframe without `clipboard-write`, and in older WebViews — all
 * routine for a panel served from an appliance on a management VLAN. So
 * `'error'` is a distinct reported state rather than a silent no-op.
 *
 * The write itself is `copyTextToClipboard` from `<CopyButton>`, which already
 * owns the secure-context detection and the `execCommand` fallback. One
 * clipboard implementation per package.
 */
export function useCopyAction(resetMs = 1600): UseCopyActionResult {
  const [status, setStatus] = useState<CopyStatus>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (timer.current !== undefined) clearTimeout(timer.current);
    };
  }, []);

  const copy = useCallback(
    (text: string) => {
      void copyTextToClipboard(text).then((ok) => {
        if (!alive.current) return;
        setStatus(ok ? 'copied' : 'error');
        if (timer.current !== undefined) clearTimeout(timer.current);
        timer.current = setTimeout(() => {
          if (alive.current) setStatus('idle');
        }, resetMs);
      });
    },
    [resetMs],
  );

  return { status, copy };
}
