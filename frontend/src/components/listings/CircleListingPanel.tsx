import {
  ChevronDownIcon,
  CopyIcon,
  EllipsisIcon,
  MegaphoneIcon,
  PencilIcon,
  RadioTowerIcon,
  RefreshCwIcon,
  Settings2Icon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import { ListingCard } from "src/components/listings/ListingCard";
import { StatusPill } from "src/components/listings/StatusPill";
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
  useListingRelays,
  useListings,
} from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";

// A relay URL as a short human label, and a normalised key for set membership
// (the backend may store a trailing slash the config does not, or vice versa).
const relayName = (url: string) =>
  url.replace(/^wss?:\/\//, "").replace(/\/$/, "");
const relayKey = (url: string) => url.trim().replace(/\/$/, "").toLowerCase();

function Dot({ ok }: { ok: boolean }) {
  return (
    <span
      className={cn(
        "size-2 shrink-0 rounded-full",
        ok ? "bg-positive-foreground" : "bg-warning-foreground"
      )}
    />
  );
}

// The circle's whole public-listing surface, on the circle's own page: its
// identity and state, every lifecycle action, where on the relay network it is
// actually published (and a one-tap republish to newly-added relays), and the
// full read-only detail — all inline. A listing belongs to its circle, so you
// never leave the circle to see or manage it; only the create/edit form opens
// on its own page.
export function CircleListingPanel({ appId }: { appId: number }) {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const { data: relayCfg } = useListingRelays();
  const [showDetails, setShowDetails] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const listing = listings?.find(
    (l) => l.hubAppId === appId && l.phase !== "deleted"
  );

  const act = async (
    l: Listing,
    action: ListingAction,
    successMsg?: string
  ) => {
    setBusy(true);
    try {
      await listingAction(l.id, action);
      await mutate();
      if (successMsg) {
        toast(successMsg);
      }
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

  // Relay coverage: which configured listing relays are carrying the live
  // version, and which were added later and have not got it yet.
  const effective = relayCfg?.effective ?? [];
  const covered = new Set((listing?.openRelays ?? []).map(relayKey));
  const relayRows = effective.map((url) => ({
    url,
    ok: covered.has(relayKey(url)),
  }));
  const pending = relayRows.filter((r) => !r.ok);

  const stateLine = isClosing
    ? t("state.closing")
    : isPaused
      ? t("state.paused")
      : t("state.discoverable");

  const refreshedDays =
    listing && listing.lastCreatedAt > 0
      ? Math.max(
          0,
          Math.floor(Date.now() / 1000 / 86400 - listing.lastCreatedAt / 86400)
        )
      : null;

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

      <CardContent className="grid gap-4">
        {!listing ? (
          <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-start gap-3">
              <div className="grid size-9 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
                <MegaphoneIcon className="size-5" />
              </div>
              <p className="text-sm text-muted-foreground">
                {t("page.notListed")}
              </p>
            </div>
            <Button asChild size="sm" className="shrink-0">
              <Link to={`/apps/${appId}/list-offer`}>
                {t("page.listAction")}
              </Link>
            </Button>
          </div>
        ) : (
          <>
            {/* Identity + plain-language state. */}
            <div className="flex items-start gap-3">
              {listing.image ? (
                <img
                  src={listing.image}
                  alt=""
                  className="size-11 shrink-0 rounded-lg border object-cover"
                />
              ) : (
                <div className="grid size-11 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
                  <MegaphoneIcon className="size-5" />
                </div>
              )}
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">
                  {listing.title || t("card.untitled")}
                </p>
                <p
                  className={cn(
                    "text-sm",
                    listing.failing
                      ? "text-destructive"
                      : "text-muted-foreground"
                  )}
                >
                  {stateLine}
                </p>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                  {refreshedDays !== null && (
                    <span>
                      {t("health.refreshed", { count: refreshedDays })}
                    </span>
                  )}
                  {listing.operatorPubkey && (
                    <span>
                      {listing.operatorVerified
                        ? t("health.operatorVerified")
                        : t("health.operatorNotNamed")}
                    </span>
                  )}
                  {listing.failing && (
                    <span className="text-destructive">
                      {t("health.failing")}
                    </span>
                  )}
                </div>
              </div>
            </div>

            {(isPaused || isClosing) && (
              <p className="text-sm text-muted-foreground">
                {isClosing ? t("page.closingHint") : t("page.pausedHint")}
              </p>
            )}

            {/* Lifecycle actions. */}
            <div className="flex flex-wrap items-center gap-2">
              {isClosing && (
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => act(listing, "reopen", t("toast.reopened"))}
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
                  <Link to={`/apps/${appId}/list-offer`}>
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

            {/* Where it's published — relay coverage + republish. A listing is
                only live on the relays that accepted it; a relay added later in
                Settings carries it only after a refresh (or this republish). */}
            {isListed && (
              <div className="grid gap-2 border-t pt-4">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <RadioTowerIcon className="size-4 text-muted-foreground" />
                    {t("relays.where")}
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {t("relays.summary", {
                      ok: relayRows.length - pending.length,
                      total: relayRows.length,
                    })}
                  </span>
                </div>

                {relayRows.length === 0 ? (
                  <p className="text-xs text-muted-foreground">
                    {t("relays.none")}
                  </p>
                ) : (
                  <ul className="grid gap-1">
                    {relayRows.map((r) => (
                      <li
                        key={r.url}
                        className="flex items-center gap-2 text-sm"
                      >
                        <Dot ok={r.ok} />
                        <span className="min-w-0 flex-1 truncate font-mono text-xs">
                          {relayName(r.url)}
                        </span>
                        <span
                          className={cn(
                            "shrink-0 text-xs",
                            r.ok
                              ? "text-muted-foreground"
                              : "font-medium text-warning-foreground"
                          )}
                        >
                          {r.ok ? t("relays.published") : t("relays.pending")}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}

                {pending.length > 0 ? (
                  <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-warning-foreground/30 bg-warning-foreground/10 p-2.5">
                    <p className="min-w-0 text-xs text-warning-foreground">
                      {t("relays.pendingCallout", { count: pending.length })}
                    </p>
                    <Button
                      size="sm"
                      disabled={busy}
                      onClick={() =>
                        act(listing, "republish", t("relays.republished"))
                      }
                    >
                      <RefreshCwIcon className="size-4" />
                      {t("relays.publishNew", { count: pending.length })}
                    </Button>
                  </div>
                ) : (
                  <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-auto px-0 text-xs text-muted-foreground hover:bg-transparent hover:text-foreground"
                      disabled={busy}
                      onClick={() =>
                        act(listing, "republish", t("relays.republished"))
                      }
                    >
                      <RefreshCwIcon className="size-3.5" />
                      {t("relays.republish")}
                    </Button>
                    <Link
                      to="/settings/services#listing-relays"
                      className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                    >
                      <Settings2Icon className="size-3.5" />
                      {t("relays.manage")}
                    </Link>
                  </div>
                )}
              </div>
            )}

            {/* Details on demand — never a reason to leave the page. */}
            <div className="border-t pt-3">
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
                <div className="mt-3 grid gap-4 lg:grid-cols-[1.4fr_1fr]">
                  <div className="grid gap-3">
                    {listing.content ? (
                      <p className="whitespace-pre-wrap text-sm">
                        {listing.content}
                      </p>
                    ) : (
                      <p className="text-sm text-muted-foreground">
                        {t("detail.noDescription")}
                      </p>
                    )}
                    <dl className="grid gap-2 text-sm">
                      {listing.location && (
                        <div className="flex items-baseline justify-between gap-4">
                          <dt className="text-xs text-muted-foreground">
                            {t("detail.place")}
                          </dt>
                          <dd className="min-w-0 text-right font-medium">
                            {listing.location}
                          </dd>
                        </div>
                      )}
                      {(listing.languages?.length ?? 0) > 0 && (
                        <div className="flex items-baseline justify-between gap-4">
                          <dt className="text-xs text-muted-foreground">
                            {t("form.languages")}
                          </dt>
                          <dd className="flex flex-wrap justify-end gap-1">
                            {listing.languages!.map((c) => (
                              <span
                                key={c}
                                className="rounded-full bg-primary/15 px-2 py-0.5 text-xs font-medium text-primary"
                              >
                                {c.toUpperCase()}
                              </span>
                            ))}
                          </dd>
                        </div>
                      )}
                      {(listing.geohashes?.length ?? 0) > 0 && (
                        <div className="flex items-baseline justify-between gap-4">
                          <dt className="text-xs text-muted-foreground">
                            {t("detail.area")}
                          </dt>
                          <dd className="font-mono text-xs">
                            {listing.geohashes!.join(", ")}
                          </dd>
                        </div>
                      )}
                    </dl>
                    {listing.connection && listing.connAppId > 0 && (
                      <div className="grid gap-1.5">
                        <p className="text-xs font-medium">
                          {t("connection.label")}
                        </p>
                        <p className="line-clamp-2 break-all rounded-md bg-muted/60 p-2 font-mono text-[11px] leading-relaxed">
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

                  {/* How people see it — the public advert. */}
                  <div className="grid gap-1.5">
                    <p className="text-xs uppercase tracking-wide text-muted-foreground">
                      {t("detail.preview")}
                    </p>
                    <ListingCard
                      type={listing.type}
                      title={listing.title}
                      content={listing.content}
                      location={listing.location}
                      languages={listing.languages ?? []}
                      geohashes={listing.geohashes ?? []}
                      status={listing.status}
                    />
                  </div>
                </div>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
