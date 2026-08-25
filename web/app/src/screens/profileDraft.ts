export type ProfileKind = 'vless' | 'socks';

export interface ProfileDraft {
  kind: ProfileKind;
  id: string;
  username: string;
  credential: string;
  submitted: boolean;
}

/**
 * Returns a fresh, non-sensitive profile draft.
 *
 * Preview dismissal is a security boundary: the draft is discarded rather
 * than restored behind the dialog, so a credential cannot remain in a
 * controlled input or its React state after Cancel, Escape, or close.
 */
export function discardProfileDraft(): ProfileDraft {
  return {
    kind: 'vless',
    id: '',
    username: '',
    credential: '',
    submitted: false,
  };
}
