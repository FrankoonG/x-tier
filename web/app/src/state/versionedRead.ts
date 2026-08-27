export interface VersionedSnapshot {
  revision: number;
}

export interface VersionedRead<T extends VersionedSnapshot> {
  data: T | null;
  failure: unknown | null;
}

/** A retained response is writable only while it matches the authoritative CAS revision. */
export function isVersionedReadCurrent<T extends VersionedSnapshot>(
  revisionRead: boolean,
  revision: number,
  read: VersionedRead<T>,
): read is VersionedRead<T> & { data: T; failure: null } {
  return revisionRead && read.failure === null && read.data?.revision === revision;
}
