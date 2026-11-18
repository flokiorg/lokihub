import {
  CopyIcon,
  EllipsisIcon,
  MegaphoneIcon,
  PencilIcon,
  UsersIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "src/components/ui/dropdown-menu";
import { useApps } from "src/hooks/useApps";
import {
  type Listing,
  type ListingAction,
  listingAction,
  useListings,
} from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";

function copy(text: string, done: string) {
  navigator.clipboard.writeText(text).then(() => toast(done));
}

// One glanceable word for a listing's state. Health detail (relay counts,
// retrying, operator) is carried by ListingHealth on the row beside it.
function StatusPill({ listing }: { listing: Listing }) {
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

// Listings is the operator's page for listed offers: each listing's state and
// health, its actions in a "…" menu, and one-tap shortcuts to list a circle
// that isn't listed yet. Creating/editing a listing happens on its own page
// (/listings/new, /listings/edit/:id). Where offers are relayed is a node
// setting under Settings → Services, not a concern of this page. lokihub never
// asks for or holds the operator's key.
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

  const act = async (l: Listing, action: ListingAction) => {
    try {
      await listingAction(l.id, action);
      await mutate();
    } catch (error) {
      handleRequestError(t("errors.action"), error);
    }
  };

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
            <li
              key={l.id}
              data-testid="listing-row"
              className="flex min-w-0 items-center gap-3 border-b px-1 py-3 last:border-b-0"
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
              <DropdownMenu modal={false}>
                <Button variant="ghost" size="icon" asChild>
                  <DropdownMenuTrigger aria-label={t("card.untitled")}>
                    <EllipsisIcon className="size-4" />
                  </DropdownMenuTrigger>
                </Button>
                <DropdownMenuContent align="end">
                  {/* Edit / pause / resume / check / unlist are only valid
                      while actually listed — a 'closing' listing is tearing
                      down, so those are hidden (matching the old inline
                      buttons' phase === "listed" gate). */}
                  {l.phase === "listed" && (
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => navigate(`/listings/edit/${l.id}`)}
                      >
                        <PencilIcon className="size-4" /> {t("actions.edit")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {l.phase === "listed" &&
                    (l.status === "paused" ? (
                      <DropdownMenuItem asChild>
                        <div onClick={() => act(l, "resume")}>
                          {t("actions.resume")}
                        </div>
                      </DropdownMenuItem>
                    ) : (
                      <DropdownMenuItem asChild>
                        <div onClick={() => act(l, "pause")}>
                          {t("actions.pause")}
                        </div>
                      </DropdownMenuItem>
                    ))}
                  {l.address && (
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() =>
                          copy(JSON.stringify(l.aTag), t("address.copied"))
                        }
                      >
                        <CopyIcon className="size-4" /> {t("address.copyTag")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {l.operatorPubkey && (
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() =>
                          copy(l.operatorCommand, t("address.copied"))
                        }
                      >
                        <CopyIcon className="size-4" />{" "}
                        {t("address.copyCommand")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {l.operatorPubkey && l.phase === "listed" && (
                    <DropdownMenuItem asChild>
                      <div onClick={() => act(l, "check-operator")}>
                        {t("actions.checkOperator")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {l.type === "circle_wallet" && l.connAppId > 0 && (
                    <DropdownMenuItem asChild>
                      <div onClick={() => act(l, "revoke-connection")}>
                        {t("actions.revokeConnection")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {l.phase === "listed" && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" asChild>
                        <div onClick={() => act(l, "unlist")}>
                          {t("actions.unlist")}
                        </div>
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
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
