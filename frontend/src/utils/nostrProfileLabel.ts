import { NostrProfile } from "src/hooks/useNostrProfiles";
import { safeNpubEncode, shortenMiddle } from "src/utils/nostr";

function shortNpub(pubkey: string): string {
  return shortenMiddle(safeNpubEncode(pubkey) ?? pubkey);
}

// Primary label for a pubkey+profile pair: display name, then name, then
// nip05, then lightning address, then a short npub (falling back to a short
// hex prefix if npub-encoding fails) — used wherever a single identifying
// string is shown for someone (pinned member chips, search result rows,
// the owner identity dropdown), so a person with no display name still
// shows their nip05/LUD16 instead of jumping straight to a bare npub.
export function primaryProfileLabel(
  pubkey: string,
  profile?: NostrProfile
): string {
  return (
    profile?.displayName ||
    profile?.name ||
    profile?.nip05 ||
    profile?.lud16 ||
    shortNpub(pubkey)
  );
}

export interface SecondaryProfileLabel {
  npub: string;
  fullNpub: string;
  identifier?: string;
}

// Secondary identifier shown under the primary label: the short npub always
// (a name alone isn't a verifiable identity, and the npub is the one thing
// that never changes), plus the nip05 — or the lightning address if there's
// no nip05. `fullNpub` is carried alongside the shortened display string so
// the caller can copy the untruncated value to the clipboard. Returned as
// separate fields rather than one joined string so the caller can put them
// on their own line. Only returned when there IS a primary name, since
// otherwise the npub/nip05/lud16 is already the primary label (via
// primaryProfileLabel's own identical fallback chain) and repeating it
// below would be redundant.
export function secondaryProfileLabel(
  pubkey: string,
  profile?: NostrProfile
): SecondaryProfileLabel | undefined {
  const hasPrimaryName = Boolean(profile?.displayName || profile?.name);
  if (!hasPrimaryName) {
    return undefined;
  }
  const fullNpub = safeNpubEncode(pubkey) ?? pubkey;
  return {
    npub: shortenMiddle(fullNpub),
    fullNpub,
    identifier: profile?.nip05 || profile?.lud16,
  };
}
