import {
  AlertTriangleIcon,
  ExternalLinkIcon,
  InfoIcon,
  PencilIcon,
  XIcon,
} from "lucide-react";
import React from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { toast } from "sonner";
import { AnchorReserveAlert } from "src/components/AnchorReserveAlert";
import AppHeader from "src/components/AppHeader";
import ExternalLink from "src/components/ExternalLink";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { MempoolAlert } from "src/components/MempoolAlert";
import { CurrencyInput } from "src/components/CurrencyInput";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { ONCHAIN_DUST_LOKI } from "src/constants";
import { useBalances } from "src/hooks/useBalances";
import { useInfo } from "src/hooks/useInfo";
import { useMempoolApi } from "src/hooks/useMempoolApi";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { RedeemOnchainFundsResponse } from "src/types";
import { request } from "src/utils/request";

export default function Onchain() {
  const { state } = useLocation();
  const navigate = useNavigate();
  const [amountDisplay, setAmountDisplay] = React.useState("");

  const address = state?.args?.address as string;

  React.useEffect(() => {
    if (!address) {
      navigate("/wallet/send");
    }
  }, [navigate, address]);

  if (!address) {
    return <Loading />;
  }

  return (
    <div className="grid gap-4">
      <AppHeader title="Send to On-chain" />
      <div className="grid gap-6 md:max-w-lg">
        <MempoolAlert />
        <div className="grid gap-2">
          <div className="text-sm font-medium">Recipient</div>
          <div className="flex items-center justify-between">
            <div className="flex flex-wrap gap-2 items-center font-mono text-sm">
              {address.match(/.{1,4}/g)?.map((word, index) => {
                if (index % 2 === 0) {
                  return (
                    <span key={index} className="text-foreground">
                      {word}
                    </span>
                  );
                } else {
                  return (
                    <span key={index} className="text-muted-foreground">
                      {word}
                    </span>
                  );
                }
              })}
            </div>
            <Link to="/wallet/send">
              <XIcon className="w-4 h-4 cursor-pointer text-muted-foreground" />
            </Link>
          </div>
        </div>
        <OnchainForm
          address={address}
          amount={amountDisplay}
          setAmount={setAmountDisplay}
        />
      </div>
    </div>
  );
}

