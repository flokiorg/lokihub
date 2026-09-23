import { useTranslation } from "react-i18next";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { CashHubStats } from "src/types";

// Follows the ForwardsWidget tile shape: a muted label over a large tabular
// value, with the fiat equivalent underneath. `sensitive` opts the figure into
// the app's balance-hiding, and `slashed-zero` keeps digits legible.
function Kpi({
  label,
  amountMloki,
  hint,
}: {
  label: string;
  amountMloki: number;
  hint?: string;
}) {
  return (
    <div>
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="text-xl font-semibold balance sensitive">
        <FormattedFlokicoinAmount amount={amountMloki} />
      </p>
      <FormattedFiatAmount amount={Math.floor(amountMloki / 1000)} />
      {hint && <p className="text-muted-foreground text-xs mt-0.5">{hint}</p>}
    </div>
  );
}

// Returns the unit key and count rather than a translated string: i18n keys
// here are strongly typed, so `t` cannot be passed around as a loose
// (key, options) => string without losing that checking.
function durationParts(secs: number): {
  unit: "Seconds" | "Minutes" | "Hours" | "Days";
  count: number;
} {
  if (secs < 60) {
    return { unit: "Seconds", count: secs };
  }
  if (secs < 3600) {
    return { unit: "Minutes", count: Math.round(secs / 60) };
  }
  if (secs < 86400) {
    return { unit: "Hours", count: Math.round(secs / 3600) };
  }
  return { unit: "Days", count: Math.round(secs / 86400) };
}

function MedianDuration({ secs }: { secs: number }) {
  const { t } = useTranslation("circles");
  const { unit, count } = durationParts(secs);
  switch (unit) {
    case "Seconds":
      return <>{t("cashKpis.durationSeconds", { count })}</>;
    case "Minutes":
      return <>{t("cashKpis.durationMinutes", { count })}</>;
    case "Hours":
      return <>{t("cashKpis.durationHours", { count })}</>;
    default:
      return <>{t("cashKpis.durationDays", { count })}</>;
  }
}

export function CashKpis({ stats }: { stats: CashHubStats }) {
  const { t } = useTranslation("circles");

  return (
    <div className="flex flex-col sm:flex-row flex-wrap gap-4 slashed-zero">
      {/*
        Outstanding leads because it is the only figure that is a live
        obligation rather than history: it is what this hub still owes to
        whoever is holding its bills.
      */}
      <Card className="flex flex-1 flex-col">
        <CardHeader className="pb-2">
          <CardTitle className="text-lg">{t("cashKpis.outstanding")}</CardTitle>
        </CardHeader>
        <CardContent className="grow">
          <div className="mb-1">
            <span className="text-2xl font-medium balance sensitive">
              <FormattedFlokicoinAmount amount={stats.outstanding_mloki} />
            </span>
          </div>
          <FormattedFiatAmount
            amount={Math.floor(stats.outstanding_mloki / 1000)}
          />
          <p className="text-muted-foreground text-xs mt-1">
            {t("cashKpis.outstandingHint", { count: stats.outstanding_count })}
          </p>
        </CardContent>
      </Card>

      <Card className="flex flex-1 flex-col">
        <CardHeader className="pb-2">
          <CardTitle className="text-lg">{t("cashKpis.flowTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="grow">
          <div className="grid grid-cols-2 gap-6">
            <Kpi
              label={t("cashKpis.issued")}
              amountMloki={stats.issued_mloki}
            />
            <Kpi
              label={t("cashKpis.redeemed")}
              amountMloki={stats.redeemed_mloki}
            />
            <Kpi
              label={t("cashKpis.returned")}
              amountMloki={stats.returned_mloki}
            />
            <Kpi
              label={t("cashKpis.feesEarned")}
              amountMloki={stats.fees_earned_mloki}
            />
          </div>
          <div className="mt-4 flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <span>
              {t("cashKpis.medianTimeToRedeem")}:{" "}
              <span className="text-foreground font-medium">
                {stats.median_time_to_redeem_secs === null ? (
                  t("cashKpis.noRedemptionsYet")
                ) : (
                  <MedianDuration secs={stats.median_time_to_redeem_secs} />
                )}
              </span>
            </span>
            {/*
              Only shown when non-zero: a write-off means value that could not
              be returned to the hub at all, so it should stand out when it
              happens rather than sit at zero as permanent furniture.
            */}
            {stats.written_off_mloki > 0 && (
              <span className="text-destructive">
                {t("cashKpis.writtenOff")}:{" "}
                <FormattedFlokicoinAmount amount={stats.written_off_mloki} />
              </span>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
