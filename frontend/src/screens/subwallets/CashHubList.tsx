import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import {
  BanknoteIcon,
  BarChart3Icon,
  ChevronDownIcon,
  ChevronRightIcon,
  CirclePlusIcon,
  SearchIcon,
} from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import { CustomPagination } from "src/components/CustomPagination";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { OverviewCardSkeleton } from "src/components/OverviewCardSkeleton";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import { Fab } from "src/components/redesign/Fab";
import { CoverageBadge } from "src/components/cash/CashHubOverview";
import { Input } from "src/components/ui/input";
import { cn } from "src/lib/utils";
import {
  CashCoverage,
  CashExpiryRunway,
  CashHubBreakdown,
  CashOutstandingChart,
} from "src/components/cash/CashMonitorCharts";
import { CashHubsSummary } from "src/components/cash/CashHubsSummary";
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

  // Toolbar: the redesign gives the list a search, a status filter and a sort
  // so a shortfall is findable from the list rather than only inside a hub.
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<
    "all" | "active" | "underfunded"
  >("all");
  const [sortBy, setSortBy] = useState<"newest" | "name" | "backing">("newest");

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

  // Per-hub outstanding/backing so each row can show "in circulation", the
  // backing it holds, and a coverage verdict — the "rows that carry state" the
  // redesign asks for. Keyed by app id from the one /cash-stats call.
  const perHub = new Map(
    (stats?.per_hub ?? []).map((h) => [h.hub_app_id, h])
  );
  const hubState = (appId: number, backingMloki: number) => {
    const outstanding = perHub.get(appId)?.outstanding_mloki ?? 0;
    return {
      outstanding,
      backing: backingMloki,
      shortfall: Math.max(0, outstanding - backingMloki),
    };
  };

  if (!cashHubApps.length) {
    return (
      <div className="grid gap-4">
        <AppHeader
          title="Cash Hubs"
          description="Mint Lokicash and track it through to redemption"
        />
        <div className="flex flex-col items-center gap-4 py-16 text-center">
          <BanknoteIcon className="size-10 text-muted-foreground" />
          <h2 className="text-lg font-semibold">No Cash Hubs yet</h2>
          <p className="max-w-md text-sm text-muted-foreground">
            A Cash Hub turns your balance into Lokicash: tokens you hand over
            like a bill. The holder redeems whenever they're ready.
          </p>
          <ResponsiveLinkButton
            to="/cash-hub/new"
            icon={CirclePlusIcon}
            text="Create a Cash Hub"
          />
        </div>
      </div>
    );
  }

  const visibleHubs = cashHubApps
    .filter((app) => {
      if (query && !app.name.toLowerCase().includes(query.toLowerCase())) {
        return false;
      }
      const { outstanding, shortfall } = hubState(app.id, app.balance);
      if (statusFilter === "active") {
        return outstanding > 0;
      }
      if (statusFilter === "underfunded") {
        return shortfall > 0;
      }
      return true;
    })
    .sort((a, b) => {
      if (sortBy === "name") {
        return a.name.localeCompare(b.name);
      }
      if (sortBy === "backing") {
        return b.balance - a.balance;
      }
      return 0; // "newest" keeps the API's created_at order
    });

  const filters: { key: typeof statusFilter; label: string }[] = [
    { key: "all", label: t("cashHubs.filterAll") },
    { key: "active", label: t("cashHubs.filterActive") },
    { key: "underfunded", label: t("cashHubs.filterUnderfunded") },
  ];

  return (
    <div className="grid gap-4">
      <AppHeader
        title="Cash Hubs"
        description="Mint Lokicash and track it through to redemption"
        contentRight={
          // On mobile the bottom FAB is the primary "New Cash Hub" action, so
          // the header button is hidden there to avoid a duplicate (matches the
          // circle list and the design's FAB-replaces-header rule).
          <div className="hidden sm:block">
            <ResponsiveLinkButton
              to="/cash-hub/new"
              icon={CirclePlusIcon}
              text="New Cash Hub"
            />
          </div>
        }
      />

      {/* The same overview one hub's dashboard opens with, totalled across
          every hub. It replaces a two-figure text line that could only ever
          show the current page's hubs, and answered none of what an operator
          checks first: what is owed, what has come back, what it earned.
          Analytics lives inside this card (its last section) instead of as
          its own block below it. While stats are still loading, a skeleton
          takes this card's place instead of rendering nothing. */}
      {stats ? (
        // CashHubsSummary renders its own hero card + stat-strip card now, so
        // it is not wrapped in an outer card (would double-frame it).
        <CashHubsSummary stats={stats} />
      ) : (
        <OverviewCardSkeleton />
      )}

      {/* Toolbar: search + status filter + sort — what this list previously
          had none of, so a shortfall is findable without opening each hub. */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="relative flex-1">
          <SearchIcon className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("cashHubs.searchPlaceholder")}
            className="ps-9"
          />
        </div>
        <div className="flex items-center gap-1 rounded-lg border p-1">
          {filters.map((f) => (
            <button
              key={f.key}
              type="button"
              onClick={() => setStatusFilter(f.key)}
              className={cn(
                "rounded-md px-3 py-1 text-sm transition-colors",
                statusFilter === f.key
                  ? "bg-muted text-foreground font-medium"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="relative">
          <select
            value={sortBy}
            onChange={(e) =>
              setSortBy(e.target.value as "newest" | "name" | "backing")
            }
            className="border-input h-9 appearance-none rounded-md border bg-transparent ps-3 pe-9 text-sm"
            aria-label={t("cashHubs.sortLabel")}
          >
            <option value="newest">
              {t("cashHubs.sortLabel")}: {t("cashHubs.sortNewest")}
            </option>
            <option value="name">
              {t("cashHubs.sortLabel")}: {t("cashHubs.sortName")}
            </option>
            <option value="backing">
              {t("cashHubs.sortLabel")}: {t("cashHubs.sortBacking")}
            </option>
          </select>
          <ChevronDownIcon className="pointer-events-none absolute end-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        </div>
        {/* Analytics is a toolbar disclosure (the design places it here, next to
            Sort), not a section inside the hero card. */}
        {stats && (
          <button
            type="button"
            onClick={() => setAnalyticsOpen((o) => !o)}
            aria-expanded={isAnalyticsOpen}
            className={cn(
              "flex h-9 items-center gap-1.5 rounded-md border px-3 text-sm transition-colors",
              isAnalyticsOpen
                ? "border-primary/40 bg-primary/15 text-foreground font-medium"
                : "border-input text-muted-foreground hover:text-foreground"
            )}
          >
            <BarChart3Icon className="size-4" />
            {t("circleHub.analyticsTitle")}
            <ChevronDownIcon
              className={cn(
                "size-4 transition-transform",
                isAnalyticsOpen && "rotate-180"
              )}
            />
          </button>
        )}
      </div>

      {/* Analytics disclosure — the same charts that were inside the hero card,
          now revealed from the toolbar button above. */}
      {stats && isAnalyticsOpen && (
        <div className="rounded-xl border bg-card p-5">
          <div className="mb-4 flex items-center justify-between gap-3">
            <h2 className="text-base font-semibold">
              {t("circleHub.analyticsTitle")}
            </h2>
            <label className="text-muted-foreground flex shrink-0 cursor-pointer items-center gap-2 text-sm font-normal">
              <Checkbox
                checked={keepAnalyticsOpen}
                onCheckedChange={(checked) =>
                  toggleKeepAnalyticsOpen(checked === true)
                }
              />
              {t("circleHub.keepAnalyticsOpen")}
            </label>
          </div>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
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
        </div>
      )}

      {/* Each hub is its own card and carries its state — a coverage verdict,
          what's in circulation, and the backing it holds — so the whole card is
          the link target and the chevron is decoration. On a phone the metrics
          wrap onto their own full-width line under the name. */}
      <div ref={appsListRef} className="flex flex-col gap-3">
        {visibleHubs.length === 0 ? (
          <p className="text-muted-foreground py-8 text-center text-sm">
            {t("cashHubs.noMatches")}
          </p>
        ) : (
          visibleHubs.map((app) => {
            const s = hubState(app.id, app.balance);
            return (
              <Link
                key={app.id}
                to={`/cash-hub/${app.id}`}
                className="hover:border-primary/50 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border bg-card p-3 transition-colors sm:flex-nowrap"
              >
                <AppAvatar app={app} className="size-8 shrink-0" />
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{app.name}</p>
                  <p className="text-muted-foreground text-xs">
                    {app.lastUsedAt
                      ? dayjs(app.lastUsedAt).fromNow()
                      : "Never used"}
                  </p>
                </div>
                <div className="flex w-full items-center justify-between gap-4 sm:w-auto sm:justify-end">
                  <CoverageBadge
                    outstandingMloki={s.outstanding}
                    shortfallMloki={s.shortfall}
                  />
                  <div className="text-end">
                    <p className="text-muted-foreground text-xs">
                      {t("cashHubs.inCirculation")}
                    </p>
                    <p className="sensitive slashed-zero text-sm font-medium">
                      <FormattedFlokicoinAmount amount={s.outstanding} />
                    </p>
                  </div>
                  <div className="text-end">
                    <p className="text-muted-foreground text-xs">
                      {t("cashHubs.backing")}
                    </p>
                    <p className="sensitive slashed-zero text-sm font-medium">
                      <FormattedFlokicoinAmount amount={app.balance} />
                    </p>
                  </div>
                  <ChevronRightIcon className="text-muted-foreground size-4 shrink-0" />
                </div>
              </Link>
            );
          })
        )}
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

      {/* Mobile thumb-first primary action (desktop uses the header button). */}
      <Fab to="/cash-hub/new" icon={CirclePlusIcon} label="New Cash Hub" />
    </div>
  );
}
