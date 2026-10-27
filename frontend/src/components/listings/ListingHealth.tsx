import { useTranslation } from "react-i18next";
import type { Listing } from "src/hooks/useListings";

// ListingHealth is the listing's one-line health: listed or not, how long ago
// it was refreshed, on how many relays, and the operator line.
export function ListingHealth({
  listing,
  now = Date.now() / 1000,
}: {
  listing: Listing;
  now?: number;
}) {
  const { t } = useTranslation("listings");
  const parts: string[] = [];
  switch (listing.phase) {
    case "listed":
      parts.push(
        listing.status === "paused" ? t("health.paused") : t("health.listed")
      );
      break;
    case "closing":
      parts.push(t("health.closing"));
      break;
    default:
      parts.push(t("health.deleted"));
  }
  if (listing.lastCreatedAt > 0) {
    const days = Math.max(0, Math.floor((now - listing.lastCreatedAt) / 86400));
    parts.push(t("health.refreshed", { count: days }));
  }
  if (listing.relaysTotal > 0) {
    parts.push(
      t("health.relays", { ok: listing.relaysOk, total: listing.relaysTotal })
    );
  }
  if (listing.operatorPubkey) {
    parts.push(
      listing.operatorVerified
        ? t("health.operatorVerified")
        : t("health.operatorNotNamed")
    );
  }
  return (
    <p
      className={
        listing.failing
          ? "text-destructive text-sm"
          : "text-muted-foreground text-sm"
      }
      data-testid="listing-health"
    >
      {parts.join(" · ")}
      {listing.failing && ` · ${t("health.failing")}`}
    </p>
  );
}
