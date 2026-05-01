import {
  CircleCheckIcon,
  CopyIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
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
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { ExternalLinkButton } from "src/components/ui/custom/external-link-button";
import { LinkButton } from "src/components/ui/custom/link-button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { useInfo } from "src/hooks/useInfo";
import { useMempoolApi } from "src/hooks/useMempoolApi";
import { useOnchainAddress } from "src/hooks/useOnchainAddress";
import { useSyncWallet } from "src/hooks/useSyncWallet";
import { copyToClipboard } from "src/lib/clipboard";
import { MempoolUtxo } from "src/types";

export default function DepositFlokicoin() {
  const { t } = useTranslation("channels");
  const { t: tc } = useTranslation("common");
  useSyncWallet();
  const {
    data: onchainAddress,
    getNewAddress,
    loadingAddress,
  } = useOnchainAddress();
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

    if (txId) {
      const utxo = mempoolAddressUtxos.find((utxo) => utxo.txid === txId);
      if (utxo?.status.confirmed) {
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setConfirmedAmount(utxo.value);
        setPendingAmount(null);
      }
    } else {
      const unconfirmed = mempoolAddressUtxos.find(
        (utxo) => !utxo.status.confirmed
      );
      if (unconfirmed) {
        setTxId(unconfirmed.txid);
        setPendingAmount(unconfirmed.value);
      }
    }
  }, [mempoolAddressUtxos, txId]);

  if (!onchainAddress) {
    return <Loading />;
  }

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("onchain.depositTitle")}
      />
      <MempoolAlert />
      <div className="w-80">
        {confirmedAmount ? (
          <DepositSuccess amount={confirmedAmount} txId={txId} />
        ) : txId ? (
          <DepositPending amount={pendingAmount} txId={txId} />
        ) : (
          <Card>
            <CardContent className="grid gap-6 justify-center">
              <a
                href={`flokicoin:${onchainAddress}`}
                target="_blank"
                className="flex justify-center"
              >
                <QRCode value={onchainAddress} />
              </a>

              <div className="flex flex-wrap gap-2 items-center justify-center">
                <OnchainAddressDisplay address={onchainAddress} />
              </div>

              <div className="flex flex-row gap-4 justify-center">
                <LoadingButton
                  variant="outline"
                  onClick={getNewAddress}
                  className="w-28"
                  loading={loadingAddress}
                >
                  {!loadingAddress && <RefreshCwIcon />}
                  {tc("actions.update")}
                </LoadingButton>
                <Button
                  variant="secondary"
                  className="w-28"
                  onClick={() => {
                    copyToClipboard(onchainAddress);
                  }}
                >
                  <CopyIcon />
                  {tc("actions.copy")}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
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
  const { t } = useTranslation("channels");

  return (
    <Card className="w-full">
      <CardHeader>
        <CardTitle className="text-center">{t("onchain.awaitingConfirm", "Awaiting Confirmation")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col items-center gap-4">
        <LottieLoading size={288} />
        {amount && (
          <div className="flex flex-col gap-2 items-center">
            <p className="text-xl font-semibold slashed-zero">
              <FormattedFlokicoinAmount amount={amount * 1000} />
            </p>
            <FormattedFiatAmount amount={amount} />
          </div>
        )}
        <div>
          <ExternalLinkButton
            to={`${info?.mempoolUrl}/tx/${txId}`}
            variant="outline"
            className="flex items-center mt-2"
          >
            {t("onchain.viewExplorer", "View on Flokicoin Explorer")}
            <ExternalLinkIcon className="size-4 ml-2" />
          </ExternalLinkButton>
        </div>
      </CardContent>
    </Card>
  );
}

function DepositSuccess({ amount, txId }: { amount: number; txId: string }) {
  const { data: info } = useInfo();
  const { t } = useTranslation("channels");

  return (
    <>
      <Card className="w-full">
        <CardHeader>
          <CardTitle className="text-center">{t("onchain.received", "Payment Received!")}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-4">
          <CircleCheckIcon className="w-72 h-72 p-2" />
          <div className="flex flex-col gap-2 items-center">
            <p className="text-xl font-semibold slashed-zero">
              <FormattedFlokicoinAmount amount={amount * 1000} />
            </p>
            <FormattedFiatAmount amount={amount} />
          </div>
          <div>
            <ExternalLinkButton
              to={`${info?.mempoolUrl}/tx/${txId}`}
              variant="outline"
              className="flex items-center mt-2"
            >
              {t("onchain.viewExplorer", "View on Flokicoin Explorer")}
              <ExternalLinkIcon className="size-4 ml-2" />
            </ExternalLinkButton>
          </div>
        </CardContent>
      </Card>
      <LinkButton to="/channels" className="mt-4 w-full">
        {t("onchain.backToNode", "Back To Node")}
      </LinkButton>
    </>
  );
}
