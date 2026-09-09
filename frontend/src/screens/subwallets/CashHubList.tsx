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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "src/components/ui/table";
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
          description="Mint spend-only Lokicash for beneficiaries, paid out on demand from your own balance"
        />
        <Card className="flex flex-col items-center gap-4 p-8 text-center">
          <BanknoteIcon className="size-10 text-muted-foreground" />
          <CardTitle className="text-lg">No Cash Hubs yet</CardTitle>
          <p className="max-w-md text-sm text-muted-foreground">
            A Cash Hub lets you mint spend-only Lokicash for one or more
            recipients in one step — they redeem their own share whenever
            they're ready, or transfer/split it on to someone else.
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
        description="Mint spend-only Lokicash for beneficiaries, paid out on demand from your own balance"
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

      <div ref={appsListRef}>
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="text-muted-foreground">Hub</TableHead>
                <TableHead className="text-muted-foreground">
                  Balance
                </TableHead>
                <TableHead className="text-muted-foreground">
                  Last activity
                </TableHead>
                <TableHead className="w-px" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {cashHubApps.map((app) => (
                <TableRow key={app.id} className="cursor-pointer">
                  <TableCell className="p-0">
                    <Link
                      to={`/apps/${app.id}`}
                      className="flex items-center gap-3 px-4 py-3"
                    >
                      <AppAvatar app={app} className="size-8 shrink-0" />
                      <span className="font-medium">{app.name}</span>
                    </Link>
                  </TableCell>
                  <TableCell>
                    <span className="sensitive">
                      <FormattedFlokicoinAmount amount={app.balance} />
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {app.lastUsedAt
                      ? dayjs(app.lastUsedAt).fromNow()
                      : "Never"}
                  </TableCell>
                  <TableCell>
                    <Link to={`/apps/${app.id}`}>
                      <ChevronRightIcon className="size-4 text-muted-foreground" />
                    </Link>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
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