function OnchainForm({
  address,
  amount: amountDisplay,
  setAmount: setAmountDisplay,
}: {
  address: string;
  amount: string;
  setAmount: React.Dispatch<React.SetStateAction<string>>;
}) {
  const navigate = useNavigate();
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const { unit, scaleInputAmount, parseInputAmount } = useUnit();
  const { data: recommendedFees, error: mempoolError } = useMempoolApi<{
    fastestFee: number;
    halfHourFee: number;
    economyFee: number;
    minimumFee: number;
  }>("/v1/fees/recommended");

  const [inputUnit, setInputUnit] = useInputUnit(balances?.onchain.spendable);

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

  const [feeRate, setFeeRate] = React.useState("");
  const [isLoading, setLoading] = React.useState(false);
  const [editFee, setEditFee] = React.useState(false);

  React.useEffect(() => {
    if (recommendedFees?.fastestFee) {
      setFeeRate(recommendedFees.fastestFee.toString());
    }
  }, [recommendedFees]);

  const onSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      if (!balances) {
        return;
      }
      if (balances.onchain.spendable <= ONCHAIN_DUST_LOKI) {
        throw new Error(
          `You currently don't have enough ${unit()} to pay for an on-chain transaction.`
        );
      }
      setLoading(true);
      const amountLoki = parseInputAmount(parseFloat(amountDisplay), inputUnit);
      const response = await request<RedeemOnchainFundsResponse>(
        "/api/wallet/redeem-onchain-funds",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify({
            toAddress: address,
            amount: amountLoki,
            feeRate: +feeRate,
          }),
        }
      );
      if (!response?.txId) {
        throw new Error("No address in response");
      }
      navigate(`/wallet/send/onchain-success`, {
        state: {
          amount: parseFloat(amountDisplay),
          txId: response.txId,
        },
      });
      toast("Successfully broadcasted transaction");
    } catch (e) {
      toast.error("Failed to send payment", {
        description: "" + e,
      });
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  if (!info || !balances || (!recommendedFees && !mempoolError)) {
    return <Loading />;
  }

  return (
    <form onSubmit={onSubmit} className="grid gap-6">
      <div className="grid gap-2">
        <Label htmlFor="amount">Amount</Label>
        <CurrencyInput
          id="amount"
          amount={amountDisplay}
          onAmountChange={(val) => setAmountDisplay(val)}
          inputUnit={inputUnit}
          onInputUnitChange={handleInputUnitChange}
          min={scaleInputAmount(ONCHAIN_DUST_LOKI, inputUnit)}
          required
          autoFocus
        />
        <div className="flex justify-between text-muted-foreground text-xs sensitive slashed-zero">
          <div>
            On-chain Balance:{" "}
            <FormattedFlokicoinAmount
              amount={balances.onchain.spendable * 1000}
            />
          </div>
          <FormattedFiatAmount
            className="text-xs"
            amount={balances.onchain.spendable}
          />
        </div>
      </div>
      <div className="grid gap-2 text-sm border-t pt-6">
        {!editFee ? (
          <div className="flex items-center justify-between">
            <p className="text-muted-foreground">On-chain Fee Rate</p>
            <div
              className="flex items-center gap-2 cursor-pointer"
              onClick={() => setEditFee(true)}
            >
              {feeRate ? (
                <p>
                  {feeRate} {unit()}/vB
                </p>
              ) : (
                <Loading className="w-4 h-4" />
              )}
              <PencilIcon className="w-4 h-4" />
            </div>
          </div>
        ) : (
          <div className="grid gap-2">
            <Label htmlFor="fee-rate">Fee Rate ({unit()}/vB)</Label>
            {mempoolError && (
              <div className="text-muted-foreground text-xs flex gap-1 items-center">
                <AlertTriangleIcon className="h-3 w-3" />
                Failed to fetch fee estimates. Try refreshing the page.
              </div>
            )}
            <Input
              id="fee-rate"
              type="number"
              value={feeRate}
              step={1}
              required
              min={recommendedFees?.minimumFee || 1}
              onChange={(e) => {
                setFeeRate(e.target.value);
              }}
            />
            {recommendedFees && (
              <div className="flex items-center mt-2 gap-4">
                <Button
                  variant="positive"
                  className="rounded-full"
                  type="button"
                  onClick={() =>
                    setFeeRate(recommendedFees.economyFee.toString())
                  }
                >
                  Low priority: {recommendedFees.economyFee}
                </Button>{" "}
                <Button
                  variant="positive"
                  className="rounded-full"
                  type="button"
                  onClick={() =>
                    setFeeRate(recommendedFees.fastestFee.toString())
                  }
                >
                  High priority: {recommendedFees.fastestFee}
                </Button>{" "}
                <ExternalLink
                  to={info?.mempoolUrl}
                  className="text-muted-foreground underline flex items-center gap-2"
                >
                  View on Flokicoin Explorer
                  <ExternalLinkIcon className="w-4 h-4" />
                </ExternalLink>
              </div>
            )}
          </div>
        )}
      </div>
      {amountDisplay &&
        parseInputAmount(parseFloat(amountDisplay), inputUnit) < 10_000 && (
          <Alert>
            <InfoIcon className="h-4 w-4" />
            <AlertTitle>Amount not ideal for On-chain transaction</AlertTitle>
            <AlertDescription>
              Small amounts can become unspendable when mempool fees increase.
              Consider using Lightning instead or sending at least{" "}
              {scaleInputAmount(10_000, inputUnit)} {inputUnit}.
            </AlertDescription>
          </Alert>
        )}
      <AnchorReserveAlert
        amount={parseInputAmount(parseFloat(amountDisplay || "0"), inputUnit)}
      />
      <div className="flex gap-2">
        <LinkButton to="/wallet/send" variant="outline">
          Back
        </LinkButton>
        <LoadingButton loading={isLoading} type="submit" className="flex-1">
          Send
        </LoadingButton>
      </div>
    </form>
  );
}
