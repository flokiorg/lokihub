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

// CircleListingPanel is the circle hub page's listing panel (WP-19 items 12 and
// 13): "List this offer", or the listing's health and its own public
// connection, separate from the members' invite. Creating/editing happens on
// the dedicated List-offer page.
export function CircleListingPanel({ appId }: { appId: number }) {
  const { t } = useTranslation("listings");
  const { data: listings } = useListings();
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
              <Link to={`/listings/edit/${listing.id}`}>
                {t("actions.edit")}
              </Link>
            </Button>
          </>
        ) : (
          <Button asChild size="sm">
            <Link to={`/listings/new?circle=${appId}`}>
              {t("dialog.title")}
            </Link>
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
