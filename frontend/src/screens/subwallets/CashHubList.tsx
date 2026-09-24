import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { BanknoteIcon, ChevronRightIcon, CirclePlusIcon } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import { CustomPagination } from "src/components/CustomPagination";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import {
  CashCoverage,
  CashExpiryRunway,
  CashHubBreakdown,
  CashOutstandingChart,
} from "src/components/cash/CashMonitorCharts";
import { CashHubsSummary } from "src/components/cash/CashHubsSummary";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Card, CardTitle } from "src/components/ui/card";
import { Checkbox } from "src/components/ui/checkbox";
import {
  LIST_APPS_LIMIT,
  SUBWALLET_APPSTORE_APP_ID,
  localStorageKeys,
} from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useAllCashHubStats } from "src/hooks/useCashHub";
import { useInfo } from "src/hooks/useInfo";

dayjs.extend(relativeTime);

// The entry point for the Cash Hub feature — a Cash Hub mints on-demand,
// spend-only lokicash for beneficiaries, held in a wallet created for that
// purpose. This list itself only needs to fetch/display cash_hub apps;
// minting, viewing, and deleting the lokicash tokens a given hub has minted
// happens on that hub's own AppDetails page
// (CashHubConfigCard/CashHubAllocations/DisconnectCashHub), reached by
// clicking into a row below.
//
// Deliberately a table, not the AppCard grid Sub-wallets/Connections use —
// a Cash Hub is a minter you monitor (balance, activity), not a wallet you
// browse, so this reads as an operational list rather than another wallet
// grid (matches the table pattern already used for Channels/Peers).
export function CashHubList() {
  const { t } = useTranslation("apps");
  const { data: info } = useInfo();
  const { data: stats } = useAllCashHubStats();
  const [page, setPage] = useState(1);
  // Mirrors the per-hub dashboard's Analytics section, down to the stored
  // "keep open" default — but under its own key, since wanting every hub's
  // chart on the list is a different decision from wanting one hub's.
  const [keepAnalyticsOpen, setKeepAnalyticsOpen] = useState(
    () =>
      localStorage.getItem(localStorageKeys.cashHubsAnalyticsKeepOpen) ===
      "true"
  );
  const [isAnalyticsOpen, setAnalyticsOpen] = useState(keepAnalyticsOpen);

  const toggleKeepAnalyticsOpen = (checked: boolean) => {
    setKeepAnalyticsOpen(checked);
    localStorage.setItem(
      localStorageKeys.cashHubsAnalyticsKeepOpen,
      String(checked)
    );
  };
  const appsListRef = useRef<HTMLDivElement>(null);
  // Same underlying query SubwalletList uses (every app tagged as a
  // sub-wallet), filtered client-side to just cash_hub — the admin API has
  // no server-side "kind" filter, only appStoreAppId/name/unused, so this
  // mirrors SubwalletList's own existing imprecision (a page may contain
  // fewer cash_hub apps than LIST_APPS_LIMIT if other sub-wallet kinds share
  // the same page) rather than introducing a new pattern for it.
  const { data: appsData } = useApps(
    undefined,
    page,
    { appStoreAppId: SUBWALLET_APPSTORE_APP_ID },
    "created_at"
  );

  const handlePageChange = (page: number) => {
    setPage(page);
    appsListRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "start",
    });
  };

  if (!info || !appsData) {
    return <Loading />;
  }

  const cashHubApps = appsData.apps.filter((app) => app.kind === "cash_hub");

  if (!cashHubApps.length) {
    return (
      <div className="grid gap-4">
        <AppHeader
          title="Cash Hubs"
          description="Mint Lokicash and track it through to redemption"
        />
        <Card className="flex flex-col items-center gap-4 p-8 text-center">
          <BanknoteIcon className="size-10 text-muted-foreground" />
          <CardTitle className="text-lg">No Cash Hubs yet</CardTitle>
          <p className="max-w-md text-sm text-muted-foreground">
            A Cash Hub turns your balance into Lokicash: tokens you hand over
            like a bill. The holder redeems whenever they're ready.
          </p>
          <ResponsiveLinkButton
            to="/cash-hub/new"
            icon={CirclePlusIcon}
            text="Create a Cash Hub"
          />
        </Card>
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <AppHeader
        title="Cash Hubs"
        description="Mint Lokicash and track it through to redemption"
        contentRight={
          <ResponsiveLinkButton
            to="/cash-hub/new"
            icon={CirclePlusIcon}
            text="New Cash Hub"
          />
        }
      />

      {/* The same overview one hub's dashboard opens with, totalled across
          every hub. It replaces a two-figure text line that could only ever
          show the current page's hubs, and answered none of what an operator
          checks first: what is owed, what has come back, what it earned. */}
      {stats && (
        <>
          <CashHubsSummary stats={stats} />

          {/* The section is NOT a card: each chart below brings its own, and
              a card inside a card just draws a second border around the same
              content. The trigger row carries the heading instead. */}
          <Accordion
            type="single"
            collapsible
            value={isAnalyticsOpen ? "analytics" : ""}
            onValueChange={(v) => setAnalyticsOpen(v === "analytics")}
          >
            <AccordionItem value="analytics" className="border-b-0">
              {/* Checkbox as a sibling of the trigger, not a child: the
                  trigger is a <button>, so a control nested inside it could
                  not be clicked without toggling the section. */}
              <div className="flex items-center gap-3 px-1">
                <AccordionTrigger className="flex-1 py-0 text-base font-semibold">
                  {t("circleHub.analyticsTitle")}
                </AccordionTrigger>
                {isAnalyticsOpen && (
                  <label className="text-muted-foreground flex shrink-0 cursor-pointer items-center gap-2 text-sm font-normal">
                    <Checkbox
                      checked={keepAnalyticsOpen}
                      onCheckedChange={(checked) =>
                        toggleKeepAnalyticsOpen(checked === true)
                      }
                    />
                    {t("circleHub.keepAnalyticsOpen")}
                  </label>
                )}
              </div>
              <AccordionContent className="pt-4 pb-0">
                {/* Coverage leads, because it is the only card here that can
                    represent a problem rather than a fact. The rest runs
                    forward in time: what is owed now, what that will do next,
                    and which hub it sits on. */}
                <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
                  {/* Coverage is a few figures, not a plot, so it takes
                      the whole row rather than sitting half-empty beside
                      one. The two categorical plots pair, and the time
                      series goes wide, where a 30-day axis is readable. */}
                  <CashCoverage
                    className="lg:col-span-2"
                    backingMloki={stats.backing_mloki}
                    outstandingMloki={stats.outstanding_mloki}
                    shortfallMloki={stats.shortfall_mloki}
                    capacityMloki={stats.balance_mloki}
                  />
                  <CashExpiryRunway stats={stats} />
                  <CashHubBreakdown stats={stats} />
                  <CashOutstandingChart
                    className="lg:col-span-2"
                    daily={stats.daily}
                    outstandingMloki={stats.outstanding_mloki}
                  />
                </div>
              </AccordionContent>
            </AccordionItem>
          </Accordion>
        </>
      )}

      {/*
        Borderless rows rather than a bordered table, matching TransactionsList:
        no Card or Table wrapper, just mapped rows with a hairline between
        them. Each row is the whole link target, so the chevron is decoration
        rather than the only clickable thing.
      */}
      <div ref={appsListRef} className="flex flex-col flex-1">
        {cashHubApps.map((app) => (
          <Link
            key={app.id}
            to={`/cash-hub/${app.id}`}
            className="flex items-center gap-3 border-b last:border-b-0 py-3 hover:bg-accent/40 transition-colors px-1 min-w-0"
          >
            <AppAvatar app={app} className="size-8 shrink-0" />
            <div className="min-w-0 flex-1">
              <p className="font-medium truncate">{app.name}</p>
              <p className="text-xs text-muted-foreground">
                {app.lastUsedAt
                  ? dayjs(app.lastUsedAt).fromNow()
                  : "Never used"}
              </p>
            </div>
            <span className="sensitive slashed-zero shrink-0">
              <FormattedFlokicoinAmount amount={app.balance} />
            </span>
            <ChevronRightIcon className="size-4 text-muted-foreground shrink-0" />
          </Link>
        ))}
      </div>

      {/* totalCount is the unfiltered sub-wallet total (the admin API has no
          server-side "kind" filter) — an upper bound, not the exact cash_hub
          count, so pagination may occasionally offer a page that turns out
          to have zero cash_hub apps on it once fetched and filtered. Safer
          than undercounting, which would hide real additional pages of
          cash_hub apps behind ones dominated by other sub-wallet kinds. */}
      <CustomPagination
        limit={LIST_APPS_LIMIT}
        totalCount={appsData.totalCount}
        page={page}
        handlePageChange={handlePageChange}
      />
    </div>
  );
}
