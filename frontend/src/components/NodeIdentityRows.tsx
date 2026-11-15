import { Copy, QrCode } from "lucide-react";

import QRCode from "src/components/QRCode";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "src/components/ui/dialog";
import { copyToClipboard } from "src/lib/clipboard";
import { shortenMiddle } from "src/utils/nostr";
import { NodeIdentityFormats } from "src/hooks/useNodeIdentity";

// IdentityRow is exported for NodeProfile, which shows a couple of other
// copyable values (the LN URI) in this exact same convention alongside it.
//
// The QR dialog is self-contained per row (its own <Dialog>, not a shared
// one keyed by which row was clicked) — same reasoning as DebugTools' log
// settings dialog: there's exactly one possible piece of content for it,
// so it doesn't need shared state to pick what to render.
export function IdentityRow({
  label,
  value,
  qr = false,
}: {
  label: string;
  value: string;
  qr?: boolean;
}) {
  return (
    <div className="grid gap-1">
      <span className="text-sm text-muted-foreground">{label}</span>
      <div className="flex items-center gap-1">
        <button
          type="button"
          onClick={() => copyToClipboard(value)}
          className="flex flex-1 items-center gap-2 text-start font-mono text-sm hover:text-muted-foreground min-w-0"
        >
          <span className="truncate">{shortenMiddle(value)}</span>
          <Copy className="h-3.5 w-3.5 shrink-0" />
        </button>
        {qr && (
          <Dialog>
            <DialogTrigger asChild>
              <button
                type="button"
                aria-label={`Show QR code for ${label}`}
                className="shrink-0 p-1 text-muted-foreground hover:text-foreground"
              >
                <QrCode className="h-3.5 w-3.5" />
              </button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-xs">
              <DialogHeader>
                <DialogTitle>{label}</DialogTitle>
              </DialogHeader>
              <QRCode value={value} />
              <p className="break-all text-center font-mono text-xs text-muted-foreground">
                {value}
              </p>
            </DialogContent>
          </Dialog>
        )}
      </div>
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
        <IdentityRow label="Nostr npub" value={identity.npub} qr />
      )}
      {identity.nprofile && (
        <IdentityRow label="Nostr nprofile" value={identity.nprofile} qr />
      )}
    </div>
  );
}
