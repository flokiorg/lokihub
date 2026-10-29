import { CopyIcon, MegaphoneIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import { ListingCard } from "src/components/listings/ListingCard";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { useApps } from "src/hooks/useApps";
import {
  type Listing,
  type ListingAction,
  listingAction,
  setListingRelays,
  useListingRelays,
  useListings,
} from "src/hooks/useListings";
import { ListOfferDialog } from "src/screens/subwallets/ListOfferDialog";
import { handleRequestError } from "src/utils/handleRequestError";

function copy(text: string, done: string) {
  navigator.clipboard.writeText(text).then(() => toast(done));
}

// Listings is the operator's page for listed offers (WP-25, WP-19 item 13):
// each listing's health, Pause/Resume, Edit, Unlist, the listing address to
// give the operator, and the listing relays. lokihub never asks for or holds
// the operator's key.
export function Listings() {
  const { t } = useTranslation("listings");
  const { data: listings, mutate } = useListings();
  const { data: circles } = useApps(100, 1, { kind: "circle_hub" });
  const [editing, setEditing] = React.useState<Listing | undefined>();
  const [listingCircle, setListingCircle] = React.useState<
    { id: number; policy?: string } | undefined
  >();

  if (!listings) {
    return <Loading />;
  }
  const listed = new Set(
    listings.filter((l) => l.phase !== "deleted").map((l) => l.hubAppId)
  );
  const unlistedCircles = (circles?.apps ?? []).filter(
    (a) => !listed.has(a.id)
  );

  const act = async (l: Listing, action: ListingAction) => {
    try {
      await listingAction(l.id, action);
      await mutate();
    } catch (error) {
      handleRequestError(t("errors.action"), error);
    }
  };

  return (
    <div className="space-y-4">
      <AppHeader title={t("page.title")} description={t("page.description")} />
      {listings.length === 0 && (
        <Card className="flex flex-col items-center gap-2 p-8 text-center">
          <MegaphoneIcon className="size-10 text-muted-foreground" />
          <CardTitle className="text-lg">{t("page.empty")}</CardTitle>
          <CardDescription>{t("page.emptyHint")}</CardDescription>
        </Card>
      )}
      {listings.map((l) => (
        <Card key={l.id} data-testid="listing-row">
          <CardHeader>
            <CardTitle className="text-base">
              {l.title || t("card.untitled")}
            </CardTitle>
            <ListingHealth listing={l} />
          </CardHeader>
          <CardContent className="space-y-3">
            <ListingCard
              type={l.type}
              title={l.title}
              content={l.content}
              location={l.location}
              languages={l.languages ?? []}
              geohashes={l.geohashes ?? []}
              status={l.status}
            />
            {l.address && (
              <div className="space-y-1 text-xs">
                <p className="font-medium">{t("address.label")}</p>
                <p className="break-all font-mono">{l.address}</p>
                <div className="flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      copy(JSON.stringify(l.aTag), t("address.copied"))
                    }
                  >
                    <CopyIcon className="size-3" /> {t("address.copyTag")}
                  </Button>
                  {l.operatorPubkey && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() =>
                        copy(l.operatorCommand, t("address.copied"))
                      }
                    >
                      <CopyIcon className="size-3" /> {t("address.copyCommand")}
                    </Button>
                  )}
                </div>
                <p className="text-muted-foreground">{t("address.hint")}</p>
              </div>
            )}
            {l.type === "circle_wallet" && l.connection && l.connAppId > 0 && (
              <div className="space-y-1 text-xs">
                <p className="font-medium">{t("connection.label")}</p>
                <p className="break-all font-mono">{l.connection}</p>
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              {l.phase === "listed" && (
                <>
                  {l.status === "paused" ? (
                    <Button size="sm" onClick={() => act(l, "resume")}>
                      {t("actions.resume")}
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => act(l, "pause")}
                    >
                      {t("actions.pause")}
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setEditing(l)}
                  >
                    {t("actions.edit")}
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => act(l, "unlist")}
                  >
                    {t("actions.unlist")}
                  </Button>
                </>
              )}
              {l.operatorPubkey && l.phase === "listed" && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => act(l, "check-operator")}
                >
                  {t("actions.checkOperator")}
                </Button>
              )}
              {l.type === "circle_wallet" && l.connAppId > 0 && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => act(l, "revoke-connection")}
                >
                  {t("actions.revokeConnection")}
                </Button>
              )}
            </div>
          </CardContent>
        </Card>
      ))}
      {unlistedCircles.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("page.listCircle")}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2">
            {unlistedCircles.map((c) => (
              <Button
                key={c.id}
                variant="outline"
                size="sm"
                onClick={() =>
                  setListingCircle({
                    id: c.id,
                    policy: c.circleIdentity?.policy,
                  })
                }
              >
                {c.name}
              </Button>
            ))}
          </CardContent>
        </Card>
      )}
      <ListingRelaysCard />
      {editing && (
        <ListOfferDialog
          open
          onOpenChange={(o) => !o && setEditing(undefined)}
          listing={editing}
          onDone={() => mutate()}
        />
      )}
      {listingCircle && (
        <ListOfferDialog
          open
          onOpenChange={(o) => !o && setListingCircle(undefined)}
          circleHubAppId={listingCircle.id}
          circlePolicy={listingCircle.policy}
          onDone={() => mutate()}
        />
      )}
    </div>
  );
}

// ListingRelaysCard edits the listing relays (OQ-44): where listings go and
// what the node's relay list names as write relays.
function ListingRelaysCard() {
  const { t } = useTranslation("listings");
  const { data, mutate } = useListingRelays();
  const [value, setValue] = React.useState<string | undefined>();
  if (!data) {
    return null;
  }
  const current = value ?? data.configured.join(", ");
  const save = async () => {
    try {
      await setListingRelays(
        current
          .split(/[\s,]+/)
          .map((r) => r.trim())
          .filter((r) => r)
      );
      await mutate();
      setValue(undefined);
      toast(t("relays.saved"));
    } catch (error) {
      handleRequestError(t("errors.relays"), error);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("relays.title")}</CardTitle>
        <CardDescription>
          {t("relays.description", { relays: data.effective.join(", ") })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex gap-2">
        <Input
          aria-label={t("relays.title")}
          placeholder="wss://"
          value={current}
          onChange={(e) => setValue(e.target.value)}
        />
        <Button onClick={save}>{t("relays.save")}</Button>
      </CardContent>
    </Card>
  );
}
