
import React from "react";
import AppHeader from "src/components/AppHeader";
import { BlockHeightWidget } from "src/components/home/widgets/BlockHeightWidget";
import { ForwardsWidget } from "src/components/home/widgets/ForwardsWidget";
import { LatestUsedAppsWidget } from "src/components/home/widgets/LatestUsedAppsWidget";
import { LightningMessageboardWidget } from "src/components/home/widgets/LightningMessageboardWidget";
import { NodeStatusWidget } from "src/components/home/widgets/NodeStatusWidget";
import { OnchainFeesWidget } from "src/components/home/widgets/OnchainFeesWidget";
import Loading from "src/components/Loading";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import OnboardingChecklist from "src/screens/wallet/OnboardingChecklist";

import AppAlert from "src/components/home/alerts/AppAlert";
import { SearchInput } from "src/components/ui/search-input";
import { localStorageKeys } from "src/constants";
import { useAppStore } from "src/hooks/useAppStore";

function getGreeting(name: string | undefined) {
  const hours = new Date().getHours();
  let greeting;

  if (hours < 11) {
    greeting = "Good Morning";
  } else if (hours < 16) {
    greeting = "Good Afternoon";
  } else {
    greeting = "Good Evening";
  }

  return `${greeting}${name ? `, ${name}` : ""}!`;
}

function DashboardAlerts() {
  const { apps, loading } = useAppStore();
  const [dismissed, setDismissed] = React.useState<string[]>(() => {
    try {
      const stored = localStorage.getItem(localStorageKeys.appAlertsHiddenUntil);
      return stored ? JSON.parse(stored) : [];
    } catch {
      return [];
    }
  });

  const handleDismiss = (appId: string) => {
    const newDismissed = [...dismissed, appId];
    setDismissed(newDismissed);
    localStorage.setItem(localStorageKeys.appAlertsHiddenUntil, JSON.stringify(newDismissed));
  };

  if (loading) return null;

  const now = Date.now() / 1000; // API returns seconds
  const dayInSeconds = 24 * 60 * 60;
  const fiveDaysAgo = now - 5 * dayInSeconds;

  // Ensure apps is an array to prevent filter errors
  const safeApps = Array.isArray(apps) ? apps : [];

  const newApps = safeApps
    .filter((app) => app.createdAt > fiveDaysAgo && !dismissed.includes(app.id))
    .sort((a, b) => b.createdAt - a.createdAt);

  const updatedApps = safeApps
    .filter(
      (app) =>
        app.updatedAt > fiveDaysAgo &&
        app.createdAt <= fiveDaysAgo && // Don't show as updated if it's new
        !dismissed.includes(app.id)
    )
    .sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <>
      {newApps.map((app) => (
        <AppAlert key={app.id} app={app} type="new" onDismiss={handleDismiss} />
      ))}
      {updatedApps.map((app) => (
        <AppAlert key={app.id} app={app} type="updated" onDismiss={handleDismiss} />
      ))}
    </>
  );
}

function Home() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();

  const [isNerd, setNerd] = React.useState(false);
  if (!info || !balances) {
    return <Loading />;
  }

  return (
    <>
      <AppHeader
        title={getGreeting(undefined)}
        contentRight={<SearchInput placeholder="Search" />}
      />
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-5 items-start justify-start">
        {/* LEFT */}
        {/* LEFT */}
        <div className="grid gap-5">
          <OnboardingChecklist />
          <DashboardAlerts />
          <LightningMessageboardWidget />
        </div>

        {/* RIGHT */}
        <div className="grid gap-5">
          <LatestUsedAppsWidget />

          <Card>
            <CardHeader>
              <div className="flex justify-between items-center">
                <CardTitle>Stats for nerds</CardTitle>
                <Button variant="secondary" onClick={() => setNerd(!isNerd)}>
                  {isNerd ? "Hide" : "Show"}
                </Button>
              </div>
            </CardHeader>
            {isNerd && (
              <CardContent>
                <div className="grid gap-5">
                  <NodeStatusWidget />
                  <BlockHeightWidget />
                  <OnchainFeesWidget />
                  <ForwardsWidget />
                </div>
              </CardContent>
            )}
          </Card>
        </div>
      </div>
    </>
  );
}

export default Home;
