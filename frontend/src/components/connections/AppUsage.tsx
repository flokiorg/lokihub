
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
import { App, Transaction } from "src/types";

export function AppUsage({ app }: { app: App }) {
  const [page, setPage] = React.useState(1);
  const { data: transactionsResponse } = useTransactions(
    app.id,
    false,
    100,
    page
  );
  const [allTransactions, setAllTransactions] = React.useState<Transaction[]>(
    []
  );
  React.useEffect(() => {
    if (transactionsResponse?.transactions.length) {
      setAllTransactions((current) =>
        [...current, ...transactionsResponse.transactions].filter(
          (v, i, a) => a.findIndex((t) => t.paymentHash === v.paymentHash) === i // remove duplicates
        )
      );
      setPage((current) => current + 1);
    }
  }, [transactionsResponse?.transactions]);

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
        <div className="grid grid-cols-1 gap-2">
          <Card className="justify-between">
            <CardHeader>
              <CardTitle>Isolated Balance</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="flex justify-between items-end">
                <div>
                  <p className="font-medium text-2xl">
                    <FormattedFlokicoinAmount amount={app.balance} />
                  </p>
                  <FormattedFiatAmount
                    amount={Math.floor(app.balance / 1000)}
                  />
                </div>
                <div className="flex gap-2 items-center">
                  {app.balance > 0 && (
                    <IsolatedAppDrawDownDialog appId={app.id}>
                      <Button size="sm" variant="outline">
                        <CircleMinusIcon />
                        Decrease
                      </Button>
                    </IsolatedAppDrawDownDialog>
                  )}
                  <IsolatedAppTopupDialog appId={app.id}>
                    <Button size="sm" variant="outline">
                      <CirclePlusIcon />
                      Increase
                    </Button>
                  </IsolatedAppTopupDialog>
                </div>
              </div>
            </CardContent>
          </Card>

        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-2">
        <Card>
          <CardHeader>
            <CardTitle>Total Spent</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="font-medium text-2xl">
              <FormattedFlokicoinAmount amount={totalSpent * 1000} />
            </p>
            <FormattedFiatAmount amount={totalSpent} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Total Received</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="font-medium text-2xl">
              <FormattedFlokicoinAmount amount={totalReceived * 1000} />
            </p>
            <FormattedFiatAmount amount={totalReceived} />
          </CardContent>
        </Card>
      </div>

      {app.maxAmount > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Budget</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-row justify-between mb-2">
              <div>
                <p className="text-xs text-secondary-foreground font-medium">
                  Left in budget
                </p>
                <p className="text-xl font-medium">
                  <FormattedFlokicoinAmount
                    amount={(app.maxAmount - app.budgetUsage) * 1000}
                  />
                </p>
                <FormattedFiatAmount amount={app.maxAmount - app.budgetUsage} />
              </div>
              <div>
                <p className="text-xs text-secondary-foreground font-medium">
                  Budget renewal
                </p>
                <p className="text-xl font-medium">
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
