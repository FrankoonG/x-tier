import { useId, useMemo, useState } from 'react';
import {
  AddressList,
  Badge,
  Button,
  Code,
  Field,
  FormActions,
  FormGrid,
  FormGridItem,
  IconShield,
  IconTrash,
  InlineMessage,
  PortRangeInput,
  formatPortRanges,
} from '@stratum/ui';
import type {
  AddressEntry,
  AddressListValidity,
  PortRange,
  PortRangeValidation,
} from '@stratum/ui';
import type { NodeEgressGrant, PeerConfig } from '../../api/types';
import type { MutationSpec } from '../../components/MutationDialog';
import {
  nodeEgressGrantEditorMode,
  nodeEgressGrantPutOperation,
  nodeEgressGrantRevokeOperation,
} from './nodeEgressGrantEditor';

export interface PeerEgressGrantEditorProps {
  peer: PeerConfig;
  grant: NodeEgressGrant | null;
  revision: number;
  current: boolean;
  onReview: (spec: MutationSpec) => void;
}

const EMPTY_VALIDITY: AddressListValidity = {
  valid: true,
  invalidIds: [],
  emptyIds: [],
  duplicateIds: [],
};

function entries(prefix: string, values: readonly string[]): AddressEntry[] {
  return values.map((value, index) => ({ id: `${prefix}-${index}`, value }));
}

function enabledValues(values: readonly AddressEntry[]): string[] {
  return values
    .filter((entry) => entry.enabled !== false)
    .map((entry) => entry.value.trim());
}

function listIsValid(validity: AddressListValidity): boolean {
  return validity.valid && validity.duplicateIds.length === 0;
}

function sameGrant(left: NodeEgressGrant, right: NodeEgressGrant | null): boolean {
  return right !== null && JSON.stringify(left) === JSON.stringify(right);
}

