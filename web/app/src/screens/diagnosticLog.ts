import type { LogLevel, LogLine } from '@stratum/ui';
import {
  formatCommand,
  type CommandShell,
  type CommandTarget,
  type JournalEntry,
} from '../api/control.ts';

const LEVEL: Record<JournalEntry['outcome'], LogLevel> = {
  pending: 'debug',
  ok: 'info',
  failed: 'warn',
  applied_with_error: 'warn',
  unreachable: 'error',
  unknown: 'fatal',
};

function requestKind(entry: JournalEntry): string {
  if (entry.dryRun) return 'preview';
  if (entry.mutating) return 'write';
  return 'read';
}

/**
 * Builds a request-log row whose visible and searchable subject is the HTTP
 * request Web actually sent. The independent CLI equivalent is retained only
 * as the explicitly secondary clipboard payload selected by Diagnostics.
 */
export function toDiagnosticLine(
  entry: JournalEntry,
  shell: CommandShell,
  target: CommandTarget | null,
): LogLine {
  const cliEquivalent = target
    ? formatCommand(entry.argv, {
        shell,
        target,
        revision: entry.mutating ? entry.revision : undefined,
        dryRun: entry.dryRun,
      })
    : '# CLI equivalent unavailable: daemon status did not supply --config and --control';
  const request = `${entry.method} ${entry.path}`;
  const suffix =
    entry.outcome === 'pending'
      ? ' ...'
      : entry.errorCode
        ? `  -> ${entry.errorCode}${entry.errorDetail ? `: ${entry.errorDetail}` : ''}`
        : '  -> ok';
  const timing = entry.durationMs != null ? ` (${entry.durationMs}ms)` : '';

  return {
    id: String(entry.id),
    timestamp: entry.at,
    level: LEVEL[entry.outcome],
    source: requestKind(entry),
    text: `${request}${suffix}${timing}`,
    copyText: cliEquivalent,
  };
}
