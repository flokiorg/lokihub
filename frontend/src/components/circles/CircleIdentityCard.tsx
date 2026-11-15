import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { Loader2, RefreshCwIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { AvatarStack } from "src/components/circles/AvatarStack";
import { FollowSetEditDialog } from "src/components/circles/FollowSetEditDialog";
import { NostrIdentityHeader } from "src/components/circles/NostrIdentityHeader";
import { WarningAlert } from "src/components/circles/WarningAlert";
import { Card, CardContent, CardHeader } from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Separator } from "src/components/ui/separator";
import { useCircleAllowlist } from "src/hooks/useCircleAllowlist";
import { useCircleFollowSet } from "src/hooks/useCircleFollowSet";
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
// For a follow set, the header is the set's author, and the members are
// the set as this hub reads it, hidden members included.
export function CircleIdentityCard({
  appId,
  identity,
}: {
  appId: number;
  identity: CircleIdentitySummary & {
    memberCount?: number;
    followSetTitle?: string;
    allowlistCount: number;
    policySyncedAt?: string;
  };
}) {
  const { t } = useTranslation("circles");
  const isFollowSet = identity.policy === "follow_set";

  const { mutate } = useSWRConfig();
  const [isSyncing, setSyncing] = React.useState(false);

  const { data: followSet, isLoading: isFollowSetLoading } = useCircleFollowSet(
    isFollowSet ? identity.id : undefined
  );
  const followSetMembers = React.useMemo(
    () =>
      followSet ? [...followSet.publicMembers, ...followSet.hiddenMembers] : [],
    [followSet]
  );

  const handleSync = async () => {
    setSyncing(true);
    try {
      await request(`/api/apps/${appId}/circle/refresh`, { method: "POST" });
      await mutate(`/api/apps/${appId}`);
      await mutate(`/api/circle-identities/${identity.id}/follow-set`);
      toast(t("circleIdentityCard.syncedToast"));
    } catch (error) {
      handleRequestError(t("circleIdentityCard.errors.sync"), error);
    }
    setSyncing(false);
  };

  const { data: allowlistData, isLoading: isAllowlistLoading } =
    useCircleAllowlist(appId, !isFollowSet);
  const memberPubkeys = isFollowSet
    ? followSetMembers
    : (allowlistData?.pubkeys ?? []);
  const { profiles, isLoading: isProfilesLoading } =
    useNostrProfiles(memberPubkeys);

  const isMembersLoading =
    (isFollowSet ? isFollowSetLoading : isAllowlistLoading) ||
    isProfilesLoading;

  return (
    <Card>
      <CardHeader>
        <NostrIdentityHeader pubkey={identity.providerPubkey} />
      </CardHeader>
      <CardContent className="grid gap-3">
        {isFollowSet ? (
          <>
            <div className="grid gap-0.5">
              {identity.followSetTitle && (
                <span className="font-medium">{identity.followSetTitle}</span>
              )}
              <span className="font-mono text-xs text-muted-foreground break-all">
                {t("followSet.addressLabel", { d: identity.followSetD })}
              </span>
            </div>
            {followSet && !followSet.resolved && (
              <WarningAlert>{t("followSet.unresolved")}</WarningAlert>
            )}
            <Separator />
            <AvatarStack
              pubkeys={memberPubkeys}
              profiles={profiles}
              isLoading={isMembersLoading}
            />
            {identity.memberCount === undefined ? (
              <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                {t("common.readingFollowSet")}
              </span>
            ) : (
              <span className="text-sm text-muted-foreground">
                {t("circleCard.member", { count: identity.memberCount })}
                {!!followSet?.hiddenMembers.length && (
                  <>
                    {" "}
                    ·{" "}
                    {t("followSet.hiddenCount", {
                      count: followSet.hiddenMembers.length,
                    })}
                  </>
                )}
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
            )}
            <div className="flex flex-wrap items-center gap-2">
              <LoadingButton
                variant="outline"
                size="sm"
                loading={isSyncing}
                onClick={handleSync}
              >
                <RefreshCwIcon className="size-3.5" /> {t("common.sync")}
              </LoadingButton>
              {identity.managed && followSet && (
                <FollowSetEditDialog
                  identityId={identity.id}
                  followSet={followSet}
                />
              )}
            </div>
            {!identity.managed && (
              <p className="text-sm text-muted-foreground">
                {t("followSet.readOnly")}
              </p>
            )}
          </>
        ) : (
          <>
            <Separator />
            <AvatarStack
              pubkeys={memberPubkeys}
              profiles={profiles}
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
