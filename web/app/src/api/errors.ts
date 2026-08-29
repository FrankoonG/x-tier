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
  'config.peer_profile_required': {
    title: 'The peer needs a transport profile',
    guidance:
      'Every enabled peer needs its own VLESS profile. Select one in the peer form before '
      + 'creating or enabling this entry.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_profile_incompatible': {
    title: 'The outbound peer profile cannot be used',
    guidance:
      'This enabled peer needs a defined VLESS profile for outbound traffic. Edit the peer and '
      + 'select a compatible profile before enabling it.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_inbound_profile_incompatible': {
    title: 'The inbound peer credential is unavailable',
    guidance:
      'This enabled peer needs a defined VLESS profile so its inbound identity can be verified. '
      + 'Edit the peer and select a compatible profile before enabling it.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_credential_duplicate': {
    title: 'That peer credential is already assigned',
    guidance:
      'Different node IDs cannot share a VLESS credential. Edit this peer and choose a unique '
      + 'profile; an existing disabled entry may keep the value but cannot be enabled with it.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_credential_quarantined': {
    title: 'That peer credential was quarantined',
    guidance:
      'This credential previously crossed peer identity boundaries. Bind the peer to a new '
      + 'unique VLESS profile, apply that change, and then enable the peer explicitly.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_credential_invalid': {
    title: 'The peer credential is invalid',
    guidance:
      'The selected VLESS profile does not contain a usable canonical credential. Repair or '
      + 'replace that profile before enabling the peer.',
    severity: 'warning',
    blocked: true,
  },
  'config.peer_credential_quarantine_fingerprint_invalid': {
    title: 'Credential quarantine evidence is malformed',
    guidance:
      'A durable quarantine fingerprint is not canonical. The daemon refused to guess which '
      + 'credential it represents; recover the configuration from trusted evidence.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_duplicate': {
    title: 'Credential quarantine evidence is duplicated',
    guidance:
      'The same retired credential appears in more than one durable quarantine record. Repair '
      + 'the stored evidence before attempting another peer change.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_reason_invalid': {
    title: 'Credential quarantine evidence has an invalid reason',
    guidance:
      'The durable record is not a recognized shared-credential quarantine. Recover or repair '
      + 'the configuration before enabling any affected peer.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_peers_required': {
    title: 'Credential quarantine evidence has no affected peers',
    guidance:
      'A durable record must retain the node IDs involved in the credential collision. Recover '
      + 'that evidence before changing the affected peers.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_peer_invalid': {
    title: 'Credential quarantine evidence has an invalid node ID',
    guidance:
      'One affected node ID is empty or non-canonical. The daemon will not discard that evidence '
      + 'automatically; repair it from a trusted source.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_peer_duplicate': {
    title: 'Credential quarantine evidence repeats a node ID',
    guidance:
      'An affected node is listed twice in one durable record. Repair the stored evidence before '
      + 'attempting another peer mutation.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_record_missing': {
    title: 'Credential quarantine evidence is incomplete',
    guidance:
      'A peer is marked quarantined but its durable credential record is missing. Recover the '
      + 'record before rotating or enabling that peer.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_reason_conflict': {
    title: 'Credential quarantine records conflict',
    guidance:
      'Two durable records disagree about why the same credential was retired. The daemon '
      + 'refused to merge them; recover the evidence from a trusted source.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_quarantine_write': {
    title: 'Credential quarantine was not safely persisted',
    guidance:
      'The daemon could not confirm the durable quarantine write. Re-read the configuration and '
      + 'repair the storage failure before retrying.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_quarantine_invalid': {
    title: 'Credential quarantine could not produce a valid configuration',
    guidance:
      'The daemon contained the collision in memory but strict validation still failed. No unsafe '
      + 'runtime was published; inspect the stored configuration before retrying.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_ledger_read': {
    title: 'Credential retirement ledger is unavailable',
    guidance:
      'The daemon cannot read the private monotonic credential ledger. Repair its ownership, '
      + 'permissions, or storage before changing peers; the configuration was not trusted alone.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_decode': {
    title: 'Credential retirement ledger is malformed',
    guidance:
      'The private ledger is not a complete supported document. Recover it from trusted host '
      + 'evidence; the daemon will not discard retired credentials.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_version': {
    title: 'Credential retirement ledger needs a different daemon version',
    guidance:
      'This daemon cannot interpret the stored ledger version. Use a compatible build before '
      + 'performing peer or recovery operations.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_invalid': {
    title: 'Credential retirement ledger contains invalid evidence',
    guidance: 'Repair the private ledger from trusted evidence before retrying.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_missing': {
    title: 'Credential retirement ledger has not been established',
    guidance:
      'A configuration containing retired credentials cannot be used until the daemon migrates '
      + 'those records into its private monotonic ledger.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_ledger_mismatch': {
    title: 'Configuration and credential ledger disagree',
    guidance:
      'The daemon refused the ordinary read so retired credentials cannot disappear during '
      + 'rollback. Run the supported migration or restore path, then refresh.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_ledger_stale': {
    title: 'The write would discard retired credentials',
    guidance:
      'The private ledger contains tombstones missing from this candidate. Re-read the current '
      + 'configuration and retry through the daemon; do not overwrite the ledger.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_ledger_merge': {
    title: 'Credential retirement evidence could not be merged',
    guidance: 'Conflicting durable records require repair from trusted host evidence.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_write': {
    title: 'Credential retirement evidence was not persisted',
    guidance:
      'The daemon stopped before publishing the configuration. Repair private-state storage and '
      + 'retry; repeating the same peer change is not safe until then.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_credential_ledger_directory': {
    title: 'Credential retirement ledger directory is unavailable',
    guidance: 'Repair the configuration directory on the host before retrying the write.',
    severity: 'danger',
    blocked: true,
  },
  'config.peer_credential_ledger_encode': {
    title: 'Credential retirement evidence could not be encoded',
    guidance: 'No configuration was published. Inspect the daemon build and stored evidence.',
    severity: 'danger',
    blocked: true,
  },
  'config.last_good_credential_ledger': {
    title: 'The runtime checkpoint could not retain credential tombstones',
    guidance:
      'The daemon refused to advance last-known-good state without the monotonic credential '
      + 'ledger. Repair private-state storage before reloading.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
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
  'config.node_egress_grant_revoke_required': {
    title: 'The direction change would orphan an egress grant',
    guidance:
      'This peer currently has permission to enter the node and use its exit. '
      + 'Cancel this review, refresh the peer, then reopen it and review the combined '
      + 'direction change and grant revocation as one atomic mutation.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'config.node_egress_grant_source_required': {
    title: 'The egress source is missing',
    guidance:
      'A grant must name the authenticated peer node ID it belongs to. This is '
      + 'a panel request error; nothing was changed.',
    severity: 'danger',
    blocked: true,
  },
  'config.node_egress_grant_source_mismatch': {
    title: 'The egress source does not match its entry',
    guidance:
      'The map key and source node ID must be identical. Re-read the grant '
      + 'before replacing it; the panel must not infer a different principal.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.node_egress_grant_peer_unknown': {
    title: 'The grant source is not a direct peer',
    guidance:
      'Node egress can be granted only to a direct address-book peer. The peer '
      + 'may have been removed by another writer; refresh before trying again.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'config.node_egress_grant_peer_inbound_required': {
    title: 'This peer cannot enter the node',
    guidance:
      'Only inbound or bidirectional peers can receive a node egress grant. '
      + 'Change the peer direction first, then configure its destinations.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_network_invalid': {
    title: 'The egress network is unsupported',
    guidance: 'This build accepts TCP grants only. Nothing was changed.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_cidr_invalid': {
    title: 'A CIDR is not canonical',
    guidance:
      'Use a network address with an explicit prefix, such as 8.8.8.0/24 or '
      + '2001:4860::/32. Host bits, IPv4-mapped IPv6 and zone identifiers are refused.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_cidr_duplicate': {
    title: 'A CIDR is repeated',
    guidance:
      'Each CIDR may appear once in its list. Remove the duplicate and review '
      + 'the complete replacement again.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_ports_required': {
    title: 'No destination ports are allowed',
    guidance: 'Add at least one TCP port or inclusive port range.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_port_invalid': {
    title: 'A destination port range is invalid',
    guidance:
      'Ports must be between 1 and 65535, with the lower endpoint first and no '
      + 'overlapping ranges.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_invalid': {
    title: 'The egress policy is invalid',
    guidance:
      'Allow at least one public or private CIDR and one TCP port. Put RFC1918, '
      + 'CGNAT and ULA ranges in the private list; explicit denies cannot '
      + 'override the built-in special-address deny set.',
    severity: 'warning',
    blocked: true,
  },
  'config.node_egress_grant_unknown': {
    title: 'No node egress grant exists',
    guidance:
      'The grant may already have been revoked. Refresh the peer before '
      + 'deciding whether a new grant is needed.',
    severity: 'info',
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
  'service.reload_apply': {
    title: 'Configuration committed, runtime apply failed',
    guidance:
      'The configuration write is on disk, but the daemon did not confirm this revision as a '
      + 'healthy data-plane state. Do not repeat or reverse the change blindly. Re-read Runtime '
      + 'and the journal; authorization changes fail closed when the runtime cannot retire the old state.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.peer_identity_required': {
    title: 'The peer identity is incomplete',
    guidance:
      'A peer needs both a unique name and a node ID. Supply both values before reviewing the '
      + 'change; no configuration was written.',
    severity: 'warning',
    blocked: true,
  },
  'service.reload_applied_unhealthy': {
    title: 'The new runtime revision is unhealthy',
    guidance:
      'The data plane published the committed revision but did not become healthy. Re-read Runtime '
      + 'before taking another action; managed traffic may be fail-stopped.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'service.reload_not_applied': {
    title: 'Configuration committed, runtime did not publish it',
    guidance:
      'The desired revision is on disk, but the live data plane did not confirm it. Re-read Runtime '
      + 'and resolve the apply failure before making another configuration change.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'service.reload_result_invalid': {
    title: 'Runtime confirmation was incomplete',
    guidance:
      'The configuration was committed, but the daemon returned no trustworthy proof that the same '
      + 'revision is active. Treat the runtime state as unknown and inspect Runtime before continuing.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'service.reload_canceled': {
    title: 'Runtime confirmation did not finish',
    guidance:
      'The configuration was committed before the reconciliation was canceled. Re-read Runtime and '
      + 'wait for the background reconciler; do not assume either the old or new data plane is active.',
    severity: 'warning',
    blocked: true,
    needsRefresh: true,
  },
  'service.committed_revision_invalid': {
    title: 'The daemon lost the committed revision boundary',
    guidance:
      'The configuration write completed, but the daemon could not identify the next revision to '
      + 'apply. Stop further writes and inspect Runtime and the journal.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
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

  'config.restore_schema_newer': {
    title: 'This configuration needs a newer daemon',
    guidance:
      'Restore was refused so an older binary cannot replace forward-compatible configuration data. '
      + 'Start a daemon version that supports the stored schema.',
    severity: 'danger',
    blocked: true,
  },

  'config.restore_backup_list': {
    title: 'Configuration history cannot be enumerated safely',
    guidance:
      'The daemon could not establish the durable revision high-water mark. Repair the private '
      + 'state directory before restoring; revision reuse is not allowed.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_backup_read': {
    title: 'Configuration history cannot be read safely',
    guidance:
      'A retained backup could not be opened as a protected regular file. Repair that storage '
      + 'condition before restoring; the daemon will not guess a revision bound.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_backup_revision_unavailable': {
    title: 'Configuration history has an unreadable revision',
    guidance:
      'A retained backup cannot prove its revision. Restore is blocked to prevent reusing a CAS '
      + 'identity for different content.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_active_quarantine_invalid': {
    title: 'Active credential quarantine evidence is invalid',
    guidance:
      'The active document contains quarantine evidence that cannot be carried forward safely. '
      + 'Recover that evidence before restoring older content.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_credential_ledger_unavailable': {
    title: 'Credential retirement ledger cannot authorize recovery',
    guidance:
      'Restore is blocked because the monotonic deny-list cannot be read safely. Repair the '
      + 'private state before retrying.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_credential_ledger_missing': {
    title: 'Recovery has no trustworthy credential ledger',
    guidance:
      'The active document is unreadable and no monotonic deny-list exists. Recover trusted '
      + 'credential evidence on the host before restoring an older checkpoint.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_revision_high_water_unavailable': {
    title: 'Recovery revision history is unavailable',
    guidance:
      'The daemon cannot read its monotonic revision reservation. Repair private-state storage '
      + 'before restoring; revision identities must never be reused.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_revision_reserve': {
    title: 'The recovery revision could not be reserved',
    guidance:
      'No configuration was published. Repair private-state storage before retrying so a failed '
      + 'restore cannot reuse the same revision later.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.restore_write': {
    title: 'The recovered configuration was not published',
    guidance:
      'The revision was reserved, but the active configuration write could not be confirmed. '
      + 'Re-read status and repair host storage before retrying.',
    severity: 'danger',
    blocked: true,
    needsRefresh: true,
  },
  'config.recovery_active_unavailable': {
    title: 'Startup recovery cannot inspect the active configuration',
    guidance: 'Repair the active file path or private state before restarting the daemon.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_read': {
    title: 'Revision reservation is unavailable',
    guidance: 'Repair private-state storage; recovery cannot safely reuse revision identities.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_decode': {
    title: 'Revision reservation is malformed',
    guidance: 'Recover the private reservation document from trusted host evidence.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_version': {
    title: 'Revision reservation needs a different daemon version',
    guidance: 'Use a compatible daemon build before attempting configuration recovery.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_negative': {
    title: 'Revision reservation is invalid',
    guidance: 'The stored high-water mark is negative. Repair it from trusted host evidence.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_write': {
    title: 'Revision reservation was not persisted',
    guidance: 'No recovery should be retried until private-state storage is repaired.',
    severity: 'danger',
    blocked: true,
  },
  'config.revision_high_water_encode': {
    title: 'Revision reservation could not be encoded',
    guidance: 'No recovery was published. Inspect the daemon build before retrying.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_quarantine_merge': {
    title: 'Credential quarantine evidence could not be merged',
    guidance:
      'The active document and checkpoint disagree about durable credential retirement. Restore '
      + 'was refused so neither record is silently discarded.',
    severity: 'danger',
    blocked: true,
  },
  'config.restore_quarantine_apply': {
    title: 'The restored checkpoint cannot enforce credential quarantine',
    guidance:
      'Carrying the durable deny-list into the checkpoint did not produce a valid configuration. '
      + 'Inspect the checkpoint and affected peers before retrying.',
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
  'cli.flag_invalid': {
    title: 'A command option is invalid',
    guidance: 'The request contains an unknown option or an invalid option value. Nothing was changed.',
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
  'control.session_changed': {
    title: 'The browser session changed',
    guidance:
      'A response from an older session arrived after browser authority changed. '
      + 'The response was discarded; retry from the current session state.',
    severity: 'warning',
    blocked: false,
  },
  'control.session_proof_missing': {
    title: 'This browser has no session proof',
    guidance:
      'The session cookie is not sufficient on its own. Sign in from this origin '
      + 'to establish browser authority again.',
    severity: 'warning',
    blocked: false,
  },

  /* -- web bridge, from internal/webbridge -----------------------------------
   * These are the bridge's own refusals: they happen in front of the command
   * layer, so none of them says anything about the state of the node. */
  'webbridge.credential_invalid': {
    title: 'That credential was not accepted',
    guidance:
      'The panel credential did not match. It is set on the daemon, not in the '
      + 'browser, so a password manager holding an old value will keep failing '
      + 'until it is updated.',
    severity: 'warning',
    blocked: false,
  },
  'webbridge.rate_limited': {
    title: 'Too many attempts',
    guidance:
      'The bridge is refusing further sign-in attempts for a short period. '
      + 'Waiting is the only thing that clears it; retrying sooner will still be refused.',
    severity: 'warning',
    blocked: false,
  },
  'webbridge.session_invalid': {
    title: 'The session is no longer valid',
    guidance:
      'The session expired or was revoked. The panel credential itself is '
      + 'unchanged — signing in again restores access.',
    severity: 'warning',
    blocked: false,
  },
  'webbridge.session_changed': {
    title: 'Sign-in raced with another session change',
    guidance:
      'The submitted credential was not applied to the browser session. Enter it '
      + 'again so sign-in starts from the current session state.',
    severity: 'warning',
    blocked: false,
  },
  'webbridge.credential_unavailable': {
    title: 'The panel credential is unavailable',
    guidance:
      'The bridge cannot read its private credential state. Repair the daemon state '
      + 'ownership or storage, then try again.',
    severity: 'danger',
    blocked: false,
  },
  'webbridge.session_unavailable': {
    title: 'The bridge could not create a session',
    guidance:
      'The credential was accepted, but the bridge could not create browser authority. '
      + 'Check the daemon log before retrying.',
    severity: 'danger',
    blocked: false,
  },
  'webbridge.csrf_invalid': {
    title: 'The request carried a stale security token',
    guidance:
      'This tab no longer holds the proof paired with the browser session. The '
      + 'panel clears its local authority and does not replay the request. Sign '
      + 'in again, review current state, and explicitly submit the action again.',
    severity: 'warning',
    blocked: false,
  },
  'webbridge.origin_forbidden': {
    title: 'The request came from an origin the bridge does not trust',
    guidance:
      'The bridge only accepts requests from the address it serves the panel '
      + 'on. Reaching it through a different hostname, a port-forward, or a '
      + 'reverse proxy that rewrites Origin will fail here.',
    severity: 'danger',
    blocked: false,
  },
  'webbridge.host_forbidden': {
    title: 'The request named a host the bridge does not serve',
    guidance:
      'The Host header did not match what the bridge is bound to. As with '
      + 'origin, this is about how the panel was reached rather than what it '
      + 'was asked to do.',
    severity: 'danger',
    blocked: false,
  },
  'webbridge.upstream_unavailable': {
    title: 'The bridge could not reach the daemon',
    guidance:
      'The web bridge answered, so the panel is being served — but the control '
      + 'plane behind it did not respond. The daemon is the thing to check.',
    severity: 'danger',
    blocked: false,
  },
  'webbridge.upstream_timeout': {
    title: 'The daemon did not answer in time',
    guidance:
      'The control plane accepted the request and then took too long. For a '
      + 'mutation this is genuinely indeterminate: re-read before assuming '
      + 'nothing was applied.',
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
