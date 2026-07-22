import useSWR from "swr";

import { useNdk } from "src/hooks/useNdk";
import {
  NostrProfile,
  nostrProfileCacheKey,
} from "src/hooks/useNostrProfiles";
import { getRelaySetForPubkey } from "src/lib/nostrRelaySet";

// useNostrProfile resolves a single pubkey's kind:0 profile. Shares its SWR
// cache key with useNostrProfiles's per-pubkey prefill, so if a batched
// allowlist fetch already resolved this pubkey elsewhere, this hook serves
// that cached value immediately instead of re-fetching.
export function useNostrProfile(pubkey?: string) {
  const { ndk, relayUrls } = useNdk();

  const { data, isLoading } = useSWR<NostrProfile | null>(
    ndk && pubkey ? nostrProfileCacheKey(pubkey, relayUrls) : null,
    async () => {
      const relaySet = await getRelaySetForPubkey(ndk!, pubkey!, relayUrls);
      const user = ndk!.getUser({ pubkey });
      const profile = await user.fetchProfile({ relaySet });
      if (!profile) {
        return null;
      }
      return {
        name: profile.name,
        displayName: profile.displayName,
        picture: profile.picture ?? profile.image,
        about: profile.about ?? profile.bio,
        nip05: profile.nip05,
        lud16: profile.lud16,
      };
    },
    { revalidateOnFocus: false, dedupingInterval: 5 * 60 * 1000 }
  );

  return { profile: data ?? undefined, isLoading };
}
