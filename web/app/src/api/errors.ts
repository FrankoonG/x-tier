/* ===========================================================================
 * THE ERROR VOCABULARY
 *
 * The daemon reports failures as `error_code: prose`. The code is the stable
 * half and the prose is not: it is written for a terminal, it is not
 * translated, and it is free to change. So the panel keys everything off the
 * code and treats the prose strictly as detail.
 *
 * EVERY CODE HERE WAS TAKEN FROM THE GO SOURCE
 * --------------------------------------------
 * An earlier version of this file was written against the codes the mock
 * daemon happened to emit — `peer.unknown`, `inbound.address_in_use`,
 * `path.nesting_forbidden`. None of them exists. The backend says
 * `config.peer_unknown`, `config.inbound_unknown`, `path.nested_disabled`.
 *
 * That is not a cosmetic mismatch. `blocked` below decides whether the Apply
 * button comes back after a refusal, so a catalogue keyed to codes that never
 * arrive means every real refusal falls through to the default — and the
 * default was re-enabling Apply on commands that could not possibly succeed.
 * The mechanism was aimed at nothing.
 *
 * Each entry answers three questions the operator actually has:
 *
 *   what happened      one sentence, in their vocabulary rather than the
 *                      daemon's
 *   what to do         a concrete next action, or an honest admission that
 *                      there isn't one
 *   how bad            `severity`, which drives the colour — and `blocked`,
 *                      which says whether retrying can possibly help
 * ======================================================================== */
import { CommandFailure, TransportFailure, type MutationPreparation } from './control.ts';

export type ErrorSeverity = 'danger' | 'warning' | 'info';

export interface ErrorAdvice {
  /** Short title. Operator vocabulary, not the daemon's. */
  title: string;
  /** What to do next, or why there is nothing to do. */
  guidance: string;
  severity: ErrorSeverity;
  /** True when retrying the same command cannot succeed. Suppresses Apply. */
  blocked: boolean;
  /** The panel should re-read before the operator tries again. */
  needsRefresh?: boolean;
}

