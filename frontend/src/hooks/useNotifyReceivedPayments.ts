import React from "react";
import { toast } from "sonner";
import { useInfo } from "src/hooks/useInfo";
import { useTransactions } from "src/hooks/useTransactions";
import { Transaction } from "src/types";
import { formatFlokicoinAmount } from "src/utils/flokicoinFormatting";

export function useNotifyReceivedPayments() {
  const { data: info } = useInfo();
  const { data: transactionsData } = useTransactions(undefined, true, 1);
  const [prevTransaction, setPrevTransaction] = React.useState<Transaction>();

  React.useEffect(() => {
    if (!transactionsData?.transactions?.length || !info) {
      return;
    }
    const latestTx = transactionsData.transactions[0];
    if (latestTx !== prevTransaction) {
      if (prevTransaction && latestTx.type === "incoming") {
        toast("Payment received", {
          description: formatFlokicoinAmount(
            latestTx.amount,
            info.flokicoinDisplayFormat
          ),
        });
      }
      setPrevTransaction(latestTx);
    }
  }, [prevTransaction, transactionsData, info]);
}
