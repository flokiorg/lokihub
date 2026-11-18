import { XIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import { ListingCard } from "src/components/listings/ListingCard";
import {
  MAX_CONTENT,
  MAX_TITLE,
  graphemes,
  splitList,
  validateListingForm,
} from "src/components/listings/text";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Textarea } from "src/components/ui/textarea";
import { useApps } from "src/hooks/useApps";
import {
  type Listing,
  type ListingFields,
  createListing,
  updateListing,
  useListings,
} from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { App } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { safeDecodeToHex } from "src/utils/nostr";

// A friendly, non-code label for the common ISO-639-1 codes the form stores.
const COMMON_LANGS: [string, string][] = [
  ["en", "English"],
  ["pt", "Português"],
  ["es", "Español"],
  ["fr", "Français"],
  ["de", "Deutsch"],
  ["it", "Italiano"],
  ["nl", "Nederlands"],
  ["zh", "中文"],
];
const langLabel = (code: string) =>
  COMMON_LANGS.find(([c]) => c === code)?.[1] ?? code.toUpperCase();

const problemKeys = {
  title: "problems.title",
  content: "problems.content",
  image: "problems.image",
  location: "problems.location",
  languages: "problems.languages",
  regions: "problems.regions",
  operator: "problems.operator",
  operatorRelay: "problems.operatorRelay",
} as const;

// ListOfferPage lists an offer, or edits a listed one — a full page (not a
// modal) so the fields have room and the preview rides along as you type. The
// listed string is built by the Hub from its own records; this form never
// takes a connection string, and the operator's relay is discovered from the
// operator's own published relay list (so there is no wss:// field here).
export function ListOfferPage() {
  const { t } = useTranslation("listings");
  const navigate = useNavigate();
  const { id } = useParams();
  const [search] = useSearchParams();

  const { data: listings } = useListings();
  const { data: circleData } = useApps(100, 1, { kind: "circle_hub" });

  const editing = !!id;
  const circleId = search.get("circle")
    ? Number(search.get("circle"))
    : undefined;

  if (!listings || (!editing && !circleData)) {
    return <Loading />;
  }

  const listing = editing
    ? listings.find((x) => x.id === Number(id))
    : undefined;

  // A stale/invalid edit id must not fall through to a blank "create" form
  // (which would submit with no circle). Show a clear not-found instead.
  if (editing && !listing) {
    return (
      <div className="grid gap-5">
        <AppHeader
          title={t("dialog.editTitle")}
          description={t("dialog.description")}
          contentRight={
            <Button variant="outline" onClick={() => navigate("/listings")}>
              {t("dialog.cancel")}
            </Button>
          }
        />
        <p className="text-sm text-muted-foreground">
          {t("pick.notFound", "That listing no longer exists.")}
        </p>
      </div>
    );
  }

  // New listing with no circle chosen yet → pick one.
  if (!editing && !circleId) {
    const listed = new Set(
      listings.filter((x) => x.phase !== "deleted").map((x) => x.hubAppId)
    );
    const unlisted = (circleData?.apps ?? []).filter((a) => !listed.has(a.id));
    return (
      <div className="grid gap-5">
        <AppHeader
          title={t("dialog.title")}
          description={t("dialog.description")}
          contentRight={
            <Button variant="outline" onClick={() => navigate("/listings")}>
              {t("dialog.cancel")}
            </Button>
          }
        />
        <div className="grid gap-2 sm:max-w-md">
          <p className="text-sm text-muted-foreground">
            {t("pick.prompt", "Which circle do you want to list?")}
          </p>
          {unlisted.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("pick.none", "All your circles are already listed.")}
            </p>
          ) : (
            unlisted.map((c) => (
              <button
                key={c.id}
                type="button"
                onClick={() => navigate(`/listings/new?circle=${c.id}`)}
                className="flex items-center gap-3 rounded-lg border bg-card p-3 text-left transition-colors hover:border-primary/50"
              >
                <span className="truncate text-sm font-semibold">{c.name}</span>
              </button>
            ))
          )}
        </div>
      </div>
    );
  }

  const circle = circleId
    ? circleData?.apps.find((a) => a.id === circleId)
    : undefined;

  // key remounts the form once its data is in, so its useState initialisers
  // see the real values rather than an empty first render.
  return (
    <ListOfferForm
      key={listing?.id ?? circleId}
      listing={listing}
      circle={circle}
      circleId={circleId}
    />
  );
}

