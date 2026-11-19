import { useTranslation } from "react-i18next";
import { type Listing } from "src/hooks/useListings";
import { cn } from "src/lib/utils";

// One glanceable word for a listing's state. Health detail (relay counts,
// retrying, operator) is carried by ListingHealth beside it.
export function StatusPill({ listing }: { listing: Listing }) {
  const { t } = useTranslation("listings");
  const base = "flex-none rounded-full px-2.5 py-1 text-xs font-medium";
  if (listing.phase === "closing") {
    return (
      <span className={cn(base, "bg-muted text-muted-foreground")}>
        {t("status.closing", "Closing")}
      </span>
    );
  }
  if (listing.status === "paused") {
    return (
      <span
        className={cn(
          base,
          "border border-warning-foreground/30 bg-warning-foreground/10 text-warning-foreground"
        )}
      >
        {t("status.paused")}
      </span>
    );
  }
  return (
    <span
      className={cn(
        base,
        "border border-positive-foreground/30 bg-positive-foreground/10 text-positive-foreground"
      )}
    >
      {t("status.listed")}
    </span>
  );
}
