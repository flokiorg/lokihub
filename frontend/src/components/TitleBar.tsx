import { Minus, Square, SquaresIntersect, X } from "lucide-react";
import { useEffect, useState } from "react";
import { cn } from "src/lib/utils";
import { isHttpMode } from "src/utils/isHttpMode";
import {
  Environment,
  Quit,
  WindowIsMaximised,
  WindowMinimise,
  WindowToggleMaximise,
} from "wailsjs/runtime/runtime";

type WailsPlatform = "windows" | "darwin" | "linux";

// Reserves top space for the OS-drawn window chrome. On Windows the app is
// launched Frameless (see wails/wails_app.go), so this bar IS the window
// chrome - it renders its own minimize/maximize/close buttons. On macOS the
// window uses mac.TitleBarHiddenInset(), which keeps the native traffic
// lights but hides the title bar's own height, so this bar exists purely to
// stop app content from sitting directly under those inset buttons - it
// renders no buttons of its own there. Linux keeps its native window
// decorations entirely, so nothing renders there.
export function TitleBar() {
  const [platform, setPlatform] = useState<WailsPlatform | null>(null);
  const [isMaximised, setIsMaximised] = useState(false);

  useEffect(() => {
    if (isHttpMode()) {
      return;
    }
    Environment().then((env) => setPlatform(env.platform as WailsPlatform));
  }, []);

  // The bar itself renders (and so takes up real layout height) for both
  // "windows" and "darwin" below - keep this condition in sync with that,
  // not just with the platform that has buttons in it.
  const rendersBar = platform === "windows" || platform === "darwin";

  useEffect(() => {
    if (!rendersBar) {
      return;
    }
    document.documentElement.dataset.hasCustomTitlebar = "true";
    return () => {
      delete document.documentElement.dataset.hasCustomTitlebar;
    };
  }, [rendersBar]);

  useEffect(() => {
    if (platform !== "windows") {
      return;
    }
    const syncMaximised = () => {
      WindowIsMaximised().then(setIsMaximised);
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
      className="flex h-8 w-full shrink-0 select-none items-center justify-end bg-background"
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
            onClick={() => WindowMinimise()}
          >
            <Minus className="h-4 w-4" />
          </button>
          <button
            type="button"
            aria-label={isMaximised ? "Restore" : "Maximize"}
            className="flex h-full w-11 items-center justify-center text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => {
              WindowToggleMaximise();
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
            onClick={() => Quit()}
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      )}
    </div>
  );
}
