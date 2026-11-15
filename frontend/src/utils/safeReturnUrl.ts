// Schemes that run or show content inside this app's page when opened,
// instead of handing off to another app or site.
const unsafeProtocols = [
  "javascript:",
  "vbscript:",
  "data:",
  "blob:",
  "file:",
  "about:",
];

// safeReturnUrl returns url if it links to another app or site, or undefined.
// A return_to link comes from whoever sent the user here, and one that ran in
// this app's page would run with the user's session. URL parses the way the
// browser will navigate, so tabs, newlines and case are already normalised.
export function safeReturnUrl(
  url: string | null | undefined
): string | undefined {
  if (!url) {
    return undefined;
  }
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  if (unsafeProtocols.includes(parsed.protocol)) {
    return undefined;
  }
  return parsed.href;
}

// returnUrlOrigin is the web origin a postMessage to the opener should target,
// derived from the validated return_to. Only http(s) return_to URLs name a real
// web origin (a custom-scheme deep link has an opaque "null" origin and no web
// opener to reach), so anything else yields undefined and the caller must not
// broadcast to a wildcard "*" target.
export function returnUrlOrigin(
  url: string | null | undefined
): string | undefined {
  if (!url) {
    return undefined;
  }
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return undefined;
  }
  return parsed.origin;
}
