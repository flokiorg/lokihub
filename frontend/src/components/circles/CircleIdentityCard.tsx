import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { Loader2, RefreshCwIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { AvatarStack } from "src/components/circles/AvatarStack";
import { NostrIdentityHeader } from "src/components/circles/NostrIdentityHeader";
import { Card, CardContent, CardHeader } from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Separator } from "src/components/ui/separator";
import { useCircleAllowlist } from "src/hooks/useCircleAllowlist";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { CircleIdentitySummary } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

dayjs.extend(relativeTime);

// CircleIdentityCard surfaces the Nostr identity behind a circle_hub
// app at the top of its detail page — name, NIP-05, npub (right under the
// name, same placement CircleCard uses for its header), and who's
// authorized all together, so "who is this circle actually run by, and
// who can use it" doesn't require cross-referencing the Permissions/
// Allowlist sections further down. Shows both NIP-05 and npub (not just
// whichever one exists) since a NIP-05 alone doesn't let anyone verify
// the identity against another Nostr client without the raw pubkey.
// For "following" policy the count already comes from our db (identity.
// followingCount) and is plain text below — unlike allowlist it's not a
// visual member list, so it doesn't need its own separator/section, and we
// don't re-fetch the following list/profiles just to render avatars for it.
export function CircleIdentityCard({
  appId,
  identity,
}: {
  appId: number;
  identity: CircleIdentitySummary & {
    followingCount?: number;
    allowlistCount: number;
    policySyncedAt?: string;
  };
}) {
  const { t } = useTranslation("circles");
  const isFollowing = identity.policy === "following";

  const { mutate } = useSWRConfig();
  const [isSyncing, setSyncing] = React.useState(false);

  const handleSync = async () => {
    setSyncing(true);
    try {
      await request(`/api/apps/${appId}/circle/refresh`, { method: "POST" });
      await mutate(`/api/apps/${appId}`);
      toast(t("circleIdentityCard.syncedToast"));
    } catch (error) {
      handleRequestError(t("circleIdentityCard.errors.sync"), error);
    }
    setSyncing(false);
  };

  const { data: allowlistData, isLoading: isAllowlistLoading } =
    useCircleAllowlist(appId, !isFollowing);
  const allowlistPubkeys = allowlistData?.pubkeys ?? [];
  const { profiles: allowlistProfiles, isLoading: isAllowlistProfilesLoading } =
    useNostrProfiles(!isFollowing ? allowlistPubkeys : []);

  const isMembersLoading = isAllowlistLoading || isAllowlistProfilesLoading;

  return (
    <Card>
      <CardHeader>
        <NostrIdentityHeader pubkey={identity.providerPubkey} />
      </CardHeader>
      <CardContent className="grid gap-3">
        {isFollowing ? (
          identity.followingCount === undefined ? (
            <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              {t("common.syncingFollowing")}
            </span>
          ) : (
            <div className="flex items-center justify-between gap-2">
              <span className="text-sm text-muted-foreground">
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
              <LoadingButton
                variant="outline"
                size="sm"
                loading={isSyncing}
                onClick={handleSync}
              >
                <RefreshCwIcon className="size-3.5" /> {t("common.sync")}
              </LoadingButton>
            </div>
          )
        ) : (
          <>
            <Separator />
            <AvatarStack
              pubkeys={allowlistPubkeys}
              profiles={allowlistProfiles}
              isLoading={isMembersLoading}
            />
            <span className="text-sm text-muted-foreground">
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
          </>
        )}
      </CardContent>
    </Card>
  );
}
