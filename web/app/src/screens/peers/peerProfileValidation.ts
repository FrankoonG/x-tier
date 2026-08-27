import type { PeerConfig, XrayProfile } from '../../api/types';

export type PeerProfileErrors = Partial<Record<'xray_profile_id', string>>;

function credentialGroupID(profile: XrayProfile): string {
  return profile.credential_group_id?.trim() || `profile:${profile.id}`;
}

export function peerCredentialGroupOwners(
  peers: PeerConfig[],
  profiles: XrayProfile[] | null,
  excludedPeerName?: string,
): Map<string, string> {
  const owners = new Map<string, string>();
  if (profiles === null) return owners;
  const profilesByID = new Map(profiles.map((profile) => [profile.id, profile]));
  for (const peer of peers) {
    if (!peer.enabled || peer.name === excludedPeerName || !peer.xray_profile_id) continue;
    const profile = profilesByID.get(peer.xray_profile_id);
    if (!profile || profile.kind !== 'vless') continue;
    owners.set(credentialGroupID(profile), peer.display_name || peer.name);
  }
  return owners;
}

export function peerProfileOwner(
  profileID: string,
  profiles: XrayProfile[] | null,
  owners: ReadonlyMap<string, string>,
): string | undefined {
  const profile = profiles?.find((candidate) => candidate.id === profileID.trim());
  return profile ? owners.get(credentialGroupID(profile)) : undefined;
}

export function peerProfileErrors(
  profileID: string,
  profiles: XrayProfile[] | null,
  required: boolean,
): PeerProfileErrors {
  const selectedID = profileID.trim();
  if (!selectedID) {
    return required ? { xray_profile_id: 'Required for an enabled peer.' } : {};
  }
  if (profiles === null) {
    return { xray_profile_id: 'Profiles were not read, so this selection cannot be verified.' };
  }

  const selected = profiles.find((profile) => profile.id === selectedID);
  if (!selected) {
    return { xray_profile_id: `The configuration does not define profile ${selectedID}.` };
  }
  if (selected.kind !== 'vless') {
    return {
      xray_profile_id: `Enabled peers require a VLESS profile; ${selectedID} is ${selected.kind}.`,
    };
  }
  return {};
}
