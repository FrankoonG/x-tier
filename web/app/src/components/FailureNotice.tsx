/* ===========================================================================
 * FAILURE PRESENTATION
 *
 * One component so that every failure in the panel reads the same way, and so
 * that the daemon's prose is never paraphrased. The layout is fixed:
 *
 *   title      the panel's words, from the code
 *   guidance   what to do, or an honest "nothing will help"
 *   code       the stable token, monospace, because that is what an operator
 *              searches the source for
 *   detail     the daemon's own message, verbatim
 *
 * The verbatim detail matters. Rewriting the daemon's message hides the one
 * piece of information the panel did not invent, and an operator comparing the
 * screen with a terminal has to be able to see the same string in both.
 * ======================================================================== */
import { Banner, Code, InlineMessage } from '@stratum/ui';
import type { BannerVariant, InlineMessageVariant } from '@stratum/ui';
import type { ErrorSeverity, FailureView } from '../api/errors';

const BANNER: Record<ErrorSeverity, BannerVariant> = {
  danger: 'danger',
  warning: 'warning',
  info: 'info',
};

const INLINE: Record<ErrorSeverity, InlineMessageVariant> = {
  danger: 'danger',
  warning: 'warning',
  info: 'info',
};

export interface FailureNoticeProps {
  failure: FailureView;
  /** Actions — Retry, Re-read, Dismiss. Suppressed in the inline form. */
  actions?: React.ReactNode;
  /** `inline` for a compact form inside a card or a form row. */
  variant?: 'banner' | 'inline';
}

export function FailureNotice({ failure, actions, variant = 'banner' }: FailureNoticeProps) {
  if (variant === 'inline') {
    return (
      <InlineMessage variant={INLINE[failure.severity]}>
        {failure.title} — <Code>{failure.code}</Code>
        {failure.detail ? <> · {failure.detail}</> : null}
      </InlineMessage>
    );
  }

  return (
    <Banner variant={BANNER[failure.severity]} title={failure.title} action={actions}>
      <div style={{ display: 'grid', gap: 'var(--stratum-space-3)' }}>
        <p style={{ margin: 0 }}>{failure.guidance}</p>
        <p style={{ margin: 0, fontSize: 'var(--stratum-text-sm)' }}>
          <Code>{failure.code}</Code>
          {failure.detail ? (
            <span style={{ color: 'var(--stratum-text-muted)' }}> — {failure.detail}</span>
          ) : null}
        </p>
      </div>
    </Banner>
  );
}
