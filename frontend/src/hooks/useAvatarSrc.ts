import { useImageProxySrc } from "src/hooks/useImageProxySrc";

// useAvatarSrc returns an <img> src for a Nostr profile picture, resolved
// through the user's image-proxy setting (P0-B). By default (mode "off") it
// returns undefined so NostrAvatar falls back to 2-letter initials and no
// external media host is ever contacted. The user opts in under Settings >
// Services > "Image proxy": "direct" loads the source URL straight from its
// host, "proxy" routes it through a user-provided template. See
// useImageProxySrc for the shared resolution (including the built-in
// same-origin proxy's token / desktop-data-URL handling).
export function useAvatarSrc(picture: string | undefined): string | undefined {
  return useImageProxySrc(picture);
}
