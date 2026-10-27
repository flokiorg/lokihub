import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ListingCard } from "src/components/listings/ListingCard";
import {
  MAX_CONTENT,
  MAX_TITLE,
  graphemes,
  splitList,
  validateListingForm,
} from "src/components/listings/text";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Checkbox } from "src/components/ui/checkbox";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Textarea } from "src/components/ui/textarea";
import {
  type Listing,
  type ListingFields,
  createListing,
  updateListing,
} from "src/hooks/useListings";
import { handleRequestError } from "src/utils/handleRequestError";
import { safeDecodeToHex } from "src/utils/nostr";

type ListOfferDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // A circle to list, or an existing listing to edit.
  circleHubAppId?: number;
  circlePolicy?: string;
  listing?: Listing;
  onDone?: (listing: Listing) => void;
};

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

// ListOfferDialog lists an offer, or edits a listed one (WP-19 item 13). The
// listed string is built by the Hub from its own records: this form never takes
// a connection string.
export function ListOfferDialog(props: ListOfferDialogProps) {
  const { t } = useTranslation("listings");
  const l = props.listing;
  const isFollowSet =
    props.circlePolicy === "follow_set" || (l?.followSetA ?? false);
  const [title, setTitle] = React.useState(l?.title ?? "");
  const [content, setContent] = React.useState(l?.content ?? "");
  const [image, setImage] = React.useState(l?.image ?? "");
  const [location, setLocation] = React.useState(l?.location ?? "");
  const [languages, setLanguages] = React.useState(
    (l?.languages ?? []).join(", ")
  );
  const [regions, setRegions] = React.useState((l?.geohashes ?? []).join(", "));
  const [operator, setOperator] = React.useState(l?.operatorPubkey ?? "");
  const [operatorRelay, setOperatorRelay] = React.useState(
    l?.operatorRelay ?? ""
  );
  const [followSetA, setFollowSetA] = React.useState(l?.followSetA ?? false);
  const [confirmFollowSet, setConfirmFollowSet] = React.useState(
    l?.followSetA ?? false
  );
  const [isLoading, setLoading] = React.useState(false);

  const problem = validateListingForm({
    title,
    content,
    image,
    location,
    languages,
    regions,
    operator,
    operatorRelay,
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
      languages: splitList(languages),
      geohashes: splitList(regions),
      hints: false,
      follow_set_a: followSetA,
      confirm_follow_set_public: followSetA && confirmFollowSet,
      operator_pubkey: operator ? (safeDecodeToHex(operator) ?? "") : "",
      operator_relay: operatorRelay.trim(),
    };
    setLoading(true);
    try {
      const result = l
        ? await updateListing(l.id, fields)
        : await createListing({
            circle_hub_app_id: props.circleHubAppId,
            ...fields,
          });
      toast(l ? t("toast.updated") : t("toast.listed"));
      if (result) {
        props.onDone?.(result);
      }
      props.onOpenChange(false);
    } catch (error) {
      handleRequestError(t("errors.save"), error);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {l ? t("dialog.editTitle") : t("dialog.title")}
          </DialogTitle>
          <DialogDescription>{t("dialog.description")}</DialogDescription>
        </DialogHeader>
        <Alert>
          <AlertDescription className="space-y-1">
            <p>{t("warnings.public")}</p>
            <p>{t("warnings.pow")}</p>
            <p>{t("warnings.circleTerms")}</p>
            {isFollowSet && <p>{t("warnings.followSet")}</p>}
          </AlertDescription>
        </Alert>
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="listing-title">{t("form.title")}</Label>
              <Input
                id="listing-title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {graphemes(title)}/{MAX_TITLE}
              </p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="listing-content">{t("form.content")}</Label>
              <Textarea
                id="listing-content"
                value={content}
                onChange={(e) => setContent(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {graphemes(content)}/{MAX_CONTENT}
              </p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="listing-image">{t("form.image")}</Label>
              <Input
                id="listing-image"
                placeholder="https://"
                value={image}
                onChange={(e) => setImage(e.target.value)}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="listing-location">{t("form.location")}</Label>
              <Input
                id="listing-location"
                value={location}
                onChange={(e) => setLocation(e.target.value)}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="listing-regions">{t("form.regions")}</Label>
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
            <div className="space-y-1">
              <Label htmlFor="listing-languages">{t("form.languages")}</Label>
              <Input
                id="listing-languages"
                placeholder="en, pt"
                value={languages}
                onChange={(e) => setLanguages(e.target.value)}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="listing-operator">{t("form.operator")}</Label>
              <Input
                id="listing-operator"
                placeholder="npub1…"
                value={operator}
                onChange={(e) => setOperator(e.target.value)}
              />
              {operator && (
                <p className="text-xs text-muted-foreground">
                  {t("warnings.operator")}
                </p>
              )}
              <Input
                aria-label={t("form.operatorRelay")}
                placeholder="wss://"
                value={operatorRelay}
                onChange={(e) => setOperatorRelay(e.target.value)}
              />
            </div>
            {isFollowSet && (
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <Checkbox
                    id="listing-follow-set"
                    checked={followSetA}
                    onCheckedChange={(v) => setFollowSetA(v === true)}
                  />
                  <Label htmlFor="listing-follow-set">
                    {t("form.followSetA")}
                  </Label>
                </div>
                {followSetA && (
                  <div className="flex items-center gap-2">
                    <Checkbox
                      id="listing-follow-set-confirm"
                      checked={confirmFollowSet}
                      onCheckedChange={(v) => setConfirmFollowSet(v === true)}
                    />
                    <Label htmlFor="listing-follow-set-confirm">
                      {t("form.followSetConfirm")}
                    </Label>
                  </div>
                )}
              </div>
            )}
          </div>
          <div className="space-y-2">
            <Label>{t("form.preview")}</Label>
            <ListingCard
              type={l?.type ?? "circle_wallet"}
              title={title}
              content={content}
              location={location}
              languages={splitList(languages)}
              geohashes={splitList(regions)}
              status={l?.status}
            />
          </div>
        </div>
        {problem && (
          <p className="text-destructive text-sm" data-testid="listing-problem">
            {t(problemKeys[problem])}
          </p>
        )}
        <DialogFooter>
          <LoadingButton
            loading={isLoading}
            disabled={
              !!problem || (followSetA && !confirmFollowSet) || isLoading
            }
            onClick={submit}
          >
            {l ? t("dialog.save") : t("dialog.list")}
          </LoadingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
