import { MegaphoneIcon, UsersIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import {
  ListingActionsMenu,
  StatusPill,
} from "src/components/listings/ListingActionsMenu";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import { useApps } from "src/hooks/useApps";
import { useListings } from "src/hooks/useListings";

// Listings is the operator's cross-circle view of listed offers: each one's
// state and health, a tap into its detail, its actions in a "…" menu, and
// one-tap shortcuts to list a circle that isn't listed yet. Creating/editing
// happens on its own page; this view is reached from the Home dashboard, not a
// sidebar item (a listing belongs to its circle). Where offers are relayed is
// a node setting under Settings → Services. lokihub never asks for the key.
export function Listings() {
  const { t } = useTranslation("listings");
  const navigate = useNavigate();
  const { data: listings, mutate } = useListings();
  const { data: circles } = useApps(100, 1, { kind: "circle_hub" });

  if (!listings) {
    return <Loading />;
  }

  const active = listings.filter((l) => l.phase !== "deleted");
  const listedHubs = new Set(active.map((l) => l.hubAppId));
  const unlisted = (circles?.apps ?? []).filter((a) => !listedHubs.has(a.id));

  const listCircle = (circleId: number) =>
    navigate(`/listings/new?circle=${circleId}`);

  return (
    <div className="grid gap-4">
      <AppHeader
        title={t("page.title")}
        description={t("page.description")}
        contentRight={
          unlisted.length > 0 && (
            <Button
              onClick={() =>
                unlisted.length === 1
                  ? listCircle(unlisted[0].id)
                  : navigate("/listings/new")
              }
            >
              <MegaphoneIcon className="size-4" />
              {t("page.listAction", "List an offer")}
            </Button>
          )
        }
      />

      {active.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-14 text-center">
          <div className="flex size-14 items-center justify-center rounded-2xl bg-accent text-accent-foreground">
            <MegaphoneIcon className="size-7" />
          </div>
          <h2 className="text-lg font-semibold">{t("page.empty")}</h2>
          <p className="max-w-sm text-sm text-muted-foreground">
            {t("page.emptyHint")}
          </p>
          {unlisted.length > 0 && (
            <div className="mt-1 flex flex-wrap justify-center gap-2">
              {unlisted.slice(0, 4).map((c, i) => (
                <Button
                  key={c.id}
                  variant={i === 0 ? "default" : "outline"}
                  onClick={() => listCircle(c.id)}
                >
                  <UsersIcon className="size-4" />
                  {c.name}
                </Button>
              ))}
            </div>
          )}
        </div>
      ) : (
        <ul className="flex flex-col">
          {active.map((l) => (
            // The row opens the listing's read-only detail; the "…" menu stops
            // the click from bubbling so its own actions still work.
            <li
              key={l.id}
              data-testid="listing-row"
              role="button"
              tabIndex={0}
              aria-label={l.title || t("card.untitled")}
              onClick={() => navigate(`/listings/${l.id}`)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  navigate(`/listings/${l.id}`);
                }
              }}
              className="flex min-w-0 cursor-pointer items-center gap-3 rounded-md border-b px-1 py-3 transition-colors last:border-b-0 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="flex size-9 flex-none items-center justify-center rounded-lg bg-accent text-accent-foreground">
                <MegaphoneIcon className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">
                  {l.title || t("card.untitled")}
                </p>
                <ListingHealth listing={l} />
              </div>
              <StatusPill listing={l} />
              <div onClick={(e) => e.stopPropagation()}>
                <ListingActionsMenu listing={l} onChanged={mutate} />
              </div>
            </li>
          ))}
        </ul>
      )}

      {active.length > 0 && unlisted.length > 0 && (
        <div className="border-t pt-4">
          <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("page.notListed", "Not listed yet — one tap to list")}
          </p>
          <div className="flex flex-wrap gap-2">
            {unlisted.map((c) => (
              <button
                key={c.id}
                type="button"
                onClick={() => listCircle(c.id)}
                className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-sm font-medium transition-colors hover:border-primary/50"
              >
                <UsersIcon className="size-4 text-muted-foreground" />
                {c.name}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
