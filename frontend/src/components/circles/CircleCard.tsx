import { ChevronRightIcon, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";

import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { NostrAvatar } from "src/components/NostrAvatar";
import { Badge } from "src/components/ui/badge";
import { Skeleton } from "src/components/ui/skeleton";
import { useCircleAllowlist } from "src/hooks/useCircleAllowlist";
import { useListings } from "src/hooks/useListings";
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
  // SWR dedupes "/api/listings" across every card, so this is one request for
  // the whole list. "Listed" = this circle has a live (non-deleted) listing.
  const { data: listings } = useListings();
  const isListed = !!listings?.some(
    (l) => l.hubAppId === app.id && l.phase !== "deleted" && l.status !== "closed"
  );

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
        {/* Mobile secondary line: the discoverability/lifecycle chips and the
            member count reflow under the name instead of being dropped at
            narrow widths (design hides nothing on mobile). The desktop columns
            below carry the same data from sm: up. */}
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5 sm:hidden">
          <Badge
            variant={isListed ? "warning" : "secondary"}
            className="px-2 py-0 text-xs font-normal"
          >
            {isListed ? t("circleCard.listed") : t("circleCard.private")}
          </Badge>
          {app.circleTeardownRequestedAt ? (
            <Badge
              variant="warning"
              className="px-2 py-0 text-xs font-normal"
            >
              {t("circleCard.closing", "Closing")}
            </Badge>
          ) : app.circleAccepting ? (
            <Badge
              variant="positive"
              className="px-2 py-0 text-xs font-normal"
            >
              {t("circleCard.accepting", "Accepting")}
            </Badge>
          ) : null}
          <span className="text-xs text-muted-foreground">
            {memberCount === undefined ? (
              <Loader2 className="inline size-3 animate-spin" />
            ) : (
              memberCount
            )}{" "}
            {t("circleCard.membersLabel")}
          </span>
        </div>
      </div>
      {/* Discoverability + lifecycle chips (design row): Listed/Private, then
          Accepting/Closing. Hidden on the narrowest widths to protect the name. */}
      <Badge
        variant={isListed ? "warning" : "secondary"}
        className="hidden shrink-0 sm:inline-flex"
      >
        {isListed ? t("circleCard.listed") : t("circleCard.private")}
      </Badge>
      {app.circleTeardownRequestedAt ? (
        <Badge variant="warning" className="hidden shrink-0 sm:inline-flex">
          {t("circleCard.closing", "Closing")}
        </Badge>
      ) : app.circleAccepting ? (
        <Badge variant="positive" className="hidden shrink-0 sm:inline-flex">
          {t("circleCard.accepting", "Accepting")}
        </Badge>
      ) : null}
      {/* Members and Held read as labeled columns, matching the design row. */}
      <div className="hidden shrink-0 text-right sm:block">
        <p className="text-xs text-muted-foreground">
          {t("circleCard.membersLabel")}
        </p>
        <p className="text-sm font-medium">
          {memberCount === undefined ? (
            <Loader2 className="inline size-3.5 animate-spin" />
          ) : (
            memberCount
          )}
        </p>
      </div>
      <div className="shrink-0 text-right">
        <p className="text-xs text-muted-foreground">
          {t("circleCard.heldLabel")}
        </p>
        <p className="sensitive text-sm font-medium">
          <FormattedFlokicoinAmount amount={app.balance} />
        </p>
      </div>
      <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" />
    </Link>
  );
}