function ListOfferForm({
  listing: l,
  circle,
  circleId,
}: {
  listing?: Listing;
  circle?: App;
  circleId?: number;
}) {
  const { t } = useTranslation("listings");
  const navigate = useNavigate();

  const circlePolicy = l ? undefined : circle?.circleIdentity?.policy;
  const isFollowSet = circlePolicy === "follow_set" || (l?.followSetA ?? false);

  const [title, setTitle] = React.useState(l?.title ?? "");
  const [content, setContent] = React.useState(l?.content ?? "");
  const [image, setImage] = React.useState(l?.image ?? "");
  const [location, setLocation] = React.useState(l?.location ?? "");
  const [languages, setLanguages] = React.useState<string[]>(
    l?.languages ?? []
  );
  const [regions, setRegions] = React.useState((l?.geohashes ?? []).join(", "));
  const [operator, setOperator] = React.useState(l?.operatorPubkey ?? "");
  const [followSetA, setFollowSetA] = React.useState(l?.followSetA ?? false);
  const [confirmFollowSet, setConfirmFollowSet] = React.useState(
    l?.followSetA ?? false
  );
  const [isLoading, setLoading] = React.useState(false);
  const [newLang, setNewLang] = React.useState("");

  const addLang = (code: string) => {
    const c = code.trim().toLowerCase();
    if (c && !languages.includes(c)) {
      setLanguages([...languages, c]);
    }
    setNewLang("");
  };

  const problem = validateListingForm({
    title,
    content,
    image,
    location,
    languages: languages.join(", "),
    regions,
    operator,
    operatorRelay: "",
  });

  const submit = async () => {
    if (problem) {
      return;
    }
    const fields: ListingFields = {
      title: title.trim(),
      content: content.trim(),
      image: image.trim(),
      location: location.trim(),
      languages,
      geohashes: splitList(regions),
      hints: false,
      follow_set_a: followSetA,
      confirm_follow_set_public: followSetA && confirmFollowSet,
      operator_pubkey: operator ? (safeDecodeToHex(operator) ?? "") : "",
      // New listings carry no manual relay — the Hub discovers it from the
      // operator's own published relay list. An existing manual override is
      // preserved on edit rather than silently wiped.
      operator_relay: l?.operatorRelay ?? "",
    };
    setLoading(true);
    try {
      const result = l
        ? await updateListing(l.id, fields)
        : await createListing({ circle_hub_app_id: circleId, ...fields });
      toast(l ? t("toast.updated") : t("toast.listed"));
      if (result) {
        navigate("/listings");
      }
    } catch (error) {
      handleRequestError(t("errors.save"), error);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="grid gap-5">
      <AppHeader
        title={l ? t("dialog.editTitle") : t("dialog.title")}
        description={t("dialog.description")}
        contentRight={
          <Button variant="outline" onClick={() => navigate("/listings")}>
            {t("dialog.cancel")}
          </Button>
        }
      />

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)] lg:items-start">
        {/* ---- the form ---- */}
        <div className="grid gap-6">
          <section className="grid gap-3">
            <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
              {t("groups.offer")}
            </h3>
            <div className="grid gap-1">
              <Label htmlFor="listing-title">{t("form.title")}</Label>
              <Input
                id="listing-title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
              <p className="text-end text-xs text-muted-foreground">
                {graphemes(title)}/{MAX_TITLE}
              </p>
            </div>
            <div className="grid gap-1">
              <Label htmlFor="listing-content">{t("form.content")}</Label>
              <Textarea
                id="listing-content"
                value={content}
                onChange={(e) => setContent(e.target.value)}
              />
              <p className="text-end text-xs text-muted-foreground">
                {graphemes(content)}/{MAX_CONTENT}
              </p>
            </div>
            <div className="grid gap-1">
              <Label htmlFor="listing-image">{t("form.image")}</Label>
              <Input
                id="listing-image"
                placeholder="https://"
                value={image}
                onChange={(e) => setImage(e.target.value)}
              />
            </div>
          </section>

          <section className="grid gap-3">
            <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
              {t("groups.find")}{" "}
              <span className="font-normal text-muted-foreground/70">
                {t("groups.optional")}
              </span>
            </h3>
            <div className="grid gap-1">
              <Label htmlFor="listing-location">{t("form.location")}</Label>
              <Input
                id="listing-location"
                value={location}
                onChange={(e) => setLocation(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label>{t("form.languages")}</Label>
              <div className="flex flex-wrap items-center gap-2">
                {languages.map((code) => (
                  <button
                    key={code}
                    type="button"
                    onClick={() =>
                      setLanguages(languages.filter((c) => c !== code))
                    }
                    className="inline-flex items-center gap-1 rounded-full bg-primary/15 px-2.5 py-1 text-xs font-medium text-primary"
                  >
                    {langLabel(code)}
                    <XIcon className="size-3" />
                  </button>
                ))}
                {COMMON_LANGS.filter(([c]) => !languages.includes(c)).map(
                  ([c, name]) => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => addLang(c)}
                      className="rounded-full border border-dashed px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:border-primary/50 hover:text-foreground"
                    >
                      + {name}
                    </button>
                  )
                )}
              </div>
              <Input
                aria-label={t("form.languages")}
                placeholder={t("form.languageAdd")}
                value={newLang}
                maxLength={3}
                className="max-w-56"
                onChange={(e) => setNewLang(e.target.value)}
                onBlur={() => newLang && addLang(newLang)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    addLang(newLang);
                  }
                }}
              />
            </div>
          </section>

          <Accordion type="single" collapsible>
            <AccordionItem value="advanced" className="border-b-0">
              <AccordionTrigger className="text-sm">
                {t("groups.advanced")}
                <span className="ms-2 text-xs font-normal text-muted-foreground">
                  {t("groups.advancedHint")}
                </span>
              </AccordionTrigger>
              <AccordionContent className="grid gap-4 pt-1">
                <div className="grid gap-1">
                  <Label htmlFor="listing-operator">{t("form.operator")}</Label>
                  <Input
                    id="listing-operator"
                    placeholder="npub1… / name@domain"
                    value={operator}
                    onChange={(e) => setOperator(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    {operator ? t("warnings.operator") : t("form.operatorHint")}
                  </p>
                </div>
                <div className="grid gap-1">
                  <Label htmlFor="listing-regions">
                    {t("form.regionsLabel")}
                  </Label>
                  <Input
                    id="listing-regions"
                    placeholder="eyc, eycd"
                    value={regions}
                    onChange={(e) => setRegions(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t("form.regionsHint")}
                  </p>
                </div>
                {isFollowSet && (
                  <div className="grid gap-2">
                    <label className="flex items-center gap-2 text-sm">
                      <Checkbox
                        checked={followSetA}
                        onCheckedChange={(v) => setFollowSetA(v === true)}
                      />
                      {t("form.followSetA")}
                    </label>
                    {followSetA && (
                      <label className="flex items-start gap-2 text-sm text-muted-foreground">
                        <Checkbox
                          className="mt-0.5"
                          checked={confirmFollowSet}
                          onCheckedChange={(v) =>
                            setConfirmFollowSet(v === true)
                          }
                        />
                        {t("warnings.followSet")}
                      </label>
                    )}
                  </div>
                )}
              </AccordionContent>
            </AccordionItem>
          </Accordion>

          {problem && (
            <p
              className="text-sm text-destructive"
              data-testid="listing-problem"
            >
              {t(problemKeys[problem])}
            </p>
          )}
        </div>

        {/* ---- sticky live preview + action ---- */}
        <aside className="grid gap-3 lg:sticky lg:top-4">
          <Alert>
            <AlertDescription>
              {t("warnings.public")} {t("warnings.circleTerms")}
            </AlertDescription>
          </Alert>
          <Label className="text-xs uppercase tracking-wide text-muted-foreground">
            {t("form.preview")}
          </Label>
          <ListingCard
            type={l?.type ?? "circle_wallet"}
            title={title}
            content={content}
            location={location}
            languages={languages}
            geohashes={splitList(regions)}
            status={l?.status}
          />
          <LoadingButton
            className="w-full"
            loading={isLoading}
            disabled={
              !!problem || (followSetA && !confirmFollowSet) || isLoading
            }
            onClick={submit}
          >
            {l ? t("dialog.save") : t("dialog.list")}
          </LoadingButton>
          <p
            className={cn(
              "text-center text-xs text-muted-foreground",
              l && "invisible"
            )}
          >
            {t("form.canUnlist")}
          </p>
        </aside>
      </div>
    </div>
  );
}
