import {
    ArrowLeftIcon,
    CopyIcon,
    ExternalLinkIcon,
    HandCoinsIcon,
    RefreshCwIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import Tick from "src/assets/illustrations/tick.svg?react";
import AppHeader from "src/components/AppHeader";
// ... (omitting lines to shorten context if possible, but for replace_file_content I need exact match or chunk)
// Wait, I should do two chunks if they are far apart.
// StartLine: 8 for import
// StartLine: 185 for usage.
// I will use multi_replace.

import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import LottieLoading from "src/components/LottieLoading";
import { MempoolAlert } from "src/components/MempoolAlert";
import OnchainAddressDisplay from "src/components/OnchainAddressDisplay";
import QRCode from "src/components/QRCode";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardFooter,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { ExternalLinkButton } from "src/components/ui/custom/external-link-button";
import { LinkButton } from "src/components/ui/custom/link-button";
import { useInfo } from "src/hooks/useInfo";
import { useMempoolApi } from "src/hooks/useMempoolApi";
import { useOnchainAddress } from "src/hooks/useOnchainAddress";
import { copyToClipboard } from "src/lib/clipboard";
import { MempoolUtxo } from "src/types";

export default function ReceiveOnchain() {
  return (
    <div className="grid gap-5">
      <AppHeader title="Receive On-chain" />
      <div className="w-full max-w-lg grid gap-5">
        <MempoolAlert />
        <ReceiveToOnchain />
      </div>
    </div>
  );
}

function ReceiveToOnchain() {
  const { data: onchainAddress, getNewAddress } = useOnchainAddress();
  const { data: mempoolAddressUtxos } = useMempoolApi<MempoolUtxo[]>(
    onchainAddress ? `/address/${onchainAddress}/utxo` : undefined,
    3000
  );

  const [txId, setTxId] = useState("");
  const [confirmedAmount, setConfirmedAmount] = useState<number | null>(null);
  const [pendingAmount, setPendingAmount] = useState<number | null>(null);

  useEffect(() => {
    if (!mempoolAddressUtxos || mempoolAddressUtxos.length === 0) {
      return;
    }

    // Always prefer confirmed transactions if available, otherwise take the first one
    const utxo =
        mempoolAddressUtxos.find((utxo) => utxo.status.confirmed) ||
        mempoolAddressUtxos[0];

    if (utxo) {
      setTxId(utxo.txid);
      if (utxo.status.confirmed) {
        setConfirmedAmount(utxo.value);
        setPendingAmount(null);
      } else {
        setPendingAmount(utxo.value);
      }
    }
  }, [mempoolAddressUtxos]);

  if (!onchainAddress) {
    return <Loading />;
  }

  return (
    <>
      {confirmedAmount ? (
        <DepositSuccess amount={confirmedAmount} txId={txId} />
      ) : txId ? (
        <DepositPending amount={pendingAmount} txId={txId} />
      ) : (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center justify-center gap-2">
              <Loading className="w-4 h-4" />
              Waiting for Payment...
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col items-center gap-6">
            <a
              href={`flokicoin:${onchainAddress}`}
              target="_blank"
              className="flex justify-center"
            >
              <QRCode value={onchainAddress} />
            </a>
            <div className="flex flex-wrap max-w-64 gap-2 items-center justify-center">
              <OnchainAddressDisplay address={onchainAddress} />
            </div>
          </CardContent>
          <CardFooter className="flex flex-col gap-2 pt-2">
            <Button
              className="w-full"
              onClick={() => {
                copyToClipboard(onchainAddress);
              }}
              variant="secondary"
            >
              <CopyIcon className="w-4 h-4 mr-2" />
              Copy Address
            </Button>
            <Button
              className="w-full"
              variant="outline"
              onClick={getNewAddress}
            >
              <RefreshCwIcon className="h-4 w-4 shrink-0 mr-2" />
              New Address
            </Button>
          </CardFooter>
        </Card>
      )}
    </>
  );
}

function DepositPending({
  amount,
  txId,
}: {
  amount: number | null;
  txId: string;
}) {
  const { data: info } = useInfo();

  return (
    <Card className="w-full">
      <CardHeader>
        <CardTitle className="flex items-center justify-center gap-2">
          <Loading className="w-4 h-4" />
          Waiting for On-chain Confirmation...
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col items-center gap-4">
        <LottieLoading size={288} />
        {amount && (
          <div className="flex flex-col gap-1 items-center">
            <p className="text-2xl font-medium slashed-zero">
              <FormattedFlokicoinAmount amount={amount * 1000} />
            </p>
            <FormattedFiatAmount amount={amount} className="text-xl" />
          </div>
        )}
      </CardContent>
      <CardFooter className="flex flex-col gap-2 pt-2">
        <ExternalLinkButton
          to={`${info?.mempoolUrl}/tx/${txId}`}
          variant="outline"
          className="w-full"
        >
          <ExternalLinkIcon className="w-4 h-4 mr-2" />
          View on Flokicoin Explorer 
        </ExternalLinkButton>
      </CardFooter>
    </Card>
  );
}

function DepositSuccess({ amount, txId }: { amount: number; txId: string }) {
  const { data: info } = useInfo();

  return (
    <Card className="w-full">
      <CardHeader>
        <CardTitle className="text-center">Transaction Received!</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col items-center gap-6">
        <Tick className="w-48" />
        <div className="flex flex-col gap-1 items-center">
          <p className="text-2xl font-medium slashed-zero">
            <FormattedFlokicoinAmount amount={amount * 1000} />
          </p>
          <FormattedFiatAmount amount={amount} className="text-xl" />
        </div>
      </CardContent>
      <CardFooter className="flex flex-col gap-2 pt-2">
        <ExternalLinkButton
          to={`${info?.mempoolUrl}/tx/${txId}`}
          variant="outline"
          className="w-full"
        >
          <ExternalLinkIcon className="w-4 h-4 mr-2" />
          View on Flokicoin Explorer 
        </ExternalLinkButton>
        <LinkButton to="/wallet/receive/onchain" variant="outline" className="w-full">
          <HandCoinsIcon className="w-4 h-4 mr-2" />
          Receive Another Payment
        </LinkButton>
        <LinkButton to="/wallet" variant="link" className="w-full">
          <ArrowLeftIcon className="w-4 h-4 mr-2" />
          Back to Wallet
        </LinkButton>
      </CardFooter>
    </Card>
  );
}
