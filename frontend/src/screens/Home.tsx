import {
  Activity,
  ArrowDownLeft,
  ArrowUpRight,
  Banknote,
  ChevronRight,
  Megaphone,
  Plug,
  Users,
  Zap,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import AppHeader from "src/components/AppHeader";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { BlockHeightWidget } from "src/components/home/widgets/BlockHeightWidget";
import { ForwardsWidget } from "src/components/home/widgets/ForwardsWidget";
import { NodeStatusWidget } from "src/components/home/widgets/NodeStatusWidget";
import { OnchainFeesWidget } from "src/components/home/widgets/OnchainFeesWidget";
import Loading from "src/components/Loading";
import { Button } from "src/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "src/components/ui/sheet";
import { useAllCashHubStats } from "src/hooks/useCashHub";
import { useApps } from "src/hooks/useApps";
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import { useListings } from "src/hooks/useListings";
import { useTransactions } from "src/hooks/useTransactions";
import OnboardingChecklist from "src/screens/wallet/OnboardingChecklist";

import AppAlert from "src/components/home/alerts/AppAlert";
import { localStorageKeys } from "src/constants";
import { useAppStore } from "src/hooks/useAppStore";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function getGreeting(name: string | undefined, t: (...args: any[]) => string) {
  const hours = new Date().getHours();
  let greeting;

  if (hours < 11) {
    greeting = t("greeting.morning");
  } else if (hours < 16) {
    greeting = t("greeting.afternoon");
  } else {
    greeting = t("greeting.evening");
  }

  return `${greeting}${name ? `, ${name}` : ""}!`;
}

function DashboardAlerts() {
  const { apps, loading } = useAppStore();
  const [dismissed, setDismissed] = React.useState<string[]>(() => {
    try {
      const stored = localStorage.getItem(
        localStorageKeys.appAlertsHiddenUntil
      );
      return stored ? JSON.parse(stored) : [];
    } catch {
      return [];
    }
  });

  const handleDismiss = (appId: string) => {
    const newDismissed = [...dismissed, appId];
    setDismissed(newDismissed);
    localStorage.setItem(
      localStorageKeys.appAlertsHiddenUntil,
      JSON.stringify(newDismissed)
    );
  };

  if (loading) return null;

  const now = Date.now() / 1000; // API returns seconds
  const dayInSeconds = 24 * 60 * 60;
  const fiveDaysAgo = now - 5 * dayInSeconds;

  // Ensure apps is an array to prevent filter errors
  const safeApps = Array.isArray(apps) ? apps : [];

  const newApps = safeApps
    .filter((app) => app.createdAt > fiveDaysAgo && !dismissed.includes(app.id))
    .sort((a, b) => b.createdAt - a.createdAt);

  const updatedApps = safeApps
    .filter(
      (app) =>
        app.updatedAt > fiveDaysAgo &&
        app.createdAt <= fiveDaysAgo && // Don't show as updated if it's new
        !dismissed.includes(app.id)
    )
    .sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <>
      {newApps.map((app) => (
        <AppAlert key={app.id} app={app} type="new" onDismiss={handleDismiss} />
      ))}
      {updatedApps.map((app) => (
        <AppAlert
          key={app.id}
          app={app}
          type="updated"
          onDismiss={handleDismiss}
        />
      ))}
    </>
  );
}

function StatTile({
  label,
  children,
  icon,
}: {
  label: string;
  children: React.ReactNode;
  icon?: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {icon}
        {label}
      </div>
      <div className="mt-1 text-2xl font-bold tabular-nums">{children}</div>
    </div>
  );
}

function Shortcut({
  to,
  icon,
  title,
  sub,
  tint,
}: {
  to: string;
  icon: React.ReactNode;
  title: string;
  sub: string;
  tint?: "accent" | "teal";
}) {
  return (
    <Link
      to={to}
      className="flex items-center gap-2.5 rounded-xl border bg-card p-3 transition-colors hover:bg-accent"
    >
      <span
        className="flex size-9 flex-none items-center justify-center rounded-lg"
        style={
          tint === "teal"
            ? {
                background: "var(--color-series-covered)",
                color: "var(--background)",
              }
            : {
                background: "var(--primary)",
                color: "var(--primary-foreground)",
              }
        }
      >
        {icon}
      </span>
      <span className="min-w-0">
        <span className="block text-sm font-semibold leading-tight">
          {title}
        </span>
        <span className="block text-xs text-muted-foreground">{sub}</span>
      </span>
    </Link>
  );
}

