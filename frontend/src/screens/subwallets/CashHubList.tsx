import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { BanknoteIcon, ChevronRightIcon, CirclePlusIcon } from "lucide-react";
import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import { CustomPagination } from "src/components/CustomPagination";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import { Card, CardTitle } from "src/components/ui/card";
import { LIST_APPS_LIMIT, SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useApps } from "src/hooks/useApps";
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
  const { data: info } = useInfo();
  const [page, setPage] = useState(1);
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
  const cashHubTotalAmount = cashHubApps.reduce(
    (total, app) => total + app.balance,
    0
  );

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

      <div className="flex flex-wrap items-center gap-x-6 gap-y-1 px-1 text-sm slashed-zero">
        <div className="flex items-baseline gap-1.5">
          <span className="text-muted-foreground">Total minted balance</span>
          <span className="font-medium sensitive">
            <FormattedFlokicoinAmount amount={cashHubTotalAmount} />
          </span>
        </div>
        <div className="flex items-baseline gap-1.5">
          <span className="text-muted-foreground">Hubs</span>
          <span className="font-medium">{cashHubApps.length}</span>
        </div>
      </div>

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
