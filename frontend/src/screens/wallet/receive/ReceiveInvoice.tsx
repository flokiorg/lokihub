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
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { InputWithAdornment } from "src/components/ui/custom/input-with-adornment";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
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
  const { data: invoiceData } = useTransaction(
    transaction ? transaction.paymentHash : "",
    true
  );

  React.useEffect(() => {
    if (invoiceData?.settledAt) {
      setPaymentDone(true);
    }
  }, [invoiceData]);
  
  const needsJit = React.useMemo(() => {
    if (!balances || !amount) return false;
    // Check if amount > inbound capacity
    // 0.8 safety factor? Original code used it.
    // (+amount * 1000 || transaction?.amount || 0) >= 0.8 * balances.lightning.totalReceivable
    const amountSat = parseInt(amount) || 0;
    return amountSat * 1000 > balances.lightning.totalReceivable;
  }, [balances, amount]);

  const fetchJitFees = React.useCallback(async () => {
    const firstLSP = info?.lsps?.[0]?.pubkey;
    if (!firstLSP) return;
    
    setJitError(null);
    try {
        const res = await request<LSPS2GetInfoResponse>(`/api/lsps2/info?lspPubkey=${firstLSP}`, {
            method: "GET",
        });
        if (res && res.opening_fee_params_menu && res.opening_fee_params_menu.length > 0) {
            setJitFeeParams(res.opening_fee_params_menu[0]);
        }
    } catch (e: any) {
        console.error("Failed to fetch JIT fees", e);
        setJitError(e.message || "Failed to fetch fee information");
    }
  }, [info]);

  React.useEffect(() => {
    const firstLSP = info?.lsps?.[0]?.pubkey;
    if (needsJit && firstLSP && !jitFeeParams && !jitError) {
        fetchJitFees();
    }
  }, [needsJit, info, jitFeeParams, jitError, fetchJitFees]);

  if (!balances || !info) {
    return <Loading />;
  }

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();

    try {
      setLoading(true);
      let jitSCID = ""; 
      let cltvDelta = 0;

      const firstLSP = info?.lsps?.[0]?.pubkey;
          if (needsJit && jitFeeParams && firstLSP) {
            const amountMloki = (parseInt(amount) || 0) * 1000;
            
            // Check limits
            const minPaymentSize = parseInt(jitFeeParams.min_payment_size_mloki);
            const maxPaymentSize = parseInt(jitFeeParams.max_payment_size_mloki);

            if (amountMloki < minPaymentSize) {
                toast.error(`Amount too small for JIT channel. Minimum: ${minPaymentSize / 1000} Loki.`);
                setLoading(false);
                return;
            }

            if (maxPaymentSize > 0 && amountMloki > maxPaymentSize) {
                 toast.error(`Amount too large for JIT channel. Maximum: ${maxPaymentSize / 1000} Loki.`);
                 setLoading(false);
                 return;
            }

            try {
                toast("Buying inbound liquidity...");
                const buyRes = await request<LSPS2BuyResponse>("/api/lsps2/buy", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                    lspPubkey: firstLSP,
                    paymentSizeMloki: amountMloki,
                    openingFeeParams: jitFeeParams
                } as LSPS2BuyRequest)
            });
            if (buyRes) {
                jitSCID = buyRes.interceptScid;
                cltvDelta = buyRes.cltvExpiryDelta;
            }
        } catch (e) {
            console.error("Failed to buy liquidity", e);
            toast.error("Failed to buy liquidity. Please try again later.");
            return;
        }
      }

      const invoice = await request<Transaction>("/api/invoices", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          amount: (parseInt(amount) || 0) * 1000,
          description,
          lspJitChannelSCID: jitSCID,
          lspCltvExpiryDelta: cltvDelta,
          lspPubkey: jitSCID ? info?.lsps?.[0]?.pubkey : undefined,
          lspFeeBaseMloki: jitSCID && jitFeeParams ? parseInt(jitFeeParams.min_fee_mloki) : undefined,
          lspFeeProportionalMillionths: jitSCID && jitFeeParams ? jitFeeParams.proportional : undefined,
        } as CreateInvoiceRequest),
      });

      if (invoice) {
        setTransaction(invoice);
        setAmount("");
        setDescription("");
        toast("Successfully created invoice");
      }
    } catch (e) {
      toast.error("Failed to create invoice", {
        description: "" + e,
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
              0.8 * balances.lightning.totalReceivable && (
              <LowReceivingCapacityAlert jitAvailable={!!info?.lsps?.[0]} />
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
                        <FormattedFiatAmount
                          amount={Math.floor(transaction.amount / 1000)}
                          className="text-xl"
                        />
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
                        <FormattedFiatAmount
                          amount={Math.floor(transaction.amount / 1000)}
                          className="text-xl"
                        />
                      </div>
                    </CardContent>
                    <CardFooter className="flex flex-col gap-2 pt-2">
                      <Button
                        onClick={() => {
                          setPaymentDone(false);
                          setTransaction(null);
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
                {needsJit && info?.lsps?.[0] && (
                  <Alert>
                    <InfoIcon className="h-4 w-4" />
                    <AlertTitle>JIT Channel Required</AlertTitle>
                    <AlertDescription className="flex flex-col gap-2">
                      <p>
                      Inbound capacity is low. 
                      </p>
                      {jitError ? (
                        <div className="text-destructive flex items-center justify-between gap-2">
                            <span>{jitError}</span>
                            <Button variant="outline" size="sm" onClick={fetchJitFees}>Retry</Button>
                        </div>
                      ) : jitFeeParams ? (
                         <>
                            <p>
                              Your LSP (<span className="text-primary">{info.lsps[0].name || `${info.lsps[0].pubkey.slice(0, 6)}...${info.lsps[0].pubkey.slice(-6)}@${info.lsps[0].host}`}</span>) will provide liquidity.
                            </p>
                            <p>
                              Estimated Fee: {Math.ceil((parseInt(jitFeeParams.min_fee_mloki) + (parseInt(amount)*1000 * jitFeeParams.proportional / 1000000)) / 1000)} loki.
                            </p>
                         </>
                      ) : (
                         " Fetching fee information..."
                      )}
                    </AlertDescription>
                  </Alert>
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

