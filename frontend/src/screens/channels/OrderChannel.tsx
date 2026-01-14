import { Check, Copy, InfoIcon, X, Zap } from "lucide-react";
import React, { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "src/components/ui/card";
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

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";

export default function OrderChannel() {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const navigate = useNavigate();
  
  const [selectedLSP, setSelectedLSP] = useState<string>("");
  const { getInfo, createOrder, getOrder, isLoading } = useLSPS1(selectedLSP);
  
  const [options, setOptions] = useState<LSPS1Option[]>([]);
  const [amount, setAmount] = useState<string>("250000"); // Default similar to preset
  const [paymentInvoice, setPaymentInvoice] = useState<string>("");
  const [orderId, setOrderId] = useState<string>("");
  const [orderState, setOrderState] = useState<string>("");
  
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
      if (orderId && selectedLSP) {
          interval = setInterval(async () => {
              const res = await getOrder(orderId);
              if (res) {
                  setOrderState(res.state);
                  if (res.state === "COMPLETED" || res.state === "FAILED") {
                      clearInterval(interval);
                      if (res.state === "COMPLETED") {
                        toast.success("Channel order completed!");
                      } else {
                        toast.error("Channel order failed");
                      }
                  }
              }
          }, 2000);
      }
      return () => clearInterval(interval);
  }, [orderId, selectedLSP, getOrder]);

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
              setOrderState("CREATED");
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
      <AppHeader
        title="Increase Inbound Liquidity"
        description="Order a channel from your LSP to increase your receiving capacity"
      />

        {!paymentInvoice ? (
           <div className="md:max-w-md max-w-full flex flex-col gap-5 flex-1">
                <LightningNetworkDark className="w-full hidden dark:block" />
                <LightningNetworkLight className="w-full dark:hidden" />
                
                <p className="text-muted-foreground">
                  Order a channel from an LSP. This provides inbound liquidity, allowing you to receive payments immediately after the channel is confirmed.
                </p>

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
                </form>
           </div>
        ) : (
            <div className="md:max-w-md w-full">
                <Card>
                   <CardHeader>
                    <CardTitle className="text-center">Pay Invoice</CardTitle>
                  </CardHeader>
                  <CardContent className="flex flex-col items-center gap-6">
                      <QRCode value={paymentInvoice} />
                      <div className="flex flex-col gap-1 items-center w-full">
                          <div className="flex gap-2 w-full max-w-sm">
                              <Input value={paymentInvoice} readOnly className="font-mono text-xs" />
                              <Button size="icon" variant="outline" onClick={copyInvoice}>
                                  <Copy className="h-4 w-4" />
                              </Button>
                          </div>
                      </div>
                      
                      {orderState === "COMPLETED" ? (
                          <Alert className="bg-green-500/15 text-green-600 border-green-500/50">
                              <Check className="h-4 w-4" />
                              <AlertTitle>Success!</AlertTitle>
                              <AlertDescription>Your channel has been ordered and is opening.</AlertDescription>
                          </Alert>
                      ) : (
                           <div className="flex items-center gap-2 text-sm text-muted-foreground animate-pulse">
                               <Loading className="h-4 w-4" />
                               Waiting for payment... ({orderState})
                           </div>
                      )}
                  </CardContent>
                  <CardFooter className="flex flex-col gap-2">
                       {orderState === "COMPLETED" ? (
                            <Button className="w-full" onClick={() => navigate("/channels")}>
                                View Channels
                            </Button>
                       ) : (
                          <div className="flex w-full gap-2">
                              <Button variant="outline" className="w-full" onClick={() => {
                                  setPaymentInvoice("");
                                  setOrderId("");
                              }}>
                                  <X className="mr-2 h-4 w-4" />
                                  Cancel
                              </Button>
                          </div>
                       )}
                  </CardFooter>
                </Card>
            </div>
        )}
    </div>
  );
}
