import { useNavigate, useParams } from "react-router-dom";
import AppHeader from "src/components/AppHeader";
import { AboutAppCard } from "src/components/connections/AboutAppCard";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import Loading from "src/components/Loading";
import { Button } from "src/components/ui/button";
import { useAppsForAppStoreApp } from "src/hooks/useApps";
import { useAppStore } from "src/hooks/useAppStore";

export function InternalAppDetail() {
  const { id } = useParams() as { id: string };
  const { apps, loading } = useAppStore();

  if (loading) {
    return <Loading />;
  }

  const appStoreApp = apps.find((x) => x.id === id);

  if (!appStoreApp) {
    return (
      <div className="p-8 text-center text-muted-foreground">
        App not found
      </div>
    );
  }

  return <InternalAppDetailContent appStoreApp={appStoreApp} />;
}

function InternalAppDetailContent({ appStoreApp }: { appStoreApp: AppStoreApp }) {
  const navigate = useNavigate();
  const connectedApps = useAppsForAppStoreApp(appStoreApp);

  if (!connectedApps) {
    return <Loading />;
  }

  const connectedApp = connectedApps[0];

  return (
    <div className="grid gap-5">
      <AppHeader title={appStoreApp.title} description={appStoreApp.description} />
      <div className="grid gap-4 max-w-lg">
        <AboutAppCard appStoreApp={appStoreApp} />
        {connectedApp ? (
          <Button onClick={() => navigate(`/apps/${connectedApp.id}`)}>
            Manage Connection
          </Button>
        ) : (
          <Button onClick={() => navigate(`/apps/new?app=${appStoreApp.id}`)}>
            Set Up
          </Button>
        )}
      </div>
    </div>
  );
}