const CATALOGUE: Record<string, ErrorAdvice> = {
  /* -- concurrency, from internal/configstore ----------------------------- */
  'config.revision_conflict': {
    title: 'Someone else changed the configuration',
    guidance:
      'Another writer committed between this form being opened and submitted. '
      + 'The change was NOT applied. Re-read, check the current values still '
      + 'warrant the edit, and submit again — the panel will not retry on its '
      + 'own, because the other writer’s intent is unknown here.',
    severity: 'warning',
    blocked: false,
    needsRefresh: true,
  },
  'config.commit_visible_and_resynced': {
    title: 'Change applied — the daemon re-read its own config',
    guidance:
      'This is reported as an error but the write is on disk and is in force. '
      + 'The daemon rebuilt its in-process view afterwards. Nothing needs '
      + 'redoing; repeating the command would apply it twice.',
    severity: 'info',
    blocked: true,
    needsRefresh: true,
  },
  'config.revision_required': {
    title: 'The command was sent without a revision',
    guidance:
      'Every mutating command must state the revision it was composed against. '
      + 'This is a panel bug, not an operator error — no configuration was '
      + 'changed.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_exhausted': {
    title: 'The revision counter is exhausted',
    guidance:
      'The configuration store cannot advance its revision any further. This '
      + 'needs attention on the host; nothing in the panel will clear it.',
    severity: 'danger',
    blocked: true,
  },
  'cli.offline_read_only': {
    title: 'This command must be run by the daemon',
    guidance:
      'Configuration changes are executed by xtierd, not by a directly-invoked '
      + 'CLI. Nothing was changed. If the panel produced this, it reached a CLI '
      + 'that is not the daemon.',
    severity: 'danger',
    blocked: true,
  },

  /* -- config store I/O --------------------------------------------------- */
  'config.file_too_large': {
    title: 'The configuration file is too large',
    guidance: 'The store refuses to load it. This needs attention on the host.',
    severity: 'danger',
    blocked: true,
  },
  'config.locked': {
    title: 'The configuration is locked by another writer',
    guidance:
      'Another process holds the store lock. Nothing was changed. Wait and try '
      + 'again — this one usually clears on its own.',
    severity: 'warning',
    blocked: false,
  },
  'config.write': {
    title: 'The configuration could not be written',
    guidance:
      'The change did not land. Check free space and write permission on the '
      + 'config directory, then submit again.',
    severity: 'danger',
    blocked: false,
  },
  'config.in_use': {
    title: 'Still referenced',
    guidance:
      'Something else in the configuration still points at this. Remove the '
      + 'references first — the detail names what is holding it.',
    severity: 'warning',
    blocked: true,
  },

  /* -- peers -------------------------------------------------------------- */
  'config.peer_unknown': {
    title: 'No such peer',
    guidance:
      'Nothing in the address book matches that name. Peers are addressed by '
      + 'NAME in commands, not by node ID — and the entry may have been removed '
      + 'by another writer since this page was read.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_exists': {
    title: 'That name is taken',
    guidance:
      'Peer names are unique — they are the handle every command uses. Pick '
      + 'another, or edit the existing entry instead.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_duplicate': {
    title: 'That name is already in the address book',
    guidance: 'Peer names are unique. Pick another, or edit the existing entry.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_node_id_required': {
    title: 'The peer has no node ID',
    guidance:
      'A peer needs a node ID because that is what path expressions resolve. '
      + 'The CLI defaults it to the peer name when the flag is omitted, so an '
      + 'empty one means it was explicitly cleared.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_gateway_required': {
    title: 'A dialable peer needs an address',
    guidance:
      'A peer this node may dial must have somewhere to dial. Give it an '
      + 'address, or set its direction to inbound-only.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_direction_invalid': {
    title: 'Not a valid direction',
    guidance: 'Direction is one of inbound, outbound or bidirectional.',
    severity: 'warning',
    blocked: true,
  },
  'config.profile_unknown': {
    title: 'No such transport profile',
    guidance:
      'The profile named is not defined in this configuration. Define it first, '
      + 'or pick one that exists.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'peer_trust.scope_forbidden': {
    title: 'That trust scope is not permitted',
    guidance: 'The requested scope is outside what this build allows.',
    severity: 'warning',
    blocked: true,
  },

  /* -- inbound ------------------------------------------------------------ */
  'config.inbound_unknown': {
    title: 'No listener of that kind',
    guidance:
      'Inbound listeners are addressed by KIND — socks, http — not by listen '
      + 'address, and there is at most one of each. Nothing of this kind is '
      + 'configured.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'config.inbound_listen_required': {
    title: 'The listener has no address',
    guidance: 'A listener needs a bind address before the daemon can open it.',
    severity: 'warning',
    blocked: true,
  },

  /* -- settings validation, from internal/settings/settings.go:67-88 -------
   * Every one of these is reachable directly from the Settings screen, and the
   * hard ceilings are compile-time constants rather than anything the daemon
   * reports — so the panel bounds its own inputs to match and treats an
   * arrival here as a bug in those bounds. */
  'settings.invalid_log_level': {
    title: 'Not a valid log level',
    guidance: 'The daemon accepts debug, info, warn and error.',
    severity: 'warning',
    blocked: true,
  },
  'settings.max_nested_depth_out_of_range': {
    title: 'Nesting depth is out of range',
    guidance: 'It must be between 1 and 10. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },
  'settings.max_response_nodes_out_of_range': {
    title: 'Response node ceiling is out of range',
    guidance: 'It must be between 1 and 65536. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },
  'settings.max_response_bytes_out_of_range': {
    title: 'Response size ceiling is out of range',
    guidance: 'It must be between 1 and 16 MiB. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },
  'settings.max_cache_entries_out_of_range': {
    title: 'Cache entry ceiling is out of range',
    guidance: 'It must be between 1 and 100000. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },
  'settings.max_fetch_fan_out_out_of_range': {
    title: 'Fetch fan-out is out of range',
    guidance: 'It must be between 1 and 64. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },

  /* -- identity ----------------------------------------------------------- */
  'identity.exists': {
    title: 'This node already has a backed identity',
    guidance:
      'There is nothing to initialise. The existing identity is intact and was '
      + 'not touched.',
    severity: 'info',
    blocked: true,
  },
  'identity.legacy_unbacked': {
    title: 'The configured identity has no cryptographic backing',
    guidance:
      'A v1 node ID predates seed backing, so there is no seed to derive it '
      + 'from and none can be created without changing the node’s identity. No '
      + 'CLI command resolves this.',
    severity: 'danger',
    blocked: true,
  },
  'identity.backing_missing': {
    title: 'The seed file backing this identity is gone',
    guidance:
      'The node ID cannot be re-derived. Recovery means restoring the seed file '
      + 'on the host — no command in this panel will do it.',
    severity: 'danger',
    blocked: true,
  },
  'identity.config_mismatch': {
    title: 'The seed backs a different identity',
    guidance:
      'The configuration and the seed file disagree, and only you know which '
      + 'one is correct. This is resolved on the host, not here.',
    severity: 'danger',
    blocked: true,
  },
  'identity.role_removed': {
    title: 'Node role is no longer settable',
    guidance:
      '`--role` was removed. Role survives only as read-only legacy metadata.',
    severity: 'info',
    blocked: true,
  },

  /* -- path resolution, from internal/route -------------------------------- */
  'path.unknown_node': {
    title: 'Unknown hop',
    guidance:
      'A hop in the expression matched no peer. Hops resolve by NODE ID, never '
      + 'by peer name or display name — a name that reads correctly will still '
      + 'fail here.',
    severity: 'warning',
    blocked: true,
  },
  'path.node_disabled': {
    title: 'A hop on this path is disabled',
    guidance:
      'The peer is administratively disabled, which is a configured decision '
      + 'rather than an observed outage. Enable it, or route around it.',
    severity: 'warning',
    blocked: true,
  },
  'path.edge_disabled': {
    title: 'An edge on this path is disabled',
    guidance: 'The link to that hop is disabled in the configuration.',
    severity: 'warning',
    blocked: true,
  },
  'path.edge_missing': {
    title: 'No edge to that hop',
    guidance:
      'There is no configured link from the previous hop to this one, so the '
      + 'path cannot be built.',
    severity: 'warning',
    blocked: true,
  },
  'path.edge_not_outbound': {
    title: 'That peer cannot be dialled',
    guidance:
      'The peer’s direction is inbound-only, so this node may not open a '
      + 'connection to it and it cannot appear on an outbound path at all.',
    severity: 'warning',
    blocked: true,
  },
  'path.nested_disabled': {
    title: 'That peer may not be an intermediate hop',
    guidance:
      'The peer does not permit nesting, so it can only be the final hop. It '
      + 'remains a perfectly valid single-hop destination — enable nesting on '
      + 'it, or move it to the end of the path.',
    severity: 'warning',
    blocked: true,
  },
  'path.terminal_not_rendr': {
    title: 'The final hop does not speak rendr',
    guidance:
      'A rendr endpoint kind requires a rendr-capable terminal. Choose the '
      + 'legacy stream endpoint, or end the path somewhere else.',
    severity: 'warning',
    blocked: true,
  },
  'path.cycle': {
    title: 'The path visits a hop twice',
    guidance: 'Each hop may appear once. Remove the repeat.',
    severity: 'warning',
    blocked: true,
  },
  'path.empty': {
    title: 'No hops given',
    guidance: 'Add at least one hop before compiling.',
    severity: 'info',
    blocked: true,
  },

  /* -- route intent -------------------------------------------------------- */
  'route.paths_empty': {
    title: 'No path given',
    guidance: 'The intent contains no paths.',
    severity: 'info',
    blocked: true,
  },
  'route.strategy_unknown': {
    title: 'Unknown strategy',
    guidance: 'This build accepts selector, race, bond and peak.',
    severity: 'warning',
    blocked: true,
  },
  'route.endpoint_unknown': {
    title: 'Unknown endpoint kind',
    guidance: 'This build accepts rendr_stream, rendr_packet and egress.',
    severity: 'warning',
    blocked: true,
  },
  'route.terminal_mismatch': {
    title: 'Grouped paths must share a terminal',
    guidance:
      'Every path in one rendr group has to end '
      + 'at the same node. These do not.',
    severity: 'warning',
    blocked: true,
  },
  'route.session_kind_mismatch': {
    title: 'Grouped paths must use one session kind',
    guidance: 'Stream and packet sessions cannot be combined in the same rendr group.',
    severity: 'warning',
    blocked: true,
  },
  'route.instance_mismatch': {
    title: 'Grouped paths reached different runtime instances',
    guidance:
      'All paths must terminate at the same observed rendr runtime instance. '
      + 'Refresh the address book and compile again.',
    severity: 'warning',
    blocked: true,
  },
  'topology.local_missing': {
    title: 'This node is not in its own topology',
    guidance:
      'The configuration has no node entry for the local node ID, so nothing '
      + 'can be resolved from it. This is a configuration fault, not a routing one.',
    severity: 'danger',
    blocked: true,
  },

  'service.reload_requires_control': {
    title: 'Reload requires the running daemon',
    guidance:
      'Runtime reconciliation cannot run in offline file mode. Connect through '
      + 'the local daemon control API and retry against the current revision.',
    severity: 'info',
    blocked: true,
  },

  'config.restore_not_required': {
    title: 'The active configuration is already valid',
    guidance:
      'Nothing was restored. Refresh the runtime view before deciding whether another action is needed.',
    severity: 'info',
    blocked: true,
  },

  'config.restore_checkpoint_unavailable': {
    title: 'No usable runtime checkpoint is available',
    guidance:
      'The daemon could not read or validate its last-known-good checkpoint. Repair the active configuration on the host.',
    severity: 'danger',
    blocked: true,
  },

  'config.restore_active_unavailable': {
    title: 'The active configuration cannot be safely inspected',
    guidance:
      'Restore only repairs a readable but invalid document. Fix the file path, permissions, or storage error first.',
    severity: 'danger',
    blocked: true,
  },

  'config.content_invalid': {
    title: 'The active configuration is invalid',
    guidance:
      'The daemon is still serving its last-known-good checkpoint. Use the restore action on the Daemon screen before reloading.',
    severity: 'warning',
    blocked: true,
  },

  /* -- argument handling ---------------------------------------------------- */
  'cli.argument_required': {
    title: 'A required argument is missing',
    guidance: 'The daemon rejected the command as incomplete. The detail names the argument.',
    severity: 'warning',
    blocked: true,
  },
  'cli.command_required': {
    title: 'A subcommand is missing',
    guidance: 'The command was sent without the verb it needs. This is a panel bug.',
    severity: 'danger',
    blocked: true,
  },
  'cli.unknown_command': {
    title: 'The daemon does not have that command',
    guidance:
      'The panel asked for something this build does not implement. That is a '
      + 'version mismatch between panel and daemon, not an operator error — and '
      + 'no retry will change it.',
    severity: 'danger',
    blocked: true,
  },
  /*
   * The daemon's own catch-all.
   *
   * `errorCode()` recovers a dotted code from a plain error only when the
   * message contains a COLON (cli.go:1016-1023). Several real failures are
   * formatted without one — `config.peer_name_required`,
   * `config.trailing_json`, the Windows secure-file checks — and every one of
   * them arrives here as the literal string `error`, with the real code
   * visible only in the message. So the message is shown prominently rather
   * than as a footnote.
   */
  error: {
    title: 'The daemon refused the command',
    guidance:
      'The daemon reported a failure without a machine-readable code — its own '
      + 'error formatting only exposes one when the message contains a colon. '
      + 'The message below is the whole of what it said, and is the thing to '
      + 'search the source for.',
    severity: 'danger',
    blocked: true,
  },
  /* Go's `flag` package, which emits bare prose with no dotted code. The panel
   * synthesises this code so an undefined flag is still recognisable. */
  flag: {
    title: 'The daemon does not accept that option',
    guidance:
      'A flag in the composed command is not defined for it. The command never '
      + 'ran and nothing was changed. This is a panel bug — retrying sends the '
      + 'same unknown flag.',
    severity: 'danger',
    blocked: true,
  },
  'command.output_not_json': {
    title: 'The command produced no machine-readable output',
    guidance:
      'It succeeded but returned text rather than JSON, so the panel cannot '
      + 'display the result. Run it in a terminal to see the output.',
    severity: 'warning',
    blocked: true,
  },

  /* -- transport ----------------------------------------------------------- */
  'control.unreachable': {
    title: 'The daemon did not answer',
    guidance:
      'This says the panel could not reach the control plane. It does NOT say '
      + 'the daemon is stopped — an unreachable daemon and a stopped one look '
      + 'identical from here, and the panel will not guess between them. Check '
      + 'that the daemon is running and that the control socket is bound.',
    severity: 'danger',
    blocked: false,
  },
  'control.http_status': {
    title: 'The control plane refused the request',
    guidance:
      'The request was rejected before it reached the command layer — usually '
      + 'a bridge or origin problem rather than anything to do with the command.',
    severity: 'danger',
    blocked: false,
  },
};

/**
 * Nothing matched.
 *
 * `blocked: true` is the deliberate default, and it is the opposite of what
 * this file used to do. An unrecognised code means the panel does not
 * understand why the daemon refused — and offering Apply again on a refusal
 * nobody can explain is how an operator ends up mashing a button that cannot
 * work. Re-reading and looking at the journal is the useful next step, and
 * both remain available.
 */
/** A dotted lowercase token — what a real error code looks like. */
const CODE_SHAPED = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/;

function unknownAdvice(code: string): ErrorAdvice {
  /*
   * A "code" is not always a code.
   *
   * `errorCode()` takes everything before the FIRST colon and calls it a code
   * if it contains a dot (cli.go:1016-1023). For an ordinary filesystem error
   * — `open /etc/xtier/config.json: permission denied` — that is the PATH, and
   * quoting a path back to the operator as an error code is worse than saying
   * nothing. When the token is not code-shaped the panel says plainly that
   * there was no usable code and lets the message speak for itself.
   */
  const looksLikeCode = CODE_SHAPED.test(code);
  return {
    title: looksLikeCode ? 'The command failed' : 'The daemon reported a raw error',
    guidance: looksLikeCode
      ? `The daemon reported ${code}, which this panel has no specific guidance `
        + 'for. The detail below is the daemon’s own message, unmodified. Apply is '
        + 'withheld because a refusal the panel cannot classify may well be one no '
        + 'retry can clear.'
      : 'This failure carried no machine-readable code. The daemon only exposes '
        + 'one when its message happens to contain a colon, so what is shown as '
        + 'the code is really the first half of the sentence — read the two '
        + 'together. Apply is withheld because the panel cannot tell whether a '
        + 'retry could clear it.',
    severity: 'danger',
    blocked: true,
  };
}

export function adviseCode(code: string): ErrorAdvice {
  return CATALOGUE[code] ?? unknownAdvice(code);
}

/** Everything the UI needs to render a failure, from the thrown value. */
export interface FailureView extends ErrorAdvice {
  code: string;
  /** The daemon's own prose. Shown verbatim, never rewritten. */
  detail: string;
  /** Comes only from the daemon's validated mutation outcome. */
  applied: boolean;
  /** Durable work performed before a definitely uncommitted configuration write. */
  preparations?: readonly MutationPreparation[];
}

export function describeFailure(err: unknown): FailureView {
  if (err instanceof CommandFailure) {
    const advice = err.code === 'config.commit_visible_and_resynced' && !err.applied
      ? unknownAdvice(err.code)
      : adviseCode(err.code);
    return {
      ...advice,
      applied: err.isAppliedDespiteError,
      preparations: err.preparations,
      code: err.code,
      detail: err.detail,
    };
  }
  if (err instanceof TransportFailure) {
    return {
      ...adviseCode('control.unreachable'),
      code: 'control.unreachable',
      detail: err.message,
      applied: false,
    };
  }
  return {
    title: 'Unexpected panel error',
    guidance:
      'This failure came from the panel itself rather than from the daemon, so '
      + 'no configuration was changed.',
    severity: 'danger',
    blocked: true,
    code: 'panel.exception',
    detail: err instanceof Error ? err.message : String(err),
    applied: false,
  };
}

/** Known codes, for the diagnostics screen's coverage table. */
export const KNOWN_ERROR_CODES = Object.keys(CATALOGUE);
