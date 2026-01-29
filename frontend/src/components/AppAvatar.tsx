import UserAvatar from "src/components/UserAvatar";
import { LOKI_ACCOUNT_APP_NAME } from "src/constants";
import { useAppLogo } from "src/hooks/useAppLogo";
import { useAppStore } from "src/hooks/useAppStore";
import { cn } from "src/lib/utils";
import { App } from "src/types";

type Props = {
  app: App;
  className?: string;
};

export default function AppAvatar({ app, className }: Props) {
  const { apps: appStoreApps } = useAppStore();

  const appStoreApp = appStoreApps.find(
    (suggestedApp) =>
      (app?.metadata?.app_store_app_id &&
        suggestedApp.id === app.metadata?.app_store_app_id) ||
      app.name.toLowerCase().includes(suggestedApp.title.toLowerCase())
  );

  const logoSrc = useAppLogo(appStoreApp?.id);

  if (app.name === LOKI_ACCOUNT_APP_NAME) {
    return <UserAvatar className={className} />;
  }

  const gradient =
    app.name
      .split("")
      .map((c) => c.charCodeAt(0))
      .reduce((a, b) => a + b, 0) % 10;
  return (
    <div
      className={cn(
        "rounded-lg relative overflow-hidden",
        !logoSrc && `avatar-gradient-${gradient}`,
        className
      )}
    >
      {logoSrc && (
        <img
          src={logoSrc}
          className={cn("absolute w-full h-full rounded-lg", className)}
        />
      )}
      {!logoSrc && (
        <span className="absolute top-1/2 left-1/2 transform -translate-x-1/2 -translate-y-1/2 text-white text-sm font-medium capitalize pointer-events-none">
          {app.name.charAt(0)}
        </span>
      )}
    </div>
  );
}
