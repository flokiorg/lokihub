import { Copy } from "lucide-react";

import { NostrAvatar } from "src/components/NostrAvatar";
import { Skeleton } from "src/components/ui/skeleton";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { copyToClipboard } from "src/lib/clipboard";
import {
  primaryProfileLabel,
  secondaryProfileLabel,
} from "src/utils/nostrProfileLabel";

// Shared avatar + name + NIP-05 + npub (with copy buttons) row for a single
// Nostr identity — used by CircleIdentityCard (a circle_hub's own identity)
// and ChildIdentityCard (a Cash/circle wallet child's resolved pubkey) so
// both render identically instead of drifting apart.
export function NostrIdentityHeader({ pubkey }: { pubkey: string }) {
  const { profile, isLoading } = useNostrProfile(pubkey);
  // Both labels come from the shared helpers rather than being rebuilt here.
  // This component used to fall back displayName -> name -> npub, skipping
  // nip05 entirely, so someone identified only by a nip05 was headed by a
  // bare npub while their actual name sat demoted in the muted line below —
  // and when a name WAS present the nip05 could be printed twice. The helpers
  // already encode both rules; the drift was in not using them.
  const label = primaryProfileLabel(pubkey, profile);
  const secondary = secondaryProfileLabel(pubkey, profile);

  return (
    <div className="flex items-center gap-3">
      <NostrAvatar
        pubkey={pubkey}
        profile={profile}
        isLoading={isLoading}
        className="h-12 w-12"
      />
      <div className="min-w-0 flex-1">
        {isLoading ? (
          <Skeleton className="h-5 w-40" />
        ) : (
          <div className="truncate text-lg font-semibold">{label}</div>
        )}
        {secondary?.identifier && (
          <button
            type="button"
            onClick={() => copyToClipboard(secondary.identifier!)}
            className="flex items-center gap-2 text-start font-mono text-sm text-muted-foreground hover:text-foreground"
          >
            <span className="truncate">{secondary.identifier}</span>
            <Copy className="h-3.5 w-3.5 shrink-0" />
          </button>
        )}
        {secondary && (
          <button
            type="button"
            onClick={() => copyToClipboard(secondary.fullNpub)}
            className="flex items-center gap-2 text-start font-mono text-sm text-muted-foreground hover:text-foreground"
          >
            <span className="truncate">{secondary.npub}</span>
            <Copy className="h-3.5 w-3.5 shrink-0" />
          </button>
        )}
      </div>
    </div>
  );
}
