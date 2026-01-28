import dayjs from "dayjs";
import {
  ArrowDownIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  CopyIcon,
  XIcon,
  Zap
} from "lucide-react";
import { useEffect, useState } from "react";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { FeeDisplay } from "src/components/lsps/FeeDisplay";
import { PayInvoiceButtons } from "src/components/lsps/PayInvoiceButtons";
import QRCode from "src/components/QRCode";
import { Button } from "src/components/ui/button";
import { LinkButton } from "src/components/ui/custom/link-button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "src/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "src/components/ui/table";
import { useLSPEventContext } from "src/context/LSPEventContext"; // Import global context
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import { useLSPS1 } from "src/hooks/useLSPS1";
import { copyToClipboard } from "src/lib/clipboard";
import { cn } from "src/lib/utils";
import { LSPS1Order } from "src/types";

const middleTruncate = (str: string, keep = 8) => {
  if (str.length <= keep * 2) return str;
  return `${str.substring(0, keep)}...${str.substring(str.length - keep)}`;
};

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";

function OrderHistory() {
  const { listOrders, isLoading } = useLSPS1(""); // Empty pubkey as we just want list function
  const { lastEvent } = useLSPEventContext(); // Use global context
  const { data: balances } = useBalances();
  const { data: info } = useInfo();
  const [orders, setOrders] = useState<LSPS1Order[]>([]);
  const [selectedOrder, setSelectedOrder] = useState<LSPS1Order | null>(null);
  const [showDetails, setShowDetails] = useState(false);
  const [paymentOrder, setPaymentOrder] = useState<LSPS1Order | null>(null);

  useEffect(() => {
    if (lastEvent?.event === "lsps5.order_state_changed" && lastEvent.properties.order_id) {
        // Optimistic update
        setOrders(prev => prev.map(o => 
            o.orderId === lastEvent.properties.order_id 
            ? { ...o, state: lastEvent.properties.state || o.state }
            : o
        ));
    }
    loadOrders();
  }, [lastEvent]); // React to any event

  // Optional: Filter specifically for OrderStateChanged to be more efficient
  // But reloading on any LSP event is generally safe for now.

  const getLSPName = (pubkey: string) => {
    if (!info?.lsps) return middleTruncate(pubkey);
    const lsp = info.lsps.find((l) => l.pubkey === pubkey);
    return lsp ? lsp.name : middleTruncate(pubkey);
  };

  const loadOrders = async () => {
    const data = await listOrders();
    setOrders(data);
  };


  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-b border-border pb-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-2xl font-bold">Liquidity Orders</h1>
          <p className="text-sm text-muted-foreground">
            Manage and track your inbound liquidity channel requests.
          </p>
        </div>
        <div className="flex items-center gap-2 w-full sm:w-auto">
          {!isLoading && orders.length > 0 && (
            <LinkButton to="/channels/inbound" size="default">
              <Zap className="mr-2 h-4 w-4" />
              Order Liquidity
            </LinkButton>
          )}
        </div>
      </div>


      {isLoading && <Loading />}

      {!isLoading && orders.length === 0 && (
        <div className="flex flex-col items-center justify-center py-20 px-4 text-center max-w-md mx-auto">
          <div className="mb-6 w-full max-w-[280px]">
            <LightningNetworkDark className="w-full hidden dark:block" />
            <LightningNetworkLight className="w-full dark:hidden" />
          </div>
          <h2 className="text-2xl font-bold mb-2">No Liquidity Orders Yet</h2>
          <p className="text-muted-foreground text-center max-w-sm mx-auto mt-2">
            You haven't ordered any inbound liquidity yet. Inbound liquidity provides the capacity needed to receive Lightning payments.
          </p>
          <LinkButton to="/channels/inbound" size="lg" className="w-full sm:w-auto mt-4">
            <Zap className="mr-2 h-4 w-4" />
            Order Inbound Liquidity
          </LinkButton>
        </div>
      )}

      {!isLoading && orders.length > 0 && (
        <div className="border rounded-lg">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Date</TableHead>
                <TableHead>Order ID</TableHead>
                <TableHead>LSP</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="text-right">Total Fee</TableHead>
                <TableHead className="text-right">Capacity</TableHead>
                <TableHead></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orders.map((order) => (
                <TableRow
                  key={order.orderId}
                  className="cursor-pointer hover:bg-muted/50"
                  onClick={() => setSelectedOrder(order)}
                >
                  <TableCell>{new Date(order.createdAt).toLocaleString()}</TableCell>
                  <TableCell className="font-mono">{middleTruncate(order.orderId, 8)}</TableCell>
                  <TableCell className="font-medium">{getLSPName(order.lspPubkey)}</TableCell>
                  <TableCell>
                    <span
                      className={`inline-flex items-center px-2 py-1 rounded-full text-xs font-medium ${
                        order.state === "COMPLETED" || order.state === "SUCCESS"
                          ? "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300"
                          : order.state === "FAILED"
                          ? "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300"
                          : order.state === "PAID"
                          ? "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-300"
                          : "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-300"
                      }`}
                    >
                      {order.state}
                    </span>
                  </TableCell>
                  <TableCell className="text-right">
                    <FormattedFlokicoinAmount amount={order.feeTotal * 1000} />
                  </TableCell>
                  <TableCell className="text-right">
                    <FormattedFlokicoinAmount amount={(order.clientBalanceLoki + (order.lspBalanceLoki || 0)) * 1000} />
                  </TableCell>
                  <TableCell>
                    {order.state === "CREATED" && (
                      <Button 
                        size="sm" 
                        variant="default"
                        onClick={(e) => {
                          e.stopPropagation();
                          setPaymentOrder(order);
                        }}
                      >
                         Pay
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <Dialog 
        open={!!selectedOrder} 
        onOpenChange={(open) => {
          if (!open) {
            setSelectedOrder(null);
            setShowDetails(false);
          }
        }}
      >
        <DialogContent className="max-w-md slashed-zero">
          <DialogHeader>
            <DialogTitle>Liquidity Order Details</DialogTitle>
          </DialogHeader>
          
          {selectedOrder && (
            <div className="flex flex-col gap-6">
              {/* Status Banner */}
              <div className="flex items-center mt-2">
                <div className={cn(
                  "flex justify-center items-center rounded-full w-14 h-14 relative shrink-0",
                  selectedOrder.state === "COMPLETED" || selectedOrder.state === "SUCCESS"
                    ? "bg-green-100 dark:bg-emerald-950"
                    : selectedOrder.state === "FAILED"
                    ? "bg-red-100 dark:bg-rose-950"
                    : "bg-blue-100 dark:bg-sky-950"
                )}>
                  {selectedOrder.state === "COMPLETED" || selectedOrder.state === "SUCCESS" ? (
                    <CheckIcon className="w-8 h-8 stroke-green-500 dark:stroke-teal-500" strokeWidth={3} />
                  ) : selectedOrder.state === "FAILED" ? (
                    <XIcon className="w-8 h-8 stroke-red-500 dark:stroke-rose-500" strokeWidth={3} />
                  ) : (
                    <ArrowDownIcon className="w-8 h-8 stroke-blue-500 dark:stroke-sky-500" strokeWidth={3} />
                  )}
                </div>
                <div className="ml-4 flex flex-col justify-center">
                  <p className="text-xl md:text-2xl font-semibold sensitive">
                    <FormattedFlokicoinAmount amount={(selectedOrder.clientBalanceLoki + (selectedOrder.lspBalanceLoki || 0)) * 1000} />
                  </p>
                  <FormattedFiatAmount amount={selectedOrder.clientBalanceLoki + (selectedOrder.lspBalanceLoki || 0)} className="text-muted-foreground" />
                </div>
              </div>

              <DialogDescription className="text-foreground max-h-[60vh] overflow-y-auto pr-2">
                <div className="flex flex-col gap-6">
                  
                  {/* Human Readable Details */}
                  <div>
                    <p>Status</p>
                    <p className="text-muted-foreground capitalize">
                      {selectedOrder.state.toLowerCase()}
                    </p>
                  </div>

                  <div>
                    <p>Date & Time</p>
                    <p className="text-muted-foreground">
                      {dayjs(selectedOrder.createdAt).format("D MMMM YYYY, HH:mm")}
                    </p>
                  </div>

                  <div>
                    <p>Fee</p>
                    <div className="flex items-center gap-2 text-muted-foreground">
                      <FormattedFlokicoinAmount amount={selectedOrder.feeTotal * 1000} />
                      <span className="text-xs">
                        ({((selectedOrder.feeTotal / (selectedOrder.clientBalanceLoki + (selectedOrder.lspBalanceLoki || 0))) * 100).toFixed(2)}%)
                      </span>
                    </div>
                  </div>

                  {/* Collapsible Technical Details */}
                  <div className="w-full pt-2">
                    <div
                      className="flex items-center gap-2 cursor-pointer text-sm font-medium hover:text-muted-foreground transition-colors"
                      onClick={() => setShowDetails(!showDetails)}
                    >
                      Technical Details
                      {showDetails ? (
                        <ChevronUpIcon className="size-4" />
                      ) : (
                        <ChevronDownIcon className="size-4" />
                      )}
                    </div>
                    
                    {showDetails && (
                      <div className="flex flex-col gap-4 mt-4 animate-in slide-in-from-top-2 duration-200">
                        <div>
                          <p className="text-sm">Order ID</p>
                          <div className="flex items-center gap-2 bg-muted/50 p-2 rounded-md">
                            <code className="text-xs text-muted-foreground break-all flex-1">
                              {selectedOrder.orderId}
                            </code>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-6 w-6 shrink-0"
                              onClick={() => copyToClipboard(selectedOrder.orderId)}
                            >
                              <CopyIcon className="h-3 w-3" />
                            </Button>
                          </div>
                        </div>

                        <div>
                          <p className="text-sm">LSP</p>
                          <div className="flex items-center gap-2 bg-muted/50 p-2 rounded-md">
                            <code className="text-xs text-muted-foreground break-all flex-1">
                                {getLSPName(selectedOrder.lspPubkey)}
                            </code>
                          </div>
                        </div>

                        <div>
                          <p className="text-sm">LSP Pubkey</p>
                          <div className="flex items-center gap-2 bg-muted/50 p-2 rounded-md">
                            <code className="text-xs text-muted-foreground break-all flex-1">
                              {selectedOrder.lspPubkey}
                            </code>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-6 w-6 shrink-0"
                              onClick={() => copyToClipboard(selectedOrder.lspPubkey)}
                            >
                              <CopyIcon className="h-3 w-3" />
                            </Button>
                          </div>
                        </div>

                        {selectedOrder.paymentInvoice && (
                          <div>
                            <p className="text-sm">Payment Invoice</p>
                            <div className="flex items-center gap-2 bg-muted/50 p-2 rounded-md">
                              <code className="text-xs text-muted-foreground break-all flex-1 line-clamp-2">
                                {selectedOrder.paymentInvoice}
                              </code>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 shrink-0"
                                onClick={() => copyToClipboard(selectedOrder.paymentInvoice)}
                              >
                                <CopyIcon className="h-3 w-3" />
                              </Button>
                            </div>
                          </div>
                        )}
                        
                        <div>
                          <p className="text-sm">Last Updated</p>
                          <p className="text-xs text-muted-foreground">
                             {dayjs(selectedOrder.updatedAt).format("D MMMM YYYY, HH:mm:ss")}
                          </p>
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              </DialogDescription>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Payment Review Modal */}
      <Dialog 
        open={!!paymentOrder} 
        onOpenChange={(open) => {
          if (!open) setPaymentOrder(null);
        }}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Review Inbound Order</DialogTitle>
          </DialogHeader>
          
          {paymentOrder && (
            <div className="flex flex-col gap-5">
                <div className="border-b pb-4">
                    <div className="flex justify-between text-sm mb-2">
                        <span className="text-muted-foreground">Incoming Liquidity</span>
                        <div className="text-right">
                            <div className="font-semibold">
                                <FormattedFlokicoinAmount amount={(paymentOrder.clientBalanceLoki + (paymentOrder.lspBalanceLoki || 0)) * 1000} />
                            </div>
                            <FormattedFiatAmount amount={paymentOrder.clientBalanceLoki + (paymentOrder.lspBalanceLoki || 0)} className="text-muted-foreground text-xs" />
                        </div>
                    </div>
                    {paymentOrder.feeTotal > 0 && (
                        <div className="flex justify-between text-sm mb-2">
                            <span className="text-muted-foreground">LSP Fee</span>
                            <div className="text-right">
                                <div className="font-semibold">
                                    <FormattedFlokicoinAmount amount={paymentOrder.feeTotal * 1000} />
                                </div>
                                <FormattedFiatAmount amount={paymentOrder.feeTotal} className="text-muted-foreground text-xs" />
                            </div>
                        </div>
                    )}
                    <div className="flex justify-between text-sm">
                         <span className="text-muted-foreground">Amount to pay</span>
                         <div className="text-right">
                            <FeeDisplay invoice={paymentOrder.paymentInvoice} />
                        </div>
                    </div>
                </div>
                
                <div className="flex flex-col items-center gap-6">
                    <div className="relative flex items-center justify-center w-full">
                        <QRCode value={paymentOrder.paymentInvoice} className="w-full max-w-[250px]" />
                    </div>

                    <div className="flex flex-col items-center gap-1">
                         <FeeDisplay invoice={paymentOrder.paymentInvoice} size="lg" />
                    </div>

                    <PayInvoiceButtons
                      paymentInvoice={paymentOrder.paymentInvoice}
                      balances={balances || null}
                      onPaid={() => {
                          setPaymentOrder(null);
                          loadOrders(); // Refresh status
                      }}
                    />
                </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

export default OrderHistory;
