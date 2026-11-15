import { Minus, Square, SquaresIntersect, X } from "lucide-react";
import { useEffect, useState } from "react";
import { cn } from "src/lib/utils";
import { isHttpMode } from "src/utils/isHttpMode";

type WailsPlatform = "windows" | "darwin" | "linux";

// The generated frontend/wailsjs bindings only exist in a `wails build`/
// `wails dev` output - this same frontend source also ships as the plain
// web build (yarn build, served by the HTTP backend), which has no such
// module to import. Wails injects the identical API as window.runtime at
// runtime instead (see frontend/wailsjs/runtime/runtime.js - every binding
// is a thin wrapper around exactly this), so call through that directly,
// matching the existing convention in LSPEventContext.tsx.
function wailsRuntime() {
  // @ts-expect-error - runtime is injected by wails
  return window.runtime as {
    Environment: () => Promise<{ platform: string }>;
    Quit: () => void;
    WindowIsMaximised: () => Promise<boolean>;
    WindowMinimise: () => void;
    WindowToggleMaximise: () => void;
  };
}

// Windows uses a custom bar in normal flow. On macOS, a transparent drag
// region overlays the full-size WebView beneath the native traffic lights.
// Layouts reserve space for controls without pushing their backgrounds down.
// Linux keeps its native window decorations.
export function TitleBar() {
  const [platform, setPlatform] = useState<WailsPlatform | null>(null);
  const [isMaximised, setIsMaximised] = useState(false);

  useEffect(() => {
    if (isHttpMode()) {
      return;
    }
    wailsRuntime()
      .Environment()
      .then((env) => setPlatform(env.platform as WailsPlatform));
  }, []);

  useEffect(() => {
    if (platform !== "windows" && platform !== "darwin") {
      return;
    }
    document.documentElement.dataset.titlebarPlatform = platform;
    return () => {
      delete document.documentElement.dataset.titlebarPlatform;
    };
  }, [platform]);

  useEffect(() => {
    if (platform !== "windows") {
      return;
    }
    const syncMaximised = () => {
      wailsRuntime().WindowIsMaximised().then(setIsMaximised);
    };
    syncMaximised();
    // Catches double-click-on-drag-region maximize/restore, which bypasses
    // the button below, so local state can go stale without this.
    window.addEventListener("resize", syncMaximised);
    return () => window.removeEventListener("resize", syncMaximised);
  }, [platform]);

  if (isHttpMode() || platform === null || platform === "linux") {
    return null;
  }

  return (
    <div
      className={cn(
        "flex h-8 w-full shrink-0 select-none items-center justify-end",
        platform === "darwin"
          ? "fixed inset-x-0 top-0 z-40 bg-transparent"
          : "bg-background"
      )}
      style={{ ["--wails-draggable" as string]: "drag" }}
    >
      {platform === "windows" && (
        <div
          className="flex h-full items-stretch"
          style={{ ["--wails-draggable" as string]: "no-drag" }}
        >
          <button
            type="button"
            aria-label="Minimize"
            className="flex h-full w-11 items-center justify-center text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => wailsRuntime().WindowMinimise()}
          >
            <Minus className="h-4 w-4" />
          </button>
          <button
            type="button"
            aria-label={isMaximised ? "Restore" : "Maximize"}
            className="flex h-full w-11 items-center justify-center text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => {
              wailsRuntime().WindowToggleMaximise();
              setIsMaximised((prev) => !prev);
            }}
          >
            {isMaximised ? (
              <SquaresIntersect className="h-3.5 w-3.5" />
            ) : (
              <Square className="h-3.5 w-3.5" />
            )}
          </button>
          <button
            type="button"
            aria-label="Close"
            className={cn(
              "flex h-full w-11 items-center justify-center text-muted-foreground",
              "hover:bg-destructive hover:text-destructive-foreground"
            )}
            onClick={() => wailsRuntime().Quit()}
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      )}
    </div>
  );
}
