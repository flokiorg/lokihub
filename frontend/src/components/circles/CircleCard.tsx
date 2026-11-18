import { ChevronRightIcon, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";

import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { NostrAvatar } from "src/components/NostrAvatar";
import { Skeleton } from "src/components/ui/skeleton";
import { useCircleAllowlist } from "src/hooks/useCircleAllowlist";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { App } from "src/types";
import { safeNpubEncode, shortenMiddle } from "src/utils/nostr";

// A circle in the Sub-wallets list — name + identity, a member count, its
// balance — as its own card, matching the Cash Hub list. The circle's own
// detail page carries the full membership roster and connection detail.
export function CircleCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");
  const identity = app.circleIdentity;

  // Hooks run unconditionally (rules of hooks); identity is only undefined for
  // a non-circle_hub app, which SubwalletList never renders here.
  const providerPubkey = identity?.providerPubkey ?? "";
  const { profile, isLoading: isProfileLoading } = useNostrProfile(
    providerPubkey || undefined
  );
  const isAllowlist = identity?.policy === "allowlist";
  const { data: allowlistData } = useCircleAllowlist(app.id, isAllowlist);

  if (!identity) {
    return null;
  }

  const npub = safeNpubEncode(providerPubkey);
  const subtitle = profile?.nip05 || (npub ? shortenMiddle(npub) : undefined);
  const memberCount = isAllowlist
    ? (allowlistData?.pubkeys.length ?? identity.allowlistCount)
    : identity.memberCount;

  return (
    <Link
      to={`/apps/${app.id}`}
      className="slashed-zero flex min-w-0 items-center gap-3 rounded-xl border bg-card p-3 transition-colors hover:border-primary/50"
    >
      <NostrAvatar
        pubkey={providerPubkey}
        profile={profile}
        isLoading={isProfileLoading}
        className="size-8 shrink-0"
      />
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{app.name}</p>
        {isProfileLoading ? (
          <Skeleton className="mt-1 h-3 w-28" />
        ) : (
          subtitle && (
            <p className="truncate font-mono text-xs text-muted-foreground">
              {subtitle}
            </p>
          )
        )}
      </div>
      <span className="hidden shrink-0 text-xs text-muted-foreground sm:block">
        {memberCount === undefined ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          t("circleCard.member", { count: memberCount })
        )}
      </span>
      <span className="sensitive shrink-0 text-sm font-medium">
        <FormattedFlokicoinAmount amount={app.balance} />
      </span>
      <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" />
    </Link>
  );
}
