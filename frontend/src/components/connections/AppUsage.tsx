
import {
  CircleMinusIcon,
  CirclePlusIcon
} from "lucide-react";
import React from "react";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { IsolatedAppDrawDownDialog } from "src/components/IsolatedAppDrawDownDialog";
import { IsolatedAppTopupDialog } from "src/components/IsolatedAppTopupDialog";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Progress } from "src/components/ui/progress";
import { useTransactions } from "src/hooks/useTransactions";
import { getBudgetRenewalLabel } from "src/lib/utils";
import { useTranslation } from "react-i18next";
import { App, Transaction } from "src/types";

export function AppUsage({ app }: { app: App }) {
  const { t } = useTranslation("apps");
  const [page, setPage] = React.useState(1);
  const { data: transactionsResponse } = useTransactions(
    app.id,
    false,
    100,
    page,
    `${app.updatedAt}-${app.balance}`
  );
  const [allTransactions, setAllTransactions] = React.useState<Transaction[]>(
    []
  );
  // Track the last page we fetched to detect fresh data vs pagination
  const [lastFetchedPage, setLastFetchedPage] = React.useState(0);

  React.useEffect(() => {
    if (transactionsResponse?.transactions.length) {
      if (page === 1) {
        // Fresh fetch (first page) - replace all transactions
        setAllTransactions(transactionsResponse.transactions);
      } else {
        // Pagination - append and deduplicate
        setAllTransactions((current) =>
          [...current, ...transactionsResponse.transactions].filter(
            (v, i, a) => a.findIndex((t) => t.paymentHash === v.paymentHash) === i
          )
        );
      }
      // Only advance page if we haven't fetched this page yet
      if (page > lastFetchedPage) {
        setLastFetchedPage(page);
        setPage((current) => current + 1);
      }
    }
  }, [transactionsResponse?.transactions, page, lastFetchedPage]);

  const totalSpent = allTransactions
    .filter((tx) => tx.type === "outgoing" && tx.state === "settled")
    .map((tx) => Math.floor(tx.amount / 1000))
    .reduce((a, b) => a + b, 0);

  const totalReceived = allTransactions
    .filter((tx) => tx.type === "incoming")
    .map((tx) => Math.floor(tx.amount / 1000))
    .reduce((a, b) => a + b, 0);



  return (
    <>
      {app.isolated && (
        <div className="grid grid-cols-1 gap-2 slashed-zero">
          <Card className="justify-between">
            <CardHeader className="pb-2">
              <CardTitle className="text-lg">{t("usage.isolatedBalance", "Isolated Balance")}</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="flex flex-wrap justify-between items-center sm:items-end gap-4">
                <div>
                  <p className="font-medium text-2xl balance sensitive">
                    <FormattedFlokicoinAmount amount={app.balance} />
                  </p>
                  <FormattedFiatAmount
                    amount={Math.floor(app.balance / 1000)}
                  />
                </div>
                {/* jit_wallet/circle_wallet balances only move via their hub's
                    allocation transfer and the wallet's own spend — manual
                    admin adjustment here would bypass that accounting. */}
                {app.kind !== "jit_wallet" && app.kind !== "circle_wallet" && (
                  <div className="flex flex-wrap gap-2 items-center">
                    {app.balance > 0 && (
                      <IsolatedAppDrawDownDialog appId={app.id}>
                        <Button size="sm" variant="outline">
                          <CircleMinusIcon />
                          {t("usage.decrease", "Decrease")}
                        </Button>
                      </IsolatedAppDrawDownDialog>
                    )}
                    <IsolatedAppTopupDialog appId={app.id}>
                      <Button size="sm" variant="outline">
                        <CirclePlusIcon />
                        {t("usage.increase", "Increase")}
                      </Button>
                    </IsolatedAppTopupDialog>
                  </div>
                )}
              </div>
            </CardContent>
          </Card>

        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-2 slashed-zero">
        <Card className="flex flex-1 flex-col">
          <CardHeader className="pb-2">
            <CardTitle className="text-lg">{t("usage.totalSpent", "Total Spent")}</CardTitle>
          </CardHeader>
          <CardContent className="grow">
            <div className="mb-1">
              <span className="text-2xl font-medium balance sensitive">
                <FormattedFlokicoinAmount amount={totalSpent * 1000} />
              </span>
            </div>
            <FormattedFiatAmount amount={totalSpent} />
          </CardContent>
        </Card>
        <Card className="flex flex-1 flex-col">
          <CardHeader className="pb-2">
            <CardTitle className="text-lg">{t("usage.totalReceived", "Total Received")}</CardTitle>
          </CardHeader>
          <CardContent className="grow">
            <div className="mb-1">
              <span className="text-2xl font-medium balance sensitive">
                <FormattedFlokicoinAmount amount={totalReceived * 1000} />
              </span>
            </div>
            <FormattedFiatAmount amount={totalReceived} />
          </CardContent>
        </Card>
      </div>

      {app.maxAmount > 0 && (
        <Card className="slashed-zero">
          <CardHeader className="pb-2">
            <CardTitle className="text-lg">{t("usage.budget", "Budget")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-row justify-between mb-2">
              <div>
                <p className="text-xs text-secondary-foreground font-medium">
                  {t("usage.leftInBudget", "Left in budget")}
                </p>
                <p className="text-xl font-medium balance sensitive">
                  <FormattedFlokicoinAmount
                    amount={(app.maxAmount - app.budgetUsage) * 1000}
                  />
                </p>
                <FormattedFiatAmount amount={app.maxAmount - app.budgetUsage} />
              </div>
              <div>
                <p className="text-xs text-secondary-foreground font-medium">
                  {t("usage.budgetRenewal", "Budget renewal")}
                </p>
                <p className="text-xl font-medium balance sensitive">
                  <FormattedFlokicoinAmount amount={app.maxAmount * 1000} />
                  {app.budgetRenewal !== "never" && (
                    <> / {getBudgetRenewalLabel(app.budgetRenewal)}</>
                  )}
                </p>
                <FormattedFiatAmount amount={app.maxAmount} />
              </div>
            </div>
            <Progress
              className="h-4"
              value={100 - (app.budgetUsage * 100) / app.maxAmount}
            />
          </CardContent>
        </Card>
      )}
    </>
  );
}