export function PeerEgressGrantEditor({
  peer,
  grant,
  revision,
  current,
  onReview,
}: PeerEgressGrantEditorProps) {
  const id = useId().replaceAll(':', '');
  const [baseRevision] = useState(revision);
  const [publicCIDRs, setPublicCIDRs] = useState<AddressEntry[]>(() =>
    entries(`${id}-public`, grant?.allow_cidrs ?? []));
  const [privateCIDRs, setPrivateCIDRs] = useState<AddressEntry[]>(() =>
    entries(`${id}-private`, grant?.allow_private_cidrs ?? []));
  const [denyCIDRs, setDenyCIDRs] = useState<AddressEntry[]>(() =>
    entries(`${id}-deny`, grant?.deny_cidrs ?? []));
  const [publicValidity, setPublicValidity] = useState(EMPTY_VALIDITY);
  const [privateValidity, setPrivateValidity] = useState(EMPTY_VALIDITY);
  const [denyValidity, setDenyValidity] = useState(EMPTY_VALIDITY);
  const [portsText, setPortsText] = useState(() => formatPortRanges(
    (grant?.allow_ports ?? []).map((range) => ({ start: range.from, end: range.to })),
  ));
  const [ports, setPorts] = useState<PortRange[]>(() =>
    (grant?.allow_ports ?? []).map((range) => ({ start: range.from, end: range.to })));
  const [portsValid, setPortsValid] = useState(grant !== null && grant.allow_ports.length > 0);

  const replacement = useMemo<NodeEgressGrant>(() => ({
    source_node_id: peer.node_id,
    network: 'tcp',
    allow_cidrs: enabledValues(publicCIDRs),
    allow_private_cidrs: enabledValues(privateCIDRs),
    deny_cidrs: enabledValues(denyCIDRs),
    allow_ports: ports.map((range) => ({ from: range.start, to: range.end })),
  }), [peer.node_id, publicCIDRs, privateCIDRs, denyCIDRs, ports]);

  const hasAllowedNetwork = replacement.allow_cidrs.length > 0
    || replacement.allow_private_cidrs.length > 0;
  const valid = hasAllowedNetwork
    && listIsValid(publicValidity)
    && listIsValid(privateValidity)
    && listIsValid(denyValidity)
    && portsValid
    && replacement.allow_ports.length > 0;
  const unchanged = sameGrant(replacement, grant);
  const stale = !current || revision !== baseRevision;
  const mode = nodeEgressGrantEditorMode(peer, grant);

  const reviewReplacement = () => onReview({
    operation: nodeEgressGrantPutOperation(peer, replacement),
    expectedRevision: baseRevision,
    title: mode === 'replace' ? `Replace egress grant for ${peer.name}` : `Grant egress to ${peer.name}`,
    description:
      `Replace the complete TCP destination policy bound to authenticated source ${peer.node_id}.`,
    confirmLabel: mode === 'replace' ? 'Replace grant' : 'Create grant',
  });

  return (
    <section aria-labelledby={`${id}-heading`} style={{ display: 'grid', gap: 'var(--stratum-space-6)' }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'space-between',
          gap: 'var(--stratum-space-4)',
          flexWrap: 'wrap',
        }}
      >
        <div style={{ display: 'grid', gap: 'var(--stratum-space-2)', minInlineSize: 0 }}>
          <h2 id={`${id}-heading`} style={{ margin: 0, fontSize: 'var(--stratum-text-md)' }}>
            Node egress
          </h2>
          <span style={{ color: 'var(--stratum-text-muted)', fontSize: 'var(--stratum-text-sm)' }}>
            Destinations this authenticated peer may reach through the local node.
          </span>
        </div>
        <Badge variant={grant ? 'success' : 'neutral'} size="sm" icon={<IconShield />}>
          {grant ? 'granted' : 'default deny'}
        </Badge>
      </div>

      <InlineMessage variant="info" size="sm">
        Source <Code>{peer.node_id}</Code> is bound to TCP. Disabled CIDR rows are omitted from the
        replacement; disabling this peer itself leaves the saved grant intact.
      </InlineMessage>

      {stale && (
        <InlineMessage variant="warning" size="sm">
          This draft was opened at revision {baseRevision}, but the current grant snapshot is not
          the same revision. Close and reopen the peer before reviewing a replacement.
        </InlineMessage>
      )}

      <FormGrid columns={2} minColumnWidth="15rem">
        <FormGridItem span="full">
          <Field
            label="Public allow CIDRs"
            optional
            description="Public destination ranges. Private, CGNAT and ULA ranges belong in the private list."
          >
            <AddressList
              entries={publicCIDRs}
              onChange={setPublicCIDRs}
              onValidityChange={setPublicValidity}
              accept={['cidr4', 'cidr6']}
              minEntries={0}
              reorderable={false}
              normalizeOnBlur
              label="Public allow CIDRs"
              labelAdd="Add public CIDR"
              placeholder="8.8.8.0/24"
              size="sm"
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Field
            label="Private allow CIDRs"
            optional
            description="Explicit RFC1918, CGNAT or ULA ranges. At least one public or private range is required."
          >
            <AddressList
              entries={privateCIDRs}
              onChange={setPrivateCIDRs}
              onValidityChange={setPrivateValidity}
              accept={['cidr4', 'cidr6']}
              minEntries={0}
              reorderable={false}
              normalizeOnBlur
              label="Private allow CIDRs"
              labelAdd="Add private CIDR"
              placeholder="10.0.0.0/8"
              size="sm"
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Field
            label="Deny CIDRs"
            optional
            description="Exceptions removed from the allowed ranges. Built-in special-address denies still apply."
          >
            <AddressList
              entries={denyCIDRs}
              onChange={setDenyCIDRs}
              onValidityChange={setDenyValidity}
              accept={['cidr4', 'cidr6']}
              minEntries={0}
              reorderable={false}
              normalizeOnBlur
              label="Deny CIDRs"
              labelAdd="Add denied CIDR"
              placeholder="8.8.8.8/32"
              size="sm"
            />
          </Field>
        </FormGridItem>

        <FormGridItem span="full">
          <Field
            label="Allowed TCP ports"
            required
            description="Inclusive destination ports. Overlapping and adjacent input is canonicalized before replacement."
          >
            <PortRangeInput
              value={portsText}
              onChange={setPortsText}
              onRangesChange={setPorts}
              onValidChange={(validation: PortRangeValidation) => setPortsValid(validation.valid)}
              required
              size="sm"
              placeholder="443, 8000-8099"
              aria-label="Allowed TCP ports"
            />
          </Field>
        </FormGridItem>
      </FormGrid>

      {!hasAllowedNetwork && (
        <InlineMessage variant="warning" size="sm">
          Add at least one enabled public or private CIDR before reviewing this grant.
        </InlineMessage>
      )}

      <FormActions align="end" divider>
        {grant && (
          <Button
            type="button"
            variant="danger"
            icon={<IconTrash />}
            onClick={() => onReview({
              operation: nodeEgressGrantRevokeOperation(peer),
              expectedRevision: baseRevision,
              title: `Revoke egress grant for ${peer.name}`,
              description:
                'Remove this source-bound grant. Future egress requests from the peer are denied by default.',
              confirmLabel: 'Revoke grant',
              destructive: true,
            })}
            disabled={stale}
          >
            Revoke
          </Button>
        )}
        <Button
          type="button"
          variant="primary"
          disabled={!valid || unchanged || stale}
          onClick={reviewReplacement}
        >
          Review replacement
        </Button>
      </FormActions>
    </section>
  );
}
