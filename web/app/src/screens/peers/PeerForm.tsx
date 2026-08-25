/* ===========================================================================
 * THE PEER FORM
 *
 * It does not "save" in one step. It describes a change and hands it to the
 * same check-then-apply dialog every other write goes through, so the daemon
 * gets to say what would happen before anything is written.
 *
 * THE FIELDS ARE EXACTLY WHAT THE BACKEND ACCEPTS — NO MORE
 * ---------------------------------------------------------
 * `peer add` declares five: `--node-id --addr --direction --profile --nested`.
 * `peer set` declares four: `--direction --nested --addr --profile`.
 *
 * The first version of this form emitted `--display-name`, `--gateway-addr`
 * and `--no-nested`. None exists. Go's `flag` package rejects an undefined
 * flag outright, so every one of those commands failed to parse — and because
 * the error arrives with no dotted code, the panel filed it under a generic
 * bucket that left Apply enabled. Naming a peer, giving it a gateway address,
 * or turning transit OFF were all dead buttons.
 *
 * Three more fields were offered and then silently overwritten by the CLI:
 *
 *   display_name    forced to the peer name on add (cli.go:613)
 *   gateway_addr    forced to `--addr`; they are ONE field, not two (:615, :654)
 *   rendr_capable   forced true on every peer the CLI creates (:620)
 *
 * They are shown here as facts, not as inputs. An input the backend ignores is
 * worse than no input: it teaches the operator a model that is not real.
 *
 * EDIT SENDS ONLY WHAT CHANGED
 * ----------------------------
 * `peer set` is a patch — `fs.Visit` applies only the flags actually supplied.
 * Emitting every field on every edit would turn an intent to change one thing
 * into an intent to overwrite four, clobbering whatever another writer changed
 * in between.
 * ======================================================================== */
import { useMemo, useState } from 'react';
import {
  AddressInput,
  Button,
  Field,
  FormActions,
  FormGrid,
  FormGridItem,
  Hint,
  InlineMessage,
  Input,
  SegmentedControl,
  Select,
  Switch,
} from '@stratum/ui';
import type { SelectOption } from '@stratum/ui';
import type { Direction, PeerConfig, XrayProfile } from '../../api/types';
import { mutations, type DomainMutation, type PeerPatchInput } from '../../api/control';

export interface PeerFormProps {
  /** `null` composes an add; a peer composes a patch. */
  peer: PeerConfig | null;
  /** Every peer, for the uniqueness checks the daemon would otherwise reject. */
  existing: PeerConfig[];
  /**
   * Profiles the config defines, or `null` if they could not be read.
   *
   * The difference matters: this form flags a peer naming a profile that is
   * not in the list, and asserting that from a failed read invents a
   * configuration fault.
   */
  profiles: XrayProfile[] | null;
  onSubmit: (operation: DomainMutation, title: string) => void;
  onCancel: () => void;
}

interface Draft {
  name: string;
  node_id: string;
  addr: string;
  direction: Direction;
  xray_profile_id: string;
  nested_enabled: boolean;
}

const toDraft = (p: PeerConfig | null): Draft => ({
  name: p?.name ?? '',
  node_id: p?.node_id ?? '',
  addr: p?.addr ?? '',
  direction: p?.direction ?? 'outbound',
  xray_profile_id: p?.xray_profile_id ?? '',
  nested_enabled: p?.nested_enabled ?? false,
});

/** Peers may nest, so uniqueness has to be checked against the whole tree —
 *  not just the rows the table happens to render. */
function flatten(peers: PeerConfig[]): PeerConfig[] {
  return peers.flatMap((p) => [p, ...flatten(p.children ?? [])]);
}

