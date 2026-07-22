import { Copy } from "lucide-react";

import { NostrAvatar } from "src/components/NostrAvatar";
import { Skeleton } from "src/components/ui/skeleton";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { copyToClipboard } from "src/lib/clipboard";
import { safeNpubEncode, shortenMiddle } from "src/utils/nostr";

// Shared avatar + name + NIP-05 + npub (with copy buttons) row for a single
// Nostr identity — used by CircleIdentityCard (a circle_hub's own identity)
// and ChildIdentityCard (a JIT/circle wallet child's resolved pubkey) so
// both render identically instead of drifting apart.
export function NostrIdentityHeader({ pubkey }: { pubkey: string }) {
  const { profile, isLoading } = useNostrProfile(pubkey);
  const npub = safeNpubEncode(pubkey);
  const shortNpub = npub ? shortenMiddle(npub) : undefined;
  const displayName = profile?.displayName || profile?.name;

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
          <div className="truncate text-lg font-semibold">
            {displayName || shortNpub || pubkey}
          </div>
        )}
        {profile?.nip05 && (
          <button
            type="button"
            onClick={() => copyToClipboard(profile.nip05!)}
            className="flex items-center gap-2 text-start font-mono text-sm text-muted-foreground hover:text-foreground"
          >
            <span className="truncate">{profile.nip05}</span>
            <Copy className="h-3.5 w-3.5 shrink-0" />
          </button>
        )}
        {npub && (
          <button
            type="button"
            onClick={() => copyToClipboard(npub)}
            className="flex items-center gap-2 text-start font-mono text-sm text-muted-foreground hover:text-foreground"
          >
            <span className="truncate">{shortNpub}</span>
            <Copy className="h-3.5 w-3.5 shrink-0" />
          </button>
        )}
      </div>
    </div>
  );
}
