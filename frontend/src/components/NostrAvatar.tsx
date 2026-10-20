import React from "react";

import { Avatar, AvatarFallback, AvatarImage } from "src/components/ui/avatar";
import { Skeleton } from "src/components/ui/skeleton";
import { useAvatarSrc } from "src/hooks/useAvatarSrc";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { NostrProfile } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { safeNpubEncode } from "src/utils/nostr";

export function NostrAvatar({
  pubkey,
  profile: profileOverride,
  isLoading: isLoadingOverride,
  className,
}: {
  pubkey: string;
  // Pass an already-resolved profile (e.g. from a parent's batched
  // useNostrProfiles call) to skip this component's own fetch entirely.
  profile?: NostrProfile;
  isLoading?: boolean;
  className?: string;
}) {
  const noOverrideGiven =
    profileOverride === undefined && isLoadingOverride === undefined;
  const fetched = useNostrProfile(noOverrideGiven ? pubkey : undefined);

  const profile = profileOverride ?? fetched.profile;
  const isLoading = isLoadingOverride ?? fetched.isLoading;

  const [imageFailed, setImageFailed] = React.useState(false);
  const pictureUrl = useAvatarSrc(profile?.picture);

  if (isLoading) {
    return <Skeleton className={cn("h-8 w-8 rounded-full", className)} />;
  }

  // A malformed or non-http(s) "picture" field (or one that fails to load)
  // falls back to initials, never a broken-image icon. See useAvatarSrc for
  // why the image always comes through our own backend.
  const showImage = !!pictureUrl && !imageFailed;

  const fallbackSource =
    profile?.displayName || profile?.name || safeNpubEncode(pubkey) || pubkey;
  const fallbackText = fallbackSource
    .replace(/^npub1/, "")
    .slice(0, 2)
    .toUpperCase();

  return (
    <Avatar className={cn("h-8 w-8 rounded-full", className)}>
      {showImage && (
        <AvatarImage
          src={pictureUrl}
          alt={profile?.name ?? "avatar"}
          onError={() => setImageFailed(true)}
        />
      )}
      <AvatarFallback className="text-[10px] font-medium leading-none">
        {fallbackText}
      </AvatarFallback>
    </Avatar>
  );
}