export function PeerForm({ peer, existing, profiles, onSubmit, onCancel }: PeerFormProps) {
  const isNew = peer === null;
  const [draft, setDraft] = useState<Draft>(() => toDraft(peer));
  const [touched, setTouched] = useState<Partial<Record<keyof Draft, boolean>>>({});

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => {
    setDraft((d) => ({ ...d, [key]: value }));
    setTouched((t) => ({ ...t, [key]: true }));
  };

  const all = useMemo(() => flatten(existing), [existing]);
  const dialable = draft.direction !== 'inbound';
  const vlessProfiles = useMemo(
    () => (profiles ?? []).filter((profile) => profile.kind === 'vless'),
    [profiles],
  );

  /* Local validation covers only what the panel can know without asking. The
   * daemon remains authoritative — these exist to avoid a round trip for a
   * mistake that is obvious here, not to duplicate its rules. */
  const errors = useMemo(() => {
    const e: Partial<Record<keyof Draft, string>> = {};
    if (isNew) {
      const name = draft.name.trim();
      const nodeID = draft.node_id.trim();
      if (!name) e.name = 'Required. This is the handle everything else addresses the peer by.';
      else if (all.some((p) => p.name === name)) {
        e.name = 'A peer already has this name.';
      }
      if (!nodeID) {
        e.node_id = 'Required. The CLI does not default a node ID from the peer name.';
      } else if (all.some((p) => p.node_id === nodeID)) {
        e.node_id = 'Another peer already uses this node ID; a path through it would be ambiguous.';
      } else if (nodeID === name || all.some((p) => p.name === nodeID)) {
        e.node_id = 'A node ID cannot collide with any peer name, including this one.';
      }
      /* `FindPeer` matches on NAME OR NODE ID (config.go:381-388), so a name
       * that collides with another peer's node id is refused too. Checking only
       * names let that through to a round trip. */
      if (name && all.some((p) => p.node_id === name)) {
        e.name = 'Another peer already uses this as its node ID, and lookups match either.';
      }
    }
    /* A peer this node may dial has to have somewhere to dial. `validatePeers`
     * refuses an outbound or bidirectional peer with no address at all, so a
     * field labelled "optional" was inviting a command the daemon rejects. */
    if (dialable && !draft.addr.trim()) {
      e.addr = 'Required for a peer this node may dial. Set inbound-only, or give it an address.';
    }
    const profileID = draft.xray_profile_id.trim();
    if (dialable && !profileID) {
      e.xray_profile_id = 'Required for a peer this node may dial.';
    } else if (profileID && profiles === null) {
      e.xray_profile_id = 'Profiles were not read, so this selection cannot be verified.';
    } else if (profileID) {
      const selected = profiles?.find((profile) => profile.id === profileID);
      if (!selected) {
        e.xray_profile_id = `The configuration does not define profile ${profileID}.`;
      } else if (selected.kind !== 'vless') {
        e.xray_profile_id = `Peer outbounds require a VLESS profile; ${profileID} is ${selected.kind}.`;
      }
    }
    return e;
  }, [draft, all, dialable, isNew, profiles]);

  const valid = Object.keys(errors).length === 0;

  const profileOptions = useMemo(
    () => {
      const options: SelectOption[] = [
        ...(!dialable ? [{ value: '', label: 'None' }] : []),
        ...vlessProfiles.map((p) => ({
          value: p.id,
          label: p.id,
          /* `kind` is the only descriptive field a profile has. `options` exists
           * but the CLI replaces it with "[REDACTED]" before it leaves the
           * process, because it can carry key material — so a profile is
           * identifiable and classifiable here, never inspectable. */
          description: 'VLESS',
        })),
      ];
      const selected = draft.xray_profile_id.trim();
      if (selected && !vlessProfiles.some((profile) => profile.id === selected)) {
        const configured = profiles?.find((profile) => profile.id === selected);
        options.push({
          value: selected,
          label: `${selected} (${profiles === null ? 'profiles not read' : configured ? `${configured.kind}; incompatible` : 'undefined'})`,
          description: 'Current value; select a VLESS profile before applying',
          disabled: true,
        });
      }
      return options;
    },
    [dialable, draft.xray_profile_id, profiles, vlessProfiles],
  );

  const submission = useMemo(() => {
    if (isNew) {
      return {
        changed: true,
        operation: mutations.peerCreate({
          name: draft.name.trim(),
          nodeId: draft.node_id.trim(),
          addr: draft.addr.trim() || undefined,
          direction: draft.direction,
          xrayProfileId: draft.xray_profile_id.trim() || undefined,
          nestedEnabled: draft.nested_enabled,
        }),
      };
    }

    // A patch: only fields that actually differ from what was read.
    const before = toDraft(peer);
    const patch: PeerPatchInput = {};
    if (draft.addr !== before.addr) patch.addr = draft.addr;
    if (draft.direction !== before.direction) patch.direction = draft.direction;
    if (draft.xray_profile_id !== before.xray_profile_id) {
      patch.xrayProfileId = draft.xray_profile_id;
    }
    if (draft.nested_enabled !== before.nested_enabled) {
      patch.nestedEnabled = draft.nested_enabled;
    }
    return {
      changed: Object.keys(patch).length > 0,
      operation: mutations.peerUpdate(peer!.name, patch),
    };
  }, [draft, isNew, peer]);

  const unchanged = !submission.changed;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!valid || unchanged) return;
        onSubmit(
          submission.operation,
          isNew ? `Add peer ${draft.name.trim()}` : `Update ${peer!.name}`,
        );
      }}
      style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}
    >
      <FormGrid columns={2} minColumnWidth="14rem">
        <Field
          label="Name"
          required={isNew}
          error={touched.name ? errors.name : undefined}
          description={
            isNew
              ? 'Unique. Every operation on this peer addresses it by this.'
              : 'The primary key, and there is no rename — a peer keeps the name it was created with.'
          }
        >
          <Input
            value={draft.name}
            onChange={(e) => set('name', e.currentTarget.value)}
            /* Read-only on edit rather than offered and silently ignored. */
            disabled={!isNew}
            placeholder="fra"
            autoComplete="off"
          />
        </Field>

        <Field
          label="Node ID"
          required={isNew}
          error={touched.node_id ? errors.node_id : undefined}
          description={
            isNew
              ? 'Required explicitly by the CLI. What path expressions resolve; it is not derived from the peer name.'
              : 'What path expressions resolve. The CLI does not allow changing it after creation.'
          }
        >
          <Input
            value={draft.node_id}
            onChange={(e) => set('node_id', e.currentTarget.value)}
            /* `peer set` declares no `--node-id`, so it cannot be changed. */
            disabled={!isNew}
            placeholder={isNew ? 'peer-node-id' : undefined}
            required={isNew}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>

        <FormGridItem span="full">
          <Field
            label="Address"
            required={draft.direction !== 'inbound'}
            optional={draft.direction === 'inbound'}
            error={touched.addr || touched.direction ? errors.addr : undefined}
            description={
              draft.direction === 'inbound'
                ? 'Optional: an inbound-only peer is never dialled from here.'
                : 'Required — the daemon refuses a dialable peer with no address.'
            }
          >
            <AddressInput
              accept={['hostport', 'ipv4', 'ipv6']}
              value={draft.addr}
              onChange={(v) => set('addr', v)}
              placeholder="198.51.100.7:443"
              hint="This one value populates both the address and the gateway address — they are a single value in the backend, not two."
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Field
            label="May dial"
            group
            description="Who is permitted to open a connection. A permission, not a traffic direction."
          >
            <SegmentedControl
              label="May dial"
              value={draft.direction}
              onValueChange={(v) => set('direction', v as Direction)}
              items={[
                { value: 'outbound', children: 'We dial them' },
                { value: 'inbound', children: 'They dial us' },
                { value: 'bidirectional', children: 'Either way' },
              ]}
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Field
            label="Transport profile"
            required={dialable}
            optional={!dialable}
            error={touched.xray_profile_id || touched.direction ? errors.xray_profile_id : undefined}
            description={
              dialable
                ? 'Required. Enabled dialable peers compile through a VLESS outbound.'
                : 'Optional for an inbound-only peer. When selected, only VLESS profiles are valid here.'
            }
          >
            <Select
              options={profileOptions}
              value={draft.xray_profile_id || ''}
              onChange={(v) => set('xray_profile_id', v ?? '')}
              placeholder={dialable ? 'Select a VLESS profile' : 'None'}
              emptyLabel="No VLESS profiles available"
              disabled={profiles === null}
              invalid={Boolean(
                (touched.xray_profile_id || touched.direction) && errors.xray_profile_id,
              )}
              aria-label="Transport profile"
              fullWidth
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Switch
            checked={draft.nested_enabled}
            onCheckedChange={(v) => set('nested_enabled', v)}
            description="Off means the peer can still be a destination — it just cannot be a middle hop. A permission, not a limitation of the peer."
          >
            May be an intermediate hop
          </Switch>
        </FormGridItem>
      </FormGrid>

      {/* Stated, not offered. Three fields the backend fills in on creation and
        * accepts no input for — an input it would ignore is worse than none. */}
      {isNew && (
        <InlineMessage variant="info">
          Three fields are filled in on creation and cannot be chosen here: the display name becomes
          the peer name, the gateway address is copied from the address, and the peer is created
          enabled and marked rendr-capable.
        </InlineMessage>
      )}

      {unchanged && (
        <InlineMessage variant="info">
          Nothing has changed, so there is nothing to apply. An edit is a patch: a field you did not
          touch is left alone, and a change that touches none of them writes nothing while still
          advancing the revision.
        </InlineMessage>
      )}
      {!isNew && !unchanged && (
        <Hint>Only the fields you changed are written. The rest are left exactly as they are.</Hint>
      )}

      <FormActions align="end" divider>
        <Button type="button" variant="subtle" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!valid || unchanged}>
          Review change
        </Button>
      </FormActions>
    </form>
  );
}
