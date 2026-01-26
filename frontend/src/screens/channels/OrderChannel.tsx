import { Copy, InfoIcon, Zap } from "lucide-react";
import React, { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
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
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import { useLSPEvents } from "src/hooks/useLSPEvents";
import { useLSPS1 } from "src/hooks/useLSPS1";
import { useLSPS2 } from "src/hooks/useLSPS2";
import { copyToClipboard } from "src/lib/clipboard";
import { cn, formatAmount } from "src/lib/utils";
import { LSPS1CreateOrderRequest, LSPS1Option, LSPS2OpeningFeeParams } from "src/types";
import { LSPS5EventType } from "src/types/lspsEvents";
import { request } from "src/utils/request";

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";
import TickIcon from "src/assets/illustrations/tick.svg?react";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";

export default function OrderChannel() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const [selectedLSP, setSelectedLSP] = useState<string>("");
  const { getInfo, createOrder, getOrder, isLoading, error: lspsError } = useLSPS1(selectedLSP);
  const { getInfo: getLSPS2Info } = useLSPS2(selectedLSP);
  const { lastEvent } = useLSPEvents(); // Listen for real-time events (webhooks/SSE)
  
  const [options, setOptions] = useState<LSPS1Option[]>([]);
  const [amount, setAmount] = useState<string>("250000"); // Default similar to preset
  const [paymentInvoice, setPaymentInvoice] = useState<string>("");
  const [orderId, setOrderId] = useState<string>("");
  const [isPaid, setIsPaid] = useState<boolean>(false);
  const [orderFee, setOrderFee] = useState<number>(0);
  const [feeParams, setFeeParams] = useState<LSPS2OpeningFeeParams | null>(null);

  useEffect(() => {
    if (lspsError) {
      toast.error(lspsError);
    }
  }, [lspsError]);
  
  const presetAmounts = [250_000, 500_000, 1_000_000];

  useEffect(() => {
    if (info?.lsps && info.lsps.length > 0) {
      // Default to first LSP if not set
      if (!selectedLSP) {
          setSelectedLSP(info.lsps[0].pubkey);
      }
    }
  }, [info, selectedLSP]);

  useEffect(() => {
    if (selectedLSP) {
      (async () => {
        // Fetch LSPS1 Options
        const res = await getInfo();
        if (res && res.options) {
          setOptions(res.options);
          // Only override amount if it's invalid for new options
          if (res.options.length > 0) {
              const min = res.options[0].min_initial_client_balance_loki;
              if (parseInt(amount) < min) {
                  setAmount(Math.ceil(min).toString());
              }
          }
        }
        
        // Fetch LSPS2 Fee Params for estimation (since LSPS1 usually uses same logic or we use JIT params as proxy)
        // Ideally LSPS1 GetInfo would return fee params too, but the spec separates them.
        // We use LSPS2 params as a best-effort estimate if LSPS1 doesn't provide them upfront.
        const feeRes = await getLSPS2Info();
        if (feeRes && feeRes.opening_fee_params_menu && feeRes.opening_fee_params_menu.length > 0) {
             setFeeParams(feeRes.opening_fee_params_menu[0]);
        }
      })();
    }
  }, [selectedLSP, getInfo, getLSPS2Info]);

  // Real-time fee estimation
  useEffect(() => {
      if (!amount || !feeParams || isPaid) return;
      
      const amountMloki = parseInt(amount) * 1000;
      const minFee = parseInt(feeParams.min_fee_mloki);
      const proportionalFee = Math.ceil((amountMloki * feeParams.proportional) / 1000000);
      
      const estimatedFeeMloki = Math.max(minFee, proportionalFee);
      
      // Update fee display if we haven't created an order yet
      if (!orderId) {
          setOrderFee(estimatedFeeMloki / 1000); // UI expects Loki (satoshis) for orderFee state currently?
          // Wait, orderFee state usage:
          // FormattedFlokicoinAmount amount={orderFee * 1000}
          // So orderFee is in Satoshis.
          // feeMloki / 1000 = Satoshis.
      }
  }, [amount, feeParams, orderId, isPaid]);

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
      } catch (e) {
         console.error(e);
      }
  };

  if (!info || !balances) return <Loading />;

  return (
    <div className="flex flex-col gap-5">
      {!paymentInvoice && (
        <AppHeader
          title="Increase Inbound Liquidity"
          description="Order a channel from your LSP to increase your receiving capacity"
        />
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
                          min={options[0]?.min_initial_client_balance_loki}
                          max={options[0]?.max_initial_client_balance_loki}
                        />

                        {/* Helper text for limits or balance if needed */}
                         {options.length > 0 && (
                             <p className="text-muted-foreground text-xs">
                                 Min: <FormattedFlokicoinAmount amount={options[0].min_initial_client_balance_loki * 1000} /> - 
                                 Max: <FormattedFlokicoinAmount amount={options[0].max_initial_client_balance_loki * 1000} />
                             </p>
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

                    {orderFee > 0 && !orderId && (
                        <div className="flex justify-between items-center text-sm p-3 bg-muted/50 rounded-md">
                            <span className="text-muted-foreground">Estimated Fee</span>
                            <div className="text-right">
                                <div className="font-medium">
                                    <FormattedFlokicoinAmount amount={orderFee * 1000} />
                                </div>
                                <FormattedFiatAmount amount={orderFee} className="text-xs text-muted-foreground" />
                            </div>
                        </div>
                    )}

                    <div className="flex gap-4 mt-2">
                        <LoadingButton 
                            type="submit" 
                            size="lg"
                            className="w-full"
                            loading={isLoading}
                            disabled={!selectedLSP || !amount}
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
                        <LinkButton to="/channels" className="w-full">
                            View Channels
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

function FeeDisplay({ invoice, size = "sm" }: { invoice: string; size?: "sm" | "lg" }) {
  const [sats, setSats] = React.useState(0);
  
  React.useEffect(() => {
     try {
         // Dynamic import to avoid breaking if not available top level, though it should be fine
         import("@lightz/lightning-tools").then(({ Invoice }) => {
             const inv = new Invoice({ pr: invoice });
             setSats(inv.satoshi);
         });
     } catch (e) {
         console.error(e);
     }
  }, [invoice]);

  if (size === "lg") {
      return (
          <div className="text-center">
              <div className="text-2xl font-semibold">
                  <FormattedFlokicoinAmount amount={sats * 1000} />
              </div>
              <FormattedFiatAmount amount={sats} className="text-muted-foreground" />
          </div>
      );
  }

  return (
      <div className="text-right">
          <div className="font-semibold">
              <FormattedFlokicoinAmount amount={sats * 1000} />
          </div>
          <FormattedFiatAmount amount={sats} className="text-muted-foreground text-xs" />
      </div>
  );
}

type PayInvoiceButtonsProps = {
  paymentInvoice: string;
  balances: { lightning: { nextMaxSpendableMPP: number } } | null;
  onPaid: () => void;
};

function PayInvoiceButtons({ paymentInvoice, balances, onPaid }: PayInvoiceButtonsProps) {
  const [isPaying, setIsPaying] = React.useState(false);
  const [invoiceAmount, setInvoiceAmount] = React.useState(0);

  React.useEffect(() => {
    import("@lightz/lightning-tools").then(({ Invoice }) => {
      const inv = new Invoice({ pr: paymentInvoice });
      setInvoiceAmount(inv.satoshi);
    }).catch(console.error);
  }, [paymentInvoice]);

  const canPayInternally =
    balances &&
    invoiceAmount > 0 &&
    balances.lightning.nextMaxSpendableMPP / 1000 > invoiceAmount;

  const handlePayNow = async () => {
    try {
      setIsPaying(true);
      await request(`/api/payments/${paymentInvoice}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
      });
      toast.success("Payment sent!");
      onPaid();
    } catch (e) {
      toast.error("Payment failed", { description: "" + e });
      console.error(e);
    } finally {
      setIsPaying(false);
    }
  };

  const copyInvoice = () => {
    copyToClipboard(paymentInvoice);
    toast.success("Invoice copied to clipboard");
  };

  return (
    <div className="flex gap-2 w-full flex-wrap">
      {canPayInternally && (
        <LoadingButton
          loading={isPaying}
          className="flex-1"
          onClick={handlePayNow}
        >
          <Zap className="mr-2 h-4 w-4" />
          Pay Now
        </LoadingButton>
      )}
      <Button variant="outline" className="flex-1" onClick={copyInvoice}>
        <Copy className="mr-2 h-4 w-4" />
        Copy Invoice
      </Button>
    </div>
  );
}
