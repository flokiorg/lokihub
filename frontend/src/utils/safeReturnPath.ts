// safeReturnPath returns path if it is a plain path inside this app, or
// undefined. The return-to targets are saved from the URL a visitor opened
// while the hub was locked and replayed after unlock, so a crafted link
// must not be able to name another origin: "//host" and "/\host" are read
// by browsers as protocol-relative, and a backslash anywhere can be
// normalised into one.
export function safeReturnPath(path: string | null | undefined): string | undefined {
  if (!path || !path.startsWith("/")) {
    return undefined;
  }
  if (path.startsWith("//") || path.includes("\\")) {
    return undefined;
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(path)) {
    return undefined;
  }
  return path;
}
