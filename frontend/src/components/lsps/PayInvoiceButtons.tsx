import { Copy, Zap } from "lucide-react";
import React from "react";
import { toast } from "sonner";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { copyToClipboard } from "src/lib/clipboard";
import { request } from "src/utils/request";

interface PayInvoiceButtonsProps {
  paymentInvoice: string;
  balances: { lightning: { nextMaxSpendableMPP: number } } | null;
  onPaid: () => void;
}

export function PayInvoiceButtons({ paymentInvoice, balances, onPaid }: PayInvoiceButtonsProps) {
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
