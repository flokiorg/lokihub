import {
  ChevronDownIcon,
  CopyIcon,
  EllipsisIcon,
  PencilIcon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import { StatusPill } from "src/components/listings/ListingActionsMenu";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "src/components/ui/dropdown-menu";
import {
  type Listing,
  type ListingAction,
  listingAction,
  useListings,
} from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";

function FieldRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-1.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="min-w-0 text-right text-sm font-medium">{children}</span>
    </div>
  );
}

// The circle's whole public-listing surface, right on the circle's page: its
// state and health, every lifecycle action (list / pause / resume / unlist /
// reopen), and the full read-only detail — description, place, the public
// connection and address — all inline. A listing belongs to its circle, so you
// never leave the circle to see or manage it (the heavy create/edit form, with
// its map, is the only thing that opens on its own page).
export function CircleListingPanel({ appId }: { appId: number }) {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const [showDetails, setShowDetails] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const listing = listings?.find(
    (l) => l.hubAppId === appId && l.phase !== "deleted"
  );

  const act = async (listingArg: Listing, action: ListingAction) => {
    setBusy(true);
    try {
      await listingAction(listingArg.id, action);
      await mutate();
    } catch (error) {
      handleRequestError(t("errors.action"), error);
    } finally {
      setBusy(false);
    }
  };
  const copy = (text: string) =>
    navigator.clipboard.writeText(text).then(() => toast(t("address.copied")));

  const isClosing = listing?.phase === "closing";
  const isListed = listing?.phase === "listed";
  const isPaused = isListed && listing.status === "paused";
  const isOpen = isListed && listing.status !== "paused";
  const languages = listing?.languages ?? [];
  const regions = listing?.geohashes ?? [];

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle className="text-base">
            {t("page.discoverability")}
          </CardTitle>
          {listing && <StatusPill listing={listing} />}
        </div>
      </CardHeader>
      <CardContent className="grid gap-3">
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
        ) : (
          <>
            <ListingHealth listing={listing} />
            {isPaused && (
              <p className="text-sm text-muted-foreground">
                {t("page.pausedHint")}
              </p>
            )}
            {isClosing && (
              <p className="text-sm text-muted-foreground">
                {t("page.closingHint")}
              </p>
            )}

            {/* Every lifecycle action inline — the state-appropriate primary one
                as a button, the rest tucked in the "…" menu. Reopen (closing)
                and Resume (paused) are the two in-place "make it discoverable
                again" actions. */}
            <div className="flex flex-wrap items-center gap-2">
              {isClosing && (
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => act(listing, "reopen")}
                >
                  {t("actions.reopen")}
                </Button>
              )}
              {isPaused && (
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => act(listing, "resume")}
                >
                  {t("actions.resume")}
                </Button>
              )}
              {isOpen && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => act(listing, "pause")}
                >
                  {t("actions.pause")}
                </Button>
              )}
              {isListed && (
                <Button asChild size="sm" variant="outline">
                  <Link to={`/listings/edit/${listing.id}`}>
                    <PencilIcon className="size-4" />
                    {t("actions.edit")}
                  </Link>
                </Button>
              )}
              <DropdownMenu modal={false}>
                <Button variant="ghost" size="icon" asChild>
                  <DropdownMenuTrigger aria-label={t("actions.more")}>
                    <EllipsisIcon className="size-4" />
                  </DropdownMenuTrigger>
                </Button>
                <DropdownMenuContent align="end">
                  {listing.address && (
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => copy(JSON.stringify(listing.aTag))}
                      >
                        <CopyIcon className="size-4" /> {t("address.copyTag")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {listing.operatorPubkey && (
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => copy(listing.operatorCommand)}
                      >
                        <CopyIcon className="size-4" />{" "}
                        {t("address.copyCommand")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {listing.operatorPubkey && isListed && (
                    <DropdownMenuItem asChild>
                      <div onClick={() => act(listing, "check-operator")}>
                        {t("actions.checkOperator")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {listing.connAppId > 0 && (
                    <DropdownMenuItem asChild>
                      <div onClick={() => act(listing, "revoke-connection")}>
                        {t("actions.revokeConnection")}
                      </div>
                    </DropdownMenuItem>
                  )}
                  {isListed && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" asChild>
                        <div onClick={() => act(listing, "unlist")}>
                          {t("actions.unlist")}
                        </div>
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>

            {/* Everything the old standalone detail page showed, inline and on
                demand — so "what is this listing about" never means leaving. */}
            <button
              type="button"
              onClick={() => setShowDetails((o) => !o)}
              className="flex w-fit items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
            >
              <ChevronDownIcon
                className={cn(
                  "size-3.5 transition-transform",
                  showDetails && "rotate-180"
                )}
              />
              {t("page.detailsToggle")}
            </button>

            {showDetails && (
              <div className="grid gap-3 rounded-lg border bg-muted/20 p-3">
                {listing.image && (
                  <img
                    src={listing.image}
                    alt=""
                    className="max-h-40 w-full rounded-md border object-cover"
                  />
                )}
                {listing.content ? (
                  <p className="whitespace-pre-wrap text-sm">
                    {listing.content}
                  </p>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {t("detail.noDescription")}
                  </p>
                )}
                {(listing.location ||
                  languages.length > 0 ||
                  regions.length > 0) && (
                  <div className="divide-y border-y">
                    {listing.location && (
                      <FieldRow label={t("form.location")}>
                        {listing.location}
                      </FieldRow>
                    )}
                    {languages.length > 0 && (
                      <FieldRow label={t("form.languages")}>
                        <span className="flex flex-wrap justify-end gap-1">
                          {languages.map((c) => (
                            <span
                              key={c}
                              className="rounded-full bg-primary/15 px-2 py-0.5 text-xs font-medium text-primary"
                            >
                              {c.toUpperCase()}
                            </span>
                          ))}
                        </span>
                      </FieldRow>
                    )}
                    {regions.length > 0 && (
                      <FieldRow label={t("form.regionsLabel")}>
                        <span className="font-mono text-xs">
                          {regions.join(", ")}
                        </span>
                      </FieldRow>
                    )}
                  </div>
                )}
                {listing.connection && listing.connAppId > 0 && (
                  <div className="grid gap-1.5">
                    <p className="text-xs font-medium">
                      {t("connection.label")}
                    </p>
                    <p className="break-all rounded-md bg-muted/60 p-2 font-mono text-xs">
                      {listing.connection}
                    </p>
                    <Button
                      variant="outline"
                      size="sm"
                      className="justify-self-start"
                      onClick={() => copy(listing.connection)}
                    >
                      <CopyIcon className="size-4" />
                      {t("connection.copy")}
                    </Button>
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
