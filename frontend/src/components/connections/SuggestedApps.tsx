import { LayoutGrid } from "lucide-react";
import React from "react";
import { Link } from "react-router";
import EmptyState from "src/components/EmptyState";
import Loading from "src/components/Loading";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardTitle,
} from "src/components/ui/card";
import { useAppLogo } from "src/hooks/useAppLogo";
import { useAppStore } from "src/hooks/useAppStore";
import { useInfo } from "src/hooks/useInfo";
import { cn } from "src/lib/utils";
import { AppStoreApp, sortedAppStoreCategories } from "./SuggestedAppData";

function SuggestedAppCard({ id, title, description, logo }: AppStoreApp) {
  const logoSrc = useAppLogo(id);
  return (
    <Link to={`/appstore/${id}`}>
      <Card className="h-full">
        <CardContent>
          <div className="flex gap-3 items-center">
            {logo && logoSrc ? (
              <img
                src={logoSrc}
                alt="logo"
                className="inline rounded-lg size-12"
              />
            ) : (
              <div className="inline rounded-lg size-12 bg-muted/50" />
            )}
            <div className="grow">
              <CardTitle>{title}</CardTitle>
              <CardDescription>{description}</CardDescription>
            </div>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

function InternalAppCard({ id, title, description, logo }: AppStoreApp) {
  return (
    <Link to={`/internal-apps/${id}`}>
      <Card className="h-full">
        <CardContent>
          <div className="flex gap-3 items-center">
            <img src={logo} alt="logo" className="inline rounded-lg size-12" />
            <div className="grow">
              <CardTitle>{title}</CardTitle>
              <CardDescription>{description}</CardDescription>
            </div>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

export default function SuggestedApps() {
  const { apps: appStoreApps, loading, error } = useAppStore();
  const { data: info } = useInfo();
  const [selectedCategories, setSelectedCategories] = React.useState<string[]>(
    []
  );

  // The app store is OFF until the operator selects a store URL under
  // Settings → Services. With none selected nothing is ever fetched, so show a
  // configure hint (not an error) — same degrade-gracefully shape as the fiat
  // rate falling back to sats/FLC. Wait for info before deciding so we don't
  // flash this state while it loads.
  if (info && !info.lokihubStoreUrl) {
    return (
      <EmptyState
        icon={LayoutGrid}
        title="App store not configured"
        description="Choose an app store under Settings → Services to browse community apps."
        buttonText="Open Settings"
        buttonLink="/settings/services"
      />
    );
  }

  if (loading) {
    return <Loading />;
  }

  if (error) {
    return <div className="text-center text-red-500">Failed to load apps</div>;
  }

  if (appStoreApps.length === 0) {
    return (
      <EmptyState
        icon={LayoutGrid}
        title="No apps yet"
        description="Check back later or submit your own app to the community."
        buttonText="Submit your app"
        buttonLink="https://github.com/flokiorg/lokihub-store"
        externalLink={true}
      />
    );
  }

  return (
    <div dir="ltr">
      <div className="flex gap-2 flex-wrap mt-6 mb-2">
        {sortedAppStoreCategories.map(([categoryId, category]) => {
          // Check if category has any apps
          const hasApps = appStoreApps.some(
            (app) => app.category === categoryId
          );

          if (!hasApps) return null;

          return (
            <Badge
              key={categoryId}
              variant={
                selectedCategories.includes(categoryId)
                  ? "default"
                  : "secondary"
              }
              className={cn(
                "cursor-pointer",
                selectedCategories.includes(categoryId)
                  ? ""
                  : "border-transparent font-normal select-none"
              )}
              onClick={() =>
                setSelectedCategories((current) => [
                  ...current.filter((c) => c !== categoryId),
                  ...(current.includes(categoryId) ? [] : [categoryId]),
                ])
              }
            >
              {category.title}
            </Badge>
          );
        })}
      </div>
      <div className="flex flex-col gap-8">
        {sortedAppStoreCategories
          .filter(
            ([categoryId]) =>
              !selectedCategories.length ||
              selectedCategories.includes(categoryId)
          )
          .map(([categoryId, category]) => {
            const categoryApps = appStoreApps.filter(
              (app) => app.category === categoryId
            );

            if (categoryApps.length === 0) {
              return null;
            }

            return (
              <div key={categoryId} className="pt-4">
                <h3 className="font-semibold text-xl">{category.title}</h3>
                <div className="grid md:grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4 mt-4">
                  {categoryApps.map((app) =>
                    app.internal ? (
                      <InternalAppCard key={app.id} {...app} />
                    ) : (
                      // @ts-ignore
                      <SuggestedAppCard key={app.id} {...app} />
                    )
                  )}
                </div>
              </div>
            );
          })}
      </div>
    </div>
  );
}
