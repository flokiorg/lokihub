import useSWRImmutable from "swr/immutable";

import { useHubInfo } from "src/hooks/useHubInfo";
import { getAuthToken } from "src/lib/auth";
import { isHttpMode } from "src/utils/isHttpMode";
import { resolveImageProxyUrl, usesBuiltinProxy } from "src/utils/imageProxy";
import { swrFetcher } from "src/utils/swr";

// AvatarImage is the desktop app's answer for the built-in /api/circle/avatar-proxy.
interface AvatarImage {
  contentType: string;
  data: string;
}

// useImageProxySrc resolves a remote profile-image URL (an avatar picture or a
// node banner) to the <img>/background src that should actually be loaded,
// honoring the user's image-proxy setting (P0-B), read from the hub-info hook:
//
//   off (default) -> undefined (caller shows initials / neutral background)
//   direct        -> the validated source URL, loaded straight from its host
//   proxy         -> the source URL substituted into the user's template
//                    ({url} percent-encoded, {b64url} base64url)
//
// The hub never loads any external image until the user opts in, so while the
// setting is still loading (mode undefined) this returns undefined too.
//
// The built-in same-origin proxy (http/avatar_proxy.go) is the one template that
// needs extra handling: it requires login, and an <img> can't send a header, so
// the web app puts its token in the URL and the desktop app (no token) fetches
// the bytes and shows them as a data URL. That handling is applied ONLY when the
// resolved URL points at that built-in proxy; any other template (direct host or
// external proxy) is returned as-is.
export function useImageProxySrc(
  source: string | undefined
): string | undefined {
  const { data: hubInfo } = useHubInfo();
  const mode = hubInfo?.imageProxyMode;
  const template = hubInfo?.imageProxyTemplate;

  const resolved = resolveImageProxyUrl(source, mode, template);
  const builtin = mode === "proxy" && usesBuiltinProxy(resolved);
  const desktop = !isHttpMode();

  const { data } = useSWRImmutable<AvatarImage>(
    builtin && desktop && resolved ? resolved : null,
    swrFetcher,
    { shouldRetryOnError: false }
  );

  if (!resolved) {
    return undefined;
  }
  if (!builtin) {
    return resolved;
  }
  if (desktop) {
    return data ? `data:${data.contentType};base64,${data.data}` : undefined;
  }
  const authToken = getAuthToken();
  return authToken
    ? `${resolved}&token=${encodeURIComponent(authToken)}`
    : undefined;
}
