import { MapPinIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import {
  MAX_CONTENT,
  MAX_TITLE,
  capText,
  isOneLine,
} from "src/components/listings/text";

type ListingCardProps = {
  type: "cash_order" | "circle_wallet";
  title: string;
  content: string;
  location: string;
  languages: string[];
  geohashes: string[];
  status?: "open" | "paused" | "closed";
};

// ListingCard previews how a wallet shows the listing, with the same caps a
// wallet applies (title and place one line of 40 characters, description 280).
// The image is never loaded here: a wallet loads it only after a tap.
export function ListingCard(props: ListingCardProps) {
  const { t } = useTranslation("listings");
  const title = capText(
    isOneLine(props.title) ? props.title : props.title.split(/\r?\n/)[0],
    MAX_TITLE
  );
  const place = capText(props.location, MAX_TITLE);
  return (
    <Card data-testid="listing-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <span data-testid="listing-card-title">
            {title || t("card.untitled")}
          </span>
          <Badge variant="secondary">
            {props.type === "cash_order"
              ? t("card.typeOrder")
              : t("card.typeCircle")}
          </Badge>
          {props.status && props.status !== "open" && (
            <Badge variant="outline">{t(`status.${props.status}`)}</Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {props.content && (
          <p
            className="text-muted-foreground whitespace-pre-wrap"
            data-testid="listing-card-content"
          >
            {capText(props.content, MAX_CONTENT)}
          </p>
        )}
        {(place || props.geohashes.length > 0) && (
          <p className="flex items-center gap-1 text-muted-foreground">
            <MapPinIcon className="size-4" />
            {place || props.geohashes.join(", ")}
          </p>
        )}
        {props.languages.length > 0 && (
          <p className="text-muted-foreground">{props.languages.join(" · ")}</p>
        )}
      </CardContent>
    </Card>
  );
}
