import { Invoice, getFiatValue } from "@lightz/lightning-tools";
import { CopyIcon } from "lucide-react";
import React from "react";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Button } from "src/components/ui/button";
import { copyToClipboard } from "src/lib/clipboard";

type PayLightningInvoiceProps = {
  invoice: string;
};

export function PayLightningInvoice({ invoice }: PayLightningInvoiceProps) {
  const amount = new Invoice({
    pr: invoice,
  }).satoshi;
  const [fiatAmount, setFiatAmount] = React.useState(0);
  React.useEffect(() => {
    getFiatValue({ satoshi: amount, currency: "USD" }).then((fiatAmount) =>
      setFiatAmount(fiatAmount)
    );
  }, [amount]);
  const copy = () => {
    copyToClipboard(invoice);
  };

  return (
    <div className="w-96 flex flex-col gap-6 p-6 items-center justify-center">
      <div className="flex items-center justify-center gap-2 text-muted-foreground">
        <Loading variant="loader" />
        <p>Waiting for lightning payment...</p>
      </div>
      <div className="w-full relative flex items-center justify-center">
        <QRCode value={invoice} className="w-full" />
      </div>
      <div>
        <p className="text-lg font-semibold">
          <FormattedFlokicoinAmount amount={amount * 1000} />
        </p>
        <p className="flex flex-col items-center justify-center">
          {new Intl.NumberFormat("en-US", {
            currency: "USD",
            style: "currency",
          }).format(fiatAmount)}
        </p>
      </div>
      <div className="flex gap-4 w-full">
        <Button
          onClick={copy}
          variant="outline"
          className="flex-1 flex gap-2 items-center justify-center"
        >
          <CopyIcon />
          Copy Invoice
        </Button>
      </div>
    </div>
  );
}
