import { Copy, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";

import { NostrAvatar } from "src/components/NostrAvatar";
import { Skeleton } from "src/components/ui/skeleton";
import { NostrProfile } from "src/hooks/useNostrProfiles";
import { copyToClipboard } from "src/lib/clipboard";
import { cn } from "src/lib/utils";
import {
  primaryProfileLabel,
  secondaryProfileLabel,
} from "src/utils/nostrProfileLabel";

// Shared avatar + name (+ optional NIP-05-verified dot) + secondary
// identifier (+ lightning icon) row — used by both the member picker's
// search results and the owner identity search dropdown so the two render
// identically instead of drifting apart.
export function NostrProfileRow({
  pubkey,
  profile,
  isVerified,
  isVerifying,
  avatarClassName = "h-9 w-9",
  showCopy = true,
}: {
  pubkey: string;
  profile?: NostrProfile;
  isVerified?: boolean;
  isVerifying?: boolean;
  avatarClassName?: string;
  // Search-result rows (e.g. MemberPicker) sit inside an already-clickable
  // item, so a nested copy button there just competes with the row's own
  // select action — only rows for an already-added identity need it.
  showCopy?: boolean;
}) {
  const { t } = useTranslation("circles");
  const primary = primaryProfileLabel(pubkey, profile);
  const secondary = secondaryProfileLabel(pubkey, profile);
  const hasLightningAddress = !!profile?.lud16;

  return (
    <>
      <NostrAvatar
        pubkey={pubkey}
        profile={profile}
        className={cn("shrink-0", avatarClassName)}
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1.5">
          <span className="truncate text-sm font-semibold">{primary}</span>
          {isVerified ? (
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full bg-positive-foreground"
              aria-label={t("profileRow.nip05Verified")}
            />
          ) : (
            isVerifying && (
              <Skeleton
                className="h-1.5 w-1.5 shrink-0 rounded-full"
                aria-label={t("profileRow.verifyingNip05")}
              />
            )
          )}
        </span>
        {secondary && (
          <>
            {showCopy ? (
              <button
                type="button"
                onClick={() => copyToClipboard(secondary.fullNpub)}
                className="flex min-w-0 items-center gap-1 text-start font-mono text-xs text-muted-foreground hover:text-foreground"
              >
                <span className="truncate">{secondary.npub}</span>
                <Copy className="h-3 w-3 shrink-0" />
              </button>
            ) : (
              <span className="flex min-w-0 items-center gap-1 font-mono text-xs text-muted-foreground">
                <span className="truncate">{secondary.npub}</span>
              </span>
            )}
            {secondary.identifier && (
              <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
                <span className="truncate">{secondary.identifier}</span>
                {hasLightningAddress && (
                  <Zap
                    className="h-3 w-3 shrink-0 text-primary"
                    aria-label={t("profileRow.lightningAddress")}
                  />
                )}
              </span>
            )}
          </>
        )}
      </span>
    </>
  );
}
