import React from "react";

import { Avatar, AvatarFallback, AvatarImage } from "src/components/ui/avatar";
import { Skeleton } from "src/components/ui/skeleton";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { NostrProfile } from "src/hooks/useNostrProfiles";
import { getAuthToken } from "src/lib/auth";
import { cn } from "src/lib/utils";
import { safeNpubEncode } from "src/utils/nostr";
import { validateHTTPURL } from "src/utils/validation";

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

  if (isLoading) {
    return <Skeleton className={cn("h-8 w-8 rounded-full", className)} />;
  }

  // Only ever pass an http(s) URL to <img src> — a malformed or non-http(s)
  // "picture" field (or one that fails to load) falls back to initials,
  // never a broken-image icon. Routed through our own backend
  // (avatar_proxy.go) instead of hotlinked directly, so the browser never
  // connects straight to an arbitrary Nostr media host — that would leak
  // the user's IP (and which pubkeys they look up) to whoever hosts each
  // picture. The route requires login (SSRF-probe surface, not just image
  // data), and an <img> tag can't set an Authorization header, so the token
  // is passed as a query param — the same fallback TokenLookup already
  // supports server-side for exactly this case.
  const authToken = getAuthToken();
  const pictureUrl =
    profile?.picture && validateHTTPURL(profile.picture, "profile picture") === null && authToken
      ? `/api/circle/avatar-proxy?url=${encodeURIComponent(profile.picture)}&token=${encodeURIComponent(authToken)}`
      : undefined;
  const showImage = !!pictureUrl && !imageFailed;

  const fallbackSource =
    profile?.displayName || profile?.name || safeNpubEncode(pubkey) || pubkey;
  const fallbackText = fallbackSource.replace(/^npub1/, "").slice(0, 2).toUpperCase();

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
