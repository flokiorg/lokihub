import { CopyIcon, PencilIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import {
  ListingActionsMenu,
  StatusPill,
} from "src/components/listings/ListingActionsMenu";
import { ListingHealth } from "src/components/listings/ListingHealth";
import { Button } from "src/components/ui/button";
import { useListings } from "src/hooks/useListings";

function FieldRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-b py-2 last:border-b-0">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span className="min-w-0 text-sm font-medium">{children}</span>
    </div>
  );
}

// ListingDetail is the read-only view of one listing — "enter" a listing to
// see what it's about and manage it, rather than dropping straight into the
// editable form (which risked an accidental re-publish). Everything here is
// the operator's own already-public advert; editing is an explicit action.
export function ListingDetail() {
  const { t } = useTranslation("listings");
  const navigate = useNavigate();
  const { id } = useParams();
  const { data: listings, mutate } = useListings();

  if (!listings) {
    return <Loading />;
  }

  const l = listings.find((x) => x.id === Number(id) && x.phase !== "deleted");

  if (!l) {
    return (
      <div className="grid gap-5">
        <AppHeader
          title={t("page.title")}
          description={t("page.description")}
          contentRight={
            <Button variant="outline" onClick={() => navigate("/listings")}>
              {t("actions.back")}
            </Button>
          }
        />
        <p className="text-sm text-muted-foreground">{t("pick.notFound")}</p>
      </div>
    );
  }

  const copy = (text: string) =>
    navigator.clipboard.writeText(text).then(() => toast(t("address.copied")));
  const languages = l.languages ?? [];
  const regions = l.geohashes ?? [];

  return (
    <div className="grid gap-5">
      <AppHeader
        title={l.title || t("card.untitled")}
        description={t("page.description")}
        contentRight={
          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => navigate("/listings")}>
              {t("actions.back")}
            </Button>
            {l.phase === "listed" && (
              <Button onClick={() => navigate(`/listings/edit/${l.id}`)}>
                <PencilIcon className="size-4" />
                {t("actions.edit")}
              </Button>
            )}
            <ListingActionsMenu
              listing={l}
              onChanged={mutate}
              includeEdit={false}
            />
          </div>
        }
      />

      <div className="flex flex-wrap items-center gap-3">
        <StatusPill listing={l} />
        <ListingHealth listing={l} />
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)] lg:items-start">
        <div className="grid gap-5">
          {l.image && (
            <img
              src={l.image}
              alt=""
              className="max-h-56 w-full rounded-lg border object-cover"
            />
          )}
          {l.content && (
            <section className="grid gap-1.5">
              <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                {t("form.content")}
              </h3>
              <p className="whitespace-pre-wrap text-sm">{l.content}</p>
            </section>
          )}
          {(l.location || languages.length > 0 || regions.length > 0) && (
            <section className="grid gap-1">
              <h3 className="mb-1 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                {t("groups.find")}
              </h3>
              {l.location && (
                <FieldRow label={t("form.location")}>{l.location}</FieldRow>
              )}
              {languages.length > 0 && (
                <FieldRow label={t("form.languages")}>
                  <span className="flex flex-wrap justify-end gap-1.5">
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
            </section>
          )}
        </div>

        <aside className="grid gap-4 lg:sticky lg:top-4">
          {l.connection && l.connAppId > 0 && (
            <section className="grid gap-1.5">
              <h3 className="text-sm font-semibold">{t("connection.label")}</h3>
              <p className="break-all rounded-md bg-muted/50 p-2 font-mono text-xs">
                {l.connection}
              </p>
              <Button
                variant="outline"
                size="sm"
                className="justify-self-start"
                onClick={() => copy(l.connection)}
              >
                <CopyIcon className="size-4" />
                {t("connection.copy", "Copy link")}
              </Button>
            </section>
          )}
          {l.operatorPubkey && (
            <p className="text-xs text-muted-foreground">
              {t("warnings.operator")}
            </p>
          )}
        </aside>
      </div>
    </div>
  );
}
