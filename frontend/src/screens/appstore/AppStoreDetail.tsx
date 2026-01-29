import React from "react";
import { useNavigate, useParams } from "react-router-dom";
import { AboutAppCard } from "src/components/connections/AboutAppCard";
import { AppLinksCard } from "src/components/connections/AppLinksCard";
import { AppStoreDetailHeader } from "src/components/connections/AppStoreDetailHeader";
import {
    AppStoreApp
} from "src/components/connections/SuggestedAppData";
import Loading from "src/components/Loading";
import { useAppsForAppStoreApp } from "src/hooks/useApps";
import { useAppStore } from "src/hooks/useAppStore";

export function AppStoreDetail() {
  const { appStoreId } = useParams() as { appStoreId: string };
  const { apps, loading } = useAppStore();
  const navigate = useNavigate();

  if (loading) {
    return <Loading />;
  }

  const appStoreApp = apps.find((x) => x.id === appStoreId);

  if (!appStoreApp) {
    return <div className="p-8 text-center">App not found</div>;
  }

  // Redirect internal apps to their dedicated pages
  if (appStoreApp.internal) {
    navigate(`/internal-apps/${appStoreId}`);
    return null;
  }

  return <AppStoreDetailInternal appStoreApp={appStoreApp} />;
}

function AppStoreDetailInternal({ appStoreApp }: { appStoreApp: AppStoreApp }) {
  const connectedApps = useAppsForAppStoreApp(appStoreApp);
  const navigate = useNavigate();

  React.useEffect(() => {
    if (connectedApps && connectedApps.length > 0) {
      navigate(`/apps/${connectedApps[0].id}`, {
        replace: true,
      });
    }
  }, [connectedApps, navigate]);

  if (!connectedApps) {
    return <Loading />;
  }

  return (
    <div className="grid gap-2">
      <AppStoreDetailHeader appStoreApp={appStoreApp} />
      <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
        <AboutAppCard appStoreApp={appStoreApp} />
        <AppLinksCard appStoreApp={appStoreApp} />
        {/* <Card>
          <CardHeader>
            <CardTitle className="text-2xl">How to Connect</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {appStoreApp.guide || (
              <ul className="list-inside list-decimal">
                <li>Install the app</li>
                <li>
                  Click{" "}
                  <Link to={`/apps/new?app=${appStoreId}`}>
                    <Button variant="link" className="px-0">
                      Connect to {appStoreApp.title}
                    </Button>
                  </Link>
                </li>
                <li>
                  Open the Loki Go app on your mobile and scan the QR code
                </li>
              </ul>
            )}
          </CardContent>
        </Card> */}
      </div>
    </div>
  );
}