function Home() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const { data: cashStats } = useAllCashHubStats();
  const { data: listings } = useListings();
  const { data: circleApps } = useApps(undefined, 1, {
    kind: "circle_hub",
    topLevelOnly: true,
  });
  const { data: txns } = useTransactions(undefined, true, 4);
  const { t } = useTranslation("home");
  const { t: tc } = useTranslation("common");

  const [isNerd, setNerd] = React.useState(false);
  if (!info || !balances) {
    return <Loading />;
  }

  const walletMloki = balances.lightning.totalSpendable;
  const cashMloki = cashStats?.balance_mloki ?? 0;
  const circleMloki = (circleApps?.apps ?? []).reduce(
    (sum, a) => sum + (a.balance || 0),
    0
  );
  const totalMloki = walletMloki + cashMloki + circleMloki;
  const pct = (v: number) => (totalMloki > 0 ? (v / totalMloki) * 100 : 0);

  const activeListings = (listings ?? []).filter((l) => l.phase !== "deleted");
  const recentTxns = txns?.transactions ?? [];

  const statusLabel = (status: string) =>
    status === "paused"
      ? t("dashboard.statusPaused")
      : status === "closed"
        ? t("dashboard.statusClosed")
        : t("dashboard.statusListed");

  return (
    <>
      <AppHeader
        title={getGreeting(undefined, t)}
        contentRight={
          <Button variant="outline" size="sm" onClick={() => setNerd(true)}>
            <Activity className="size-4" />
            {t("widgets.statsForNerds")}
          </Button>
        }
      />
      <div className="grid gap-5">
        <DashboardAlerts />
        <OnboardingChecklist />

        {/* Hero: the total, how it splits across wallet/cash/circles, and the
            two primary actions. */}
        <div className="flex flex-col gap-5 rounded-xl border bg-card p-5 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("dashboard.totalLabel")}
            </div>
            <div className="mt-1 text-4xl font-bold tabular-nums">
              <FormattedFlokicoinAmount amount={totalMloki} />
            </div>
            <div className="mt-4 flex h-2.5 overflow-hidden rounded-full bg-muted">
              <span
                style={{
                  width: `${pct(walletMloki)}%`,
                  background: "var(--primary)",
                }}
              />
              <span
                style={{
                  width: `${pct(cashMloki)}%`,
                  background: "var(--color-series-covered)",
                }}
              />
              <span
                style={{
                  width: `${pct(circleMloki)}%`,
                  background: "var(--color-series-info)",
                }}
              />
            </div>
            <div className="mt-3 flex flex-wrap gap-4 text-sm text-muted-foreground">
              <span className="inline-flex items-center gap-1.5">
                <i
                  className="size-2.5 rounded-sm"
                  style={{ background: "var(--primary)" }}
                />
                {t("dashboard.wallet")}{" "}
                <b className="font-semibold tabular-nums text-foreground">
                  <FormattedFlokicoinAmount amount={walletMloki} />
                </b>
              </span>
              <span className="inline-flex items-center gap-1.5">
                <i
                  className="size-2.5 rounded-sm"
                  style={{ background: "var(--color-series-covered)" }}
                />
                {t("dashboard.cashHubs")}{" "}
                <b className="font-semibold tabular-nums text-foreground">
                  <FormattedFlokicoinAmount amount={cashMloki} />
                </b>
              </span>
              <span className="inline-flex items-center gap-1.5">
                <i
                  className="size-2.5 rounded-sm"
                  style={{ background: "var(--color-series-info)" }}
                />
                {t("dashboard.circles")}{" "}
                <b className="font-semibold tabular-nums text-foreground">
                  <FormattedFlokicoinAmount amount={circleMloki} />
                </b>
              </span>
            </div>
          </div>
          <div className="flex gap-3 sm:shrink-0">
            <Button asChild className="flex-1 sm:flex-none">
              <Link to="/wallet/receive">
                <ArrowDownLeft className="size-4" />
                {t("dashboard.receive")}
              </Link>
            </Button>
            <Button asChild variant="outline" className="flex-1 sm:flex-none">
              <Link to="/wallet/send">
                <ArrowUpRight className="size-4" />
                {t("dashboard.send")}
              </Link>
            </Button>
          </div>
        </div>

        {/* Quick actions — a full-width shortcut row. */}
        <div className="grid gap-3 rounded-xl border bg-card p-4">
          <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("dashboard.quickActions")}
          </div>
          <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-4">
            <Shortcut
              to="/cash-hub"
              icon={<Zap className="size-4" />}
              title={t("dashboard.mint")}
              sub={t("dashboard.mintSub")}
            />
            <Shortcut
              to="/sub-wallets/new/circle"
              icon={<Users className="size-4" />}
              title={t("dashboard.newCircle")}
              sub={t("dashboard.newCircleSub")}
              tint="teal"
            />
            <Shortcut
              to="/listings"
              icon={<Megaphone className="size-4" />}
              title={t("dashboard.listOffer")}
              sub={t("dashboard.listOfferSub")}
            />
            <Shortcut
              to="/apps/new"
              icon={<Plug className="size-4" />}
              title={t("dashboard.connectApp")}
              sub={t("dashboard.connectAppSub")}
              tint="teal"
            />
          </div>
        </div>

        {/* Key figures. */}
        <div className="grid grid-cols-2 gap-5 rounded-xl border bg-card p-4 sm:grid-cols-3">
          <StatTile label={t("dashboard.canReceive")}>
            <FormattedFlokicoinAmount
              amount={balances.lightning.totalReceivable}
            />
          </StatTile>
          <StatTile
            label={t("dashboard.cashOutstanding")}
            icon={<Banknote className="size-3.5" />}
          >
            {cashStats?.outstanding_count ?? 0}{" "}
            <span className="text-xs font-normal text-muted-foreground">
              {t("dashboard.billsLive")}
            </span>
          </StatTile>
          <StatTile
            label={t("dashboard.listed")}
            icon={<Megaphone className="size-3.5" />}
          >
            {activeListings.length}
          </StatTile>
        </div>

        {/* What's listed + recent activity — each in its own bordered panel so
            the two columns read as distinct blocks, not one blurred row. */}
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2 lg:items-start">
          <section className="grid gap-3 rounded-xl border bg-card p-4">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold">{t("dashboard.whatsListed")}</h3>
              <Link
                to="/listings"
                className="inline-flex items-center gap-0.5 text-sm font-semibold text-primary"
              >
                {t("dashboard.manage")}
                <ChevronRight className="size-4" />
              </Link>
            </div>
            {activeListings.length === 0 ? (
              <div className="flex flex-col items-center gap-2 py-6 text-center">
                <Megaphone className="size-8 text-muted-foreground" />
                <p className="font-semibold">{t("dashboard.nothingListed")}</p>
                <p className="max-w-xs text-sm text-muted-foreground">
                  {t("dashboard.nothingListedHint")}
                </p>
                <Button asChild variant="secondary" size="sm">
                  <Link to="/listings">{t("dashboard.listSomething")}</Link>
                </Button>
              </div>
            ) : (
              <div className="grid gap-2">
                {activeListings.slice(0, 4).map((l) => (
                  <Link
                    key={l.id}
                    to="/listings"
                    className="flex items-center gap-3 rounded-lg p-2 hover:bg-accent"
                  >
                    {l.type === "circle_wallet" ? (
                      <Users className="size-5 text-muted-foreground" />
                    ) : (
                      <Banknote className="size-5 text-muted-foreground" />
                    )}
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">
                        {l.title || tc("untitled", "Untitled")}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {t("dashboard.onRelays", {
                          ok: l.relaysOk,
                          total: l.relaysTotal,
                        })}
                      </div>
                    </div>
                    <span className="text-xs font-semibold text-muted-foreground">
                      {statusLabel(l.status)}
                    </span>
                  </Link>
                ))}
              </div>
            )}
          </section>

          <section className="grid gap-3 rounded-xl border bg-card p-4">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold">{t("dashboard.recentActivity")}</h3>
              <Link
                to="/wallet"
                className="inline-flex items-center gap-0.5 text-sm font-semibold text-primary"
              >
                {t("dashboard.all")}
                <ChevronRight className="size-4" />
              </Link>
            </div>
            {recentTxns.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted-foreground">
                {t("dashboard.noActivity")}
              </p>
            ) : (
              <div className="grid gap-1">
                {recentTxns.map((tx, i) => {
                  const incoming = tx.type === "incoming";
                  return (
                    <div
                      key={`${tx.paymentHash || "tx"}-${i}`}
                      className="flex items-center gap-3 rounded-lg p-2"
                    >
                      <span className="flex size-8 flex-none items-center justify-center rounded-full bg-muted text-muted-foreground">
                        {incoming ? (
                          <ArrowDownLeft className="size-4" />
                        ) : (
                          <ArrowUpRight className="size-4" />
                        )}
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="font-medium">
                          {incoming
                            ? t("dashboard.received")
                            : t("dashboard.sent")}
                        </div>
                        <div className="truncate text-xs text-muted-foreground">
                          {tx.description || ""}
                        </div>
                      </div>
                      <span
                        className="text-sm font-semibold tabular-nums"
                        style={
                          incoming
                            ? { color: "var(--color-series-covered)" }
                            : undefined
                        }
                      >
                        {incoming ? "+" : "−"}
                        <FormattedFlokicoinAmount amount={tx.amount} />
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </section>
        </div>

        {/* Chain / node metrics live in a right-hand drawer (opened from the
            header) rather than an inline section — the page is width-limited,
            and this keeps the dashboard focused. */}
        <Sheet open={isNerd} onOpenChange={setNerd}>
          <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{t("widgets.statsForNerds")}</SheetTitle>
            </SheetHeader>
            <div className="grid gap-5 px-4 pb-6">
              <NodeStatusWidget />
              <BlockHeightWidget />
              <OnchainFeesWidget />
              <ForwardsWidget />
            </div>
          </SheetContent>
        </Sheet>
      </div>
    </>
  );
}

export default Home;
