import { BanknoteIcon, CoinsIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router-dom";
import AppHeader from "src/components/AppHeader";
import {
  CashStatusBadge,
  CashStatusLegend,
} from "src/components/cash/CashStatusBadge";
import {
  CashFlowChart,
  CashOutstandingChart,
} from "src/components/cash/CashFlowChart";
import { CashKpis } from "src/components/cash/CashKpis";
import { CustomPagination } from "src/components/CustomPagination";
import EmptyState from "src/components/EmptyState";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { Badge } from "src/components/ui/badge";
import { Skeleton } from "src/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "src/components/ui/tabs";
import { LIST_CASH_ALLOCATIONS_LIMIT } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useCashClaims, useCashHubStats } from "src/hooks/useCashHub";
import { copyToClipboard } from "src/lib/clipboard";
import { CashSliceStatus, CashWalletClaim } from "src/types";
import { formatClaimDeadline } from "src/utils/cashWallet";
import { shortenMiddle } from "src/utils/nostr";

type Facet = CashSliceStatus | "";

// One row per BILL, expanding to its slices — a bill is the thing that
// circulates and the thing that gets archived, so it is what the KPIs and
// charts above are counting. The previous view grouped by wallet only on its
// unfiltered tab; this makes the grouping the actual shape of the list.
type BillGroup = {
  walletAppID: number;
  walletPubkey: string;
  cashToken?: string;
  archived: boolean;
  totalMloki: number;
  expiresAt?: number;
  slices: CashWalletClaim[];
};

function groupIntoBills(claims: CashWalletClaim[]): BillGroup[] {
  const order: number[] = [];
  const byWallet = new Map<number, BillGroup>();
  for (const c of claims) {
    let group = byWallet.get(c.wallet_app_id);
    if (!group) {
      group = {
        walletAppID: c.wallet_app_id,
        walletPubkey: c.wallet_pubkey ?? "",
        cashToken: c.cash_token,
        archived: c.archived,
        totalMloki: 0,
        expiresAt: c.expires_at,
        slices: [],
      };
      byWallet.set(c.wallet_app_id, group);
      order.push(c.wallet_app_id);
    }
    group.totalMloki += c.amount_mloki;
    group.slices.push(c);
  }
  return order.map((id) => byWallet.get(id) as BillGroup);
}

