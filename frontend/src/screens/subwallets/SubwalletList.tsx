import {
  CirclePlusIcon,
  HelpCircle,
  ShieldCheckIcon,
  TriangleAlert,
  TriangleAlertIcon,
} from "lucide-react";
import { useRef, useState } from "react";
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
import { LIST_APPS_LIMIT, SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useBalances } from "src/hooks/useBalances";
import { useHubInfo } from "src/hooks/useHubInfo";
import { useInfo } from "src/hooks/useInfo";
import { SubwalletIntro } from "src/screens/subwallets/SubwalletIntro";

export function SubwalletList() {
  const { data: info } = useInfo();
  const { data: hubInfo } = useHubInfo();
  const chainName = hubInfo?.nativeAsset?.name ?? "Flokicoin";
  const chainNameLower = chainName.toLowerCase();
  const [page, setPage] = useState(1);
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

  return (
    <div className="grid gap-4">
      <AppHeader
        title="Sub-wallets"
        description="Create sub-wallets for yourself, friends, family or coworkers"
        contentRight={
          <>
            <SubWalletInfoDialog
              trigger={
                <Button variant="outline" size="icon">
                  <HelpCircle className="size-4" />
                </Button>
              }
            />
            <ResponsiveLinkButton
              to="/sub-wallets/new"
              icon={CirclePlusIcon}
              text="New Sub-wallet"
            />
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

      <div className="slashed-zero rounded-xl border bg-card p-5">
        <div className="grid grid-cols-1 gap-5 sm:grid-cols-3">
          <div>
            <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Total balance
            </div>
            <div className="balance sensitive mt-1 text-2xl font-bold">
              <FormattedFlokicoinAmount amount={subwalletTotalAmount} />
            </div>
            <FormattedFiatAmount amount={subwalletTotalAmount / 1000} />
          </div>
          <div>
            <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Sub-wallets
            </div>
            <div className="mt-1 text-2xl font-bold">
              {subwalletApps.length}
            </div>
          </div>
          <div>
            <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Backing
            </div>
            <div className="mt-1 flex items-center gap-1.5 text-sm font-medium">
              {isSufficientlyBacked ? (
                <>
                  <ShieldCheckIcon className="size-4 text-positive-foreground" />
                  <span className="text-positive-foreground">Fully backed</span>
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
          </div>
        </div>
      </div>
      <div className="mt-6">
        <h3 className="mb-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
          Managed Sub-wallets
        </h3>
        <div ref={appsListRef} className="app-list flex flex-col gap-3">
          {subwalletApps.map((app, index) =>
            app.kind === "circle_hub" ? (
              <CircleCard key={app.id} app={app} />
            ) : (
              // Non-circle sub-wallets render as a bordered AppCard; the
              // list's own gap gives them spacing from their neighbours.
              <div key={index}>
                <AppCard app={app} />
              </div>
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
    </div>
  );
}
