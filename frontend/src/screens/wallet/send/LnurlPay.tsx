import { Invoice } from "@lightz/lightning-tools";
import type { LightningAddress } from "@lightz/lightning-tools/lnurl";
import { XIcon } from "lucide-react";
import React from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { PaymentFailedAlert } from "src/components/PaymentFailedAlert";
import { PendingPaymentAlert } from "src/components/PendingPaymentAlert";
import { SpendingAlert } from "src/components/SpendingAlert";
import { CurrencyInput } from "src/components/CurrencyInput";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { useBalances } from "src/hooks/useBalances";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { PayInvoiceResponse, TransactionMetadata } from "src/types";
import { request } from "src/utils/request";

export default function LnurlPay() {
  const { state } = useLocation();
  const navigate = useNavigate();
  const { data: balances } = useBalances();
  const { scaleInputAmount, parseInputAmount } = useUnit();

  const lnAddress = state?.args?.lnAddress as LightningAddress;
  const identifier = lnAddress.lnurlpData?.identifier;
  const [amountDisplay, setAmountDisplay] = React.useState("");
  const [comment, setComment] = React.useState("");
  const [isLoading, setLoading] = React.useState(false);
  const [invoice, setInvoice] = React.useState<Invoice>();
  const [errorMessage, setErrorMessage] = React.useState("");

  const [inputUnit, setInputUnit] = useInputUnit(balances?.lightning.totalSpendable);

  const handleInputUnitChange = (newUnit: "FLC" | "loki") => {
    if (amountDisplay) {
      const amountLoki = parseInputAmount(parseFloat(amountDisplay), inputUnit);
      if (!isNaN(amountLoki)) {
        const newAmount = scaleInputAmount(amountLoki, newUnit);
        setAmountDisplay(newAmount.toString());
      }
    }
    setInputUnit(newUnit);
  };

  const onSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setErrorMessage("");
    try {
      if (!lnAddress) {
        throw new Error("no lightning address set");
      }
      setLoading(true);
      const amountLoki = parseInputAmount(parseFloat(amountDisplay), inputUnit);
      const invoice = await lnAddress.requestInvoice({
        satoshi: amountLoki,
        comment,
      });
      setInvoice(invoice);
      const metadata: TransactionMetadata = {
        ...(comment && { comment }),
        ...(identifier && { recipient_data: { identifier } }),
      };
      const payInvoiceResponse = await request<PayInvoiceResponse>(
        `/api/payments/${invoice.paymentRequest}`,
        {
          method: "POST",
          body: JSON.stringify({
            metadata,
          }),
          headers: {
            "Content-Type": "application/json",
          },
        }
      );
      if (!payInvoiceResponse?.preimage) {
        throw new Error("No preimage in response");
      }
      navigate(`/wallet/send/success`, {
        state: {
          preimage: payInvoiceResponse.preimage,
          invoice,
          to: lnAddress.address,
          pageTitle: "Send to Lightning Address",
        },
      });
      toast("Successfully paid invoice");
    } catch (e) {
      console.error(e);
      setErrorMessage("" + e);
      toast.error("Failed to send payment", {
        description: "" + e,
      });
    } finally {
      setLoading(false);
    }
  };

  React.useEffect(() => {
    if (!lnAddress) {
      navigate("/wallet/send");
    }
  }, [navigate, lnAddress]);

  if (!balances || !lnAddress) {
    return <Loading />;
  }

  return (
    <div className="grid gap-4">
      <AppHeader title="Send to Lightning Address" />
      <div className="max-w-lg grid gap-4">
        <PendingPaymentAlert />
        {errorMessage && invoice && (
          <PaymentFailedAlert
            errorMessage={errorMessage}
            invoice={invoice.paymentRequest}
          />
        )}
      </div>
      <form onSubmit={onSubmit} className="grid gap-6 max-w-lg">
        <div className="grid gap-2">
          <div className="text-sm font-medium">Recipient</div>
          <div className="flex items-center justify-between">
            <p className="text-sm">{lnAddress.address}</p>
            <Link to="/wallet/send">
              <XIcon className="w-4 h-4 cursor-pointer text-muted-foreground" />
            </Link>
          </div>
        </div>
        {lnAddress.lnurlpData?.description && (
          <div className="grid gap-2">
            <Label>Description</Label>
            <p className="text-muted-foreground text-sm">
              {lnAddress.lnurlpData.description}
            </p>
          </div>
        )}
        <div className="grid gap-2">
          <Label htmlFor="amount">Amount</Label>
          <CurrencyInput
            id="amount"
            amount={amountDisplay}
            onAmountChange={(val) => setAmountDisplay(val)}
            inputUnit={inputUnit}
            onInputUnitChange={handleInputUnitChange}
            min={scaleInputAmount(1, inputUnit)}
            required
            autoFocus
          />
          <div className="grid gap-2">
            <div className="flex justify-between text-xs text-muted-foreground sensitive slashed-zero">
              <div>
                Spending Balance:{" "}
                <FormattedFlokicoinAmount
                  amount={balances.lightning.totalSpendable}
                />
              </div>
              <FormattedFiatAmount
                className="text-xs"
                amount={Math.floor(balances.lightning.totalSpendable / 1000)}
              />
            </div>
          </div>
        </div>
        {!!lnAddress.lnurlpData?.commentAllowed && (
          <div className="grid gap-2">
            <Label htmlFor="comment">Comment</Label>
            <Input
              id="comment"
              type="text"
              value={comment}
              placeholder="Optional"
              onChange={(e) => {
                setComment(e.target.value);
              }}
            />
          </div>
        )}
        <SpendingAlert amount={parseInputAmount(parseFloat(amountDisplay || "0"), inputUnit)} />
        <div className="flex gap-2">
          <LinkButton to="/wallet/send" variant="outline">
            Back
          </LinkButton>
          <LoadingButton loading={isLoading} type="submit" className="flex-1">
            Send
          </LoadingButton>
        </div>
      </form>
    </div>
  );
}
