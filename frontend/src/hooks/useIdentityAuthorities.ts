import { useCallback, useState } from "react";
import { IdentityAuthority } from "src/types";
import { request } from "src/utils/request";

export function useIdentityAuthorities() {
  const [authorities, setAuthorities] = useState<IdentityAuthority[]>([]);
  const [loading, setLoading] = useState(false);
  const [initialized, setInitialized] = useState(false);

  const fetchIdentityAuthorities = useCallback(async () => {
    setLoading(true);
    try {
      const data = await request<IdentityAuthority[]>(
        "/api/identity-authorities"
      );
      setAuthorities(data ?? []);
    } finally {
      setLoading(false);
      setInitialized(true);
    }
  }, []);

  const addIdentityAuthority = useCallback(
    async (pubkey: string, name: string, relayUrls: string[]) => {
      await request("/api/identity-authorities", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pubkey, name, relay_urls: relayUrls }),
      });
      await fetchIdentityAuthorities();
    },
    [fetchIdentityAuthorities]
  );

  const removeIdentityAuthority = useCallback(
    async (pubkey: string) => {
      await request(`/api/identity-authorities/${pubkey}`, {
        method: "DELETE",
      });
      await fetchIdentityAuthorities();
    },
    [fetchIdentityAuthorities]
  );

  // Diffs `original` (last-saved backend state) against `current` (local
  // working state) and issues only the add/remove calls needed, mirroring
  // useLSPSManagement's saveLSPChanges so Identity Authorities save via the
  // same "Save Services" button instead of their own per-row API calls.
  const saveIdentityAuthorityChanges = useCallback(
    async (original: IdentityAuthority[], current: IdentityAuthority[]) => {
      const currentPubkeys = new Set(current.map((a) => a.pubkey));
      const originalMap = new Map(original.map((a) => [a.pubkey, a]));

      const promises: Promise<unknown>[] = [];

      for (const authority of original) {
        if (!currentPubkeys.has(authority.pubkey)) {
          promises.push(
            request(`/api/identity-authorities/${authority.pubkey}`, {
              method: "DELETE",
            })
          );
        }
      }

      for (const authority of current) {
        if (!originalMap.has(authority.pubkey)) {
          promises.push(
            request("/api/identity-authorities", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({
                pubkey: authority.pubkey,
                name: authority.name,
                relay_urls: authority.relay_urls ?? [],
              }),
            })
          );
        }
      }

      await Promise.all(promises);
      await fetchIdentityAuthorities();
    },
    [fetchIdentityAuthorities]
  );

  return {
    authorities,
    loading,
    initialized,
    fetchIdentityAuthorities,
    addIdentityAuthority,
    removeIdentityAuthority,
    saveIdentityAuthorityChanges,
  };
}
