import { Copy } from "lucide-react";

import { copyToClipboard } from "src/lib/clipboard";
import { shortenMiddle } from "src/utils/nostr";
import { NodeIdentityFormats } from "src/hooks/useNodeIdentity";

// IdentityRow is exported for NodeProfile, which shows a couple of other
// copyable values (the LN URI) in this exact same convention alongside it.
export function IdentityRow({
  label,
  value,
}: {
  label: string;
  value: string;
}) {
  return (
    <div className="grid gap-1">
      <span className="text-sm text-muted-foreground">{label}</span>
      <button
        type="button"
        onClick={() => copyToClipboard(value)}
        className="flex items-center gap-2 text-start font-mono text-sm hover:text-muted-foreground"
      >
        <span className="truncate">{shortenMiddle(value)}</span>
        <Copy className="h-3.5 w-3.5 shrink-0" />
      </button>
    </div>
  );
}

// NodeIdentityRows renders the hub's LN/Nostr identity (hex, npub, nprofile)
// as copyable rows. Shared by NodeAlias and the Services status card rather
// than duplicated — Channels.tsx's "Advanced" dropdown renders the same
// values in a different idiom (menu items) and isn't a good fit for this
// shape, so it stays separate.
export function NodeIdentityRows({
  identity,
}: {
  identity: NodeIdentityFormats;
}) {
  if (!identity.hex) {
    return null;
  }
  return (
    <div className="grid gap-3">
      <IdentityRow label="Pubkey (hex)" value={identity.hex} />
      {identity.npub && (
        <IdentityRow label="Nostr npub" value={identity.npub} />
      )}
      {identity.nprofile && (
        <IdentityRow label="Nostr nprofile" value={identity.nprofile} />
      )}
    </div>
  );
}
