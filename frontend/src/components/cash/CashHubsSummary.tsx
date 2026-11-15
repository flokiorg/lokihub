import React from "react";
import { useTranslation } from "react-i18next";
import {
  MedianDuration,
  MetricInfo,
  Stat,
} from "src/components/cash/CashHubOverview";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { Card, CardContent } from "src/components/ui/card";
import { CashHubStats } from "src/types";

// The same figures CashHubOverview shows for one hub, totalled across every
// hub on the node — the Cash Hubs list's answer to "what is my whole cash
// operation doing", which previously required opening each hub in turn and
// adding up by hand.
//
// It reuses that card's own Stat/MetricInfo, so a metric cannot come to mean
// one thing on a dashboard and another on the list. What it deliberately
// leaves out is everything hub-specific: an isolated balance, a budget and
// its renewal belong to one hub and do not total into anything meaningful.
export function CashHubsSummary({
  stats,
  analytics,
}: {
  stats: CashHubStats;
  // See CashHubOverview's own analytics prop — same reasoning, same slot
  // shape, just totalled across every hub instead of one.
  analytics?: React.ReactNode;
}) {
  const { t } = useTranslation("circles");
  const { t: ta } = useTranslation("apps");

  return (
    <Card className="slashed-zero">
      <CardContent className="grid gap-4">
        {/* The same lead pair as one hub's dashboard — what can still be
            issued against, and what is already owed — only totalled. Both
            come from the server: the list filters its page of apps
            client-side, so anything counted here would be the page's figure
            rather than the node's. */}
        <div className="grid gap-4 sm:grid-cols-2">
          <Stat
            label={ta("usage.isolatedBalance")}
            info={t("cashKpis.balanceInfo")}
            amountMloki={stats.balance_mloki}
            size="lg"
          >
            <p className="text-muted-foreground mt-1 text-xs">
              {stats.hubs_count} {t("cashKpis.allHubsCountHint")}
            </p>
          </Stat>
          <Stat
            label={t("cashKpis.outstanding")}
            info={t("cashKpis.outstandingInfo")}
            amountMloki={stats.outstanding_mloki}
            size="lg"
          >
            <p className="text-muted-foreground mt-1 text-xs">
              {t("cashKpis.outstandingHint", {
                count: stats.outstanding_count,
              })}
            </p>
          </Stat>
        </div>

        <div className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
          <Stat
            label={t("cashKpis.issued")}
            info={t("cashKpis.issuedInfo")}
            amountMloki={stats.minted_mloki}
          />
          <Stat
            label={t("cashKpis.redeemed")}
            info={t("cashKpis.redeemedInfo")}
            amountMloki={stats.redeemed_mloki}
          />
          <Stat
            label={t("cashKpis.returned")}
            info={t("cashKpis.returnedInfo")}
            amountMloki={stats.returned_mloki}
          />
          <Stat
            label={t("cashKpis.feesEarned")}
            info={t("cashKpis.feesEarnedInfo")}
            amountMloki={stats.fees_earned_mloki}
          />
        </div>

        <div className="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-xs">
          <span className="flex items-center gap-1.5">
            <MetricInfo text={t("cashKpis.medianTimeToRedeemInfo")} />
            {t("cashKpis.medianTimeToRedeem")}:{" "}
            <span className="text-foreground font-medium">
              {stats.median_time_to_redeem_secs === null ? (
                t("cashKpis.noRedemptionsYet")
              ) : (
                <MedianDuration secs={stats.median_time_to_redeem_secs} />
              )}
            </span>
          </span>
          {/* Same rule as the dashboard's: a write-off is value that could
              not be returned anywhere, so it appears only when it happens
              rather than sitting at zero as permanent furniture. */}
          {stats.written_off_mloki > 0 && (
            <span className="text-destructive flex items-center gap-1.5">
              <MetricInfo text={t("cashKpis.writtenOffInfo")} />
              {t("cashKpis.writtenOff")}:{" "}
              <FormattedFlokicoinAmount amount={stats.written_off_mloki} />
            </span>
          )}
        </div>

        {analytics && <div className="border-t pt-4">{analytics}</div>}
      </CardContent>
    </Card>
  );
}
