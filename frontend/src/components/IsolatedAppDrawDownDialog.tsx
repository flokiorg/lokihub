import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
    DialogTrigger,
} from "src/components/ui/dialog";
import { Label } from "src/components/ui/label";
import { CurrencyInput } from "src/components/CurrencyInput";
import { useApp } from "src/hooks/useApp";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";
import { useSWRConfig } from "swr";

type IsolatedAppTopupProps = {
  appId: number;
};

export function IsolatedAppDrawDownDialog({
  appId,
  children,
}: React.PropsWithChildren<IsolatedAppTopupProps>) {
  const { t } = useTranslation("wallet");
  const { mutate: reloadApp } = useApp(appId);
  const { mutate } = useSWRConfig();
  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [amountDisplay, setAmountDisplay] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [open, setOpen] = React.useState(false);

  const [inputUnit, setInputUnit] = useInputUnit(undefined);

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

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    try {
      const amountLoki = parseInputAmount(parseFloat(amountDisplay), inputUnit);
      await request(`/api/transfers`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          fromAppId: appId,
          amountLoki: amountLoki,
        }),
      });
      await reloadApp();
      // Invalidate global caches to update other components
      await mutate(
        (key) => typeof key === "string" && (key.startsWith("/api/balances") || key.startsWith("/api/transactions")),
        undefined,
        { revalidate: true }
      );
      toast(t("subwallets.decreaseBalance.successToast", { amount: amountDisplay, unit: inputUnit }));
      reset();
    } catch (error) {
      handleRequestError("Failed to decrease sub-wallet balance", error);
    }
    setLoading(false);
  }

  function reset() {
    setOpen(false);
    setAmountDisplay("");
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{children}</DialogTrigger>
      <DialogContent>
        <form onSubmit={onSubmit}>
          <DialogHeader>
            <DialogTitle>{t("subwallets.decreaseBalance.title")}</DialogTitle>
            <DialogDescription>
              {t("subwallets.decreaseBalance.description")}
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2 mt-5">
            <Label htmlFor="amount">{t("subwallets.decreaseBalance.amount")}</Label>
            <CurrencyInput
              autoFocus
              id="amount"
              amount={amountDisplay}
              onAmountChange={(val) => setAmountDisplay(val)}
              inputUnit={inputUnit}
              onInputUnitChange={handleInputUnitChange}
              required
              min={scaleInputAmount(1, inputUnit)}
            />
          </div>
          <DialogFooter className="mt-5">
            <LoadingButton loading={loading}>{t("subwallets.decreaseBalance.decreaseButton")}</LoadingButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
