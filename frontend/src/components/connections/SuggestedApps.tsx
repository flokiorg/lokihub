import { LayoutGrid } from "lucide-react";
import React from "react";
import { Link } from "react-router-dom";
import EmptyState from "src/components/EmptyState";
import Loading from "src/components/Loading";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardTitle,
} from "src/components/ui/card";
import { useAppStore } from "src/hooks/useAppStore";
import { cn } from "src/lib/utils";
import { swrFetcher } from "src/utils/swr";
import useSWR from "swr";
import {
  AppStoreApp,
  sortedAppStoreCategories
} from "./SuggestedAppData";

function SuggestedAppCard({ id, title, description }: AppStoreApp) {
  const { data: logoBase64 } = useSWR(`/api/appstore/logos/${id}`, swrFetcher);
  const image = logoBase64 ? `data:image/png;base64,${logoBase64}` : null;

  return (
    <Link to={`/appstore/${id}`}>
      <Card className="h-full">
        <CardContent>
          <div className="flex gap-3 items-center">
            {image ? (
                <img src={image} alt="logo" className="inline rounded-lg size-12" />
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
  const [selectedCategories, setSelectedCategories] = React.useState<string[]>(
    []
  );

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
    <>
      <div className="flex gap-2 flex-wrap mt-6 mb-2">
        {sortedAppStoreCategories.map(([categoryId, category]) => {
          // Check if category has any apps
          const hasApps = appStoreApps.some((app) =>
            (app.categories as string[]).includes(categoryId)
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
            const categoryApps = appStoreApps.filter((app) =>
              (app.categories as string[]).includes(categoryId)
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
                    ) : ( // @ts-ignore
                      <SuggestedAppCard key={app.id} {...app} logo={`/api/appstore/logos/${app.id}`} />
                    )
                  )}
                </div>
              </div>
            );
          })}
      </div>
    </>
  );
}
