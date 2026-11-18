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
import { listingAction, useListings } from "src/hooks/useListings";
import { handleRequestError } from "src/utils/handleRequestError";

// The circle's public-listing control, shown on the circle's own page: whether
// this circle is discoverable, and list / resume / unlist / manage it right
// here — no trip to a separate Listings screen. (Creating a new listing still
// opens the full List-offer form, since it has a map + display fields.)
export function CircleListingPanel({ appId }: { appId: number }) {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const listing = listings?.find(
    (l) => l.hubAppId === appId && l.phase !== "deleted"
  );

  // A paused listing is reopened in place (Resume). Unlisting, by contrast, is
  // a one-way graceful retirement: it enters "closing", keeps serving whoever
  // already holds the link until the last version expires, and only then can
  // the circle be listed afresh — so "closing" offers no reopen, just a plain
  // explanation of what's happening.
  const isClosing = listing?.phase === "closing";
  const isPaused = listing?.phase === "listed" && listing.status === "paused";

  const resume = async () => {
    if (!listing) {
      return;
    }
    try {
      await listingAction(listing.id, "resume");
      await mutate();
    } catch (error) {
      handleRequestError(t("errors.action"), error);
    }
  };

  const manageButton = listing && (
    <Button asChild size="sm" variant="outline">
      <Link to={`/listings/${listing.id}`}>{t("actions.manage")}</Link>
    </Button>
  );

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("page.discoverability")}</CardTitle>
      </CardHeader>
      <CardContent>
        {!listing ? (
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
        ) : isClosing ? (
          // Being retired — no lifecycle actions apply, so just explain it and
          // link to the read-only detail rather than show a dead "…" menu.
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <StatusPill listing={listing} />
              <ListingHealth listing={listing} />
            </div>
            <p className="text-sm text-muted-foreground">
              {t("page.closingHint")}
            </p>
            <div>{manageButton}</div>
          </div>
        ) : (
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                <StatusPill listing={listing} />
                <ListingHealth listing={listing} />
              </div>
              {/* Resume (for a paused listing) is the one true "reopen"; Manage
                  opens the read-only detail; the "…" menu does the rest of the
                  lifecycle (pause / unlist / copy) inline. */}
              <div className="flex shrink-0 items-center gap-2">
                {isPaused && (
                  <Button size="sm" onClick={resume}>
                    {t("actions.resume")}
                  </Button>
                )}
                {manageButton}
                <ListingActionsMenu
                  listing={listing}
                  onChanged={mutate}
                  includeEdit={false}
                />
              </div>
            </div>
            {isPaused && (
              <p className="text-sm text-muted-foreground">
                {t("page.pausedHint")}
              </p>
            )}
            {listing.connection && listing.connAppId > 0 && (
              <div className="text-xs">
                <p className="font-medium">{t("connection.label")}</p>
                <p className="break-all font-mono">{listing.connection}</p>
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
