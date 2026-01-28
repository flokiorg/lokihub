import {
  ArrowLeftIcon,
  CopyIcon,
  InfoIcon,
  LinkIcon,
  PlusIcon
} from "lucide-react";

import React from "react";
import { toast } from "sonner";
import Tick from "src/assets/illustrations/tick.svg?react";
import AppHeader from "src/components/AppHeader";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import LowReceivingCapacityAlert from "src/components/LowReceivingCapacityAlert";
import QRCode from "src/components/QRCode";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Checkbox } from "src/components/ui/checkbox";
import { InputWithAdornment } from "src/components/ui/custom/input-with-adornment";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger
} from "src/components/ui/dialog";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { useBalances } from "src/hooks/useBalances";


import { useInfo } from "src/hooks/useInfo";
import { useTransaction } from "src/hooks/useTransaction";
import { copyToClipboard } from "src/lib/clipboard";
import { CreateInvoiceRequest, LSPS2BuyRequest, LSPS2BuyResponse, LSPS2GetInfoResponse, LSPS2OpeningFeeParams, Transaction } from "src/types";

import { request } from "src/utils/request";

export default function ReceiveInvoice() {
  const { data: info, hasChannelManagement } = useInfo();
  const { data: balances } = useBalances();

  const [isLoading, setLoading] = React.useState(false);
  const [amount, setAmount] = React.useState<string>("");
  const [description, setDescription] = React.useState<string>("");
  const [transaction, setTransaction] = React.useState<Transaction | null>(
    null
  );
  const [paymentDone, setPaymentDone] = React.useState(false);
  const [jitFeeParams, setJitFeeParams] = React.useState<LSPS2OpeningFeeParams | null>(null);
  const [jitError, setJitError] = React.useState<string | null>(null);
  const [jitApplied, setJitApplied] = React.useState(false);
  const [selectedLspPubkey, setSelectedLspPubkey] = React.useState<string>("");
  const [senderPaysFee, setSenderPaysFee] = React.useState(true);
  const { data: invoiceData } = useTransaction(
    transaction ? transaction.paymentHash : "",
    true
  );



  React.useEffect(() => {
    if (invoiceData?.settledAt) {
      setPaymentDone(true);
    }
  }, [invoiceData]);
  
  // Initialize selectedLspPubkey with the first available LSP
  React.useEffect(() => {
    if (info?.lsps?.length && !selectedLspPubkey) {
      setSelectedLspPubkey(info.lsps[0].pubkey);
    }
  }, [info, selectedLspPubkey]);

  const needsJit = React.useMemo(() => {
    if (!balances || !amount) return false;
    // Check if amount > inbound capacity
    // 0.8 safety factor? Original code used it.
    // (+amount * 1000 || transaction?.amount || 0) >= 0.8 * balances.lightning.totalReceivable
    const amountSat = parseInt(amount) || 0;
    // Note: This check relies on the raw input amount to determine if JIT is needed. 
    // In "Sender Pays" mode, the actual incoming amount might be higher, making JIT even more likely.
    return amountSat * 1000 > balances.lightning.totalReceivable;
  }, [balances, amount]);

  const fetchJitFees = React.useCallback(async () => {
    if (!selectedLspPubkey) return;
    
    setJitError(null);
    try {
        const res = await request<LSPS2GetInfoResponse>(`/api/lsps2/info?lspPubkey=${selectedLspPubkey}`, {
            method: "GET",
        });
        if (res && res.opening_fee_params_menu && res.opening_fee_params_menu.length > 0) {
            setJitFeeParams(res.opening_fee_params_menu[0]);
        }
    } catch (e: any) {
        console.error("Failed to fetch JIT fees", e);
        setJitError(e.message || "Failed to fetch fee information");
    }
  }, [selectedLspPubkey]);

  React.useEffect(() => {
    // Only fetch if we need JIT and haven't fetched for the selected LSP yet (or if info changed)
    // We re-fetch if selectedLspPubkey changes.
    if (needsJit && selectedLspPubkey && (!jitFeeParams || jitError)) {
        fetchJitFees();
    }
    // Also re-fetch if selected lsp changes
  }, [needsJit, selectedLspPubkey, jitFeeParams, jitError, fetchJitFees]);

  // Effect to refetch when LSP changes
  React.useEffect(() => {
    if (needsJit && selectedLspPubkey) {
        fetchJitFees();
    }
  }, [selectedLspPubkey, needsJit, fetchJitFees]);

  if (!balances || !info) {
    return <Loading />;
  }

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();

    try {
      setLoading(true);
      let jitSCID = ""; 
      let cltvDelta = 0;
      let jitLSP = "";

      const firstLSP = selectedLspPubkey || info?.lsps?.[0]?.pubkey;
      // Calculate amount in mloki (1 sat = 1000 mloki)
      const inputAmountMloki = (parseInt(amount) || 0) * 1000;
      let invoiceAmountMloki = inputAmountMloki;
      let buyLiquidityAmountMloki = inputAmountMloki;
      let feeMloki = 0;

          if (needsJit && jitFeeParams && firstLSP) {
            
            // Calculate Fees and Amounts based on "Sender Pays" vs "Receiver Pays"
            const minFee = parseInt(jitFeeParams.min_fee_mloki);
            const proportionalPpm = jitFeeParams.proportional;
            
            if (senderPaysFee) {
                // Sender Pays:
                // Input = Net Amount (what user receives).
                // Gross = ??
                // Logic: Gross - Fee(Gross) = Net.
                // Fee = Max(MinFee, Gross * Rate).
                // If MinFee dominates: Gross = Net + MinFee.
                // If Propatioal dominates: Gross = Net / (1 - Rate).
                
                const rate = proportionalPpm / 1000000;
                // Solve for Gross using proportional assumption
                // Use Floor to align with integer math.
                const grossCandidate = Math.floor(inputAmountMloki / (1 - rate));
                const feeCandidate = Math.floor(grossCandidate * rate);
                
                // Actual Fee is Max of MinFee or Proportional
                feeMloki = Math.max(minFee, feeCandidate);
                
                // If MinFee was larger, recalculate Gross
                buyLiquidityAmountMloki = inputAmountMloki + feeMloki;
                invoiceAmountMloki = inputAmountMloki; // We receive Input.
            } else {
                // Receiver Pays (Default):
                // Input = Gross Amount (what sender pays total, roughly).
                // Net = Input - Fee.
                // Logic: Fee = Fee(Input).
                
                const proportionalFee = Math.floor((inputAmountMloki * proportionalPpm) / 1000000);
                feeMloki = Math.max(minFee, proportionalFee);
                
                buyLiquidityAmountMloki = inputAmountMloki;
                invoiceAmountMloki = inputAmountMloki - feeMloki; // We receive Input - Fee.
            }

            // Check limits on the GROSS amount (what goes through the channel)
            const minPaymentSize = parseInt(jitFeeParams.min_payment_size_mloki);
            const maxPaymentSize = parseInt(jitFeeParams.max_payment_size_mloki);

            if (buyLiquidityAmountMloki < minPaymentSize) {
                toast.error(`Amount too small for JIT payment. Minimum: ${minPaymentSize / 1000} Loki.`);
                setLoading(false);
                return;
            }

            if (maxPaymentSize > 0 && buyLiquidityAmountMloki > maxPaymentSize) {
                 toast.error(`Amount too large for JIT payment. Maximum: ${maxPaymentSize / 1000} Loki.`);
                 setLoading(false);
                 return;
            }

            // Calculate Net Amount for Invoice
            // The invoice monitors the amount RECEIVED.
            // LSP receives Gross -> Deducts Fee -> Fowards Net.
            // So Invoice MUST expect Net.


            try {
                toast("Buying inbound liquidity...");
                const buyRes = await request<LSPS2BuyResponse>("/api/lsps2/buy", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                    lspPubkey: firstLSP,
                    paymentSizeMloki: buyLiquidityAmountMloki, // Buy liquidity for GROSS amount
                    openingFeeParams: jitFeeParams
                } as LSPS2BuyRequest)
            });
                if (buyRes) {
                 jitSCID = buyRes.interceptScid;
                 cltvDelta = buyRes.cltvExpiryDelta;
                 jitLSP = buyRes.lspNodeID;
                }
            } catch (e: any) {
                console.error("Failed to buy liquidity", e);
                toast.error("Failed to buy liquidity", {
                    description: e.message || "Unknown error"
                });
                return;
            }
          }

      const invoice = await request<Transaction>("/api/invoices", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          amount: invoiceAmountMloki, // Invoice uses NET amount
          description,
          lspJitChannelSCID: jitSCID,
          lspCltvExpiryDelta: cltvDelta || 144,
          lspPubkey: jitSCID ? (jitLSP || info?.lsps?.[0]?.pubkey) : undefined,
          lspFeeBaseMloki: jitSCID ? feeMloki : undefined, // Base Fee = Calculated Fee
          lspFeeProportionalMillionths: jitSCID ? 0 : undefined, // Proportional Fee = 0 (All fees in base)
        } as CreateInvoiceRequest),
      });

      if (invoice) {
        setTransaction(invoice);
        // If we got a JIT SCID, we consider JIT applied
        if (jitSCID) {
           setJitApplied(true);
        }
        setAmount("");
        setDescription("");
        toast("Successfully created invoice");
      }
    } catch (e: any) {
      toast.error("Failed to create invoice", {
        description: e.message || "Unknown error",
      });
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const copy = () => {
    copyToClipboard(transaction?.invoice as string);
  };



  return (
    <div className="grid gap-5">
      <AppHeader title={transaction ? "Lightning Invoice" : "Create Invoice"} />
      <div className="flex flex-col md:flex-row gap-12">
        <div className="w-full md:max-w-lg grid gap-6">
          {hasChannelManagement &&
            (+amount * 1000 || transaction?.amount || 0) >=
              0.8 * balances.lightning.totalReceivable && !jitApplied && (
              <LowReceivingCapacityAlert jitAvailable={!!info?.lsps?.length} />
            )}
          <div>
            {transaction ? (
              <Card>
                {!paymentDone ? (
                  <>
                    <CardHeader>
                      <CardTitle className="flex justify-center">
                        <Loading className="size-4 mr-2" />
                        <p>Waiting for payment</p>
                      </CardTitle>
                    </CardHeader>
                    <CardContent className="flex flex-col items-center gap-6">
                      <QRCode value={transaction.invoice} />
                      <div className="flex flex-col gap-1 items-center">
                        <p className="text-2xl font-medium slashed-zero">
                          <FormattedFlokicoinAmount amount={transaction.amount} />
                        </p>
                        <div className="flex flex-col items-center">
                            <FormattedFiatAmount
                              amount={Math.floor(transaction.amount / 1000)}
                              className="text-xl"
                            />

                        </div>
                      </div>
                    </CardContent>
                    <CardFooter className="flex flex-col gap-2">
                      <Button
                        className="w-full"
                        onClick={copy}
                        variant="outline"
                      >
                        <CopyIcon className="w-4 h-4 mr-2" />
                        Copy Invoice
                      </Button>
                    </CardFooter>
                  </>
                ) : (
                  <>
                    <CardHeader>
                      <CardTitle className="text-center">
                        Payment Received
                      </CardTitle>
                    </CardHeader>
                    <CardContent className="flex flex-col items-center gap-6">
                      <Tick className="w-48" />
                      <div className="flex flex-col gap-1 items-center">
                        <p className="text-2xl font-medium slashed-zero">
                          <FormattedFlokicoinAmount amount={transaction.amount} />
                        </p>
                        <div className="flex flex-col items-center">
                            <FormattedFiatAmount
                              amount={Math.floor(transaction.amount / 1000)}
                              className="text-xl"
                            />

                        </div>
                      </div>
                    </CardContent>
                    <CardFooter className="flex flex-col gap-2 pt-2">
                      <Button
                        onClick={() => {
                          setPaymentDone(false);
                          setTransaction(null);
                          setJitApplied(false);
                        }}
                        variant="outline"
                        className="w-full"
                      >
                        <PlusIcon className="w-4 h-4 mr-2" />
                        Create Another Invoice
                      </Button>
                      <LinkButton
                        to="/wallet"
                        variant="link"
                        className="w-full"
                      >
                        <ArrowLeftIcon className="w-4 h-4 mr-2" />
                        Back to Wallet
                      </LinkButton>
                    </CardFooter>
                  </>
                )}
              </Card>
            ) : (
              <form onSubmit={handleSubmit} className="grid gap-6">
                <div className="grid gap-2">
                  <Label htmlFor="amount">Amount (Loki)</Label>
                  <InputWithAdornment
                    id="amount"
                    type="number"
                    value={amount?.toString()}
                    placeholder="Amount in Loki..."
                    onChange={(e) => {
                      setAmount(e.target.value.trim());
                    }}
                    min={1}
                    autoFocus
                    endAdornment={
                      <FormattedFiatAmount amount={+amount} className="mr-2" />
                    }
                  />
                </div>
                {needsJit && info?.lsps && info.lsps.length > 0 && (
                  <div className="rounded-lg border bg-muted/40 p-3 grid gap-3">
                     <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                             <span className="text-sm font-medium">JIT Payment</span>
                             <Dialog>
                                <DialogTrigger asChild>
                                    <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground">
                                        <InfoIcon className="h-4 w-4" />
                                    </Button>
                                </DialogTrigger>
                                <DialogContent className="max-w-lg">
                                    <DialogHeader>
                                        <DialogTitle>JIT Payment Explained</DialogTitle>
                                    </DialogHeader>
                                    
                                    <div className="grid gap-4 py-2">
                                        <div className="space-y-3">
                                            <div>
                                                <h4 className="font-semibold text-sm mb-1">Why is this needed?</h4>
                                                <p className="text-sm text-muted-foreground leading-relaxed">
                                                    Lightning payments require <strong>inbound liquidity</strong> (receiving capacity). 
                                                    You currently don't have enough capacity to receive this amount directly.
                                                </p>
                                            </div>
                                            
                                            <div>
                                                <h4 className="font-semibold text-sm mb-1">How it works</h4>
                                                <p className="text-sm text-muted-foreground leading-relaxed">
                                                    Your LSP will automatically open a new channel <strong>Just-In-Time (JIT)</strong> when the payment arrives. 
                                                    This ensures your payment succeeds immediately without you needing to manually manage channels.
                                                </p>
                                            </div>
                                        </div>

                                        {jitFeeParams && (
                                            <>
                                                <div className="border-t pt-4">
                                                    <h4 className="font-semibold text-sm mb-2">Fee Structure</h4>
                                                    <div className="bg-muted/50 p-3 rounded-md grid grid-cols-2 gap-y-2 text-sm">
                                                        <span className="text-muted-foreground">Minimum Fee:</span>
                                                        <span className="font-medium text-right">{jitFeeParams.min_fee_mloki ? parseInt(jitFeeParams.min_fee_mloki)/1000 : 0} Loki</span>
                                                        
                                                        <span className="text-muted-foreground">Proportional Rate:</span>
                                                        <span className="font-medium text-right">{(jitFeeParams.proportional / 10000).toFixed(2)}% ({jitFeeParams.proportional} ppm)</span>
                                                        
                                                        <div className="col-span-2 text-xs text-muted-foreground mt-2 border-t pt-2">
                                                            The fee is the <strong>higher</strong> of the Minimum Fee or the calculated Proportional Fee.
                                                        </div>
                                                    </div>
                                                </div>

                                                <div className="border-t pt-4">
                                                    <h4 className="font-semibold text-sm mb-2">Who pays the fee?</h4>
                                                    <div className="space-y-3">
                                                        <div className="grid grid-cols-[120px_1fr] gap-2 items-start">
                                                            <span className="text-sm font-medium">Receiver Pays:</span>
                                                            <p className="text-sm text-muted-foreground">
                                                                (Default) The fee is deducted from the amount you receive.
                                                            </p>
                                                        </div>
                                                        <div className="grid grid-cols-[120px_1fr] gap-2 items-start">
                                                            <span className="text-sm font-medium">Sender Pays:</span>
                                                            <p className="text-sm text-muted-foreground">
                                                                The fee is added to the invoice total. The sender pays the extra cost.
                                                            </p>
                                                        </div>
                                                    </div>
                                                </div>
                                            </>
                                        )}
                                    </div>
                                </DialogContent>
                             </Dialog>
                        </div>
                        {jitFeeParams && (
                            <div className="text-right text-sm">
                                <div className="font-medium">
                                    Fee: <FormattedFlokicoinAmount amount={(() => {
                                      const inputAmt = (parseInt(amount)||0)*1000;
                                      const minFee = parseInt(jitFeeParams.min_fee_mloki);
                                      const rate = jitFeeParams.proportional / 1000000;
                                      let finalFee = 0;
                                      
                                      if (senderPaysFee) {
                                          const gross = Math.floor(inputAmt / (1 - rate));
                                          const feeCand = Math.floor(gross * rate);
                                          finalFee = Math.max(minFee, feeCand);
                                      } else {
                                          const prop = Math.floor(inputAmt * rate);
                                          finalFee = Math.max(minFee, prop);
                                      }
                                      return Math.round(finalFee / 1000) * 1000;
                                  })()} />
                                </div>
                                {senderPaysFee ? (
                                    <div className="text-muted-foreground text-xs">
                                         Sender pays: <FormattedFlokicoinAmount amount={(() => {
                                          const inputAmt = (parseInt(amount)||0)*1000;
                                          const minFee = parseInt(jitFeeParams.min_fee_mloki);
                                          const rate = jitFeeParams.proportional / 1000000;
                                          const gross = Math.floor(inputAmt / (1 - rate));
                                          const fee = Math.max(minFee, Math.floor(gross * rate));
                                          const total = inputAmt + fee;
                                          return Math.round(total / 1000) * 1000;
                                         })()} />
                                    </div>
                                ) : (
                                    <div className="text-muted-foreground text-xs">
                                         You receive: <FormattedFlokicoinAmount amount={(() => {
                                          const inputAmt = (parseInt(amount)||0)*1000;
                                          const minFee = parseInt(jitFeeParams.min_fee_mloki);
                                          const rate = jitFeeParams.proportional / 1000000;
                                          const prop = Math.floor(inputAmt * rate);
                                          const fee = Math.max(minFee, prop);
                                          const receive = inputAmt - fee;
                                          return Math.max(0, Math.round(receive / 1000) * 1000);
                                         })()} />
                                    </div>
                                )}
                            </div>
                        )}
                     </div>

                     <div className="flex flex-col sm:flex-row gap-4 sm:items-end sm:justify-between">
                        <div className="grid gap-1.5 flex-1 min-w-[200px]">
                            <Label className="text-xs font-medium text-muted-foreground">Liquidity Provider</Label>
                            <Select 
                                value={selectedLspPubkey} 
                                onValueChange={(val) => {
                                    setSelectedLspPubkey(val);
                                    setJitFeeParams(null); // Reset params to force refetch
                                }}
                            >
                                <SelectTrigger className="bg-background">
                                    <SelectValue placeholder="Select LSP" />
                                </SelectTrigger>
                                <SelectContent>
                                    {info.lsps.map((lsp) => (
                                        <SelectItem key={lsp.pubkey} value={lsp.pubkey}>
                                            {lsp.name || `${lsp.pubkey.slice(0, 8)}...`}
                                        </SelectItem>
                                    ))}
                                </SelectContent>
                            </Select>
                       </div>

                       {jitError ? (
                            <div className="text-destructive text-sm flex items-center justify-between gap-2 pb-2">
                                <span>{jitError}</span>
                                <Button variant="outline" size="sm" onClick={fetchJitFees}>Retry</Button>
                            </div>
                       ) : (
                             <div className="flex items-center space-x-2 pb-2.5">
                                <Checkbox 
                                    id="senderPays" 
                                    checked={senderPaysFee}
                                    onCheckedChange={(checked) => setSenderPaysFee(checked as boolean)}
                                />
                                <label
                                    htmlFor="senderPays"
                                        className="text-sm font-normal leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70 cursor-pointer"
                                >
                                    Include fee in invoice (Sender pays)
                                </label>
                            </div>
                       )}
                    </div>
                  </div>
                )}
                <div className="grid gap-2">
                  <Label htmlFor="description">Description</Label>
                  <Input
                    id="description"
                    type="text"
                    value={description}
                    placeholder="For e.g. who is sending this payment?"
                    onChange={(e) => setDescription(e.target.value)}
                  />
                </div>
                <LoadingButton
                  className="w-full md:w-fit"
                  loading={isLoading}
                  type="submit"
                  disabled={!amount}
                >
                  Create Invoice
                </LoadingButton>
                <div className="grid gap-2 border-t pt-6">

                    <LinkButton
                      to="/wallet/receive/onchain"
                      variant="outline"
                      className="w-full"
                    >
                      <LinkIcon className="h-4 w-4" />
                      Receive from On-chain
                    </LinkButton>
                </div>
              </form>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

