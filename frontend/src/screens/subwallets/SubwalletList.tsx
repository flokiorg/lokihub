import {
  ChevronDownIcon,
  CirclePlusIcon,
  HelpCircle,
  SearchIcon,
  ShieldCheckIcon,
  TriangleAlert,
  TriangleAlertIcon,
} from "lucide-react";
import { useMemo, useRef, useState } from "react";
import AppHeader from "src/components/AppHeader";
import AppCard from "src/components/connections/AppCard";
import { CircleCard } from "src/components/circles/CircleCard";
import { CustomPagination } from "src/components/CustomPagination";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import { SubWalletInfoDialog } from "src/components/SubWalletInfoDialog";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import { LinkButton } from "src/components/ui/custom/link-button";
import { Fab } from "src/components/redesign/Fab";
import { LIST_APPS_LIMIT, SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useBalances } from "src/hooks/useBalances";
import { useHubInfo } from "src/hooks/useHubInfo";
import { useFiatRate } from "src/hooks/useFiatRate";
import { useInfo } from "src/hooks/useInfo";
import { useListings } from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { SubwalletIntro } from "src/screens/subwallets/SubwalletIntro";

type CircleFilter = "all" | "accepting" | "listed";
type CircleSort = "newest" | "name" | "held";

export function SubwalletList() {
  const { data: info } = useInfo();
  const { data: fiatRate, error: fiatRateError } = useFiatRate();
  const hasFiat = info?.currency !== "LOKI" && !fiatRateError && !!fiatRate;
  const { data: hubInfo } = useHubInfo();
  const chainName = hubInfo?.nativeAsset?.name ?? "Flokicoin";
  const chainNameLower = chainName.toLowerCase();
  const [page, setPage] = useState(1);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<CircleFilter>("all");
  const [sort, setSort] = useState<CircleSort>("newest");
  const appsListRef = useRef<HTMLDivElement>(null);
  const { data: appsData } = useApps(
    undefined,
    page,
    {
      appStoreAppId: SUBWALLET_APPSTORE_APP_ID,
    },
    "created_at"
  );
  const { data: balances } = useBalances();
  // One request (SWR-deduped) tells us which circles are listed on the network,
  // powering the "Listed" filter without a per-row fetch.
  const { data: listings } = useListings();
  const listedHubIds = useMemo(
    () =>
      new Set(
        (listings ?? [])
          .filter((l) => l.phase !== "deleted" && l.status !== "closed")
          .map((l) => l.hubAppId)
      ),
    [listings]
  );

  const handlePageChange = (page: number) => {
    setPage(page);
    appsListRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "start",
    });
  };

  if (!info || !appsData || !balances || !balances) {
    return <Loading />;
  }

  // cash_hub apps have their own dedicated top-level page (/cash-hub) now —
  // excluded here (and from this screen's own aggregate stats below) so
  // they're not double-listed.
  const subwalletApps = appsData.apps.filter((app) => app.kind !== "cash_hub");

  if (!subwalletApps.length) {
    return <SubwalletIntro />;
  }

  const subwalletTotalAmount =
    subwalletApps.reduce((total, app) => total + app.balance, 0) || 0;
  const isSufficientlyBacked =
    subwalletTotalAmount <= balances.lightning.totalSpendable;
  const backingShortfall =
    subwalletTotalAmount - balances.lightning.totalSpendable;

  // Toolbar: search by name, filter (all / accepting / listed), sort. Mirrors
  // the Cash Hubs list toolbar so the two lists operate identically.
  const acceptingCount = subwalletApps.filter((a) => a.circleAccepting).length;
  const listedCount = subwalletApps.filter((a) =>
    listedHubIds.has(a.id)
  ).length;
  const q = query.trim().toLowerCase();
  const visibleApps = subwalletApps
    .filter((a) => (q ? a.name.toLowerCase().includes(q) : true))
    .filter((a) =>
      filter === "accepting"
        ? a.circleAccepting
        : filter === "listed"
          ? listedHubIds.has(a.id)
          : true
    )
    .sort((a, b) => {
      if (sort === "name") {
        return a.name.localeCompare(b.name);
      }
      if (sort === "held") {
        return b.balance - a.balance;
      }
      return 0; // "newest" keeps the server's created_at order
    });

  const filters: { key: CircleFilter; label: string; count: number }[] = [
    { key: "all", label: "All", count: subwalletApps.length },
    { key: "accepting", label: "Accepting", count: acceptingCount },
    { key: "listed", label: "Listed", count: listedCount },
  ];

  return (
    <div className="grid gap-4">
      <AppHeader
        title="Circles"
        description="Shared wallets you host for a group — members request their own and pay each other instantly, for free."
        contentRight={
          <>
            <SubWalletInfoDialog
              trigger={
                <Button variant="outline" size="icon">
                  <HelpCircle className="size-4" />
                </Button>
              }
            />
            {/* On mobile the bottom FAB is the primary "New Circle" action, so
                the header button is hidden there to avoid a duplicate (design
                rule: the FAB replaces the header action on narrow widths). */}
            <div className="hidden sm:block">
              <ResponsiveLinkButton
                to="/sub-wallets/new"
                icon={CirclePlusIcon}
                text="New Circle"
              />
            </div>
          </>
        }
      />

      {!isSufficientlyBacked && (
        <Alert variant="warning">
          <TriangleAlert />
          <AlertTitle>
            Sub-wallets you manage are insufficiently backed
          </AlertTitle>
          <AlertDescription className="flex flex-col gap-3">
            <div className="flex flex-row flex-wrap items-center gap-x-2">
              There's not enough {chainNameLower} in your spending balance to
              honor all balances of sub-wallets under your management. Increase
              spending capacity by opening a channel or review your channel
              statuses to back them up again.
              <LinkButton to="/wallet/receive" variant="secondary">
                Deposit {chainName}
              </LinkButton>
            </div>
            <div className="text-sm">
              Your spendable balance is{" "}
              <span className="font-medium sensitive">
                <FormattedFlokicoinAmount
                  amount={balances.lightning.totalSpendable}
                />
              </span>
              , which is{" "}
              <span className="font-medium sensitive">
                <FormattedFlokicoinAmount amount={backingShortfall} />
              </span>{" "}
              short of the total sub-wallet balance.
            </div>
          </AlertDescription>
        </Alert>
      )}

      {/* Hero: what the node holds across its circles (with a top-up control)
          beside a backing panel that says — at a glance — whether every circle
          wallet is covered by the node's spendable balance. Mirrors the cash /
          circle detail heroes. */}
      <div className="slashed-zero grid overflow-hidden rounded-xl border bg-card shadow-sm sm:grid-cols-[1.3fr_1fr]">
        <div className="min-w-0 p-5 sm:p-6">
          <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Hosted for members ·{" "}
            {subwalletApps.length === 1
              ? "1 circle"
              : `${subwalletApps.length} circles`}
          </div>
          <div className="balance sensitive mt-1 text-2xl font-bold">
            <FormattedFlokicoinAmount amount={subwalletTotalAmount} />
          </div>
          <div className="text-muted-foreground mt-1 text-xs">
            {hasFiat && (
              <>
                <FormattedFiatAmount amount={subwalletTotalAmount / 1000} />{" "}
              </>
            )}
            held across your circles
          </div>
          <div className="mt-3">
            <LinkButton to="/wallet/receive" variant="outline" size="sm">
              <CirclePlusIcon />
              Add funds
            </LinkButton>
          </div>
        </div>
        {/* Tinted right half — backing coverage, divided by the hairline. */}
        <div className="min-w-0 border-t bg-muted/40 p-5 sm:border-s sm:border-t-0 sm:p-6">
          <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Backing
          </div>
          <div className="mt-1 flex items-center gap-1.5 text-sm font-medium">
            {isSufficientlyBacked ? (
              <>
                <ShieldCheckIcon className="size-4 text-positive-foreground" />
                <span className="text-positive-foreground">Backed</span>
              </>
            ) : (
              <>
                <TriangleAlertIcon className="size-4 text-warning-foreground" />
                <span className="text-warning-foreground">
                  Insufficiently backed
                </span>
              </>
            )}
          </div>
          {/* GREEN when fully backed, amber when short — not the default meter. */}
          <div className="bg-muted mt-2 h-2 w-full overflow-hidden rounded-full">
            <div
              className={cn(
                "h-full rounded-full transition-all",
                isSufficientlyBacked
                  ? "bg-positive-foreground"
                  : "bg-warning-foreground"
              )}
              style={{
                width: `${
                  subwalletTotalAmount > 0
                    ? Math.max(
                        0,
                        Math.min(
                          100,
                          (balances.lightning.totalSpendable /
                            subwalletTotalAmount) *
                            100
                        )
                      )
                    : 100
                }%`,
              }}
            />
          </div>
          <div className="text-muted-foreground mt-2 text-xs">
            {isSufficientlyBacked
              ? "Every circle wallet is covered by node balance"
              : "Circle wallets exceed your spendable balance"}
          </div>
        </div>
      </div>
      {/* Toolbar: search · All/Accepting/Listed · sort — matches the Cash Hubs
          list so both top-level lists operate the same way. */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-[180px] flex-1 items-center gap-2 rounded-lg border bg-card px-3 py-2">
          <SearchIcon className="size-4 shrink-0 text-muted-foreground" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search circles"
            className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {filters.map((f) => (
            <button
              key={f.key}
              type="button"
              onClick={() => setFilter(f.key)}
              className={cn(
                "rounded-full border px-3 py-1.5 text-sm font-medium transition-colors",
                filter === f.key
                  ? "border-primary/40 bg-primary/15 text-foreground"
                  : "border-border bg-card text-muted-foreground hover:text-foreground"
              )}
            >
              {f.label}
              <span className="ms-1.5 text-xs text-muted-foreground slashed-zero">
                {f.count}
              </span>
            </button>
          ))}
        </div>
        <div className="relative">
          <select
            value={sort}
            onChange={(e) => setSort(e.target.value as CircleSort)}
            className="appearance-none rounded-lg border bg-card py-2 ps-3 pe-9 text-sm text-foreground"
            aria-label="Sort circles"
          >
            <option value="newest">Sort: Newest</option>
            <option value="name">Sort: Name</option>
            <option value="held">Sort: Held</option>
          </select>
          <ChevronDownIcon className="pointer-events-none absolute end-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        </div>
      </div>

      <div className="mt-1">
        <div ref={appsListRef} className="app-list flex flex-col gap-3">
          {visibleApps.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No circles match your filter.
            </p>
          ) : (
            visibleApps.map((app, index) =>
              app.kind === "circle_hub" ? (
                <CircleCard key={app.id} app={app} />
              ) : (
                // Non-circle sub-wallets render as a bordered AppCard; the
                // list's own gap gives them spacing from their neighbours.
                <div key={index}>
                  <AppCard app={app} />
                </div>
              )
            )
          )}
        </div>
      </div>

      <CustomPagination
        limit={LIST_APPS_LIMIT}
        totalCount={appsData.totalCount}
        page={page}
        handlePageChange={handlePageChange}
      />

      {/* Mobile thumb-first primary action (desktop uses the header button). */}
      <Fab to="/sub-wallets/new" icon={CirclePlusIcon} label="New Circle" />
    </div>
  );
}
