import { ImageProxyMode } from "src/types";
import { validateHTTPURL } from "src/utils/validation";

// The built-in, same-origin hardened proxy (http/avatar_proxy.go). Offered as a
// one-click suggested template value — NOT the default. The default is "off".
export const BUILTIN_IMAGE_PROXY_TEMPLATE = "/api/circle/avatar-proxy?url={url}";

// base64urlEncode encodes a string as URL-safe base64 with no padding, matching
// Go's base64.RawURLEncoding. UTF-8 is encoded first so non-latin1 URLs survive
// btoa.
export function base64urlEncode(input: string): string {
  const bytes = new TextEncoder().encode(input);
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

// resolveImageProxyUrl turns a source profile-image URL into the URL that should
// actually be loaded, per the user's image-proxy setting (P0-B). It returns
// undefined whenever nothing should be loaded — mode "off", a missing or
// non-http(s) source, or a "proxy" template with no usable placeholder — so
// callers fall back to initials / neutral background. This is the single shared
// place the policy lives; both avatars and the node banner go through it.
export function resolveImageProxyUrl(
  source: string | undefined,
  mode: ImageProxyMode | undefined,
  template: string | undefined
): string | undefined {
  // Only ever consider http(s) sources; anything else is dropped before any
  // mode is applied.
  if (!source || validateHTTPURL(source, "image") !== null) {
    return undefined;
  }

  switch (mode) {
    case "direct":
      return source;
    case "proxy": {
      const tpl = (template ?? "").trim();
      if (tpl.includes("{url}")) {
        return tpl.replace("{url}", encodeURIComponent(source));
      }
      if (tpl.includes("{b64url}")) {
        return tpl.replace("{b64url}", base64urlEncode(source));
      }
      // A "proxy" mode with no placeholder can't produce a URL — treat as off.
      return undefined;
    }
    // "off" and anything unknown (including an unloaded setting) load nothing.
    default:
      return undefined;
  }
}

// usesBuiltinProxy reports whether a resolved URL points at the built-in
// same-origin proxy, which (unlike an external template) needs the login token /
// desktop data-URL handling in useImageProxySrc.
export function usesBuiltinProxy(resolved: string | undefined): boolean {
  return !!resolved && resolved.startsWith("/api/circle/avatar-proxy");
}
