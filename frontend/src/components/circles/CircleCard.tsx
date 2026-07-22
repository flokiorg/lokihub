import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { Card, CardContent, CardHeader } from "src/components/ui/card";
import { Skeleton } from "src/components/ui/skeleton";
import { AvatarStack } from "src/components/circles/AvatarStack";
import { AppCardConnectionInfo } from "src/components/connections/AppCardConnectionInfo";
import { NostrAvatar } from "src/components/NostrAvatar";
import { useCircleAllowlist } from "src/hooks/useCircleAllowlist";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { App } from "src/types";
import { safeNpubEncode, shortenMiddle } from "src/utils/nostr";

dayjs.extend(relativeTime);

export function CircleCard({ app }: { app: App }) {
  const navigate = useNavigate();
  const { t } = useTranslation("circles");
  const identity = app.circleIdentity;

  // Every hook below must run unconditionally (rules of hooks) — identity is
  // only undefined for a non-circle_hub app, which SubwalletList.tsx
  // never renders as a CircleCard, but we guard the body defensively anyway.
  const providerPubkey = identity?.providerPubkey ?? "";
  const { profile, isLoading: isProfileLoading } = useNostrProfile(providerPubkey || undefined);

  const isAllowlist = identity?.policy === "allowlist";
  const { data: allowlistData, isLoading: isAllowlistLoading } = useCircleAllowlist(
    app.id,
    isAllowlist
  );
  const memberPubkeys = allowlistData?.pubkeys ?? [];
  const { profiles: memberProfiles, isLoading: isMembersLoading } = useNostrProfiles(
    isAllowlist ? memberPubkeys : []
  );

  if (!identity) {
    return null;
  }

  const npub = safeNpubEncode(providerPubkey);
  const shortNpub = npub ? shortenMiddle(npub) : undefined;

  return (
    <Card
      className="flex flex-col cursor-pointer"
      onClick={() => navigate(`/apps/${app.id}`)}
    >
      <CardHeader>
        <div className="flex flex-row items-center gap-3">
          <NostrAvatar
            pubkey={providerPubkey}
            profile={profile}
            isLoading={isProfileLoading}
            className="h-10 w-10"
          />
          <div className="flex-1 min-w-0">
            <div className="font-semibold text-lg truncate">{app.name}</div>
            {isProfileLoading ? (
              <Skeleton className="h-4 w-32 mt-1" />
            ) : (
              <>
                {profile?.nip05 && (
                  <div className="text-sm text-muted-foreground truncate">
                    {profile.nip05}
                  </div>
                )}
                {shortNpub && (
                  <div className="text-xs text-muted-foreground/70 font-mono truncate">
                    {shortNpub}
                  </div>
                )}
              </>
            )}
          </div>
          {isAllowlist && (
            <AvatarStack
              pubkeys={memberPubkeys}
              profiles={memberProfiles}
              isLoading={isAllowlistLoading || isMembersLoading}
              max={5}
            />
          )}
        </div>
      </CardHeader>
      <CardContent className="flex-1 flex flex-col gap-3 slashed-zero">
        {identity.policy === "following" && (
          identity.followingCount === undefined ? (
            <span className="text-sm text-muted-foreground flex items-center gap-1.5">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              {t("common.syncingFollowing")}
            </span>
          ) : (
            <span className="text-sm">
              {t("common.following", { count: identity.followingCount })}
              {identity.policySyncedAt && (
                <>
                  {" "}
                  ·{" "}
                  {t("common.synced", {
                    time: dayjs(identity.policySyncedAt).fromNow(),
                  })}
                </>
              )}
            </span>
          )
        )}
        {isAllowlist && (
          <span className="text-sm">
            {t("circleCard.member", { count: identity.allowlistCount })}
            {identity.policySyncedAt && (
              <>
                {" "}
                ·{" "}
                {t("common.updated", {
                  time: dayjs(identity.policySyncedAt).fromNow(),
                })}
              </>
            )}
          </span>
        )}
        <AppCardConnectionInfo connection={app} />
      </CardContent>
    </Card>
  );
}
