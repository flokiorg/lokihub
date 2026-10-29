import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { useListings } from "src/hooks/useListings";
import { ListOfferDialog } from "src/screens/subwallets/ListOfferDialog";

// CircleListingPanel is the circle hub page's listing panel (WP-19 items 12 and
// 13): "List this offer", or the listing's health and its own public
// connection, separate from the members' invite.
export function CircleListingPanel({
  appId,
  policy,
}: {
  appId: number;
  policy?: string;
}) {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const [open, setOpen] = React.useState(false);
  const listing = listings?.find(
    (l) => l.hubAppId === appId && l.phase !== "deleted"
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("page.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {listing ? (
          <>
            <ListingHealth listing={listing} />
            {listing.connection && listing.connAppId > 0 && (
              <div className="text-xs">
                <p className="font-medium">{t("connection.label")}</p>
                <p className="break-all font-mono">{listing.connection}</p>
              </div>
            )}
            <Button asChild size="sm" variant="outline">
              <Link to="/listings">{t("actions.edit")}</Link>
            </Button>
          </>
        ) : (
          <Button size="sm" onClick={() => setOpen(true)}>
            {t("dialog.title")}
          </Button>
        )}
      </CardContent>
      {open && (
        <ListOfferDialog
          open
          onOpenChange={setOpen}
          circleHubAppId={appId}
          circlePolicy={policy}
          onDone={() => mutate()}
        />
      )}
    </Card>
  );
}
