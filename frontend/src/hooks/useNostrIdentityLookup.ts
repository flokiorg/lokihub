import { useNostrProfileSearch } from "src/hooks/useNostrProfileSearch";
import { useResolvedNostrIdentity } from "src/hooks/useResolvedNostrIdentity";

// Raw input that already looks like a direct identity (hex/npub1.../
// nprofile1... or a NIP-05 "name@domain") resolves through
// useResolvedNostrIdentity on its own and shouldn't also trigger a
// free-text network search.
const DIRECT_FORMAT_REGEX = /^(npub1|nprofile1)[a-z0-9]+$|^[0-9a-fA-F]{64}$|@/i;

// useNostrIdentityLookup combines direct identity resolution (hex/npub/
// nprofile/NIP-05, via useResolvedNostrIdentity) with a NIP-50 free-text
// relay search (useNostrProfileSearch) fallback for anything else — shared
// by every "type an identity, or search a name" field (the owner identity
// input, the member picker's manual-add box) so they behave identically
// instead of each re-implementing the same direct-vs-search branching.
export function useNostrIdentityLookup(rawValue: string) {
  const { hex, isResolving, isInvalid } = useResolvedNostrIdentity(rawValue);
  const trimmed = rawValue.trim();
  const isSearchCandidate =
    !hex &&
    !isResolving &&
    trimmed.length > 0 &&
    !DIRECT_FORMAT_REGEX.test(trimmed);
  const { results, isSearching } = useNostrProfileSearch(
    isSearchCandidate ? trimmed : ""
  );

  return { hex, isResolving, isInvalid, isSearchCandidate, results, isSearching };
}
