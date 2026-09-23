import { InfoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { CashSliceStatus } from "src/types";

// Every status maps onto a Badge variant that already exists — no new variants
// were needed, and the colours carry meaning: value still owed is neutral,
// value paid out is positive, value that merely moved is outlined, a window
// that lapsed is a warning, and value that could not be returned is
// destructive because it is a real loss.
const VARIANT: Record<
  CashSliceStatus,
  "secondary" | "positive" | "outline" | "warning" | "destructive"
> = {
  unclaimed: "secondary",
  redeemed: "positive",
  split: "outline",
  expired: "warning",
  reclaimed: "outline",
  "written-off": "destructive",
};

const CASH_STATUS_ORDER: CashSliceStatus[] = [
  "unclaimed",
  "redeemed",
  "split",
  "expired",
  "reclaimed",
  "written-off",
];

export function CashStatusBadge({ status }: { status: CashSliceStatus }) {
  const { t } = useTranslation("circles");
  return (
    <Badge variant={VARIANT[status] ?? "secondary"}>
      {t(`cashStatus.${status}.label`)}
    </Badge>
  );
}

// CashStatusLegend explains all six statuses in one place.
//
// Deliberately rendered ONCE per view, next to the facets — not per row. A
// tooltip on every badge would repeat the same six definitions down the whole
// table and add nothing after the first read.
export function CashStatusLegend() {
  const { t } = useTranslation("circles");
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          aria-label={t("cashStatus.legendLabel")}
          className="text-muted-foreground hover:text-foreground transition-colors"
        >
          <InfoIcon className="h-4 w-4" />
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">
          <ul className="grid gap-1.5 py-1">
            {CASH_STATUS_ORDER.map((status) => (
              <li key={status} className="flex items-start gap-2">
                <span className="font-medium whitespace-nowrap">
                  {t(`cashStatus.${status}.label`)}
                </span>
                <span className="text-muted-foreground">
                  {t(`cashStatus.${status}.description`)}
                </span>
              </li>
            ))}
          </ul>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
