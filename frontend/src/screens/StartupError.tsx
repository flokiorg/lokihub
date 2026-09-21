import { TriangleAlertIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Button } from "src/components/ui/button";
import { useTheme } from "src/components/ui/theme-provider";
import { StarryNight } from "src/screens/Intro";
import { isHttpMode } from "src/utils/isHttpMode";
import { AppError } from "src/utils/request";

function closeApp() {
  if (isHttpMode()) {
    window.close();
    return;
  }
  // @ts-expect-error - runtime is injected by wails
  window.runtime.Quit();
}

// Full-screen replacement for the whole app when the backend failed to start
// (see LaunchStartupErrorApp in wails/startup_error.go). Styled after the
// pre-onboarding Intro screen, and shows nothing but the error.
export function StartupError({ error }: { error: AppError }) {
  const { t } = useTranslation("common");
  const { setDarkMode } = useTheme();

  React.useEffect(() => {
    // Same as Intro: the starfield only reads well in dark mode.
    setDarkMode("dark");
    return () => setDarkMode("system");
  }, [setDarkMode]);

  return (
    <StarryNight>
      <div className="flex flex-col justify-center items-center h-screen gap-8 p-5">
        <TriangleAlertIcon className="w-16 h-16 text-destructive" />
        <div className="flex flex-col gap-4 text-center items-center max-w-lg w-full">
          <div className="text-3xl font-semibold text-foreground">
            {t("startupError.title")}
          </div>
          <div className="text-lg text-muted-foreground font-semibold">
            {t("startupError.description")}
          </div>
          <div
            dir="ltr"
            className="w-full p-3 bg-muted rounded-md font-mono text-sm text-start break-all max-h-40 overflow-y-auto select-text"
          >
            {error.message}
          </div>
          {error.body?.version && (
            <div className="text-xs text-muted-foreground font-mono">
              Lokihub {error.body.version}
            </div>
          )}
        </div>
        <Button size="lg" onClick={closeApp}>
          {t("startupError.close")}
        </Button>
      </div>
    </StarryNight>
  );
}