function BillRow({ bill }: { bill: BillGroup }) {
  const { t } = useTranslation("circles");
  const [expanded, setExpanded] = React.useState(false);
  // A bill need not have a deadline at all (a hub may issue with no expiry),
  // and an archived one's is historical rather than actionable.
  const deadline =
    bill.expiresAt !== undefined ? formatClaimDeadline(bill.expiresAt) : undefined;

  // An archived bill has no cash token — deliberately, since a token for a
  // destroyed bill would look spendable. Its pubkey is what an operator
  // correlates with logs, so that is shown instead.
  const identifier = bill.cashToken
    ? shortenMiddle(bill.cashToken, 14, 6)
    : shortenMiddle(bill.walletPubkey, 10, 6);

  return (
    <div className="border-b last:border-b-0 py-3">
      <div className="flex items-center gap-3 min-w-0">
        <div className="rounded-full bg-primary/10 text-primary p-2 shrink-0">
          <CoinsIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <button
            type="button"
            className="font-mono text-sm truncate hover:underline text-start"
            onClick={() =>
              copyToClipboard(bill.cashToken || bill.walletPubkey)
            }
          >
            {identifier}
          </button>
          <div className="flex flex-wrap items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground">
              {t("cashBills.recipients", { count: bill.slices.length })}
            </span>
            {deadline?.label && (
              <span className="text-xs text-muted-foreground" title={deadline.title}>
                {deadline.label}
              </span>
            )}
            {bill.archived && (
              <Badge variant="outline" className="text-[11px]">
                {t("cashBills.archived")}
              </Badge>
            )}
          </div>
        </div>
        <div className="text-end shrink-0 slashed-zero">
          <p className="font-medium sensitive">
            <FormattedFlokicoinAmount amount={bill.totalMloki} />
          </p>
          {bill.slices.length > 1 ? (
            <button
              type="button"
              className="text-xs text-muted-foreground hover:text-foreground"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? t("cashBills.hideSlices") : t("cashBills.showSlices")}
            </button>
          ) : (
            <CashStatusBadge status={bill.slices[0].status} />
          )}
        </div>
      </div>

      {expanded && (
        <div className="mt-2 ps-11 grid gap-1.5">
          {bill.slices.map((s) => (
            <div
              key={`${s.archived}-${s.id}`}
              className="flex items-center justify-between gap-3 text-sm min-w-0"
            >
              <span className="font-mono text-xs text-muted-foreground truncate">
                {shortenMiddle(s.identity_value, 8, 6)}
              </span>
              <div className="flex items-center gap-2 shrink-0 slashed-zero">
                <span className="sensitive">
                  <FormattedFlokicoinAmount amount={s.amount_mloki} />
                </span>
                <CashStatusBadge status={s.status} />
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function CashHubDashboard() {
  const { id } = useParams() as { id: string };
  const hubId = Number(id);
  const { t } = useTranslation("circles");

  const [facet, setFacet] = React.useState<Facet>("");
  const [page, setPage] = React.useState(1);
  const listRef = React.useRef<HTMLDivElement>(null);

  const { data: hub } = useApp(hubId);
  const { data: stats } = useCashHubStats(hubId);
  const { data: claims, isLoading } = useCashClaims({
    hubId,
    limit: LIST_CASH_ALLOCATIONS_LIMIT,
    offset: (page - 1) * LIST_CASH_ALLOCATIONS_LIMIT,
    status: facet,
  });

  const counts = claims?.counts;
  const facets: { value: Facet; label: string; count: number }[] = [
    { value: "", label: t("cashHubAllocations.statusAll"), count: counts?.all ?? 0 },
    { value: "unclaimed", label: t("cashStatus.unclaimed.label"), count: counts?.unclaimed ?? 0 },
    { value: "redeemed", label: t("cashStatus.redeemed.label"), count: counts?.redeemed ?? 0 },
    { value: "split", label: t("cashStatus.split.label"), count: counts?.split ?? 0 },
    { value: "expired", label: t("cashStatus.expired.label"), count: counts?.expired ?? 0 },
    { value: "reclaimed", label: t("cashStatus.reclaimed.label"), count: counts?.reclaimed ?? 0 },
  ];

  const bills = groupIntoBills(claims?.claims ?? []);

  if (!hub || !stats) {
    return <Loading />;
  }

  return (
    <>
      <AppHeader title={hub.name} description={t("cashBills.dashboardSubtitle")} />

      <div className="grid gap-5">
        <CashKpis stats={stats} />

        <div className="grid grid-cols-1 lg:grid-cols-2 gap-5 items-start">
          <CashOutstandingChart daily={stats.daily} />
          <CashFlowChart daily={stats.daily} />
        </div>

        <div ref={listRef} className="flex flex-col flex-1">
          <div className="flex items-center gap-2 mb-2 overflow-x-auto">
            <Tabs
              value={facet}
              onValueChange={(v) => {
                setFacet(v as Facet);
                setPage(1);
              }}
            >
              <TabsList>
                {facets.map((f) => (
                  <TabsTrigger
                    key={f.value || "all"}
                    value={f.value}
                    className="shrink-0 gap-1.5"
                  >
                    {f.label}
                    <Badge
                      variant={
                        f.value === "expired" && f.count > 0
                          ? "warning"
                          : "secondary"
                      }
                      className="px-1.5 py-0 text-[11px] font-normal tabular-nums"
                    >
                      {f.count}
                    </Badge>
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
            {/* Once per view, never per row. */}
            <CashStatusLegend />
          </div>

          {isLoading && !claims ? (
            <div className="grid gap-3 py-2">
              {Array.from({ length: 4 }).map((_, i) => (
                <div key={i} className="flex items-center gap-3">
                  <Skeleton className="h-8 w-8 shrink-0 rounded-full" />
                  <Skeleton className="h-5 flex-1" />
                  <Skeleton className="h-5 w-24" />
                </div>
              ))}
            </div>
          ) : !bills.length ? (
            <EmptyState
              icon={BanknoteIcon}
              title={t("cashBills.emptyTitle")}
              description={t("cashBills.emptyDescription")}
              buttonText=""
              buttonLink=""
              showButton={false}
              showBorder={false}
            />
          ) : (
            <>
              {bills.map((b) => (
                <BillRow key={`${b.archived}-${b.walletAppID}`} bill={b} />
              ))}
              <CustomPagination
                limit={LIST_CASH_ALLOCATIONS_LIMIT}
                totalCount={claims?.totalCount ?? 0}
                page={page}
                handlePageChange={(p) => {
                  setPage(p);
                  listRef.current?.scrollIntoView({
                    behavior: "smooth",
                    block: "start",
                  });
                }}
              />
            </>
          )}
        </div>
      </div>
    </>
  );
}
