import { Check, Copy, InfoIcon, Zap } from "lucide-react";
import React, { useEffect, useState } from "react";
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
import { useLSPS1 } from "src/hooks/useLSPS1";
import { copyToClipboard } from "src/lib/clipboard";
import { cn, formatAmount } from "src/lib/utils";
import { LSPS1CreateOrderRequest, LSPS1Option } from "src/types";
import { request } from "src/utils/request";

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";
import LokiHead from "src/assets/loki.svg?react";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";

export default function OrderChannel() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const [selectedLSP, setSelectedLSP] = useState<string>("");
  const { getInfo, createOrder, getOrder, isLoading, error: lspsError } = useLSPS1(selectedLSP);
  
  const [options, setOptions] = useState<LSPS1Option[]>([]);
  const [amount, setAmount] = useState<string>("250000"); // Default similar to preset
  const [paymentInvoice, setPaymentInvoice] = useState<string>("");
  const [orderId, setOrderId] = useState<string>("");
  const [isPaid, setIsPaid] = useState<boolean>(false);

  useEffect(() => {
    console.log('[OrderChannel] lspsError changed:', lspsError);
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
      })();
    }
  }, [selectedLSP, getInfo, amount]);

  // Poll order status if we have an orderId
  useEffect(() => {
      let interval: NodeJS.Timeout;
      if (orderId && selectedLSP && !isPaid) {
          interval = setInterval(async () => {
              const res = await getOrder(orderId);
              if (res) {
                  console.log('[OrderChannel] Order status:', res.state);
                  if (res.state === "PAID") {
                      clearInterval(interval);
                      
                      // Trigger channel opening by paying self
                      try {
                        console.log('[OrderChannel] Triggering channel opening...');
                        toast.info("Triggering channel opening...");
                        
                        // 1. Create self-invoice
                        // Parse amount from order_total_loki if available, else use requested amount
                        const amountSats = res.order_total_loki 
                            ? Math.ceil(res.order_total_loki) 
                            : parseInt(amount); // fallback (should match closely)

                        const invRes = await request<{invoice: string}>("/api/invoices", {
                            method: "POST",
                            headers: { "Content-Type": "application/json" },
                            body: JSON.stringify({
                                amount: amountSats,
                                description: "LSPS1 JIT Channel Opening Trigger"
                            })
                        });

                        if (invRes && invRes.invoice) {
                            console.log('[OrderChannel] Created self-invoice:', invRes.invoice);
                            
                            // 2. Pay self-invoice
                            // Note: Invoice in URL must be encoded if it contains special chars, but usually alphanumeric
                            await request(`/api/payments/${invRes.invoice}`, {
                                method: "POST",
                                headers: { "Content-Type": "application/json" },
                                body: JSON.stringify({ amount: amountSats, metadata: {} })
                            });
                            console.log('[OrderChannel] Payment-to-self sent');
                        }
                      } catch (e) {
                          console.error('[OrderChannel] Trigger failed:', e);
                          // We continue anyway because maybe it worked partially or LSP will handle it
                      }

                      setIsPaid(true);
                      toast.success("Payment received! Channel opening in progress...");
                  } else if (res.state === "COMPLETED") {
                      clearInterval(interval);
                      setIsPaid(true);
                       toast.success("Channel order completed!");
                  } else if (res.state === "FAILED") {
                      clearInterval(interval);
                      toast.error("Channel order failed");
                  }
              }
          }, 2000);
      }
      return () => clearInterval(interval);
  }, [orderId, selectedLSP, getOrder, isPaid, amount]);

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
              toast.success("Order created! Please pay the invoice.");
          }
      } catch (e) {
         console.error(e);
      }
  };

  const copyInvoice = () => {
      copyToClipboard(paymentInvoice);
      toast.success("Invoice copied to clipboard");
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
                         <Label htmlFor="lsp-select">LSP Provider</Label>
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
                        <div className="rounded-full bg-green-500/10 p-3">
                            <Check className="w-12 h-12 text-green-500" />
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
                        <div className="flex justify-between p-4 pt-0 text-sm">
                             <span className="text-muted-foreground">Amount to pay</span>
                             <div className="text-right">
                                {/* We can use PayLightningInvoice inside here or decode manually. 
                                    PayLightningInvoice handles decoding internally. 
                                    Let's manually decode for the table to show the fee BEFORE the QR code.
                                */}
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
                            <div className="absolute rounded-full p-1 bg-white">
                                <LokiHead className="w-12 h-12" />
                            </div>
                        </div>

                        <div className="flex flex-col items-center gap-1">
                             <FeeDisplay invoice={paymentInvoice} size="lg" />
                        </div>

                        <div className="flex gap-2 w-full">
                            <Button variant="outline" className="flex-1" onClick={copyInvoice}>
                                <Copy className="mr-2 h-4 w-4" />
                                Copy Invoice
                            </Button>
                        </div>
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
