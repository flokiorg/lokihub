import { useTranslation } from "react-i18next";
import { AvatarStack } from "src/components/circles/AvatarStack";
import { WarningAlert } from "src/components/circles/WarningAlert";
import { useNostrFollowing } from "src/hooks/useNostrFollowing";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";

// FollowingPreview shows a read-only confirmation of who will be authorized
// under "Sync with following" — no per-person selection needed, this is the
// zero-extra-click path for the member-authorization step.
export function FollowingPreview({
  ownerPubkeyHex,
}: {
  ownerPubkeyHex: string | undefined;
}) {
  const { t } = useTranslation("circles");
  const { followingPubkeys, isLoading: isFollowingLoading } =
    useNostrFollowing(ownerPubkeyHex);
  const { profiles, isLoading: isProfilesLoading } = useNostrProfiles(
    ownerPubkeyHex ? followingPubkeys : []
  );

  if (!ownerPubkeyHex) {
    return null;
  }

  const isLoading = isFollowingLoading || isProfilesLoading;

  if (!isLoading && followingPubkeys.length === 0) {
    return <WarningAlert>{t("followingPreview.empty")}</WarningAlert>;
  }

  return (
    <div className="flex flex-col gap-2">
      <AvatarStack
        pubkeys={followingPubkeys}
        profiles={profiles}
        isLoading={isLoading}
      />
      {!isLoading && (
        <p className="text-sm text-muted-foreground">
          {t("followingPreview.summary", { count: followingPubkeys.length })}
        </p>
      )}
    </div>
  );
}
