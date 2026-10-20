import useSWRImmutable from "swr/immutable";

import { getAuthToken } from "src/lib/auth";
import { isHttpMode } from "src/utils/isHttpMode";
import { swrFetcher } from "src/utils/swr";
import { validateHTTPURL } from "src/utils/validation";

// AvatarImage is the desktop app's answer for /api/circle/avatar-proxy.
interface AvatarImage {
  contentType: string;
  data: string;
}

// useAvatarSrc returns an <img> src for a Nostr profile picture, always
// fetched through our own backend (avatar_proxy.go) so the browser never
// connects straight to an arbitrary media host. That would leak the user's IP
// (and which pubkeys they look up) to whoever hosts each picture. The route
// requires login, and an <img> can't send a header, so the web app puts its
// token in the URL. The desktop app has no token: it asks its backend for the
// bytes instead and shows them as a data URL. Anything but an http(s) URL, or
// a failed fetch, returns undefined so callers fall back to initials or an icon.
export function useAvatarSrc(picture: string | undefined): string | undefined {
  const validPicture =
    picture && validateHTTPURL(picture, "profile picture") === null
      ? picture
      : undefined;
  const proxyPath = validPicture
    ? `/api/circle/avatar-proxy?url=${encodeURIComponent(validPicture)}`
    : undefined;
  const desktop = !isHttpMode();

  const { data } = useSWRImmutable<AvatarImage>(
    desktop && proxyPath ? proxyPath : null,
    swrFetcher,
    { shouldRetryOnError: false }
  );

  if (!proxyPath) {
    return undefined;
  }
  if (desktop) {
    return data ? `data:${data.contentType};base64,${data.data}` : undefined;
  }
  const authToken = getAuthToken();
  return authToken
    ? `${proxyPath}&token=${encodeURIComponent(authToken)}`
    : undefined;
}
