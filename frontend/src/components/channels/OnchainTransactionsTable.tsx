import dayjs from "dayjs";
import { ArrowDownIcon, ArrowUpIcon, Loader2 } from "lucide-react";
import { useEffect, useRef } from "react";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Table, TableBody, TableCell, TableRow } from "src/components/ui/table";
import { useInfo } from "src/hooks/useInfo";
import { useOnchainTransactions } from "src/hooks/useOnchainTransactions";
import { cn } from "src/lib/utils";

export function OnchainTransactionsTable() {
  const { data: info } = useInfo();
  const {
    transactions,
    setSize,
    isReachingEnd,
    isLoading,
    isLoadingMore,
    error,
  } = useOnchainTransactions();

  const loadMoreRef = useRef<HTMLDivElement | null>(null);
  const observerRef = useRef<IntersectionObserver | null>(null);
  
  // Store latest values in refs to avoid stale closures
  const isLoadingMoreRef = useRef(isLoadingMore);
  const isReachingEndRef = useRef(isReachingEnd);
  
  useEffect(() => {
    isLoadingMoreRef.current = isLoadingMore;
    isReachingEndRef.current = isReachingEnd;
  });

  useEffect(() => {
    const currentElement = loadMoreRef.current;
    
    if (observerRef.current) {
      observerRef.current.disconnect();
    }

    if (!currentElement || isReachingEnd) {
      return;
    }

    observerRef.current = new IntersectionObserver(
      (entries) => {
        const entry = entries[0];
        
        if (entry.isIntersecting && !isLoadingMoreRef.current && !isReachingEndRef.current) {
          setSize((prevSize) => prevSize + 1);
        }
      },
      {
        threshold: 0.1,
        rootMargin: '500px', // Start loading well before reaching the bottom (~20% before)
      }
    );

    observerRef.current.observe(currentElement);

    return () => {
      if (observerRef.current) {
        observerRef.current.disconnect();
      }
    };
  }, [setSize, isReachingEnd]); // Re-setup when these change

  if (!transactions?.length) {
    if (isLoading) {
      return (
        <Card className="mt-6">
            <CardHeader>
                <CardTitle className="text-2xl">On-Chain Transactions</CardTitle>
            </CardHeader>
            <CardContent>
                <div className="flex justify-center p-4">
                    <Loader2 className="animate-spin" />
                </div>
            </CardContent>
        </Card>
      )
    }
    return null;
  }

  return (
    <Card className="mt-6">
      <CardHeader>
        <CardTitle className="text-2xl">On-Chain Transactions</CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableBody>
            {transactions.map((tx) => {
              const Icon = tx.type == "outgoing" ? ArrowUpIcon : ArrowDownIcon;
              return (
                <TableRow
                  key={tx.txId}
                  className="cursor-pointer"
                  onClick={() => {
                    window.open(`${info?.mempoolUrl}/tx/${tx.txId}`, "_blank");
                  }}
                >
                  <TableCell className="flex items-center gap-2">
                    <div
                      className={cn(
                        "flex justify-center items-center rounded-full w-10 h-10 relative",
                        tx.state === "unconfirmed"
                          ? "bg-blue-100 dark:bg-sky-950 animate-pulse"
                          : tx.type === "outgoing"
                            ? "bg-orange-100 dark:bg-amber-950"
                            : "bg-green-100 dark:bg-emerald-950"
                      )}
                      title={`${tx.numConfirmations} confirmations`}
                    >
                      <Icon
                        strokeWidth={3}
                        className={cn(
                          "size-6",
                          tx.state === "unconfirmed"
                            ? "stroke-blue-500 dark:stroke-sky-500"
                            : tx.type === "outgoing"
                              ? "stroke-orange-500 dark:stroke-amber-500"
                              : "stroke-green-500 dark:stroke-teal-500"
                        )}
                      />
                    </div>
                    <div className="md:flex md:gap-2 md:items-center">
                      <p className="font-semibold text-lg">
                        {tx.type == "outgoing"
                          ? tx.state === "confirmed"
                            ? "Sent"
                            : "Sending"
                          : tx.state === "confirmed"
                            ? "Received"
                            : "Receiving"}
                      </p>
                      <p
                        className="text-muted-foreground"
                        title={dayjs(tx.createdAt * 1000)
                          .local()
                          .format("D MMMM YYYY, HH:mm")}
                      >
                        {dayjs(tx.createdAt * 1000)
                          .local()
                          .fromNow()}
                      </p>
                    </div>
                  </TableCell>

                  <TableCell>
                    <div className="flex flex-col items-end">
                      <div className="flex flex-row gap-1">
                        <p
                          className={cn(
                            tx.type == "incoming" &&
                              "text-green-600 dark:text-emerald-500"
                          )}
                        >
                          {tx.type == "outgoing" ? "-" : "+"}
                          <span className="font-medium">
                            <FormattedFlokicoinAmount
                              amount={tx.amountLoki * 1000}
                            />
                          </span>
                        </p>
                      </div>
                      <FormattedFiatAmount
                        className="text-xs"
                        amount={tx.amountLoki}
                      />
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        {!isReachingEnd && !error && (
          <div 
            ref={loadMoreRef}
            className="flex items-center justify-center p-4"
          >
            <Loader2 className="animate-spin h-6 w-6 text-muted-foreground" />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
