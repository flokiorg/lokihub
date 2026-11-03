import { isHttpMode } from "src/utils/isHttpMode";

// canOpenFolder reports whether openFolder can do anything: the desktop (Wails)
// build with a known path. The web build has no local filesystem to reveal.
export function canOpenFolder(path?: string | null): path is string {
  return !isHttpMode() && !!path;
}

// openFolder reveals a local directory in the OS file manager. Desktop only.
// window.runtime.BrowserOpenURL is injected by Wails on every window —
// including the startup-error window, which binds no Go methods — so this
// works even when the backend never came up.
export function openFolder(path: string) {
  if (!canOpenFolder(path)) {
    return;
  }
  // Build a file:// URL: normalize Windows backslashes, make the path
  // absolute ("C:/..." -> "/C:/..."), and encode spaces (e.g. macOS
  // "Application Support").
  let p = path.replace(/\\/g, "/");
  if (!p.startsWith("/")) {
    p = "/" + p;
  }
  const url = "file://" + encodeURI(p);
  // @ts-expect-error - runtime is injected by wails
  window.runtime?.BrowserOpenURL(url);
}
