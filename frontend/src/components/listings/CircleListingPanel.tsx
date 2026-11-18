import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
  ListingActionsMenu,
  StatusPill,
} from "src/components/listings/ListingActionsMenu";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { useListings } from "src/hooks/useListings";

// The circle's public-listing control, shown on the circle's own page: whether
// this circle is discoverable, and list / unlist / pause / manage it right
// here — no trip to a separate Listings screen. (Creating a new listing still
// opens the full List-offer form, since it has a map + display fields.)
export function CircleListingPanel({ appId }: { appId: number }) {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const listing = listings?.find(
    (l) => l.hubAppId === appId && l.phase !== "deleted"
  );
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("page.discoverability")}</CardTitle>
      </CardHeader>
      <CardContent>
        {listing ? (
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                <StatusPill listing={listing} />
                <ListingHealth listing={listing} />
              </div>
              {/* Manage opens the read-only detail; the "…" menu does the
                  inline lifecycle (pause / resume / unlist / copy) so you
                  never have to leave the circle to unlist it. */}
              <div className="flex shrink-0 items-center gap-2">
                <Button asChild size="sm" variant="outline">
                  <Link to={`/listings/${listing.id}`}>
                    {t("actions.manage")}
                  </Link>
                </Button>
                <ListingActionsMenu
                  listing={listing}
                  onChanged={mutate}
                  includeEdit={false}
                />
              </div>
            </div>
            {listing.connection && listing.connAppId > 0 && (
              <div className="text-xs">
                <p className="font-medium">{t("connection.label")}</p>
                <p className="break-all font-mono">{listing.connection}</p>
              </div>
            )}
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              {t("page.notListed")}
            </p>
            <Button asChild size="sm" className="shrink-0">
              <Link to={`/listings/new?circle=${appId}`}>
                {t("page.listAction")}
              </Link>
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
