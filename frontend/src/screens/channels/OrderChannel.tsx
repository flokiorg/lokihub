import { History, InfoIcon, Zap } from "lucide-react";
import React, { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Card, CardContent } from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from "src/components/ui/select";
import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
} from "src/components/ui/tooltip";
import { useLSPEventContext } from "src/context/LSPEventContext"; // Use global context
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import { useLSPS1 } from "src/hooks/useLSPS1";
import { cn, formatAmount } from "src/lib/utils";
import { LSPS1CreateOrderRequest, LSPS1GetInfoResponse } from "src/types";
import { LSPS5EventType } from "src/types/lspsEvents";
import { request } from "src/utils/request";

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";
import TickIcon from "src/assets/illustrations/tick.svg?react";
import { FeeDisplay } from "src/components/lsps/FeeDisplay";
import { PayInvoiceButtons } from "src/components/lsps/PayInvoiceButtons";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";

export default function OrderChannel() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const [selectedLSP, setSelectedLSP] = useState<string>("");
  // Unified loading and error state handling
  const { getInfo, createOrder, getOrder, isLoading: lsps1Loading, error: lsps1Error } = useLSPS1(selectedLSP);
  const { lastEvent } = useLSPEventContext(); 
  
  const [amount, setAmount] = useState<string>(""); // Start empty to allow smart prefill
  const [paymentInvoice, setPaymentInvoice] = useState<string>("");
  const [orderId, setOrderId] = useState<string>("");
  const [isPaid, setIsPaid] = useState<boolean>(false);
  const [orderFee, setOrderFee] = useState<number>(0);
  const [dataLoadError, setDataLoadError] = useState<string | null>(null);
  const [lsps1Info, setLsps1Info] = useState<LSPS1GetInfoResponse | null>(null);

  // Initialize selectedLSP with the first available LSP
  useEffect(() => {
    if (info?.lsps?.length && !selectedLSP) {
      setSelectedLSP(info.lsps[0].pubkey);
    }
  }, [info, selectedLSP]);

  // Unified loading state
  const isLoading = lsps1Loading;

  const fetchData = useCallback(async () => {
    if (!selectedLSP) return;
    
    setDataLoadError(null);
    try {
        const lsps1Res = await getInfo();

        // Handle LSPS1 Options
        if (lsps1Res) {
            setLsps1Info(lsps1Res);
        }

    } catch (e: any) {
        console.error("Failed to fetch LSP data", e);
        setDataLoadError(e.message || "Failed to load LSP data");
        toast.error("Failed to connect to LSP. Please try again.");
    }
  }, [selectedLSP, getInfo]); // Removed amount dependency to avoid prefill triggers

  useEffect(() => {
    if (selectedLSP) {
        fetchData();
    }
  }, [selectedLSP]); // Removed fetchData from deps to avoid loop if fetchData changes unnecessarily, though useCallback handles it.

  const validationError = React.useMemo(() => {
      if (!amount || !lsps1Info) return null;
      
      const amtNum = parseInt(amount);
      if (isNaN(amtNum)) return "Invalid amount";

      const minNum = Number(lsps1Info.min_initial_lsp_balance_loki);
      const maxNum = Number(lsps1Info.max_initial_lsp_balance_loki);

      if (!isNaN(minNum) && amtNum < minNum) {
          return `Amount below minimum of ${minNum} Loki`;
      }
      if (!isNaN(maxNum) && maxNum > 0 && amtNum > maxNum) {
          return `Amount exceeds maximum of ${maxNum} Loki`;
      }
      
      return null;
  }, [amount, lsps1Info]);

  const estimatedFee = React.useMemo(() => {
    if (!amount || !lsps1Info?.opening_fee_params?.length) return 0;
    
    const amtLoki = parseInt(amount);
    if (isNaN(amtLoki)) return 0;
    
    const amtMloki = amtLoki * 1000;
    const params = lsps1Info.opening_fee_params[0];
    
    const minFee = parseInt(params.min_fee_mloki);
    const proportionalFee = Math.ceil((amtMloki * params.proportional) / 1000000);
    
    return Math.max(minFee, proportionalFee) / 1000;
  }, [amount, lsps1Info]);


  const checkOrderStatus = useCallback(async () => {
    if (!orderId || isPaid) return;
    
    const res = await getOrder(orderId);
    if (res) {
        if (res.state === "PAID") {
            // Trigger channel opening by paying self
            try {
              toast.info("Triggering channel opening...");
              
              const amountSats = res.order_total_loki 
                  ? Math.ceil(res.order_total_loki) 
                  : parseInt(amount);

              const invRes = await request<{invoice: string}>("/api/invoices", {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({
                      amount: amountSats,
                      description: "LSPS1 JIT Channel Opening Trigger"
                  })
              });

              if (invRes && invRes.invoice) {
                  await request(`/api/payments/${invRes.invoice}`, {
                      method: "POST",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ amount: amountSats, metadata: {} })
                  });
              }
            } catch (e: any) {
                console.error('[OrderChannel] Trigger failed:', e);
                toast.error("Channel opening trigger failed", {
                    description: e.message || "Payment failure"
                });
            }

            setIsPaid(true);
            toast.success("Payment received! Channel opening in progress...");
        } else if (res.state === "COMPLETED") {
            setIsPaid(true);
             toast.success("Channel order completed!");
        } else if (res.state === "FAILED") {
            toast.error("Channel order failed");
        }
    }
  }, [orderId, isPaid, getOrder, amount]);

  // Trigger check on real-time events
  useEffect(() => {
    if (!orderId || isPaid) return;
    
    if (lastEvent?.properties.order_id === orderId) {
       
       if (lastEvent.event === LSPS5EventType.OrderStateChanged) {
           const { state, error: errMsg, channel_point } = lastEvent.properties;
           
           if (state === "FAILED") {
               toast.error("Channel order failed", {
                   description: errMsg || "Unknown error from LSP"
               });
               // Optionally stop polling here or update local state
           } else if (state === "COMPLETED") {
               toast.success("Channel order completed!", {
                   description: `Channel Point: ${channel_point}`
               });
               setIsPaid(true); // Move to success view
           }
           
           // Always refresh status to be sure
           checkOrderStatus();
       } else if (lastEvent.event === LSPS5EventType.PaymentIncoming) {
           checkOrderStatus();
       }
    }
  }, [lastEvent, orderId, isPaid, checkOrderStatus]);

  // Poll order status if we have an orderId
  // Poll order status if we have an orderId and polling is enabled
  useEffect(() => {
      let interval: NodeJS.Timeout;
      // Strict check: Only poll if explicitly enabled in backend config
      if (orderId && selectedLSP && !isPaid && info?.enablePolling) {
          interval = setInterval(() => {
              checkOrderStatus();
          }, 5000); // Polling every 5s if enabled
      }
      return () => clearInterval(interval);
  }, [orderId, selectedLSP, isPaid, checkOrderStatus, info?.enablePolling]);

  const handleCreateOrder = async (e: React.FormEvent) => {
      e.preventDefault();
      if (!selectedLSP || !amount) return;
      
      try {
          const amountSats = parseInt(amount);
          const req: LSPS1CreateOrderRequest = {
              lsp_pubkey: selectedLSP,
              amount_loki: amountSats,
              channel_expiry_blocks: 144 * 30, 
              announce_channel: false, 
              opening_fee_params: lsps1Info?.opening_fee_params?.[0]
          };
          
          const res = await createOrder(req);
          if (res) {
              setOrderId(res.order_id);
              setPaymentInvoice(res.payment_invoice);
              if (res.fee_total_loki) {
                  setOrderFee(res.fee_total_loki);
              }
              toast.success("Order created! Please pay the invoice.");
          }
      } catch (e: any) {
         console.error(e);
         toast.error("Failed to create order", {
             description: e.message || "Unknown error"
         });
      }
  };

  const presetAmounts = [250_000, 500_000, 1_000_000];

  if (!info || !balances) return <Loading />;

  return (
    <div className="flex flex-col gap-5">
      {!paymentInvoice && (
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-b border-border pb-4 mb-5">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-bold font-sans">Increase Inbound Liquidity</h1>
            <p className="text-sm text-muted-foreground">
              Order a channel from your LSP to increase your receiving capacity
            </p>
          </div>
          <div className="flex items-center gap-2">
            <LinkButton to="/channels/history" variant="secondary" size="sm" className="hidden sm:flex">
              <History className="w-4 h-4 mr-2" />
              History
            </LinkButton>
          </div>
        </div>
      )}



        {!paymentInvoice ? (
           <div className="md:max-w-md max-w-full flex flex-col gap-5 flex-1">
                <LightningNetworkDark className="w-full hidden dark:block" />
                <LightningNetworkLight className="w-full dark:hidden" />
                
                <p className="text-muted-foreground">
                  Order a channel from an LSP. This provides inbound liquidity, allowing you to receive payments immediately after the channel is confirmed.
                </p>

                <Alert className="bg-muted/50">
                  <InfoIcon className="h-4 w-4" />
                  <AlertDescription className="flex flex-col gap-2">
                    <p className="text-sm">
                      Looking to send funds instead? You might need spending capacity.
                    </p>
                    <LinkButton
                      to="/channels/outgoing"
                      variant="outline"
                      size="sm"
                      className="w-full sm:w-auto"
                    >
                      Increase Spending Balance
                    </LinkButton>
                  </AlertDescription>
                </Alert>

                <form onSubmit={handleCreateOrder} className="md:max-w-md max-w-full flex flex-col gap-5 flex-1">
                    <div className="grid gap-1.5">
                        <TooltipProvider>
                          <Tooltip>
                            <TooltipTrigger type="button">
                              <div className="flex flex-row gap-2 items-center justify-start text-sm">
                                <Label htmlFor="amount">
                                  Increase inbound liquidity (loki)
                                </Label>
                                <InfoIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                              </div>
                            </TooltipTrigger>
                            <TooltipContent>
                              The size of the channel you want to order. You will pay a fee for this service.
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>

                        <Input
                          id="amount"
                          type="number"
                          required
                          value={amount}
                          onChange={(e) => setAmount(e.target.value.trim())}
                          min={lsps1Info?.min_initial_lsp_balance_loki}
                          max={lsps1Info?.max_initial_lsp_balance_loki}
                        />

                        {/* Helper text for limits or balance if needed */}
                          {lsps1Info && (
                                <div className="flex items-center gap-1.5 text-xs text-muted-foreground px-1 font-sans">
                                    <span className="opacity-70">Order Range:</span>
                                    <div className="flex items-center gap-1.5 text-foreground/90">
                                        <FormattedFlokicoinAmount amount={Number(lsps1Info.min_initial_lsp_balance_loki) * 1000} />
                                        <span className="opacity-40">—</span>
                                        <FormattedFlokicoinAmount amount={Number(lsps1Info.max_initial_lsp_balance_loki) * 1000} />
                                    </div>
                                </div>
                         )}

                        <div className="grid grid-cols-3 gap-1.5 text-muted-foreground text-xs">
                          {presetAmounts.map((preset) => (
                            <div
                              key={preset}
                              className={cn(
                                "text-center border rounded p-2 cursor-pointer hover:border-muted-foreground",
                                +(amount || "0") === preset &&
                                  "border-primary hover:border-primary"
                              )}
                              onClick={() => setAmount(preset.toString())}
                            >
                              {formatAmount(preset * 1000, 0)}
                            </div>
                          ))}
                        </div>
                    </div>

                    <div className="grid gap-1.5">
                         <Label htmlFor="lsp-select">LSP</Label>
                         {info.lsps && info.lsps.length > 0 ? (
                             <Select value={selectedLSP} onValueChange={setSelectedLSP}>
                                <SelectTrigger className="w-full">
                                    <SelectValue placeholder="Select an LSP" />
                                </SelectTrigger>
                                <SelectContent>
                                    {info.lsps.map((lsp) => (
                                        <SelectItem key={lsp.pubkey} value={lsp.pubkey}>
                                            {lsp.name || lsp.pubkey.substring(0, 16) + "..."}
                                        </SelectItem>
                                    ))}
                                </SelectContent>
                             </Select>
                         ) : (
                             <Input value="No LSPs Configured" disabled />
                         )}
                         <div className="text-muted-foreground text-xs">
                            <span className="mr-1">Manage providers in</span>
                            <LinkButton to="/settings/services" variant="link" className="h-auto p-0 text-xs underline">
                                Settings &gt; Services
                            </LinkButton>
                         </div>
                    </div>

                    {/* Error and Fee Display Zone */}
                    <div className="flex flex-col gap-3 mt-4">
                        {(dataLoadError || lsps1Error) && (
                            <Alert variant="destructive">
                                <AlertDescription className="flex flex-row items-center justify-between text-xs">
                                    <span>{dataLoadError || lsps1Error}</span>
                                    <LoadingButton 
                                        variant="outline" 
                                        size="sm" 
                                        onClick={fetchData} 
                                        loading={isLoading}
                                        className="h-7 bg-background text-foreground"
                                    >
                                        Retry
                                    </LoadingButton>
                                </AlertDescription>
                            </Alert>
                        )}

                        {validationError && (
                            <div className="text-[11px] text-destructive bg-destructive/5 px-3 py-2 rounded border border-destructive/10 flex items-center gap-2">
                                <InfoIcon className="h-3 w-3" />
                                {validationError}
                            </div>
                        )}

                        {/* Show either estimated fee or actual fee if already created */}
                        {(estimatedFee > 0 || orderFee > 0) && !orderId && (
                            <div className="flex justify-between items-center text-sm p-3 bg-muted/50 rounded-md">
                                <span className="text-muted-foreground font-medium">
                                    {orderFee > 0 ? "Fee" : "Estimated Fee"}
                                </span>
                                <div className="text-right">
                                    <div className="font-semibold text-primary">
                                        <FormattedFlokicoinAmount amount={(orderFee || estimatedFee) * 1000} />
                                    </div>
                                    <FormattedFiatAmount amount={orderFee || estimatedFee} className="text-[10px] text-muted-foreground" />
                                </div>
                            </div>
                        )}
                    </div>

                    <div className="flex gap-4 mt-2">
                        <LoadingButton 
                            type="submit" 
                            size="lg"
                            className="w-full"
                            loading={isLoading}
                            disabled={!selectedLSP || !amount || !!validationError}
                        >
                            <Zap className="mr-2 h-4 w-4" />
                            Order Channel
                        </LoadingButton>
                    </div>
                    <div className="flex justify-center mt-2">
                      <LinkButton
                        to="/channels/outgoing"
                        variant="link"
                        className="text-muted-foreground text-xs"
                      >
                        Need spending capacity instead?
                      </LinkButton>
                    </div>
                </form>
           </div>
        ) : isPaid ? (
            <div className="md:max-w-md w-full">
                <AppHeader
                    title="Payment Received!"
                    description="Your channel is being opened"
                />
                <Card className="mt-5">
                    <CardContent className="flex flex-col items-center gap-6 pt-6">
                        <div className="p-3">
                            <TickIcon className="w-16 h-16" />
                        </div>
                        <div className="text-center space-y-2">
                            <p className="text-lg font-semibold">Payment Complete</p>
                            <p className="text-muted-foreground text-sm">
                                The LSP is now opening a channel to provide you with <FormattedFlokicoinAmount amount={parseInt(amount) * 1000} /> of inbound liquidity.
                            </p>
                            <p className="text-muted-foreground text-xs">
                                This may take a few minutes. You'll see the new channel in your channels list once it's confirmed.
                            </p>
                        </div>
                        <LinkButton to="/channels/history" className="w-full">
                            View Order History
                        </LinkButton>
                    </CardContent>
                </Card>
            </div>
        ) : (
            <div className="md:max-w-md w-full">
                <AppHeader
                    title="Review Channel Purchase"
                    description="Complete Payment to open a channel to your node"
                />
                <Card className="mt-5">
                    <div className="border-b">
                        <div className="flex justify-between p-4 text-sm">
                            <span className="text-muted-foreground">Incoming Liquidity</span>
                            <div className="text-right">
                                <div className="font-semibold">
                                    <FormattedFlokicoinAmount amount={parseInt(amount) * 1000} />
                                </div>
                                <FormattedFiatAmount amount={parseInt(amount)} className="text-muted-foreground text-xs" />
                            </div>
                        </div>
                        {orderFee > 0 && (
                            <div className="flex justify-between p-4 pt-0 text-sm">
                                <span className="text-muted-foreground">LSP Fee</span>
                                <div className="text-right">
                                    <div className="font-semibold">
                                        <FormattedFlokicoinAmount amount={orderFee * 1000} />
                                    </div>
                                    <FormattedFiatAmount amount={orderFee} className="text-muted-foreground text-xs" />
                                </div>
                            </div>
                        )}
                        <div className="flex justify-between p-4 pt-0 text-sm">
                             <span className="text-muted-foreground">Amount to pay</span>
                             <div className="text-right">
                                <FeeDisplay invoice={paymentInvoice} />
                            </div>
                        </div>
                    </div>
                    
                    <CardContent className="flex flex-col items-center gap-6 pt-6">
                        <div className="flex items-center gap-2 text-muted-foreground animate-pulse">
                            <Loading className="h-4 w-4" />
                            <p>Waiting for lightning payment...</p>
                        </div>

                        <div className="relative flex items-center justify-center w-full">
                            <QRCode value={paymentInvoice} className="w-full max-w-[250px]" />
                        </div>

                        <div className="flex flex-col items-center gap-1">
                             <FeeDisplay invoice={paymentInvoice} size="lg" />
                        </div>

                        <PayInvoiceButtons
                          paymentInvoice={paymentInvoice}
                          balances={balances}
                          onPaid={() => setIsPaid(true)}
                        />
                    </CardContent>
                </Card>
            </div>
        )}
    </div>
  );
}

