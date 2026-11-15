import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import { Skeleton } from "src/components/ui/skeleton";
import { NostrAvatar } from "src/components/NostrAvatar";
import { NostrProfile } from "src/hooks/useNostrProfiles";

// AvatarStack renders overlapping "coin" avatars for a set of pubkeys,
// collapsing anything past `max` into a "+N more" badge. Shared by
// CircleCard (existing circles) and FollowingPreview (pre-creation preview)
// so both surfaces render member lists identically.
export function AvatarStack({
  pubkeys,
  profiles,
  isLoading,
  max = 8,
}: {
  pubkeys: string[];
  profiles: Map<string, NostrProfile>;
  isLoading: boolean;
  max?: number;
}) {
  const { t } = useTranslation("circles");
  const visible = pubkeys.slice(0, max);
  const overflowCount = pubkeys.length - visible.length;

  if (isLoading) {
    return (
      <div className="flex -space-x-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton
            key={i}
            className="h-7 w-7 rounded-full border-2 border-background"
          />
        ))}
      </div>
    );
  }

  return (
    <div className="flex items-center">
      <div className="flex -space-x-2">
        {visible.map((pubkey) => (
          <NostrAvatar
            key={pubkey}
            pubkey={pubkey}
            profile={profiles.get(pubkey)}
            isLoading={false}
            className="h-7 w-7 border-2 border-background"
          />
        ))}
      </div>
      {overflowCount > 0 && (
        <Badge variant="outline" className="ms-2">
          {t("avatarStack.more", { count: overflowCount })}
        </Badge>
      )}
    </div>
  );
}
